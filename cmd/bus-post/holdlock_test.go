package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/tabsend"
)

// bus-post --hold-lock holds the very lock bus-post's own tmux sends take (typingLock) until stdin ends.
func TestHoldLockHoldsTheLockBusPostTypesUnder(t *testing.T) {
	t.Setenv("CRIUS_HOME", t.TempDir())
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	var errs bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- holdLock([]string{"1s"}, inR, outW, &errs) }()
	buf := make([]byte, 7)
	if _, err := io.ReadFull(outR, buf); err != nil || string(buf) != "LOCKED\n" {
		t.Fatalf("--hold-lock said %q (%v)", buf, err)
	}
	old := tabsend.LockWait
	tabsend.LockWait = 100 * time.Millisecond
	defer func() { tabsend.LockWait = old }()
	if err := sendTmux("demo:builder", "hi"); !errors.Is(err, tabsend.ErrRefused) || !errors.Is(err, tabsend.ErrLockBusy) {
		t.Fatalf("a tmux send while --hold-lock held the lock gave %v, want refused: busy", err)
	}
	inW.Close()
	if rc := <-done; rc != 0 {
		t.Fatalf("--hold-lock exit %d: %s", rc, errs.String())
	}
}

func TestHoldLockRefusesABadWait(t *testing.T) {
	for _, args := range [][]string{nil, {"soon"}, {"0s"}, {"3m"}, {"1s", "x"}} {
		var errs bytes.Buffer
		if rc := holdLock(args, strings.NewReader(""), io.Discard, &errs); rc != 2 || !strings.Contains(errs.String(), "usage: bus-post --hold-lock") {
			t.Errorf("%q: exit %d, said %q; want 2 and usage", args, rc, errs.String())
		}
	}
}
