package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/orzazade/crius/internal/launch"
	"github.com/orzazade/crius/internal/sessionstate"
	"github.com/orzazade/crius/internal/wakewatch"
)

// freshener is crius-spawn -fresh: it ends a role's current session and starts a new one in its place (11-20: -close
// only marked axi qa-2's session closed, its claude ran on, and the next spawn made two live sessions for the role).
type freshener struct {
	home    string
	show    func(id string) (wakewatch.T3Thread, error) // reads a T3 thread
	process func(config, id string) (int, error)        // the live claude pid that has session id open, 0 when none
	signal  func(pid int, sig syscall.Signal) error     // syscall.Kill; sig 0 asks whether pid still runs
	spawn   func() (string, error)                      // the normal spawn of the new session; what it prints
	procs   func() ([]wakewatch.Proc, error)            // every process, to see the claude's own jobs T3 does not list
	// wait and tries bound the wait for the turn boundary; stopWait and stopTries the wait for the claude to exit after
	// each signal.
	wait, stopWait   time.Duration
	tries, stopTries int
}

// fresh waits until the role's current session, in a T3 thread, is at a turn boundary (no run open, no open request,
// no background job of its own, listed by T3 or not), marks it closed, stops its claude by its own pid (SIGTERM, then
// SIGKILL), and then spawns the new session. It refuses, and changes nothing, when the thread cannot be read or stays
// busy, when it cannot tell whether the claude runs, or when another -fresh for the role runs. A role whose session
// runs in a cmux tab is refused too: its turn boundary is read from the tab (-move), not here.
func (f freshener) fresh(project, role string) (string, error) {
	dir := filepath.Join(f.home, "roles")
	unlock, err := sessionstate.LockRole(dir, project, role) // held until the new session started (07bd54d review)
	if errors.Is(err, sessionstate.ErrLocked) {
		return "", fmt.Errorf("another crius-spawn -fresh for %s %s runs (%v): nothing was closed or stopped", project, role, err)
	} else if err != nil {
		return "", err
	}
	defer unlock()
	r, err := sessionstate.ReadRole(dir, project, role)
	if err != nil {
		return "", fmt.Errorf("no session to replace for %s %s: %w", project, role, err)
	}
	thread, err := launch.RoleThread(f.home, project, role, r)
	if err != nil {
		return "", err
	}
	if thread == "" {
		return "", fmt.Errorf("%s %s runs in a cmux tab, not a T3 thread: -fresh reads a thread's turn boundary; /exit the tab, then -close and spawn", project, role)
	}
	old := r // the session that runs in the thread: the newest one crius-hook filed for it, else the role's file
	if ss, err := sessionstate.List(filepath.Join(f.home, "sessions")); err == nil || errors.Is(err, sessionstate.ErrDamaged) {
		var best sessionstate.State
		for _, s := range ss {
			if s.Thread == thread && (best.SessionID == "" || s.Time.After(best.Time)) {
				best = s
			}
		}
		if best.SessionID != "" {
			old = best
		}
	} else {
		return "", fmt.Errorf("crius-hook's session files: %w", err)
	}
	if old.SessionID != r.SessionID { // the mark would close neither the role nor only this thread (review P2-2)
		return "", fmt.Errorf("T3 thread %s runs session %s, but the role's file names %s: nothing was closed or stopped; crius-spawn -resume %s %s first, or -close and stop it by hand",
			thread, old.SessionID, r.SessionID, project, role)
	}
	if cur, err := sessionstate.Read(filepath.Join(f.home, "sessions"), old.SessionID); err == nil && cur.Config != "" {
		old.Config = cur.Config
	}
	config := old.Config
	if config == "" {
		config = old.FiledConfig()
	}
	if _, err := f.boundary(thread, config, old.SessionID); err != nil {
		return "", err
	}
	prev, err := sessionstate.ClosedSessions(dir, project, role)
	if err != nil {
		return "", err
	}
	if err := sessionstate.CloseRole(dir, project, role, old.SessionID); err != nil {
		return "", err
	}
	// undo puts the closed mark back as it was, so a failure leaves no live claude marked closed: CloseRole moves an id
	// to the end, so closing prev's ids again in order gives prev back
	undo := func(err error) error {
		uerr := sessionstate.OpenRole(dir, project, role, old.SessionID)
		for _, id := range prev {
			if uerr == nil {
				uerr = sessionstate.CloseRole(dir, project, role, id)
			}
		}
		if uerr != nil {
			return fmt.Errorf("%w; and its closed mark could not be put back: %v", err, uerr)
		}
		return err
	}
	pid, err := f.boundary(thread, config, old.SessionID) // a bus line may have woken it since
	if err != nil {
		return "", undo(err)
	}
	stopped := "no live claude found for it" // a T3 thread may have none between turns
	if pid != 0 {
		if err := f.stop(pid); err != nil {
			return "", undo(fmt.Errorf("session %s: %v, so it stays open and nothing was started", old.SessionID, err))
		}
		stopped = fmt.Sprintf("claude pid %d stopped", pid)
	}
	out, err := f.spawn()
	head := fmt.Sprintf("OK fresh %s %s: old session %s (T3 thread %s) closed, %s", project, role, old.SessionID, thread, stopped)
	if err != nil {
		return "", fmt.Errorf("%s; the new session did not start: %w", head, err)
	}
	return head + "\nnew: " + out, nil
}

// boundary waits until thread has no run open, no open request and no background job of its own, neither one T3
// lists nor a Bash shell under session id's claude that T3 does not list (07bd54d review: such a ⏳ job died with the
// claude). It gives that claude's pid, 0 when none runs; not knowing whether one runs is an error.
func (f freshener) boundary(thread, config, id string) (int, error) {
	var why string
	for i := 0; i < f.tries; i++ {
		if i > 0 {
			time.Sleep(f.wait)
		}
		th, err := f.show(thread)
		if err != nil {
			return 0, fmt.Errorf("cannot read T3 thread %s: %v; nothing was closed or stopped", thread, err)
		}
		var busy []string
		if th.Active || th.Status == "running" {
			busy = append(busy, "a run is open")
		}
		if th.Pending > 0 {
			busy = append(busy, fmt.Sprintf("%d open request(s)", th.Pending))
		}
		if len(th.Background) > 0 {
			busy = append(busy, "background job(s) "+strings.Join(th.Background, ", "))
		}
		if len(busy) > 0 {
			why = strings.Join(busy, "; ")
			continue
		}
		pid, err := f.process(config, id)
		if err != nil {
			return 0, fmt.Errorf("session %s: cannot tell whether its claude runs: %w; nothing was closed or stopped", id, err)
		}
		if pid == 0 {
			return 0, nil
		}
		ps, err := f.procs()
		if err != nil {
			return 0, fmt.Errorf("session %s: cannot list the processes under its claude pid %d: %w; nothing was closed or stopped", id, pid, err)
		}
		job, what, ok := wakewatch.OwnJob(ps, pid)
		if !ok {
			return pid, nil
		}
		why = fmt.Sprintf("its claude pid %d runs a job of its own that T3 does not list: pid %d (%s)", pid, job, what)
	}
	return 0, fmt.Errorf("T3 thread %s is not at a turn boundary after %s: %s; nothing was closed or stopped",
		thread, (time.Duration(f.tries) * f.wait).Round(time.Second), why)
}

// stop ends pid: SIGTERM, then SIGKILL when it is still there after stopTries waits; an error when it outlives both.
func (f freshener) stop(pid int) error {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL} {
		if err := f.signal(pid, sig); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return nil
			}
			return fmt.Errorf("%s to claude pid %d: %v", sig, pid, err)
		}
		for i := 0; i < f.stopTries; i++ {
			time.Sleep(f.stopWait)
			if err := f.signal(pid, 0); errors.Is(err, syscall.ESRCH) {
				return nil
			}
		}
	}
	return fmt.Errorf("claude pid %d still runs after SIGTERM and SIGKILL", pid)
}
