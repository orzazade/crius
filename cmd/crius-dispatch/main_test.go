package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/dispatch"
)

var t0 = time.Date(2026, 11, 10, 17, 0, 0, 0, time.FixedZone("+04", 4*3600))

// Only projects with a DISPATCH.toml are dispatched; a failing project is said once on Crius's bus as an ALERT, the
// others still run, and the same failure next minute is not said again.
func TestOnlyConfiguredProjectsAndOneAlertPerFailure(t *testing.T) {
	home := t.TempDir()
	for _, p := range []string{"alpha", "beta", "gamma"} {
		os.MkdirAll(filepath.Join(home, "state", p), 0o755)
	}
	os.MkdirAll(filepath.Join(home, "bus"), 0o755)
	os.WriteFile(filepath.Join(home, "state", "alpha", "DISPATCH.toml"), []byte("cap = 1\nprefix = \"p-\"\nprompt = \"do {id}\"\n"), 0o644)
	os.WriteFile(filepath.Join(home, "state", "beta", "DISPATCH.toml"), []byte("cap = 0\n"), 0o644) // broken config
	var ran []string
	mk := func(home, project string, now time.Time) dispatch.Deps {
		ran = append(ran, project)
		return dispatch.Deps{Home: home, Project: project, Now: now,
			Spawn: func(string, string) error { return nil }, Close: func(string) error { return nil },
			Alive: func(string) (bool, error) { return true, nil }, PRState: func(int) (string, error) { return "active", nil },
			Post: func(string, string, string) error { return nil }}
	}
	err := run(home, t0, mk)
	if err == nil || !strings.Contains(err.Error(), "beta: DISPATCH.toml needs cap >= 1") {
		t.Fatalf("want beta's error, got %v", err)
	}
	if strings.Join(ran, ",") != "alpha,beta" {
		t.Fatalf("ran %q", ran)
	}
	run(home, t0.Add(time.Minute), mk)
	if _, err := os.Stat(markPath(home, "beta")); err != nil {
		t.Fatalf("no error mark after a failing run, so the clean-run check below tests nothing: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(home, "bus", "crius.log"))
	if n := strings.Count(string(b), " ALERT crius:dispatch -> crius:lead — dispatcher failed (log "+filepath.Join(home, "logs", "dispatch.log")+"): beta: DISPATCH.toml needs cap >= 1"); n != 1 {
		t.Fatalf("want one ALERT, got %d: %q", n, b)
	}
	os.WriteFile(filepath.Join(home, "state", "beta", "DISPATCH.toml"), []byte("cap = 1\nprefix = \"p-\"\nprompt = \"do {id}\"\n"), 0o644)
	if err := run(home, t0.Add(2*time.Minute), mk); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(markPath(home, "beta")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the error mark stayed after a clean run: %v", err)
	}
}

// A role with no role file, or whose session is closed on purpose, is not alive.
func TestAliveReadsTheRoleFile(t *testing.T) {
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, "roles"), 0o755)
	if ok, err := alive(home, "alpha", "p-ac1"); ok || err != nil {
		t.Fatalf("no role file: %v %v", ok, err)
	}
}

// Review round 2 P3: an error that only changes its numbers (a new tab's surface, a quota figure) is not said again.
func TestAnErrorWithNewNumbersIsNotSaidAgain(t *testing.T) {
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, "state", "alpha"), 0o755)
	os.MkdirAll(filepath.Join(home, "bus"), 0o755)
	os.WriteFile(filepath.Join(home, "state", "alpha", "DISPATCH.toml"), []byte("cap = 1\nprefix = \"p-\"\nprompt = \"do {id}\"\n"), 0o644)
	n := 0
	mk := func(home, project string, now time.Time) dispatch.Deps {
		n++
		return dispatch.Deps{Home: home, Project: project, Now: now,
			Spawn: func(string, string) error { return nil }, Close: func(string) error { return nil },
			Alive:   func(string) (bool, error) { return false, fmt.Errorf("ps saw surface:%d", n) },
			PRState: func(int) (string, error) { return "active", nil }, Post: func(string, string, string) error { return nil }}
	}
	os.WriteFile(filepath.Join(home, "state", "alpha", "BOARD.toml"), []byte("[[row]]\nid = \"AC1\"\ntext = \"x\"\nstate = \"ready\"\nopened = 2026-11-10T16:00:00Z\nupdated = 2026-11-10T16:00:00Z\n"), 0o644)
	run(home, t0, mk)
	run(home, t0.Add(time.Minute), mk)
	b, _ := os.ReadFile(filepath.Join(home, "bus", "crius.log"))
	if c := strings.Count(string(b), " ALERT crius:dispatch"); c != 1 {
		t.Fatalf("want one ALERT, got %d: %q", c, b)
	}
}

// Review round 3 P3-4: the dedupe keeps row ids and roles: the same error on another row is said. Each error goes
// through alert, a minute apart, and the bus is counted: AC12 again (new surface and duration only) stays quiet, AC13 is
// said.
func TestTheSameErrorOnAnotherRowIsSaid(t *testing.T) {
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, "bus"), 0o755)
	os.MkdirAll(filepath.Join(home, "state", "alpha"), 0o755)
	say := func(i int, id string, surface int, took string) {
		err := fmt.Errorf("alpha: %s: is alpha:p-%s alive: damaged role file (surface:%d, after %s)", id, strings.ToLower(id), surface, took)
		if aerr := alert(home, "alpha", t0.Add(time.Duration(i)*time.Minute), err); aerr != nil {
			t.Fatalf("alert %d: %v", i, aerr)
		}
	}
	count := func(id string) int {
		b, _ := os.ReadFile(filepath.Join(home, "bus", "crius.log"))
		return strings.Count(string(b), "alpha: "+id+": is alpha:p-"+strings.ToLower(id)+" alive")
	}
	say(0, "AC12", 1000070039, "5.7s")
	say(1, "AC12", 1000070040, "1m6.1s")
	if n := count("AC12"); n != 1 {
		t.Fatalf("AC12's error with a new surface and duration was said %d times, want 1", n)
	}
	say(2, "AC13", 1000070041, "350ms")
	if n := count("AC13"); n != 1 {
		t.Fatalf("AC13's error after AC12's was said %d times, want 1", n)
	}
}

// ci-proof's FAIL, STALE and NONE exit 1 with a line: that is an answer for the merge queue, not an error. Exit 2 (it
// could not look) is an error carrying its LOUD line.
func TestCIProofsLineIsTheAnswerAndItsLoudExitAnError(t *testing.T) {
	bin := t.TempDir()
	fake := filepath.Join(bin, "ci-proof")
	os.WriteFile(fake, []byte("#!/bin/sh\ncase \"$*\" in\n"+
		"'-pipeline 7 8001') echo 'STALE pr=8001 head=aaaaaaaa merge=bbbbbbbb build=4 result=target-moved'; exit 1;;\n"+
		"'8002') echo 'PASS pr=8002 head=cccccccc merge=dddddddd build=5 result=succeeded suites=X 1/1'; exit 0;;\n"+
		"*) echo \"LOUD ci-proof: PR 8003: cannot read the PR: $*\"; exit 2;;\nesac\n"), 0o755)
	if l, err := ciProof(fake, 8001, 7); err != nil || l != "STALE pr=8001 head=aaaaaaaa merge=bbbbbbbb build=4 result=target-moved" {
		t.Errorf("exit 1: %q %v", l, err)
	}
	if l, err := ciProof(fake, 8002, 0); err != nil || !strings.HasPrefix(l, "PASS pr=8002 ") {
		t.Errorf("no pipeline: %q %v", l, err)
	}
	if l, err := ciProof(fake, 8003, 0); err == nil || !strings.Contains(err.Error(), "LOUD ci-proof: PR 8003: cannot read the PR: 8003") {
		t.Errorf("exit 2: %q %v", l, err)
	}
}

// Review P3-3: an exit 0 with no line is a failure to look, not an answer.
func TestCIProofWithNoLineIsAnError(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "ci-proof")
	os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0o755)
	if l, err := ciProof(fake, 8001, 0); err == nil {
		t.Errorf("no line taken as an answer: %q", l)
	}
}

// The merge command's output (stdout and stderr) comes back with how it ended; a non-zero exit is an error.
func TestRunMergeGivesItsOutputAndExit(t *testing.T) {
	out, err := runMerge(context.Background(), "echo gate ok; echo 'REFUSED merge.sh rc 16: no vote' >&2; exit 16", time.Minute)
	if err == nil || err.Error() != "exit status 16" || !strings.Contains(out, "gate ok") || !strings.Contains(out, "REFUSED merge.sh rc 16: no vote") {
		t.Errorf("refused: %q %v", out, err)
	}
	if out, err := runMerge(context.Background(), "echo 'merge.sh: PR 8001 completed'", time.Minute); err != nil || strings.TrimSpace(out) != "merge.sh: PR 8001 completed" {
		t.Errorf("completed: %q %v", out, err)
	}
	if _, err := runMerge(context.Background(), "sleep 5", 100*time.Millisecond); err == nil || !strings.Contains(err.Error(), "no answer in") {
		t.Errorf("limit: %v", err)
	}
}

// Review P3-1: a merge command past its limit is killed with everything it started (an az call in flight could
// still complete the PR), and its outcome is said as unknown.
func TestAMergePastItsLimitIsKilledWholeAndUnknown(t *testing.T) {
	pidf := filepath.Join(t.TempDir(), "child")
	_, err := runMerge(context.Background(), "sleep 30 & echo $! > "+pidf+"; wait", 300*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "outcome unknown") {
		t.Fatalf("err %v", err)
	}
	b, rerr := os.ReadFile(pidf)
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if rerr != nil || pid <= 0 {
		t.Fatalf("no child pid: %v %q", rerr, b)
	}
	time.Sleep(200 * time.Millisecond)
	if syscall.Kill(pid, 0) == nil {
		syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("child %d still runs after the limit", pid)
	}
}

// Review r2 P2-A: the command runs in its own process group, so a dispatcher stopped by launchd (SIGTERM, on a reinstall)
// must stop it too: cancelling the parent context kills the whole group, and the outcome is unknown.
func TestAMergeStopsWithItsDispatcher(t *testing.T) {
	pidf := filepath.Join(t.TempDir(), "child")
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	_, err := runMerge(ctx, "sleep 30 & echo $! > "+pidf+"; wait", time.Minute)
	if err == nil || !errors.Is(err, dispatch.ErrOutcomeUnknown) {
		t.Fatalf("err %v", err)
	}
	b, _ := os.ReadFile(pidf)
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	time.Sleep(200 * time.Millisecond)
	if pid <= 0 || syscall.Kill(pid, 0) == nil {
		syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("child %d still runs after its dispatcher stopped", pid)
	}
}

// The land command runs detached under its lock: the lock is held from before startLand returns until the command ends,
// the command itself does not hold it (it is closed for it), its output is in the log, its exit code lands in rc, and
// startLand returns before it ends. No clock (a timed look flaked under load, 11-20): the command waits on a pipe the
// test writes, and the test sees its end by taking the lock.
func TestStartLandRunsDetachedUnderItsLock(t *testing.T) {
	dir := t.TempDir()
	lock, log, rc, gate := filepath.Join(dir, "landq.lock"), filepath.Join(dir, "landq.log"), filepath.Join(dir, "it's rc"), filepath.Join(dir, "gate")
	if err := syscall.Mkfifo(gate, 0o600); err != nil {
		t.Fatal(err)
	}
	// opened read-write so it never blocks; closed at the end, so a command still reading it on a failed test gets EOF
	g, err := os.OpenFile(gate, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { g.Close() })
	if err := startLand("echo hi; { : >&3; } 2>/dev/null && echo lock-fd-open; read x < '"+gate+"'; exit 7", lock, log, rc); err != nil {
		t.Fatal(err)
	}
	l, err := os.OpenFile(lock, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := syscall.Flock(int(l.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); !errors.Is(err, syscall.EWOULDBLOCK) {
		t.Fatalf("the lock was not held once startLand returned (flock: %v)", err)
	}
	if _, err := os.Stat(rc); err == nil {
		t.Fatal("startLand returned after the command ended: it must not wait for it")
	}
	if _, err := g.WriteString("go\n"); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(l.Fd()), syscall.LOCK_EX); err != nil { // let go when bash ends, after it wrote rc
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(rc); strings.TrimSpace(string(b)) != "7" {
		t.Fatalf("rc file %q, want 7", b)
	}
	if o, _ := os.ReadFile(log); strings.TrimSpace(string(o)) != "hi" {
		t.Fatalf("log %q, want hi", o)
	}
}

// A lock already held (another landing run) is an error, and no command runs; the last run's log is left as it is.
func TestStartLandRefusesWhileTheLockIsHeld(t *testing.T) {
	dir := t.TempDir()
	lock, log, rc, ran := filepath.Join(dir, "landq.lock"), filepath.Join(dir, "landq.log"), filepath.Join(dir, "landq.rc"), filepath.Join(dir, "ran")
	l, err := os.OpenFile(lock, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err := syscall.Flock(int(l.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(log, []byte("LANDED PR 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = startLand("touch '"+ran+"'", lock, log, rc)
	if err == nil || !strings.Contains(err.Error(), "is held") {
		t.Fatalf("err %v, want the lock is held", err)
	}
	if _, err := os.Stat(ran); err == nil {
		t.Fatal("the command ran while the lock was held")
	}
	if o, _ := os.ReadFile(log); string(o) != "LANDED PR 1\n" {
		t.Fatalf("log %q: the last run's log was changed", o)
	}
}

// Review 11-21: Go durations (time.Duration's String: 1m2.5s, 350ms, 2h3m4.5s, 812µs) and timestamps change every run with
// no new cause, so an error that only differs in them is one error and is said once.
func TestAnErrorWithNewDurationsOrTimesIsOneError(t *testing.T) {
	for _, p := range [][2]string{
		{"merge.sh: no answer in 1m2.5s", "merge.sh: no answer in 1m4.25s"},
		{"ps took 350ms", "ps took 1.2s"},
		{"land ran 2h3m4.5s", "land ran 2h3m9s"},
		{"az took 812µs", "az took 95µs"},
		{"az took 812us then 3ns", "az took 9us then 40ns"},
		{"stale at 2026-11-21T03:04:05+04:00 after 1m2.5s", "stale at 2026-11-21T03:05:07+04:00 after 1m3s"},
		{"stale at 2026-11-21 03:04:05.123Z", "stale at 2026-11-22 13:44:00.9Z"},
		{"lock held since 03:04:05 (5% left)", "lock held since 03:05:59 (17% left)"},
		{"stale since 17:04, after -1m30s and .5s", "stale since 17:05, after -2m and .7s"},
		{"at 2026-11-21 00:52:59.93 +0400 +04 m=+0.0106", "at 2026-11-21 00:53:59.1 +0400 +04 m=+60.2"},
	} {
		if a, b := alertKey(errors.New(p[0])), alertKey(errors.New(p[1])); a != b {
			t.Errorf("errors %q and %q differ only in durations or times but have keys %q and %q", p[0], p[1], a, b)
		}
	}
}

// The other side: errors that differ in what went wrong, the row, the role or the PR keep different keys.
func TestDifferentErrorsKeepDifferentKeys(t *testing.T) {
	for _, p := range [][2]string{
		{"alpha: AC12: PR 8001 refused: no vote", "alpha: AC12: PR 8001 refused: build failed"},
		{"alpha: AC12: PR 8001 refused: no vote", "alpha: AC12: PR 8002 refused: no vote"},
		{"alpha: AC12: is alpha:p-ac12 alive: damaged role file", "alpha: AC13: is alpha:p-ac13 alive: damaged role file"},
		{"alpha: F-118: no answer in 1m2s", "alpha: F-119: no answer in 1m2s"},
		{"cap = 0 in DISPATCH.toml", "cap = 1 in DISPATCH.toml"},
		{"alpha: R-2h: no answer", "alpha: R-3h: no answer"},
		{"is alpha:p-5m alive", "is alpha:p-6m alive"},
		{"BOARD.toml:12:40: expected key", "BOARD.toml:13:2: expected key"},
		{"dial tcp 10.0.0.5:22: refused", "dial tcp 10.0.0.5:23: refused"},
	} {
		if a, b := alertKey(errors.New(p[0])), alertKey(errors.New(p[1])); a == b {
			t.Errorf("different errors %q and %q have one key %q", p[0], p[1], a)
		}
	}
}
