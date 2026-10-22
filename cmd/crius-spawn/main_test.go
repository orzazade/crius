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
