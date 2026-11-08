package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/sessionstate"
	"github.com/orzazade/crius/internal/testutil"
)

// moveRig is a role whose session runs on max1 (folder m1) in tab ws/sf, with max2 (folder m2) picked by the rule.
type moveRig struct {
	home, m1, m2, id string
	state            string // crius-hook's state of the session
	children         []proc // the old Claude's child processes
	typed            []string
	resumed          []string // project role id cwd acct dir of each resume
	exits            bool     // /exit stops the old Claude
	resumeIn         string   // the folder the resumed Claude really runs in
	m                mover
}

func newMoveRig(t *testing.T) *moveRig {
	t.Helper()
	x := &moveRig{home: t.TempDir(), id: "6f1c2a9e-3b4d-4e5f-8a7b-9c0d1e2f3a4b", state: sessionstate.Idle, exits: true}
	d := t.TempDir()
	x.m1, x.m2 = filepath.Join(d, ".claude-m1"), filepath.Join(d, ".claude-m2")
	x.resumeIn = x.m2
	testutil.Write(t, filepath.Join(x.m1, ".claude.json"), `{"oauthAccount":{"accountUuid":"acct-1"}}`)
	testutil.Write(t, filepath.Join(x.m2, ".claude.json"), `{"oauthAccount":{"accountUuid":"acct-2"}}`)
	testutil.Write(t, filepath.Join(x.home, "accounts.toml"), "[[account]]\nname = \"max1\"\nplan = \"max\"\ndirs = [\""+x.m1+
		"\"]\n[[account]]\nname = \"max2\"\nplan = \"max\"\ndirs = [\""+x.m2+"\"]\n")
	s := sessionstate.State{SessionID: x.id, Cwd: "/w/team/wt", Project: "team", Role: "dev", Config: x.m1, Workspace: "WS-1", Surface: "SF-1"}
	if err := sessionstate.WriteRole(filepath.Join(x.home, "roles"), s, true); err != nil {
		t.Fatal(err)
	}
	run := func(dir string) {
		status := fmt.Sprintf(`{"pid":%d,"sessionId":%q,"procStart":%q}`, os.Getpid(), x.id, testutil.ProcStart(t, os.Getpid()))
		testutil.Write(t, filepath.Join(dir, "sessions", "1.json"), status)
	}
	run(x.m1)
	x.m = mover{
		home: x.home,
		pick: func(project, want string) (string, string, error) { return "max2", x.m2, nil },
		state: func(id string) (string, error) {
			return x.state, nil
		},
		procs: func() ([]proc, error) {
			return append([]proc{{pid: os.Getpid(), ppid: 1, command: "claude"}}, x.children...), nil
		},
		cmux: func(args ...string) (string, error) {
			x.typed = append(x.typed, strings.Join(args, " "))
			if len(args) > 0 && args[0] == "send-key" && x.exits && strings.Contains(strings.Join(x.typed, "\n"), "/exit") {
				if err := os.Remove(filepath.Join(x.m1, "sessions", "1.json")); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
			}
			return "", nil
		},
		resume: func(project, role, id, cwd, acct, dir string) (string, string, error) {
			x.resumed = append(x.resumed, strings.Join([]string{project, role, id, cwd, acct, dir}, " "))
			run(x.resumeIn)
			return "WS-1", "SF-1", nil
		},
		tries: 3,
	}
	return x
}

// crius:lead 11-08: max1 at 95% with 6 tabs on it; a move must resume the SAME session on the next account by the rule,
// at a turn boundary, never cutting a running background build, and show the new Claude's folder.
func TestAMoveResumesTheSameSessionOnThePickedAccount(t *testing.T) {
	x := newMoveRig(t)
	x.children = []proc{{pid: 70, ppid: os.Getpid(), command: "/bin/zsh -c eval '~/.claude/skills/cmux-team/bin/team-wait.sh --mode worker'"},
		{pid: 71, ppid: os.Getpid(), command: "node /x/mcp-server.js"}}
	out, err := x.m.move("team", "dev")
	if err != nil {
		t.Fatalf("idle with only its waiter: %v", err)
	}
	if want := "team dev " + x.id + " /w/team/wt max2 " + x.m2; len(x.resumed) != 1 || x.resumed[0] != want {
		t.Fatalf("resumed %q, want %q", x.resumed, want)
	}
	if !strings.Contains(strings.Join(x.typed, "\n"), "send --workspace WS-1 --surface SF-1 -- /exit") {
		t.Errorf("typed %q: want /exit into its own tab", x.typed)
	}
	// crius:lead 11-08: a moved tab still filed on its old folder was flagged "wrong account" and -resume would bring
	// it back there: the move files the new folder.
	if r, err := sessionstate.ReadRole(filepath.Join(x.home, "roles"), "team", "dev"); err != nil || r.Config != x.m2 || r.FiledConfig() != x.m2 {
		t.Errorf("role file after the move: config %q filed %q (%v), want both %q", r.Config, r.FiledConfig(), err, x.m2)
	}
	for _, w := range []string{"session " + x.id, fmt.Sprintf("pid %d", os.Getpid()), "CLAUDE_CONFIG_DIR " + x.m2, "max1 -> max2"} {
		if !strings.Contains(out, w) {
			t.Errorf("out %q lacks %q", out, w)
		}
	}
}

func TestAMoveWaitsForTheTurnAndBackgroundJobs(t *testing.T) {
	x := newMoveRig(t)
	x.state = sessionstate.Running
	if _, err := x.m.move("team", "dev"); err == nil || !strings.Contains(err.Error(), "a turn is running") {
		t.Errorf("running turn: %v", err)
	}
	x = newMoveRig(t)
	x.children = []proc{{pid: 72, ppid: os.Getpid(), command: "/bin/zsh -c eval 'npm run build 2>&1 | tail -5'"}}
	if _, err := x.m.move("team", "dev"); err == nil || !strings.Contains(err.Error(), "1 background job runs") ||
		!strings.Contains(err.Error(), "npm run build") {
		t.Errorf("background build: %v", err)
	}
	for _, x := range []*moveRig{x} {
		if len(x.typed) != 0 || len(x.resumed) != 0 {
			t.Errorf("refused, yet typed %q resumed %q", x.typed, x.resumed)
		}
	}
}

// Live 11-08: aitch was handed --primary ~/.claude-max2 and put the session on its fallback max1 again; a move that
// lands anywhere but the picked folder fails, naming where it runs.
func TestAMoveThatLandsElsewhereFails(t *testing.T) {
	x := newMoveRig(t)
	x.resumeIn = x.m1
	if _, err := x.m.move("team", "dev"); err == nil || !strings.Contains(err.Error(), "runs in "+x.m1+", not "+x.m2) {
		t.Errorf("landed on max1: %v", err)
	}
	x = newMoveRig(t)
	x.m.pick = func(project, want string) (string, string, error) { return "max1", x.m1, nil }
	if _, err := x.m.move("team", "dev"); err == nil || !strings.Contains(err.Error(), "already on max1") {
		t.Errorf("pick is its own account: %v", err)
	}
	x = newMoveRig(t)
	x.exits = false
	if _, err := x.m.move("team", "dev"); err == nil || !strings.Contains(err.Error(), "did not exit") || len(x.resumed) != 0 {
		t.Errorf("did not exit: %v, resumed %q", err, x.resumed)
	}
}

// Review of fe19215 P2-2: a bus line can start a turn between the idle check and Enter; /exit then waits behind the turn.
// The move waits for it, where it used to give up after its reads and leave the queued /exit to end the session later.
func TestAMoveWaitsForATurnThatStartedAfterExit(t *testing.T) {
	x := newMoveRig(t)
	x.exits = false
	reads := 0
	x.m.state = func(id string) (string, error) {
		if !strings.Contains(strings.Join(x.typed, "\n"), "/exit") {
			return sessionstate.Idle, nil
		}
		reads++
		if reads == 6 { // the turn ends and the queued /exit runs
			if err := os.Remove(filepath.Join(x.m1, "sessions", "1.json")); err != nil {
				t.Fatal(err)
			}
			return sessionstate.Idle, nil
		}
		return sessionstate.Running, nil
	}
	if _, err := x.m.move("team", "dev"); err != nil || len(x.resumed) != 1 {
		t.Fatalf("turn after /exit: %v, resumed %q", err, x.resumed)
	}
}

// Review of fe19215 P2-4: the old Claude can still be in front of its tab after its sessions file is gone; Resume then
// refuses "not a shell". The move tries again, and a resume that fails names the folder the session is now filed on.
func TestAMoveRetriesTheResumeAndFilesTheNewFolderFirst(t *testing.T) {
	x := newMoveRig(t)
	resume := x.m.resume
	x.m.resume = func(project, role, id, cwd, acct, dir string) (string, string, error) {
		if r, err := sessionstate.ReadRole(filepath.Join(x.home, "roles"), "team", "dev"); err != nil || r.FiledConfig() != x.m2 {
			t.Errorf("at the resume the role is filed on %q (%v), want %q", r.FiledConfig(), err, x.m2)
		}
		if len(x.resumed) == 0 {
			x.resumed = append(x.resumed, "refused")
			return "", "", fmt.Errorf("team · dev (WS-1 SF-1) runs claude in front, not a shell: left alone")
		}
		return resume(project, role, id, cwd, acct, dir)
	}
	if out, err := x.m.move("team", "dev"); err != nil || len(x.resumed) != 2 || !strings.Contains(out, "max1 -> max2") {
		t.Fatalf("claude still in front once: %v %q, resumed %q", err, out, x.resumed)
	}
	x = newMoveRig(t)
	x.m.resume = func(project, role, id, cwd, acct, dir string) (string, string, error) {
		return "", "", fmt.Errorf("two tabs are titled team · dev")
	}
	_, err := x.m.move("team", "dev")
	if err == nil || !strings.Contains(err.Error(), "filed on "+x.m2+", but did not resume") || !strings.Contains(err.Error(), "crius-spawn -resume team dev") {
		t.Errorf("resume failed: %v", err)
	}
}

// Review of fe19215 P2-3: a background subagent or workflow runs inside Claude with no shell child, so the idle check
// cannot see it, and the move pressed Enter on Claude's "Exit and stop tasks" dialog whatever it listed. It is accepted
// only when it lists the session's bus waiters alone; else the dialog is left with Escape and nothing moves.
func TestAMoveStopsOnlyItsBusWaiterAtExit(t *testing.T) {
	// the live layout (11-08, quotes qa-frontend)
	dialog := func(tasks ...string) string {
		return "✻ Cooked for 19s · done 6:28 AM\n▔▔▔▔▔▔▔▔\n   Background work is running\n   The following will stop when you exit:\n\n   " +
			strings.Join(tasks, "\n   ") + "\n\n   ❯ 1. Exit and stop tasks\n     2. Move to background and exit\n     3. Stay\n\n   " +
			"Enter to confirm · Esc to cancel\n"
	}
	waiter := "shell · B=~/.crius/state/quotes/build; cd $B && ~/.claude…"
	for _, c := range []struct {
		name   string
		screen string
		moved  bool
	}{
		{"its waiter", dialog(waiter), true},
		{"a subagent too", dialog(waiter, "agent · Deep review of the branch"), false},
		{"a subagent, not its waiter", dialog("agent · Deep review of the branch"), false},
		{"more than it lists", dialog(waiter, "+ 2 more items"), false},
	} {
		x := newMoveRig(t)
		x.children = []proc{{pid: 70, ppid: os.Getpid(), command: "/bin/zsh -c eval '~/.claude/skills/cmux-team/bin/team-wait.sh --mode worker'"}}
		x.exits = false
		shown := false
		x.m.cmux = func(args ...string) (string, error) {
			x.typed = append(x.typed, strings.Join(args, " "))
			switch {
			case args[0] == "read-screen" && strings.Contains(strings.Join(x.typed, "\n"), "/exit"):
				shown = true
				return c.screen, nil
			case args[0] == "send-key" && shown && args[len(args)-1] == "enter":
				if err := os.Remove(filepath.Join(x.m1, "sessions", "1.json")); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
			}
			return "", nil
		}
		_, err := x.m.move("team", "dev")
		last := x.typed[len(x.typed)-1]
		// live 11-08 (quotes qa-frontend): cmux refuses a key while a dialog is open unless forced
		if !strings.HasPrefix(last, "send-key --force ") {
			t.Errorf("%s: dialog answered with %q, want send-key --force", c.name, last)
		}
		if c.moved && (err != nil || len(x.resumed) != 1 || !strings.HasSuffix(last, " enter")) {
			t.Errorf("%s: %v, resumed %q, last key %q", c.name, err, x.resumed, last)
		}
		if !c.moved && (err == nil || !strings.Contains(err.Error(), "besides its 1 bus waiter") || len(x.resumed) != 0 || !strings.HasSuffix(last, " escape")) {
			t.Errorf("%s: %v, resumed %q, last key %q", c.name, err, x.resumed, last)
		}
	}
}

// Review r2 P2-1, P2-2: the dialog was matched by its words anywhere on the screen (a conversation that quotes them), and
// was answered again on every read, so an exit already accepted could get an Escape and a false "nothing moved".
func TestAMoveAnswersOnlyAnOpenDialogAndOnlyOnce(t *testing.T) {
	quoted := "⏺ The move presses Enter on \"Exit and stop tasks\" when the following will stop when you exit: only its waiter\n\n❯ \n"
	for _, c := range []struct {
		name    string
		screens []string
	}{
		{"quoted in the conversation", []string{quoted, quoted}},
		{"read again after Enter", []string{
			"   The following will stop when you exit:\n\n   shell · team-wait\n\n   ❯ 1. Exit and stop tasks\n     3. Stay\n\n   Enter to confirm · Esc to cancel\n",
			"   The following will stop when you exit:\n\n   shell · team-wait\n   stopping…\n\n   ❯ 1. Exit and stop tasks\n     3. Stay\n\n   Enter to confirm · Esc to cancel\n"}},
	} {
		x := newMoveRig(t)
		x.children = []proc{{pid: 70, ppid: os.Getpid(), command: "/bin/zsh -c eval '~/.claude/skills/cmux-team/bin/team-wait.sh --mode worker'"}}
		x.exits = false
		reads, passes := 0, 0
		x.m.state = func(string) (string, error) {
			if strings.Contains(strings.Join(x.typed, "\n"), "/exit") {
				if passes++; passes == 4 { // Claude ends on the fourth pass after /exit
					if err := os.Remove(filepath.Join(x.m1, "sessions", "1.json")); err != nil {
						t.Fatal(err)
					}
				}
			}
			return sessionstate.Idle, nil
		}
		x.m.cmux = func(args ...string) (string, error) {
			x.typed = append(x.typed, strings.Join(args, " "))
			if args[0] == "read-screen" {
				reads++
				return c.screens[min(reads, len(c.screens))-1], nil
			}
			return "", nil
		}
		x.m.tries = 5
		if _, err := x.m.move("team", "dev"); err != nil || len(x.resumed) != 1 {
			t.Errorf("%s: %v, resumed %q", c.name, err, x.resumed)
		}
		if n := strings.Count(strings.Join(x.typed, "\n"), "escape"); n != 0 {
			t.Errorf("%s: %d escapes typed: %q", c.name, n, x.typed)
		}
	}
}

// Review r3 P1: the waiters were counted before /exit. A turn that a bus line started in between can end the waiter and
// start a build; the dialog lists it cut short like a waiter, one line for one waiter, and Enter killed the build.
// The shell children are counted again when the dialog is open.
func TestAMoveCountsTheJobsAgainAtTheDialog(t *testing.T) {
	x := newMoveRig(t)
	x.children = []proc{{pid: 70, ppid: os.Getpid(), command: "/bin/zsh -c eval '~/.claude/skills/cmux-team/bin/team-wait.sh --mode worker'"}}
	x.exits = false
	x.m.cmux = func(args ...string) (string, error) {
		x.typed = append(x.typed, strings.Join(args, " "))
		if args[0] == "read-screen" {
			// the waiter ended and a build runs in its place
			x.children = []proc{{pid: 72, ppid: os.Getpid(), command: "/bin/zsh -c eval 'B=~/.crius/state/quotes/build; cd $B && npm run build'"}}
			return "   The following will stop when you exit:\n\n   shell · B=~/.crius/state/quotes/build; cd $B && npm…\n\n" +
				"   ❯ 1. Exit and stop tasks\n     3. Stay\n\n   Enter to confirm · Esc to cancel\n", nil
		}
		return "", nil
	}
	_, err := x.m.move("team", "dev")
	if last := x.typed[len(x.typed)-1]; err == nil || !strings.HasSuffix(last, " escape") || len(x.resumed) != 0 {
		t.Fatalf("a build in the waiter's place: %v, last key %q, resumed %q", err, last, x.resumed)
	}
}

// crius:lead 11-08: backend-core's turn ended on a weekly-limit 429, which records no Stop, so its state stayed
// "running" and -move refused a tab idle at its prompt. A turn whose transcript ends on Claude Code's API-error message
// has ended; one whose transcript ends on a tool call still runs.
func TestATurnThatEndedOnAnAPIErrorIsABoundary(t *testing.T) {
	transcript := func(x *moveRig, last string) {
		testutil.Write(t, filepath.Join(x.m1, "projects", "-w-team-wt", x.id+".jsonl"),
			`{"type":"user","message":{"role":"user","content":"go on"}}`+"\n"+
				`{"type":"api-request"}`+"\n"+last+"\n"+`{"type":"system","subtype":"x"}`+"\n")
	}
	x := newMoveRig(t)
	x.state = sessionstate.Running
	transcript(x, `{"type":"assistant","isApiErrorMessage":true,"error":"rate_limit","message":{"role":"assistant","content":[{"type":"text","text":"You've hit your weekly limit"}]}}`)
	if _, err := x.m.move("team", "dev"); err != nil {
		t.Fatalf("a turn ended on a 429: %v", err)
	}
	if len(x.resumed) != 1 {
		t.Errorf("resumed %q", x.resumed)
	}
	// its exit dialog (its bus waiter) is answered at once, not waited on as a running turn for turnCap
	x = newMoveRig(t)
	x.state, x.exits, x.m.wait = sessionstate.Running, false, time.Millisecond
	x.m.tries = 3
	transcript(x, `{"type":"assistant","isApiErrorMessage":true,"error":"rate_limit"}`)
	x.children = []proc{{pid: 70, ppid: os.Getpid(), command: "/bin/zsh -c eval '~/.claude/skills/cmux-team/bin/team-wait.sh --mode worker'"}}
	shown := false
	x.m.cmux = func(args ...string) (string, error) {
		x.typed = append(x.typed, strings.Join(args, " "))
		switch {
		case args[0] == "read-screen" && strings.Contains(strings.Join(x.typed, "\n"), "/exit"):
			shown = true
			return "   Background work is running\n   The following will stop when you exit:\n\n   shell · team-wait\n\n" +
				"   ❯ 1. Exit and stop tasks\n     2. Move to background and exit\n     3. Stay\n\n   Enter to confirm · Esc to cancel\n", nil
		case args[0] == "send-key" && shown && args[len(args)-1] == "enter":
			if err := os.Remove(filepath.Join(x.m1, "sessions", "1.json")); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
		}
		return "", nil
	}
	done := make(chan error, 1)
	go func() { _, err := x.m.move("team", "dev"); done <- err }()
	select {
	case err := <-done:
		if err != nil || len(x.resumed) != 1 {
			t.Errorf("exit dialog after a 429: %v, resumed %q", err, x.resumed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the exit waited on the stale running state")
	}
	x = newMoveRig(t)
	x.state = sessionstate.Running
	transcript(x, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash"}]}}`)
	if _, err := x.m.move("team", "dev"); err == nil || !strings.Contains(err.Error(), "a turn is running") {
		t.Errorf("a turn in a tool call: %v", err)
	}
}
