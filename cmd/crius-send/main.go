// Command crius-send types one line into the Claude tab at a workspace and surface, then Enter, and proves it went:
//
//	crius-send <workspace:N> <surface:N> <text…>
//
// It types only into a tab where Claude is in front and the box holds no draft, presses Enter once the box holds the
// text, and reads the box after, pressing Enter again up to 3 in all: it exits 0 when the box no longer holds the text,
// 1 (NOT SENT) when it still does or nothing was typed, 3 (MAYBE SENT: look at the tab, do not send again) when Enter
// was pressed but the box could not be read after, and 2 for a call it refuses before typing (X14: a leader's messages
// were left typed but unsent).
//
//	crius-send <project:role> <text…>
//
// sends to a team role that runs as a T3 Code thread (its role file has no tab and a t3_thread), with `t3 thread send`,
// under the same contract: 0 only when `t3 thread show` proves the text is in the thread or queued as its next turn (a
// busy thread; T3 never interrupts a run, and the output says so), 1 NOT SENT when t3 refused it, 3 MAYBE SENT when it
// was sent and no look proved it, 2 for a role with no thread filed.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/orzazade/crius/internal/outbox"
	"github.com/orzazade/crius/internal/redact"
	"github.com/orzazade/crius/internal/store"
	"github.com/orzazade/crius/internal/t3cli"
	"github.com/orzazade/crius/internal/wakewatch"
)

var (
	workspaceRe = regexp.MustCompile(`^workspace:\d+$`)
	surfaceRe   = regexp.MustCompile(`^surface:\d+$`)
	// roleRe is project:role as role files name them (sessionstate.RolePath)
	roleRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*:[a-z0-9][a-z0-9-]*$`)
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, out, errs io.Writer) int {
	if len(args) >= 2 && !workspaceRe.MatchString(args[0]) && roleRe.MatchString(args[0]) {
		return sendRole(args[0], strings.Join(args[1:], " "), out, errs)
	}
	if len(args) < 3 {
		fmt.Fprintln(errs, "usage: crius-send <workspace:N> <surface:N> <text…> | crius-send <project:role> <text…>")
		return 2
	}
	ws, surface, text := args[0], args[1], strings.Join(args[2:], " ")
	// a bare number is text to cmux, which then types into the caller's own tab
	if !workspaceRe.MatchString(ws) || !surfaceRe.MatchString(surface) {
		fmt.Fprintf(errs, "crius-send: %q %q: give workspace:N and surface:N as cmux tree prints them\n", ws, surface)
		return 2
	}
	if why := refuse(text); why != "" {
		fmt.Fprintln(errs, "crius-send: not sent:", why)
		return 2
	}
	text = wakewatch.Clean(text) // secrets masked and spaces joined, as crius-say types it
	if err := outbox.SendTo(store.Home(), ws, surface, text); errors.Is(err, outbox.ErrMaybeSent) {
		fmt.Fprintf(errs, "MAYBE SENT %s: %v\n", surface, err)
		return 3
	} else if err != nil {
		fmt.Fprintf(errs, "NOT SENT %s: %v\n", surface, err)
		return 1
	}
	fmt.Fprintf(out, "SENT %s\n", surface)
	return 0
}

// sendRole sends text to a team role that runs as a T3 Code thread (outbox.T3Role) with t3cli.Deliver, which proves it
// arrived: 0 in the thread or queued as its next turn (said), 1 NOT SENT (t3 refused it), 3 MAYBE SENT (sent, not
// proved), 2 refused before sending: a role with no thread filed, or text crius-say would not send either. It stores
// nothing, as the tab form.
func sendRole(role, text string, out, errs io.Writer) int {
	if why := refuse(text); why != "" {
		fmt.Fprintln(errs, "crius-send: not sent:", why)
		return 2
	}
	home := store.Home()
	thread, err := outbox.T3Role(home, role)
	if err != nil {
		fmt.Fprintf(errs, "crius-send: NOT SENT, nothing sent: %v\n", err)
		return 2
	}
	if thread == "" {
		fmt.Fprintf(errs, "crius-send: NOT SENT, nothing sent: role %s has no T3 Code thread filed in %s (no role file, or "+
			"it runs in a cmux tab): for a tab give workspace:N surface:N as cmux tree prints them\n", role, filepath.Join(home, "roles"))
		return 2
	}
	d, err := t3cli.Deliver(t3cli.Bin(home, outbox.T3), thread, wakewatch.Clean(text), outbox.CmuxTimeout)
	switch {
	case err == nil:
		fmt.Fprintf(out, "SENT %s: %s\n", role, outbox.Said(thread, d))
		return 0
	case errors.Is(err, t3cli.ErrUnproved):
		fmt.Fprintf(errs, "MAYBE SENT %s (T3 Code thread %s): %v: look at the thread, do not send again\n", role, thread, err)
		return 3
	}
	fmt.Fprintf(errs, "NOT SENT %s (T3 Code thread %s): %v\n", role, thread, err)
	return 1
}

// refuse says why text cannot go as one line without a change in meaning, as crius-say's rules: no line break (it would
// press Enter early), no backslash (cmux reads \n as Enter), at most MaxLine runes with secrets masked, and not empty.
func refuse(text string) string {
	switch masked, _ := redact.Redact(text); {
	case strings.ContainsAny(text, "\r\n"):
		return "a line break would press Enter early: send one line"
	case strings.Contains(text, `\`):
		return `cmux reads a backslash as a key (\n is Enter), so the text cannot hold one`
	case len([]rune(masked)) > wakewatch.MaxLine:
		return fmt.Sprintf("%d runes with secrets masked, at most %d go in one line", len([]rune(masked)), wakewatch.MaxLine)
	case wakewatch.Clean(text) == "":
		return "the text is empty"
	}
	return ""
}
