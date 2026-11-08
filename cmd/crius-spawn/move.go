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

// turnCap is how long a move waits for a turn that started after /exit was typed.
const turnCap = 45 * time.Minute

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
		waiters, err := m.idle(r, pid)
		if err != nil {
			return "", fmt.Errorf("%s %s: %w: nothing moved, try at its next turn boundary", project, role, err)
		}
		if err := m.exit(r, waiters); err != nil {
			return "", fmt.Errorf("%s %s: %w", project, role, err)
		}
	}
	// File the picked folder before the resume, so a failed resume is retried there by crius-spawn -resume, not on the
	// account the session left.
	if err := m.file(project, role, r.SessionID, "", dir); err != nil {
		return "", fmt.Errorf("%s %s: session %s has exited from %s, but %w; resume it with crius-spawn -resume %s %s", project, role, r.SessionID, r.Config, err, project, role)
	}
	var ws, surface string
	for i := 0; ; i++ {
		// The old Claude can drop its sessions file before its process ends, and Resume leaves a tab with Claude in front
		// alone (review of fe19215 P2-4).
		ws, surface, err = m.resume(project, role, r.SessionID, r.Cwd, acct, dir)
		if err == nil || !strings.Contains(err.Error(), "not a shell") || i+1 >= m.tries {
			break
		}
		time.Sleep(m.wait)
	}
	if err != nil {
		return "", fmt.Errorf("%s %s: session %s has exited from %s and is filed on %s, but did not resume: %w; resume it with crius-spawn -resume %s %s", project, role, r.SessionID, r.Config, dir, err, project, role)
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
	if err := m.file(project, role, r.SessionID, dir, dir); err != nil {
		return "", fmt.Errorf("%s %s: session %s runs in %s, but %w", project, role, r.SessionID, dir, err)
	}
	return fmt.Sprintf("OK %s %s session %s moved %s -> %s: pid %d CLAUDE_CONFIG_DIR %s (%s %s)", project, role, r.SessionID, from, acct, now, dir, ws, surface), nil
}

// file writes the folder the role's session is filed on (and, when config is set, the one it runs in), so a later -resume
// and wakewatch's folder checks keep the session where it was moved (crius:lead 11-08: quotes frontend, moved to max2,
// still filed on .claude-work-max, was flagged on another account). The role is read again first: the resumed Claude's
// hook may have written it since.
func (m mover) file(project, role, id, config, filed string) error {
	roles := filepath.Join(m.home, "roles")
	cur, err := sessionstate.ReadRole(roles, project, role)
	if err != nil {
		return fmt.Errorf("its role file could not be read to file it on %s: %w", filed, err)
	}
	if cur.SessionID != id {
		return fmt.Errorf("the role now names session %s: not filed on %s", cur.SessionID, filed)
	}
	if config != "" {
		cur.Config = config
	}
	cur.Filed = filed
	if err := sessionstate.WriteRole(roles, cur, true); err != nil {
		return fmt.Errorf("filing it on %s failed: %w", filed, err)
	}
	return nil
}

// idle is nil when the session's turn has ended and no background job but its bus waiter runs under its Claude (pid);
// it counts the waiters.
func (m mover) idle(r sessionstate.State, pid int) (int, error) {
	st, err := m.state(r.SessionID)
	if err != nil {
		return 0, err
	}
	if st != sessionstate.Idle {
		return 0, fmt.Errorf("a turn is running (state %q)", st)
	}
	ps, err := m.procs()
	if err != nil {
		return 0, fmt.Errorf("cannot list its background jobs: %w", err)
	}
	var jobs []string
	waiters := 0
	for _, p := range ps {
		w := strings.Fields(p.command)
		if p.ppid != pid || len(w) < 2 || !shells[filepath.Base(w[0])] || w[1] != "-c" {
			continue
		}
		if strings.Contains(p.command, "team-wait.sh") {
			waiters++
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
		return waiters, nil
	case 1:
		return 0, fmt.Errorf("1 background job runs (%s)", jobs[0])
	}
	return 0, fmt.Errorf("%d background jobs run (%s)", len(jobs), strings.Join(jobs, "; "))
}

// stopList gives the tasks Claude's exit dialog says will stop ("Background work is running / The following will stop
// when you exit:", then one line per task, then "Exit and stop tasks"); ok is false when the screen shows no such
// dialog or a line that counts more tasks than it lists.
func stopList(screen string) (tasks []string, ok bool) {
	head := strings.Index(screen, "will stop when you exit:")
	if head < 0 {
		return nil, false
	}
	rest := screen[head+len("will stop when you exit:"):]
	tail := strings.Index(rest, "Exit and stop tasks")
	if tail < 0 {
		return nil, false
	}
	tail = strings.LastIndex(rest[:tail], "\n") + 1 // the option's own line, its number included
	for _, l := range strings.Split(rest[:tail], "\n") {
		l = strings.Trim(l, " \t│╭╮╰╯─❯>")
		if l == "" {
			continue
		}
		if strings.Contains(l, " more") || strings.Contains(l, " item") {
			return nil, false
		}
		tasks = append(tasks, l)
	}
	return tasks, true
}

// exit types /exit into the session's own tab and waits for its Claude to stop. Claude's "Exit and stop tasks" dialog is
// accepted only when it lists as many tasks as the session has bus waiters, which the resume prompt restarts; a
// background subagent or workflow runs inside Claude with no shell, so idle cannot see it, but the dialog lists it
// (review of fe19215 P2-3). Then the dialog is left with Escape (no turn runs) and nothing moves.
func (m mover) exit(r sessionstate.State, waiters int) error {
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
	turn := time.Now().Add(turnCap)
	for i := 0; i < m.tries; {
		pid, _, err := sessionstate.Process(r.Config, r.SessionID)
		if err != nil {
			return err
		}
		if pid == 0 {
			return nil
		}
		// A turn that started between the idle check and Enter (a bus line woke it) holds /exit queued until it ends:
		// wait for it rather than give up and leave the queued /exit to end the session with nothing resumed (review of
		// fe19215 P2-2).
		if st, err := m.state(r.SessionID); err == nil && st == sessionstate.Running && time.Now().Before(turn) {
			time.Sleep(m.wait)
			continue
		}
		i++
		if screen, err := m.cmux(append(append([]string{"read-screen"}, tab...), "--lines", "40")...); err == nil && strings.Contains(screen, "Exit and stop tasks") {
			key := "enter"
			tasks, ok := stopList(screen)
			if !ok || len(tasks) != waiters {
				key = "escape"
			}
			if out, err := m.cmux(append(append([]string{"send-key"}, tab...), key)...); err != nil {
				return fmt.Errorf("cmux send-key: %v: %s", err, out)
			}
			if key == "escape" {
				return fmt.Errorf("its exit would stop background work besides its %d bus waiter(s) (%q): left running, nothing moved, try when it ends", waiters, tasks)
			}
		}
		time.Sleep(m.wait)
	}
	return fmt.Errorf("session %s did not exit after /exit in %s %s: nothing resumed; if a turn runs, /exit may be queued behind it and end the session later, then resume it with crius-spawn -resume %s %s", r.SessionID, r.Workspace, r.Surface, r.Project, r.Role)
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
