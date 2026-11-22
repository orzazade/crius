package wakewatch

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/sessionstate"
)

// t3WaitingFor is how long a role on ⏳ stays quiet while it waits on an item in flight with a live bus waiter (11-17
// 23:10 and 23:14: two QA threads drew "its job may be gone" at 47 min while each waited, waiter armed, on an item in
// another role's heavy run). Past it, one alert names what it waits on.
const t3WaitingFor = 2 * time.Hour

// purposeTurn is how long before the ⏳ reply a role's own bus line still speaks for it (the turn that ended on it): an
// older "Idle" line says nothing of a later turn that took up work.
const purposeTurn = 15 * time.Minute

// waitHold is what a waiting role waits on: the item it named, who has it in flight, and the waiter that will wake it.
// purpose is set instead of item when it is idle on purpose (purposeIn): the words that say it has no work or name
// what it waits for. With neither, it waits on its bus: the waiter alone holds it, and named lists the items it named
// that nothing has in flight.
type waitHold struct {
	item, by, waiter, purpose, named string
}

// onBusText says a hold on the bus, with the items it named: "no item in flight; named 5521".
func (h waitHold) onBusText() string {
	if h.named == "" {
		return "no item in flight"
	}
	return "no item in flight; named " + h.named
}

var (
	// clauseRe splits text into clauses: at a sentence end before a space, a semicolon or a line break.
	clauseRe = regexp.MustCompile(`[.!?]\s+|;\s*|\n+`)
	// noWorkRe is a clause that says the role has no work: "Idle:" or "Idle until …" at its start, "I am idle", "no work",
	// "nothing to do" or "no <role> NEED/READY" (in capitals: the bus line types). A bare "idle" further on ("runner
	// idle 358/358", "lane 2 is now idle") or "no work" in a longer phrase ("no work was lost") speaks of something else.
	noWorkRe = regexp.MustCompile(`(?i)^idle(?:\s*[:,–—-]|\s+until\b|\s+for now\b|$)|\b(?:i am|i'm|stay(?:ing)?|sit(?:ting)?) idle\b|\bno (?:more |open |new |pending )?work(?:$|\s*[:,(–—-]| (?:left|queued|open|now|yet|for me|to do)\b)|\bnothing (?:to do|open|pending|queued)\b|\bno [\w:-]+ (?-i:NEED|READY)\b`)
	// clockRe is a clock time (08:00); waitWordRe a word that makes a clock time in its clause one the role waits for.
	clockRe    = regexp.MustCompile(`\b([01]?\d|2[0-3]):([0-5]\d)\b`)
	waitWordRe = regexp.MustCompile(`(?i)\b(?:wait(?:s|ing)?|scheduled|due|check)\b`)
	// replyWaitRe is a clause that says it waits for someone's reply, answer or decision.
	replyWaitRe = regexp.MustCompile(`(?i)\bwait(?:s|ing)? (?:for|on) .*\b(?:reply|answer|decision)\b`)
)

// purposeIn gives the first clause of text that says the role has no work or names what it waits for (a clock time with
// a wait or check word, someone's reply), without the status emoji; "" when none does. A clause that names an item
// ("waiting for 5521's suite") is no purpose: that is the in-flight check's to judge. A clock time counts until
// t3WaitAfter past it (read as the next such time from an hour before since): a check that should have run by now
// and has not moved the role is what the alert is for.
func purposeIn(text string, since, now time.Time) string {
	for _, c := range clauseRe.Split(text, -1) {
		c = strings.TrimSpace(strings.TrimRight(strings.Trim(strings.TrimSpace(c), "⏳❓✅"), " .!?"))
		if c == "" || len(itemsIn(c)) > 0 {
			continue
		}
		if noWorkRe.MatchString(c) || waitWordRe.MatchString(c) && dueAhead(c, since, now) || replyWaitRe.MatchString(c) {
			if r := []rune(c); len(r) > 120 {
				c = string(r[:120]) + "…"
			}
			return c
		}
	}
	return ""
}

// passedIn gives the first clause of text, with no item, that names a clock time with a wait or check word, all of
// whose times are over t3WaitAfter past at now (dueAhead): a step the role set itself that should have moved it.
func passedIn(text string, since, now time.Time) string {
	for _, c := range clauseRe.Split(text, -1) {
		c = strings.TrimSpace(strings.TrimRight(strings.Trim(strings.TrimSpace(c), "⏳❓✅"), " .!?"))
		if c == "" || len(itemsIn(c)) > 0 || !waitWordRe.MatchString(c) || !clockRe.MatchString(c) || dueAhead(c, since, now) {
			continue
		}
		if r := []rune(c); len(r) > 120 {
			c = string(r[:120]) + "…"
		}
		return c
	}
	return ""
}

// dueAhead says whether a clock time in c is still ahead, or under t3WaitAfter past, at now. Each time is read in
// since's local day, the next day when it falls over an hour before since.
func dueAhead(c string, since, now time.Time) bool {
	s := since.Local()
	for _, m := range clockRe.FindAllStringSubmatch(c, -1) {
		h, _ := strconv.Atoi(m[1]) // clockRe holds digits only; Atoi reads "08" as 8 (fmt.Sscan reads it as octal 0)
		mi, _ := strconv.Atoi(m[2])
		due := time.Date(s.Year(), s.Month(), s.Day(), h, mi, 0, 0, time.Local)
		if due.Before(s.Add(-time.Hour)) {
			due = due.AddDate(0, 0, 1)
		}
		if !now.After(due.Add(t3WaitAfter)) {
			return true
		}
	}
	return false
}

// statusPart gives the paragraph of a reply that holds its last ⏳ (its last paragraph when it has none), with the
// paragraph before it when that one is the emoji alone ("… is scheduled.\n\n⏳"): where it says what it waits on.
func statusPart(text string) string {
	ps := strings.Split(strings.TrimSpace(text), "\n\n")
	i := len(ps) - 1
	for j := i; j >= 0; j-- {
		if strings.Contains(ps[j], "⏳") {
			i = j
			break
		}
	}
	if i > 0 && strings.TrimSpace(strings.Trim(strings.TrimSpace(ps[i]), "⏳❓✅")) == "" {
		return ps[i-1] + "\n\n" + ps[i]
	}
	return ps[i]
}

// landedTypes are bus line types that say an item has finished: the role waiting on it should be at work.
var landedTypes = map[string]bool{"DONE": true, "VERIFIED": true, "PASS": true, "LANDED": true, "MERGED": true}

var (
	// prItemRe is a PR number written bare: 4 to 6 digits (shorter ones are counts, minutes and times).
	prItemRe = regexp.MustCompile(`^\d{4,6}$`)
	// rowItemRe is a row id: a capital, a dash and two or more digits (F-118); one or two capitals and two or more
	// digits (B105); or a capital, digits and parts that start with a letter (O12.E, R3.C4). A capital and one digit
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

// waiterOf gives the bus waiter (isWaiter) that runs under one of the claude pids, "buswait.sh (pid N)", and when it
// started (zero when not known); of several, the oldest, and zero when any start is not known (the start floors which
// lines end the hold: the oldest floors fewest). "" when none runs.
func waiterOf(procs []Proc, pids map[int]bool) (string, time.Time) {
	parent := map[int]int{}
	for _, p := range procs {
		parent[p.PID] = p.PPID
	}
	name, start := "", time.Time{}
	for _, p := range procs {
		w := strings.Fields(p.Command)
		if len(w) == 0 || !isWaiter(w) {
			continue
		}
		for pid, n := p.PPID, 0; pid > 1 && n < 64; pid, n = parent[pid], n+1 {
			if pids[pid] {
				if name == "" || !start.IsZero() && (p.Start.IsZero() || p.Start.Before(start)) {
					name, start = fmt.Sprintf("%s (pid %d)", progOf(w), p.PID), p.Start
				}
				break
			}
		}
	}
	return name, start
}

// bashTaskMark is in the command of the shell Claude Code's Bash tool starts for each command; it sources the shell
// snapshot. Under a claude with no open turn, such a shell is a background task (11-20: api-split's gate ran so).
const bashTaskMark = "/shell-snapshots/snapshot-"

// ownJobOf gives a background job of the role's own: a Bash tool shell (bashTaskMark) that is a child of one of the
// claude pids and runs no bus waiter in it or under it. Claude Code's other children (its stdin watchdog shell, MCP
// servers) are no job, and a job's shell that was left to launchd is no longer under the claude. what is the command
// it runs (the text it evals, else its first child's, else its own), cut to 80 characters.
func ownJobOf(procs []Proc, pids map[int]bool) (pid int, what string, ok bool) {
	kids := map[int][]Proc{}
	for _, p := range procs {
		kids[p.PPID] = append(kids[p.PPID], p)
	}
	var runsWaiter func(p Proc, depth int) bool
	runsWaiter = func(p Proc, depth int) bool {
		if isWaiter(strings.Fields(p.Command)) {
			return true
		}
		return depth < 64 && slices.ContainsFunc(kids[p.PID], func(k Proc) bool { return runsWaiter(k, depth+1) })
	}
	for _, p := range procs {
		if !pids[p.PPID] || !strings.Contains(p.Command, bashTaskMark) || runsWaiter(p, 0) {
			continue
		}
		what = p.Command
		if _, ev, ok := strings.Cut(p.Command, " eval '"); ok {
			what, _, _ = strings.Cut(ev, "' < /dev/null")
		} else if k := kids[p.PID]; len(k) > 0 {
			what = k[0].Command
		}
		if r := []rune(what); len(r) > 80 {
			what = string(r[:80]) + "…"
		}
		return p.PID, what, true
	}
	return 0, "", false
}

// OwnJob gives a background job of claude pid's own, as ownJobOf finds it: a Bash tool shell under it that runs no bus
// waiter. T3 does not list such a job (crius-spawn -fresh waits for it before it stops the claude).
func OwnJob(procs []Proc, pid int) (job int, what string, ok bool) {
	return ownJobOf(procs, map[int]bool{pid: true})
}

// quietTypes are the line types team-wait.sh (member mode) does not wake a role for on their own.
var quietTypes = map[string]bool{"ACK": true, "INFO": true, "WAIT": true, "VERIFIED": true, "STATUS": true}

// wakes says whether a bus line from another role may wake who's waiter. It is a superset of team-wait.sh's member
// rule, so a line it may wake on is never read as one it sleeps through (that would hold a stuck role for 2 h): a
// NEED-<ROLE> line; for a lead, any line (a lead wakes on BLOCKED, BUG and lines with no arrow too); a line to who, in
// cc, to the whole team, or naming who as a target in its text ("-> qa", "(cc qa)") that comes from a lead or Crius
// (its boss), is of a type that is not quiet, or asks something ("?", "<role>:") or names the owner.
func wakes(project, who, typ, from, to, text string) bool {
	_, role, _ := strings.Cut(who, ":")
	if typ == "NEED-"+strings.ToUpper(role) || role == "lead" {
		return true
	}
	aimed := slices.ContainsFunc(recipients(to), func(r string) bool {
		return busName(project, r) == who || strings.EqualFold(r, project+":all") || strings.EqualFold(r, "all")
	})
	if !aimed && !targetRe(project, role).MatchString(text) {
		return false
	}
	if f := busName(project, from); f == "crius:lead" || strings.HasSuffix(f, ":lead") || !quietTypes[typ] {
		return true
	}
	low := strings.ToLower(text)
	return strings.Contains(text, "?") || strings.Contains(low, role+":") || strings.Contains(low, "owner")
}

// targetRe matches text that names role as a target, as team-wait.sh's named() reads it: after "->", "→" or "cc", in
// a list of names.
func targetRe(project, role string) *regexp.Regexp {
	r := regexp.QuoteMeta(role)
	return regexp.MustCompile(`(?i)(?:->|→|(?:^|[^A-Za-z0-9-])\(?cc:?)\s+(?:[A-Za-z0-9:_-]+,\s*)*(?:` + regexp.QuoteMeta(project) + `:)?` + r + `(?:$|[^A-Za-z0-9_-])`)
}

// waitingOn says whether who, a role whose last reply ends on ⏳, is waiting. It is when a bus waiter runs under one of
// its live claudes (its session sessionID, or one crius-hook files under who): the waiter wakes it (11-19: two roles
// waiting on a live waiter drew "its job may be gone" because the hold also asked for an item or purpose words). Then
// h says on what: an item named in lastReply or in its newest bus line that is in a heavy run, or that a line from
// another role names since who's newest line (since, the idle spell's start, when who has none; never more than
// t3WaitingFor before since) and does not say it landed (h.item); else words that say it has no work or name what it
// waits for (h.purpose), in its last reply's status part (statusPart), or (when the reply names no item) in its newest
// bus line from within purposeTurn before since (purposeIn; 11-18); else its bus alone (neither set). Three things end
// the hold, each a sign it should have moved: a line to who that its waiter should have woken it for, posted after its
// last reply and after the minute the waiter started (one before it the waiter read from its cursor, or the turn that
// started it had; 11-19); a line saying an item it waits on landed, posted after who's own newest line (in log order)
// and after the minute the waiter started; and a clock time it named to act at that passed (passedIn). why is "" when it holds; otherwise why not. unread is true when a check could not be read:
// unreadable is never waiting. A background job of its own that runs under its claude (ownJobOf) names what it
// is at (h.item) ahead of its bus, and holds it even when an item it waits on landed or a time it named passed (11-20:
// api-split's gate ran 50 min as its own Bash task and drew "its job may be gone").
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
	var started time.Time
	if h.waiter, started = waiterOf(ps, pids); h.waiter == "" {
		return h, "no bus waiter runs under its claude", false
	}
	lines, _, err := busLines(d.Home, who)
	if err != nil {
		return h, "could not read its bus: " + err.Error(), true
	}
	project := strings.SplitN(who, ":", 2)[0]
	var mine time.Time
	var mineText string
	mineAt := -1 // its newest line's place in the log: lines are in the order they were posted, stamps only to the minute
	for i, m := range lines {
		if busName(project, m[3]) != who {
			continue
		}
		if at, err := lineTime(m[1], d.Now); err == nil && !at.Before(mine) {
			mine, mineText, mineAt = at, m[5], i
		}
	}
	floor := mine // lines are stamped to the minute: one in the same minute as who's own counts as after it
	if floor.IsZero() {
		floor = since
	}
	if old := since.Add(-t3WaitingFor); floor.Before(old) {
		floor = old // a line days old says nothing of now
	}
	purposeLine := mineText // its words count for the reply only when posted in the turn that ended on it, or after
	if mine.Before(since.Add(-purposeTurn)) {
		purposeLine = ""
	}
	items := itemsIn(lastReply, mineText)
	// a line to who counts from its last reply: the turn that ended on ⏳ read what came before it. Not from who's own
	// newest line: one after the reply comes from a script, not a turn. Lines are stamped to the minute, so one in the
	// reply's minute counts (an alert too many, never one too few).
	toFloor := since.Truncate(time.Minute)
	// Nor from before its waiter started: a waiter reads from its cursor, so a live one has let every line since then
	// pass, and with no cursor the turn that started it came after the line. Here the trade-off goes the other way: one
	// in the waiter's first minute is read as before it (11-19: a VERIFIED line read in the turn that started the waiter
	// drew a false alert), so a waking line posted later in that minute is caught only by the t3WaitingFor alert. The
	// start is taken a second early: ps gives whole seconds.
	var waiterFloor time.Time // zero when its start is not known
	if !started.IsZero() {
		waiterFloor = started.Add(-time.Second).Truncate(time.Minute).Add(time.Minute)
	}
	if waiterFloor.After(toFloor) {
		toFloor = waiterFloor
	}
	for _, m := range lines {
		t, err := lineTime(m[1], d.Now)
		if err != nil || t.Before(toFloor) || m[3] == "crius:wakewatch" || busName(project, m[3]) == who {
			continue
		}
		if wakes(project, who, m[2], m[3], m[4], m[5]) {
			return h, fmt.Sprintf("%s's %s line to it at %s came after its newest line, and its waiter has not moved it", m[3], m[2], t.Local().Format("15:04")), false
		}
	}
	// idle on purpose: an item in flight is named first; else words that say it has no work or name what it waits for,
	// in its reply's status part, or in its newest bus line (posted within purposeTurn of the reply) when the reply names
	// no item (a reply that took up an item says more of now than an "Idle" line)
	idleOnPurpose := func() bool {
		h.purpose = purposeIn(statusPart(lastReply), since, d.Now)
		if h.purpose == "" && len(itemsIn(lastReply)) == 0 {
			h.purpose = purposeIn(purposeLine, mine, d.Now) // a time in the line is read from when it was posted
		}
		return h.purpose != ""
	}
	// its own background job still runs under its claude: it is at work, whatever it named (11-20)
	ownJob := func() bool {
		pid, what, ok := ownJobOf(ps, pids)
		if ok {
			h.item, h.by = fmt.Sprintf("its own background job (pid %d)", pid), "a Bash task under its claude: "+what
		}
		return ok
	}
	const noJob = "; no background job of its own runs under its claude"
	// on its bus, unless it named a time to act that passed without moving it (review P2-2)
	onBus := func() (waitHold, string, bool) {
		h.named = strings.Join(items, ", ")
		c := passedIn(statusPart(lastReply), since, d.Now)
		if c == "" && len(itemsIn(lastReply)) == 0 {
			c = passedIn(purposeLine, mine, d.Now)
		}
		if c != "" {
			return h, fmt.Sprintf("the time it named passed over %d min ago without moving it (%q)", int(t3WaitAfter.Minutes()), c), false
		}
		return h, "", false
	}
	if len(items) == 0 {
		if idleOnPurpose() || ownJob() {
			return h, "", false
		}
		return onBus()
	}
	var landed []string
	for _, item := range items {
		if by, ok := heavyRunOf(ps, item); ok {
			h.item, h.by = item, by
			return h, "", false
		}
		var at time.Time
		var newest []string
		newestAt := -1
		for i, m := range lines {
			if m[3] == "crius:wakewatch" || busName(project, m[3]) == who || !mentions(m[5], item) {
				continue
			}
			if t, err := lineTime(m[1], d.Now); err == nil && !t.Before(floor) && !t.Before(at) {
				at, newest, newestAt = t, m, i
			}
		}
		switch {
		case newest == nil:
		// posted before its own newest line, or before its waiter started (a landed line to it woke the turn that
		// started the waiter; one to others is the turn's to have read): it knew (11-19, review P1-1, r2 P1-A). A role
		// whose reply still waits on it is then held up to t3WaitingFor, not t3WaitAfter.
		case landedTypes[newest[2]] && (newestAt < mineAt || at.Before(waiterFloor)):
		case landedTypes[newest[2]]:
			landed = append(landed, fmt.Sprintf("%s: %s posted %s at %s", item, newest[3], newest[2], at.Local().Format("15:04")))
		default:
			h.item, h.by = item, fmt.Sprintf("%s's %s line at %s", newest[3], newest[2], at.Local().Format("15:04"))
			return h, "", false
		}
	}
	if landed == nil { // an item it waits on that landed should have put it to work
		if idleOnPurpose() || ownJob() {
			return h, "", false
		}
		return onBus()
	}
	if ownJob() {
		return h, "", false
	}
	why = fmt.Sprintf("none of %s is in flight (no heavy-run.sh names it, and no other role's line names it since %s's newest)%s",
		strings.Join(items, ", "), who, noJob)
	if landed != nil {
		why += "; landed: " + strings.Join(landed, "; ")
	}
	return h, why, false
}

// Waiting prints, for each role named (project:role) or, with none, each T3 builder and each other role in a T3 thread whose
// last reply ends on ⏳ and that runs no turn, one line: WAITING ON <item> (<who>) with its waiter, or why the idle alert is
// not held; for a builder its verdict, and what Crius would be told (or was). It only reads, and sends nothing
// (crius-wakewatch waiting). Idle counts from the thread's last update; the park, WAIT, closed and finished
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
	code := 0
	if d.States != nil {
		var err error
		if states, err = d.States(); err != nil {
			fmt.Fprintln(out, "FAILED: could not read crius-hook's session records (a role's newer session may be missed):", err)
			code = 2
		}
	}
	crius, _ := CriusThread(d.Home)
	builders, err := readBuilders(d.Home)
	if err != nil && !os.IsNotExist(err) {
		fmt.Fprintln(out, "FAILED: could not read which roles are builders:", err)
		code = 2
	}
	var st state // a copy: what this run would know of each builder's stretch, never saved
	if b, err := os.ReadFile(filepath.Join(d.Home, "state", "wakewatch.json")); err == nil {
		err = json.Unmarshal(b, &st)
		if err != nil {
			fmt.Fprintln(out, "FAILED: could not read wakewatch's state (a builder's stretch counts from its thread's update time):", err)
			code = 2
		}
	} else if !os.IsNotExist(err) {
		fmt.Fprintln(out, "FAILED:", err)
		code = 2
	}
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
		if builders[who] && !running && !th.Settled && th.Pending == 0 && !notStarted[th.Status] {
			sp, key := st.builderSpell(who, id, th, time.Time{})
			idle := d.Now.Sub(sp.Since)
			look := builderLook{sesErr: sesErr, procs: readProcs, thread: d.Thread, now: d.Now, jobs: sp.Jobs,
				pids: func(id string) map[int]bool { return threadPids(ss, states, d.Home, id, r) }}
			v, what := look.verdict(id, th, 0)
			says := builderSays(who, th, idle, v, what, t3Reply(th))
			switch _, said := st.Typed[key]; {
			case says == "":
				says = "nothing to say now"
			case said:
				says = "already said in this stretch"
			default:
				says = "would say: " + says
			}
			fmt.Fprintf(out, "%s builder %s %d min (T3 thread %s, status %s): %s; %s\n", who, strings.ToUpper(v), int(idle.Minutes()), id, th.Status, what, says)
			continue
		}
		if all && builders[who] {
			fmt.Fprintf(out, "%s builder: runs a turn, waits on a request, or is settled (T3 thread %s, status %s, %d open requests)\n", who, id, th.Status, th.Pending)
			continue
		}
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
		if len(th.Background) > 0 { // t3Idle holds it on its own T3 background job before waitingOn
			fmt.Fprintf(out, "%s WAITING ON its own T3 background job(s) %s; idle %d min after ⏳, held until %d min (T3 thread %s)\n",
				who, strings.Join(th.Background, ", "), idle, int(t3WaitingFor.Minutes()), id)
			continue
		}
		h, why, unread := waitingOn(d, readProcs, ss, sesErr, states, who, r.SessionID, th.LastReply, th.Updated)
		if unread {
			code = 2
		}
		switch {
		case why == "" && h.purpose != "":
			fmt.Fprintf(out, "%s IDLE on purpose: %q, bus waiter %s; idle %d min after ⏳, said once after %d min (T3 thread %s)\n",
				who, h.purpose, h.waiter, idle, int(t3WaitAfter.Minutes()), id)
		case why == "" && h.item == "":
			fmt.Fprintf(out, "%s WAITING ON ITS BUS (%s), bus waiter %s; idle %d min after ⏳, held until %d min (T3 thread %s)\n",
				who, h.onBusText(), h.waiter, idle, int(t3WaitingFor.Minutes()), id)
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
