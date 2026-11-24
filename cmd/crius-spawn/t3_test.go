package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/launch"
	"github.com/orzazade/crius/internal/sessionstate"
)

// A role that runs as a T3 Code thread comes back in its own thread: -resume sends it the resume prompt there, never
// opens a tab, and opens a role closed on purpose, as for a tab.
func TestResumeOfAT3RoleSendsItsThreadTheResumePrompt(t *testing.T) {
	home := t.TempDir()
	s := sessionstate.State{SessionID: "6f1c2a9e-3b4d-4e5f-8a7b-9c0d1e2f3a4b", Cwd: "/Users/o/Projects/lab", Project: "lab", Role: "probe",
		Config: t.TempDir(), Thread: "mcp:th-1"}
	roles := filepath.Join(home, "roles")
	if err := sessionstate.WriteRole(roles, s, true); err != nil {
		t.Fatal(err)
	}
	if err := sessionstate.CloseRole(roles, "lab", "probe", s.SessionID); err != nil {
		t.Fatal(err)
	}
	d := launch.Deps{Cmux: func(args ...string) (string, error) {
		t.Errorf("cmux %q called for a T3 role", args)
		return "", errors.New("no cmux")
	}, Lock: filepath.Join(home, "launch.lock")}
	var calls []string
	runs := 3
	cli := func(stdin string, args ...string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		switch args[1] {
		case "send":
			if stdin != launch.ResumedPrompt {
				t.Errorf("sent %q", stdin)
			}
			runs++
			return `{"threadId":"mcp:th-1"}`, nil
		case "show":
			return `{"thread":{"status":"waiting","runCount":` + string(rune('0'+runs)) + `,"pendingRequestCount":0,"archived":false},"messages":[]}`, nil
		}
		return "", errors.New("unexpected")
	}
	if err := resume(home, "lab", "probe", d, launch.T3{CLI: cli, Tries: 2}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(calls, "\n"), "thread send --json mcp:th-1 -") {
		t.Errorf("t3 calls %q", calls)
	}
	if c, err := sessionstate.ClosedRole(roles, "lab", "probe"); err != nil || c != "" {
		t.Errorf("still closed: %q %v", c, err)
	}
}

// Owner 10-30: a move keeps the same session. T3 cannot: a change of a thread's provider instance starts a new Claude
// session that gets only a summary of the thread (11-18: AXI api-split). So a move of a T3 role is refused, plainly,
// before any account is picked or anything is typed.
func TestAMoveOfAT3RoleIsRefused(t *testing.T) {
	x := newMoveRig(t)
	picked := false
	x.m.pick = func(project, want, config string) (string, string, error) { picked = true; return "max2", x.m2, nil }
	s := sessionstate.State{SessionID: x.id, Cwd: "/w/team/wt", Project: "team", Role: "dev", Config: x.m1, Thread: "mcp:th-1"}
	if err := sessionstate.WriteRole(filepath.Join(x.home, "roles"), s, true); err != nil {
		t.Fatal(err)
	}
	_, err := x.m.move("team", "dev")
	if err == nil || !strings.Contains(err.Error(), "team dev runs in T3 thread mcp:th-1: T3 cannot move a thread to another account as the same session") {
		t.Errorf("err %v", err)
	}
	if picked || x.typed != nil || x.resumed != nil {
		t.Errorf("acted anyway: picked %v, typed %q, resumed %q", picked, x.typed, x.resumed)
	}
}

// A resume T3 refuses (here an archived thread) leaves a role closed on purpose closed (demo 11-19: it was opened).
func TestARefusedT3ResumeLeavesTheRoleClosed(t *testing.T) {
	home := t.TempDir()
	roles := filepath.Join(home, "roles")
	s := sessionstate.State{SessionID: "6f1c2a9e-3b4d-4e5f-8a7b-9c0d1e2f3a4b", Project: "lab", Role: "probe", Thread: "mcp:th-1"}
	if err := sessionstate.WriteRole(roles, s, true); err != nil {
		t.Fatal(err)
	}
	if err := sessionstate.CloseRole(roles, "lab", "probe", s.SessionID); err != nil {
		t.Fatal(err)
	}
	cli := func(stdin string, args ...string) (string, error) {
		return `{"thread":{"status":"completed","runCount":2,"archived":true},"messages":[]}`, nil
	}
	err := resume(home, "lab", "probe", launch.Deps{}, launch.T3{CLI: cli, Tries: 1})
	if err == nil || !strings.Contains(err.Error(), "is archived in T3: nothing resumed") {
		t.Errorf("err %v", err)
	}
	if c, err := sessionstate.ClosedRole(roles, "lab", "probe"); err != nil || c != s.SessionID {
		t.Errorf("closed %q %v, want it still closed", c, err)
	}
}

// A role whose sessions moved to a new thread may still name the old one in its file (11-17: cortex:lead); -resume
// goes to the thread of the role's newest session, as wakewatch watches it, never the old one.
func TestAT3ResumeGoesToTheNewestSessionsThread(t *testing.T) {
	home := t.TempDir()
	old := sessionstate.State{SessionID: "6f1c2a9e-3b4d-4e5f-8a7b-9c0d1e2f3a4b", Project: "lab", Role: "probe", Thread: "mcp:old",
		Time: time.Now().Add(-time.Hour)}
	if err := sessionstate.WriteRole(filepath.Join(home, "roles"), old, true); err != nil {
		t.Fatal(err)
	}
	if err := sessionstate.Write(filepath.Join(home, "sessions"), old); err != nil {
		t.Fatal(err)
	}
	cur := sessionstate.State{SessionID: "7f1c2a9e-3b4d-4e5f-8a7b-9c0d1e2f3a4b", Project: "lab", Role: "probe", Thread: "mcp:new", Time: time.Now()}
	if err := sessionstate.Write(filepath.Join(home, "sessions"), cur); err != nil {
		t.Fatal(err)
	}
	var sent []string
	runs := 1
	cli := func(stdin string, args ...string) (string, error) {
		if args[1] == "send" {
			sent = append(sent, args[3])
			runs++
		}
		return fmt.Sprintf(`{"thread":{"status":"completed","runCount":%d},"messages":[]}`, runs), nil
	}
	if err := resume(home, "lab", "probe", launch.Deps{}, launch.T3{CLI: cli, Tries: 2}); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 || sent[0] != "mcp:new" {
		t.Errorf("sent to %q, want mcp:new", sent)
	}
}
