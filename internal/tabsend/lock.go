package tabsend

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// LockFile is the lock every sender holds on the machine whose pane it types into (bus-post there, crius-tsend from the
// Mac through Hold): two sends at once mix their keys in one pane, or press Enter on each other's text.
func LockFile(home string) string { return filepath.Join(home, "state", "tsend.lock") }

// LockWait is how long a sender waits for the lock before it gives up with nothing typed. One send takes seconds, so a
// lock held longer is held by a sender that hung. It stays well under wakewatch's 30 s limit on a whole crius-tsend run
// (crius-wakewatch providerCall), so a run that waited still has time to type.
var LockWait = 10 * time.Second

// HoldMax bounds how long Hold keeps a lock for a sender on another machine: a Mac that went to sleep mid-send sends
// no end, and the box's sshd may not notice for hours. Every step of a send is bounded, so a send never needs this long.
const HoldMax = 3 * time.Minute

// ErrLockBusy: another sender held the lock for the whole wait; nothing was typed.
var ErrLockBusy = errors.New("another sender held the typing lock")

// lockPoll is how often a waiting sender tries the lock again.
const lockPoll = 50 * time.Millisecond

// Lock waits up to wait for the file lock at path and holds it; the func it gives lets it go. A lock still held after
// wait is an error that wraps ErrLockBusy.
func Lock(path string, wait time.Duration) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() { f.Close() }, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EINTR) {
			f.Close()
			return nil, fmt.Errorf("lock %s: %w", path, err)
		}
		if !time.Now().Before(deadline) {
			f.Close()
			return nil, fmt.Errorf("%w %s for %s", ErrLockBusy, path, wait)
		}
		time.Sleep(lockPoll)
	}
}

// heldLine is what Hold says once it holds the lock.
const heldLine = "LOCKED"

// Hold takes the lock at path (waiting up to wait), says LOCKED on out, and keeps the lock until in ends, but no longer
// than most: then it lets it go with an error. A sender on another machine holds this machine's lock through it, over one ssh
// call whose stdin it closes when it is done (crius-tsend runs `bus-post --hold-lock` on the box): so bus-post on the
// box and crius-tsend from the Mac take one lock.
func Hold(path string, wait, most time.Duration, in io.Reader, out io.Writer) error {
	unlock, err := Lock(path, wait)
	if err != nil {
		return err
	}
	defer unlock()
	if _, err := fmt.Fprintln(out, heldLine); err != nil {
		return err
	}
	ended := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, in)
		ended <- err
	}()
	select {
	case err := <-ended:
		return err
	case <-time.After(most):
		return fmt.Errorf("let the lock %s go after holding it %s with no end from its sender", path, most)
	}
}

// holdEnd bounds how long ending a hold waits for its command to exit before it kills it.
const holdEnd = 10 * time.Second

// HoldOver starts cmd, a Hold on the machine that holds the pane (through ssh), and returns once it says LOCKED; the
// func it gives ends the hold (closes its stdin, waits for it to exit). A cmd that exits or says anything else first is
// an error with what it said, wrapping ErrLockBusy when the far side said its lock was busy; one that says nothing
// within limit is killed and is an error.
func HoldOver(cmd *exec.Cmd, limit time.Duration) (func(), error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	var said bytes.Buffer
	cmd.Stdout, cmd.Stderr = w, &said
	cmd.WaitDelay = time.Second // a child that keeps stderr open must not hold the end of the hold
	err = cmd.Start()
	w.Close()
	if err != nil {
		r.Close()
		return nil, err
	}
	first := make(chan string, 1)
	go func() {
		b := bufio.NewReader(r)
		line, _ := b.ReadString('\n')
		first <- line
		io.Copy(io.Discard, b) // keep reading until the holder exits, so it never writes into a closed pipe
		r.Close()
	}()
	exited := make(chan error, 1)
	end := func() error {
		go func() { exited <- cmd.Wait() }()
		select {
		case err := <-exited:
			return err
		case <-time.After(holdEnd):
			cmd.Process.Kill()
			return <-exited
		}
	}
	select {
	case line := <-first:
		if strings.TrimSpace(line) == heldLine {
			return func() { stdin.Close(); end() }, nil
		}
		stdin.Close()
		werr := end()
		text := strings.TrimSpace(strings.TrimSpace(line) + " " + strings.TrimSpace(said.String()))
		if strings.Contains(text, ErrLockBusy.Error()) {
			return nil, fmt.Errorf("%w: %s", ErrLockBusy, text)
		}
		return nil, fmt.Errorf("the lock holder did not hold the lock (%v): %s", werr, text)
	case <-time.After(limit):
		cmd.Process.Kill()
		end()
		return nil, fmt.Errorf("the lock holder said nothing in %s", limit)
	}
}
