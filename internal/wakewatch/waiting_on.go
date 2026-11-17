package wakewatch

import (
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/sessionstate"
)

// t3WaitingFor is how long a role on ⏳ stays quiet while it waits on an item in flight with a live bus waiter (11-17
// 23:10 and 23:14: two QA threads drew "its job may be gone" at 47 min while each waited, waiter armed, on an item in
// another role's heavy run). Past it, one alert names what it waits on.
const t3WaitingFor = 2 * time.Hour

// waitHold is what a waiting role waits on: the item it named, who has it in flight, and the waiter that will wake it.
type waitHold struct {
	item, by, waiter string
}

// landedTypes are bus line types that say an item has finished: the role waiting on it should be at work.
var landedTypes = map[string]bool{"DONE": true, "VERIFIED": true, "PASS": true, "LANDED": true, "MERGED": true}

var (
	// prItemRe is a PR number written bare: 4 to 6 digits (shorter ones are counts, minutes and times).
	prItemRe = regexp.MustCompile(`^\d{4,6}$`)
	// rowItemRe is a row id: a capital, a dash and two or more digits (F-118); one or two capitals and two or more
	// digits (B105); or a capital, digits and parts that start with a letter (O27.E, R3.C4). A capital and one digit
	// (S1, P1) is a severity; UTF-8, SHA-256, TS-1 and Go1.22 are no items.
	rowItemRe = regexp.MustCompile(`^(?:[A-Z]-\d{2,}|[A-Z]{1,2}\d{2,}|[A-Z]\d+(?:\.[A-Za-z][A-Za-z0-9]*)+)$`)
	// hashItemRe is the number after a "#": a PR number of 3 to 6 digits ("step #2" is a count).
	hashItemRe = regexp.MustCompile(`^\d{3,6}$`)
	// notItemBefore and notItemAfter are words beside a bare number that make it a count, port, pid or time, not a PR.
	notItemBefore = map[string]bool{"port": true, "pid": true, "pids": true, "eta": true, "rc": true, "exit": true, "line": true, "in": true}
	notItemAfter  = map[string]bool{"passed": true, "failed": true, "tests": true, "ms": true, "lines": true, "bytes": true, "files": true}
	// itemSplitRe splits text into words that may be items: what is left between the characters no item holds.
	itemSplitRe = regexp.MustCompile(`[^A-Za-z0-9.#-]+`)
)

// words gives the words of text an item could be, each without the dots and dashes that end a sentence or a clause.
func words(text string) []string {
	var out []string
	for _, w := range itemSplitRe.Split(text, -1) {
		if w = strings.TrimRight(w, ".-"); w != "" {
			out = append(out, w)
		}
	}
	return out
}

// itemsIn gives the PR numbers and row ids the texts name, each once, in the order first named.
func itemsIn(texts ...string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range texts {
		ws := words(t)
		for i, w := range ws {
			item := w
			if j := strings.LastIndexByte(w, '#'); j >= 0 {
				if item = w[j+1:]; !hashItemRe.MatchString(item) {
					continue
				}
			} else if prItemRe.MatchString(w) {
				if i > 0 && notItemBefore[strings.ToLower(ws[i-1])] || i+1 < len(ws) && notItemAfter[strings.ToLower(ws[i+1])] {
					continue
				}
			} else if !rowItemRe.MatchString(w) {
				continue
			}
			if k := itemKey(item); !seen[k] {
				seen[k] = true
				out = append(out, item)
			}
		}
	}
	return out
}

// itemKey is the form items are compared in: lower case, without dots, dashes, underscores or "#". A heavy run's job id
// writes R3.C4 as r3c4.
func itemKey(s string) string {
	return strings.ToLower(strings.NewReplacer(".", "", "-", "", "_", "", "#", "").Replace(s))
}

// mentions says whether text names item as a whole word.
func mentions(text, item string) bool {
	k := itemKey(item)
	return slices.ContainsFunc(words(text), func(w string) bool {
		if i := strings.LastIndexByte(w, '#'); i >= 0 {
			w = w[i+1:]
		}
		return itemKey(w) == k
	})
}

// heavyRunOf gives the heavy run that names item: a heavy-run.sh process whose words before "--" (its role, job id and
// labels) hold it, whole or as a dash-separated part of the job id (q2-r3c4). by is "<role>'s heavy run (pid N)".
func heavyRunOf(procs []Proc, item string) (by string, ok bool) {
	k := itemKey(item)
	for _, p := range procs {
		w := strings.Fields(p.Command)
		if progOf(w) != "heavy-run.sh" {
			continue
		}
		at := slices.IndexFunc(w, func(s string) bool { return filepath.Base(s) == "heavy-run.sh" })
		args := w[at+1:]
		if end := slices.Index(args, "--"); end >= 0 {
			args = args[:end]
		}
		if len(args) < 2 {
			continue
		}
		for _, a := range args[1:] {
			if itemKey(a) == k || slices.ContainsFunc(strings.Split(a, "-"), func(part string) bool { return itemKey(part) == k }) {
				return fmt.Sprintf("%s's heavy run (pid %d)", args[0], p.PID), true
			}
		}
	}
	return "", false
}

// waiterOf gives the bus waiter (isWaiter) that runs under one of the claude pids: "buswait.sh (pid N)".
func waiterOf(procs []Proc, pids map[int]bool) (string, bool) {
	parent := map[int]int{}
	for _, p := range procs {
		parent[p.PID] = p.PPID
	}
	for _, p := range procs {
		w := strings.Fields(p.Command)
		if len(w) == 0 || !isWaiter(w) {
			continue
		}
		for pid, n := p.PPID, 0; pid > 1 && n < 64; pid, n = parent[pid], n+1 {
			if pids[pid] {
				return fmt.Sprintf("%s (pid %d)", progOf(w), p.PID), true
			}
		}
	}
	return "", false
}

// quietTypes are the line types team-wait.sh (member mode) does not wake a role for: a line of one only wakes it when
// it asks it something ("?" or "<role>:") or carries OWNER. INFO wakes a lead: members answer the lead in INFO.
var quietTypes = map[string]bool{"ACK": true, "INFO": true, "WAIT": true, "VERIFIED": true, "STATUS": true, "HEAVY": true}

// wakes says whether a bus line (type typ, to, text) wakes who's waiter, as team-wait.sh decides it: a line to who, or
// to the whole team (<project>:all, all), of a type that is not quiet; a quiet one only when it asks who something or
// carries OWNER (to the whole team: OWNER or "<role>:" only).
func wakes(project, who, typ, to, text string) bool {
	direct, team := false, false
	for _, r := range recipients(to) {
		switch {
		case busName(project, r) == who:
			direct = true
		case strings.EqualFold(r, project+":all") || strings.EqualFold(r, "all"):
			team = true
		}
	}
	if !direct && !team {
		return false
	}
	_, role, _ := strings.Cut(who, ":")
	quiet := quietTypes[typ] && !(typ == "INFO" && role == "lead" && direct)
	if !quiet {
		return true
	}
	named := strings.Contains(strings.ToLower(text), role+":")
	return strings.Contains(text, "OWNER") || named || direct && strings.Contains(text, "?")
}

// waitingOn says whether who, a role whose last reply ends on ⏳, waits on an item in flight: (a) a bus waiter runs
// under one of its live claudes (its session sessionID, or one crius-hook files under who), and (b) an item named in
// lastReply or in its newest bus line is in a heavy run, or a line from another role names it since who's newest line
// (since, the idle spell's start, when who has none; never more than t3WaitingFor before since), and that line does not
// say it landed. A line to who since its newest line and its last reply ends the hold: its waiter should have woken it,
// so a thread that has not moved is what the alert is for. why is "" when it holds; otherwise why not. unread is true when a check
// could not be read: unreadable is never waiting.
func waitingOn(d Deps, procs func() ([]Proc, error), ss []Session, sesErr error, states []sessionstate.State,
	who, sessionID, lastReply string, since time.Time) (h waitHold, why string, unread bool) {
	if sesErr != nil {
		return h, "could not list the live claude sessions: " + sesErr.Error(), true
	}
	pids := map[int]bool{}
	for _, s := range byRole(ss, states)[who] {
		pids[s.PID] = true
	}
	for _, s := range ss {
		if s.ID == sessionID && s.PID > 0 {
			pids[s.PID] = true
		}
	}
	if len(pids) == 0 {
		return h, "no live claude of it was found, so no bus waiter under it", false
	}
	if procs == nil {
		return h, "no process list to read", true
	}
	ps, err := procs()
	if err != nil {
		return h, "could not read the process list: " + err.Error(), true
	}
	if h.waiter, _ = waiterOf(ps, pids); h.waiter == "" {
		return h, "no bus waiter runs under its claude", false
	}
	lines, _, err := busLines(d.Home, who)
	if err != nil {
		return h, "could not read its bus: " + err.Error(), true
	}
	project := strings.SplitN(who, ":", 2)[0]
	var mine time.Time
	var mineText string
	for _, m := range lines {
		if busName(project, m[3]) != who {
			continue
		}
		if at, err := lineTime(m[1], d.Now); err == nil && !at.Before(mine) {
			mine, mineText = at, m[5]
		}
	}
	items := itemsIn(lastReply, mineText)
	if len(items) == 0 {
		return h, "its last reply and its newest bus line name no item (a PR number or a row id)", false
	}
	floor := mine // lines are stamped to the minute: one in the same minute as who's own counts as after it
	if floor.IsZero() {
		floor = since
	}
	if old := since.Add(-t3WaitingFor); floor.Before(old) {
		floor = old // a line days old says nothing of now
	}
	// a line to who counts from its last reply too: the turn that ended on ⏳ read what came before it. Lines are stamped
	// to the minute, so one in the reply's minute counts (an alert too many, never one too few).
	toFloor := since.Truncate(time.Minute)
	if mine.After(toFloor) {
		toFloor = mine
	}
	for _, m := range lines {
		t, err := lineTime(m[1], d.Now)
		if err != nil || t.Before(toFloor) || m[3] == "crius:wakewatch" || busName(project, m[3]) == who {
			continue
		}
		if wakes(project, who, m[2], m[4], m[5]) {
			return h, fmt.Sprintf("%s's %s line to it at %s came after its newest line, and its waiter has not moved it", m[3], m[2], t.Local().Format("15:04")), false
		}
	}
	var landed []string
	for _, item := range items {
		if by, ok := heavyRunOf(ps, item); ok {
			h.item, h.by = item, by
			return h, "", false
		}
		var at time.Time
		var newest []string
		for _, m := range lines {
			if m[3] == "crius:wakewatch" || busName(project, m[3]) == who || !mentions(m[5], item) {
				continue
			}
			if t, err := lineTime(m[1], d.Now); err == nil && !t.Before(floor) && !t.Before(at) {
				at, newest = t, m
			}
		}
		switch {
		case newest == nil:
		case landedTypes[newest[2]]:
			landed = append(landed, fmt.Sprintf("%s: %s posted %s at %s", item, newest[3], newest[2], at.Local().Format("15:04")))
		default:
			h.item, h.by = item, fmt.Sprintf("%s's %s line at %s", newest[3], newest[2], at.Local().Format("15:04"))
			return h, "", false
		}
	}
	why = fmt.Sprintf("none of %s is in flight (no heavy-run.sh names it, and no other role's line names it since %s's newest)",
		strings.Join(items, ", "), who)
	if landed != nil {
		why += "; landed: " + strings.Join(landed, "; ")
	}
	return h, why, false
}

// Waiting prints, for each role named (project:role) or, with none, each role in a T3 thread whose last reply ends on
// ⏳ and that runs no turn, one line: WAITING ON <item> (<who>) with its waiter, or why the idle alert is not held. It
// only reads (crius-wakewatch waiting). Idle counts from the thread's last update; the park, WAIT, closed and finished
// checks of the alert are not applied. It returns 0, or 2 when a role or a check could not be read.
func Waiting(d Deps, out io.Writer, roles []string) int {
	dir := filepath.Join(d.Home, "roles")
	all := len(roles) == 0
	if all {
		files, err := filepath.Glob(filepath.Join(dir, "*.json"))
		if err != nil {
			fmt.Fprintln(out, "FAILED: could not list", dir+":", err)
			return 2
		}
		for _, f := range files {
			project, role, _ := strings.Cut(strings.TrimSuffix(filepath.Base(f), ".json"), ".")
			roles = append(roles, project+":"+role)
		}
	}
	ss, sesErr := d.Sessions()
	var states []sessionstate.State
	if d.States != nil {
		states, _ = d.States()
	}
	crius, _ := CriusThread(d.Home)
	var procs []Proc
	var procErr error
	read := false
	readProcs := func() ([]Proc, error) {
		if !read {
			read = true
			procs, procErr = d.Procs()
		}
		return procs, procErr
	}
	code := 0
	for _, who := range roles {
		who = strings.ToLower(strings.TrimSpace(who))
		project, role, _ := strings.Cut(who, ":")
		r, err := sessionstate.ReadRole(dir, project, role)
		if err != nil {
			fmt.Fprintf(out, "%s FAILED: %v\n", who, err)
			code = 2
			continue
		}
		id := newestThread(d.Home, project, role, r, states)
		if id == "" || id == crius {
			if !all {
				fmt.Fprintf(out, "%s runs in no T3 thread of its own\n", who)
			}
			continue
		}
		th, err := d.Thread(id)
		if err != nil {
			fmt.Fprintf(out, "%s FAILED: thread %s: %v\n", who, id, err)
			code = 2
			continue
		}
		mark := statusMark(th.LastReply)
		running := (th.Status == "running" || th.Active) && th.Pending == 0
		if all && (running || mark != "⏳" || th.Settled) {
			continue
		}
		if running {
			fmt.Fprintf(out, "%s runs a turn (T3 thread %s)\n", who, id)
			continue
		}
		if mark != "⏳" {
			fmt.Fprintf(out, "%s last reply ends on %q, not ⏳: the waiting rule does not apply (T3 thread %s)\n", who, mark, id)
			continue
		}
		idle := int(d.Now.Sub(th.Updated).Minutes())
		h, why, unread := waitingOn(d, readProcs, ss, sesErr, states, who, r.SessionID, th.LastReply, th.Updated)
		if unread {
			code = 2
		}
		switch {
		case why == "":
			fmt.Fprintf(out, "%s WAITING ON %s (%s), bus waiter %s; idle %d min after ⏳, held until %d min (T3 thread %s)\n",
				who, h.item, h.by, h.waiter, idle, int(t3WaitingFor.Minutes()), id)
		default:
			fmt.Fprintf(out, "%s not waiting: %s; idle %d min after ⏳, alert after %d min (T3 thread %s)\n",
				who, why, idle, int(t3WaitAfter.Minutes()), id)
		}
	}
	return code
}
