package wakewatch

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/orzazade/crius/internal/quota"
	"github.com/orzazade/crius/internal/sessionstate"
)

// CriusThreadFile is the file, under the Crius home, that holds the id of the T3 Code thread Crius runs in (one line,
// e.g. mcp:5bba4b79-…). While it exists Crius has no cmux tab: outbox sends its messages with `t3 thread send`, and the
// checks of a cmux tab titled crius are left out.
func CriusThreadFile(home string) string { return filepath.Join(home, "state", "crius", "t3-thread") }

// CriusThread gives the id in CriusThreadFile; "" with no error when there is no such file. A file that cannot be read,
// or holds no single id (empty, two words, two lines, or a word starting with "-" that t3 would read as a flag), is an
// error: the file says Crius has moved, so its messages must not go to a cmux tab.
func CriusThread(home string) (string, error) {
	path := CriusThreadFile(home)
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	f := strings.Fields(string(b))
	if len(f) != 1 || strings.HasPrefix(f[0], "-") {
		return "", fmt.Errorf("%s holds %q, not one T3 Code thread id", path, strings.TrimSpace(string(b)))
	}
	return f[0], nil
}

// T3Thread is what `t3 thread show --json` tells of a thread: its status ("running", "completed", …), whether a run is
// open (activeRunId), how many requests (an approval or a question) wait on someone, when it last changed, its newest
// assistant message ("" when the messages read hold none), whether a user message came after that reply, and whether
// the owner or Crius settled or archived it (T3's sign that nobody wants more from the thread). Child says T3 started it
// for a parent thread with delegate_task (relationshipToParent "subagent"): T3 hands its result to that parent.
// Background names the thread's own background jobs still running (pendingBackgroundTasks, "<taskId> (<description>)"):
// T3 wakes the thread when one ends. Tasks are the same jobs with their kind. Run names its latest run: a new one is a new
// turn, while Updated also moves with no turn (11-23: T3 moved it 30 min after a run ended).
type T3Thread struct {
	Status     string
	Active     bool
	Pending    int
	Updated    time.Time
	LastReply  string
	Unanswered bool
	Settled    bool
	Child      bool
	Background []string
	Tasks      []T3Task
	Run        string
}

// T3Task is one of a thread's background jobs in T3: Child is the thread of a subagent.
type T3Task struct{ ID, Kind, Description, Child string }

// ErrT3NoAnswer marks a t3 call that said nothing in its whole per-call limit: the T3 server hangs, so the check that
// made the call reads no more threads on this run.
var ErrT3NoAnswer = errors.New("t3 gave no answer in time")

// ErrT3BudgetSpent marks a read not made, or cut short, because slow t3 answers used up the run's time for t3. It is no
// hang, but that time is gone, so the check reads no more threads on this run either.
var ErrT3BudgetSpent = errors.New("not read on this run: slow t3 answers used up the run's time for t3")

// t3Stop says why a check reads no more threads on this run after a read failed with err, or "" when it reads on.
func t3Stop(err error) string {
	switch {
	case errors.Is(err, ErrT3BudgetSpent):
		return "not read: slow t3 answers used up this run's time for t3"
	case errors.Is(err, ErrT3NoAnswer):
		return "not read: t3 gave no answer for an earlier thread on this run"
	}
	return ""
}

const (
	t3IdleAfter = 10 * time.Minute // a thread whose last reply ends on neither ⏳ nor ❓ (✅ or none), or that waits on a request
	t3WaitAfter = 45 * time.Minute // a thread whose last reply ends on ⏳, with no bus line from its role in that time
	t3Tail      = 80               // how much of the last reply the alert quotes, in characters
)

// RoleThread is roleThread, for outbox: crius-say and crius-send find a role's thread as this watch does.
func RoleThread(home string, r sessionstate.State) string { return roleThread(home, r) }

// roleThread gives the T3 Code thread the role's current session runs in: from that session's own file, which crius-hook
// rewrites on every event, else from the role's file (as bus-post reads it); "" when it runs in none.
func roleThread(home string, r sessionstate.State) string {
	if cur, err := sessionstate.Read(filepath.Join(home, "sessions"), r.SessionID); err == nil && cur.Thread != "" {
		return cur.Thread
	}
	return r.Thread
}

// newestThread gives the T3 Code thread of the role's newest session, "" when that session runs in none (a cmux tab).
// The sessions are the role's current one (with roleThread's thread) and each one crius-hook filed under project:role
// (states), dated by their latest event. A role whose sessions moved to a new thread may still name an old one in its
// file (11-17: cortex:lead); an older session never speaks for the role, so one moved back to a tab is not watched here.
// A session crius-spawn -close or -fresh closed (any id in the mark) never speaks for the role either while the role's file names another one (11-20:
// axi qa-2's closed claude kept running after a new session started, so its thread was the newest and woke Crius as
// idle). When the role's file names the closed session, the whole role is closed: the callers deal with that.
func newestThread(home, project, role string, r sessionstate.State, states []sessionstate.State) string {
	type seen struct {
		thread string
		at     time.Time
	}
	// an unreadable mark closes nothing here; t3Idle and the dead-role check read it again and fail loud on it
	closed, _ := sessionstate.ClosedSessions(filepath.Join(home, "roles"), project, role)
	by := map[string]seen{}
	for _, s := range states {
		if s.Project == project && s.Role == role && (!slices.Contains(closed, s.SessionID) || s.SessionID == r.SessionID) {
			by[s.SessionID] = seen{s.Thread, s.Time}
		}
	}
	cur := seen{roleThread(home, r), r.Time} // the role's file is dated when it was filed
	if o, ok := by[r.SessionID]; ok {
		cur.at = o.at // the session's own file, rewritten on every event
	} else if c, err := sessionstate.Read(filepath.Join(home, "sessions"), r.SessionID); err == nil && !c.Time.IsZero() {
		cur.at = c.Time
	}
	by[r.SessionID] = cur
	var newest, best string
	var at time.Time
	for sid, s := range by {
		if newest == "" || s.at.After(at) || s.at.Equal(at) && sid < newest {
			newest, best, at = sid, s.thread, s.at
		}
	}
	return best
}

// t3Idle wakes Crius for each team member that runs as a T3 Code thread and has stopped while it still owes work
// (11-15: AXI api-split, ms-flow and qa sat idle 13 to 50 min, found by hand at 04:05 and 05:48; nothing watched a
// thread). It looks at each open role of a watched team whose newest session runs in a thread other than Crius's own,
// and leaves out, as the tab check does, a parked role, a lead whose goal is finished, and a role whose WAIT on the bus
// still runs, and a thread settled or archived in T3: the owner or Crius is done with it (11-17: an old Cortex lead
// thread, settled 13 h before, woke Crius as idle 758 min). A thread with no open run is idle since its last update (or since the line that unparked it or ended its
// WAIT, when later). It wakes Crius when:
//   - a request (an approval or a question) has waited on someone for over t3IdleAfter;
//   - a message to it after its last reply has no answer, after t3IdleAfter;
//   - its last reply's newest status emoji (RULES §3.5) is ⏳, its own named job, and neither the thread nor a bus line
//     from the role has moved for over t3WaitAfter, unless it is waiting: a background job of its own runs in T3
//     (pendingBackgroundTasks; T3 wakes it when the job ends), or a bus waiter runs under its claude and an item it named
//     is in flight, or its own background job runs under its claude (waitingOn). Either wakes Crius once past
//     t3WaitingFor, naming the job or the item; or it is idle on purpose (a bus waiter runs and it said it has no work
//     or named what it waits for), said once as IDLE, never as a job that may be gone;
//   - it is anything but ❓ (which waits on the owner), after t3IdleAfter.
//
// The key holds the idle spell's start, and Run says it once (Typed): one wake per idle spell. A role that cannot be
// read, or a thread t3 cannot show, is a loud "failed to look" alert of its own, said every 20 min while it lasts.
// A thread in prompted waits on a permission prompt stuckPrompts watches: its open requests are left to that check.
//
// A role wakewatch-projects names as a builder (readBuilders) is judged, once its run has ended, by what runs
// (builderLook.verdict), whatever its reply or a WAIT line says, once per stretch (builderSpell). Parked, closed and settled
// ones and one on an open request are treated as above; the marks, the 45 min, the WAIT and the other holds are not for it.
func t3Idle(d Deps, st *state, ss []Session, sesErr error, watch projects, park parked, prompted map[string]bool) [][2]string {
	if d.Thread == nil {
		return nil
	}
	dir := filepath.Join(d.Home, "roles")
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return [][2]string{{"t3-failed", "the T3 thread idle check failed to look: " + err.Error()}}
	}
	var out [][2]string
	crius, err := CriusThread(d.Home)
	if err != nil {
		out = append(out, [2]string{"t3-failed", "the T3 thread idle check failed to look: " + err.Error()})
	}
	var down accountsDown
	var done map[string]bool // read once a thread is idle
	var procs []Proc         // read once a role on ⏳ is quiet too long (waitingOn)
	var procErr error
	procsRead := false
	readProcs := func() ([]Proc, error) {
		if !procsRead {
			procsRead = true
			if d.Procs == nil {
				procErr = errors.New("no process list to read")
			} else {
				procs, procErr = d.Procs()
			}
		}
		return procs, procErr
	}
	var states []sessionstate.State
	statesRead := false
	stop := "" // why no more threads are read on this run
	builders, gone := st.knownBuilders(d.Home)
	out = append(out, gone...)
	seen := map[string]bool{} // the builders found
	child := func(id string) (T3Thread, error) {
		if stop != "" {
			return T3Thread{}, errors.New(stop)
		}
		c, err := d.Thread(id)
		stop = t3Stop(err)
		return c, err
	}
	for _, f := range files {
		project, role, _ := strings.Cut(strings.TrimSuffix(filepath.Base(f), ".json"), ".")
		who := project + ":" + role
		if !watch.has(project) || park.has(who, project) {
			continue
		}
		builder := builders[strings.ToLower(who)]
		failed := func(why string) {
			out = append(out, [2]string{"t3-failed:" + who, fmt.Sprintf("the T3 thread idle check failed to look at %s · %s: %s", project, role, why)})
		}
		r, err := sessionstate.ReadRole(dir, project, role)
		if err != nil {
			seen[strings.ToLower(who)] = true
			failed(err.Error())
			continue
		}
		if !statesRead { // its errors are readHooks' "states-failed" alert; the roles' own files still name a thread each
			if d.States != nil {
				states, _ = d.States()
			}
			statesRead = true
		}
		closed, closedErr := sessionstate.IsClosed(dir, project, role, r.SessionID)
		id := newestThread(d.Home, project, role, r, states)
		seen[strings.ToLower(who)] = id != "" && id != crius || closed
		if id == "" || id == crius {
			continue
		}
		if closedErr != nil {
			failed(closedErr.Error())
			continue
		} else if closed {
			continue
		}
		p, unparked, err := parkedOnBus(d.Home, who, d.Now)
		if err != nil {
			failed("its bus: " + err.Error())
			continue
		} else if p {
			continue
		}
		w, err := waitOnBus(d.Home, who, d.Now)
		if err != nil {
			failed("its bus: " + err.Error())
			continue
		} else if w.silent && !builder { // 11-15: ms-flow's WAIT on an event would have kept it quiet 12 h
			continue
		}
		if stop != "" {
			failed(stop)
			continue
		}
		th, err := d.Thread(id)
		if err != nil {
			if stop = t3Stop(err); errors.Is(err, ErrT3BudgetSpent) {
				failed(fmt.Sprintf("thread %s: %v", id, err)) // the thread is not at fault: no hint to close its role
			} else {
				failed(fmt.Sprintf("thread %s: %v (if the role ended on purpose: crius-spawn -close %s %s)", id, err, project, role))
			}
			continue
		}
		if th.Settled || finishedChild(th) && !builder {
			continue
		}
		name := fmt.Sprintf("%s · %s (T3 thread %s)", project, role, id)
		if (th.Status == "running" || th.Active) && th.Pending == 0 || th.Pending > 0 && prompted[id] {
			continue
		}
		if billingStop(th.LastReply) { // the account's failure, not the thread's: one line per account below
			down.add(r.Config, name, th)
			continue
		}
		if builder && th.Pending == 0 {
			if notStarted[th.Status] { // an open run has no idle stretch yet
				if n := d.Now.Sub(th.Updated); n > t3UnknownAfter {
					out = append(out, [2]string{"t3idle:" + who + ":" + id + ":" + th.Status + ":" + th.Run, fmt.Sprintf(
						"%s has a run %s for %d min that has not started. %s", name, th.Status, int(n.Minutes()), t3Reply(th))})
				}
				continue
			}
			sp, key := st.builderSpell(strings.ToLower(who), id, th, unparked)
			idle := d.Now.Sub(sp.Since)
			if idle <= t3IdleAfter {
				continue
			}
			look := builderLook{sesErr: sesErr, procs: readProcs, thread: child, now: d.Now, jobs: sp.Jobs,
				pids: func(id string) map[int]bool { return threadPids(ss, states, d.Home, id, r) }}
			v, what := look.verdict(id, th, 0)
			if says := builderSays(name, th, idle, v, what, t3Reply(th)); says != "" {
				out = append(out, [2]string{key, says})
			}
			continue
		}
		from := th.Updated
		for _, t := range []time.Time{unparked, w.from} { // a line that unparked it or ended its WAIT starts the spell afresh
			if t.After(from) {
				from = t
			}
		}
		if d.Now.Sub(from) <= t3IdleAfter {
			continue
		}
		if done == nil {
			done, _, _, _, _ = finishedLeads(d.Home) // with no projects.toml no lead is known to be finished: it is checked
		}
		if done[strings.ToLower(who)] {
			continue
		}
		tail := t3Reply(th)
		key := "t3idle:" + who + ":" + id + ":" + from.UTC().Format(time.RFC3339Nano)
		idle := int(d.Now.Sub(from).Minutes())
		switch mark := statusMark(th.LastReply); {
		case th.Pending > 0:
			out = append(out, [2]string{key, fmt.Sprintf("%s waits on %d open request(s), an approval or a question, idle %d min. %s", name, th.Pending, idle, tail)})
		case th.Unanswered:
			out = append(out, [2]string{key, fmt.Sprintf("%s is idle %d min: not running, and a message to it after its last reply has no answer. %s", name, idle, tail)})
		case mark == "❓":
		case mark == "⏳":
			last, _, err := lastFrom(d.Home, who, "", d.Now)
			if err != nil {
				failed("its bus: " + err.Error())
				continue
			}
			moved := from
			if last.After(moved) {
				moved = last
			}
			quiet := d.Now.Sub(moved)
			if quiet <= t3WaitAfter {
				continue
			}
			// 11-20: axi qa and qa-2 woke Crius 4 times as "job may be gone" while T3 ran their job. A task T3 still lists after
			// its process died (a T3 restart) moves the alert from 45 min to t3WaitingFor, which names it.
			if len(th.Background) > 0 {
				if quiet > t3WaitingFor {
					out = append(out, [2]string{key, fmt.Sprintf("%s is idle %d min after a ⏳ reply, WAITING ON its own T3 background job(s) %s, "+
						"and no bus line from it for %d min: over %d h, so check that the job still moves. %s",
						name, idle, strings.Join(th.Background, ", "), int(quiet.Minutes()), int(t3WaitingFor.Hours()), tail)})
				}
				continue
			}
			h, why, _ := waitingOn(d, readProcs, ss, sesErr, states, who, r.SessionID, th.LastReply, from)
			switch {
			case why == "" && h.purpose != "": // idle on purpose: said once per thread and purpose (Typed keeps it window+resend), not per spell
				out = append(out, [2]string{"t3idle:" + who + ":" + id + ":on-purpose:" + h.purpose, fmt.Sprintf(
					"%s is IDLE, waiting on purpose: %q, with its bus waiter %s live; idle %d min after a ⏳ reply, no bus line from it for %d min. %s",
					name, h.purpose, h.waiter, idle, int(quiet.Minutes()), tail)})
			case why == "" && quiet <= t3WaitingFor: // waiting on an item in flight or its bus, its waiter armed: not idle yet
			case why == "" && h.item == "":
				out = append(out, [2]string{key, fmt.Sprintf("%s is idle %d min after a ⏳ reply, WAITING ON ITS BUS (%s) with its bus waiter %s live, "+
					"and no bus line from it for %d min: over %d h, so check that its waiter still wakes it. %s",
					name, idle, h.onBusText(), h.waiter, int(quiet.Minutes()), int(t3WaitingFor.Hours()), tail)})
			case why == "":
				out = append(out, [2]string{key, fmt.Sprintf("%s is idle %d min after a ⏳ reply, WAITING ON %s (%s) with its bus waiter %s live, "+
					"and no bus line from it for %d min: over %d h, so check that %s still moves. %s",
					name, idle, h.item, h.by, h.waiter, int(quiet.Minutes()), int(t3WaitingFor.Hours()), h.item, tail)})
			default:
				out = append(out, [2]string{key, fmt.Sprintf("%s is idle %d min after a ⏳ reply, with no bus line from it for %d min: its job may be gone. Not held as waiting: %s. %s",
					name, idle, int(quiet.Minutes()), why, tail)})
			}
		default:
			out = append(out, [2]string{key, fmt.Sprintf("%s is idle %d min: not running, and its last reply ends on %s, not ⏳ or ❓. %s", name, idle, markName(mark), tail)})
		}
	}
	out = append(out, down.alerts(d.Home)...)
	return append(out, missingBuilders(builders, seen, watch, park)...)
}

// billingRe is the reply of a thread whose account cannot run: its subscription is unpaid (11-25: max3, payment past due).
var billingRe = regexp.MustCompile(`(?i)payment (is )?past due|can.?t use your Claude subscription`)

// billingStop says a reply is the billing failure itself, not a reply of the role that quotes it: a role's own reply
// ends on a status mark (RULES §3.5); the CLI's error has none.
func billingStop(reply string) bool {
	return billingRe.MatchString(reply) && statusMark(reply) == ""
}

// billingWords quotes the reply from its billing words on, at most 160 characters, white space folded.
func billingWords(reply string) string {
	r := []rune(strings.Join(strings.Fields(reply[billingRe.FindStringIndex(reply)[0]:]), " "))
	return string(r[:min(len(r), 160)])
}

// accountsDown gathers the threads that stopped on a billing failure, by the config folder their role runs under.
// FU-WAKEWATCH-BILLING (11-25 02:0x): 4 AXI threads on max3 woke Crius 4 times for one unpaid account.
type accountsDown map[string]*downAccount

type downAccount struct {
	names []string
	reply string
}

func (a *accountsDown) add(config, name string, th T3Thread) {
	if *a == nil {
		*a = accountsDown{}
	}
	x := (*a)[config]
	if x == nil {
		x = &downAccount{reply: th.LastReply}
		(*a)[config] = x
	}
	x.names = append(x.names, name)
}

// alerts gives one ACCOUNT-DOWN line per account, keyed by its folder alone: a thread that joins, moves or closes adds no
// line; Run says it again after window+resend while it lasts.
func (a accountsDown) alerts(home string) [][2]string {
	as, _ := quota.LoadAccounts(home) // the name is only a label: with no file the folder names the account
	var out [][2]string
	for _, config := range slices.Sorted(maps.Keys(a)) {
		x := a[config]
		acct := config
		if config == "" {
			acct = "an unknown config folder (its role file names none)"
		} else if row := as.Dir(config); row != nil {
			acct = row.Name + " (" + config + ")"
		}
		slices.Sort(x.names)
		out = append(out, [2]string{"t3idle:account-down:" + config, fmt.Sprintf(
			"ACCOUNT-DOWN %s: %d T3 thread(s) on it stopped on a billing failure (%q): %s. Its subscription cannot run: move its roles to an "+
				"account that can (crius-spawn -move <project> <role>) or close them, and take its row out of accounts.toml until it is paid. %s",
			acct, len(x.names), billingWords(x.reply), strings.Join(x.names, "; "), t3Reply(T3Thread{LastReply: x.reply}))})
	}
	return out
}

// t3Reply quotes the end of a thread's last reply for an alert.
func t3Reply(th T3Thread) string {
	reply := []rune(strings.Join(strings.Fields(th.LastReply), " "))
	if len(reply) == 0 {
		return "No reply in its latest messages."
	}
	return "Last reply: " + string(reply[max(0, len(reply)-t3Tail):])
}

// finishedChild says th is a child T3 started for a parent thread with delegate_task, whose run completed and that asks
// nothing: T3 handed its result to the parent, which follows it up, so the child is done, not idle (11-20 00:1x: the
// night delegate crius-runlog woke Crius as idle 13 min on its closing ✅). A child still running, one whose run failed,
// was interrupted or cancelled, one with an open request or an unanswered message, one with no reply in the messages
// read, and one that ends on ❓ or ⏳ is checked as any thread. A parent that is gone or settled no longer follows the
// result up; that is the parent's own idle check, not the child's.
func finishedChild(th T3Thread) bool {
	mark := statusMark(th.LastReply)
	return th.Child && th.Status == "completed" && !th.Active && th.Pending == 0 && !th.Unanswered &&
		strings.TrimSpace(th.LastReply) != "" && mark != "❓" && mark != "⏳"
}

// markName names the status mark a reply ends on for an alert: the mark, or "no status mark".
func markName(mark string) string {
	if mark == "" {
		return "no status mark"
	}
	return mark
}

// statusMark gives the newest of ❓, ✅ and ⏳ in text, "" when it has none.
func statusMark(text string) string {
	j := strings.LastIndexAny(text, "❓✅⏳")
	if j < 0 {
		return ""
	}
	r, _ := utf8.DecodeRuneInString(text[j:])
	return string(r)
}

// NewestThread is newestThread for crius-spawn: -resume and -move go to the thread wakewatch watches for the role.
func NewestThread(home, project, role string, r sessionstate.State, states []sessionstate.State) string {
	return newestThread(home, project, role, r, states)
}
