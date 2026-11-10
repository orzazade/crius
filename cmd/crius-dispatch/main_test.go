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
	b, _ := os.ReadFile(filepath.Join(home, "bus", "crius.log"))
	if n := strings.Count(string(b), " ALERT crius:dispatch -> crius:lead — dispatcher failed: beta: DISPATCH.toml needs cap >= 1"); n != 1 {
		t.Fatalf("want one ALERT, got %d: %q", n, b)
	}
	os.WriteFile(filepath.Join(home, "state", "beta", "DISPATCH.toml"), []byte("cap = 1\nprefix = \"p-\"\nprompt = \"do {id}\"\n"), 0o644)
	if err := run(home, t0.Add(2*time.Minute), mk); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "state", "dispatch.error")); !errors.Is(err, os.ErrNotExist) {
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

// Review round 3 P3-4: the dedupe keeps row ids and roles: the same error on another row is said.
func TestTheSameErrorOnAnotherRowIsSaid(t *testing.T) {
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, "bus"), 0o755)
	for i, id := range []string{"AC12", "AC13"} {
		seen := filepath.Join(home, "state", "dispatch.error")
		_ = seen
		err := fmt.Errorf("alpha: %s: is alpha:p-%s alive: damaged role file", id, strings.ToLower(id))
		if got := alertKey(err); i == 0 {
			os.MkdirAll(filepath.Join(home, "state"), 0o755)
			os.WriteFile(seen, []byte(got), 0o644)
		} else if b, _ := os.ReadFile(seen); string(b) == got {
			t.Fatalf("AC13's error has AC12's key %q", got)
		}
	}
	if alertKey(errors.New("x surface:1000070039 workspace:12 after 5.7s")) != alertKey(errors.New("x surface:1000070040 workspace:13 after 6.1s")) {
		t.Fatal("new surfaces or durations made a new key")
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
