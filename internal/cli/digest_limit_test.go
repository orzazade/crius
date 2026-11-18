package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// 11-06 13:55-14:46 (S2): crius-rec digest ran 51 min at ~80% CPU and nobody heard. A run past its time limit says which
// step it was on and exits non-zero, so crius-digest-run posts its ALERT. The hang here is a read of the last report
// that blocks until the test releases it, as the waits query did.
//
// 11-19: this test used a FIFO as the last report and ended the read by opening and closing a writer. On macOS a
// signal that reaches the blocked reader near that close can lose the end-of-file: the reader then waits in read(2)
// with no writer left (lsof shows only the reader), and the test hung until go test's 10 min limit. A probe of that
// unblock hung 5 of 400 times with signals arriving. The read now blocks on a channel, and every wait here is bounded.
func TestDigestPastItsLimitNamesTheStepAndFails(t *testing.T) {
	home := findStore(t)
	dir := filepath.Join(home, "state", "digest")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "2026-11-06-0900.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	limit, exit, read := digestLimit, digestExit, readReport
	t.Cleanup(func() { digestLimit, digestExit, readReport = limit, exit, read })
	// the steps before "last report" take milliseconds; 5 s leaves room on a loaded Mac (review of b8754c7 P3)
	digestLimit = 5 * time.Second
	exited := make(chan int, 1)
	digestExit = func(code int) { exited <- code }
	release := make(chan struct{})
	var once sync.Once
	readReport = func(string) ([]byte, error) {
		<-release
		return nil, errors.New("released by the test")
	}
	t.Cleanup(func() { once.Do(func() { close(release) }) }) // a failed test still ends the run's goroutine

	var out bytes.Buffer
	errb := &lockedBuffer{}
	done := make(chan int, 1)
	go func() {
		done <- digestCmd(home, time.Date(2026, 11, 6, 13, 55, 0, 0, time.Local), false, false, &out, errb)
	}()
	var code int
	select {
	case code = <-exited:
	case rc := <-done:
		t.Fatalf("the run ended (rc %d) without its limit firing: %s", rc, errb.String())
	case <-time.After(60 * time.Second):
		t.Fatal("no time limit: the run is still blocked after 60 s")
	}
	msg := errb.String()
	if code == 0 || code == exitPartial || !strings.Contains(msg, "time limit") || !strings.Contains(msg, `step "last report"`) {
		t.Fatalf("want a non-zero exit naming step \"last report\", got %d: %s", code, msg)
	}
	// unblock the stuck read so the run's goroutine ends
	once.Do(func() { close(release) })
	select {
	case rc := <-done:
		if rc != exitError {
			t.Fatalf("after its read was released the run returned %d, want %d (the read's error): %s", rc, exitError, errb.String())
		}
	case <-time.After(60 * time.Second):
		t.Fatal("the run is still blocked 60 s after its last-report read was released")
	}
}

// lockedBuffer is stderr for a run whose time limit and main path both write to it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
