package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeT3 puts a t3 in $CRIUS_HOME/bin, where thread looks first, that keeps its arguments in dir/calls, prints dir/out
// and dir/err, and exits with the code in dir/rc (0 when there is none).
func fakeT3(t *testing.T) (dir string) {
	t.Helper()
	dir, home := t.TempDir(), t.TempDir()
	t.Setenv("CRIUS_HOME", home)
	os.MkdirAll(filepath.Join(home, "bin"), 0o755)
	script := "#!/bin/sh\nd=" + dir + "\necho \"$*\" >> $d/calls\ncat $d/out 2>/dev/null\ncat $d/err >&2 2>/dev/null\n" +
		"[ -f $d/rc ] && exit $(cat $d/rc)\nexit 0\n"
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
