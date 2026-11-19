package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/wakewatch"
)

// fakeT3 puts a t3 in $CRIUS_HOME/bin, where thread looks first, that keeps its arguments in dir/calls, sleeps the
// seconds in dir/delay (none when there is no such file), prints dir/out and dir/err, and exits with the code in dir/rc
// (0 when there is none).
func fakeT3(t *testing.T) (dir string) {
	t.Helper()
	dir, home := t.TempDir(), t.TempDir()
	t3Until = time.Time{} // each test is a run of its own
	t.Setenv("CRIUS_HOME", home)
	os.MkdirAll(filepath.Join(home, "bin"), 0o755)
	script := "#!/bin/sh\nd=" + dir + "\necho \"$*\" >> $d/calls\n[ -f $d/delay ] && sleep $(cat $d/delay)\n" +
		"cat $d/out 2>/dev/null\ncat $d/err >&2 2>/dev/null\n[ -f $d/rc ] && exit $(cat $d/rc)\nexit 0\n"
	if err := os.WriteFile(filepath.Join(home, "bin", "t3"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func put(t *testing.T, dir, name, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The shape `t3 thread show --json --messages N` printed for AXI ms-flow on 11-15 (fields cut to the ones read, plus a
// few it carries): the reply comes as several assistant messages, and a user message may follow them.
const showJSON = `{"thread":{"threadId":"mcp:launch:5a5f","title":"AXI · ms-flow","status":"completed","activeRunId":null,
"pendingRequestCount":0,"createdAt":"2026-11-13T08:36:32.545Z","updatedAt":"2026-11-15T01:47:57.723Z"},
"messages":[{"role":"assistant","runId":"r196","text":"My G150 green job is still queued."},
{"role":"assistant","runId":"r196","text":"open the PR into envhub and post NEED-QA. ✅"},
{"role":"user","runId":"r197","text":"[queue q3] owner: go"}]}`

func TestThreadReadsStatusUpdateAndLastReply(t *testing.T) {
	dir := fakeT3(t)
	put(t, dir, "out", showJSON)
	th, err := thread("mcp:launch:5a5f")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 11, 15, 1, 47, 57, 723e6, time.UTC)
	if th.Status != "completed" || th.Active || th.Pending != 0 || !th.Updated.Equal(want) ||
		th.LastReply != "open the PR into envhub and post NEED-QA. ✅" || !th.Unanswered {
		t.Fatalf("got %+v", th)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "calls"))
	if string(b) != "thread show --json --messages 10 mcp:launch:5a5f\n" {
		t.Fatalf("t3 calls %q", b)
	}
	put(t, dir, "out", strings.Replace(showJSON, `"activeRunId":null`, `"activeRunId":"run:7"`, 1))
	if th, err := thread("x"); err != nil || !th.Active {
		t.Fatalf("an open run: %+v %v", th, err)
	}
	put(t, dir, "out", strings.Replace(strings.Replace(showJSON, `"pendingRequestCount":0`, `"pendingRequestCount":2`, 1),
		`,
{"role":"user","runId":"r197","text":"[queue q3] owner: go"}`, "", 1))
	if th, err := thread("x"); err != nil || th.Pending != 2 || th.Unanswered {
		t.Fatalf("two open requests, the reply last: %+v %v", th, err)
	}
}

// "Failed to look" is not "found nothing": a t3 that fails, or prints what is no thread, is an error that names why.
func TestThreadFailuresAreErrors(t *testing.T) {
	dir := fakeT3(t)
	put(t, dir, "err", "Thread nosuch is no longer available.\n    at stack\n")
	put(t, dir, "rc", "1")
	if _, err := thread("nosuch"); err == nil || !strings.Contains(err.Error(), "exit status 1: Thread nosuch is no longer available.") {
		t.Fatalf("a failed call: %v", err)
	}
	os.Remove(filepath.Join(dir, "rc"))
	os.Remove(filepath.Join(dir, "err"))
	for out, want := range map[string]string{
		"Server not running": "printed no thread JSON",
		`{"messages":[]}`:    "printed no thread",
		`{"thread":{"status":"completed","updatedAt":""}}`: "thread updatedAt",
	} {
		put(t, dir, "out", out)
		if _, err := thread("x"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want %q", out, err, want)
		}
	}
}

// 11-17 15:30: the T3 idle check woke Crius for the old Cortex lead thread, settled in T3 at 22:51 the night before. A
// thread settled or archived in T3 is read as Settled; one that is neither is not.
func TestThreadReadsSettledOrArchived(t *testing.T) {
	dir := fakeT3(t)
	for field, want := range map[string]bool{"": false, `"settled":true,`: true, `"archived":true,`: true,
		`"settled":false,"archived":false,`: false} {
		put(t, dir, "out", strings.Replace(showJSON, `"activeRunId":null,`, `"activeRunId":null,`+field, 1))
		th, err := thread("x")
		if err != nil || th.Settled != want {
			t.Errorf("%q: want Settled %v, got %+v %v", field, want, th, err)
		}
	}
}

// scaleT3 divides the t3 call limit by n for one test, so a fake t3 can be "slower than 10 s" in a fraction of that.
func scaleT3(t *testing.T, n time.Duration) {
	t.Helper()
	was := t3Timeout
	t3Timeout /= n
	t.Cleanup(func() { t3Timeout = was })
}

// Under heavy load (load ~170 on 14 cores) t3 answered after more than the old 10 s limit while every thread was idle
// on purpose, and each run said a failed look. A t3 that answers late is an answer: the thread is read. Times are
// scaled by 4: the fake answers after 3 s, which is 12 s, so the 7.5 s limit leaves 4.5 s for process start under load.
func TestThreadSlowAnswerIsRead(t *testing.T) {
	scaleT3(t, 4)
	dir := fakeT3(t)
	put(t, dir, "out", showJSON)
	put(t, dir, "delay", "3")
	th, err := thread("mcp:launch:5a5f")
	if err != nil {
		t.Fatalf("a t3 that answers after 12 s (scaled) is not a failed look: %v", err)
	}
	if th.Status != "completed" || th.LastReply == "" {
		t.Fatalf("got %+v", th)
	}
	t.Logf("read after 12 s (scaled): status %s, last reply read", th.Status)
}

// A t3 that never answers in the whole per-call limit is still a hang: ErrT3NoAnswer, which stops the check's other
// reads for the run.
func TestThreadNoAnswerIsAHang(t *testing.T) {
	was := t3Timeout
	t3Timeout = 300 * time.Millisecond
	t.Cleanup(func() { t3Timeout = was })
	dir := fakeT3(t)
	put(t, dir, "out", showJSON)
	put(t, dir, "delay", "5")
	_, err := thread("x")
	if !errors.Is(err, wakewatch.ErrT3NoAnswer) || !strings.Contains(err.Error(), "(300ms)") {
		t.Fatalf("a t3 that never answers: %v", err)
	}
	t.Logf("the failed-look text: %v", err)
}

// All t3 calls of one run share t3Budget, so slow answers for many threads cannot hold the run for long: once it is
// spent, a read is not tried, and the error says the slow answers used the time, never that t3 hangs.
func TestThreadRunBudgetSpentIsNoHang(t *testing.T) {
	dir := fakeT3(t)
	put(t, dir, "out", showJSON)
	if _, err := thread("first"); err != nil {
		t.Fatal(err)
	}
	t3Until = time.Now().Add(-time.Second)
	_, err := thread("second")
	if !errors.Is(err, wakewatch.ErrT3BudgetSpent) || errors.Is(err, wakewatch.ErrT3NoAnswer) ||
		strings.Contains(err.Error(), "no answer") {
		t.Fatalf("a read after the run's t3 time is spent: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "calls")); strings.Contains(string(b), "second") {
		t.Fatalf("t3 was asked after the budget was spent: %q", b)
	}
	t.Logf("the not-read text: %v", err)
}

// A call made with less than the per-call limit left of the run's budget gets only what is left; if t3 says nothing in
// that time, that is the spent budget, not a hang (t3 never had its full limit).
func TestThreadShortOfBudgetGetsWhatIsLeft(t *testing.T) {
	dir := fakeT3(t)
	put(t, dir, "out", showJSON)
	if _, err := thread("first"); err != nil {
		t.Fatal(err)
	}
	t3Until = time.Now().Add(300 * time.Millisecond)
	put(t, dir, "delay", "5")
	_, err := thread("second")
	m := regexp.MustCompile(`it had ([0-9.]+)ms left`).FindStringSubmatch(fmt.Sprint(err))
	if !errors.Is(err, wakewatch.ErrT3BudgetSpent) || errors.Is(err, wakewatch.ErrT3NoAnswer) || m == nil {
		t.Fatalf("a call with 300ms of the run's t3 time left: %v", err)
	}
	if left, _ := strconv.ParseFloat(m[1], 64); left > 300 {
		t.Fatalf("the call had %sms, more than was left: %v", m[1], err)
	}
}

// The shape `t3 thread show --json` printed on 11-20 for crius-runlog, a child Crius started with delegate_task (fields cut
// to the ones read, plus the lineage ones T3 adds): relationshipToParent "subagent" marks it. A top-level thread has null.
const childJSON = `{"thread":{"threadId":"thread:delegated-task:command%3Amcp%3Ac9bc86ee%3Adelegate-task%3Ae2cdb650",
"title":"night: crius-runlog","createdBy":"agent","creationSource":"mcp","status":"completed","activeRunId":null,
"parentThreadId":"mcp:337dfff5","relationshipToParent":"subagent","runCount":1,"pendingRequestCount":0,"archived":false,
"settled":false,"updatedAt":"2026-11-19T20:01:36.902Z"},
"messages":[{"role":"assistant","runId":"r1","text":"commit attribution line was dropped because your no-AI-credit hook blocks it. ✅"}]}`

func TestThreadReadsADelegateChild(t *testing.T) {
	dir := fakeT3(t)
	put(t, dir, "out", childJSON)
	if th, err := thread("x"); err != nil || !th.Child || th.Status != "completed" {
		t.Fatalf("a delegate child: %+v %v", th, err)
	}
	for _, rel := range []string{`null`, `"fork"`} {
		put(t, dir, "out", strings.Replace(childJSON, `"subagent"`, rel, 1))
		if th, err := thread("x"); err != nil || th.Child {
			t.Fatalf("relationshipToParent %s read as a child: %+v %v", rel, th, err)
		}
	}
}
