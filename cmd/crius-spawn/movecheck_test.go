package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// checkRig is a Crius home with no t3 of its own, and the fake t3 of testdata/bin on the PATH: it prints
// testdata/threads/<id>.json, and fails for a thread with no file there.
func checkRig(t *testing.T) (home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("CRIUS_HOME", home)
	bin, err := filepath.Abs(filepath.Join("testdata", "bin"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return home
}

// moveInput is the PreToolUse hook input for a t3_thread_configure call that changes the thread's model selection.
func moveInput(threadID string) string {
	id := ""
	if threadID != "" {
		id = `"threadId":"` + threadID + `",`
	}
	return `{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"mcp__t3-code__t3_thread_configure","tool_input":{` + id +
		`"modelSelection":{"instanceId":"claude-max2","model":"claude-opus-5-5","provider":"claude","options":{}}}}`
}

// decide runs the check on one hook input and gives the deny reason, "" when it allows (prints nothing).
func decide(t *testing.T, home, input string) string {
	t.Helper()
	var out bytes.Buffer
	runMoveCheck(home, strings.NewReader(input), &out)
	if out.Len() == 0 {
		return ""
	}
	var v struct {
		H struct {
			Event    string `json:"hookEventName"`
			Decision string `json:"permissionDecision"`
			Reason   string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &v); err != nil {
		t.Fatalf("not hook JSON: %v: %q", err, out.String())
	}
	if v.H.Event != "PreToolUse" || v.H.Decision != "deny" || v.H.Reason == "" {
		t.Fatalf("not a deny with a reason: %q", out.String())
	}
	return v.H.Reason
}

func wantIn(t *testing.T, reason string, parts ...string) {
	t.Helper()
	if reason == "" {
		t.Fatalf("allowed; want a deny naming %q", parts)
	}
	for _, p := range parts {
		if !strings.Contains(reason, p) {
			t.Fatalf("deny reason lacks %q:\n%s", p, reason)
		}
	}
}

// 11-18 20:37: two threads moved to another account mid-job lost their background test runs. A thread with an open run
// is never moved.
func TestMoveCheckDeniesAThreadWithAnOpenRun(t *testing.T) {
	home := checkRig(t)
	wantIn(t, decide(t, home, moveInput("th-run")), "th-run", "a run is open", "Running the full suite now.",
		"READY-RESTART", filepath.Join(home, "state", "move-ok", "th-run"))
}

// RULES §3.5: a last reply that ends on ⏳ says the thread's own background job runs; a restart would kill it.
func TestMoveCheckDeniesAThreadWhoseLastReplyEndsOnAnHourglass(t *testing.T) {
	home := checkRig(t)
	wantIn(t, decide(t, home, moveInput("th-wait")), "th-wait", "ends on ⏳", "in the background (shell b3), about 35 min. ⏳")
}

func TestMoveCheckDeniesAThreadWithAnOpenRequest(t *testing.T) {
	home := checkRig(t)
	wantIn(t, decide(t, home, moveInput("th-ask")), "th-ask", "1 open request")
}

// A message sent after the last reply, with no run open yet, starts a turn any moment: moving the thread then cuts it.
func TestMoveCheckDeniesAThreadWithAMessageQueuedAfterItsLastReply(t *testing.T) {
	home := checkRig(t)
	wantIn(t, decide(t, home, moveInput("th-queued")), "th-queued", "a message after its last reply waits for a turn",
		"Batch two merged. ✅")
}

// The newest status emoji counts: an ⏳ earlier in a reply that ends on ✅ names a job that is over.
func TestMoveCheckAllowsAnIdleThreadThatEndsOnACheckMark(t *testing.T) {
	home := checkRig(t)
	if r := decide(t, home, moveInput("th-done")); r != "" {
		t.Fatalf("an idle thread whose reply ends on ✅ was denied: %s", r)
	}
}

// With no modelSelection the tool only reads the thread's settings: nothing restarts.
func TestMoveCheckAllowsAReadAndOtherTools(t *testing.T) {
	home := checkRig(t)
	for _, in := range []string{
		`{"tool_name":"mcp__t3-code__t3_thread_configure","tool_input":{"threadId":"th-run"}}`,
		`{"tool_name":"mcp__t3-code__t3_thread_configure","tool_input":{"threadId":"th-run","modelSelection":null}}`,
		`{"tool_name":"mcp__t3-code__t3_thread_read","tool_input":{"threadId":"th-run"}}`,
	} {
		if r := decide(t, home, in); r != "" {
			t.Fatalf("%s was denied: %s", in, r)
		}
	}
}

// "Failed to look" is never "found nothing": a thread the local t3 cannot show (unknown here, or on another machine),
// JSON that is no thread, or hook input that cannot be read is a deny that says why and names the override.
func TestMoveCheckDeniesWhenTheStateCannotBeRead(t *testing.T) {
	home := checkRig(t)
	wantIn(t, decide(t, home, moveInput("th-elsewhere")), "cannot read T3 thread th-elsewhere",
		"Thread th-elsewhere is no longer available.", filepath.Join(home, "state", "move-ok", "th-elsewhere"))
	wantIn(t, decide(t, home, moveInput("th-bad")), "cannot read T3 thread th-bad", "printed no thread JSON",
		filepath.Join(home, "state", "move-ok", "th-bad"))
	wantIn(t, decide(t, home, `{"tool_name":`), "cannot read the hook input")
	t.Setenv("PATH", t.TempDir())
	wantIn(t, decide(t, home, moveInput("th-done")), "cannot read T3 thread th-done", "t3")
}

// A call with no threadId configures the caller's own thread, which is mid-turn while it makes the call.
func TestMoveCheckDeniesMovingTheCallersOwnThread(t *testing.T) {
	home := checkRig(t)
	wantIn(t, decide(t, home, moveInput("")), "own thread", filepath.Join(home, "state", "move-ok", "self"))
}

// The override lets Crius move a thread on purpose: one move per file, which the check deletes.
func TestMoveCheckOverrideAllowsOneMoveAndIsConsumed(t *testing.T) {
	home := checkRig(t)
	dir := filepath.Join(home, "state", "move-ok")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"th-run", "self", "th-elsewhere"} {
		if err := os.WriteFile(filepath.Join(dir, id), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"th-run", "", "th-elsewhere"} {
		if r := decide(t, home, moveInput(id)); r != "" {
			t.Fatalf("move of %q with its override file was denied: %s", id, r)
		}
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("override files left after use: %v", left)
	}
	wantIn(t, decide(t, home, moveInput("th-run")), "a run is open")
	// An id that is no plain file name gets no override file outside the folder.
	wantIn(t, decide(t, home, moveInput("../th-run")), "cannot read T3 thread ../th-run")
}
