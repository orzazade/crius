package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/orzazade/crius/internal/wakewatch"
)

// configureTool is the T3 Code MCP tool that, given a modelSelection, changes a thread's provider instance: that
// restarts the thread's provider session and kills its running background shells (11-18 20:37: two long test runs
// died when Crius moved two threads to another Max account).
const configureTool = "mcp__t3-code__t3_thread_configure"

// moveCheckT3Timeout bounds the t3 call: the server answers in 2-5 s under heavy load. The hook's own timeout in
// settings.json must be larger (45 s), or Claude Code cuts the check before it can say why.
const moveCheckT3Timeout = 30 * time.Second

// runMoveCheck is the PreToolUse hook for configureTool (crius-spawn -move-check, hook input on in). It prints
// nothing, which lets the call go on, for another tool, for a call with no modelSelection (it only reads), for one
// whose instanceId is the thread's current provider instance (nothing restarts), and for a move of a thread that is
// idle. It prints a deny with its reason for a move of a thread that is busy (an open run, an open request, a
// message after its last reply that waits for a turn, or a last reply whose newest status emoji is ⏳: RULES §3.5,
// its own background job runs), of the caller's own thread (no threadId: that thread is mid-turn while it makes the
// call, and its own jobs would die too), or of a thread whose state cannot be read (failed to look is never found
// nothing; the local t3 cannot read a thread on another machine either). A file move-ok/<threadId> (move-ok/self for
// the caller's own thread) under the Crius state folder turns one such deny into a go, on purpose: the check deletes
// it then, and only then.
func runMoveCheck(home string, in io.Reader, out io.Writer) {
	if reason := moveCheck(home, in); reason != "" {
		enc := json.NewEncoder(out)
		enc.SetEscapeHTML(false) // the reason's "&&" stays readable
		enc.Encode(map[string]any{"hookSpecificOutput": map[string]string{
			"hookEventName": "PreToolUse", "permissionDecision": "deny",
			"permissionDecisionReason": "crius-spawn -move-check: " + reason}})
	}
}

// moveCheck gives the reason to deny the call, "" to let it go on.
func moveCheck(home string, in io.Reader) string {
	var v struct {
		Tool  string `json:"tool_name"`
		Input struct {
			Thread string          `json:"threadId"`
			Model  json.RawMessage `json:"modelSelection"`
		} `json:"tool_input"`
	}
	if err := json.NewDecoder(in).Decode(&v); err != nil {
		return fmt.Sprintf("cannot read the hook input (%v), so no move of a T3 thread is let through.", err)
	}
	if v.Tool != configureTool || len(v.Input.Model) == 0 || string(v.Input.Model) == "null" {
		return ""
	}
	id, key := v.Input.Thread, v.Input.Thread
	if id == "" {
		key = "self"
	}
	if key == "." || key == ".." || strings.HasPrefix(key, "-") || strings.ContainsAny(key, `/\`) {
		return fmt.Sprintf("cannot read T3 thread %s: not a plain thread id.", id)
	}
	ok := filepath.Join(home, "state", "move-ok", key)
	// deny is the answer for a move the check would refuse: the override, when there is one, is used up only here.
	deny := func(why string) string {
		if os.Remove(ok) == nil {
			return ""
		}
		return fmt.Sprintf("%s To move it anyway, on purpose: mkdir -p %s && touch %s (good for one move; the check deletes it).",
			why, filepath.Dir(ok), ok)
	}
	if id == "" {
		return deny("this call has no threadId, so it would move the caller's own thread, which is mid-turn while it " +
			"makes the call; a new provider instance restarts its provider session and kills its running background " +
			"shells. Pass the thread id of an idle thread instead.")
	}
	th, instance, err := showThread(home, id)
	if err != nil {
		return deny(fmt.Sprintf("cannot read T3 thread %s, so it may be busy: %v.", id, err))
	}
	var sel struct {
		Instance string `json:"instanceId"`
	}
	json.Unmarshal(v.Input.Model, &sel) // not an object: no instanceId, checked as a change
	if instance != "" && sel.Instance == instance {
		return "" // the same provider instance: nothing restarts
	}
	var busy []string
	if th.Active {
		busy = append(busy, "a run is open (activeRunId)")
	} else if th.Status == "running" {
		busy = append(busy, "its status is running")
	}
	if th.Pending > 0 {
		busy = append(busy, fmt.Sprintf("%d open request(s) wait on someone", th.Pending))
	}
	if th.Unanswered && !th.Active {
		busy = append(busy, "a message after its last reply waits for a turn")
	}
	if newestMark(th.LastReply) == "⏳" {
		busy = append(busy, "its last reply ends on ⏳ (its own background job runs)")
	}
	if len(busy) == 0 {
		return ""
	}
	to := "(none given)"
	if sel.Instance != "" {
		to = strconv.Quote(sel.Instance)
	}
	reply := []rune(strings.Join(strings.Fields(th.LastReply), " "))
	tail := fmt.Sprintf("Last reply: %q.", string(reply[max(0, len(reply)-120):]))
	if len(reply) == 0 {
		tail = "No reply in its latest messages."
	}
	return deny(fmt.Sprintf("T3 thread %s is busy: %s. Moving it from provider instance %q to %s restarts its provider "+
		"session and kills its running background shells. %s Move it at a READY-RESTART, or once no run is open and its "+
		"last reply names no running job (ends on ✅ or ❓).", id, strings.Join(busy, "; "), instance, to, tail))
}

// showThread reads a thread with `t3 thread show --json`: the t3 in the Crius bin folder when there is one (nvm keeps
// t3 off a hook's PATH), else t3 on the PATH. It gives the thread's provider instance too ("" when t3 names none).
func showThread(home, id string) (wakewatch.T3Thread, string, error) {
	t3 := "t3"
	if p := filepath.Join(home, "bin", "t3"); fileExists(p) {
		t3 = p
	}
	ctx, cancel := context.WithTimeout(context.Background(), moveCheckT3Timeout)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, t3, "thread", "show", "--json", "--messages", "10", id)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil && !(errors.Is(err, exec.ErrWaitDelay) && ctx.Err() == nil) {
		if ctx.Err() != nil {
			err = fmt.Errorf("%w (%s)", wakewatch.ErrT3NoAnswer, moveCheckT3Timeout)
		}
		said, _, _ := strings.Cut(strings.TrimSpace(stderr.String()+"\n"+stdout.String()), "\n")
		return wakewatch.T3Thread{}, "", fmt.Errorf("t3 thread show: %v: %s", err, said)
	}
	var v struct {
		Thread *struct {
			Status      string  `json:"status"`
			Instance    string  `json:"providerInstanceId"`
			ActiveRunID *string `json:"activeRunId"`
			Pending     int     `json:"pendingRequestCount"`
			Background  []struct {
				TaskID      string `json:"taskId"`
				Description string `json:"description"`
			} `json:"pendingBackgroundTasks"`
		} `json:"thread"`
		Messages []struct {
			Role string `json:"role"`
			Text string `json:"text"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &v); err != nil || v.Thread == nil {
		return wakewatch.T3Thread{}, "", fmt.Errorf("t3 thread show printed no thread JSON: %.80q", stdout.String())
	}
	th := wakewatch.T3Thread{Status: v.Thread.Status, Pending: v.Thread.Pending,
		Active: v.Thread.ActiveRunID != nil && *v.Thread.ActiveRunID != ""}
	for _, bg := range v.Thread.Background { // -fresh waits for these; the move check does not read them
		job := bg.TaskID
		if bg.Description != "" {
			job += " (" + bg.Description + ")"
		}
		th.Background = append(th.Background, job)
	}
	for i := len(v.Messages) - 1; i >= 0; i-- {
		if v.Messages[i].Role == "assistant" {
			th.LastReply = v.Messages[i].Text
			break
		}
		th.Unanswered = th.Unanswered || v.Messages[i].Role == "user"
	}
	return th, v.Thread.Instance, nil
}

// newestMark gives the newest of ❓, ✅ and ⏳ in text, "" when it has none.
func newestMark(text string) string {
	j := strings.LastIndexAny(text, "❓✅⏳")
	if j < 0 {
		return ""
	}
	r, _ := utf8.DecodeRuneInString(text[j:])
	return string(r)
}

// fileExists says p is there, a link that points nowhere too: running it then fails under its own name.
func fileExists(p string) bool { _, err := os.Lstat(p); return err == nil }
