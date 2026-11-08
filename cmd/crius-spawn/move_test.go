package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
