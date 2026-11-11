package outbox

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeT3 replaces T3 with a script that keeps its arguments and stdin in dir, one call per line of calls, and exits as
// dir/fail says: "" (no file) exits 0, else it prints the file's text on stderr and exits 1.
func fakeT3(t *testing.T) (dir string) {
	dir = t.TempDir()
	script := filepath.Join(dir, "t3")
	body := "#!/bin/sh\n" +
		"d=" + dir + "\n" +
		"echo \"$*\" >> \"$d/calls\"\n" +
		"cat >> \"$d/stdin\"\n" +
		"if [ -f \"$d/fail\" ]; then cat \"$d/fail\" >&2; exit 1; fi\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	old := T3
	t.Cleanup(func() { T3 = old })
	T3 = script
	return dir
}

func threadFile(t *testing.T, home, body string) {
	t.Helper()
	dir := filepath.Join(home, "state", "crius")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "t3-thread"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readOr(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return string(b)
}

func storedFiles(t *testing.T, home string) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(home, "outbox", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

// Crius moves from a cmux tab into a T3 Code thread (owner, 2026-11-11): with ~/.crius/state/crius/t3-thread naming the
// thread, a message for Crius goes to `t3 thread send <id> -` with the text on stdin, and nothing is typed into cmux.
func TestCriusWithAT3ThreadGetsItsMessagesThroughT3(t *testing.T) {
	tab := &box{title: "crius"}
	fake(t, tab)
	dir := fakeT3(t)
	home := t.TempDir()
	threadFile(t, home, "mcp:5bba4b79-b3e9-4b48-954e-0f5fabcd580b\n")
	if err := Send(home, "crius", "WAKEWATCH: quotes:lead waits on you"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := readOr(t, filepath.Join(dir, "calls")); got != "thread send mcp:5bba4b79-b3e9-4b48-954e-0f5fabcd580b -\n" {
		t.Errorf("t3 called with %q", got)
	}
	if got := readOr(t, filepath.Join(dir, "stdin")); got != "WAKEWATCH: quotes:lead waits on you" {
		t.Errorf("t3 got stdin %q", got)
	}
	if len(tab.typed) != 0 || tab.draft != "" {
		t.Errorf("typed into cmux: %q, box %q", tab.typed, tab.draft)
	}
	if left := storedFiles(t, home); len(left) != 0 {
		t.Errorf("still stored after t3 took it: %q", left)
	}
}

// A t3 that fails is loud and keeps the message, as a cmux failure does: the next flush sends it once t3 works.
func TestT3FailureHoldsTheMessageAndSaysWhy(t *testing.T) {
	tab := &box{title: "crius"}
	fake(t, tab)
	dir := fakeT3(t)
	home := t.TempDir()
	threadFile(t, home, "mcp:abc\n")
	if since, _, err := Held(home, "crius"); err != nil || !since.IsZero() {
		t.Fatalf("Held before any try: %v, %v", since, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fail"), []byte("no T3 Code server is running\n    at node:internal/stack"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Send(home, "crius", "first")
	if !errors.Is(err, ErrHeld) || !strings.Contains(err.Error(), "no T3 Code server is running") || strings.Contains(err.Error(), "node:internal") {
		t.Fatalf("want a held error naming t3's reason on its first line only, got %v", err)
	}
	if left := storedFiles(t, home); len(left) != 1 {
		t.Fatalf("want the message stored, got %q", left)
	}
	// wakewatch's check of the thread reads what t3 did not take
	if since, why, err := Held(home, "crius"); err != nil || since.IsZero() || !strings.Contains(why, "no T3 Code server is running") {
		t.Fatalf("Held after a failed try: %v, %q, %v", since, why, err)
	}
	if since, _, err := Held(home, "quotes · lead"); err != nil || !since.IsZero() {
		t.Fatalf("Held for another tab: %v, %v", since, err)
	}
	if len(tab.typed) != 0 || tab.draft != "" {
		t.Errorf("fell back to cmux: %q, box %q", tab.typed, tab.draft)
	}
	if err := os.Remove(filepath.Join(dir, "fail")); err != nil {
		t.Fatal(err)
	}
	if aside, held := Flush(home); len(aside) != 0 || held != nil {
		t.Fatalf("Flush: aside %v, held %v", aside, held)
	}
	if got := readOr(t, filepath.Join(dir, "stdin")); got != "firstfirst" {
		t.Errorf("t3 stdin over both calls %q, want the text twice (failed try, then the flush)", got)
	}
	if left := storedFiles(t, home); len(left) != 0 {
		t.Errorf("still stored after the flush: %q", left)
	}
}

// Without the file nothing changes: Crius's messages are typed into its cmux tab, and t3 is never run.
func TestWithoutAT3ThreadCriusIsTypedIntoCmux(t *testing.T) {
	tab := &box{title: "crius"}
	fake(t, tab)
	dir := fakeT3(t)
	home := t.TempDir()
	if err := Send(home, "crius", "hello"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(tab.typed) != 1 || tab.typed[0] != "hello" {
		t.Errorf("typed %q", tab.typed)
	}
	if got := readOr(t, filepath.Join(dir, "calls")); got != "" {
		t.Errorf("t3 ran without a thread file: %q", got)
	}
}

// The thread is Crius's alone: another tab's messages still go to cmux while the file exists.
func TestOtherTabsStayOnCmuxWithAT3Thread(t *testing.T) {
	tab := &box{title: "quotes · lead"}
	fake(t, tab)
	dir := fakeT3(t)
	home := t.TempDir()
	threadFile(t, home, "mcp:abc\n")
	if err := Send(home, "quotes · lead", "hello"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(tab.typed) != 1 || tab.typed[0] != "hello" {
		t.Errorf("typed %q", tab.typed)
	}
	if got := readOr(t, filepath.Join(dir, "calls")); got != "" {
		t.Errorf("t3 ran for another tab: %q", got)
	}
}

// A thread file with no usable id is loud and holds the message: it sends to neither t3 nor Crius's old cmux tab.
func TestABadT3ThreadFileHoldsCriussMessages(t *testing.T) {
	for _, body := range []string{"", "\n", "mcp:a mcp:b\n", "mcp:a\nmcp:b\n", "--wait\n"} {
		tab := &box{title: "crius"}
		fake(t, tab)
		dir := fakeT3(t)
		home := t.TempDir()
		threadFile(t, home, body)
		err := Send(home, "crius", "hello")
		if !errors.Is(err, ErrHeld) || !strings.Contains(err.Error(), "t3-thread") {
			t.Errorf("%q: want a held error naming the file, got %v", body, err)
		}
		if got := readOr(t, filepath.Join(dir, "calls")); got != "" {
			t.Errorf("%q: t3 ran: %q", body, got)
		}
		if len(tab.typed) != 0 || tab.draft != "" {
			t.Errorf("%q: typed into cmux: %q, box %q", body, tab.typed, tab.draft)
		}
	}
}

// nvm keeps t3 off the PATH of launchd jobs (wakewatch) and other shells: the link in the Crius bin folder is used
// when there is one (review P2-1).
func TestT3LinkInTheCriusBinIsUsed(t *testing.T) {
	tab := &box{title: "crius"}
	fake(t, tab)
	dir := fakeT3(t)
	T3 = filepath.Join(t.TempDir(), "no-t3-here")
	home := t.TempDir()
	threadFile(t, home, "mcp:abc\n")
	if err := os.MkdirAll(filepath.Join(home, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "t3"), filepath.Join(home, "bin", "t3")); err != nil {
		t.Fatal(err)
	}
	if err := Send(home, "crius", "via the link"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := readOr(t, filepath.Join(dir, "stdin")); got != "via the link" {
		t.Errorf("t3 got stdin %q", got)
	}
}
