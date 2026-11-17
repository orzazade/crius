// Command crius-tsend types a message into a Claude Code session in a tmux pane on another machine, then Enter, and
// proves it went (a message with line breaks goes in as one bracketed paste; /clear is proved by the cleared screen):
//
//	crius-tsend <box> <session:window> <text…>
//
// <box> is a host name in ~/.crius/state/playbook/hosts.json (legion, thinkpad), whose ssh command it uses. It clears
// the box, types the text literally, presses Enter in an ssh call of its own (in one call with the text Claude takes
// Enter as a new line in a paste: 11-15 04:06), and reads the pane until the text has left the box, pressing Enter once
// more if it has not. Exit 0 SENT; 1 NOT SENT (the box may hold the text); 2 refused before typing; 3 MAYBE SENT
// (Enter pressed, the pane could not be read after: look at the tab, do not send again).
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/orzazade/crius/internal/playbook"
	"github.com/orzazade/crius/internal/store"
	"github.com/orzazade/crius/internal/tabsend"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, sshRunner))
}

// callTimeout bounds one ssh call, so a hung box fails the send instead of holding it.
const callTimeout = 30 * time.Second

func sshRunner(h playbook.Host) tabsend.Runner {
	return func(line string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()
		args := append(append([]string{}, h.SSH[1:]...), h.Target, line)
		return exec.CommandContext(ctx, h.SSH[0], args...).CombinedOutput()
	}
}

func run(args []string, out, errs io.Writer, runner func(playbook.Host) tabsend.Runner) int {
	if len(args) < 3 {
		fmt.Fprintln(errs, "usage: crius-tsend <box> <session:window> <text…>")
		return 2
	}
	box, target, text := args[0], args[1], strings.Join(args[2:], " ")
	hostsFile := filepath.Join(store.Home(), "state", "playbook", "hosts.json")
	hosts, err := playbook.LoadHosts(hostsFile)
	if err != nil {
		fmt.Fprintf(errs, "crius-tsend: NOT SENT, nothing typed: %v\n", err)
		return 2
	}
	var names []string
	for _, h := range hosts {
		if !strings.EqualFold(h.Name, box) {
			names = append(names, h.Name)
			continue
		}
		unlock, err := lock(filepath.Join(store.Home(), "state", "tsend.lock"))
		if err != nil {
			fmt.Fprintf(errs, "crius-tsend: NOT SENT, nothing typed: %v\n", err)
			return 2
		}
		defer unlock()
		err = tabsend.Send(runner(h), target, text)
		where := h.Name + " " + target
		switch {
		case err == nil:
			fmt.Fprintf(out, "SENT %s\n", where)
			return 0
		case errors.Is(err, tabsend.ErrMaybeSent):
			fmt.Fprintf(errs, "MAYBE SENT %s: %v: look at the tab, do not send again\n", where, err)
			return 3
		case errors.Is(err, tabsend.ErrRefused):
			fmt.Fprintf(errs, "NOT SENT %s: %v\n", where, err)
			return 2
		}
		fmt.Fprintf(errs, "NOT SENT %s: %v\n", where, err)
		return 1
	}
	fmt.Fprintf(errs, "crius-tsend: NOT SENT, nothing typed: no box %q in %s (it has: %s)\n", box, hostsFile, strings.Join(names, ", "))
	return 2
}

// lock holds a file lock, so two sends never type into a pane at once (one could press Enter on the other's text).
func lock(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return func() { f.Close() }, nil
}
