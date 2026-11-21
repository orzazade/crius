package main

// A role whose Claude Code runs in a tmux window (Staffy on the ThinkPad, Quotes on the Legion) has no cmux tab and no
// T3 Code thread, so a bus line alone does not wake it: on 11-21 at 22:08 and 22:52 bus-post said "no tab and no T3
// Code thread filed" and Crius typed the lines in by hand with crius-tsend; the Staffy builder had sat 33 h before with
// nothing to wake it. bus-post runs on the box that holds the window (bus-post-thinkpad and bus-post-legion run it
// there), so a line that asks such a role to act is typed into the local tmux window <project>:<role>, as crius-tsend
// types it (tabsend.Send). The lock (tabsend.LockFile) keeps every typer into this machine's panes apart: bus-posts here,
// and a crius-tsend from the Mac, which holds this machine's lock through `bus-post --hold-lock` (holdLock).

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/store"
	"github.com/orzazade/crius/internal/tabsend"
)

// tmuxCall bounds one tmux call, as crius-tsend bounds one ssh call: a hung server fails the send instead of holding it.
const tmuxCall = 30 * time.Second

// runLocal runs one shell line on this machine: tabsend's Runner, as crius-tsend's runs it on a box over ssh.
func runLocal(line string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), tmuxCall)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", line)
	cmd.WaitDelay = time.Second // a child that keeps the output open must not hold the call past its limit
	return cmd.CombinedOutput()
}

// tmuxWindows gives session:window for every window of this machine's tmux server: none when tmux is not installed or
// no server runs, an error when it could not tell.
func tmuxWindows() ([]string, error) {
	if _, err := exec.LookPath("tmux"); err != nil {
		return nil, nil
	}
	out, err := runLocal("tmux list-windows -a -F '#{session_name}:#{window_name}'")
	if err != nil {
		said := strings.TrimSpace(string(out))
		// No server: "no server running on <socket>", or "error connecting to <socket> (No such file or directory)".
		if strings.Contains(said, "no server running") || strings.Contains(said, "error connecting to") &&
			strings.Contains(said, "No such file or directory") {
			return nil, nil
		}
		return nil, fmt.Errorf("tmux list-windows: %v: %s", err, said)
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n"), nil
}

// tmuxLook is this machine's tmux windows, listed once per post and only when a role may be in one.
type tmuxLook struct {
	done    bool
	windows []string
	err     error
}

// has says role (project:role) is a window of this machine's tmux server.
func (l *tmuxLook) has(role string) (bool, error) {
	if !l.done {
		l.windows, l.err = tmuxWindows()
		l.done = true
	}
	return slices.Contains(l.windows, role), l.err
}

// team gives the windows of this machine's tmux session named project ("staffy", "quotes"), where the team's roles run;
// none when there is no such session. Call it after has.
func (l *tmuxLook) team(project string) []string {
	var out []string
	for _, w := range l.windows {
		if s, win, ok := strings.Cut(w, ":"); ok && s == project {
			out = append(out, win)
		}
	}
	return out
}

// typingLock is this machine's typing lock: the one path both sendTmux and holdLock take, so a bus-post here and a
// crius-tsend from the Mac can never lock two different files.
func typingLock() string { return tabsend.LockFile(store.Home()) }

// sendTmux types text into the local tmux window target and proves it went, holding this machine's typing lock (the one
// a crius-tsend from the Mac holds through holdLock).
func sendTmux(target, text string) error {
	unlock, err := tabsend.Lock(typingLock(), tabsend.LockWait)
	if err != nil {
		return fmt.Errorf("%w: %w", tabsend.ErrRefused, err)
	}
	defer unlock()
	return tabsend.Send(runLocal, target, text)
}

// maxHoldWait bounds the wait a --hold-lock caller may ask for.
const maxHoldWait = 2 * time.Minute

// holdLock is `bus-post --hold-lock <wait>`: it waits up to wait for this machine's typing lock, says LOCKED, and holds
// it until stdin ends (at most tabsend.HoldMax). crius-tsend on the Mac runs it over ssh around its typing into a pane here, so it and a bus-post
// here never type at once. Exit 0 held and let go, 1 not held (busy for the whole wait, or the lock failed), 2 usage.
func holdLock(args []string, in io.Reader, out, errs io.Writer) int {
	var wait time.Duration
	var err error
	if len(args) == 1 {
		wait, err = time.ParseDuration(args[0])
	}
	if len(args) != 1 || err != nil || wait <= 0 || wait > maxHoldWait {
		fmt.Fprintf(errs, "usage: bus-post --hold-lock <wait, 1ms to %s>\n", maxHoldWait)
		return 2
	}
	if err := tabsend.Hold(typingLock(), wait, tabsend.HoldMax, in, out); err != nil {
		fmt.Fprintf(errs, "bus-post: %v\n", err)
		return 1
	}
	return 0
}

// tmuxSay says what happened to a send to role's tmux window on this box, and gives its outcome and whether it failed.
func tmuxSay(role string, err error) (what string, failed bool, line string) {
	box := boxName()
	where := "tmux " + box + " " + role
	switch {
	case err == nil:
		return "delivered (tmux " + box + ")", false, fmt.Sprintf("bus-post: delivered to %s (tmux %s)\n", role, box)
	case errors.Is(err, tabsend.ErrMaybeSent):
		return fmt.Sprintf("MAYBE delivered (%s): %v", where, err), true,
			fmt.Sprintf("bus-post: posted, but MAYBE delivered to %s (%s): %v: look at the window, do not send again\n",
				role, where, err)
	}
	return fmt.Sprintf("NOT delivered (%s): %v", where, err), true,
		fmt.Sprintf("bus-post: posted, but NOT delivered to %s (%s): %v; send it by hand: crius-tsend %s %s <text>\n",
			role, where, err, box, role)
}

// boxName is this machine's short host name (thinkpad, legion), as the playbook's hosts.json names the boxes.
func boxName() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "this machine"
	}
	h, _, _ = strings.Cut(h, ".")
	return strings.ToLower(h)
}

// teamOf gives the project of a project:role name.
func teamOf(role string) string {
	p, _, _ := strings.Cut(role, ":")
	return p
}
