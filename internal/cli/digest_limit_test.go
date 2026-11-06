package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// 11-06 13:55-14:46 (S2): crius-rec digest ran 51 min at ~80% CPU and nobody heard. A run past its time limit says which
// step it was on and exits non-zero, so crius-digest-run posts its ALERT. The hang here is a last report that is a FIFO:
// reading it blocks forever, as the waits query did.
func TestDigestPastItsLimitNamesTheStepAndFails(t *testing.T) {
	home := findStore(t)
	dir := filepath.Join(home, "state", "digest")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "2026-11-06-0900.json")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	limit, exit := digestLimit, digestExit
	t.Cleanup(func() { digestLimit, digestExit = limit, exit })
	digestLimit = 100 * time.Millisecond
	exited := make(chan int, 1)
	digestExit = func(code int) { exited <- code }

	var out, errb bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- digestCmd(home, time.Date(2026, 11, 6, 13, 55, 0, 0, time.Local), false, false, &out, &errb)
	}()
	var code int
	select {
	case code = <-exited:
	case rc := <-done:
		t.Fatalf("the run ended (rc %d) without its limit firing: %s", rc, errb.String())
	case <-time.After(30 * time.Second):
		t.Fatal("no time limit: the run is still blocked after 30 s")
	}
	msg := errb.String()
	if code == 0 || code == exitPartial || !strings.Contains(msg, "time limit") || !strings.Contains(msg, `step "last report"`) {
		t.Fatalf("want a non-zero exit naming step \"last report\", got %d: %s", code, msg)
	}
	// unblock the stuck read so the run's goroutine ends
	if f, err := os.OpenFile(fifo, os.O_WRONLY, 0); err == nil {
		f.Close()
	}
	<-done
}
