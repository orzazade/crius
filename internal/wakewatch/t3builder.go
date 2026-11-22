package wakewatch

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/sessionstate"
)

// t3UnknownAfter is how long a builder whose evidence cannot be read stays quiet: never taken as busy for longer.
const t3UnknownAfter = 20 * time.Minute

// readBuilders gives the T3 builders home/wakewatch-projects names: its entries with a colon (axi:api-split), beside the
// projects it watches. They are the threads builderVerdict judges; every other role in a T3 thread keeps t3Idle's holds
// (a QA role waits for others on purpose: 11-20 to 11-22 axi's two had 148 turns over 10 min apart). watched says a file it cannot read.
func readBuilders(home string) map[string]bool {
	b, _ := os.ReadFile(filepath.Join(home, "wakewatch-projects"))
	out := names(b)
	maps.DeleteFunc(out, func(n string, _ bool) bool { return !strings.Contains(n, ":") })
	return out
}

// t3Spell is a builder's idle stretch: the thread and run that ended, and when. It starts at the thread's update time as
// first seen after the run, and T3 moving that time later with no run is the same stretch (11-23: it moved 30 min after
// the run ended, and the second look at it was a second alert).
type t3Spell struct {
	Thread, Run string
	Since       time.Time
}

// builderSpell gives a builder's stretch, stored in st, and its alert key. A bus line that unparked it or ended its WAIT
// (after) starts it afresh. The keys of its earlier stretches go.
func (st *state) builderSpell(who, id string, th T3Thread, after time.Time) (time.Time, string) {
	run := th.Run
	if run == "" {
		run = th.Updated.UTC().Format(time.RFC3339Nano)
	}
	s := st.T3Spells[who]
	if s.Thread != id || s.Run != run {
		s = t3Spell{id, run, th.Updated}
	}
	if after.After(s.Since) {
		s.Since = after
	}
	if st.T3Spells == nil {
		st.T3Spells = map[string]t3Spell{}
	}
	st.T3Spells[who] = s
	key := builderKey(who) + id + ":" + s.Since.UTC().Format(time.RFC3339Nano)
	maps.DeleteFunc(st.Typed, func(k string, _ time.Time) bool { return strings.HasPrefix(k, builderKey(who)) && k != key })
	return s.Since, key
}

// builderKeys begins the alert key of every builder: said once per stretch, however long it lasts (Run never ages it out).
const builderKeys = "t3idle:builder:"

func builderKey(who string) string { return builderKeys + who + ":" }

// dropBuilders forgets the stretches of roles that are builders no more.
func (st *state) dropBuilders(builders map[string]bool) {
	for who := range st.T3Spells {
		if !builders[who] {
			delete(st.T3Spells, who)
			maps.DeleteFunc(st.Typed, func(k string, _ time.Time) bool { return strings.HasPrefix(k, builderKey(who)) })
		}
	}
}

// passThrough are programs that only run another: a builder's job is what they run.
var passThrough = map[string]bool{"env": true, "nice": true, "nohup": true, "time": true, "timeout": true, "gtimeout": true, "caffeinate": true}

// shellRuns is what is alive in one background shell of a claude.
type shellRuns struct {
	waiters, jobs, tails []Proc
}

// runsIn sorts what is alive at p and under it: bus waiters (isWaiter), tails of a file that is no bus log, and jobs. A
// shell, a script a shell runs and a passThrough count as what runs under them, and a sleep or a filter (waiterSteps,
// waiterHelpers under a waiter) as nothing: a script that only sleeps now shows no work.
func runsIn(kids map[int][]Proc, p Proc) (r shellRuns) {
	var walk func(p Proc, helpers map[string]bool, depth int)
	walk = func(p Proc, helpers map[string]bool, depth int) {
		w := strings.Fields(p.Command)
		if len(w) == 0 || depth > 64 || w[0] == "<defunct>" || strings.HasPrefix(w[0], "(") { // (bash): ps's name of one exiting
			return
		}
		switch prog := progOf(w); {
		case isWaiter(w):
			r.waiters, helpers = append(r.waiters, p), waiterHelpers
		case prog == "tail" && !stdinTail(w):
			r.tails = append(r.tails, p)
		case jobShells[filepath.Base(w[0])] || passThrough[prog] || helpers[prog] || prog == "tail":
		default:
			r.jobs = append(r.jobs, p)
			return
		}
		for _, k := range kids[p.PID] {
			walk(k, helpers, depth+1)
		}
	}
	walk(p, waiterSteps, 0)
	return r
}

// builderVerdict says from what runs whether a builder with no open run works: "busy", "idle" or "unknown", and what it
// saw. T3 shows such a thread as "waiting" while it awaits a background job (t3_background_wait) or a subagent and as
// "completed" otherwise, and wakes it when one ends: the status says nothing of what the job is (11-23: api-split and qa
// "waited" on their bus waiters alone). So it looks at the shells under the thread's own claude (pids, sesErr when they
// are not known) and at its subagents' threads:
//   - busy: a job runs in a shell (a test run, a gate, the heavy queue), or a subagent runs a turn or moved within
//     t3IdleAfter (one has no process of its own);
//   - idle: nothing runs, or bus waiters alone;
//   - unknown: anything else. A shell that only sleeps or tails a log watches something else, which may be dead; a job T3
//     lists that no shell runs may have died; a list could not be read.
func builderVerdict(pids map[int]bool, sesErr error, procs func() ([]Proc, error), child func(id string) (T3Thread, error), now time.Time, th T3Thread) (verdict, what string) {
	var unknown, waiters []string
	shells, commands := 0, 0
	for _, t := range th.Tasks {
		if t.Kind != "subagent" {
			commands++
		}
	}
	ps, err := []Proc(nil), sesErr
	switch {
	case sesErr != nil:
		unknown = append(unknown, "could not list the live claude sessions: "+sesErr.Error())
	case len(pids) > 0:
		if ps, err = procs(); err != nil {
			unknown = append(unknown, "could not read the process list: "+err.Error())
		}
	case commands > 0:
		unknown = append(unknown, "no live claude of it was found")
	}
	kids := map[int][]Proc{}
	for _, p := range ps {
		kids[p.PPID] = append(kids[p.PPID], p)
	}
	for _, p := range ps {
		w := strings.Fields(p.Command)
		if !pids[p.PPID] || !strings.Contains(p.Command, bashTaskMark) && !isWaiter(w) {
			continue
		}
		shells++
		text, _, _ := strings.Cut(shellText(p.Command), "' < /dev/null")
		if r := []rune(text); len(r) > 100 {
			text = string(r[:100]) + "…"
		}
		r := runsIn(kids, p)
		switch {
		case len(r.jobs) > 0:
			j := r.jobs[0]
			return "busy", fmt.Sprintf("its own job runs (%.140s (pid %d))", j.Command, j.PID)
		case len(r.tails) > 0 || len(r.waiters) == 0:
			unknown = append(unknown, fmt.Sprintf("only a poll loop or a log watch runs in %q (pid %d)", text, p.PID))
		default:
			waiters = append(waiters, fmt.Sprintf("%s (pid %d)", progOf(strings.Fields(r.waiters[0].Command)), r.waiters[0].PID))
		}
	}
	if err == nil && len(pids) > 0 && shells < commands {
		unknown = append(unknown, fmt.Sprintf("T3 lists %d background jobs of its own, %d run (%s)", commands, shells, strings.Join(th.Background, ", ")))
	}
	for _, t := range th.Tasks {
		switch t.Kind {
		case "command", "monitor":
		case "subagent":
			c, err := child(t.Child)
			switch {
			case err != nil:
				unknown = append(unknown, fmt.Sprintf("its subagent %q (thread %s) could not be read: %v", t.Description, t.Child, err))
			case c.Status == "running" || c.Active || now.Sub(c.Updated) <= t3IdleAfter:
				return "busy", fmt.Sprintf("its subagent works (%s, thread %s)", t.Description, t.Child)
			default:
				unknown = append(unknown, fmt.Sprintf("its subagent %q (thread %s, status %s) has not moved for %d min", t.Description, t.Child, c.Status, int(now.Sub(c.Updated).Minutes())))
			}
		default:
			unknown = append(unknown, fmt.Sprintf("T3 lists a %q job (%s)", t.Kind, t.Description))
		}
	}
	slices.Sort(waiters)
	switch {
	case len(unknown) > 0:
		return "unknown", strings.Join(append(unknown, waiters...), "; ")
	case len(waiters) > 0:
		return "idle", "only its bus waiter runs (" + strings.Join(waiters, ", ") + ")"
	}
	return "idle", "no background job runs"
}

// builderSays is what Crius hears of a builder idle this long, "" while it is quiet. One that works is asked about past
// t3WaitingFor: a live process may hang.
func builderSays(name string, th T3Thread, idle time.Duration, verdict, what, tail string) string {
	min := int(idle.Minutes())
	if th.Unanswered {
		what += "; a message to it after its last reply has no answer"
	}
	switch {
	case th.Pending > 0 && idle > t3IdleAfter:
		return fmt.Sprintf("%s waits on %d open request(s), an approval or a question, idle %d min. %s", name, th.Pending, min, tail)
	case verdict == "idle" && idle > t3IdleAfter:
		return fmt.Sprintf("%s is idle %d min (status %s): %s. Feed or wake it. %s", name, min, th.Status, what, tail)
	case verdict == "unknown" && idle > t3UnknownAfter:
		return fmt.Sprintf("%s is idle %d min (status %s) and wakewatch cannot tell whether it works: %s. Look at it. %s", name, min, th.Status, what, tail)
	case verdict == "busy" && idle > t3WaitingFor:
		return fmt.Sprintf("%s is idle %d min (status %s), over %d h, and wakewatch cannot tell whether its job still moves: %s. Look at it. %s",
			name, min, th.Status, int(t3WaitingFor.Hours()), what, tail)
	}
	return ""
}

// threadPids gives the claudes that run T3 thread id: the sessions crius-hook files under it, and the role's current one
// when its thread is id. Another claude of the same role (an older session) is not among them.
func threadPids(ss []Session, states []sessionstate.State, home, id string, r sessionstate.State) map[int]bool {
	pids := map[int]bool{}
	for _, s := range ss {
		mine := s.ID == r.SessionID && roleThread(home, r) == id ||
			slices.ContainsFunc(states, func(st sessionstate.State) bool { return st.SessionID == s.ID && st.Thread == id })
		if mine && s.PID > 0 {
			pids[s.PID] = true
		}
	}
	return pids
}

// missingBuilders names each builder t3Idle found no thread to watch for (seen holds those it did), or whose project is not
// watched, and that is not parked.
func missingBuilders(builders, seen map[string]bool, watch projects, park parked) (out [][2]string) {
	for _, who := range slices.Sorted(maps.Keys(builders)) {
		project, role, _ := strings.Cut(who, ":")
		if seen[who] || park.has(who, project) {
			continue
		}
		what := "it has no role file, or runs in no T3 thread of its own"
		if !watch.has(project) {
			what = "that list does not name its project " + project
		}
		out = append(out, [2]string{"t3-failed:" + who, fmt.Sprintf("the T3 thread idle check failed to look at %s · %s: wakewatch-projects named a builder, but %s", project, role, what)})
	}
	return out
}
