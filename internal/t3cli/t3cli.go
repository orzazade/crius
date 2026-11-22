// Package t3cli runs the T3 Code CLI (`t3`, v0.1.10) for Crius's tools: one way to call it (Bin, Run), the shape
// `t3 thread show --json` prints (Show), and Deliver, which hands a message to a thread and proves it arrived.
package t3cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var (
	// ErrNoAnswer marks a t3 call killed at its time limit: what it did is unknown.
	ErrNoAnswer = errors.New("t3 gave no answer in time")
	// ErrNotSent marks a message t3 refused to send: the caller may send it again.
	ErrNotSent = errors.New("not sent")
	// ErrUnproved marks a message that may have gone, but no `t3 thread show` proved it: sending it again could give it
	// twice.
	ErrUnproved = errors.New("sent, but t3 thread show did not prove it arrived")
)

var (
	// ProveTries is how many times Deliver reads the thread for its proof, ProveWait the pause between: T3 writes the
	// run a moment after the send returns. Tests shorten the wait.
	ProveTries = 5
	ProveWait  = 500 * time.Millisecond
)

// showMessages is how many of a thread's latest messages Deliver reads for the one it sent.
const showMessages = "20"

// Bin is the t3 link in the Crius bin folder when there is one (nvm keeps t3 off the PATH of launchd jobs and of other
// shells), a link that points nowhere too (running it then fails under its own name); else fallback.
func Bin(home, fallback string) string {
	p := filepath.Join(home, "bin", "t3")
	if _, err := os.Lstat(p); err == nil {
		return p
	}
	return fallback
}

// Run runs bin with args and stdin, bounded by timeout. err is nil when t3 exited 0 (a child it left holding the pipes
// does not count), else it wraps ErrNoAnswer when the limit killed it.
func Run(bin string, timeout time.Duration, stdin string, args ...string) (stdout, stderr []byte, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var out, errOut bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = strings.NewReader(stdin), &out, &errOut
	cmd.WaitDelay = time.Second
	if err = cmd.Run(); err != nil && !(errors.Is(err, exec.ErrWaitDelay) && ctx.Err() == nil) {
		if ctx.Err() != nil {
			err = fmt.Errorf("%w (%s)", ErrNoAnswer, timeout)
		}
		return out.Bytes(), errOut.Bytes(), err
	}
	return out.Bytes(), errOut.Bytes(), nil
}

// FirstLine gives the first line t3 printed across the outputs given: a Node stack trace after it says no more.
func FirstLine(outs ...[]byte) string {
	said, _, _ := strings.Cut(strings.TrimSpace(string(bytes.Join(outs, []byte("\n")))), "\n")
	return strings.TrimSpace(said)
}

// Show is what `t3 thread show --json` prints, cut to the fields Crius reads.
type Show struct {
	Thread *struct {
		Status      string  `json:"status"`
		ActiveRunID *string `json:"activeRunId"`
		LatestRunID *string `json:"latestRunId"`
		Pending     int     `json:"pendingRequestCount"`
		UpdatedAt   string  `json:"updatedAt"`
		Settled     bool    `json:"settled"`
		Archived    bool    `json:"archived"`
		// "subagent" for a thread T3 started with delegate_task, "fork" for a fork, null for a top-level thread
		RelationshipToParent string `json:"relationshipToParent"`
		// the thread's own background jobs still running (a command or a test run it started); T3 wakes it when one ends
		Background []struct {
			TaskID        string `json:"taskId"`
			Kind          string `json:"kind"` // "command", "subagent" (a native one or a delegate_task child)
			Description   string `json:"description"`
			ChildThreadID string `json:"childThreadId"` // a subagent's own thread
		} `json:"pendingBackgroundTasks"`
	} `json:"thread"`
	Messages []struct {
		Role  string `json:"role"`
		RunID string `json:"runId"`
		Text  string `json:"text"`
	} `json:"messages"`
}

// Delivery is what became of a message Deliver proved: RunID is the run T3 made for it, and Queued says the thread was
// busy on an earlier run, so it takes the message as its next turn (T3 never interrupts a run).
type Delivery struct {
	RunID  string
	Queued bool
}

// Deliver hands text to thread id with `t3 thread send --json <id> -` (the text on stdin) and proves it arrived with
// `t3 thread show --json`: the message is in the thread under the run the send named, or that run is the thread's latest
// while an earlier one is active (queued as its next turn). An error wraps ErrNotSent when t3 refused the send, and
// ErrUnproved when it may have gone but no look proved it.
func Deliver(bin, id, text string, timeout time.Duration) (Delivery, error) {
	out, errOut, err := Run(bin, timeout, text, "thread", "send", "--json", id, "-")
	if errors.Is(err, ErrNoAnswer) {
		return Delivery{}, fmt.Errorf("t3 thread send %s: %w: %w", id, err, ErrUnproved)
	}
	if err != nil {
		return Delivery{}, fmt.Errorf("t3 thread send %s: %w: %v: %s", id, ErrNotSent, err, FirstLine(errOut, out))
	}
	var sent struct {
		RunID string `json:"runId"`
	}
	if json.Unmarshal(out, &sent) != nil || sent.RunID == "" {
		return Delivery{}, fmt.Errorf("t3 thread send %s exited 0 but printed no run id (%.80q): %w", id, out, ErrUnproved)
	}
	var last error
	for try := 0; try < ProveTries; try++ {
		if try > 0 {
			time.Sleep(ProveWait)
		}
		var queued bool
		if queued, last = proved(bin, id, sent.RunID, text, timeout); last == nil {
			return Delivery{RunID: sent.RunID, Queued: queued}, nil
		}
	}
	return Delivery{RunID: sent.RunID}, fmt.Errorf("t3 thread send %s gave run %s: %w: %v", id, sent.RunID, ErrUnproved, last)
}

// proved reads thread id once and says whether it holds text under run (queued false) or names run as its latest while an
// earlier run is active (queued true); an error says what it saw instead.
func proved(bin, id, run, text string, timeout time.Duration) (queued bool, err error) {
	out, errOut, err := Run(bin, timeout, "", "thread", "show", "--json", "--messages", showMessages, id)
	if err != nil {
		return false, fmt.Errorf("t3 thread show: %v: %s", err, FirstLine(errOut, out))
	}
	var s Show
	if err := json.Unmarshal(out, &s); err != nil || s.Thread == nil {
		return false, fmt.Errorf("t3 thread show printed no thread JSON: %.80q", out)
	}
	// A run can hold more than one message (two queued on a busy thread): another one in it is not this one.
	for _, m := range s.Messages {
		if m.Role == "user" && m.RunID == run && squash(m.Text) == squash(text) {
			return false, nil
		}
	}
	active, latest := deref(s.Thread.ActiveRunID), deref(s.Thread.LatestRunID)
	r, ok := ordinal(run)
	a, aok := ordinal(active)
	l, lok := ordinal(latest)
	if ok && aok && lok && a < r && r <= l {
		return true, nil
	}
	return false, fmt.Errorf("the thread lists no message of run %s (active run %q, latest %q)", run, active, latest)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ordinal gives the number at the end of a run id (run:thread:<thread>:ordinal:N): runs of a thread count up.
func ordinal(run string) (int, bool) {
	i := strings.LastIndex(run, ":ordinal:")
	if i < 0 {
		return 0, false
	}
	n, err := strconv.Atoi(run[i+len(":ordinal:"):])
	return n, err == nil
}

// squash drops all white space: T3 may trim the text it was given.
func squash(s string) string { return strings.Join(strings.Fields(s), "") }
