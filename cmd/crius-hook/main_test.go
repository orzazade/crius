package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
		fmt.Sprintf(`{"pid":%d,"sessionId":"s-1","cwd":"/w/crius","name":"quotes · p1","status":"busy","procStart":%q}`, pid, testutil.ProcStart(t, pid)))
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
	fire(t, w, 0, 50)
	s, err := sessionstate.Read(filepath.Join(w.crius, "sessions"), "s-1")
	if err != nil || len(s.Waits) != 50 {
		t.Errorf("%d of 50 dialogs kept: %v %v", len(s.Waits), s.Waits, err)
	}
}

// fire sends the PermissionRequests of agents a<from>..a<to-1> in parallel.
func fire(t *testing.T, w world, from, to int) {
	t.Helper()
	var wg sync.WaitGroup
	for i := from; i < to; i++ {
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
}

// crius:lead 11-08: under full-suite load 7 of 20 parallel events were lost: an event that waited 1 s for the
// session's lock was skipped. Here the lock is held longer than any hook waits; every event still lands, applied by the
// session's next hook once the lock is free.
func TestEventsWhileTheLockIsHeldLandWithTheNextEvent(t *testing.T) {
	w := newWorld(t)
	dir := filepath.Join(w.crius, "sessions")
	unlock, err := sessionstate.Lock(dir, "s-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	free := make(chan struct{})
	time.AfterFunc(1500*time.Millisecond, func() { unlock(); close(free) })
	fire(t, w, 0, 20)
	<-free
	fire(t, w, 20, 21)
	s, err := sessionstate.Read(dir, "s-1")
	if err != nil || len(s.Waits) != 21 {
		t.Errorf("%d of 21 dialogs kept: %v %v", len(s.Waits), s.Waits, err)
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

// A session T3 Code runs has T3CODE_THREAD_ID in its env: the hook files it in the session's and the role's file, so
// bus-post can hand a line for the role to its thread (11-15: 22 hand relays in 10 days). The env is read on every
// event, like the cmux surface: a session resumed outside T3 Code names no thread.
func TestAT3ThreadIsFiledFromTheEnv(t *testing.T) {
	w := newWorld(t)
	env := append(w.env()[:3], "T3CODE_THREAD_ID=mcp:launch:abc")
	run(t, env, `{"session_id":"s-1","hook_event_name":"SessionStart","source":"startup","cwd":"/w/crius"}`)
	s, _ := sessionstate.Read(filepath.Join(w.crius, "sessions"), "s-1")
	r, err := sessionstate.ReadRole(filepath.Join(w.crius, "roles"), "quotes", "p1")
	if s.Thread != "mcp:launch:abc" || err != nil || r.Thread != "mcp:launch:abc" || r.Surface != "" {
		t.Fatalf("session %+v; role %+v, %v", s, r, err)
	}
	run(t, w.env(), `{"session_id":"s-1","hook_event_name":"SessionStart","source":"resume","cwd":"/w/crius"}`)
	r, _ = sessionstate.ReadRole(filepath.Join(w.crius, "roles"), "quotes", "p1")
	if r.Thread != "" || r.Surface != "sf-3" {
		t.Errorf("resumed in cmux, role %+v", r)
	}
	// Any event refreshes the session's file (the role's file only changes with its session): bus-post reads the thread
	// there, so a session that runs on is found as soon as it runs one hook.
	run(t, env, `{"session_id":"s-1","hook_event_name":"Stop","cwd":"/w/crius"}`)
	if s, _ := sessionstate.Read(filepath.Join(w.crius, "sessions"), "s-1"); s.Thread != "mcp:launch:abc" {
		t.Errorf("after a Stop in T3 Code, session %+v", s)
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
	testutil.Write(t, filepath.Join(w.claude, "sessions", "2.json"), fmt.Sprintf(`{"pid":%d,"sessionId":"s-2","procStart":%q}`, os.Getpid(), testutil.ProcStart(t, os.Getpid())))
	dir := filepath.Join(w.crius, "sessions")
	if err := sessionstate.Write(dir, sessionstate.State{SessionID: "s-2", Name: "quotes · p9"}); err != nil {
		t.Fatal(err)
	}
	run(t, w.env(), `{"session_id":"s-2","hook_event_name":"SessionStart","source":"resume","cwd":"/w/quotes"}`)
	if s, _ := sessionstate.Read(dir, "s-2"); s.Name != "" || s.Role != "unknown" {
		t.Errorf("%+v", s)
	}
}

// One session's held lock must not stop another session's hook, nor its own: a hook that finds the lock held queues
// its event for the next hook and does not wait until Claude Code's hook timeout (review P3, 10-22; crius:lead 11-08).
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
	// s-1's prompt waits in its queue, not lost: the session's next hook after the lock is free applies it first
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	run(t, w.env(), `{"session_id":"s-1","hook_event_name":"PreToolUse","tool_name":"Bash"}`)
	if s, err := sessionstate.Read(filepath.Join(w.crius, "sessions"), "s-1"); err != nil || s.State != sessionstate.Running ||
		s.PromptSHA != sessionstate.PromptSHA("go") || s.Event != "PreToolUse" {
		t.Errorf("s-1: %+v %v", s, err)
	}
	if b, err := os.ReadFile(filepath.Join(w.crius, "logs", "crius-hook.log")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("log %q, %v", b, err)
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
		fmt.Sprintf(`{"pid":%d,"sessionId":"s-2","name":"quotes · p1","procStart":%q}`, os.Getpid(), testutil.ProcStart(t, os.Getpid())))
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

// A role's file keeps when its session id took the role (started), for wakewatch restart --from-ready: a new id sets it,
// a resume or compact of the same id keeps it (a T3 thread resumes on each line sent to it). A file written before
// started was kept goes by its time, the latest by which that session held the role.
func TestARoleFileKeepsWhenItsSessionTookTheRole(t *testing.T) {
	w := newWorld(t)
	path := filepath.Join(w.crius, "roles", "quotes.p1.json")
	read := func(after string) (id string, at, started time.Time) {
		t.Helper()
		var r struct {
			ID      string    `json:"session_id"`
			Time    time.Time `json:"time"`
			Started time.Time `json:"started"`
		}
		b, err := os.ReadFile(path)
		if err == nil {
			err = json.Unmarshal(b, &r)
		}
		if err != nil {
			t.Fatalf("after %s: %v", after, err)
		}
		return r.ID, r.Time, r.Started
	}
	run(t, w.env(), `{"session_id":"s-1","hook_event_name":"SessionStart","source":"startup","cwd":"/w/crius"}`)
	id, at, first := read("the start of s-1")
	if id != "s-1" || first.IsZero() || !first.Equal(at) {
		t.Fatalf("after the start of s-1: %s, time %v, started %v; want started at its time", id, at, first)
	}
	for _, src := range []string{"resume", "compact"} {
		run(t, w.env(), `{"session_id":"s-1","hook_event_name":"SessionStart","source":"`+src+`","cwd":"/w/crius"}`)
		if id, at, started := read(src); id != "s-1" || !started.Equal(first) || at.Before(first) {
			t.Errorf("after a %s of s-1: %s, time %v, started %v; want started kept at %v", src, id, at, started, first)
		}
	}
	// the live process now holds id (/clear), so the hook files id's events under the role
	holds := func(id string) {
		pid := os.Getpid()
		testutil.Write(t, filepath.Join(w.claude, "sessions", fmt.Sprintf("%d.json", pid)),
			fmt.Sprintf(`{"pid":%d,"sessionId":%q,"cwd":"/w/crius","name":"quotes · p1","procStart":%q}`, pid, id, testutil.ProcStart(t, pid)))
	}
	holds("s-2")
	run(t, w.env(), `{"session_id":"s-2","hook_event_name":"SessionStart","source":"clear","cwd":"/w/crius"}`)
	if id, at, started := read("the start of s-2"); id != "s-2" || !started.Equal(at) || started.Before(first) {
		t.Errorf("after the start of s-2: %s, time %v, started %v; want started at its time", id, at, started)
	}
	old := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	testutil.Write(t, path, `{"session_id":"s-3","event":"SessionStart","detail":"resume","time":"`+old.Format(time.RFC3339)+
		`","project":"quotes","role":"p1","cwd":"/w/crius"}`)
	holds("s-3")
	run(t, w.env(), `{"session_id":"s-3","hook_event_name":"SessionStart","source":"resume","cwd":"/w/crius"}`)
	if id, _, started := read("a resume of a file with no started"); id != "s-3" || !started.Equal(old) {
		t.Errorf("after a resume of a file with no started: %s, started %v; want its old time %v", id, started, old)
	}
	// a back-filled start is marked as only a bound, and stays marked through the next resume; a known one is not
	bound := func() bool { b, _ := os.ReadFile(path); return strings.Contains(string(b), `"started_bound":true`) }
	if !bound() {
		t.Errorf("after a resume of a file with no started: no started_bound mark")
	}
	run(t, w.env(), `{"session_id":"s-3","hook_event_name":"SessionStart","source":"compact","cwd":"/w/crius"}`)
	if _, _, started := read("a compact of s-3"); !started.Equal(old) || !bound() {
		t.Errorf("after a compact of s-3: started %v, bound %v; want %v kept, marked", started, bound(), old)
	}
	holds("s-4")
	run(t, w.env(), `{"session_id":"s-4","hook_event_name":"SessionStart","source":"clear","cwd":"/w/crius"}`)
	if bound() {
		t.Errorf("after the start of s-4: a known start is marked as a bound")
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
		fmt.Sprintf(`{"pid":%d,"sessionId":"s-2","cwd":"/w/crius","name":"quotes · p1","procStart":%q}`, os.Getpid(), testutil.ProcStart(t, os.Getpid())))
	run(t, w.env(), `{"session_id":"s-2","hook_event_name":"UserPromptSubmit","prompt":"go","cwd":"/w/crius"}`)
	s, err := sessionstate.ReadRole(roles, "quotes", "p1")
	if err != nil || s.SessionID != "s-2" || s.Config != w.claude {
		t.Fatalf("role file %+v, %v; want s-2 with config %s", s, err, w.claude)
	}
}

// Crius 10-29 00:28: cmux restored sessions with no CLAUDE_CONFIG_DIR, so they came back on the default folder,
// another account, and their SessionStart filed that folder as the role's: crius-spawn -resume then kept them there. A
// restart of the role's own session keeps the folder its start filed; the session's file says where it runs now.
func TestARestoreInAnotherFolderKeepsTheRolesFolder(t *testing.T) {
	w := newWorld(t)
	roles := filepath.Join(w.crius, "roles")
	run(t, w.env(), `{"session_id":"s-1","hook_event_name":"SessionStart","source":"startup","cwd":"/w/crius"}`)
	def := filepath.Join(w.home, ".claude")
	testutil.Write(t, filepath.Join(def, "sessions", fmt.Sprintf("%d.json", os.Getpid())),
		fmt.Sprintf(`{"pid":%d,"sessionId":"s-1","cwd":"/w/crius","name":"quotes · p1","procStart":%q}`, os.Getpid(), testutil.ProcStart(t, os.Getpid())))
	var env []string
	for _, e := range w.env() {
		if !strings.HasPrefix(e, "CLAUDE_CONFIG_DIR=") {
			env = append(env, e)
		}
	}
	env = append(env, "CLAUDE_CONFIG_DIR="+def)
	run(t, env, `{"session_id":"s-1","hook_event_name":"SessionStart","source":"resume","cwd":"/w/crius"}`)
	// Config stays where it runs: crius-spawn's "still runs" check and takesRole look for its process there (review).
	r, err := sessionstate.ReadRole(roles, "quotes", "p1")
	if err != nil || r.SessionID != "s-1" || r.Detail != "resume" || r.Config != def || r.FiledConfig() != w.claude {
		t.Fatalf("role file %+v, %v; want s-1 resumed, running in %s, filed in %s", r, err, def, w.claude)
	}
	// A /clear gives the same Claude a new id: still the role's account (review: the alert went quiet after it).
	testutil.Write(t, filepath.Join(def, "sessions", fmt.Sprintf("%d.json", os.Getpid())),
		fmt.Sprintf(`{"pid":%d,"sessionId":"s-2","cwd":"/w/crius","name":"quotes · p1","procStart":%q}`, os.Getpid(), testutil.ProcStart(t, os.Getpid())))
	run(t, env, `{"session_id":"s-2","hook_event_name":"SessionStart","source":"clear","cwd":"/w/crius"}`)
	if r, err = sessionstate.ReadRole(roles, "quotes", "p1"); err != nil || r.SessionID != "s-2" || r.FiledConfig() != w.claude {
		t.Fatalf("after /clear: role file %+v, %v; want s-2 filed in %s", r, err, w.claude)
	}
	// A restart of s-2 keeps it; a fresh start of a new session (crius-spawn, -move) files the folder it starts in.
	run(t, env, `{"session_id":"s-2","hook_event_name":"SessionStart","source":"startup","cwd":"/w/crius"}`)
	if r, err = sessionstate.ReadRole(roles, "quotes", "p1"); err != nil || r.FiledConfig() != w.claude {
		t.Fatalf("a restart of s-2: role file %+v, %v; want filed in %s", r, err, w.claude)
	}
	testutil.Write(t, filepath.Join(def, "sessions", fmt.Sprintf("%d.json", os.Getpid())),
		fmt.Sprintf(`{"pid":%d,"sessionId":"s-3","cwd":"/w/crius","name":"quotes · p1","procStart":%q}`, os.Getpid(), testutil.ProcStart(t, os.Getpid())))
	run(t, env, `{"session_id":"s-3","hook_event_name":"SessionStart","source":"startup","cwd":"/w/crius"}`)
	if r, err = sessionstate.ReadRole(roles, "quotes", "p1"); err != nil || r.SessionID != "s-3" || r.FiledConfig() != def {
		t.Fatalf("a new session: role file %+v, %v; want s-3 filed in %s", r, err, def)
	}
}

// crius:lead 11-07 19:58: seven roles moved, same session, from one Max account to another (max3 to max2) kept max3 as
// their filed folder, so wakewatch called the move a wrong account and -resume would take them back. A resume in a
// folder of another Max row of the same persona files that folder; one in another persona's or an enterprise folder
// keeps the role's.
func TestAResumeOnAnotherMaxAccountFilesItsFolder(t *testing.T) {
	h := t.TempDir()
	crius := filepath.Join(h, ".crius")
	testutil.Write(t, filepath.Join(crius, "projects.toml"), "[projects.team]\ncwd = [\"/w/team\"]\n")
	s := testutil.MaxSeats(t, h, crius)
	roles := filepath.Join(crius, "roles")
	in := func(cfg, source string) {
		testutil.Write(t, filepath.Join(cfg, "sessions", fmt.Sprintf("%d.json", os.Getpid())),
			fmt.Sprintf(`{"pid":%d,"sessionId":"s-1","cwd":"/w/team","name":"team · r1","procStart":%q}`, os.Getpid(), testutil.ProcStart(t, os.Getpid())))
		env := []string{"HOME=" + h, "CRIUS_HOME=" + crius, "CLAUDE_CONFIG_DIR=" + cfg, "CMUX_WORKSPACE_ID=ws-9", "CMUX_SURFACE_ID=sf-3"}
		run(t, env, `{"session_id":"s-1","hook_event_name":"SessionStart","source":"`+source+`","cwd":"/w/team"}`)
	}
	in(s.WorkB, "startup")
	for _, c := range []struct{ cfg, filed string }{
		{s.WorkA, s.WorkA},    // max3 -> max2, one persona: the move is filed
		{s.Personal, s.WorkA}, // another persona's Max folder (a restore with no CLAUDE_CONFIG_DIR): kept
		{s.Ent, s.WorkA},      // the enterprise row: kept
		{s.WorkB, s.WorkB},    // back on max3
	} {
		in(c.cfg, "resume")
		r, err := sessionstate.ReadRole(roles, "team", "r1")
		if err != nil || r.SessionID != "s-1" || r.Config != c.cfg || r.FiledConfig() != c.filed {
			t.Fatalf("resumed in %s: role file %+v, %v; want filed in %s", c.cfg, r, err, c.filed)
		}
	}
	// accounts.toml unreadable: the role keeps its folder, and the log says why.
	testutil.Write(t, filepath.Join(crius, "accounts.toml"), "[[account]\n")
	in(s.WorkA, "resume")
	if r, err := sessionstate.ReadRole(roles, "team", "r1"); err != nil || r.FiledConfig() != s.WorkB {
		t.Fatalf("accounts.toml unread: role file %+v, %v; want filed in %s", r, err, s.WorkB)
	}
	if log := testutil.Read(t, filepath.Join(crius, "logs", "crius-hook.log")); !strings.Contains(log, "accounts.toml") ||
		!strings.Contains(log, "keeps its filed folder") {
		t.Fatalf("log %q, want the unread accounts.toml and the kept folder", log)
	}
}

// Review of item 8: after /clear the new id's first event may come before its start; in the role's own tab it keeps the
// role's folder. A /clear in another tab of the same role files its own.
func TestAClearInTheRolesTabKeepsItsFolder(t *testing.T) {
	w := newWorld(t)
	roles := filepath.Join(w.crius, "roles")
	run(t, w.env(), `{"session_id":"s-1","hook_event_name":"SessionStart","source":"startup","cwd":"/w/crius"}`)
	def := filepath.Join(w.home, ".claude")
	status := func(id string) {
		testutil.Write(t, filepath.Join(def, "sessions", fmt.Sprintf("%d.json", os.Getpid())),
			fmt.Sprintf(`{"pid":%d,"sessionId":%q,"cwd":"/w/crius","name":"quotes · p1","procStart":%q}`, os.Getpid(), id, testutil.ProcStart(t, os.Getpid())))
	}
	in := func(surface string) []string {
		var env []string
		for _, e := range w.env() {
			if !strings.HasPrefix(e, "CLAUDE_CONFIG_DIR=") && !strings.HasPrefix(e, "CMUX_SURFACE_ID=") {
				env = append(env, e)
			}
		}
		return append(env, "CLAUDE_CONFIG_DIR="+def, "CMUX_SURFACE_ID="+surface)
	}
	status("s-1")
	run(t, in("sf-3"), `{"session_id":"s-1","hook_event_name":"SessionStart","source":"resume","cwd":"/w/crius"}`)
	status("s-2") // /clear: the same process now holds s-2, and its prompt is filed before its start
	run(t, in("sf-3"), `{"session_id":"s-2","hook_event_name":"UserPromptSubmit","prompt":"go","cwd":"/w/crius"}`)
	r, err := sessionstate.ReadRole(roles, "quotes", "p1")
	if err != nil || r.SessionID != "s-2" || r.FiledConfig() != w.claude {
		t.Fatalf("role file %+v, %v; want s-2 filed in %s", r, err, w.claude)
	}
	status("s-3")
	run(t, in("sf-9"), `{"session_id":"s-3","hook_event_name":"SessionStart","source":"clear","cwd":"/w/crius"}`)
	if r, err = sessionstate.ReadRole(roles, "quotes", "p1"); err != nil || r.SessionID != "s-3" || r.FiledConfig() != def {
		t.Fatalf("another tab's /clear: role file %+v, %v; want s-3 filed in %s", r, err, def)
	}
}

// OF4: a session's start prunes the files of sessions kept no longer; a prompt does not (it runs on every turn).
func TestASessionStartPrunesOldSessions(t *testing.T) {
	w := newWorld(t)
	dir := filepath.Join(w.crius, "sessions")
	old := time.Now().Add(-8 * 24 * time.Hour)
	if err := sessionstate.Write(dir, sessionstate.State{SessionID: "gone-1", State: sessionstate.Ended, Time: old}); err != nil {
		t.Fatal(err)
	}
	testutil.Write(t, filepath.Join(dir, "gone-1.json.lock"), "")
	for _, f := range []string{"gone-1.json", "gone-1.json.lock"} {
		if err := os.Chtimes(filepath.Join(dir, f), old, old); err != nil {
			t.Fatal(err)
		}
	}
	run(t, w.env(), `{"session_id":"s-1","hook_event_name":"UserPromptSubmit","prompt":"go"}`)
	if _, err := os.Stat(filepath.Join(dir, "gone-1.json")); err != nil {
		t.Fatalf("a prompt pruned: %v", err)
	}
	run(t, w.env(), `{"session_id":"s-1","hook_event_name":"SessionStart","source":"startup"}`)
	left, _ := filepath.Glob(filepath.Join(dir, "*"))
	want := []string{filepath.Join(dir, ".queue"), filepath.Join(dir, "s-1.json"), filepath.Join(dir, "s-1.json.lock")}
	if !reflect.DeepEqual(left, want) {
		t.Errorf("left %q, want %q", left, want)
	}
	if q, _ := os.ReadDir(filepath.Join(dir, ".queue")); len(q) != 0 {
		t.Errorf("queue left %v", q)
	}
	if b, err := os.ReadFile(filepath.Join(w.crius, "logs", "crius-hook.log")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("log %q, %v", b, err)
	}
}

// Review of the queue P3-1: Prune holds a session's lock without applying its queue, and a hook killed while it held
// the lock leaves its queue: the next start of any session applies it.
func TestAStartAppliesAQueueLeftBehind(t *testing.T) {
	w := newWorld(t)
	dir := filepath.Join(w.crius, "sessions")
	stopped := time.Now().Add(-time.Hour).Round(time.Second)
	if err := sessionstate.Queue(dir, sessionstate.Event{In: sessionstate.Input{SessionID: "s-2", Event: "Stop"}, Time: stopped}); err != nil {
		t.Fatal(err)
	}
	run(t, w.env(), `{"session_id":"s-1","hook_event_name":"SessionStart","source":"startup"}`)
	// applied an hour late, it keeps its own time: an old Stop is not "idle since now" (review r2 P3-3)
	if s, err := sessionstate.Read(dir, "s-2"); err != nil || s.State != sessionstate.Idle || !s.Time.Equal(stopped) {
		t.Errorf("s-2: %+v %v", s, err)
	}
	if left, _ := sessionstate.Queued(dir, "s-2"); len(left) != 0 {
		t.Errorf("left %v", left)
	}
}

// Review of the queue P2-1: a hook applies queued events only up to its limit, inside Claude Code's hook timeout; the
// rest wait for the session's next hook and the error says how many.
func TestADrainPastItsLimitLeavesTheRestAndSaysSo(t *testing.T) {
	w := newWorld(t)
	t.Setenv("CRIUS_HOME", w.crius)
	dir := filepath.Join(w.crius, "sessions")
	for _, e := range []string{"UserPromptSubmit", "Stop"} {
		if err := sessionstate.Queue(dir, sessionstate.Event{In: sessionstate.Input{SessionID: "s-1", Event: e}, Time: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	err := drain(w.crius, dir, "s-1", time.Now().Add(-time.Second))
	if err == nil || !strings.Contains(err.Error(), "s-1: 2 queued events left for its next hook at the 3s limit") {
		t.Errorf("drain: %v", err)
	}
	if left, _ := sessionstate.Queued(dir, "s-1"); len(left) != 2 {
		t.Errorf("left %v", left)
	}
	if err := drain(w.crius, dir, "s-1", time.Now().Add(drainFor)); err != nil {
		t.Fatal(err)
	}
	if s, err := sessionstate.Read(dir, "s-1"); err != nil || s.State != sessionstate.Idle || s.Event != "Stop" {
		t.Errorf("s-1: %+v %v", s, err)
	}
}
