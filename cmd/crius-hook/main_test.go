package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/sessionstate"
	"github.com/orzazade/crius/internal/testutil"
)

var bin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "crius-hook-test")
	if err != nil {
		panic(err)
	}
	bin = filepath.Join(dir, "crius-hook")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		panic(string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// world is a home with projects.toml and a Claude config folder whose own status file names session s-1.
type world struct{ home, crius, claude string }

func newWorld(t *testing.T) world {
	h := t.TempDir()
	w := world{h, filepath.Join(h, ".crius"), filepath.Join(h, "claude")}
	testutil.Write(t, filepath.Join(w.crius, "projects.toml"), "[projects.quotes]\ncwd = [\"/w/quotes\"]\n")
	pid := os.Getpid() // a live process: status files of dead ones are skipped
	testutil.Write(t, filepath.Join(w.claude, "sessions", fmt.Sprintf("%d.json", pid)),
		fmt.Sprintf(`{"pid":%d,"sessionId":"s-1","cwd":"/w/crius","name":"quotes · p1","status":"busy"}`, pid))
	return w
}

func (w world) env() []string {
	return []string{"HOME=" + w.home, "CRIUS_HOME=" + w.crius, "CLAUDE_CONFIG_DIR=" + w.claude,
		"CMUX_WORKSPACE_ID=ws-9", "CMUX_SURFACE_ID=sf-3"}
}

// run feeds one hook input to the binary. The hook must exit 0 and print nothing on stdout: Claude Code puts a
// SessionStart or UserPromptSubmit hook's stdout into the model's context, and exit 2 blocks the prompt or tool.
func run(t *testing.T, env []string, stdin string) (stderr string) {
	t.Helper()
	cmd := exec.Command(bin)
	cmd.Env = env
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) || err != nil {
		t.Fatalf("exit: %v; stderr %q", err, errb.String())
	}
	if out.Len() != 0 {
		t.Fatalf("stdout %q", out.String())
	}
	return errb.String()
}

func TestAPromptWritesTheSessionsFile(t *testing.T) {
	w := newWorld(t)
	run(t, w.env(), `{"session_id":"s-1","hook_event_name":"UserPromptSubmit","cwd":"/w/crius","prompt":"wake: go"}`)
	s, err := sessionstate.Read(filepath.Join(w.crius, "sessions"), "s-1")
	if err != nil {
		t.Fatal(err)
	}
	if s.State != sessionstate.Running || s.Name != "quotes · p1" || s.Project != "quotes" || s.Role != "p1" ||
		s.Cwd != "/w/crius" || s.Workspace != "ws-9" || s.Surface != "sf-3" || s.PromptSHA != sessionstate.PromptSHA("wake: go") {
		t.Errorf("%+v", s)
	}
	run(t, w.env(), `{"session_id":"s-1","hook_event_name":"Stop","cwd":"/w/crius"}`)
	s, _ = sessionstate.Read(filepath.Join(w.crius, "sessions"), "s-1")
	if s.State != sessionstate.Idle || s.PromptSHA != sessionstate.PromptSHA("wake: go") {
		t.Errorf("after Stop: %+v", s)
	}
}

// Without a -n name the folder gives the project, as ingest files it.
func TestASessionWithNoNameIsFiledByFolder(t *testing.T) {
	w := newWorld(t)
	run(t, w.env(), `{"session_id":"s-2","hook_event_name":"SessionStart","source":"startup","cwd":"/w/quotes/web"}`)
	s, err := sessionstate.Read(filepath.Join(w.crius, "sessions"), "s-2")
	if err != nil || s.State != sessionstate.Idle || s.Name != "" || s.Project != "quotes" || s.Role != "unknown" {
		t.Errorf("%+v %v", s, err)
	}
}

func logText(t *testing.T, w world) string {
	b, err := os.ReadFile(filepath.Join(w.crius, "logs", "crius-hook.log"))
	if err != nil {
		t.Fatalf("no log: %v", err)
	}
	return string(b)
}

func TestUnreadableInputIsLoggedAndTheSessionGoesOn(t *testing.T) {
	w := newWorld(t)
	run(t, w.env(), "not json")
	if l := logText(t, w); !strings.Contains(l, "unreadable hook input") {
		t.Errorf("log %q", l)
	}
}

func TestABadSessionIDIsLogged(t *testing.T) {
	w := newWorld(t)
	run(t, w.env(), `{"session_id":"../x","hook_event_name":"Stop"}`)
	if l := logText(t, w); !strings.Contains(l, `session id "../x"`) {
		t.Errorf("log %q", l)
	}
}

// A missing projects.toml loses the project and role, not the state.
func TestAMissingProjectsTomlIsLoggedButTheStateIsWritten(t *testing.T) {
	w := newWorld(t)
	os.Remove(filepath.Join(w.crius, "projects.toml"))
	run(t, w.env(), `{"session_id":"s-1","hook_event_name":"Stop","cwd":"/w/crius"}`)
	s, err := sessionstate.Read(filepath.Join(w.crius, "sessions"), "s-1")
	if err != nil || s.State != sessionstate.Idle || s.Name != "quotes · p1" || s.Project != "" {
		t.Errorf("%+v %v", s, err)
	}
	if l := logText(t, w); !strings.Contains(l, "projects.toml") {
		t.Errorf("log %q", l)
	}
}

// When even the log cannot be written, stderr says so and the hook still exits 0.
func TestAHomeThatIsAFileGoesToStderr(t *testing.T) {
	w := newWorld(t)
	f := filepath.Join(w.home, "file")
	testutil.Write(t, f, "x")
	env := append(w.env(), "CRIUS_HOME="+f)
	if e := run(t, env, `{"session_id":"s-1","hook_event_name":"Stop"}`); !strings.Contains(e, "crius-hook:") {
		t.Errorf("stderr %q", e)
	}
}

// No HOME and no CRIUS_HOME: store.Home panics; the hook recovers and exits 0.
func TestNoHomeAtAllStillExitsZero(t *testing.T) {
	if e := run(t, []string{"PATH=/usr/bin"}, `{"session_id":"s-1","hook_event_name":"Stop"}`); !strings.Contains(e, "crius-hook:") {
		t.Errorf("stderr %q", e)
	}
}

// Events of one session run as parallel processes (subagents, parallel tools). Each reads, changes and writes the
// file; without a lock the last writer drops what the others wrote (review P2, 10-22).
func TestParallelEventsLoseNothing(t *testing.T) {
	w := newWorld(t)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Go(func() {
			cmd := exec.Command(bin)
			cmd.Env = w.env()
			cmd.Stdin = strings.NewReader(fmt.Sprintf(`{"session_id":"s-1","hook_event_name":"PermissionRequest","agent_id":"a%02d"}`, i))
			if out, err := cmd.CombinedOutput(); err != nil || len(out) != 0 {
				t.Errorf("%d: %v %q", i, err, out)
			}
		})
	}
	wg.Wait()
	s, err := sessionstate.Read(filepath.Join(w.crius, "sessions"), "s-1")
	if err != nil || len(s.Waits) != 20 {
		t.Errorf("%d of 20 dialogs kept: %v %v", len(s.Waits), s.Waits, err)
	}
}

// A file that cannot be read as a state (cut short by a power loss, an older format) is logged and replaced, not
// left to freeze the session at its last good state.
func TestADamagedFileIsLoggedAndReplaced(t *testing.T) {
	w := newWorld(t)
	testutil.Write(t, filepath.Join(w.crius, "sessions", "s-1.json"), `{"state":"runn`)
	run(t, w.env(), `{"session_id":"s-1","hook_event_name":"Stop","cwd":"/w/crius"}`)
	s, err := sessionstate.Read(filepath.Join(w.crius, "sessions"), "s-1")
	if err != nil || s.State != sessionstate.Idle {
		t.Errorf("%+v %v", s, err)
	}
	if l := logText(t, w); !strings.Contains(l, "s-1.json") {
		t.Errorf("log %q", l)
	}
}

// The cmux surface is the session's own process env, read on each event: a session resumed outside cmux must not
// keep naming the surface it had, which may now hold another session.
func TestASurfaceIsNotKeptWhenTheEnvHasNone(t *testing.T) {
	w := newWorld(t)
	run(t, w.env(), `{"session_id":"s-1","hook_event_name":"SessionStart","source":"startup"}`)
	run(t, w.env()[:3], `{"session_id":"s-1","hook_event_name":"SessionStart","source":"resume"}`)
	s, _ := sessionstate.Read(filepath.Join(w.crius, "sessions"), "s-1")
	if s.Workspace != "" || s.Surface != "" {
		t.Errorf("%+v", s)
	}
}

// Claude Code leaves the status file of a crashed process behind: its old name must not win over the live one.
func TestADeadProcessesStatusFileGivesNoName(t *testing.T) {
	w := newWorld(t)
	testutil.Write(t, filepath.Join(w.claude, "sessions", "0001.json"),
		`{"pid":99999999,"sessionId":"s-1","name":"old · name"}`)
	run(t, w.env(), `{"session_id":"s-1","hook_event_name":"Stop"}`)
	s, _ := sessionstate.Read(filepath.Join(w.crius, "sessions"), "s-1")
	if s.Name != "quotes · p1" {
		t.Errorf("name %q", s.Name)
	}
}

// A session started with -n and resumed without it has a live status file with no name: the old name must go, or the
// session stays filed under the old project and role (review P3, 10-22).
func TestALiveStatusFileWithNoNameClearsTheOldName(t *testing.T) {
	w := newWorld(t)
	testutil.Write(t, filepath.Join(w.claude, "sessions", "2.json"), fmt.Sprintf(`{"pid":%d,"sessionId":"s-2"}`, os.Getpid()))
	dir := filepath.Join(w.crius, "sessions")
	if err := sessionstate.Write(dir, sessionstate.State{SessionID: "s-2", Name: "quotes · p9"}); err != nil {
		t.Fatal(err)
	}
	run(t, w.env(), `{"session_id":"s-2","hook_event_name":"SessionStart","source":"resume","cwd":"/w/quotes"}`)
	if s, _ := sessionstate.Read(dir, "s-2"); s.Name != "" || s.Role != "unknown" {
		t.Errorf("%+v", s)
	}
}

// One session's held lock must not stop another session's hook, and a lock held too long is logged, not waited on
// until Claude Code's hook timeout (review P3, 10-22).
func TestALockHeldByOneSessionDoesNotStopAnother(t *testing.T) {
	w := newWorld(t)
	run(t, w.env(), `{"session_id":"s-1","hook_event_name":"Stop"}`)
	locks, _ := filepath.Glob(filepath.Join(w.crius, "sessions", "*lock*"))
	if len(locks) != 1 {
		t.Fatalf("lock files %v", locks)
	}
	f, err := os.OpenFile(locks[0], os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"s-2", "s-1"} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		cmd := exec.CommandContext(ctx, bin)
		cmd.Env, cmd.Stdin = w.env(), strings.NewReader(`{"session_id":"`+id+`","hook_event_name":"UserPromptSubmit","prompt":"go"}`)
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil || len(out) != 0 {
			t.Fatalf("%s: %v %q", id, err, out)
		}
	}
	if s, err := sessionstate.Read(filepath.Join(w.crius, "sessions"), "s-2"); err != nil || s.State != sessionstate.Running {
		t.Errorf("s-2: %+v %v", s, err)
	}
	if l := logText(t, w); !strings.Contains(l, "UserPromptSubmit s-1") || !strings.Contains(l, ".lock locked for 1s, event skipped\n") {
		t.Errorf("log %q", l)
	}
}

// A bad projects.toml would log on every event of every session, and wakewatch alerts on each growth of the log: it is
// logged once until the file changes (review P3, 10-22).
func TestABadProjectsTomlIsLoggedOnce(t *testing.T) {
	w := newWorld(t)
	os.Remove(filepath.Join(w.crius, "projects.toml"))
	for range 3 {
		run(t, w.env(), `{"session_id":"s-1","hook_event_name":"PreToolUse","tool_name":"Bash"}`)
	}
	if n := strings.Count(logText(t, w), "projects.toml"); n != 1 {
		t.Errorf("%d lines: %q", n, logText(t, w))
	}
}

// Claude Code runs hooks for a remote call it serves with session_id "served:<caller's id>": not a session of ours, and
// not an error to log on every such call.
func TestARemoteCallIsNotASession(t *testing.T) {
	w := newWorld(t)
	run(t, w.env(), `{"session_id":"served:s-1","hook_event_name":"PreToolUse","tool_name":"Bash"}`)
	if files, _ := filepath.Glob(filepath.Join(w.crius, "sessions", "*.json")); len(files) != 0 {
		t.Errorf("files %v", files)
	}
	if _, err := os.Stat(filepath.Join(w.crius, "logs", "crius-hook.log")); !os.IsNotExist(err) {
		t.Errorf("log: %v", err)
	}
}

// A role's file names its current session, for crius-spawn -resume: the newest SessionStart, or else the first session
// seen with that role. The hook went live at 15:33 on 10-22, after every quotes role had started: none of them had a
// SessionStart to go by.
func TestARoleNamesItsCurrentSession(t *testing.T) {
	w := newWorld(t)
	testutil.Write(t, filepath.Join(w.claude, "sessions", "2.json"),
		fmt.Sprintf(`{"pid":%d,"sessionId":"s-2","name":"quotes · p1"}`, os.Getpid()))
	roles := filepath.Join(w.crius, "roles")
	for _, c := range []struct{ in, want, detail string }{
		{`{"session_id":"s-1","hook_event_name":"Stop","cwd":"/w/crius"}`, "s-1", ""},
		{`{"session_id":"s-2","hook_event_name":"SessionStart","source":"startup","cwd":"/w/crius"}`, "s-2", "startup"},
		{`{"session_id":"s-1","hook_event_name":"Stop","cwd":"/w/crius"}`, "s-2", "startup"},
		{`{"session_id":"s-1","hook_event_name":"SessionStart","source":"resume","cwd":"/w/crius"}`, "s-1", "resume"},
		{`{"session_id":"s-1","hook_event_name":"SessionEnd","reason":"other","cwd":"/w/crius"}`, "s-1", "resume"},
	} {
		run(t, w.env(), c.in)
		s, err := sessionstate.ReadRole(roles, "quotes", "p1")
		if err != nil || s.SessionID != c.want || s.Detail != c.detail || s.Cwd != "/w/crius" || s.Name != "quotes · p1" {
			t.Fatalf("after %s: role file %+v, %v; want %s", c.in, s, err, c.want)
		}
	}
	// No role, no file: a session filed as "unknown" is no role to resume.
	run(t, w.env(), `{"session_id":"s-3","hook_event_name":"SessionStart","source":"startup","cwd":"/w/quotes"}`)
	if names, _ := filepath.Glob(filepath.Join(roles, "*")); len(names) != 1 {
		t.Errorf("role files %q, want quotes.p1.json alone", names)
	}
}

// Review 10-22 (item 5): a SessionStart the hook did not file under the role (after /clear the new id may not be in
// Claude Code's status file yet) left the old session current for good. A role whose session no longer runs goes to
// the next session of that role seen; the config folder is kept for that check and for crius-spawn -resume.
func TestARoleWhoseSessionEndedGoesToTheNextOne(t *testing.T) {
	w := newWorld(t)
	roles := filepath.Join(w.crius, "roles")
	run(t, w.env(), `{"session_id":"s-1","hook_event_name":"Stop","cwd":"/w/crius"}`)
	// /clear: the same process now holds s-2
	testutil.Write(t, filepath.Join(w.claude, "sessions", fmt.Sprintf("%d.json", os.Getpid())),
		fmt.Sprintf(`{"pid":%d,"sessionId":"s-2","cwd":"/w/crius","name":"quotes · p1"}`, os.Getpid()))
	run(t, w.env(), `{"session_id":"s-2","hook_event_name":"UserPromptSubmit","prompt":"go","cwd":"/w/crius"}`)
	s, err := sessionstate.ReadRole(roles, "quotes", "p1")
	if err != nil || s.SessionID != "s-2" || s.Config != w.claude {
		t.Fatalf("role file %+v, %v; want s-2 with config %s", s, err, w.claude)
	}
}
