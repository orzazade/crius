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

// t3Timeout bounds one t3 call. A call takes about 1 s, 2-4 s under heavy load, and at a load of ~170 on 14 cores some
// took over the old 10 s while every thread was idle on purpose, so each run said a failed look. A t3 that answers late
// is alive and its answer is read; one that says nothing in t3Timeout hangs (ErrT3NoAnswer), and the check that made the
// call reads no more threads on this run (each check stops only its own reads).
var t3Timeout = 30 * time.Second

// t3Budget bounds all t3 calls of one run (the process is one run), so slow answers for many threads cannot hold it
// without end: a call gets what is left of it when that is less than t3Timeout, and once it is spent a read is not
// tried. Either way the read fails with ErrT3BudgetSpent, which says slow answers used the time, not that t3 hangs.
// 240 s: launchd starts a run every 300 s and never while one still runs (a long run makes it skip a start), and at
// 12-25 s a call, as under the heavy load this is for, it reads 10-20 threads.
var t3Budget = 4 * time.Minute

// t3Until is when this run's t3Budget ends, set by its first call.
var t3Until time.Time

// t3Messages is how many of a thread's latest messages are read to find its last reply: a reply comes as several
// assistant messages, and a message sent to the thread may follow it.
const t3Messages = "10"

// thread reads a T3 Code thread with `t3 thread show --json`: the t3 in the Crius bin folder when there is one (nvm keeps
// t3 off launchd's PATH), else t3 on the PATH.
func thread(id string) (wakewatch.T3Thread, error) {
	now := time.Now()
	if t3Until.IsZero() {
		t3Until = now.Add(t3Budget)
	}
	limit := min(t3Timeout, t3Until.Sub(now))
	if limit <= 0 {
		return wakewatch.T3Thread{}, fmt.Errorf("t3 thread show: %w (all %s of it)", wakewatch.ErrT3BudgetSpent, t3Budget)
	}
	out, errOut, err := t3cli.Run(t3cli.Bin(store.Home(), "t3"), limit, "", "thread", "show", "--json", "--messages", t3Messages, id)
	if err != nil {
		switch {
		case errors.Is(err, t3cli.ErrNoAnswer) && limit < t3Timeout: // t3 never had its full limit: no hang
			err = fmt.Errorf("%w (it had %s left)", wakewatch.ErrT3BudgetSpent, limit.Round(time.Millisecond))
		case errors.Is(err, t3cli.ErrNoAnswer):
			err = fmt.Errorf("%w (%s)", wakewatch.ErrT3NoAnswer, limit.Round(time.Millisecond))
		}
		// %w: the checks find ErrT3NoAnswer and ErrT3BudgetSpent with errors.Is to stop reading threads for the run
		return wakewatch.T3Thread{}, fmt.Errorf("t3 thread show: %w: %s", err, t3cli.FirstLine(errOut, out)) // a stack trace after it says no more
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
		Pending: v.Thread.Pending, Updated: at, Settled: v.Thread.Settled || v.Thread.Archived,
		Child: v.Thread.RelationshipToParent == "subagent"}
	if v.Thread.LatestRunID != nil {
		th.Run = *v.Thread.LatestRunID
	}
	for _, bg := range v.Thread.Background {
		job := bg.TaskID
		if bg.Description != "" {
			job += " (" + bg.Description + ")"
		}
		th.Background = append(th.Background, job)
		th.Tasks = append(th.Tasks, wakewatch.T3Task{ID: bg.TaskID, Kind: bg.Kind, Description: bg.Description, Child: bg.ChildThreadID})
	}
	for i := len(v.Messages) - 1; i >= 0; i-- {
		if v.Messages[i].Role == "assistant" {
			th.LastReply = v.Messages[i].Text
			break
		}
		th.Unanswered = th.Unanswered || v.Messages[i].Role == "user"
	}
	return th, nil
}
