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
//	[hosts]                      # a DECISION that names a host word is checked there: <cmd> "cat -- <path>"
//	legion = "~/.crius/state/quotes/build/legion/L"
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
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
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
	hostTimeout  = 20 * time.Second
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
	cmd, cwd  string
	kind, raw string // raw: the kind before the alias
	seconds   int
	waitS     int
	ok        bool // code 0 and not killed: only these are step times
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
			Cmd     string  `json:"cmd"`
			Cwd     string  `json:"cwd"`
			Seconds float64 `json:"seconds"`
			WaitS   float64 `json:"waitS"`
			Code    int     `json:"code"`
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
		raw := stepKind(j.Cmd)
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
		out = append(out, stepRun{at: t, cmd: j.Cmd, cwd: j.Cwd, kind: kind, raw: raw, seconds: int(j.Seconds),
			waitS: int(j.WaitS), ok: j.Code == 0 && !j.Killed})
	}
	if err := sc.Err(); err != nil {
		return nil, bad, err
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].at.Before(out[b].at) })
	return out, bad, nil
}

var (
	reShell   = regexp.MustCompile(`^(?:bash|sh) -c\s+`)
	reTimeout = regexp.MustCompile(`^timeout\s+\S+\s+`)
	rePerl    = regexp.MustCompile(`^perl -e alarm shift; exec @ARGV or die "exec: \$!"\s+\S+\s+`)
	reCd      = regexp.MustCompile(`^cd\s+\S+\s*&&\s*`)
	reExport  = regexp.MustCompile(`^export\s+[^;]*;\s*`)
	reSha     = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
)

// stepKind is a command less what changes from run to run of one step: shell wrappers, cd into a worktree, timeouts,
// output redirection, quiet and build flags, shas and the folders of absolute paths. A --filter stays with its expression
// (unquoted): each test set is its own step, and the whole project another (one kind for every filter flagged 7 false
// changes on the Legion log of 11-21).
func stepKind(cmd string) string {
	s := strings.TrimSpace(cmd)
	for {
		before := s
		s = strings.TrimSpace(s)
		if m := reShell.FindString(s); m != "" {
			s = strings.Trim(s[len(m):], `"'`)
		}
		for _, re := range []*regexp.Regexp{reTimeout, rePerl, reCd, reExport} {
			if m := re.FindString(s); m != "" {
				s = s[len(m):]
			}
		}
		if s == before {
			break
		}
	}
	f := strings.Fields(s)
	var out []string
	for i := 0; i < len(f); i++ {
		w := f[i]
		switch {
		case w == "|" || w == "&&" || w == ";" || w == "||" || strings.HasPrefix(w, ">") || strings.HasPrefix(w, "2>") ||
			strings.HasPrefix(w, "&>"):
			i = len(f) // the rest is what the output goes to, or a next command
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
// over 5 min and 10 min or more longer) after a run where it was not. One NEED per change, keyed on its first slow run.
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
			if slow && !wasSlow && now.Sub(rs[i].at) <= stepRecent {
				first := w[0]
				for _, x := range w {
					if float64(x.seconds) > stepFactor*bm {
						first = x
						break
					}
				}
				was := ""
				if raws := rawKinds(base); len(raws) == 1 && raws[0] != k {
					was = fmt.Sprintf(", before as `%s`", raws[0])
				} else if len(raws) > 1 {
					was = fmt.Sprintf(", before as `%s`", strings.Join(raws, "`, `"))
				}
				text := fmt.Sprintf("step slower on %s: `%s` %s -> %s min (median of the last 3 runs vs %d runs in the 7 days before%s); first slow run %s in %s",
					name, k, mins(bm), mins(wm), len(base), was, first.at.Local().Format("2006-01-02 15:04"), first.cwd)
				key := "steps:" + name + ":" + k + ":" + first.at.UTC().Format(time.RFC3339)
				if err := r.queueLine(tx, key, "NEED", "crius", "crius:lead", text); err != nil {
					return err
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
	reArrow = regexp.MustCompile("^[)\"'`]?\\s*(\\S+)\\s*->\\s*(\\S+)") // after a closing quote or paren
	reTo    = regexp.MustCompile(`\bto\s+(\d+)\b`)
)

// decisionSet is the file and value one DECISION sets.
type decisionSet struct {
	host, path, value string
}

// parseDecision finds the settings change in a DECISION's text: "<path or setting> <old> -> <new>", or a path or
// setting followed later by "to <number>". The host is the first [hosts] name the text holds as a word ("" is here).
func parseDecision(text string, c *stepConf) (decisionSet, bool) {
	var d decisionSet
	lower := strings.ToLower(text)
	var hosts []string
	for h := range c.Hosts {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	for _, h := range hosts {
		if regexp.MustCompile(`\b` + regexp.QuoteMeta(strings.ToLower(h)) + `\b`).MatchString(lower) {
			d.host = h
			break
		}
	}
	type hit struct {
		at        int // where the name ends
		path      string
		nameStart int
	}
	var hits []hit
	for _, m := range rePath.FindAllStringSubmatchIndex(text, -1) {
		hits = append(hits, hit{m[3], strings.TrimRight(text[m[2]:m[3]], ".:"), m[2]})
	}
	var names []string
	for n := range c.Settings {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if i := strings.Index(lower, strings.ToLower(n)); i >= 0 {
			hits = append(hits, hit{i + len(n), c.Settings[n], i})
		}
	}
	sort.Slice(hits, func(a, b int) bool { return hits[a].nameStart < hits[b].nameStart })
	clean := func(v string) string { return strings.TrimRight(v, ".,;:)") }
	for _, h := range hits { // the arrow form, right after the name
		if m := reArrow.FindStringSubmatch(text[h.at:]); m != nil {
			d.path, d.value = h.path, clean(m[2])
			return d, d.value != ""
		}
	}
	for _, h := range hits { // the "to N" form, anywhere after the name
		if m := reTo.FindStringSubmatch(text[h.at:]); m != nil {
			d.path, d.value = h.path, m[1]
			return d, true
		}
	}
	return d, false
}

// readSetting reads a settings file here, or on host with its command.
func readSetting(c *stepConf, host, path string) (string, error) {
	if host == "" {
		b, err := os.ReadFile(expand(path))
		return strings.TrimSpace(string(b)), err
	}
	arg := "cat -- '" + strings.ReplaceAll(path, "'", `'\''`) + "'"
	if rest, ok := strings.CutPrefix(path, "~/"); ok { // the other machine's home
		arg = "cat -- ~/'" + strings.ReplaceAll(rest, "'", `'\''`) + "'"
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

// checkDecisions: each Crius DECISION of the last day that sets a file, 30 minutes on, must find the file holding its
// value. Only the latest decision per file counts. One NEED per decision that is not applied (or cannot be checked).
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
	for _, k := range order {
		d := latest[k]
		if now.Sub(d.ts) < decisionWait {
			continue
		}
		r.st.DecisionChecks++
		key := "decision:" + d.ts.UTC().Format(time.RFC3339) + ":" + k
		where := d.path
		if d.host != "" {
			where = d.host + ":" + d.path
		}
		got, err := readSetting(c, d.host, d.path)
		var text string
		switch {
		case err != nil:
			text = fmt.Sprintf("decision not checked: %s cannot be read (%v); the decision of %s says %s: %s",
				where, err, d.ts.Local().Format("2006-01-02T15:04"), d.value, d.text)
		case got != d.value:
			text = fmt.Sprintf("decision not applied: %s holds %s, the decision says %s (crius:lead %s, %s ago): %s",
				where, got, d.value, d.ts.Local().Format("2006-01-02T15:04"), now.Sub(d.ts).Round(time.Minute), d.text)
		default:
			continue
		}
		if err := r.inTx(func(tx *sql.Tx) error { return r.queueLine(tx, key, "NEED", "crius", "crius:lead", text) }); err != nil {
			r.warn(true, "decision check: %v", err)
		}
	}
}
