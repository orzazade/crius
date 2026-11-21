package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/tabsend"
	"github.com/orzazade/crius/internal/testutil"
)

// tmuxServer starts a tmux server of the test's own (TestMain points TMUX_TMPDIR at an empty folder, so no test reaches
// the real one) with session "crius", window name running command, and stops the server when the test ends.
func tmuxServer(t *testing.T, name string, command ...string) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("no tmux on this machine")
	}
	args := append([]string{"new-session", "-d", "-x", "250", "-y", "40", "-s", "crius", "-n", name}, command...)
	if out, err := exec.Command("tmux", args...).CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session: %v: %s", err, out)
	}
	t.Cleanup(func() { exec.Command("tmux", "kill-server").Run() })
}

// fakePromptIn starts a server whose window name runs the fake Claude box, and waits until the box is drawn.
func fakePromptIn(t *testing.T, name string) {
	t.Helper()
	tmuxServer(t, name, fakePrompt(t))
	for range 100 {
		out, _ := exec.Command("tmux", "capture-pane", "-p", "-e", "-t", "crius:"+name).CombinedOutput()
		if _, boxed := tabsend.Box(string(out)); boxed {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the fake box never showed in crius:%s:\n%s", name, screenOf(t, "crius:"+name))
}

// fakePrompt builds the stand-in for Claude Code's input box that tabsend's live check uses, and gives its path.
func fakePrompt(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "fakeprompt")
	if out, err := exec.Command("go", "build", "-o", bin, "github.com/orzazade/crius/internal/tabsend/fakeprompt/run").CombinedOutput(); err != nil {
		t.Fatalf("go build fakeprompt: %v: %s", err, out)
	}
	return bin
}

func screenOf(t *testing.T, target string) string {
	t.Helper()
	out, err := exec.Command("tmux", "capture-pane", "-p", "-t", target).CombinedOutput()
	if err != nil {
		t.Fatalf("tmux capture-pane: %v: %s", err, out)
	}
	return string(out)
}

// 11-21 22:08 and 22:52: bus-post on the ThinkPad posted a DECISION to staffy:builder, a Claude Code tab in the tmux
// window staffy:builder there, said "no tab and no T3 Code thread filed", and woke no one; Crius typed it in by hand with
// crius-tsend. A line that asks a role in a local tmux window to act is typed into that window, as crius-tsend types it.
func TestALineToARoleInATmuxWindowIsTypedIntoIt(t *testing.T) {
	home, log := setup(t)
	// As crius-hook files a session outside cmux and T3 Code (the ThinkPad's staffy.builder.json): no tab, no thread.
	testutil.Write(t, filepath.Join(home, "roles", "crius.tx.json"), `{"session_id":"s-tx","project":"crius","role":"tx"}`)
	fakePromptIn(t, "tx")
	rc, errs := post(t, at("2026-11-21T22:08"), log, "DECISION", "crius:lead", "crius:tx", "go on with S-12")
	want := "2026-11-21T22:08 DECISION crius:lead -> crius:tx — go on with S-12"
	if busText(t, log) != want+"\n" {
		t.Fatalf("rc %d: %s\nbus: %q", rc, errs, busText(t, log))
	}
	if rc != 0 || strings.Contains(errs, "no tab and no T3") || !strings.Contains(errs, "delivered to crius:tx (tmux "+boxName()+")") {
		t.Fatalf("rc %d, stderr %q", rc, errs)
	}
	if s := screenOf(t, "crius:tx"); !strings.Contains(s, "go on with S-12") {
		t.Errorf("the window does not show the line:\n%s", s)
	}
}

// The Legion keeps no role files at all: a role with none, in a local tmux window, is typed into too.
func TestARoleWithNoRoleFileInATmuxWindowIsTypedIntoIt(t *testing.T) {
	_, log := setup(t)
	fakePromptIn(t, "tx")
	rc, errs := post(t, at("2026-11-21T22:52"), log, "ORDER", "crius:lead", "crius:tx", "rebase on develop")
	if rc != 0 || !strings.Contains(errs, "delivered to crius:tx (tmux "+boxName()+")") {
		t.Fatalf("rc %d, stderr %q", rc, errs)
	}
}

// A window of the role's name that shows no Claude Code box takes nothing, and the post says so and exits 8: never silent.
func TestAWindowWithNoClaudeBoxIsNotDeliveredAndSaysSo(t *testing.T) {
	home, log := setup(t)
	testutil.Write(t, filepath.Join(home, "roles", "crius.tx.json"), `{"session_id":"s-tx","project":"crius","role":"tx"}`)
	tmuxServer(t, "tx", "sleep", "600")
	rc, errs := post(t, at("2026-11-21T22:08"), log, "DECISION", "crius:lead", "crius:tx", "go on with S-12")
	if rc != rcNotDelivered || !strings.Contains(errs, "NOT delivered to crius:tx (tmux "+boxName()+" crius:tx)") ||
		!strings.Contains(errs, "crius-tsend "+boxName()+" crius:tx") {
		t.Fatalf("rc %d, stderr %q", rc, errs)
	}
}

// An INFO is not typed in, as it is not sent to a thread; a window of another name is not the role's.
func TestOnlyActTypesToTheRolesOwnWindowAreTyped(t *testing.T) {
	home, log := setup(t)
	testutil.Write(t, filepath.Join(home, "roles", "crius.tx.json"), `{"session_id":"s-tx","project":"crius","role":"tx"}`)
	tmuxServer(t, "tx", "sleep", "600") // no box: a send would fail loud
	if rc, errs := post(t, at("2026-11-21T22:08"), log, "INFO", "crius:lead", "crius:tx", "fyi"); rc != 0 || strings.Contains(errs, "tmux") {
		t.Errorf("INFO: rc %d, stderr %q", rc, errs)
	}
	testutil.Write(t, filepath.Join(home, "roles", "crius.ty.json"), `{"session_id":"s-ty","project":"crius","role":"ty"}`)
	rc, errs := post(t, at("2026-11-21T22:09"), log, "DECISION", "crius:lead", "crius:ty", "go")
	if rc != 0 || !strings.Contains(errs, "crius:ty has no tab and no T3 Code thread filed") || strings.Contains(errs, "(tmux") {
		t.Errorf("other window: rc %d, stderr %q", rc, errs)
	}
}

// Every test runs against a tmux server of its own: TestMain's TMUX_TMPDIR is not the real one.
func TestTestsNeverReachTheRealTmuxServer(t *testing.T) {
	if os.Getenv("TMUX") != "" || !strings.HasPrefix(os.Getenv("TMUX_TMPDIR"), os.TempDir()) && !strings.HasPrefix(os.Getenv("TMUX_TMPDIR"), "/tmp/") {
		t.Fatalf("TMUX %q, TMUX_TMPDIR %q", os.Getenv("TMUX"), os.Getenv("TMUX_TMPDIR"))
	}
}
