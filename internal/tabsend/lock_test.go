package tabsend

import (
	"errors"
	"io"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// A lock held for the whole wait gives up with ErrLockBusy, and is free again once let go.
func TestLockGivesUpAfterItsWait(t *testing.T) {
	path := LockFile(t.TempDir())
	unlock, err := Lock(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(path, 100*time.Millisecond); !errors.Is(err, ErrLockBusy) {
		t.Fatalf("a held lock gave %v, want ErrLockBusy", err)
	}
	unlock()
	again, err := Lock(path, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("a let-go lock gave %v", err)
	}
	again()
}

// Two typers that each hold the lock while they type one key at a time never mix their keys.
func TestTwoTypersUnderTheLockDoNotMixTheirKeys(t *testing.T) {
	path := LockFile(t.TempDir())
	var mu sync.Mutex
	var pane strings.Builder
	var wg sync.WaitGroup
	for _, text := range []string{"aaaaaaaa", "bbbbbbbb"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock, err := Lock(path, 5*time.Second)
			if err != nil {
				t.Error(err)
				return
			}
			defer unlock()
			for _, c := range text {
				mu.Lock()
				pane.WriteRune(c)
				mu.Unlock()
				time.Sleep(5 * time.Millisecond)
			}
			mu.Lock()
			pane.WriteString("\n")
			mu.Unlock()
		}()
	}
	wg.Wait()
	for _, line := range strings.Split(strings.TrimSpace(pane.String()), "\n") {
		if line != "aaaaaaaa" && line != "bbbbbbbb" {
			t.Fatalf("the pane got mixed keys: %q", pane.String())
		}
	}
}

// Hold holds the lock until its input ends; HoldOver reads its LOCKED and its end lets the lock go.
func TestHoldKeepsTheLockUntilItsInputEnds(t *testing.T) {
	path := LockFile(t.TempDir())
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- Hold(path, time.Second, HoldMax, inR, outW) }()
	buf := make([]byte, len(heldLine)+1)
	if _, err := io.ReadFull(outR, buf); err != nil || string(buf) != heldLine+"\n" {
		t.Fatalf("Hold said %q (%v), want %q", buf, err, heldLine)
	}
	if _, err := Lock(path, 100*time.Millisecond); !errors.Is(err, ErrLockBusy) {
		t.Fatalf("the lock was free while Hold held it: %v", err)
	}
	inW.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	unlock, err := Lock(path, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("the lock stayed held after Hold's input ended: %v", err)
	}
	unlock()
}

func TestHoldOver(t *testing.T) {
	end, err := HoldOver(exec.Command("/bin/sh", "-c", "echo LOCKED; cat >/dev/null; echo bye >&2"), time.Second)
	if err != nil {
		t.Fatalf("a holder that says LOCKED gave %v", err)
	}
	end()
	for _, c := range []struct {
		name, script, says string
		busy               bool
	}{
		{"busy", "echo 'bus-post: another sender held the typing lock x for 20s' >&2; exit 1", "another sender held", true},
		{"old bus-post", "echo 'usage: bus-post …' >&2; exit 2", "did not hold the lock (exit status 2): usage: bus-post", false},
		{"silent", "sleep 5", "said nothing in 300ms", false},
	} {
		_, err := HoldOver(exec.Command("/bin/sh", "-c", c.script), 300*time.Millisecond)
		if err == nil || !strings.Contains(err.Error(), c.says) || errors.Is(err, ErrLockBusy) != c.busy {
			t.Errorf("%s: gave %v; want one saying %q, busy %v", c.name, err, c.says, c.busy)
		}
	}
}

// A sender that never ends its hold (a Mac gone to sleep mid-send) loses the lock after the hold's limit.
func TestHoldLetsGoAfterItsLimit(t *testing.T) {
	path := LockFile(t.TempDir())
	inR, _ := io.Pipe() // never closed
	err := Hold(path, time.Second, 200*time.Millisecond, inR, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "with no end from its sender") {
		t.Fatalf("a hold with no end gave %v", err)
	}
	unlock, err := Lock(path, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("the lock stayed held after the hold's limit: %v", err)
	}
	unlock()
}
