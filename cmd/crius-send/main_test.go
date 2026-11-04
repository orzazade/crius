package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/outbox"
)

// fakeTab replaces cmux with one Claude tab, surface:4 in workspace:2, and gives what was typed and pressed.
func fakeTab(t *testing.T, enterGoes bool) *[]string {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ps"), []byte("#!/bin/sh\necho '7 7 /x/claude'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("CRIUS_HOME", t.TempDir())
	old, oldWait, oldRetry := outbox.Cmux, outbox.BoxWait, outbox.RetryWait
	t.Cleanup(func() { outbox.Cmux, outbox.BoxWait, outbox.RetryWait = old, oldWait, oldRetry })
	outbox.BoxWait, outbox.RetryWait = time.Millisecond, time.Millisecond
	draft := ""
	var done []string
	outbox.Cmux = func(args ...string) ([]byte, error) {
		switch args[0] {
		case "--json":
			return []byte(`{"windows":[{"workspaces":[{"ref":"workspace:2","panes":[{"surfaces":[` +
				`{"ref":"surface:4","id":"S4","title":"staffy · backend-dev","tty":"ttys015","type":"terminal"}]}]}]}]}`), nil
		case "read-screen":
			return []byte("──────────────\n❯ " + draft + "\n──────────────\n"), nil
		case "rpc":
			state := "empty"
			if draft != "" {
				state = "draft"
			}
			return []byte(`{"surface_id":"S4","state":"` + state + `"}`), nil
		case "send":
			draft = args[len(args)-1]
			done = append(done, "typed "+draft)
		case "send-key":
			done = append(done, "Enter")
			if enterGoes {
				draft = ""
			}
		}
		return nil, nil
	}
	return &done
}

// X14 (staffy:leader via Crius 11-04 16:35): crius-send <workspace> <surface> <text> types, presses Enter once the box
// holds the text, and exits 0 only when the box no longer holds it; 1 and a loud NOT SENT when it stays.
func TestSendTypesPressesEnterAndProvesIt(t *testing.T) {
	done := fakeTab(t, true)
	var out, errs strings.Builder
	if code := run([]string{"workspace:2", "surface:4", "read", "your brief"}, &out, &errs); code != 0 {
		t.Fatalf("exit %d: %s", code, errs.String())
	}
	if strings.Join(*done, "|") != "typed read your brief|Enter" || !strings.Contains(out.String(), "SENT surface:4") {
		t.Fatalf("%q %q", *done, out.String())
	}
	done = fakeTab(t, false) // every Enter goes nowhere
	out.Reset()
	errs.Reset()
	if code := run([]string{"workspace:2", "surface:4", "read your brief"}, &out, &errs); code != 1 ||
		!strings.Contains(errs.String(), "NOT SENT surface:4: ") || !strings.Contains(errs.String(), "still holds the message after Enter") ||
		strings.Count(strings.Join(*done, "|"), "Enter") != 3 {
		t.Fatalf("exit %d: %q %q", code, errs.String(), *done)
	}
}

// Review of cffd3a6: an Enter that may have gone, but could not be proven (the box gone after it), exits 3, MAYBE SENT:
// a caller must look at the tab, not send again.
func TestSendSaysMaybeSentWhenTheBoxIsGoneAfterEnter(t *testing.T) {
	done := fakeTab(t, true)
	old := outbox.Cmux
	pressed := false
	outbox.Cmux = func(args ...string) ([]byte, error) {
		if args[0] == "send-key" {
			pressed = true
		}
		if args[0] == "read-screen" && pressed {
			return []byte("Select model\n❯ 1. Default\n"), nil
		}
		return old(args...)
	}
	var out, errs strings.Builder
	if code := run([]string{"workspace:2", "surface:4", "/model"}, &out, &errs); code != 3 || !strings.Contains(errs.String(), "MAYBE SENT surface:4: ") {
		t.Fatalf("exit %d: %q %q", code, errs.String(), *done)
	}
}

// A bare number is text to cmux and goes to the caller's own tab; a line break would press Enter early, a backslash is a
// key, and a line over 500 runes is cut by the box rules crius-say keeps. Each is refused with nothing typed.
func TestSendRefusesWhatCouldGoWrong(t *testing.T) {
	for name, args := range map[string][]string{
		"too few":            {"workspace:2", "surface:4"},
		"bare workspace":     {"2", "surface:4", "go"},
		"bare surface":       {"workspace:2", "1000050125", "go"},
		"a line break":       {"workspace:2", "surface:4", "go\non"},
		"a backslash":        {"workspace:2", "surface:4", `go\n`},
		"501 runes":          {"workspace:2", "surface:4", strings.Repeat("a", 501)},
		"only spaces":        {"workspace:2", "surface:4", "   "},
		"surface as a title": {"workspace:2", "staffy · backend-dev", "go"},
	} {
		done := fakeTab(t, true)
		var out, errs strings.Builder
		if code := run(args, &out, &errs); code != 2 || len(*done) != 0 || errs.Len() == 0 {
			t.Errorf("%s: exit %d, did %q, said %q", name, code, *done, errs.String())
		}
	}
}
