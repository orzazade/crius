package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 10, 16, 0, 0, 0, time.FixedZone("+04", 4*3600))

func cli(t *testing.T, home string, min int, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := run(home, t0.Add(time.Duration(min)*time.Minute), args, &out)
	return out.String(), err
}

func must(t *testing.T, home string, min int, args ...string) string {
	t.Helper()
	out, err := cli(t, home, min, args...)
	if err != nil {
		t.Fatalf("%q: %v", args, err)
	}
	return out
}

// The board a person or the dispatcher drives: add rows, move them, list them, pick the next one.
func TestAddSetListNext(t *testing.T) {
	home := t.TempDir()
	must(t, home, 0, "demo", "add", "AC1", "--text", "A seller sees the reach of each customer", "--files", "src/reach/**,server/Reach/**",
		"--mockup", "mockups/reach.png", "--story", "4242")
	must(t, home, 0, "demo", "add", "AC2", "--text", "A seller filters the list", "--files", "src/list/**")
	for i, s := range []string{"check-written", "ready"} {
		must(t, home, i+1, "demo", "set", "AC1", s, "--by", "crius:lead")
		must(t, home, i+1, "demo", "set", "AC2", s, "--by", "crius:lead")
	}
	if out := must(t, home, 3, "demo", "next"); out != "AC1\n" {
		t.Fatalf("next %q", out)
	}
	must(t, home, 3, "demo", "set", "AC1", "building", "--by", "crius:dispatch", "--session", "demo:pilot-ac1", "--why", "picked")
	must(t, home, 4, "demo", "set", "AC1", "pr", "--pr", "8101")
	out := must(t, home, 5, "demo", "list")
	want := "AC1 pr demo:pilot-ac1 PR 8101 · A seller sees the reach of each customer\nAC2 ready · A seller filters the list\n"
	if out != want {
		t.Fatalf("list\n%q\nwant\n%q", out, want)
	}
	if out := must(t, home, 5, "demo", "list", "--state", "ready"); out != "AC2 ready · A seller filters the list\n" {
		t.Fatalf("list --state ready %q", out)
	}
	if out := must(t, home, 5, "demo", "next"); out != "AC2\n" {
		t.Fatalf("next %q", out)
	}
	must(t, home, 6, "demo", "set", "AC2", "blocked", "--question", "which list?")
	if _, err := cli(t, home, 6, "demo", "next"); !errors.Is(err, errNone) {
		t.Fatalf("want errNone (exit 3) with nothing ready, got %v", err)
	}
	ev, _ := os.ReadFile(filepath.Join(home, "state", "demo", "BOARD.events.log"))
	if !strings.Contains(string(ev), "2026-11-10T16:03:00+04:00 AC1 ready -> building by crius:dispatch: picked\n") {
		t.Fatalf("events %q", ev)
	}
}

// Bad arguments are a usage error (exit 2); a move the board refuses is an error (exit 1) that says why.
func TestErrors(t *testing.T) {
	home := t.TempDir()
	for _, args := range [][]string{
		{}, {"demo"}, {"../x", "list"}, {"demo", "fly"}, {"demo", "add", "AC1"}, {"demo", "set", "AC1"},
		{"demo", "set", "AC1", "ready", "--pr", "x"},
	} {
		var u usage
		if _, err := cli(t, home, 0, args...); !errors.As(err, &u) {
			t.Errorf("%q: want a usage error, got %v", args, err)
		}
	}
	must(t, home, 0, "demo", "add", "AC1", "--text", "x")
	_, err := cli(t, home, 1, "demo", "set", "AC1", "building")
	var u usage
	if err == nil || errors.As(err, &u) || !strings.Contains(err.Error(), "AC1: open -> building is not allowed") {
		t.Fatalf("want the refusal, got %v", err)
	}
}

// Review P3-9/10: --files splits on commas outside braces; set on a project with no board says so and leaves no
// folder behind; a negative --pr is a usage error.
func TestBraceFilesAMissingBoardAndANegativePR(t *testing.T) {
	home := t.TempDir()
	must(t, home, 0, "demo", "add", "AC1", "--text", "x", "--files", "src/*.{ts,tsx}, server/a.cs")
	out := must(t, home, 0, "demo", "list")
	if out != "AC1 open · x\n" {
		t.Fatalf("list %q", out)
	}
	b, _ := os.ReadFile(filepath.Join(home, "state", "demo", "BOARD.toml"))
	if !strings.Contains(string(b), `files = ["src/*.{ts,tsx}", "server/a.cs"]`) {
		t.Fatalf("files split wrong:\n%s", b)
	}
	_, err := cli(t, home, 1, "demoo", "set", "AC1", "check-written")
	if err == nil || !strings.Contains(err.Error(), "project demoo has no board") {
		t.Fatalf("want no board, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, "state", "demoo")); !os.IsNotExist(err) {
		t.Fatalf("a mistyped project left a folder: %v", err)
	}
	var u usage
	if _, err := cli(t, home, 1, "demo", "set", "AC1", "check-written", "--pr", "-5"); !errors.As(err, &u) {
		t.Fatalf("want a usage error for --pr -5, got %v", err)
	}
}
