package ingest

// The step watch and decision follow-through (11-21). Two misses the owner caught and Crius did not: on 11-19/20 the
// Quotes Legion gate began running tests/Quote.Api.Tests whole (40 min) where the grouped runner took 10-15, and a Crius
// DECISION ("raise Legion heavy slots to 2", 11-20 17:55) was never applied. Both are read here, each ingest run (every
// 10 min), from <home>/stepwatch.toml:
//
//	[[log]]                      # a heavy runner's runs.log (JSON lines: at, cmd, cwd, seconds, waitS, code, killed)
//	name = "legion"              # shown in the line
//	path = "~/.crius/state/quotes/build/legion/heavy-runs.log"   # a copy pulled from another machine is fine
//
//	[alias]                      # one step under two commands: a kind that starts with the key counts as the value
//	"bun api-groups.ts run" = "dotnet test tests/Quote.Api.Tests"
//
//	[hosts]                      # a DECISION with this word just before the setting is checked there:
//	legion = "~/.crius/state/quotes/build/legion/L"   # <cmd> "head -c 4096 -- <path>"
//
//	[settings]                   # names a DECISION may use in place of a path
//	"heavy slots" = "~/AppxiteRig/heavy/slots"
//
// No stepwatch.toml: both checks are off. A listed log that cannot be read makes the run partial (loud).

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/BurntSushi/toml"

	"github.com/orzazade/crius/internal/store"
)

const (
	stepFactor   = 1.5     // the last 3 runs' median over this times the earlier median is slower
	stepFloor    = 5 * 60  // and over 5 minutes
	stepGain     = 10 * 60 // and 10 minutes or more longer per run (the Mac log of 11-21 had 26 changes of +3 to +7 min in 2 days)
	stepWindow   = 7 * 24 * time.Hour
	stepBaseMin  = 5              // earlier runs needed for a median (3 let one-off scripts in)
	stepRecent   = 48 * time.Hour // a change or a day older than this is history: not posted (no flood on install)
	waitFloor    = 30 * 60        // a day's wait under 30 min is never a NEED
	decisionWait = 30 * time.Minute
	decisionAge  = 24 * time.Hour // decisions older than this are not checked
	readFails    = 3              // reads failed in a row (one per run) before "not checked" is posted
	hostTimeout  = 20 * time.Second
	settingMax   = 64 // a one-value settings file holds at most this, on one line
)

type stepConf struct {
	Log []struct {
		Name, Path string
	}
	Alias    map[string]string
	Hosts    map[string]string
	Settings map[string]string
}

func loadStepConf(home string) (*stepConf, error) {
	path := filepath.Join(home, "stepwatch.toml")
	var c stepConf
	md, err := toml.DecodeFile(path, &c)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if und := md.Undecoded(); len(und) > 0 {
		return nil, fmt.Errorf("%s: unknown keys %v", path, und)
	}
	for _, l := range c.Log {
		if l.Name == "" || l.Path == "" {
			return nil, fmt.Errorf("%s: every [[log]] needs name and path", path)
		}
	}
	return &c, nil
}

// stepRun is one finished heavy job.
type stepRun struct {
	at        time.Time
	cwd       string
	kind, raw string // raw: the kind before the alias
	seconds   int
	waitS     int
	ok        bool   // code 0 and not killed: only these are step times
	unwatched bool   // its command was cut and no full name is on record: no step
	script    string // the file a cut run is keyed on (heavy.ts key), for the not-watched warning
}

func readRuns(path string, alias map[string]string) ([]stepRun, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	var out []stepRun
	bad := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var j struct {
			At      string  `json:"at"`
			Key     string  `json:"key"`
			Cmd     string  `json:"cmd"`
			Cwd     string  `json:"cwd"`
			Seconds float64 `json:"seconds"`
			WaitS   float64 `json:"waitS"`
			Code    *int    `json:"code"`
			Killed  bool    `json:"killed"`
		}
		if len(strings.TrimSpace(sc.Text())) == 0 {
			continue
		}
		if err := json.Unmarshal(sc.Bytes(), &j); err != nil {
			bad++
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, j.At)
		if err != nil {
			bad++
			continue
		}
		cmd, whole := fullCmd(j.Cmd, j.Key, j.Cwd)
		raw := ""
		if whole {
			raw = stepKind(cmd)
		}
		kind := raw
		// the longest alias key wins, so the pick does not depend on map order
		best := ""
		for k := range alias {
			if (raw == k || strings.HasPrefix(raw, k+" ")) && len(k) > len(best) {
				best = k
			}
		}
		if best != "" {
			kind = alias[best]
		}
		out = append(out, stepRun{at: t, cwd: j.Cwd, kind: kind, raw: raw, seconds: int(j.Seconds),
			waitS: int(j.WaitS), unwatched: !whole, script: j.Key, ok: kind != "" && j.Code != nil && *j.Code == 0 && !j.Killed})
	}
	if err := sc.Err(); err != nil {
		return nil, bad, err
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].at.Before(out[b].at) })
	return out, bad, nil
}

// heavy.ts cuts a record's cmd at this many characters (cmd.slice(0, 300)), so a cmd this long may be cut.
const cmdCut = 300

// fullCmd is the whole command of a record, and false when none is on record. A cut cmd is no step name: different steps
// share its first 300 characters (11-21 03:36, all-suites.ts parts read as one step 3 -> 21 min). heavy.ts's key holds
// the whole words as "<words> in <cwd>" for a command that names no file; for one that does, the key is that file alone
// and its other words are lost, so a cut one of those has no full name.
func fullCmd(cmd, key, cwd string) (string, bool) {
	if words, ok := strings.CutSuffix(key, " in "+cwd); ok && cwd != "" && words != "" {
		return words, true
	}
	return cmd, len(cmd) < cmdCut
}

var (
	reShell   = regexp.MustCompile(`^(?:bash|sh) -c\s+`)
	reTimeout = regexp.MustCompile(`^timeout\s+\S+\s+`)
	rePerl    = regexp.MustCompile(`^perl -e alarm shift; exec @ARGV or die "exec: \$!"\s+\S+\s+`)
	reCd      = regexp.MustCompile(`^cd\s+\S+\s*&&\s*`)
	reExport  = regexp.MustCompile(`^export\s+[^;]*;\s*`)
	reEnv     = regexp.MustCompile(`^env(?:\s+|$)`)
	reAssign  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*=\S*(?:\s+|$)`) // VAR=value: a value may be a credential
	reSha     = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
)

// unwrap takes off the wrappers that run a command: sh -c, timeout, the perl alarm, cd X &&, export X;, env and VAR=v.
func unwrap(s string) string {
	for {
		before := s
		s = strings.TrimSpace(s)
		if m := reShell.FindString(s); m != "" {
			s = strings.Trim(s[len(m):], `"'`)
		}
		for _, re := range []*regexp.Regexp{reTimeout, rePerl, reCd, reExport, reEnv, reAssign} {
			if m := re.FindString(s); m != "" {
				s = s[len(m):]
			}
		}
		if s == before {
			return s
		}
	}
}

// stepKind is a command less what changes from run to run of one step: shell wrappers, cd into a worktree, timeouts,
// variable settings (a value may be a credential), output redirection and pipes, quiet and build flags, shas and the
// folders of absolute paths. A chain (a && b; c) keeps every command but cd, export and echo: a step that adds a command
// is another step. A --filter stays with its expression (unquoted): each test set is its own step, and the whole project
// another (one kind for every filter flagged 7 false changes on the Legion log of 11-21). "" is no step (only settings).
func stepKind(cmd string) string {
	f := strings.Fields(unwrap(cmd))
	var segs [][]string
	cur := []string{}
	for _, w := range f {
		switch {
		case w == "&&" || w == "||" || w == ";":
			segs, cur = append(segs, cur), []string{}
		case strings.HasSuffix(w, ";") && !strings.Contains(w, "="):
			segs, cur = append(segs, append(cur, strings.TrimSuffix(w, ";"))), []string{}
		default:
			cur = append(cur, w)
		}
	}
	segs = append(segs, cur)
	var out []string
	for _, seg := range segs {
		k := normCmd(strings.Fields(unwrap(strings.Join(seg, " "))))
		first, _, _ := strings.Cut(k, " ")
		switch first {
		case "", "cd", "export", "echo", "true", ":":
			continue
		}
		out = append(out, k)
	}
	return strings.Join(out, " && ")
}

// normCmd is one command of a chain, normalized (see stepKind).
func normCmd(f []string) string {
	var out []string
	for i := 0; i < len(f); i++ {
		w := f[i]
		switch {
		case w == "|" || w == "|&":
			i = len(f) // the rest of this command is what its output goes through
			continue
		case w == ">" || w == ">>" || w == "2>" || w == "2>>" || w == "&>" || w == "1>" || w == "<":
			i++ // and its file
			continue
		case strings.HasPrefix(w, ">") || strings.HasPrefix(w, "2>") || strings.HasPrefix(w, "&>") || strings.HasPrefix(w, "1>") ||
			strings.HasPrefix(w, "<"):
			continue
		case w == "--filter":
			if i+1 < len(f) { // the expression, quoted with spaces or not, without its quotes
				i++
				expr := []string{f[i]}
				if q := f[i][0]; (q == '"' || q == '\'') && !(len(f[i]) > 1 && strings.HasSuffix(f[i], string(q))) {
					for i+1 < len(f) && !strings.HasSuffix(f[i], string(q)) {
						i++
						expr = append(expr, f[i])
					}
				}
				w += " " + strings.Trim(strings.Join(expr, " "), `"'`)
			}
		case w == "--nologo" || strings.HasPrefix(w, "-p:") || strings.HasPrefix(w, "-clp:") || strings.HasPrefix(w, "-v:") ||
			strings.HasPrefix(w, "--verbosity"):
			continue
		case w == "-v" && i+1 < len(f):
			i++
			continue
		case reAssign.MatchString(w):
			continue
		case reSha.MatchString(w) && strings.ContainsAny(w, "0123456789") && strings.ContainsAny(w, "abcdef"):
			continue
		case strings.HasPrefix(w, "/") || strings.HasPrefix(w, "~/") || strings.HasPrefix(w, "$HOME/"):
			w = filepath.Base(w)
		case strings.HasSuffix(w, ".csproj") && strings.Contains(w, "/"):
			// tests/X/X.csproj is the project folder tests/X
			if d := filepath.Dir(w); strings.TrimSuffix(filepath.Base(w), ".csproj") == filepath.Base(d) {
				w = d
			}
		}
		out = append(out, w)
	}
	return strings.Join(out, " ")
}

func median(xs []int) float64 {
	s := slices.Clone(xs)
	slices.Sort(s)
	n := len(s)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return float64(s[n/2])
	}
	return float64(s[n/2-1]+s[n/2]) / 2
}

func mins(sec float64) string { return fmt.Sprintf("%.0f", sec/60) }

// ingestSteps reads each configured runs.log and queues the step and queue-wait NEEDs, then checks decisions.
func (r *runner) ingestSteps() {
	c, err := loadStepConf(r.home)
	if err != nil {
		r.warn(true, "step watch: %v", err)
		return
	}
	if c == nil {
		return
	}
	now := clock()
	for _, l := range c.Log {
		runs, bad, err := readRuns(expand(l.Path), c.Alias)
		if err != nil {
			r.warn(true, "step watch: cannot read %s log %s: %v", l.Name, l.Path, err)
			continue
		}
		if bad > 0 {
			r.warn(false, "step watch: %s log %s: %d lines not read", l.Name, l.Path, bad)
		}
		r.st.StepRuns += len(runs)
		// a cut run of a script is not watched (no full name on record); say so, with the scripts it hits most, each run
		cut, n := map[string]int{}, 0
		for _, s := range runs {
			if s.unwatched {
				cut[filepath.Base(s.script)]++
				n++
			}
		}
		if n > 0 {
			r.st.StepUnwatched += n
			var top []string
			for s := range cut {
				top = append(top, s)
			}
			sort.Slice(top, func(a, b int) bool { return cut[top[a]] > cut[top[b]] || cut[top[a]] == cut[top[b]] && top[a] < top[b] })
			for i := range top {
				top[i] = fmt.Sprintf("%s %d", top[i], cut[top[i]])
			}
			r.warn(false, "step watch: %s: %d of %d runs not watched, command cut at %d characters with only the script on record (%s)",
				l.Name, n, len(runs), cmdCut, strings.Join(top[:min(3, len(top))], ", "))
		}
		if err := r.inTx(func(tx *sql.Tx) error {
			if err := r.stepChanges(tx, l.Name, runs, now); err != nil {
				return err
			}
			return r.queueWaits(tx, l.Name, runs, now)
		}); err != nil {
			r.warn(true, "step watch: %s: %v", l.Name, err)
		}
	}
	r.checkDecisions(c, now)
}

// stepChanges walks each kind's good runs in time order. At each run, the last 3 runs' median is compared with the
// median of the kind's runs in the 7 days before the first of them; a change is a run where that turns slower (over 1.5x,
// over 5 min and 10 min or more longer) after a run where it was not. A step that swings between slow and fast is one
// change: a new one counts only a week after the last, or when it is 1.5x slower again than the last change's "after"
// (review P2: bash s-tip-i.sh posted 1 -> 12 min three times in 7 hours). One NEED per change, keyed on its first slow
// run and a hash of the kind (a kind may hold a secret the line's redaction catches but a key would keep).
func (r *runner) stepChanges(tx *sql.Tx, name string, runs []stepRun, now time.Time) error {
	byKind := map[string][]stepRun{}
	var kinds []string
	for _, s := range runs {
		if !s.ok {
			continue
		}
		if _, seen := byKind[s.kind]; !seen {
			kinds = append(kinds, s.kind)
		}
		byKind[s.kind] = append(byKind[s.kind], s)
	}
	for _, k := range kinds {
		rs := byKind[k]
		wasSlow := false
		var lastAt time.Time // the last change: its first slow run and its "after" median
		var lastAfter float64
		lo := 0 // the first run inside the 7-day window
		for i := 2; i < len(rs); i++ {
			w := rs[i-2 : i+1]
			for lo < i-2 && rs[lo].at.Before(w[0].at.Add(-stepWindow)) {
				lo++
			}
			base := rs[lo : i-2]
			if len(base) < stepBaseMin {
				wasSlow = false
				continue
			}
			var bs, ws []int
			for _, b := range base {
				bs = append(bs, b.seconds)
			}
			for _, x := range w {
				ws = append(ws, x.seconds)
			}
			bm, wm := median(bs), median(ws)
			slow := wm > stepFactor*bm && wm > stepFloor && wm-bm >= stepGain
			if slow && !wasSlow && (lastAt.IsZero() || !w[0].at.Before(lastAt.Add(stepWindow)) || wm > stepFactor*lastAfter) {
				first := w[0]
				for _, x := range w {
					if float64(x.seconds) > stepFactor*bm {
						first = x
						break
					}
				}
				lastAt, lastAfter = first.at, wm
				if now.Sub(rs[i].at) <= stepRecent {
					var raws []string
					if rs := rawKinds(base); len(rs) > 1 || len(rs) == 1 && rs[0] != k {
						raws = rs
					}
					text := r.stepLine(name, k, raws, bm, wm, len(base), first)
					key := "steps:" + name + ":" + sha1Hex([]byte(k))[:12] + ":" + first.at.UTC().Format(time.RFC3339)
					if err := r.queueLine(tx, key, "NEED", "crius", "crius:lead", text); err != nil {
						return err
					}
				}
			}
			wasSlow = slow
		}
	}
	return nil
}

// rawKinds is the commands (before the alias) of base, most runs first.
func rawKinds(base []stepRun) []string {
	n := map[string]int{}
	for _, b := range base {
		n[b.raw]++
	}
	var out []string
	for k := range n {
		out = append(out, k)
	}
	sort.Slice(out, func(a, b int) bool { return n[out[a]] > n[out[b]] || n[out[a]] == n[out[b]] && out[a] < out[b] })
	return out
}

// queueWaits: per local day, the jobs' total wait against their total run time (every run, failed ones too: a slot
// they held was held). A recent day whose wait is longer, and over 30 min, gets one NEED.
func (r *runner) queueWaits(tx *sql.Tx, name string, runs []stepRun, now time.Time) error {
	type tot struct{ run, wait, n int }
	days := map[string]*tot{}
	var order []string
	for _, s := range runs {
		d := s.at.Local().Format("2006-01-02")
		if days[d] == nil {
			days[d] = &tot{}
			order = append(order, d)
		}
		days[d].run += s.seconds
		days[d].wait += s.waitS
		days[d].n++
	}
	for _, d := range order {
		t := days[d]
		start, err := time.ParseInLocation("2006-01-02", d, time.Local)
		if err != nil {
			return err
		}
		if now.Sub(start) > stepRecent || t.wait <= t.run || t.wait < waitFloor {
			continue
		}
		text := fmt.Sprintf("heavy queue on %s %s: jobs waited %.1f h in all and ran %.1f h (%d runs); wait over run time means too few slots or too many jobs at once",
			name, d, float64(t.wait)/3600, float64(t.run)/3600, t.n)
		if err := r.queueLine(tx, "steps:wait:"+name+":"+d, "NEED", "crius", "crius:lead", text); err != nil {
			return err
		}
	}
	return nil
}

var (
	// a path starts at a word edge: qa/heavy-max and M1/M2 are not paths (both read as /heavy-max, /M2 on 11-18, 11-20)
	rePath  = regexp.MustCompile("(?:^|[\\s(\"'`])((?:~/|/)[^\\s()\",;'`]+)")
	reArrow = regexp.MustCompile("^[)\"'`:=]*\\s*(\\S+)\\s*->\\s*(\\S+)") // after a closing quote or paren, or a colon
	// "to N" counts only after a verb that sets (review P2: "move the Api gate to 3 groups", "QA batch up to 3")
	reTo = regexp.MustCompile(`(?i)\b(?:raise|lower|set|change|bump|increase|decrease|reduce|drop)\b[^.;]{0,30}?\bto\s+(\d+)\b`)
)

// decisionSet is the file and value one DECISION sets.
type decisionSet struct {
	host, path, value string
}

// parseDecision finds the settings change in a DECISION's text: "<path or setting> <old> -> <new>", or a path or setting
// followed later by a verb that sets and "to <number>". The host is a [hosts] name among the 3 words just before the path
// or setting ("Legion heavy slots"); none is here (review P2: "Mac heavy slots 1 -> 2 (the Legion stays at 1)").
func parseDecision(text string, c *stepConf) (decisionSet, bool) {
	type hit struct {
		start, end int // the name in text
		path       string
	}
	var hits []hit
	for _, m := range rePath.FindAllStringSubmatchIndex(text, -1) {
		hits = append(hits, hit{m[2], m[3], strings.TrimRight(text[m[2]:m[3]], ".:")})
	}
	var names []string
	for n := range c.Settings {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names { // matched on text itself: lower-casing can change a string's length (review P3: a panic)
		re := regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(n) + `\b`)
		for _, m := range re.FindAllStringIndex(text, -1) {
			hits = append(hits, hit{m[0], m[1], c.Settings[n]})
		}
	}
	sort.SliceStable(hits, func(a, b int) bool { return hits[a].start < hits[b].start })
	hostOf := func(h hit) string {
		words := strings.Fields(text[:h.start])
		if len(words) > 3 {
			words = words[len(words)-3:]
		}
		for _, w := range words {
			w = strings.Trim(w, ".,;:()`'\"")
			for name := range c.Hosts {
				if strings.EqualFold(w, name) {
					return name
				}
			}
		}
		return ""
	}
	clean := func(v string) string { return strings.TrimRight(v, ".,;:)") }
	for _, h := range hits { // the arrow form, right after the name
		if m := reArrow.FindStringSubmatch(text[h.end:]); m != nil && clean(m[2]) != "" {
			return decisionSet{hostOf(h), h.path, clean(m[2])}, true
		}
	}
	for _, h := range hits { // the "raise it to N" form, anywhere after the name
		if m := reTo.FindStringSubmatch(text[h.end:]); m != nil {
			return decisionSet{hostOf(h), h.path, m[1]}, true
		}
	}
	return decisionSet{}, false
}

// readSetting reads the start of a settings file here, or on host with its command.
func readSetting(c *stepConf, host, path string) (string, error) {
	if host == "" {
		f, err := os.Open(expand(path))
		if err != nil {
			return "", err
		}
		defer f.Close()
		b, err := io.ReadAll(io.LimitReader(f, 4096))
		return strings.TrimSpace(string(b)), err
	}
	arg := "head -c 4096 -- '" + strings.ReplaceAll(path, "'", `'\''`) + "'"
	if rest, ok := strings.CutPrefix(path, "~/"); ok { // the other machine's home
		arg = "head -c 4096 -- ~/'" + strings.ReplaceAll(rest, "'", `'\''`) + "'"
	}
	ctx, cancel := context.WithTimeout(context.Background(), hostTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, expand(c.Hosts[host]), arg).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			err = fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// decisionState is <home>/state/stepwatch.json: per decision key, its outcome ("ok", "alerted", "skip": not a one-value
// file) or the reads failed in a row. A decision with an outcome is not read again.
type decisionState struct {
	Done  map[string]string `json:"done,omitempty"`
	Fails map[string]int    `json:"fails,omitempty"`
}

func loadDecisionState(path string) decisionState {
	var s decisionState
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &s) // a broken file starts over: at worst one decision is read again
	}
	if s.Done == nil {
		s.Done = map[string]string{}
	}
	if s.Fails == nil {
		s.Fails = map[string]int{}
	}
	return s
}

func saveDecisionState(path string, s decisionState) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// checkDecisions: each Crius DECISION of the last day that sets a file, 30 minutes on, must find the file holding its
// value. Only the latest decision per file counts. One NEED per decision that is not applied; a file that cannot be read
// three runs in a row is one NEED of its own (a host asleep once does not use up the "not applied" line).
func (r *runner) checkDecisions(c *stepConf, now time.Time) {
	rows, err := r.db.Query(`select ts, coalesce(text, '') from event where kind = 'decision' and project = 'crius'
		and actor = 'lead' and ts >= ? order by ts, id`, now.Add(-decisionAge).UTC().Format("2006-01-02T15:04:05Z"))
	if err != nil {
		r.warn(true, "decision check: %v", err)
		return
	}
	type dec struct {
		ts time.Time
		decisionSet
		text string
	}
	latest := map[string]dec{}
	var order []string
	for rows.Next() {
		var ts, text string
		if err := rows.Scan(&ts, &text); err != nil {
			rows.Close()
			r.warn(true, "decision check: %v", err)
			return
		}
		t, err := time.Parse("2006-01-02T15:04:05Z", ts)
		if err != nil {
			continue
		}
		d, ok := parseDecision(text, c)
		if !ok {
			continue
		}
		k := d.host + ":" + d.path
		if _, seen := latest[k]; !seen {
			order = append(order, k)
		}
		latest[k] = dec{t, d, text}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		r.warn(true, "decision check: %v", err)
		return
	}
	statePath := filepath.Join(r.home, "state", "stepwatch.json")
	st := loadDecisionState(statePath)
	keep := decisionState{Done: map[string]string{}, Fails: map[string]int{}} // only the decisions still in the window
	for _, k := range order {
		d := latest[k]
		key := "decision:" + d.ts.UTC().Format(time.RFC3339) + ":" + k
		if done, ok := st.Done[key]; ok {
			keep.Done[key] = done
			continue
		}
		if now.Sub(d.ts) < decisionWait {
			continue
		}
		r.st.DecisionChecks++
		where := d.path
		if d.host != "" {
			where = d.host + ":" + d.path
		}
		got, err := readSetting(c, d.host, d.path)
		var text, akey string
		switch {
		case err != nil:
			keep.Fails[key] = st.Fails[key] + 1
			if keep.Fails[key] < readFails {
				continue
			}
			text, akey = fmt.Sprintf("decision not checked: %s cannot be read %d runs in a row (%v); the decision of %s says %s: %s",
				where, keep.Fails[key], err, d.ts.Local().Format("2006-01-02T15:04"), d.value, d.text), key+":unread"
		case got == d.value:
			keep.Done[key] = "ok"
			continue
		case len(got) > settingMax || strings.ContainsAny(got, "\r\n"):
			keep.Done[key] = "skip" // a document, not a one-value setting: the decision was about something else
			continue
		default:
			text, akey = fmt.Sprintf("decision not applied: %s holds %s, the decision says %s (crius:lead %s, %s ago): %s",
				where, got, d.value, d.ts.Local().Format("2006-01-02T15:04"), now.Sub(d.ts).Round(time.Minute), d.text), key
			keep.Done[key] = "alerted"
		}
		if err := r.inTx(func(tx *sql.Tx) error { return r.queueLine(tx, akey, "NEED", "crius", "crius:lead", text) }); err != nil {
			r.warn(true, "decision check: %v", err)
			delete(keep.Done, key) // try again next run
		}
	}
	if err := saveDecisionState(statePath, keep); err != nil {
		r.warn(true, "decision check: %v", err)
	}
}

// stepLine is a step's NEED text, made to fit a bus line (300 characters): a long step, the steps it ran as before and
// the folder are shortened until it fits, the step last, so the line keeps its minutes and its first slow run. Each name
// is redacted before it is shortened: a cut credential may no longer match a redaction rule.
func (r *runner) stepLine(log, k string, raws []string, bm, wm float64, nbase int, first stepRun) string {
	k, cwd := r.redacted(k), r.redacted(first.cwd)
	for i := range raws {
		raws[i] = r.redacted(raws[i])
	}
	line := func(name, was, cwd string) string {
		return fmt.Sprintf("step slower on %s: `%s` %s -> %s min (median of the last 3 runs vs %d runs in the 7 days before%s); first slow run %s in %s",
			log, name, mins(bm), mins(wm), nbase, was, first.at.Local().Format("2006-01-02 15:04"), cwd)
	}
	var all, one, count string
	if len(raws) > 0 {
		var names []string
		for _, w := range raws {
			names = append(names, shortName(w, 100))
		}
		all = ", before as `" + strings.Join(names, "`, `") + "`"
		one = ", before as `" + shortName(raws[0], 60) + "`"
		if len(raws) > 1 {
			one += fmt.Sprintf(" and %d more", len(raws)-1)
		}
		count = fmt.Sprintf(", before under %d other commands", len(raws))
	}
	short := shortPath(cwd)
	want := min(utf8.RuneCountInString(k), 40)
	for _, t := range [][2]string{{all, cwd}, {one, cwd}, {one, short}, {count, short}} {
		if room := store.MaxLen - utf8.RuneCountInString(line("", t[0], t[1])); room >= want {
			return line(shortName(k, min(room, 100)), t[0], t[1])
		}
	}
	return line(shortName(k, 40), count, short) // fit cuts the end
}

// shortName keeps a long step's start and end: two steps may differ only at the end of a long command.
func shortName(k string, max int) string {
	r := []rune(k)
	if len(r) <= max {
		return k
	}
	head := (max - 3) * 2 / 5
	return string(r[:head]) + " … " + string(r[len(r)-(max-3-head):])
}

// shortPath is a long folder's last two parts.
func shortPath(p string) string {
	if utf8.RuneCountInString(p) <= 60 {
		return p
	}
	parts := strings.Split(strings.TrimRight(p, "/"), "/")
	if len(parts) <= 2 {
		return shortName(p, 60)
	}
	return shortName("…/"+strings.Join(parts[len(parts)-2:], "/"), 60)
}
