package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/quota"
	"github.com/orzazade/crius/internal/sessionstate"
)

// proc is one line of ps: a process, its parent and its command.
type proc struct {
	pid, ppid int
	command   string
}

// mover moves a role's session to another Max account as the same session (owner 10-30: a move resumes the same
// session, never a fresh one; crius:lead 11-08: at a turn boundary, never cutting a running background build).
type mover struct {
	home   string
	pick   func(project, want string) (name, dir string, err error) // the account by the rule (quota.Pick)
	state  func(id string) (string, error)                          // crius-hook's state of a session
	procs  func() ([]proc, error)
	cmux   func(args ...string) (string, error)
	resume func(project, role, id, cwd, acct, dir string) (ws, surface string, err error) // launch.Resume
	want   string                                                                         // -account: "max" or a row's name
	tries  int                                                                            // reads of the old Claude while it exits
	wait   time.Duration
}

// shells start a tool's shell command; a Claude child that runs one while no turn runs is a background job.
var shells = map[string]bool{"zsh": true, "bash": true, "sh": true}

// move resumes the role's current session on the account the rule picks and says where it runs. A session whose turn
// runs, or that runs a background job other than its bus waiter (team-wait.sh, re-armed by the resume prompt), is left
// alone: the caller moves it at its next turn boundary. The new Claude must run in the picked folder.
func (m mover) move(project, role string) (string, error) {
	r, err := sessionstate.ReadRole(filepath.Join(m.home, "roles"), project, role)
	if errors.Is(err, fs.ErrNotExist) {
		err = unfiled(m.home, project, role, "move", err)
	}
	if err != nil {
		return "", fmt.Errorf("no session to move for %s %s: %w", project, role, err)
	}
	as, err := quota.LoadAccounts(m.home)
	if err != nil {
		return "", err
	}
	from := "an account no row lists"
	if a := as.Dir(r.Config); a != nil {
		from = a.Name
	} else if login, err := quota.LoginOf(r.Config); err == nil && as.Of(login) != nil {
		from = as.Of(login).Name
	}
	want := m.want
	if want == "" {
		want = "max"
	}
	acct, dir, err := m.pick(project, want)
	if err != nil {
		return "", err
	}
	if acct == "" || dir == "" {
		return "", fmt.Errorf("%s %s: no Max account to move to", project, role)
	}
	if acct == from {
		return "", fmt.Errorf("%s %s: already on %s, the account the rule picks: nothing moved", project, role, acct)
	}
	pid, _, err := sessionstate.Process(r.Config, r.SessionID)
	if err != nil {
		return "", err
	}
	if pid != 0 {
		if err := m.idle(r, pid); err != nil {
			return "", fmt.Errorf("%s %s: %w: nothing moved, try at its next turn boundary", project, role, err)
		}
		if err := m.exit(r); err != nil {
			return "", fmt.Errorf("%s %s: %w", project, role, err)
		}
	}
	ws, surface, err := m.resume(project, role, r.SessionID, r.Cwd, acct, dir)
	if err != nil {
		return "", err
	}
	now, _, err := sessionstate.Process(dir, r.SessionID)
	if err != nil {
		return "", err
	}
	if now == 0 {
		where := "no folder crius can see"
		if p, _, err := sessionstate.Process(r.Config, r.SessionID); err == nil && p != 0 {
			where = r.Config
		}
		return "", fmt.Errorf("%s %s: session %s resumed in %s %s, but it runs in %s, not %s", project, role, r.SessionID, ws, surface, where, dir)
	}
	return fmt.Sprintf("OK %s %s session %s moved %s -> %s: pid %d CLAUDE_CONFIG_DIR %s (%s %s)", project, role, r.SessionID, from, acct, now, dir, ws, surface), nil
}

// idle is nil when the session's turn has ended and no background job but its bus waiter runs under its Claude (pid).
func (m mover) idle(r sessionstate.State, pid int) error {
	st, err := m.state(r.SessionID)
	if err != nil {
		return err
	}
	if st != sessionstate.Idle {
		return fmt.Errorf("a turn is running (state %q)", st)
	}
	ps, err := m.procs()
	if err != nil {
		return fmt.Errorf("cannot list its background jobs: %w", err)
	}
	var jobs []string
	for _, p := range ps {
		w := strings.Fields(p.command)
		if p.ppid != pid || len(w) < 2 || !shells[filepath.Base(w[0])] || w[1] != "-c" || strings.Contains(p.command, "team-wait.sh") {
			continue
		}
		cmd := p.command
		if i := strings.Index(cmd, "eval '"); i >= 0 {
			cmd = cmd[i+len("eval '"):]
		}
		jobs = append(jobs, fmt.Sprintf("%d %.60s", p.pid, cmd))
	}
	switch len(jobs) {
	case 0:
		return nil
	case 1:
		return fmt.Errorf("1 background job runs (%s)", jobs[0])
	}
	return fmt.Errorf("%d background jobs run (%s)", len(jobs), strings.Join(jobs, "; "))
}

// exit types /exit into the session's own tab and waits for its Claude to stop, answering Claude's "Exit and stop tasks"
// question (only its bus waiter is left, which the resume prompt restarts).
func (m mover) exit(r sessionstate.State) error {
	if r.Workspace == "" || r.Surface == "" {
		return fmt.Errorf("session %s has no tab on file to type /exit into", r.SessionID)
	}
	tab := []string{"--workspace", r.Workspace, "--surface", r.Surface}
	if out, err := m.cmux(append(append([]string{"send"}, tab...), "--", "/exit")...); err != nil {
		return fmt.Errorf("cmux send /exit: %v: %s", err, out)
	}
	if out, err := m.cmux(append(append([]string{"send-key"}, tab...), "enter")...); err != nil {
		return fmt.Errorf("cmux send-key: %v: %s", err, out)
	}
	for i := 0; i < m.tries; i++ {
		pid, _, err := sessionstate.Process(r.Config, r.SessionID)
		if err != nil {
			return err
		}
		if pid == 0 {
			return nil
		}
		if screen, err := m.cmux(append(append([]string{"read-screen"}, tab...), "--lines", "30")...); err == nil && strings.Contains(screen, "Exit and stop tasks") {
			if out, err := m.cmux(append(append([]string{"send-key"}, tab...), "enter")...); err != nil {
				return fmt.Errorf("cmux send-key: %v: %s", err, out)
			}
		}
		time.Sleep(m.wait)
	}
	return fmt.Errorf("session %s did not exit after /exit in %s %s: nothing resumed", r.SessionID, r.Workspace, r.Surface)
}

// psProcs lists every process with its parent and command.
func psProcs() ([]proc, error) {
	out, err := exec.Command("/bin/ps", "-axww", "-o", "pid=,ppid=,command=").Output()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	var ps []proc
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.Fields(l)
		if len(f) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("ps: bad line %q", l)
		}
		ps = append(ps, proc{pid: pid, ppid: ppid, command: strings.Join(f[2:], " ")})
	}
	if len(ps) == 0 {
		return nil, fmt.Errorf("ps listed no process")
	}
	return ps, nil
}
