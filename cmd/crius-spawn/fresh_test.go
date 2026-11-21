package main

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/sessionstate"
	"github.com/orzazade/crius/internal/testutil"
	"github.com/orzazade/crius/internal/wakewatch"
)

// freshHome is a home where axi:qa-2's current session s-old runs in T3 thread th-old under config folder cfg.
func freshHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	testutil.Write(t, filepath.Join(home, "roles", "axi.qa-2.json"),
		`{"session_id":"s-old","project":"axi","role":"qa-2","t3_thread":"th-old","config":"/cfg","time":"2026-11-20T15:00:00Z"}`)
	testutil.Write(t, filepath.Join(home, "sessions", "s-old.json"),
		`{"session_id":"s-old","project":"axi","role":"qa-2","t3_thread":"th-old","config":"/cfg","time":"2026-11-20T15:00:00Z"}`)
	return home
}

// fakeFresh records what -fresh did, in order: "show", "kill <sig>", "spawn". The old claude is pid 16238 until a
// signal stops it (or never, with stubborn).
type fakeFresh struct {
	threads  []wakewatch.T3Thread // one per read; the last one repeats
	showErr  error
	alive    bool
	stubborn bool // ignores SIGTERM
	did      []string
	spawned  bool
	jobs     [][]wakewatch.Proc // the process list, one per read; the last one repeats; none: only the claude
	procsErr error
	said     strings.Builder // what -fresh said while it waited
	// kind and child are each listed job's kind and child thread (T3Thread.Background names the jobs); kind
	// "command" when empty
	kind, child string
	procErr     error     // what reading Claude Code's status files fails with
	clock       time.Time // the wait's clock: each pause moves it on, so no test waits for real
}

func (f *fakeFresh) freshener(home string) freshener {
	return freshener{home: home, wait: time.Second, limit: 4 * time.Second, stopWait: time.Millisecond, stopTries: 3, log: &f.said,
		now: func() time.Time { return f.clock }, sleep: func(d time.Duration) { f.clock = f.clock.Add(d) },
		show: func(id string) (wakewatch.T3Thread, []t3Job, error) {
			f.did = append(f.did, "show "+id)
			if f.showErr != nil {
				return wakewatch.T3Thread{}, nil, f.showErr
			}
			th := f.threads[0]
			if len(f.threads) > 1 {
				f.threads = f.threads[1:]
			}
			kind := f.kind
			if kind == "" {
				kind = "command"
			}
			var jobs []t3Job
			for _, b := range th.Background {
				jobs = append(jobs, t3Job{ID: b, Kind: kind, Child: f.child})
			}
			return th, jobs, nil
		},
		process: func(config, id string) (int, error) {
			if f.procErr != nil {
				return 0, f.procErr
			}
			if config != "/cfg" || id != "s-old" || !f.alive {
				return 0, nil
			}
			return 16238, nil
		},
		signal: func(pid int, sig syscall.Signal) error {
			if pid != 16238 {
				return errors.New("signalled the wrong pid")
			}
			if sig == 0 {
				if f.alive {
					return nil
				}
				return syscall.ESRCH
			}
			f.did = append(f.did, "kill "+sig.String())
			if sig == syscall.SIGKILL || !f.stubborn {
				f.alive = false
			}
			return nil
		},
		procs: func() ([]wakewatch.Proc, error) {
			f.did = append(f.did, "ps")
			if f.procsErr != nil {
				return nil, f.procsErr
			}
			ps := []wakewatch.Proc{{PID: 16238, PPID: 1, Command: "claude --resume s-old"}}
			if len(f.jobs) > 0 {
				ps = append(ps, f.jobs[0]...)
				if len(f.jobs) > 1 {
					f.jobs = f.jobs[1:]
				}
			}
			return ps, nil
		},
		spawn: func() (string, error) {
			f.did = append(f.did, "spawn")
			f.spawned = true
			if f.alive {
				return "", errors.New("2 live sessions for one role: the old claude pid 16238 still runs")
			}
			return "OK t3 th-new session s-new on max1 (T3 instance claude-max1)", nil
		}}
}

var idleThread = wakewatch.T3Thread{Status: "completed", LastReply: "qa done ✅"}

// 11-20: crius-spawn -close axi qa-2 only marked the session closed; its claude (pid 16238, a T3 thread) kept running,
// the next crius-spawn started a second session, and wakewatch raised "2 live sessions for one role". -fresh closes the
// old session, stops its claude by its own pid, and only then starts the new one, printing both ids.
func TestFreshStopsTheOldClaudeBeforeTheNewSessionStarts(t *testing.T) {
	home := freshHome(t)
	f := &fakeFresh{threads: []wakewatch.T3Thread{idleThread}, alive: true}
	out, err := f.freshener(home).fresh("axi", "qa-2")
	if err != nil {
		t.Fatalf("fresh: %v (did %q)", err, f.did)
	}
	if f.alive {
		t.Fatalf("the old claude pid 16238 still runs after -fresh (did %q)", f.did)
	}
	if k := strings.Join(f.did, ","); !strings.Contains(k, "kill terminated,spawn") {
		t.Errorf("want SIGTERM to pid 16238 just before the spawn, did %q", f.did)
	}
	if c, _ := sessionstate.ClosedRole(filepath.Join(home, "roles"), "axi", "qa-2"); c != "s-old" {
		t.Errorf("closed mark %q, want s-old", c)
	}
	for _, want := range []string{"s-old", "th-old", "pid 16238", "s-new", "th-new"} {
		if !strings.Contains(out, want) {
			t.Errorf("output %q lacks %q", out, want)
		}
	}
}

// A claude that ignores SIGTERM gets SIGKILL; one that outlives that too fails -fresh before any spawn.
func TestFreshKillsAStubbornClaude(t *testing.T) {
	f := &fakeFresh{threads: []wakewatch.T3Thread{idleThread}, alive: true, stubborn: true}
	if _, err := f.freshener(freshHome(t)).fresh("axi", "qa-2"); err != nil {
		t.Fatalf("fresh: %v", err)
	}
	if k := strings.Join(f.did, ","); !strings.Contains(k, "kill terminated,kill killed,spawn") {
		t.Errorf("want SIGTERM, SIGKILL, then spawn; did %q", f.did)
	}
}

// -fresh waits for the turn boundary: a run open, an open request or a background job of the thread's own holds it.
// It refuses, naming why, when the thread stays busy or cannot be read, and then closes, stops and starts nothing.
func TestFreshWaitsForATurnBoundaryAndRefusesWhenItCannotSeeOne(t *testing.T) {
	busy := wakewatch.T3Thread{Status: "running", Active: true}
	job := wakewatch.T3Thread{Status: "completed", Background: []string{"b7x (go test ./...)"}}
	f := &fakeFresh{threads: []wakewatch.T3Thread{busy, job, idleThread}, alive: true}
	if _, err := f.freshener(freshHome(t)).fresh("axi", "qa-2"); err != nil || f.alive {
		t.Fatalf("after the run and the job ended: err %v, alive %v (did %q)", err, f.alive, f.did)
	}
	if n := strings.Count(strings.Join(f.did, ","), "show th-old"); n < 3 {
		t.Errorf("want it to read the thread until the boundary (3 reads), did %q", f.did)
	}
	for _, c := range []struct {
		name string
		f    *fakeFresh
		want string
	}{
		{"always busy", &fakeFresh{threads: []wakewatch.T3Thread{job}, alive: true}, "b7x (go test ./...)"},
		{"unreadable", &fakeFresh{showErr: errors.New("t3 gave no answer in time"), alive: true}, "cannot read T3 thread th-old: t3 gave no answer in time"},
	} {
		home := freshHome(t)
		_, err := c.f.freshener(home).fresh("axi", "qa-2")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want a refusal naming %q, got %v", c.name, c.want, err)
		}
		if c.f.spawned || !c.f.alive {
			t.Errorf("%s: a refusal still spawned (%v) or stopped the old claude (alive %v)", c.name, c.f.spawned, c.f.alive)
		}
		if m, _ := sessionstate.ClosedRole(filepath.Join(home, "roles"), "axi", "qa-2"); m != "" {
			t.Errorf("%s: a refusal still closed the role (%q)", c.name, m)
		}
	}
}

// Review P2-1/P2-5: a failure after the close puts the closed mark back as it was (here an older session's mark), so a
// live claude is never left marked closed and an older closed session stays closed.
func TestFreshPutsTheClosedMarkBackWhenItCannotStopTheClaude(t *testing.T) {
	home := freshHome(t)
	testutil.Write(t, filepath.Join(home, "roles", "axi.qa-2.closed"), "s-older\n")
	f := &fakeFresh{threads: []wakewatch.T3Thread{idleThread}, alive: true}
	fr := f.freshener(home)
	fr.signal = func(pid int, sig syscall.Signal) error { return syscall.EPERM }
	_, err := fr.fresh("axi", "qa-2")
	if err == nil || !strings.Contains(err.Error(), "operation not permitted") {
		t.Fatalf("want the stop's failure, got %v", err)
	}
	if f.spawned {
		t.Errorf("spawned although the old claude still runs")
	}
	if m, _ := sessionstate.ClosedSessions(filepath.Join(home, "roles"), "axi", "qa-2"); strings.Join(m, ",") != "s-older" {
		t.Errorf("closed mark %q, want the earlier s-older back alone", m)
	}
}

// Review P2-2: a thread whose newest session is not the one the role's file names is refused, changing nothing.
func TestFreshRefusesWhenTheThreadsSessionIsNotTheRolesOwn(t *testing.T) {
	home := freshHome(t)
	testutil.Write(t, filepath.Join(home, "sessions", "s-moved.json"),
		`{"session_id":"s-moved","project":"axi","role":"qa-2","t3_thread":"th-old","config":"/cfg","time":"2026-11-20T16:00:00Z"}`)
	f := &fakeFresh{threads: []wakewatch.T3Thread{idleThread}, alive: true}
	_, err := f.freshener(home).fresh("axi", "qa-2")
	if err == nil || !strings.Contains(err.Error(), "runs session s-moved, but the role's file names s-old") {
		t.Fatalf("want a refusal naming both sessions, got %v", err)
	}
	if f.spawned || !f.alive {
		t.Errorf("a refusal spawned (%v) or stopped the claude (alive %v)", f.spawned, f.alive)
	}
}

// gateJob is a Bash tool shell of the old claude's own (pid 16238) that runs a gate: T3 does not list it as a
// background job (11-20: api-split's gate ran 50 min so), and stopping the claude would end it.
var gateJob = []wakewatch.Proc{
	{PID: 17001, PPID: 16238, Command: "/bin/zsh -c source /Users/x/.claude/shell-snapshots/snapshot-zsh-1.sh && eval 'go test ./...' < /dev/null"},
	{PID: 17002, PPID: 17001, Command: "go test ./..."},
}

// 07bd54d review (c): -fresh read only the background jobs T3 lists, so a ⏳ job the old claude runs as its own Bash
// shell (which T3 does not list) died with the claude. -fresh waits for it as for any background job, and refuses,
// naming it, when it still runs at the end of the wait; a failed process list is a refusal too, never "no job".
func TestFreshWaitsForTheOldClaudesOwnJobThatT3DoesNotList(t *testing.T) {
	f := &fakeFresh{threads: []wakewatch.T3Thread{idleThread}, alive: true, jobs: [][]wakewatch.Proc{gateJob, gateJob, nil}}
	if _, err := f.freshener(freshHome(t)).fresh("axi", "qa-2"); err != nil || f.alive {
		t.Fatalf("after the job ended: err %v, alive %v (did %q)", err, f.alive, f.did)
	}
	if n := strings.Count(strings.Join(f.did, ","), "ps"); n < 3 {
		t.Errorf("want it to read the processes until the job ended (3 reads), did %q", f.did)
	}
	for _, c := range []struct {
		name string
		f    *fakeFresh
		want string
	}{
		{"job runs on", &fakeFresh{threads: []wakewatch.T3Thread{idleThread}, alive: true, jobs: [][]wakewatch.Proc{gateJob}}, "go test ./..."},
		{"ps failed", &fakeFresh{threads: []wakewatch.T3Thread{idleThread}, alive: true, procsErr: errors.New("ps: exit status 1")}, "ps: exit status 1"},
	} {
		home := freshHome(t)
		_, err := c.f.freshener(home).fresh("axi", "qa-2")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want a refusal naming %q, got %v", c.name, c.want, err)
		}
		if c.f.spawned || !c.f.alive {
			t.Errorf("%s: a refusal still spawned (%v) or stopped the old claude (alive %v)", c.name, c.f.spawned, c.f.alive)
		}
		if m, _ := sessionstate.ClosedRole(filepath.Join(home, "roles"), "axi", "qa-2"); m != "" {
			t.Errorf("%s: a refusal left the role closed (%q)", c.name, m)
		}
	}
}

// 07bd54d review (d): two -fresh runs for one role at once both passed the boundary and both spawned. The second run,
// started while the first is inside its spawn, is refused at once and touches nothing; the first one still finishes.
func TestTwoFreshRunsForOneRoleStartOneSession(t *testing.T) {
	home := freshHome(t)
	first := &fakeFresh{threads: []wakewatch.T3Thread{idleThread}, alive: true}
	in, release := make(chan struct{}), make(chan struct{})
	fr := first.freshener(home)
	spawn := fr.spawn
	fr.spawn = func() (string, error) {
		close(in)
		<-release
		return spawn()
	}
	var wg sync.WaitGroup
	var firstErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, firstErr = fr.fresh("axi", "qa-2")
	}()
	<-in
	second := &fakeFresh{threads: []wakewatch.T3Thread{idleThread}, alive: true}
	_, err := second.freshener(home).fresh("axi", "qa-2")
	close(release)
	wg.Wait()
	if err == nil || !strings.Contains(err.Error(), "another crius-spawn -fresh for axi qa-2 runs") {
		t.Errorf("second -fresh while the first spawns: want a refusal, got %v (did %q)", err, second.did)
	}
	if second.spawned || len(second.did) > 0 {
		t.Errorf("the refused run still did %q", second.did)
	}
	if firstErr != nil || !first.spawned {
		t.Errorf("first -fresh: err %v, spawned %v", firstErr, first.spawned)
	}
}

// Review P3: a job of the claude's own that starts between the two boundary reads (after the close) refuses at the
// second read, and the closed mark goes back exactly as it was.
func TestFreshPutsTheMarkBackWhenAJobStartsAfterTheClose(t *testing.T) {
	home := freshHome(t)
	testutil.Write(t, filepath.Join(home, "roles", "axi.qa-2.closed"), "s-older\ns-old\ns-oldest\n")
	f := &fakeFresh{threads: []wakewatch.T3Thread{idleThread}, alive: true, jobs: [][]wakewatch.Proc{nil, gateJob}}
	_, err := f.freshener(home).fresh("axi", "qa-2")
	if err == nil || !strings.Contains(err.Error(), "go test ./...") {
		t.Fatalf("want a refusal naming the job, got %v (did %q)", err, f.did)
	}
	if f.spawned || !f.alive {
		t.Errorf("a refusal spawned (%v) or stopped the claude (alive %v)", f.spawned, f.alive)
	}
	if m, _ := sessionstate.ClosedSessions(filepath.Join(home, "roles"), "axi", "qa-2"); strings.Join(m, ",") != "s-older,s-old,s-oldest" {
		t.Errorf("closed mark %q, want it back as s-older,s-old,s-oldest", m)
	}
}

// staleJob is the 11-21 axi:ms-flow thread: done with its turn, but T3 still lists a background job that a T3 restart
// had killed, so the job never ends and T3 never takes it off the list.
var staleJob = wakewatch.T3Thread{Status: "completed", LastReply: "suite started ⏳", Background: []string{"bq41 (Run the ms-flow suite)"}}

// 11-21: crius-spawn -fresh -backend t3 axi ms-flow waited 2 x 15 min with no output for a background job T3 listed but
// a T3 restart had killed. No claude runs the session, so nothing can run that job: -fresh refuses at its first read,
// naming the thread, the job and the safe next step, and closes, stops and starts nothing.
func TestFreshRefusesAtOnceWhenT3ListsAJobNoClaudeRuns(t *testing.T) {
	home := freshHome(t)
	f := &fakeFresh{threads: []wakewatch.T3Thread{staleJob}}
	_, err := f.freshener(home).fresh("axi", "qa-2")
	if err == nil {
		t.Fatalf("want a refusal, got none (did %q)", f.did)
	}
	for _, want := range []string{"th-old", "bq41 (Run the ms-flow suite)", "s-old", "no claude",
		"t3_thread_interrupt threadId=th-old stop=true", "t3_thread_send threadId=th-old freshContext=true"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal lacks %q: %v", want, err)
		}
	}
	if strings.Contains(err.Error(), "\n") {
		t.Errorf("refusal is more than one line: %q", err)
	}
	if n := strings.Count(strings.Join(f.did, ","), "show th-old"); n != 1 {
		t.Errorf("want it to refuse at the first read, it read the thread %d times (did %q)", n, f.did)
	}
	if f.spawned {
		t.Errorf("a refusal still spawned")
	}
	if m, _ := sessionstate.ClosedRole(filepath.Join(home, "roles"), "axi", "qa-2"); m != "" {
		t.Errorf("a refusal closed the role (%q)", m)
	}
}

// A thread that stays busy to the end of the wait (here a job its live claude may still run) refuses in one line that
// names the thread, what held it and the safe next step, so Crius never has to look up what to do.
func TestFreshNamesTheNextStepWhenTheWaitRunsOut(t *testing.T) {
	for _, c := range []struct {
		name string
		th   wakewatch.T3Thread
		want string
	}{
		{"job listed", staleJob, "background job(s) bq41 (Run the ms-flow suite)"},
		{"run open", wakewatch.T3Thread{Status: "running", Active: true}, "a run is open"},
	} {
		f := &fakeFresh{threads: []wakewatch.T3Thread{c.th}, alive: true}
		_, err := f.freshener(freshHome(t)).fresh("axi", "qa-2")
		if err == nil {
			t.Fatalf("%s: want a refusal, got none", c.name)
		}
		for _, want := range []string{"T3 thread th-old", c.want, "nothing was closed or stopped",
			"t3_thread_interrupt threadId=th-old stop=true", "t3_thread_send threadId=th-old freshContext=true"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: refusal lacks %q: %v", c.name, want, err)
			}
		}
		if strings.Contains(err.Error(), "\n") || f.spawned || !f.alive {
			t.Errorf("%s: more than one line (%q), or it spawned (%v) or stopped the claude (alive %v)", c.name, err, f.spawned, f.alive)
		}
	}
}

// 11-21: the wait printed nothing for 15 min, so Crius could not tell a busy thread from a hung crius-spawn. While it
// waits, -fresh says what it waits on: at its first busy read, when that changes, and once a minute while it stays.
func TestFreshSaysWhatItWaitsOn(t *testing.T) {
	run := wakewatch.T3Thread{Status: "running", Active: true}
	f := &fakeFresh{threads: []wakewatch.T3Thread{run, staleJob}, alive: true}
	fr := f.freshener(freshHome(t))
	fr.wait, fr.limit = 20*time.Second, 2*time.Minute // reads at 0 s, 20 s, ... 120 s on the fake clock
	if _, err := fr.fresh("axi", "qa-2"); err == nil {
		t.Fatalf("want a refusal at the end of the wait")
	}
	said := f.said.String()
	for _, want := range []string{"waiting on T3 thread th-old (0s of 2m0s): a run is open",
		"(20s of 2m0s): background job(s) bq41 (Run the ms-flow suite)", "(1m20s of 2m0s): background job(s) bq41"} {
		if !strings.Contains(said, want) {
			t.Errorf("while it waited it said %q, which lacks %q", said, want)
		}
	}
	if n := strings.Count(said, "\n"); n != 3 {
		t.Errorf("want 3 lines (first read, change, a minute on), got %d: %q", n, said)
	}
}

// The wait crius-spawn runs ends well inside the time Crius waits on a command (11-21: 240 x 5 s = 20 min outlived
// Crius's own 15 min wait twice, so its refusal was never seen), it is a limit in time, not a count of reads (a slow
// t3 answer stretched those), and it says what it waits on.
func TestFreshWaitEndsInsideCriusesWait(t *testing.T) {
	fr := newFreshener(t.TempDir(), nil)
	if fr.limit <= 0 || fr.limit > 5*time.Minute {
		t.Errorf("-fresh waits up to %s for a turn boundary, want at most 5m0s", fr.limit)
	}
	if fr.log == nil || fr.now == nil || fr.sleep == nil {
		t.Errorf("-fresh has no log writer (%v), clock (%v) or pause (%v)", fr.log != nil, fr.now != nil, fr.sleep != nil)
	}
}

// Review P1: T3 lists jobs that run outside the thread's claude too: a delegate_task child thread (kind subagent with a
// childThreadId) and a monitor (a pull-request watch the T3 server runs). With no claude for the session they still
// run, so -fresh waits for them, and its refusal never tells Crius to stop the thread (stop=true would end them too).
func TestFreshWaitsForAJobThatRunsOutsideTheClaude(t *testing.T) {
	for _, c := range []struct{ kind, child string }{{"subagent", "th-child"}, {"monitor", ""}, {"background_task", ""}} {
		job := wakewatch.T3Thread{Status: "completed", Background: []string{"d9 (review the diff)"}}
		f := &fakeFresh{threads: []wakewatch.T3Thread{job}, kind: c.kind, child: c.child}
		_, err := f.freshener(freshHome(t)).fresh("axi", "qa-2")
		if err == nil {
			t.Fatalf("%s: want a refusal at the end of the wait, got none", c.kind)
		}
		if n := strings.Count(strings.Join(f.did, ","), "show th-old"); n != 5 {
			t.Errorf("%s: want it to wait the whole limit (5 reads), it read %d times: %v", c.kind, n, err)
		}
		for _, want := range []string{"T3 thread th-old", "d9 (review the diff)", "let d9 (review the diff) end"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: refusal lacks %q: %v", c.kind, want, err)
			}
		}
		if strings.Contains(err.Error(), "threadId=th-old stop=true") || strings.Contains(err.Error(), "no claude runs") {
			t.Errorf("%s: refusal tells Crius to stop the thread or calls the job dead: %v", c.kind, err)
		}
	}
	// a subagent with no child thread runs inside the claude: no claude, so nothing runs it
	f := &fakeFresh{threads: []wakewatch.T3Thread{staleJob}, kind: "subagent"}
	if _, err := f.freshener(freshHome(t)).fresh("axi", "qa-2"); err == nil || !strings.Contains(err.Error(), "no claude runs") {
		t.Errorf("in-claude subagent with no claude: want the at-once refusal, got %v", err)
	}
}

// An open run or request next to a listed job is not a dead listing, even with no claude found: the run may be
// starting one. And a status file caught mid-write (review P2) is read again, not taken as an answer.
func TestFreshWaitsWhenTheJobListingIsNotProvenDead(t *testing.T) {
	both := wakewatch.T3Thread{Status: "running", Active: true, Background: []string{"bq41 (Run the ms-flow suite)"}}
	for _, c := range []struct {
		name string
		f    *fakeFresh
		want string
	}{
		{"run open", &fakeFresh{threads: []wakewatch.T3Thread{both}}, "a run is open; background job(s) bq41"},
		{"status file", &fakeFresh{threads: []wakewatch.T3Thread{staleJob}, procErr: errors.New("12345.json: unexpected end of JSON input")},
			"cannot tell whether its claude runs: 12345.json: unexpected end of JSON input"},
	} {
		_, err := c.f.freshener(freshHome(t)).fresh("axi", "qa-2")
		if err == nil || !strings.Contains(err.Error(), c.want) || strings.Contains(err.Error(), "no claude runs") {
			t.Errorf("%s: want the end-of-wait refusal naming %q, got %v", c.name, c.want, err)
		}
		if n := strings.Count(strings.Join(c.f.did, ","), "show th-old"); n != 5 {
			t.Errorf("%s: want it to wait the whole limit (5 reads), it read %d times", c.name, n)
		}
	}
}

// A dead listing seen at the second read (after the close) refuses the same way, and puts the closed mark back.
func TestFreshPutsTheMarkBackWhenADeadListingShowsAfterTheClose(t *testing.T) {
	home := freshHome(t)
	testutil.Write(t, filepath.Join(home, "roles", "axi.qa-2.closed"), "s-older\n")
	f := &fakeFresh{threads: []wakewatch.T3Thread{idleThread, staleJob}}
	_, err := f.freshener(home).fresh("axi", "qa-2")
	if err == nil || !strings.Contains(err.Error(), "no claude runs its session s-old") {
		t.Fatalf("want the dead-listing refusal, got %v (did %q)", err, f.did)
	}
	if f.spawned {
		t.Errorf("a refusal spawned")
	}
	if m, _ := sessionstate.ClosedSessions(filepath.Join(home, "roles"), "axi", "qa-2"); strings.Join(m, ",") != "s-older" {
		t.Errorf("closed mark %q, want s-older back alone", m)
	}
}
