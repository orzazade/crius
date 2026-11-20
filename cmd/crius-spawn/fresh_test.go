package main

import (
	"errors"
	"path/filepath"
	"strings"
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
}

func (f *fakeFresh) freshener(home string) freshener {
	return freshener{home: home, wait: time.Millisecond, tries: 5, stopWait: time.Millisecond, stopTries: 3,
		show: func(id string) (wakewatch.T3Thread, error) {
			f.did = append(f.did, "show "+id)
			if f.showErr != nil {
				return wakewatch.T3Thread{}, f.showErr
			}
			th := f.threads[0]
			if len(f.threads) > 1 {
				f.threads = f.threads[1:]
			}
			return th, nil
		},
		process: func(config, id string) (int, error) {
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
	if m, _ := sessionstate.ClosedRole(filepath.Join(home, "roles"), "axi", "qa-2"); m != "s-older" {
		t.Errorf("closed mark %q, want the earlier s-older back", m)
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
