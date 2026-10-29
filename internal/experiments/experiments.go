// Package experiments builds the daily experiment scorecard: one number per metric of experiments E1-E8 (the table in
// docs/LEARNINGS.md), for one local day, from the record (record.db), the bus lines in it, Crius's own logs and the
// Quotes rig's quote count (owner 2026-10-25, brief fixes-1025 item 7; sources agreed with crius:lead 18:38). The 09:00
// goal report writes it to state/experiments/<day>.md and includes it. A metric with no source says which line is
// missing; none is left blank.
package experiments

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/redact"
	"github.com/orzazade/crius/internal/store"
)

// Row is one experiment's numbers for the day.
type Row struct{ ID, Name, Value, Target string }

// Deps is what Build reads besides the record: Home is the Crius home (logs/load.log, logs/bus-post-refused.log,
// state/digest/); Worktrees gives the Quotes repo's worktrees now and their size; Leftovers the rig's newest count of
// leftover test quotes on 7090 and when it ran (zero time: no count).
type Deps struct {
	Home      string
	Worktrees func() (n int, gb float64, err error)
	Leftovers func() (n int, at time.Time, err error)
}

// Build counts the local day that starts at day's midnight.
func Build(db *sql.DB, d Deps, day time.Time) ([]Row, error) {
	from := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, day.Location())
	to := from.AddDate(0, 0, 1)
	b := builder{db: db, d: d, from: from, to: to}
	var rows []Row
	for _, f := range []func() (Row, error){b.e1, b.e2, b.e3, b.e4, b.e5, b.e6, b.e7, b.e8} {
		r, err := f()
		if err != nil {
			return nil, err
		}
		rows = append(rows, r)
	}
	return rows, nil
}

type builder struct {
	db       *sql.DB
	d        Deps
	from, to time.Time
}

// eventTS and requestTS are the stored forms of an instant: event.ts has whole seconds, request.ts_first milliseconds.
func eventTS(t time.Time) string   { return t.UTC().Format("2006-01-02T15:04:05Z") }
func requestTS(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

const quotesCalls = " from request r join session s on s.id = r.session where s.project = 'quotes' and r.ts_first >= ? and r.ts_first < ?"

// ctx is a call's context: everything it read, cached or not.
const ctx = "(r.tok_in + r.tok_cache_read + r.tok_cache_write)"

func (b builder) count(kinds, where string, args ...any) (int, error) {
	var n int
	err := b.db.QueryRow("select count(*) from event where source_key like 'bus:%' and kind in ("+kinds+") and ts >= ? and ts < ?"+where,
		append([]any{eventTS(b.from), eventTS(b.to)}, args...)...).Scan(&n)
	return n, err
}

// E1: $ per landed item and per day (Quotes calls), CPU load (wakewatch's samples).
func (b builder) e1() (Row, error) {
	var cost float64
	if err := b.db.QueryRow("select coalesce(sum(r.cost_list_usd), 0)"+quotesCalls, requestTS(b.from), requestTS(b.to)).Scan(&cost); err != nil {
		return Row{}, err
	}
	landed, err := b.count("'landed'", " and project = 'quotes'")
	if err != nil {
		return Row{}, err
	}
	per := "no LANDED"
	if landed > 0 {
		per = fmt.Sprintf("$%.2f per LANDED (%d LANDED)", cost/float64(landed), landed)
	}
	load, err := b.load()
	if err != nil {
		return Row{}, err
	}
	return Row{"E1", "Smaller team", fmt.Sprintf("%s; $%.2f a day; %s", per, cost, load), "under $30; under $800; under 10"}, nil
}

// load sums up the day's samples in logs/load.log ("<RFC 3339> <1 min> <5 min> <15 min>", one per wakewatch run).
func (b builder) load() (string, error) {
	var sum, top float64
	n := 0
	err := eachLine(filepath.Join(b.d.Home, "logs", "load.log"), func(line string) error {
		f := strings.Fields(line)
		if len(f) < 2 {
			return nil
		}
		at, err := time.Parse(time.RFC3339, f[0])
		if err != nil || at.Before(b.from) || !at.Before(b.to) {
			return nil
		}
		v, err := strconv.ParseFloat(f[1], 64)
		if err != nil {
			return fmt.Errorf("load.log: %q: %v", line, err)
		}
		sum, top, n = sum+v, max(top, v), n+1
		return nil
	})
	if err != nil || n == 0 {
		return "no load samples", err
	}
	return fmt.Sprintf("load %.1f average, %.1f top (%d samples)", sum/float64(n), top, n), nil
}

var hexRe = regexp.MustCompile(`\b[0-9a-f]{8,40}\b`)

// shas gives the words of 8-40 hex with a letter: a sha or its start.
func shas(text string) []string {
	var out []string
	for _, w := range hexRe.FindAllString(text, -1) {
		if strings.ContainsAny(w, "abcdef") {
			out = append(out, w)
		}
	}
	return out
}

type busLine struct {
	at                time.Time
	kind, actor, text string
}

// lines gives Quotes bus lines of the kinds from since to the end of the day, oldest first.
func (b builder) lines(kinds string, since time.Time) ([]busLine, error) {
	rows, err := b.db.Query("select ts, kind, coalesce(actor, ''), coalesce(text, '') from event where source_key like 'bus:%' and project = 'quotes' and kind in ("+
		kinds+") and ts >= ? and ts < ? order by ts, id", eventTS(since), eventTS(b.to))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []busLine
	for rows.Next() {
		var ts string
		var l busLine
		if err := rows.Scan(&ts, &l.kind, &l.actor, &l.text); err != nil {
			return nil, err
		}
		if l.at, err = time.Parse(time.RFC3339, ts); err != nil {
			return nil, fmt.Errorf("event ts %q: %w", ts, err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// E2: READY -> PASS: from the first READY (up to 3 days back) of an item sha, the first one it names, to the first PASS
// whose item sha, the first one it names, is the same (not a base sha it also cites); once per item, so a reposted
// READY and PASS are no second pair. A sha counts by its first 8 hex: lines cite one at 8 or 9.
func (b builder) e2() (Row, error) {
	ls, err := b.lines("'ready', 'pass'", b.from.AddDate(0, 0, -3))
	if err != nil {
		return Row{}, err
	}
	ready, paired := map[string]time.Time{}, map[string]bool{}
	var mins []float64
	for _, l := range ls {
		s := shas(l.text)
		if len(s) == 0 || paired[s[0][:8]] {
			continue
		}
		item := s[0][:8]
		if l.kind == "ready" {
			if _, ok := ready[item]; !ok {
				ready[item] = l.at
			}
			continue
		}
		if r, ok := ready[item]; ok {
			if !l.at.Before(b.from) {
				mins = append(mins, l.at.Sub(r).Minutes())
			}
			paired[item] = true
		}
	}
	median := "READY -> PASS: no pairs"
	if len(mins) > 0 {
		sort.Float64s(mins)
		m := mins[len(mins)/2]
		if len(mins)%2 == 0 {
			m = (mins[len(mins)/2-1] + mins[len(mins)/2]) / 2
		}
		median = fmt.Sprintf("READY -> PASS median %.0f min (%d pairs)", m, len(mins))
	}
	return Row{"E2", "Speed-up", "owner bug -> on 7090: no source (needs a line naming the owner bug's id in its check-7090 DONE); " +
		median + "; gate minutes: no source (needs batch-gate START and END lines)", "under 1 h; under 10 min; under 25 min"}, nil
}

// E3: QA1's context per call and items (its PASS and FAIL lines); a FAIL of the day naming an item QA1 had PASSed
// (up to 7 days back).
func (b builder) e3() (Row, error) {
	var calls int
	var avg float64
	if err := b.db.QueryRow("select count(*), coalesce(avg("+ctx+"), 0)"+quotesCalls+" and s.role = 'qa1'",
		requestTS(b.from), requestTS(b.to)).Scan(&calls, &avg); err != nil {
		return Row{}, err
	}
	items, err := b.count("'pass', 'fail'", " and project = 'quotes' and actor = 'qa1'")
	if err != nil {
		return Row{}, err
	}
	ls, err := b.lines("'pass', 'fail'", b.from.AddDate(0, 0, -7))
	if err != nil {
		return Row{}, err
	}
	passed := map[string]bool{} // the item sha, the first one a QA1 PASS names, by its first 8 hex
	after := 0
	for _, l := range ls {
		s := shas(l.text)
		if l.kind == "pass" && l.actor == "qa1" && len(s) > 0 {
			passed[s[0][:8]] = true
			continue
		}
		if l.kind == "fail" && !l.at.Before(b.from) && slices.ContainsFunc(s, func(w string) bool { return passed[w[:8]] }) {
			after++
		}
	}
	return Row{"E3", "Fresh context per item, QA1", fmt.Sprintf("%.0fK context per QA1 call (%d calls); %d QA1 PASS/FAIL; %d FAIL after a QA1 PASS",
		avg/1000, calls, items, after), "under 50K; 0"}, nil
}

// E4: Quotes calls over 150K context, and the average.
func (b builder) e4() (Row, error) {
	var n, over int
	var avg float64
	if err := b.db.QueryRow("select count(*), coalesce(sum("+ctx+" > 150000), 0), coalesce(avg("+ctx+"), 0)"+quotesCalls,
		requestTS(b.from), requestTS(b.to)).Scan(&n, &over, &avg); err != nil {
		return Row{}, err
	}
	share := 0
	if n > 0 {
		share = over * 100 / n
	}
	return Row{"E4", "Compaction window 150K", fmt.Sprintf("%d%% of calls over 150K (%d of %d); %.0fK average context", share, over, n, avg/1000),
		"under 20%; under 90K"}, nil
}

// E5: wakes with no action, from the transcripts of Quotes sessions that made calls that day. A wake is a turn opened
// by a task notification; it took no action when it called no tool, or only re-arm tools: Monitor, TaskStop,
// ToolSearch, a Bash waiter on the bus (cost study A's rule).
func (b builder) e5() (Row, error) {
	rows, err := b.db.Query("select distinct s.role, coalesce(s.transcript_path, '')"+quotesCalls, requestTS(b.from), requestTS(b.to))
	if err != nil {
		return Row{}, err
	}
	type tr struct{ role, path string }
	var trs []tr
	for rows.Next() {
		var t tr
		if err := rows.Scan(&t.role, &t.path); err != nil {
			rows.Close()
			return Row{}, err
		}
		if t.path != "" {
			trs = append(trs, t)
		}
	}
	if err := rows.Close(); err != nil {
		return Row{}, err
	}
	wakes, idle := 0, map[string]int{}
	total, missing := 0, 0
	for _, t := range trs {
		if _, err := os.Stat(t.path); errors.Is(err, fs.ErrNotExist) { // moved away: the record has the calls, not the wakes
			missing++
			continue
		}
		w, n, err := b.wakes(t.path)
		if err != nil {
			return Row{}, err
		}
		wakes += w
		idle[t.role] += n
		total += n
	}
	var by []string
	for role, n := range idle {
		if n > 0 {
			by = append(by, fmt.Sprintf("%s %d", role, n))
		}
	}
	sort.Strings(by)
	v := fmt.Sprintf("%d of %d wakes with no action", total, wakes)
	if len(by) > 0 {
		v += " (" + strings.Join(by, ", ") + ")"
	}
	if missing > 0 {
		v += fmt.Sprintf("; %d of %d transcripts missing", missing, len(trs))
	}
	return Row{"E5", "Filtered bus waiter", v, "under 200 a day"}, nil
}

var rearm = []string{"Monitor", "TaskStop", "ToolSearch"}

// waiterRe is a command that waits on the bus, as the Quotes transcripts of 10-29 show them: team-wait.sh, a bus-*.ts
// wait script, or a read of ~/.crius/bus (tail, grep).
var waiterRe = regexp.MustCompile(`team-wait\.sh|\bbus[\w-]*\.ts\s+wait\b|\.crius/bus/`)

// waiter tells a background command that waits on the bus from one that acts, such as bus-post posting a line.
func waiter(cmd string) bool {
	return waiterRe.MatchString(cmd) && !strings.Contains(cmd, "bus-post")
}

// wakes reads one transcript: the wakes that day and how many took no action.
func (b builder) wakes(path string) (wakes, idle int, err error) {
	open, acted := false, false
	end := func() {
		if open && !acted {
			idle++
		}
		open = false
	}
	err = eachLine(path, func(line string) error {
		var e struct {
			Type        string
			IsSidechain bool
			Timestamp   string
			Origin      struct{ Kind string }
			Message     struct{ Content json.RawMessage }
		}
		if json.Unmarshal([]byte(line), &e) != nil || e.IsSidechain {
			return nil // a line that is not an entry, or a sub-agent's, is not the session's turn
		}
		switch e.Type {
		case "user":
			if e.Origin.Kind == "" && strings.Contains(string(e.Message.Content), `"tool_result"`) {
				return nil // a tool's answer goes on the same turn
			}
			end()
			if e.Origin.Kind == "task-notification" {
				at, err := time.Parse(time.RFC3339, e.Timestamp)
				if err == nil && !at.Before(b.from) && at.Before(b.to) {
					open, acted, wakes = true, false, wakes+1
				}
			}
		case "assistant":
			if !open {
				return nil
			}
			var parts []struct {
				Type, Name string
				Input      struct {
					Command    string `json:"command"`
					Background bool   `json:"run_in_background"`
				}
			}
			if json.Unmarshal(e.Message.Content, &parts) != nil {
				return nil
			}
			for _, p := range parts {
				if p.Type != "tool_use" || slices.Contains(rearm, p.Name) || p.Name == "Bash" && p.Input.Background && waiter(p.Input.Command) {
					continue
				}
				acted = true
			}
		}
		return nil
	})
	end()
	return wakes, idle, err
}

// E6: repeats of a known mistake: the typed time refused by bus-post (its refusal log) or still on the bus (a writer
// that is not bus-post), and the other guards' refusals.
func (b builder) e6() (Row, error) {
	typed, other := 0, 0
	err := eachLine(filepath.Join(b.d.Home, "logs", "bus-post-refused.log"), func(line string) error {
		f := strings.Fields(line)
		if len(f) == 0 {
			return nil
		}
		at, err := time.Parse(time.RFC3339, f[0])
		if err != nil || at.Before(b.from) || !at.Before(b.to) {
			return nil
		}
		if strings.Contains(line, "typed time") {
			typed++
		} else {
			other++
		}
		return nil
	})
	if err != nil {
		return Row{}, err
	}
	rows, err := b.db.Query("select ts, project, coalesce(actor, ''), coalesce(text, '') from event where source_key like 'bus:%' and ts >= ? and ts < ?",
		eventTS(b.from), eventTS(b.to))
	if err != nil {
		return Row{}, err
	}
	defer rows.Close()
	slipped := 0
	for rows.Next() {
		var ts, project, actor, text string
		if err := rows.Scan(&ts, &project, &actor, &text); err != nil {
			return Row{}, err
		}
		at, err := time.Parse(time.RFC3339, ts)
		if err != nil {
			return Row{}, fmt.Errorf("event ts %q: %w", ts, err)
		}
		if store.CheckTypedTime(project, actor, text, at.In(b.from.Location())) != nil {
			slipped++
		}
	}
	if err := rows.Err(); err != nil {
		return Row{}, err
	}
	return Row{"E6", "Guards instead of rules", fmt.Sprintf("typed time: %d (%d refused by bus-post, %d on the bus); other guard refusals: %d",
		typed+slipped, typed, slipped, other), "0"}, nil
}

var keptRe = regexp.MustCompile(`(?:\(\d+\)|the question) by crius:lead`)

// E7: NEED-OWNER lines, and the answered questions the goal reports of the day kept out of the owner list.
func (b builder) e7() (Row, error) {
	need, err := b.count("'need-owner'", "")
	if err != nil {
		return Row{}, err
	}
	kept := 0
	files, err := filepath.Glob(filepath.Join(b.d.Home, "state", "digest", b.from.Format("2006-01-02")+"-*.md"))
	if err != nil {
		return Row{}, err
	}
	for _, f := range files {
		t, err := os.ReadFile(f)
		if err != nil {
			return Row{}, err
		}
		for _, l := range strings.Split(string(t), "\n") {
			if _, note, ok := strings.Cut(l, "left out of Needs the owner:"); ok {
				kept += len(keptRe.FindAllString(note, -1))
			}
		}
	}
	return Row{"E7", "Owner time", fmt.Sprintf("%d NEED-OWNER; %d answered questions kept out of the owner list", need, kept),
		"only owner-only questions; 0 re-sent"}, nil
}

// E8: the Quotes repo's worktrees now, and the rig's newest count of leftover test quotes on 7090.
func (b builder) e8() (Row, error) {
	n, gb, err := b.d.Worktrees()
	if err != nil {
		return Row{}, err
	}
	left, at, err := b.d.Leftovers()
	if err != nil {
		return Row{}, err
	}
	quotes := "no rig count of test quotes on 7090"
	if !at.IsZero() {
		quotes = fmt.Sprintf("%d leftover test quotes on 7090 (rig count %s)", left, at.Format("2006-01-02 15:04"))
	}
	return Row{"E8", "Runs clean up", fmt.Sprintf("%d worktrees, %.0f GB now; %s", n, gb, quotes), "under 10 and 10 GB; under 20"}, nil
}

// eachLine calls f with each line of a file; a missing file has no lines.
func eachLine(path string, f func(string) error) error {
	fh, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer fh.Close()
	r := bufio.NewReader(fh)
	for {
		line, err := r.ReadString('\n')
		if line != "" {
			if err := f(strings.TrimRight(line, "\n")); err != nil {
				return err
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// Markdown is the scorecard as the goal report shows it.
func Markdown(day time.Time, rows []Row) string {
	var s strings.Builder
	fmt.Fprintf(&s, "## Experiments (%s; targets: docs/LEARNINGS.md)\n\n| # | Experiment | %s | Target |\n|---|---|---|---|\n",
		day.Format("2006-01-02"), day.Format("01-02"))
	for _, r := range rows {
		fmt.Fprintf(&s, "| %s | %s | %s | %s |\n", r.ID, r.Name, r.Value, r.Target)
	}
	return s.String()
}

// Write saves the scorecard as state/experiments/<day>.md, redacted like every sink.
func Write(home string, day time.Time, md string) error {
	dir := filepath.Join(home, "state", "experiments")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	text, _ := redact.Redact(md)
	return os.WriteFile(filepath.Join(dir, day.Format("2006-01-02")+".md"), []byte(text), 0o600)
}

var countDirRe = regexp.MustCompile(`^quote-count-(\d{8}T\d{4})$`)

// Leftovers reads the rig's newest quote count in dir (~/AppxiteRig/run/quote-count-<local stamp>/run.log): the
// "run leftovers not named, not seed: N" line of its 7090 part. No count yet is zero time; a count without the line
// is an error (its format changed).
func Leftovers(dir string) func() (int, time.Time, error) {
	return func() (int, time.Time, error) {
		es, err := os.ReadDir(dir)
		if err != nil {
			return 0, time.Time{}, err
		}
		newest := ""
		for _, e := range es {
			if m := countDirRe.FindStringSubmatch(e.Name()); m != nil && e.IsDir() && m[1] > newest {
				newest = m[1]
			}
		}
		if newest == "" {
			return 0, time.Time{}, nil
		}
		at, err := time.ParseInLocation("20060102T1504", newest, time.Local)
		if err != nil {
			return 0, time.Time{}, err
		}
		log := filepath.Join(dir, "quote-count-"+newest, "run.log")
		part, n := "", -1
		err = eachLine(log, func(line string) error {
			if strings.HasPrefix(line, "== ") {
				part, _, _ = strings.Cut(strings.TrimPrefix(line, "== "), ":")
			}
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), "run leftovers not named, not seed: "); ok && part == "7090" {
				var err error
				n, err = strconv.Atoi(v)
				return err
			}
			return nil
		})
		if err == nil && n < 0 {
			err = fmt.Errorf("%s has no 7090 \"run leftovers not named, not seed\" line", log)
		}
		return n, at, err
	}
}

// Worktrees counts the repo's worktrees besides the main checkout (git worktree list) and their size on disk (du).
func Worktrees(repo string) func() (int, float64, error) {
	return func() (int, float64, error) {
		out, err := exec.Command("git", "-C", repo, "worktree", "list", "--porcelain").Output()
		if err != nil {
			return 0, 0, fmt.Errorf("git worktree list in %s: %v", repo, err)
		}
		var paths []string
		for _, l := range strings.Split(string(out), "\n") {
			if p, ok := strings.CutPrefix(l, "worktree "); ok {
				paths = append(paths, p)
			}
		}
		if len(paths) < 2 {
			return 0, 0, nil
		}
		var kb float64
		for _, p := range paths[1:] {
			if _, err := os.Stat(p); errors.Is(err, fs.ErrNotExist) {
				continue // listed but gone (prunable): it takes no disk
			}
			out, err := exec.Command("du", "-sk", p).Output()
			f := strings.Fields(string(out))
			if err != nil || len(f) == 0 {
				return 0, 0, fmt.Errorf("du -sk %s: %v", p, err)
			}
			v, err := strconv.ParseFloat(f[0], 64)
			if err != nil {
				return 0, 0, fmt.Errorf("du -sk %s: %v", p, err)
			}
			kb += v
		}
		return len(paths) - 1, kb / (1 << 20), nil
	}
}
