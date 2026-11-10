package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
