package wakewatch

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/sessionstate"
	"github.com/orzazade/crius/internal/testutil"
)

// The review of 22f4c1c: what one rule for T3 builders must still get right.

func setThread(f *fake, id string, fn func(*T3Thread)) {
	th := f.threads[id]
	fn(&th)
	f.threads[id] = th
}

// every gives the minutes from..to, 5 apart.
func every(from, to int) (at []time.Duration) {
	for m := from; m <= to; m += 5 {
		at = append(at, time.Duration(m)*time.Minute)
	}
	return at
}

// P1-1: an approval asked inside a run is no idle stretch, so the stretch after that run is still said, and from its end.
func TestAStretchStartsWhenTheRunEndsNotAtARequestInsideIt(t *testing.T) {
	for name, c := range map[string]struct {
		asked int // minutes the request waited
		want  int // alerts before the run ended
	}{"a request said at 12 min": {15, 1}, "a request answered at 4 min": {4, 0}} {
		t.Run(name, func(t *testing.T) {
			home := builderHome(t)
			f := builderFake(0, "waiting", mergeReply, nil)
			setThread(f, "th-api", func(x *T3Thread) { x.Active, x.Pending = true, 1 })
			for m := 0; m <= c.asked; m += 4 {
				runOnce(t, home, f, t0.Add(time.Duration(m)*time.Minute))
			}
			setThread(f, "th-api", func(x *T3Thread) { x.Pending, x.Status = 0, "running" })
			wantWakes(t, home, f, c.want, every(20, 75)...)
			setThread(f, "th-api", func(x *T3Thread) {
				x.Active, x.Status, x.Updated, x.LastReply = false, "completed", t0.Add(76*time.Minute), doneReply
			})
			wantWakes(t, home, f, c.want, 77*time.Minute, 85*time.Minute) // 9 min
			has(t, wantWakes(t, home, f, c.want+1, every(87, 180)...), "idle 11 min", "nothing left to build")
		})
	}
}

// A run that is open but never starts is said once, and the stretch after it still is.
func TestARunThatNeverStartsIsSaidApartFromTheStretch(t *testing.T) {
	home := builderHome(t)
	f := builderFake(0, "queued", doneReply, nil)
	wantWakes(t, home, f, 0, every(0, 20)...)
	has(t, wantWakes(t, home, f, 1, every(25, 60)...), "quotes · api", "queued for 25 min", "has not started")
	setThread(f, "th-api", func(x *T3Thread) { x.Status, x.Updated = "completed", t0.Add(65*time.Minute) })
	has(t, wantWakes(t, home, f, 2, every(70, 120)...), "idle 15 min")
}

// P2-1: one bad read of the list neither says a stretch again nor passes for "no builders".
func TestOneBadReadOfTheBuilderListSaysNothingTwice(t *testing.T) {
	home := builderHome(t)
	list := filepath.Join(home, "wakewatch-projects")
	f := builderFake(11*time.Minute, "completed", doneReply, nil)
	wantWakes(t, home, f, 1, 0)
	testutil.Write(t, list, "quotes\n")
	wantWakes(t, home, f, 1, 5*time.Minute)
	testutil.Write(t, list, "quotes\nquotes:api\n")
	wantWakes(t, home, f, 1, every(10, 60)...)
	if w := strings.Join(f.sent, "\n"); strings.Contains(w, "idle rule is off") {
		t.Errorf("one bad read was said: %q", w)
	}
}

func TestAListThatStopsNamingBuildersIsSaidOnce(t *testing.T) {
	home := builderHome(t)
	f := builderFake(0, "running", doneReply, nil)
	setThread(f, "th-api", func(x *T3Thread) { x.Active = true })
	runOnce(t, home, f, t0)
	testutil.Write(t, filepath.Join(home, "wakewatch-projects"), "quotes\n")
	for _, d := range every(5, 120) {
		runOnce(t, home, f, t0.Add(d))
	}
	if w := strings.Join(f.sent, "\n"); strings.Count(w, "idle rule is off") != 1 || !strings.Contains(w, "quotes:api") {
		t.Errorf("want the lost list said once, got %q", w)
	}
}

// A role that is a builder no more keeps nothing: named again, its stretch is a new look.
func TestADroppedBuilderKeepsNoStretch(t *testing.T) {
	home := builderHome(t)
	list := filepath.Join(home, "wakewatch-projects")
	f := builderFake(11*time.Minute, "completed", doneReply, nil)
	wantWakes(t, home, f, 1, 0)
	testutil.Write(t, list, "quotes quotes:other")
	runOnce(t, home, f, t0.Add(5*time.Minute))
	testutil.Write(t, list, "quotes quotes:api")
	n := len(t3Wakes(f, "Feed or wake it"))
	runOnce(t, home, f, t0.Add(40*time.Minute))
	if m := len(t3Wakes(f, "Feed or wake it")); m != n+1 {
		t.Errorf("want the stretch said again once the role is a builder again, got %d more: %q", m-n, f.sent)
	}
}

// P2-2, P2-3: a subagent that waits on a person is no work; one that waits on its own job is judged by that job.
func TestASubagentThatWaitsIsJudgedByWhatItWaitsOn(t *testing.T) {
	task := []T3Task{{ID: "aa5d", Kind: "subagent", Description: "Build F5", Child: "th-child"}}
	start := func(child T3Thread, procs ...Proc) (string, *fake) {
		f := builderFake(0, "waiting", mergeReply, task, busWaiterShell)
		f.threads["th-child"] = child
		f.states = []sessionstate.State{{SessionID: "s-child", Thread: "th-child"}}
		f.ses = append(f.ses, Session{ID: "s-child", PID: 200, Status: "idle"})
		f.procs = append(append(f.procs, Proc{PID: 200, PPID: 1, Command: "claude"}), procs...)
		return builderHome(t), f
	}
	t.Run("a request, though it moved now", func(t *testing.T) {
		home, f := start(T3Thread{Status: "running", Active: true, Pending: 1, Updated: t0.Add(25 * time.Minute)})
		has(t, wantWakes(t, home, f, 1, every(10, 40)...), "Build F5", "waits on 1 open request", "idle 25 min")
	})
	waiting := T3Thread{Status: "waiting", Updated: t0.Add(-time.Hour), Tasks: []T3Task{gateTask}, Background: []string{"gate"}}
	t.Run("its own gate", func(t *testing.T) {
		home, f := start(waiting, Proc{PID: 220, PPID: 200, Command: snap + "go test ./...'"}, Proc{PID: 221, PPID: 220, Command: "go test ./..."})
		for _, d := range every(0, 60) {
			f.procs[len(f.procs)-1].CPU += 3 * time.Second
			runOnce(t, home, f, t0.Add(d))
		}
		wantWakes(t, home, f, 0)
	})
	t.Run("its own bus waiter", func(t *testing.T) {
		home, f := start(waiting, Proc{PID: 220, PPID: 200, Command: snap + "crius-wait x'"}, Proc{PID: 221, PPID: 220, Command: "bash /h/bin/crius-wait x"})
		has(t, wantWakes(t, home, f, 1, every(0, 60)...), "idle 25 min", "Build F5", "status waiting", "only its bus waiter runs")
	})
	t.Run("a subagent of its own", func(t *testing.T) { // one level is followed
		loop := waiting
		loop.Tasks = []T3Task{{ID: "x", Kind: "subagent", Description: "Deeper", Child: "th-child"}}
		home, f := start(loop)
		has(t, wantWakes(t, home, f, 1, every(0, 60)...), "Deeper", "cannot tell")
	})
}

// P2-4: a job that uses no CPU for 20 min is no proof of work; one that uses some, or a new one, is.
func TestAJobThatUsesNoCPUIsUnknown(t *testing.T) {
	f := builderFake(0, "waiting", mergeReply, []T3Task{waiterTask, gateTask}, busWaiterShell, gateShell)
	f.procs[len(f.procs)-1].CPU = 40 * time.Second // used before it hung
	has(t, wantWakes(t, builderHome(t), f, 1, every(0, 110)...), "cannot tell", "used no CPU for", "heavy.ts", "pid 121")

	t.Run("a new job in a new shell", func(t *testing.T) {
		home := builderHome(t)
		f := builderFake(0, "waiting", mergeReply, []T3Task{waiterTask, gateTask}, busWaiterShell, gateShell)
		wantWakes(t, home, f, 0, every(0, 30)...)
		for i := range f.procs {
			if f.procs[i].PID == 120 || f.procs[i].PPID == 120 {
				f.procs[i].PID, f.procs[i].PPID = f.procs[i].PID+20, map[int]int{100: 100, 120: 140}[f.procs[i].PPID]
			}
		}
		wantWakes(t, home, f, 0, every(35, 55)...)
	})
	t.Run("work in a child of its job", func(t *testing.T) {
		home := builderHome(t)
		f := builderFake(0, "waiting", mergeReply, []T3Task{waiterTask, gateTask}, busWaiterShell, gateShell, []Proc{{PID: 122, PPID: 121, Command: "dotnet test"}})
		for _, d := range every(0, 110) {
			f.procs[len(f.procs)-1].CPU += 400 * time.Millisecond // under a second a look, over one in 20 min
			runOnce(t, home, f, t0.Add(d))
		}
		wantWakes(t, home, f, 0)
	})
}

func TestABunRunBusWatchIsAWaiter(t *testing.T) {
	f := builderFake(11*time.Minute, "waiting", mergeReply, []T3Task{waiterTask},
		[]Proc{{PID: 110, PPID: 100, Command: snap + "bun run /h/bus-watch.ts'"}, {PID: 111, PPID: 110, Command: "bun run /h/bus-watch.ts"}})
	has(t, wantWakes(t, builderHome(t), f, 1, 0), "only its bus waiter runs")
}

// P2-5: nobody goes on with a run that broke, or a message with no answer, whatever runs.
func TestABrokenRunOrAnUnansweredMessageIsSaidThoughAJobRuns(t *testing.T) {
	for _, status := range []string{"failed", "interrupted", "cancelled"} {
		f := builderFake(11*time.Minute, status, mergeReply, []T3Task{gateTask}, gateShell)
		has(t, wantWakes(t, builderHome(t), f, 1, every(0, 60)...), "idle 11 min", "status "+status, "heavy.ts")
	}
	f := builderFake(11*time.Minute, "waiting", mergeReply, []T3Task{gateTask}, gateShell)
	setThread(f, "th-api", func(x *T3Thread) { x.Unanswered = true })
	has(t, wantWakes(t, builderHome(t), f, 1, every(0, 60)...), "idle 11 min", "has no answer", "heavy.ts")
}

// P2-6: a WAIT line is a builder's own word: it holds nothing. A park does, and the line that ends it starts a stretch.
func TestABuildersWAITHoldsNothingAndAParkDoes(t *testing.T) {
	home := builderHome(t)
	bus := filepath.Join(home, "bus", "quotes.log")
	testutil.Write(t, bus, stamp(t0.Add(-31*time.Minute))+" WAIT quotes:api -> quotes:lead — waiting for ms-flow's merge\n")
	f := builderFake(30*time.Minute, "completed", doneReply, nil)
	has(t, wantWakes(t, home, f, 1, every(0, 30)...), "idle 30 min")

	home = builderHome(t)
	bus = filepath.Join(home, "bus", "quotes.log")
	park := stamp(t0.Add(-31*time.Minute)) + " PARKED quotes:lead -> quotes:api — Park now, nothing to do until the owner answers.\n"
	testutil.Write(t, bus, park)
	f = builderFake(30*time.Minute, "completed", doneReply, nil)
	wantWakes(t, home, f, 0, every(0, 30)...)
	testutil.Write(t, bus, park+stamp(t0.Add(31*time.Minute))+" ORDER quotes:lead -> quotes:api — Build item 4 now.\n")
	wantWakes(t, home, f, 0, 35*time.Minute, 40*time.Minute)
	has(t, wantWakes(t, home, f, 1, every(45, 90)...), "idle 14 min")
}

// P2-7: a role seen in thread A, then B, then A again says each stretch once; its keys do not pile up.
func TestARoleThatFlapsBetweenTwoThreadsSaysEachStretchOnce(t *testing.T) {
	home := builderHome(t)
	f := builderFake(11*time.Minute, "completed", doneReply, nil)
	f.threads["th-b"] = T3Thread{Status: "completed", Run: "run:b", Updated: t0.Add(-15 * time.Minute), LastReply: "B is done. ✅"}
	in := func(th string, d time.Duration) {
		testutil.Write(t, filepath.Join(home, "sessions", "s-api.json"), `{"session_id":"s-api","t3_thread":"`+th+`"}`)
		runOnce(t, home, f, t0.Add(d))
	}
	for i, th := range []string{"th-api", "th-b", "th-api", "th-b", "th-api"} {
		in(th, time.Duration(5*i)*time.Minute)
	}
	w := wantWakes(t, home, f, 2)
	has(t, w, "th-b", "idle 20 min", "B is done")
}

// A finished run's stretch makes way for the next one's: one key per thread.
func TestABuildersOldStretchKeysGo(t *testing.T) {
	home := builderHome(t)
	f := builderFake(11*time.Minute, "completed", doneReply, nil)
	runOnce(t, home, f, t0)
	setThread(f, "th-api", func(x *T3Thread) { x.Run, x.Updated = "run:2", t0.Add(30*time.Minute) })
	runOnce(t, home, f, t0.Add(45*time.Minute))
	if n := strings.Count(testutil.Read(t, filepath.Join(home, "state", "wakewatch.json")), `"t3idle:builder:quotes:api:th-api:`); n < 1 {
		t.Fatal("no key of the stretch is in the state: this test looks at nothing")
	}
	if s := testutil.Read(t, filepath.Join(home, "state", "wakewatch.json")); strings.Contains(s, "t3idle:builder:quotes:api:th-api:"+t0.Add(-11*time.Minute).UTC().Format(time.RFC3339)) {
		t.Errorf("the first stretch's key is still kept: %s", s)
	}
}

// P3-1: a tab that cannot be typed into gets its alert on the bus, once.
func TestABuilderAlertThatWentToTheBusIsNotSentAgain(t *testing.T) {
	home := builderHome(t)
	f := builderFake(11*time.Minute, "completed", doneReply, nil)
	f.sendErr = errors.New("no tab")
	for _, d := range every(0, 90) {
		_ = Run(f.deps(home, t0.Add(d))) // it says it cannot send
	}
	n := 0
	for _, s := range f.tried {
		if strings.Contains(s, "Feed or wake it") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("want the stretch tried once, got %d: %q", n, f.tried)
	}
}

// P2-8: what else tells the cases apart.
func TestWhatABuildersShellsShow(t *testing.T) {
	waiter := func(kid string) []Proc {
		return append(append([]Proc{}, busWaiterShell...), Proc{PID: 116, PPID: 111, Command: kid})
	}
	tailShell := []Proc{{PID: 150, PPID: 100, Command: snap + "tail -f /h/run.log'"}, {PID: 151, PPID: 150, Command: "tail -f /h/run.log"}}
	for name, c := range map[string]struct {
		f    *fake
		want []string
	}{
		"a waiter's own helper is no job": {builderFake(25*time.Minute, "waiting", mergeReply, []T3Task{waiterTask}, waiter("jq -r .x")), []string{"only its bus waiter runs", "Feed"}},
		"a log watch alone":               {builderFake(25*time.Minute, "waiting", mergeReply, []T3Task{pollTask}, tailShell), []string{"cannot tell", "log watch"}},
		"a log watch beside a waiter":     {builderFake(25*time.Minute, "waiting", mergeReply, []T3Task{waiterTask, pollTask}, busWaiterShell, tailShell), []string{"cannot tell", "log watch", "team-wait.sh (pid 111)"}},
		"a log watch in its waiter's shell": {builderFake(25*time.Minute, "waiting", mergeReply, []T3Task{waiterTask}, busWaiterShell,
			[]Proc{{PID: 117, PPID: 110, Command: "tail -f /h/run.log"}}), []string{"cannot tell", "log watch"}},
		"a monitor":            {builderFake(25*time.Minute, "waiting", mergeReply, []T3Task{{ID: "m", Kind: "monitor", Description: "Watch the bus"}}, busWaiterShell), []string{"only its bus waiter runs", "Feed"}},
		"no claude and no job": {func() *fake { f := builderFake(25*time.Minute, "completed", doneReply, nil); f.ses = nil; return f }(), []string{"no live claude of it was found", "Feed"}},
	} {
		t.Run(name, func(t *testing.T) { has(t, wantWakes(t, builderHome(t), c.f, 1, 0), c.want...) })
	}
}

// A session crius-hook files under the thread is the thread's, beside the role's current one; one in another thread is not.
func TestTheRolesClaudeInAnotherThreadIsNotTheBuildersEvidence(t *testing.T) {
	home := builderHome(t)
	f := builderFake(0, "waiting", mergeReply, []T3Task{gateTask}, gateShell) // the role's filed session, in th-api
	f.threads["th-b"] = T3Thread{Status: "waiting", Run: "run:b", Updated: t0.Add(-11 * time.Minute), LastReply: mergeReply}
	f.ses = append(f.ses, Session{ID: "s-b", PID: 300, Status: "idle"})
	f.states = []sessionstate.State{{SessionID: "s-b", Project: "quotes", Role: "api", Thread: "th-b", Time: t0.Add(24 * time.Hour)}}
	f.procs = append(f.procs, Proc{PID: 300, PPID: 1, Command: "claude"}, Proc{PID: 310, PPID: 300, Command: "bash /h/bin/crius-wait api"})
	has(t, wantWakes(t, home, f, 1, 0), "T3 thread th-b", "only its bus waiter runs")
}

func TestABuilderThatIsADelegatesThreadIsIdleWhenItFinishes(t *testing.T) {
	f := builderFake(11*time.Minute, "completed", doneReply, nil)
	setThread(f, "th-api", func(x *T3Thread) { x.Child = true })
	has(t, wantWakes(t, builderHome(t), f, 1, 0), "idle 11 min", "nothing left to build")
}

func TestABuilderWithNoClaudeFoundNamesThatAlone(t *testing.T) {
	f := builderFake(25*time.Minute, "waiting", mergeReply, []T3Task{waiterTask}, busWaiterShell)
	f.ses = nil
	if w := wantWakes(t, builderHome(t), f, 1, 0); strings.Contains(w, "T3 lists 1 background jobs") {
		t.Errorf("it counts shells it could not look for: %q", w)
	}
}

func TestABuildersClaudesAreThoseOfItsThread(t *testing.T) {
	job := []Proc{{PID: 300, PPID: 1, Command: "claude"}, {PID: 301, PPID: 300, Command: snap + "go test ./...'"}, {PID: 302, PPID: 301, Command: "go test ./..."}}
	for thread, want := range map[string]int{"th-api": 0, "th-other": 1} {
		f := builderFake(11*time.Minute, "waiting", mergeReply, []T3Task{waiterTask}, busWaiterShell, job)
		f.ses = append(f.ses, Session{ID: "s-2", PID: 300, Status: "idle"})
		f.states = []sessionstate.State{{SessionID: "s-2", Thread: thread}}
		wantWakes(t, builderHome(t), f, want, 0)
	}
}

// A slow t3 stops the reads: a subagent is not read after that, and one that is slow stops the rest.
func TestNoThreadIsReadAfterT3GaveNoAnswer(t *testing.T) {
	home := threadHome(t, "api", "web")
	testutil.Write(t, filepath.Join(home, "wakewatch-projects"), "quotes quotes:api quotes:web")
	f := builderFake(25*time.Minute, "waiting", mergeReply, []T3Task{
		{ID: "a", Kind: "subagent", Description: "One", Child: "th-1"}, {ID: "b", Kind: "subagent", Description: "Two", Child: "th-2"}}, busWaiterShell)
	f.threads["th-web"] = T3Thread{Status: "running", Active: true}
	d := f.deps(home, t0)
	read := d.Thread
	asked := ""
	d.Thread = func(id string) (T3Thread, error) {
		asked += id + " "
		if id == "th-1" {
			return T3Thread{}, ErrT3NoAnswer
		}
		return read(id)
	}
	if err := Run(d); err != nil {
		t.Fatal(err)
	}
	if asked != "th-api th-1 " {
		t.Errorf("want no read after t3 gave no answer, got %q", asked)
	}
}

// A builder that is closed, or whose thread is Crius's own, is none to watch: one is done, the other is loud.
func TestAClosedBuilderIsQuietAndCriussOwnThreadIsLoud(t *testing.T) {
	home := builderHome(t)
	testutil.Write(t, filepath.Join(home, "roles", "quotes.api.closed"), "s-api\n")
	f := builderFake(25*time.Minute, "completed", doneReply, nil)
	for _, d := range every(0, 30) {
		runOnce(t, home, f, t0.Add(d))
	}
	if len(f.sent) != 0 {
		t.Errorf("a closed builder is said: %q", f.sent)
	}
	testutil.Write(t, filepath.Join(home, "sessions", "s-api.json"), `{"session_id":"s-api"}`) // with no thread
	for _, d := range every(35, 60) {
		runOnce(t, home, f, t0.Add(d))
	}
	if len(f.sent) != 0 {
		t.Errorf("a closed builder with no thread is said: %q", f.sent)
	}
	home = builderHome(t)
	testutil.Write(t, filepath.Join(home, "state", "crius", "t3-thread"), "th-api\n")
	f = builderFake(25*time.Minute, "completed", doneReply, nil)
	runOnce(t, home, f, t0)
	runOnce(t, home, f, t0.Add(5*time.Minute))
	has(t, strings.Join(f.sent, "\n"), "named a builder")
}
