package t3cli

import (
	"errors"
	"os"
	"path/filepath"

	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/testutil"
)

// fakeT3 is testutil.FakeT3 with Deliver's pause between looks shortened.
func fakeT3(t *testing.T) (bin, dir string) {
	t.Helper()
	bin, dir = testutil.FakeT3(t)
	old, oldWait := ProveTries, ProveWait
	t.Cleanup(func() { ProveTries, ProveWait = old, oldWait })
	ProveWait = time.Millisecond
	return bin, dir
}

func put(t *testing.T, dir, name, body string) { testutil.Put(t, filepath.Join(dir, name), body) }

func run(n int) string { return testutil.T3Run("abc", n) }

func sentAs(n int) string { return testutil.T3Sent("abc", n) }

func shown(active, latest int, msgs ...string) string {
	return testutil.T3Shown("abc", active, latest, msgs...)
}

// An idle thread starts a run on the message at once: the proof is the message itself in the thread, under the run the
// send named.
func TestDeliverProvesAMessageByItsRunInTheThread(t *testing.T) {
	bin, dir := fakeT3(t)
	put(t, dir, "send.out", sentAs(2))
	put(t, dir, "show", shown(2, 2, "user", "1", "wait", "assistant", "1", "ok", "user", "2", "check the build"))
	d, err := Deliver(bin, "mcp:abc", "check the build", time.Second)
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if d.Queued || d.RunID != run(2) {
		t.Errorf("got %+v, want run 2, in the thread", d)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "calls")); string(got) != "thread send --json mcp:abc -\nthread show --json --messages 20 mcp:abc\n" {
		t.Errorf("calls %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "stdin")); string(got) != "check the build" {
		t.Errorf("stdin %q", got)
	}
}

// A busy thread takes the message as its next turn (T3 never interrupts): show lists it in no message yet, but names its
// run as the thread's latest while an earlier run is active. That is proof, and Queued says so.
func TestDeliverProvesAQueuedMessageByTheThreadsLatestRun(t *testing.T) {
	bin, dir := fakeT3(t)
	put(t, dir, "send.out", sentAs(3))
	put(t, dir, "show", shown(2, 3, "user", "2", "build it"))
	d, err := Deliver(bin, "mcp:abc", "check the build", time.Second)
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if !d.Queued || d.RunID != run(3) {
		t.Errorf("got %+v, want run 3, queued", d)
	}
}

// A show that does not yet list the run is read again: T3 writes the run a moment after the send returns.
func TestDeliverLooksAgainUntilTheRunShows(t *testing.T) {
	bin, dir := fakeT3(t)
	put(t, dir, "send.out", sentAs(2))
	put(t, dir, "show.1", shown(0, 1, "user", "1", "wait"))
	put(t, dir, "show", shown(2, 2, "user", "2", "check the build"))
	if _, err := Deliver(bin, "mcp:abc", "check the build", time.Second); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
}

// A send t3 refused (an unknown thread, a server that is down) is not sent: the caller may send it again.
func TestDeliverSaysNotSentWhenTheSendFails(t *testing.T) {
	bin, dir := fakeT3(t)
	put(t, dir, "send.out", "Thread mcp:abc was not found.\n    at stack")
	put(t, dir, "send.rc", "1")
	_, err := Deliver(bin, "mcp:abc", "check the build", time.Second)
	if !errors.Is(err, ErrNotSent) || errors.Is(err, ErrUnproved) || !strings.Contains(err.Error(), "Thread mcp:abc was not found.") ||
		strings.Contains(err.Error(), "stack") {
		t.Errorf("err %v, want not sent with t3's first line", err)
	}
}

// Sent, but no look proves it: unproved, never "sent" and never "not sent" (sending it again could give it twice).
func TestDeliverSaysUnprovedWhenNoShowProvesIt(t *testing.T) {
	for name, c := range map[string]struct{ send, show string }{
		"run never shows":    {sentAs(4), shown(2, 3)},
		"other text in run":  {sentAs(2), shown(2, 2, "user", "2", "something else")},
		"show fails":         {sentAs(2), ""},
		"send printed no id": {"Sent.", shown(2, 2)},
	} {
		t.Run(name, func(t *testing.T) {
			bin, dir := fakeT3(t)
			put(t, dir, "send.out", c.send)
			if c.show != "" {
				put(t, dir, "show", c.show)
			}
			_, err := Deliver(bin, "mcp:abc", "check the build", time.Second)
			if !errors.Is(err, ErrUnproved) || errors.Is(err, ErrNotSent) {
				t.Errorf("err %v, want unproved", err)
			}
		})
	}
}

// A send killed at its time limit may have reached the server: unproved, not "not sent".
func TestDeliverSaysUnprovedWhenTheSendHangs(t *testing.T) {
	bin, dir := fakeT3(t)
	put(t, dir, "hang", "")
	_, err := Deliver(bin, "mcp:abc", "check the build", 200*time.Millisecond)
	if !errors.Is(err, ErrUnproved) || !errors.Is(err, ErrNoAnswer) {
		t.Errorf("err %v, want unproved after no answer", err)
	}
}

// Bin prefers the t3 link in the Crius bin folder (nvm keeps t3 off launchd's PATH), a dangling one too.
func TestBinPrefersTheCriusLink(t *testing.T) {
	home := t.TempDir()
	if got := Bin(home, "t3"); got != "t3" {
		t.Errorf("no link: %q", got)
	}
	if err := os.MkdirAll(filepath.Join(home, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/nowhere/t3", filepath.Join(home, "bin", "t3")); err != nil {
		t.Fatal(err)
	}
	if got := Bin(home, "t3"); got != filepath.Join(home, "bin", "t3") {
		t.Errorf("with a link: %q", got)
	}
}
