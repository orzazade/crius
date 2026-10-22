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
	if err := say(t.TempDir(), "quotes · lead", "check the build\nthen post DONE"); err != nil {
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
		err := say(home, "quotes · lead", text)
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
		{errors.New("lock: timed out"), 1},
	} {
		if got := exitCode(c.err); got != c.want {
			t.Errorf("exitCode(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}
