package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/outbox"
	"github.com/orzazade/crius/internal/t3cli"
	"github.com/orzazade/crius/internal/testutil"
	"github.com/orzazade/crius/internal/wakewatch"
)

// A message to a lead's tab goes in as one line, by the tab's title, and Enter sends it.
func TestSayTypesOneLineIntoATabByTitle(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ps"), []byte("#!/bin/sh\necho '7 7 /x/claude'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	old, oldWait := outbox.Cmux, outbox.BoxWait
	t.Cleanup(func() { outbox.Cmux, outbox.BoxWait = old, oldWait })
	outbox.BoxWait = time.Millisecond
	draft := ""
	var typed []string
	outbox.Cmux = func(args ...string) ([]byte, error) {
		switch args[0] {
		case "--json":
			return []byte(`{"windows":[{"workspaces":[{"ref":"workspace:2","panes":[{"surfaces":[` +
				`{"ref":"surface:9","title":"crius","tty":"ttys015","type":"terminal"},` +
				`{"ref":"surface:4","title":"quotes · lead","tty":"ttys015","type":"terminal"}]}]}]}]}`), nil
		case "read-screen":
			return []byte("──────────────\n❯ " + draft + "\n──────────────\n"), nil
		case "send":
			if args[4] != "surface:4" {
				t.Errorf("typed into %s, want the lead's tab surface:4", args[4])
			}
			draft = args[len(args)-1]
		case "send-key":
			typed, draft = append(typed, draft), ""
		}
		return nil, nil
	}
	if _, err := say(t.TempDir(), "quotes · lead", "check the build\nthen post DONE"); err != nil {
		t.Fatal(err)
	}
	if len(typed) != 1 || typed[0] != "check the build then post DONE" {
		t.Errorf("typed %q, want one line", typed)
	}
}

// Review 10-22: a long message was cut at 500 runes and a backslash became "/", and the caller never knew. Text that
// cannot go in as written is refused, and nothing is stored or typed.
func TestSayRefusesTextItWouldChange(t *testing.T) {
	old := outbox.Cmux
	t.Cleanup(func() { outbox.Cmux = old })
	outbox.Cmux = func(args ...string) ([]byte, error) {
		t.Errorf("cmux called: %q", args)
		return nil, nil
	}
	for name, text := range map[string]string{
		"too long":  strings.Repeat("x", wakewatch.MaxLine+1),
		"backslash": `see C:\build\log`,
		// Re-review 10-22: masking grows "pg://u:p@db" by a rune, past MaxLine, and the cut came after it.
		"too long once masked": strings.Repeat("x", wakewatch.MaxLine-12) + " pg://u:p@db",
	} {
		home := t.TempDir()
		_, err := say(home, "quotes · lead", text)
		if err == nil || !strings.Contains(err.Error(), "not sent") {
			t.Errorf("%s: err %v, want refused", name, err)
		}
		if left, _ := filepath.Glob(filepath.Join(home, "outbox", "*.json")); len(left) != 0 {
			t.Errorf("%s: stored %q", name, left)
		}
	}
}

// Re-review 10-22: every failure exited 1, so a caller could not tell a message stored for later (sending it again
// would type it twice) from one that was never stored (not sending it again loses it).
func TestExitCodeTellsHeldFromLost(t *testing.T) {
	for _, c := range []struct {
		err  error
		want int
	}{
		{nil, 0},
		{fmt.Errorf("x: %w", outbox.ErrHeld), 3},
		{fmt.Errorf("x: %w: set aside", outbox.ErrOnto), 3},
		{fmt.Errorf("x: %w, so it is not typed again", outbox.ErrMaybeSent), 3},
		{errors.New("lock: timed out"), 1},
	} {
		if got := exitCode(c.err); got != c.want {
			t.Errorf("exitCode(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}

// fakeT3 points outbox at testutil.FakeT3 and shortens the pause between looks for the proof.
func fakeT3(t *testing.T) (dir string) {
	t.Helper()
	bin, dir := testutil.FakeT3(t)
	old, oldWait, oldCmux := outbox.T3, t3cli.ProveWait, outbox.Cmux
	t.Cleanup(func() { outbox.T3, t3cli.ProveWait, outbox.Cmux = old, oldWait, oldCmux })
	outbox.T3, t3cli.ProveWait = bin, time.Millisecond
	outbox.Cmux = func(args ...string) ([]byte, error) {
		t.Errorf("cmux called for a role in a T3 thread: %q", args)
		return nil, errors.New("no cmux")
	}
	return dir
}

// A team role that runs as a T3 Code thread is named project:role: crius-say hands the line to its thread and says where
// it went, here queued as the busy thread's next turn (T3 never interrupts a run).
func TestSayToARoleInAT3ThreadSaysWhereItWent(t *testing.T) {
	dir := fakeT3(t)
	home := t.TempDir()
	testutil.Put(t, filepath.Join(home, "roles", "axi.qa.json"), `{"project":"axi","role":"qa","session_id":"s1","t3_thread":"mcp:qa"}`)
	testutil.Put(t, filepath.Join(dir, "send.out"), testutil.T3Sent("qa", 3))
	testutil.Put(t, filepath.Join(dir, "show"), testutil.T3Shown("qa", 2, 3))
	said, err := say(home, "axi:qa", "check the build\nthen post DONE")
	if code := exitCode(err); code != 0 {
		t.Fatalf("exit %d: %v", code, err)
	}
	if !strings.Contains(said, "mcp:qa") || !strings.Contains(said, "next turn") || !strings.Contains(said, "busy") {
		t.Errorf("said %q, want the thread, busy and next turn", said)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "stdin")); string(got) != "check the build then post DONE" {
		t.Errorf("t3 got %q, want one line", got)
	}
}

// Sent to a thread, but no look proved it: exit 3 (do not send again), never 0.
func TestSayToAT3ThreadWithNoProofExits3(t *testing.T) {
	dir := fakeT3(t)
	home := t.TempDir()
	testutil.Put(t, filepath.Join(home, "roles", "axi.qa.json"), `{"project":"axi","role":"qa","session_id":"s1","t3_thread":"mcp:qa"}`)
	testutil.Put(t, filepath.Join(dir, "send.out"), testutil.T3Sent("qa", 3))
	if _, err := say(home, "axi:qa", "check the build"); exitCode(err) != 3 {
		t.Errorf("exit %d (%v), want 3", exitCode(err), err)
	}
}
