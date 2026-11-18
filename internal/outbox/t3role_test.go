package outbox

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/t3cli"
	"github.com/orzazade/crius/internal/testutil"
)

// provingT3 replaces T3 with testutil.FakeT3 and shortens the pause between looks for the proof.
func provingT3(t *testing.T) (dir string) {
	t.Helper()
	script, dir := testutil.FakeT3(t)
	old, oldWait := T3, t3cli.ProveWait
	t.Cleanup(func() { T3, t3cli.ProveWait = old, oldWait })
	T3, t3cli.ProveWait = script, time.Millisecond
	return dir
}

func putFile(t *testing.T, path, body string) { testutil.Put(t, path, body) }

// T3 answers, as printed on 11-19: the send names the run it made, the show lists the message under that run (idle) or
// names that run as the thread's latest while an earlier one is active (busy).
var (
	sentRun3   = testutil.T3Sent("qa", 3)
	showInRun3 = testutil.T3Shown("qa", 3, 3, "user", "3", "check the build")
	showBusy   = testutil.T3Shown("qa", 2, 3)
)

// noCmux fails the test on any cmux call: a role in a T3 thread has no tab to type into.
func noCmux(t *testing.T) {
	old := Cmux
	t.Cleanup(func() { Cmux = old })
	Cmux = func(args ...string) ([]byte, error) {
		t.Errorf("cmux called for a T3 role: %q", args)
		return nil, errors.New("no cmux")
	}
}

// qaInAThread files axi:qa as crius-hook does for a session that runs in a T3 Code thread: no tab, a thread.
func qaInAThread(t *testing.T, home string) {
	putFile(t, filepath.Join(home, "roles", "axi.qa.json"), `{"project":"axi","role":"qa","session_id":"s1","t3_thread":"mcp:qa"}`)
}

// A team role that runs as a T3 Code thread has no cmux tab: crius-say's message to it goes through `t3 thread send`,
// and is sent only once `t3 thread show` proves it is in the thread.
func TestARoleInAT3ThreadGetsItsMessageThroughT3WithProof(t *testing.T) {
	noCmux(t)
	dir := provingT3(t)
	home := t.TempDir()
	qaInAThread(t, home)
	putFile(t, filepath.Join(dir, "send.out"), sentRun3)
	putFile(t, filepath.Join(dir, "show"), showInRun3)
	said, err := SendSaid(home, "axi:qa", "check the build")
	if err != nil {
		t.Fatalf("SendSaid: %v", err)
	}
	if got := readOr(t, filepath.Join(dir, "calls")); got != "thread send --json mcp:qa -\nthread show --json --messages 20 mcp:qa\n" {
		t.Errorf("t3 calls %q", got)
	}
	if got := readOr(t, filepath.Join(dir, "stdin")); got != "check the build" {
		t.Errorf("t3 stdin %q", got)
	}
	if !strings.Contains(said, "mcp:qa") || !strings.Contains(said, "in the thread") {
		t.Errorf("said %q, want the thread and that it is in it", said)
	}
	if left := storedFiles(t, home); len(left) != 0 {
		t.Errorf("still stored after the proof: %q", left)
	}
}

// A busy thread takes the message as its next turn (T3 never interrupts a run), and the caller is told so.
func TestABusyThreadGetsTheMessageAsItsNextTurnAndSaysSo(t *testing.T) {
	noCmux(t)
	dir := provingT3(t)
	home := t.TempDir()
	qaInAThread(t, home)
	putFile(t, filepath.Join(dir, "send.out"), sentRun3)
	putFile(t, filepath.Join(dir, "show"), showBusy)
	said, err := SendSaid(home, "axi:qa", "check the build")
	if err != nil {
		t.Fatalf("SendSaid: %v", err)
	}
	if !strings.Contains(said, "busy") || !strings.Contains(said, "next turn") {
		t.Errorf("said %q, want busy and next turn", said)
	}
}

// A send t3 refused keeps the message stored (held: crius-say exits 3), and wakewatch's Flush delivers it later through
// T3, with the same proof.
func TestAT3MessageNotSentIsHeldAndFlushDeliversItLater(t *testing.T) {
	noCmux(t)
	dir := provingT3(t)
	home := t.TempDir()
	qaInAThread(t, home)
	putFile(t, filepath.Join(dir, "send.out"), "Server not running.\n  at stack")
	putFile(t, filepath.Join(dir, "send.rc"), "1")
	err := Send(home, "axi:qa", "check the build")
	if !errors.Is(err, ErrHeld) || !strings.Contains(err.Error(), "Server not running.") {
		t.Fatalf("Send: %v, want held with t3's reason", err)
	}
	if left := storedFiles(t, home); len(left) != 1 {
		t.Fatalf("stored %q, want the message", left)
	}
	os.Remove(filepath.Join(dir, "send.rc"))
	putFile(t, filepath.Join(dir, "send.out"), sentRun3)
	putFile(t, filepath.Join(dir, "show"), showInRun3)
	aside, held := Flush(home)
	if held != nil || len(aside) != 0 {
		t.Fatalf("Flush: aside %v, held %v", aside, held)
	}
	if left := storedFiles(t, home); len(left) != 0 {
		t.Errorf("still stored after Flush delivered it: %q", left)
	}
	if got := readOr(t, filepath.Join(dir, "stdin")); got != "check the buildcheck the build" {
		t.Errorf("t3 stdin %q, want the text sent twice (refused, then sent)", got)
	}
}

// Sent but not proved: it may be in the thread, so it is never sent again. It is set aside in outbox/dropped, named
// (crius-say exits 3; Flush lists it), never dropped without a word.
func TestAnUnprovedT3MessageIsSetAsideAndNamed(t *testing.T) {
	noCmux(t)
	dir := provingT3(t)
	home := t.TempDir()
	qaInAThread(t, home)
	putFile(t, filepath.Join(dir, "send.out"), sentRun3) // no show: every look fails
	err := Send(home, "axi:qa", "check the build")
	if !errors.Is(err, ErrMaybeSent) {
		t.Fatalf("Send: %v, want maybe sent", err)
	}
	if left := storedFiles(t, home); len(left) != 0 {
		t.Errorf("still stored, so a later run would send it again: %q", left)
	}
	if kept, _ := filepath.Glob(filepath.Join(home, "outbox", "dropped", "*.json")); len(kept) != 1 {
		t.Errorf("set aside %q, want one file in outbox/dropped", kept)
	}
	if n := strings.Count(readOr(t, filepath.Join(dir, "calls")), "thread send"); n != 1 {
		t.Errorf("%d sends, want 1", n)
	}
}

// Flush names an unproved message among what it set aside, so wakewatch reports it.
func TestFlushNamesAnUnprovedT3Message(t *testing.T) {
	noCmux(t)
	dir := provingT3(t)
	home := t.TempDir()
	qaInAThread(t, home)
	putFile(t, filepath.Join(dir, "send.rc"), "1")
	if err := Send(home, "axi:qa", "check the build"); !errors.Is(err, ErrHeld) {
		t.Fatalf("Send: %v, want held", err)
	}
	os.Remove(filepath.Join(dir, "send.rc"))
	putFile(t, filepath.Join(dir, "send.out"), sentRun3)
	aside, held := Flush(home)
	if held != nil || len(aside) != 1 || !errors.Is(aside[0], ErrMaybeSent) || !strings.Contains(aside[0].Error(), "axi:qa") {
		t.Errorf("Flush: aside %v, held %v; want the message named as maybe sent", aside, held)
	}
}

// A role file that cannot be read says the role may have moved: nothing is typed into a cmux tab of the same title, and
// the message is held, loud.
func TestADamagedRoleFileHoldsTheMessageLoudly(t *testing.T) {
	noCmux(t)
	provingT3(t)
	home := t.TempDir()
	putFile(t, filepath.Join(home, "roles", "axi.qa.json"), "{not json")
	err := Send(home, "axi:qa", "check the build")
	if !errors.Is(err, ErrHeld) || !strings.Contains(err.Error(), "axi.qa.json") {
		t.Errorf("Send: %v, want held naming the role file", err)
	}
}

// A role with a cmux tab, or a title that names no role, is typed into its tab as before: t3 is never run.
func TestARoleWithATabStaysOnCmux(t *testing.T) {
	for name, role := range map[string]string{
		"role with a tab": `{"project":"axi","role":"qa","session_id":"s1","workspace":"workspace:1","surface":"surface:1","t3_thread":"mcp:qa"}`,
		"no role file":    "",
	} {
		t.Run(name, func(t *testing.T) {
			tab := &box{title: "axi:qa"}
			fake(t, tab)
			dir := provingT3(t)
			home := t.TempDir()
			if role != "" {
				putFile(t, filepath.Join(home, "roles", "axi.qa.json"), role)
			}
			if err := Send(home, "axi:qa", "check the build"); err != nil {
				t.Fatalf("Send: %v", err)
			}
			if len(tab.typed) != 1 || tab.typed[0] != "check the build" {
				t.Errorf("typed %q", tab.typed)
			}
			if got := readOr(t, filepath.Join(dir, "calls")); got != "" {
				t.Errorf("t3 run: %q", got)
			}
		})
	}
}

// Review 11-19: a crius-say killed after its t3 send went, before the file was removed, left the message stored unmarked,
// and the next Flush sent it again. The send is marked before it goes, and a marked T3 message is set aside as maybe sent,
// never sent again.
func TestAT3MessageMarkedSentIsNotSentAgain(t *testing.T) {
	noCmux(t)
	dir := provingT3(t)
	home := t.TempDir()
	qaInAThread(t, home)
	putFile(t, filepath.Join(dir, "send.out"), sentRun3)
	putFile(t, filepath.Join(dir, "show"), showInRun3)
	if err := os.MkdirAll(filepath.Join(home, "outbox"), 0o700); err != nil {
		t.Fatal(err)
	}
	m, err := add(home, "axi:qa", "check the build")
	if err != nil {
		t.Fatal(err)
	}
	m.Entered = true // as a send left it when it was killed after t3 took the message
	if err := write(m.path, m.Message); err != nil {
		t.Fatal(err)
	}
	aside, held := Flush(home)
	if held != nil || len(aside) != 1 || !errors.Is(aside[0], ErrMaybeSent) {
		t.Errorf("Flush: aside %v, held %v; want it set aside as maybe sent", aside, held)
	}
	if got := readOr(t, filepath.Join(dir, "calls")); got != "" {
		t.Errorf("sent again: t3 calls %q", got)
	}
}

// A send t3 refused is unmarked again, so the next Flush may send it.
func TestAT3MessageT3RefusedStaysUnmarked(t *testing.T) {
	noCmux(t)
	dir := provingT3(t)
	home := t.TempDir()
	qaInAThread(t, home)
	putFile(t, filepath.Join(dir, "send.rc"), "1")
	if err := Send(home, "axi:qa", "check the build"); !errors.Is(err, ErrHeld) {
		t.Fatalf("Send: %v, want held", err)
	}
	all, err := list(home)
	if err != nil || len(all) != 1 || all[0].Entered {
		t.Errorf("stored %+v (%v), want one unmarked message", all, err)
	}
}
