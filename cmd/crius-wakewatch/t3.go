package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/orzazade/crius/internal/store"
	"github.com/orzazade/crius/internal/t3cli"
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
	out, errOut, err := t3cli.Run(t3cli.Bin(store.Home(), "t3"), t3Timeout, "", "thread", "show", "--json", "--messages", t3Messages, id)
	if err != nil {
		if errors.Is(err, t3cli.ErrNoAnswer) {
			err = fmt.Errorf("%w (%s)", wakewatch.ErrT3NoAnswer, t3Timeout)
		}
		return wakewatch.T3Thread{}, fmt.Errorf("t3 thread show: %v: %s", err, t3cli.FirstLine(errOut, out)) // a stack trace after it says no more
	}
	return parseThread(out)
}

// parseThread reads what `t3 thread show --json` prints. JSON that has no thread or no update time is an error: the
// check would read it as a thread idle since 0001.
func parseThread(b []byte) (wakewatch.T3Thread, error) {
	var v t3cli.Show
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
