package sessionstate

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 22, 14, 30, 0, 0, time.UTC)

func TestEachHookEventGivesItsState(t *testing.T) {
	for _, c := range []struct {
		in   Input
		want string
	}{
		{Input{Event: "SessionStart", Source: "startup"}, Idle},
		{Input{Event: "SessionStart", Source: "resume"}, Idle},
		{Input{Event: "UserPromptSubmit", Prompt: "go"}, Running},
		{Input{Event: "PreToolUse", ToolName: "Bash"}, Running},
		{Input{Event: "PermissionRequest", ToolName: "Bash"}, Waiting},
		{Input{Event: "Notification", NotificationType: "permission_prompt"}, Waiting},
		{Input{Event: "Notification", NotificationType: "elicitation_dialog"}, Waiting},
		{Input{Event: "Notification", NotificationType: "elicitation_url_dialog"}, Waiting},
		{Input{Event: "Notification", NotificationType: "worker_permission_prompt"}, Waiting},
		{Input{Event: "Notification", NotificationType: "idle_prompt"}, Idle},
		{Input{Event: "Stop"}, Idle},
		{Input{Event: "SessionEnd", Reason: "clear"}, Ended},
	} {
		c.in.SessionID = "s1"
		got := Apply(State{State: "before"}, c.in, t0)
		if got.State != c.want || got.Event != c.in.Event || !got.Time.Equal(t0) || got.SessionID != "s1" {
			t.Errorf("%+v: got state %q event %q time %v id %q, want %q", c.in, got.State, got.Event, got.Time, got.SessionID, c.want)
		}
	}
}

// A notification that says nothing about who the session waits on (auth_success, push_notification, …) keeps the
// state it had: guessing idle or running from it would be a state nobody saw.
func TestANotificationOfAnotherKindKeepsTheState(t *testing.T) {
	for _, kind := range []string{"auth_success", "push_notification", "agent_completed", ""} {
		got := Apply(State{State: Running}, Input{SessionID: "s1", Event: "Notification", NotificationType: kind}, t0)
		if got.State != Running || got.Detail != kind || !got.Time.Equal(t0) {
			t.Errorf("%q: got %+v", kind, got)
		}
	}
}

func TestTheDetailNamesWhatHappened(t *testing.T) {
	for _, c := range []struct {
		in   Input
		want string
	}{
		{Input{Event: "SessionStart", Source: "compact"}, "compact"},
		{Input{Event: "SessionEnd", Reason: "prompt_input_exit"}, "prompt_input_exit"},
		{Input{Event: "PreToolUse", ToolName: "Edit"}, "Edit"},
		{Input{Event: "PermissionRequest", ToolName: "Write"}, "Write"},
		{Input{Event: "Notification", NotificationType: "permission_prompt"}, "permission_prompt"},
		{Input{Event: "Stop"}, ""},
	} {
		if got := Apply(State{Detail: "old"}, c.in, t0).Detail; got != c.want {
			t.Errorf("%+v: detail %q, want %q", c.in, got, c.want)
		}
	}
}

// The prompt's hash stays until the next prompt: a watcher reads it after the turn has moved on to tools or ended.
func TestThePromptHashOutlivesLaterEvents(t *testing.T) {
	s := Apply(State{}, Input{SessionID: "s1", Event: "UserPromptSubmit", Prompt: "wake: quotes p1 idle\n"}, t0)
	if s.PromptSHA == "" || s.PromptSHA != PromptSHA("wake: quotes p1 idle") || !s.PromptTime.Equal(t0) {
		t.Fatalf("after the prompt: %+v", s)
	}
	for _, ev := range []string{"PreToolUse", "Notification", "Stop"} {
		s = Apply(s, Input{SessionID: "s1", Event: ev}, t0.Add(time.Minute))
	}
	if s.PromptSHA != PromptSHA("wake: quotes p1 idle") || !s.PromptTime.Equal(t0) {
		t.Errorf("after the turn: %+v", s)
	}
}

func TestPromptSHAIsSixteenHexDigitsOfTheTrimmedPrompt(t *testing.T) {
	a, b := PromptSHA("  hello\n"), PromptSHA("hello")
	if len(a) != 16 || a != b || PromptSHA("hello!") == a {
		t.Errorf("%q %q %q", a, b, PromptSHA("hello!"))
	}
}

func TestWriteThenReadGivesTheSameState(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	s := State{SessionID: "abc-1", State: Waiting, Event: "Notification", Detail: "permission_prompt", Time: t0,
		Cwd: "/w", Name: "quotes: p1", Project: "quotes", Role: "p1", Workspace: "ws", Surface: "sf",
		PromptSHA: "0123456789abcdef", PromptTime: t0, Waits: []string{"main"}}
	if err := Write(dir, s); err != nil {
		t.Fatal(err)
	}
	got, err := Read(dir, "abc-1")
	if err != nil || !reflect.DeepEqual(got, s) {
		t.Fatalf("read %+v, %v; want %+v", got, err, s)
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("dir: %v %v", fi, err)
	}
	left, _ := filepath.Glob(filepath.Join(dir, "*"))
	if len(left) != 1 {
		t.Errorf("files left: %v", left) // no temp file stays behind
	}
}

// A session id becomes a file name: one that could leave the folder is refused.
func TestASessionIDThatIsNotAPlainNameIsRefused(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"", "../x", "a/b", ".", ".."} {
		if err := Write(dir, State{SessionID: id}); err == nil {
			t.Errorf("%q written", id)
		}
		if _, err := Read(dir, id); err == nil {
			t.Errorf("%q read", id)
		}
	}
}

func TestReadingAMissingSessionIsAnError(t *testing.T) {
	if _, err := Read(t.TempDir(), "nope"); err == nil {
		t.Error("no error")
	}
}

// After a person answers a dialog the tool runs: PostToolUse (or its failure, or a denial) means running again, not
// still waiting until the next tool or the end of the turn (review P1, 10-22).
func TestAnAnsweredDialogIsRunningAgain(t *testing.T) {
	for _, after := range []string{"PostToolUse", "PostToolUseFailure", "PermissionDenied", "PreToolUse"} {
		s := Apply(State{}, Input{SessionID: "s1", Event: "PreToolUse", ToolName: "Bash"}, t0)
		s = Apply(s, Input{SessionID: "s1", Event: "PermissionRequest", ToolName: "Bash"}, t0)
		s = Apply(s, Input{SessionID: "s1", Event: after, ToolName: "Bash"}, t0)
		if s.State != Running || len(s.Waits) != 0 {
			t.Errorf("after %s: %+v", after, s)
		}
	}
}

// Subagents share the session id. A tool of subagent B must not hide the dialog subagent A waits on.
func TestOneSubagentsDialogStaysWhileAnotherWorks(t *testing.T) {
	s := Apply(State{}, Input{SessionID: "s1", Event: "PermissionRequest", AgentID: "a"}, t0)
	s = Apply(s, Input{SessionID: "s1", Event: "PreToolUse", AgentID: "b"}, t0)
	s = Apply(s, Input{SessionID: "s1", Event: "PostToolUse", AgentID: "b"}, t0)
	if s.State != Waiting || len(s.Waits) != 1 || s.Waits[0] != "a" {
		t.Fatalf("while a waits: %+v", s)
	}
	s = Apply(s, Input{SessionID: "s1", Event: "PostToolUse", AgentID: "a"}, t0)
	if s.State != Running || len(s.Waits) != 0 {
		t.Errorf("after a's tool ran: %+v", s)
	}
}

// The end of the turn, an idle prompt, a new start and the end of the session close every open dialog.
func TestTheTurnsEndClosesEveryDialog(t *testing.T) {
	for _, in := range []Input{{Event: "Stop"}, {Event: "Notification", NotificationType: "idle_prompt"},
		{Event: "SessionStart", Source: "resume"}, {Event: "SessionEnd", Reason: "other"}} {
		in.SessionID = "s1"
		got := Apply(State{State: Waiting, Waits: []string{"a", "main"}}, in, t0)
		if got.State == Waiting || len(got.Waits) != 0 {
			t.Errorf("%+v: %+v", in, got)
		}
	}
}

// Auto-compact fires SessionStart (source compact) in the middle of a turn: the session is still working.
func TestACompactKeepsTheState(t *testing.T) {
	got := Apply(State{State: Running}, Input{SessionID: "s1", Event: "SessionStart", Source: "compact"}, t0)
	if got.State != Running || got.Detail != "compact" {
		t.Errorf("%+v", got)
	}
}

// List gives every session's state in a folder. A damaged file is named in the error and the others are still given;
// the hook's temp and lock files are not states. A missing folder is fs.ErrNotExist: the hook never ran here.
func TestListGivesEveryStateAndNamesADamagedFile(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"a1", "b2"} {
		if err := Write(dir, State{SessionID: id, State: Idle, Time: t0}); err != nil {
			t.Fatal(err)
		}
	}
	for name, text := range map[string]string{"c3.json": `{"state":"runn`, ".tmp-123": "{", ".lock": ""} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := List(dir)
	if len(got) != 2 || got[0].SessionID != "a1" || got[1].SessionID != "b2" {
		t.Errorf("%+v", got)
	}
	if !errors.Is(err, ErrDamaged) || !strings.Contains(err.Error(), "c3.json") {
		t.Errorf("err %v", err)
	}
	if _, err := List(filepath.Join(dir, "none")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing folder: %v", err)
	}
}

// Claude Code 2.1.308 builds the Notification hook input without the agent's context: it never has agent_id. A
// subagent's dialog that a person takes more than 6 s to answer gets one; it must not add "main", which nothing clears
// while the main agent waits in its Agent tool (review P1, 10-22).
func TestANotificationDoesNotAddMainToASubagentsDialog(t *testing.T) {
	s := Apply(State{}, Input{SessionID: "s1", Event: "PermissionRequest", AgentID: "a"}, t0)
	s = Apply(s, Input{SessionID: "s1", Event: "Notification", NotificationType: "permission_prompt"}, t0)
	s = Apply(s, Input{SessionID: "s1", Event: "PostToolUse", AgentID: "a"}, t0)
	if s.State != Running || len(s.Waits) != 0 {
		t.Errorf("after a's tool ran: %+v", s)
	}
}

// A dialog no PermissionRequest opened (an MCP elicitation) is known only from its notification, which names no
// agent: it waits until any agent's next tool event or the turn's end.
func TestADialogOnlyANotificationOpenedClosesOnAnyToolEvent(t *testing.T) {
	s := Apply(State{State: Running}, Input{SessionID: "s1", Event: "Notification", NotificationType: "elicitation_dialog"}, t0)
	if s.State != Waiting || len(s.Waits) != 1 {
		t.Fatalf("after the notification: %+v", s)
	}
	s = Apply(s, Input{SessionID: "s1", Event: "PostToolUse", AgentID: "b"}, t0)
	if s.State != Running || len(s.Waits) != 0 {
		t.Errorf("after a tool event: %+v", s)
	}
}

// A person's "No" or Esc at a dialog fires no hook. The next prompt can only be submitted when no dialog holds the
// box, so it closes every dialog, a subagent's too (review of 2026-10-22).
func TestAPromptClosesEveryDialog(t *testing.T) {
	s := Apply(State{}, Input{SessionID: "s1", Event: "PermissionRequest", AgentID: "a"}, t0)
	s = Apply(s, Input{SessionID: "s1", Event: "PermissionRequest"}, t0)
	s = Apply(s, Input{SessionID: "s1", Event: "UserPromptSubmit", Prompt: "go on"}, t0)
	if s.State != Running || len(s.Waits) != 0 {
		t.Errorf("after the prompt: %+v", s)
	}
}

// Esc while the main agent's tool runs fires PostToolUseFailure with is_interrupt and no Stop: the turn is over.
// A subagent's interrupted tool does not end the turn.
func TestAnInterruptedToolEndsTheTurn(t *testing.T) {
	s := Apply(State{}, Input{SessionID: "s1", Event: "PermissionRequest", AgentID: "a"}, t0)
	s = Apply(s, Input{SessionID: "s1", Event: "PreToolUse", ToolName: "Bash"}, t0)
	if got := Apply(s, Input{SessionID: "s1", Event: "PostToolUseFailure", ToolName: "Bash", IsInterrupt: true}, t0); got.State != Idle || len(got.Waits) != 0 {
		t.Errorf("main interrupted: %+v", got)
	}
	s = Apply(State{}, Input{SessionID: "s1", Event: "PreToolUse", AgentID: "b"}, t0)
	if got := Apply(s, Input{SessionID: "s1", Event: "PostToolUseFailure", AgentID: "b", IsInterrupt: true}, t0); got.State != Running {
		t.Errorf("subagent interrupted: %+v", got)
	}
}
