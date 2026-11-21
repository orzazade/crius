package main

import (
	"errors"
	"fmt"
	"io"
	"os"
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
	show    func(id string) (wakewatch.T3Thread, []t3Job, error) // reads a T3 thread and the background jobs it lists
	process func(config, id string) (int, error)                 // the live claude pid that has session id open, 0 when none
	signal  func(pid int, sig syscall.Signal) error              // syscall.Kill; sig 0 asks whether pid still runs
	spawn   func() (string, error)                               // the normal spawn of the new session; what it prints
	procs   func() ([]wakewatch.Proc, error)                     // every process, to see the claude's own jobs T3 does not list
	log     io.Writer                                            // where the wait for the turn boundary says what it waits on
	now     func() time.Time                                     // the clock the wait's limit is read on
	sleep   func(time.Duration)                                  // the pause between two reads of the thread
	// wait is the pause between two reads of the thread and limit the whole wait for the turn boundary, both reads of
	// it together; stopWait and stopTries bound the wait for the claude to exit after each signal.
	wait, limit, stopWait time.Duration
	stopTries             int
}

// freshLimit bounds -fresh's whole wait for a turn boundary. 11-21: 240 reads 5 s apart (20 min, silent, longer still
// when T3 answered slowly) outlived Crius's own 15 min wait twice, so its refusal was never seen. With the new session's
// own start-up checks (2 x 2 min, internal/launch RunT3), -fresh ends inside about 10 min.
const freshLimit = 5 * time.Minute

// newFreshener is the -fresh crius-spawn runs: it reads T3, Claude Code's status files and ps, signals by pid, and
// spawns the new session with spawn.
func newFreshener(home string, spawn func() (string, error)) freshener {
	return freshener{home: home, wait: 5 * time.Second, limit: freshLimit, stopWait: 250 * time.Millisecond, stopTries: 40,
		log: os.Stderr, now: time.Now, sleep: time.Sleep,
		show: func(id string) (wakewatch.T3Thread, []t3Job, error) {
			th, _, jobs, err := showThread(home, id)
			return th, jobs, err
		},
		process: func(config, id string) (int, error) {
			pid, _, err := sessionstate.Process(config, id)
			return pid, err
		},
		procs: func() ([]wakewatch.Proc, error) {
			ps, err := psProcs()
			out := make([]wakewatch.Proc, len(ps))
			for i, p := range ps {
				out[i] = wakewatch.Proc{PID: p.pid, PPID: p.ppid, Command: p.command}
			}
			return out, err
		},
		signal: func(pid int, sig syscall.Signal) error { return syscall.Kill(pid, sig) },
		spawn:  spawn}
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
	start := f.now()
	until := start.Add(f.limit) // one limit for both reads of the boundary
	if _, err := f.boundary(thread, config, old.SessionID, start, until); err != nil {
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
	pid, err := f.boundary(thread, config, old.SessionID, start, until) // a bus line may have woken it since
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

// t3Job is one background job T3 lists for a thread (pendingBackgroundTasks): its id, its kind ("command", "subagent",
// "monitor", "background_task"), the child thread a delegated subagent runs in, and what it does.
type t3Job struct{ ID, Kind, Child, What string }

// inClaude says the job runs inside the thread's own claude, so it cannot outlive it: a Bash command, or a subagent of
// that claude's own. A delegated child thread runs a claude of its own, and a monitor may be a pull-request watch the
// T3 server runs; t3_thread_interrupt stop=true ends both (review P1).
func (j t3Job) inClaude() bool { return j.Kind == "command" || (j.Kind == "subagent" && j.Child == "") }

// String is the job as one line: "<id> (<what it does>)".
func (j t3Job) String() string {
	s := j.ID
	if j.What != "" {
		s += " (" + j.What + ")"
	}
	return strings.Join(strings.Fields(s), " ")
}

// boundary waits until thread has no run open, no open request and no background job of its own, neither one T3
// lists nor a Bash shell under session id's claude that T3 does not list (07bd54d review: such a ⏳ job died with the
// claude). It gives that claude's pid, 0 when none runs; not knowing whether one runs is an error. It reads the thread
// at least once and waits no longer than until (start is when -fresh began). The wait is never silent: it says on
// f.log what it waits on when that changes and once a minute, and every refusal is one line that names the thread, what
// held it and the safe next step (11-21: axi:ms-flow's -fresh waited 2 x 15 min with no output).
func (f freshener) boundary(thread, config, id string, start, until time.Time) (int, error) {
	stop := fmt.Sprintf("stop the thread (t3_thread_interrupt threadId=%s stop=true), then send the role's prompt with "+
		"freshContext (t3_thread_send threadId=%s freshContext=true)", thread, thread)
	var why string
	var said time.Time
	var outside []string // listed jobs that may run outside the claude, at the last read
	for {
		th, jobs, err := f.show(thread)
		if err != nil {
			return 0, fmt.Errorf("cannot read T3 thread %s: %v; nothing was closed or stopped; run -fresh again once `t3 thread show %s` answers",
				thread, err, thread)
		}
		var busy, listed []string
		if th.Active || th.Status == "running" {
			busy = append(busy, "a run is open")
		}
		if th.Pending > 0 {
			busy = append(busy, fmt.Sprintf("%d open request(s)", th.Pending))
		}
		outside = nil
		for _, j := range jobs {
			listed = append(listed, j.String())
			if !j.inClaude() {
				outside = append(outside, j.String())
			}
		}
		if len(listed) > 0 {
			busy = append(busy, "background job(s) "+strings.Join(listed, ", "))
		}
		hold := strings.Join(busy, "; ")
		if len(busy) == 1 && len(listed) > 0 && len(outside) == 0 {
			// Only jobs that run inside the claude hold the thread, so with no claude for the session nothing runs
			// them: T3 lists jobs a restart killed (11-21), and waiting never empties that list.
			switch pid, err := f.process(config, id); {
			case err != nil: // a status file caught mid-write: read again (review P2)
				hold += fmt.Sprintf(" (cannot tell whether its claude runs: %v)", err)
			case pid == 0:
				return 0, fmt.Errorf("T3 thread %s lists %s, but no claude runs its session %s, so nothing can run them "+
					"(a T3 restart kills a thread's jobs and leaves them listed); nothing was closed or stopped. Next: check "+
					"that no process runs them, then %s", thread, busy[0], id, stop)
			}
		}
		if hold == "" {
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
			hold = fmt.Sprintf("its claude pid %d runs a job of its own that T3 does not list: pid %d (%s)", pid, job, what)
		}
		now := f.now()
		if hold != why || now.Sub(said) >= time.Minute {
			why, said = hold, now
			fmt.Fprintf(f.log, "crius-spawn: waiting on T3 thread %s (%s of %s): %s\n", thread,
				now.Sub(start).Round(time.Second), until.Sub(start), why)
		}
		if !now.Before(until) {
			break
		}
		f.sleep(f.wait)
	}
	next := "if that work is real, let it end and run -fresh again; if it is stuck or gone (a T3 restart leaves dead jobs listed), " + stop
	if len(outside) > 0 {
		next = fmt.Sprintf("let %s end, then run -fresh again: it may run outside the claude (a delegated thread or a "+
			"watch), and t3_thread_interrupt stop=true would end it too", strings.Join(outside, ", "))
	}
	return 0, fmt.Errorf("T3 thread %s is not at a turn boundary after %s: %s; nothing was closed or stopped. Next: %s",
		thread, f.now().Sub(start).Round(time.Second), why, next)
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
