package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orzazade/crius/internal/launch"
	"github.com/orzazade/crius/internal/sessionstate"
	"github.com/orzazade/crius/internal/testutil"
)

// Live run 10-22: cmux prints a deprecation notice on stderr, and crius-spawn parsed it with the reply, so it failed
// after making the workspace. A notice must not reach the parse; on failure it must show.
func TestCmuxStderrStaysOutOfTheReplyUnlessItFails(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\necho \"cmux: 'new-workspace' is now an alias\" >&2\n[ \"$1\" = bad ] && { echo 'Error: no such thing' >&2; exit 1; }\necho 'OK workspace:9'\n"
	if err := os.WriteFile(filepath.Join(dir, "cmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if out, err := runCmux("new-workspace"); err != nil || out != "OK workspace:9\n" {
		t.Fatalf("%q %v", out, err)
	}
	if out, err := runCmux("bad"); err == nil || !strings.Contains(out, "Error: no such thing") {
		t.Fatalf("%q %v", out, err)
	}
}

// Brief 10-22 item 5: -resume takes the session id and folder from the role's file that crius-hook keeps; a role with
// no file says so, naming the file.
func TestResumeReadsTheRolesCurrentSession(t *testing.T) {
	home := t.TempDir()
	var got []string
	d := launch.Deps{Cmux: func(args ...string) (string, error) {
		got = append(got, strings.Join(args, " "))
		return "", errors.New("stop here")
	}, Lock: filepath.Join(home, "launch.lock")}
	err := resume(home, "quotes", "lead", d)
	if err == nil || !strings.Contains(err.Error(), filepath.Join(home, "roles", "quotes.lead.json")) {
		t.Errorf("no role file: err %v, want one naming the file", err)
	}
	s := sessionstate.State{SessionID: "6f1c2a9e-3b4d-4e5f-8a7b-9c0d1e2f3a4b", Cwd: "/Users/o/Projects/quotes", Project: "quotes", Role: "lead", Config: t.TempDir()}
	if err := sessionstate.WriteRole(filepath.Join(home, "roles"), s, true); err != nil {
		t.Fatal(err)
	}
	if err := resume(home, "quotes", "lead", d); err == nil || len(got) != 1 || got[0] != "tree --all" {
		t.Errorf("err %v, calls %q: want Resume called with the recorded session", err, got)
	}
}

// Review 10-22 (item 5): a session still running in a tab not titled "<project> · <role>" (quotes: rig, live) was
// resumed a second time, two Claudes on one transcript. A live process holding the session refuses the resume.
func TestAResumeOfARunningSessionIsRefused(t *testing.T) {
	home, config := t.TempDir(), t.TempDir()
	id := "6f1c2a9e-3b4d-4e5f-8a7b-9c0d1e2f3a4b"
	status := fmt.Sprintf(`{"pid":%d,"sessionId":%q,"name":"quotes · rig","procStart":%q}`, os.Getpid(), id, testutil.ProcStart(t, os.Getpid()))
	if err := os.MkdirAll(filepath.Join(config, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "sessions", "1.json"), []byte(status), 0o600); err != nil {
		t.Fatal(err)
	}
	s := sessionstate.State{SessionID: id, Cwd: "/Users/o/Projects/quotes", Project: "quotes", Role: "rig", Config: config}
	if err := sessionstate.WriteRole(filepath.Join(home, "roles"), s, true); err != nil {
		t.Fatal(err)
	}
	var got []string
	d := launch.Deps{Cmux: func(args ...string) (string, error) {
		got = append(got, strings.Join(args, " "))
		return "", errors.New("stop here")
	}}
	err := resume(home, "quotes", "rig", d)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("still runs in process %d", os.Getpid())) || len(got) != 0 {
		t.Errorf("err %v, cmux calls %q: want a refusal naming the process and no cmux call", err, got)
	}
}

// Review of item 8: a session cmux restored in another folder than its role's start filed still runs there; the "still
// runs" check looks where it runs (config), not where it was filed, so it is not resumed a second time.
func TestAResumeOfASessionRestoredElsewhereIsRefused(t *testing.T) {
	home, filed, running := t.TempDir(), t.TempDir(), t.TempDir()
	id := "6f1c2a9e-3b4d-4e5f-8a7b-9c0d1e2f3a4b"
	testutil.Write(t, filepath.Join(running, "sessions", "1.json"),
		fmt.Sprintf(`{"pid":%d,"sessionId":%q,"name":"quotes · rig","procStart":%q}`, os.Getpid(), id, testutil.ProcStart(t, os.Getpid())))
	s := sessionstate.State{SessionID: id, Cwd: "/Users/o/Projects/quotes", Project: "quotes", Role: "rig", Config: running, Filed: filed}
	if err := sessionstate.WriteRole(filepath.Join(home, "roles"), s, true); err != nil {
		t.Fatal(err)
	}
	var got []string
	d := launch.Deps{Cmux: func(args ...string) (string, error) {
		got = append(got, strings.Join(args, " "))
		return "", errors.New("stop here")
	}}
	err := resume(home, "quotes", "rig", d)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("still runs in process %d", os.Getpid())) || len(got) != 0 {
		t.Errorf("err %v, cmux calls %q: want a refusal naming the process and no cmux call", err, got)
	}
}

// Owner 10-23: with no -account, or -account max, crius-spawn starts the session on the Max account with the most room,
// in that account's folder linked to the project's persona. The figures come from the record store, the folders from
// accounts.toml and the persona and pause_at from projects.toml.
func TestPickReadsTheAccountsTheStoreAndTheProject(t *testing.T) {
	home, d := t.TempDir(), t.TempDir()
	transcripts := filepath.Join(d, ".claude-work", "projects")
	m2 := filepath.Join(d, ".claude-max2")
	if err := os.MkdirAll(transcripts, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(m2, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "state"), 0o700); err != nil { // crius-rec init
		t.Fatal(err)
	}
	if err := os.Symlink(transcripts, filepath.Join(m2, "projects")); err != nil {
		t.Fatal(err)
	}
	testutil.Write(t, filepath.Join(m2, ".claude.json"), `{"oauthAccount":{"accountUuid":"acct-2"}}`)
	testutil.Write(t, filepath.Join(home, "projects.toml"), "[transcripts]\nwork = \""+transcripts+
		"\"\n[projects.quotes]\npersona = \"work\"\naccounts = [\"max\"]\npause_at = 90\n")
	testutil.Write(t, filepath.Join(home, "accounts.toml"), "[[account]]\nname = \"max2\"\nplan = \"max\"\ndirs = [\""+m2+"\"]\n")
	if name, dir, why, err := pick(home, "quotes", "max"); err != nil || name != "max2" || dir != m2 || !strings.Contains(why, "max2: quota not measured") {
		t.Errorf("max: %q %q %q %v, want max2 in %s, not measured", name, dir, why, err, m2)
	}
	if _, _, _, err := pick(home, "quotes", "max9"); err == nil || !strings.Contains(err.Error(), "no Max account max9") {
		t.Errorf("max9: %v, want no such account", err)
	}
	if _, _, _, err := pick(home, "gone", "max"); err == nil || !strings.Contains(err.Error(), "no project gone") {
		t.Errorf("gone: %v, want no such project", err)
	}
}

// Owner 10-23: a session that ran in a Max row's folder of accounts.toml resumes there, titled with the row; one that
// ran elsewhere (or on an enterprise row) resumes through aitch's route as before.
func TestResumeKeepsTheSessionsMaxAccount(t *testing.T) {
	home, d := t.TempDir(), t.TempDir()
	m2, ent, other := filepath.Join(d, ".claude-max2"), filepath.Join(d, ".claude-work"), filepath.Join(d, ".claude")
	testutil.Write(t, filepath.Join(m2, ".claude.json"), `{"oauthAccount":{"accountUuid":"acct-2"}}`)
	testutil.Write(t, filepath.Join(ent, ".claude.json"), `{"oauthAccount":{"accountUuid":"acct-ent"}}`)
	testutil.Write(t, filepath.Join(home, "accounts.toml"), "[[account]]\nname = \"max2\"\nplan = \"max\"\ndirs = [\""+m2+
		"\"]\n[[account]]\nname = \"ent\"\nplan = \"enterprise\"\ndirs = [\""+ent+"\"]\n")
	for config, want := range map[string][2]string{m2: {"max2", m2}, ent: {}, other: {}} {
		if acct, dir, err := sessionAccount(home, config); err != nil || acct != want[0] || dir != want[1] {
			t.Errorf("%s: %q %q %v, want %q", config, acct, dir, err, want)
		}
	}
	testutil.Write(t, filepath.Join(home, "accounts.toml"), "[[account]]\nname = \"max2\"\n")
	if _, _, err := sessionAccount(home, m2); err == nil {
		t.Error("a broken accounts.toml gave no error")
	}
}

// Owner 10-23: -move relaunches the role from the session crius-hook keeps as its current one: in its folder, off the
// Max account it ran on. A session still running is refused (two Claudes on one worktree), and so is a role with none.
func TestAMoveReadsTheRolesSessionAndItsAccount(t *testing.T) {
	home, d := t.TempDir(), t.TempDir()
	m1 := filepath.Join(d, ".claude-work-max")
	testutil.Write(t, filepath.Join(m1, ".claude.json"), `{"oauthAccount":{"accountUuid":"acct-1"}}`)
	testutil.Write(t, filepath.Join(home, "accounts.toml"), "[[account]]\nname = \"max1\"\nplan = \"max\"\ndirs = [\""+m1+"\"]\n")
	s := sessionstate.State{SessionID: "6f1c2a9e-3b4d-4e5f-8a7b-9c0d1e2f3a4b", Cwd: "/Users/o/Projects/quotes/.worktrees/lead", Project: "quotes", Role: "lead", Config: m1}
	if err := sessionstate.WriteRole(filepath.Join(home, "roles"), s, true); err != nil {
		t.Fatal(err)
	}
	if m, err := moved(home, "quotes", "lead"); err != nil || m == nil || *m != (launch.Moved{Cwd: s.Cwd, From: "max1"}) {
		t.Errorf("%+v %v", m, err)
	}
	// Review 10-23: a folder no row lists, signed in to max1's login, is on max1 too, so a move to max1 is refused.
	m2 := filepath.Join(d, ".claude-other")
	testutil.Write(t, filepath.Join(m2, ".claude.json"), `{"oauthAccount":{"accountUuid":"acct-1"}}`)
	s2 := sessionstate.State{SessionID: "7a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d", Cwd: "/Users/o/Projects/quotes/.worktrees/dev", Project: "quotes", Role: "dev", Config: m2}
	if err := sessionstate.WriteRole(filepath.Join(home, "roles"), s2, true); err != nil {
		t.Fatal(err)
	}
	if m, err := moved(home, "quotes", "dev"); err != nil || m == nil || m.From != "max1" {
		t.Errorf("unlisted folder on max1's login: %+v %v", m, err)
	}
	if _, err := moved(home, "quotes", "rig"); err == nil || !strings.Contains(err.Error(), "no session to move for quotes rig") {
		t.Errorf("no role: %v", err)
	}
	status := fmt.Sprintf(`{"pid":%d,"sessionId":%q,"name":"quotes · lead","procStart":%q}`, os.Getpid(), s.SessionID, testutil.ProcStart(t, os.Getpid()))
	testutil.Write(t, filepath.Join(m1, "sessions", "1.json"), status)
	if _, err := moved(home, "quotes", "lead"); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("still runs in process %d", os.Getpid())) {
		t.Errorf("running: %v", err)
	}
}
