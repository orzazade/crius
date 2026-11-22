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
// projects it watches. Every other role in a T3 thread keeps t3Idle's holds (a QA role waits for others on purpose: 11-20
// to 11-22 axi's two had 148 waits over 10 min between turns).
func readBuilders(home string) (map[string]bool, error) {
	b, err := os.ReadFile(filepath.Join(home, "wakewatch-projects"))
	out := names(b)
	maps.DeleteFunc(out, func(n string, _ bool) bool { return !strings.Contains(n, ":") })
	return out, err
}

// knownBuilders is readBuilders for a run. A list that names none, or cannot be read, though the last run knew some, keeps
// them for a run (one bad read would end every stretch and say it again); on the next their rule is off, said once. The
// stretches of roles that are builders no more go.
func (st *state) knownBuilders(home string) (map[string]bool, [][2]string) {
	b, err := readBuilders(home)
	var gone [][2]string
	switch {
	case len(b) > 0:
		st.T3NoNames = 0
	case len(st.T3Builders) > 0:
		if st.T3NoNames++; st.T3NoNames < lookMisses {
			for _, who := range st.T3Builders {
				b[who] = true
			}
			return b, nil
		}
		why := "it names none"
		if err != nil {
			why = "it cannot be read: " + err.Error()
		}
		gone = [][2]string{{"projects-unknown:builders", "wakewatch-projects named the T3 builders " + strings.Join(st.T3Builders, ", ") +
			" and now " + why + ", so their idle rule is off"}}
	}
	st.T3Builders = slices.Sorted(maps.Keys(b))
	for k := range st.T3Spells {
		if who, _, _ := strings.Cut(k, " "); !b[who] {
			delete(st.T3Spells, k)
			maps.DeleteFunc(st.Typed, func(key string, _ time.Time) bool { return strings.HasPrefix(key, builderKeys+who+":") })
		}
	}
	return b, gone
}

// t3Spell is a builder's idle stretch in one thread: the run that ended, and when. It starts at the thread's update time
// as first seen after that run, and T3 moving that time later with no run is the same stretch (11-23: it moved 30 min
// after the run ended, and the second look at it was a second alert). Jobs is what the jobs of the thread, and of each
// delegate's thread under it, had used of the CPU when they were last seen to move.
type t3Spell struct {
	Run   string
	Since time.Time
	Jobs  map[string]jobCPU
}

// jobCPU is the CPU time of each process of a thread's jobs, and when it was read.
type jobCPU struct {
	Used map[int]time.Duration
	At   time.Time
}

// notStarted are the statuses of a thread whose latest run is open but not running yet.
var notStarted = map[string]bool{"queued": true, "preparing": true, "starting": true}

// builderSpell gives a builder's stretch in a thread whose run has ended, stored in st, and its alert key. A bus line that
// unparked it (after) starts it afresh. The keys of its earlier stretches in that thread go.
func (st *state) builderSpell(who, id string, th T3Thread, after time.Time) (t3Spell, string) {
	run := th.Run
	if run == "" {
		run = th.Updated.UTC().Format(time.RFC3339Nano)
	}
	s := st.T3Spells[who+" "+id]
	if s.Run != run {
		s = t3Spell{Run: run, Since: th.Updated, Jobs: map[string]jobCPU{}}
	}
	if s.Jobs == nil {
		s.Jobs = map[string]jobCPU{}
	}
	if after.After(s.Since) {
		s.Since = after
	}
	if st.T3Spells == nil {
		st.T3Spells = map[string]t3Spell{}
	}
	st.T3Spells[who+" "+id] = s
	mine := builderKeys + who + ":" + id + ":"
	key := mine + s.Since.UTC().Format(time.RFC3339Nano)
	maps.DeleteFunc(st.Typed, func(k string, _ time.Time) bool { return strings.HasPrefix(k, mine) && k != key })
	return s, key
}

// builderKeys begins the alert key of every builder: said once per stretch, however long it lasts (Run never ages it out).
const builderKeys = "t3idle:builder:"

// passThrough are programs that only run another: a builder's job is what they run.
var passThrough = map[string]bool{"env": true, "nice": true, "nohup": true, "time": true, "timeout": true, "gtimeout": true, "caffeinate": true}

// shellRuns is what is alive in one background shell of a claude.
type shellRuns struct {
	waiters, jobs, tails []Proc
}

// runsIn sorts what is alive at p and under it: bus waiters (isWaiter), other tails of a file, and jobs. A shell, a script
// a shell runs and a passThrough count as what runs under them, and a sleep or a filter (waiterSteps, waiterHelpers under
// a waiter) as nothing: a script that only sleeps now shows no work.
func runsIn(kids map[int][]Proc, p Proc) (r shellRuns) {
	var walk func(p Proc, helpers map[string]bool, depth int)
	walk = func(p Proc, helpers map[string]bool, depth int) {
		w := strings.Fields(p.Command)
		if len(w) > 2 && filepath.Base(w[0]) == "bun" && w[1] == "run" { // bun run bus-watch.ts
			w = slices.Delete(w, 1, 2)
		}
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

// builderLook is what verdict looks at a builder with: the claude sessions (pids gives those of a thread, sesErr why they
// are not known), the process list, the threads of its subagents, and the CPU its jobs had used (jobs, by thread).
type builderLook struct {
	sesErr error
	procs  func() ([]Proc, error)
	pids   func(thread string) map[int]bool
	thread func(id string) (T3Thread, error)
	now    time.Time
	jobs   map[string]jobCPU
}

// jobMoves is the CPU time the jobs of a thread use at least, all told, in t3UnknownAfter, or they may hang.
const jobMoves = time.Second

// still is how long the jobs of thread id, run by shells, have not moved: used jobMoves of CPU, started in a new shell, or
// been seen for the first time.
func (l builderLook) still(id string, shells []int, used map[int]time.Duration) time.Duration {
	last, ok := l.jobs[id]
	var sum time.Duration
	for pid, cpu := range used {
		if was, ok := last.Used[pid]; ok && was <= cpu {
			cpu -= was
		}
		sum += cpu
	}
	fresh := slices.ContainsFunc(shells, func(pid int) bool { _, was := last.Used[pid]; return !was })
	if !ok || fresh || sum >= jobMoves {
		l.jobs[id] = jobCPU{used, l.now}
		return 0
	}
	return l.now.Sub(last.At)
}

// verdict says from what runs whether a builder's thread with no open run works: "busy", "idle" or "unknown", and what it
// saw. T3 shows such a thread as "waiting" while it awaits a background job (t3_background_wait) or a subagent and as
// "completed" otherwise, and wakes it when one ends: the status says nothing of what the job is (11-23: api-split and qa
// "waited" on their bus waiters alone). So it looks at the shells under the thread's own claude and at its subagents'
// threads:
//   - busy: a job in a shell (a test run, a gate) that used CPU within t3UnknownAfter, or a subagent that runs a turn, moved
//     within t3IdleAfter (a native one has no process of its own) or waits on such a job of its own;
//   - idle: nothing runs, or bus waiters alone;
//   - unknown: anything else. A shell that only sleeps or tails a log watches something else, which may be dead; a job
//     that uses no CPU may hang, or be a server left on (or wait on another machine); a job T3 lists that no shell runs
//     may have died; a subagent waits on a request; a list could not be read.
//
// depth is 1 for a delegate's thread: one level of subagents is followed.
func (l builderLook) verdict(id string, th T3Thread, depth int) (verdict, what string) {
	var unknown, waiters []string
	pids := l.pids(id)
	shells, commands := 0, 0
	for _, t := range th.Tasks {
		if t.Kind != "subagent" {
			commands++
		}
	}
	ps, err := []Proc(nil), l.sesErr
	switch {
	case l.sesErr != nil:
		unknown = append(unknown, "could not list the live claude sessions: "+l.sesErr.Error())
	case len(pids) > 0:
		if ps, err = l.procs(); err != nil {
			unknown = append(unknown, "could not read the process list: "+err.Error())
		}
	case commands > 0:
		unknown = append(unknown, "no live claude of it was found, though T3 lists "+strings.Join(th.Background, ", "))
	}
	kids := map[int][]Proc{}
	for _, p := range ps {
		kids[p.PPID] = append(kids[p.PPID], p)
	}
	job, used, jobShellPids := "", map[int]time.Duration{}, []int(nil)
	var use func(p Proc, depth int)
	use = func(p Proc, depth int) {
		used[p.PID] = p.CPU
		for _, k := range kids[p.PID] {
			if depth < 64 {
				use(k, depth+1)
			}
		}
	}
	for _, p := range ps {
		if !pids[p.PPID] || !strings.Contains(p.Command, bashTaskMark) && !isWaiter(strings.Fields(p.Command)) {
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
			used[p.PID], jobShellPids = p.CPU, append(jobShellPids, p.PID)
			for _, j := range r.jobs {
				use(j, 0)
			}
			job = fmt.Sprintf("%.140s (pid %d)", r.jobs[0].Command, r.jobs[0].PID)
		case len(r.tails) > 0 || len(r.waiters) == 0:
			unknown = append(unknown, fmt.Sprintf("only a poll loop or a log watch runs in %q (pid %d)", text, p.PID))
		default:
			waiters = append(waiters, fmt.Sprintf("%s (pid %d)", progOf(strings.Fields(r.waiters[0].Command)), r.waiters[0].PID))
		}
	}
	if job != "" {
		still := l.still(id, jobShellPids, used)
		if still <= t3UnknownAfter {
			return "busy", "its own job runs (" + job + ")"
		}
		unknown = append(unknown, fmt.Sprintf("its own job has used no CPU for %d min (%s)", int(still.Minutes()), job))
	}
	if err == nil && len(pids) > 0 && shells < commands {
		unknown = append(unknown, fmt.Sprintf("T3 lists %d background jobs of its own, %d run (%s)", commands, shells, strings.Join(th.Background, ", ")))
	}
	for _, t := range th.Tasks {
		switch t.Kind {
		case "command", "monitor":
		case "subagent":
			name := fmt.Sprintf("its subagent %q (thread %s)", t.Description, t.Child)
			c, err := l.thread(t.Child)
			switch {
			case err != nil:
				unknown = append(unknown, fmt.Sprintf("%s could not be read: %v", name, err))
			case c.Pending > 0:
				unknown = append(unknown, fmt.Sprintf("%s waits on %d open request(s)", name, c.Pending))
			case c.Status == "running" || c.Active || l.now.Sub(c.Updated) <= t3IdleAfter:
				return "busy", name + " works"
			default:
				at := fmt.Sprintf("%s, status %s, has not moved for %d min", name, c.Status, int(l.now.Sub(c.Updated).Minutes()))
				if depth == 0 && c.Status == "waiting" {
					v, w := l.verdict(t.Child, c, 1)
					if v == "busy" {
						return "busy", name + " works: " + w
					}
					at += ": " + w
				}
				unknown = append(unknown, at)
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
	case len(pids) == 0:
		return "idle", "no background job is listed in T3, and no live claude of it was found"
	}
	return "idle", "no background job runs"
}

// brokenRun are the statuses of a run that did not end well: nobody goes on with it, whatever still runs.
var brokenRun = map[string]bool{"failed": true, "interrupted": true, "cancelled": true}

// builderSays is what Crius hears of a builder idle this long, "" while it is quiet. One that works is asked about past
// t3WaitingFor, and at t3IdleAfter when its run broke or a message to it has no answer.
func builderSays(name string, th T3Thread, idle time.Duration, verdict, what, tail string) string {
	min := int(idle.Minutes())
	if th.Unanswered {
		what = "a message to it after its last reply has no answer; " + what
	}
	switch {
	case idle <= t3IdleAfter:
		return ""
	case verdict == "idle", th.Unanswered, brokenRun[th.Status]:
		return fmt.Sprintf("%s is idle %d min (status %s): %s. Feed or wake it. %s", name, min, th.Status, what, tail)
	case verdict == "unknown" && idle > t3UnknownAfter:
		return fmt.Sprintf("%s is idle %d min (status %s) and wakewatch cannot tell whether it works: %s. Look at it. %s", name, min, th.Status, what, tail)
	case verdict == "busy" && idle > t3WaitingFor:
		return fmt.Sprintf("%s is idle %d min (status %s), over %d h, and wakewatch cannot tell whether its job is still the right one: %s. Look at it. %s",
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
