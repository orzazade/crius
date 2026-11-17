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
	"strings"
	"time"

	"github.com/orzazade/crius/internal/store"
	"github.com/orzazade/crius/internal/wakewatch"
)

// t3Timeout bounds one t3 call: it takes about 1 s; a hung server must not hold the run.
const t3Timeout = 10 * time.Second

// t3Messages is how many of a thread's latest messages are read to find its last reply: a reply comes as several
// assistant messages, and a message sent to the thread may follow it.
const t3Messages = "10"

// thread reads a T3 Code thread with `t3 thread show --json`: the t3 in the Crius bin folder when there is one (nvm keeps
// t3 off launchd's PATH), else t3 on the PATH.
func thread(id string) (wakewatch.T3Thread, error) {
	t3 := "t3"
	if p := filepath.Join(store.Home(), "bin", "t3"); fileThere(p) {
		t3 = p
	}
	ctx, cancel := context.WithTimeout(context.Background(), t3Timeout)
	defer cancel()
	var out, errOut bytes.Buffer
	cmd := exec.CommandContext(ctx, t3, "thread", "show", "--json", "--messages", t3Messages, id)
	cmd.Stdout, cmd.Stderr = &out, &errOut
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil && !(errors.Is(err, exec.ErrWaitDelay) && ctx.Err() == nil) {
		said, _, _ := strings.Cut(strings.TrimSpace(errOut.String()+"\n"+out.String()), "\n") // a stack trace after it says no more
		if ctx.Err() != nil {
			err = fmt.Errorf("%w (%s)", wakewatch.ErrT3NoAnswer, t3Timeout)
		}
		return wakewatch.T3Thread{}, fmt.Errorf("t3 thread show: %v: %s", err, said)
	}
	return parseThread(out.Bytes())
}

// parseThread reads what `t3 thread show --json` prints. JSON that has no thread or no update time is an error: the
// check would read it as a thread idle since 0001.
func parseThread(b []byte) (wakewatch.T3Thread, error) {
	var v struct {
		Thread *struct {
			Status      string  `json:"status"`
			ActiveRunID *string `json:"activeRunId"`
			Pending     int     `json:"pendingRequestCount"`
			UpdatedAt   string  `json:"updatedAt"`
			Settled     bool    `json:"settled"`
			Archived    bool    `json:"archived"`
		} `json:"thread"`
		Messages []struct {
			Role string `json:"role"`
			Text string `json:"text"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return wakewatch.T3Thread{}, fmt.Errorf("t3 thread show printed no thread JSON (%v): %.80q", err, b)
	}
	if v.Thread == nil {
		return wakewatch.T3Thread{}, fmt.Errorf("t3 thread show printed no thread: %.80q", b)
	}
	at, err := time.Parse(time.RFC3339Nano, v.Thread.UpdatedAt)
	if err != nil {
		return wakewatch.T3Thread{}, fmt.Errorf("t3 thread show: thread updatedAt %q: %v", v.Thread.UpdatedAt, err)
	}
	th := wakewatch.T3Thread{Status: v.Thread.Status, Active: v.Thread.ActiveRunID != nil && *v.Thread.ActiveRunID != "",
		Pending: v.Thread.Pending, Updated: at, Settled: v.Thread.Settled || v.Thread.Archived}
	for i := len(v.Messages) - 1; i >= 0; i-- {
		if v.Messages[i].Role == "assistant" {
			th.LastReply = v.Messages[i].Text
			break
		}
		th.Unanswered = th.Unanswered || v.Messages[i].Role == "user"
	}
	return th, nil
}

// fileThere says p is there, a link that points nowhere too: running it then fails under its own name.
func fileThere(p string) bool { _, err := os.Lstat(p); return err == nil }
