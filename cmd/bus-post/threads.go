package main

// A T3 Code thread has no cmux tab and no waiter, so a bus line alone does not wake it: Crius relayed lines into threads
// by hand 22 times in 10 days, and on 11-15 AXI qa sat idle 20 min on a NEED addressed to it. A line that asks a thread
// role to act is handed to its thread as well, with `t3 thread send <id> -` (the line on stdin; a busy thread takes it as
// its next turn). The thread is the one crius-hook filed in the role's file from the session's T3CODE_THREAD_ID.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/orzazade/crius/internal/sessionstate"
	"github.com/orzazade/crius/internal/wakewatch"
)

// t3CLI is the T3 Code CLI used when the Crius home has no bin/t3 link. Tests replace it.
var t3CLI = "t3"

// t3Timeout bounds one t3 call: it takes about 1 s; a hung server must not hold the sender.
const t3Timeout = 10 * time.Second

// actTypes are the line types that ask the reader to act; only these go to a thread. An INFO, ACK or START waits for the
// thread's next read of the bus.
var actTypes = map[string]bool{"DONE": true, "NEED": true, "ASK": true, "FAIL": true, "PASS": true, "DECISION": true,
	"CORRECTION": true, "CORRECTED": true, "ANSWER": true, "ORDER": true, "OWNER": true, "READY": true, "GO": true,
	"PAUSE": true}

// threadRole is a main recipient filed with no tab (no workspace, no surface). id is the T3 Code thread its current
// session runs in, "" when none is known: a session outside both cmux and T3 Code, or one that has run no hook since
// crius-hook began to file T3CODE_THREAD_ID.
type threadRole struct{ role, id string }

// threadRecipients gives each main recipient of to (a copy asks nothing of its reader, as wakewatch's addressed has it)
// whose current session crius-hook filed with no tab. The thread comes from that session's own file, which the hook
// rewrites on every event; the role's file is rewritten only when the role changes session. crius:lead is left out: its
// own waiter reads the bus. A role with no file is not known to be a thread and is left out too.
func threadRecipients(to, project, home string) []threadRole {
	var th []threadRole
	main, _, _ := strings.Cut(to, "(")
	for _, n := range strings.FieldsFunc(main, func(r rune) bool { return r == ',' || r == ' ' }) {
		if strings.EqualFold(n, "cc") {
			break
		}
		n = busRole(n, project)
		if n == "" || n == "crius:lead" || slices.ContainsFunc(th, func(t threadRole) bool { return t.role == n }) {
			continue
		}
		p, r, _ := strings.Cut(n, ":")
		s, err := sessionstate.ReadRole(filepath.Join(home, "roles"), p, r)
		if err != nil || s.Workspace != "" || s.Surface != "" {
			continue
		}
		id := s.Thread
		if cur, err := sessionstate.Read(filepath.Join(home, "sessions"), s.SessionID); err == nil {
			id = cur.Thread
		}
		th = append(th, threadRole{n, id})
	}
	return th
}

// deliver hands line, of type typ from from, to the thread of every main recipient it asks to act, one t3 call per
// thread, all at once. It never sends to the poster's own thread (its role, or the thread it runs in) nor to Crius's
// thread. It says on stderr who got it and who did not, and gives the roles that got it, and whether a send failed.
func deliver(home, log, typ, from, to, line string, stderr io.Writer) (got []string, failed bool) {
	if !actTypes[typ] {
		return nil, false
	}
	own := os.Getenv("T3CODE_THREAD_ID")
	crius, err := wakewatch.CriusThread(home)
	if err != nil {
		fmt.Fprintf(stderr, "bus-post: %v; a role filed in that thread may get this line\n", err)
	}
	type send struct {
		id    string
		roles []string
		err   error
	}
	var sends []*send
	for _, t := range threadRecipients(to, logProject(log), home) {
		i := slices.IndexFunc(sends, func(s *send) bool { return s.id == t.id })
		switch {
		case t.role == strings.ToLower(from):
		case t.id != "" && (t.id == own || t.id == crius):
		case t.id == "":
			fmt.Fprintf(stderr, "bus-post: posted, but %s has no tab and no T3 Code thread filed: a bus line does not "+
				"wake it; send it to its session too\n", t.role)
		case strings.HasPrefix(t.id, "-"):
			failed = true
			fmt.Fprintf(stderr, "bus-post: posted, but not delivered to %s: its filed thread id %q is no thread id; send "+
				"it to the thread by hand (t3_thread_send, mode auto)\n", t.role, t.id)
		case i >= 0: // two roles in one thread: one send
			sends[i].roles = append(sends[i].roles, t.role)
		default:
			sends = append(sends, &send{id: t.id, roles: []string{t.role}})
		}
	}
	text := line + "\n(on the bus " + shown(log) + ")"
	var wg sync.WaitGroup
	for _, s := range sends {
		wg.Go(func() { s.err = sendT3(home, s.id, text) })
	}
	wg.Wait()
	for _, s := range sends {
		who := strings.Join(s.roles, ", ")
		if s.err != nil {
			failed = true
			fmt.Fprintf(stderr, "bus-post: posted, but not delivered to %s (thread %s): %v; send it to the thread by "+
				"hand (t3_thread_send, mode auto)\n", who, s.id, s.err)
			continue
		}
		got = append(got, s.roles...)
		fmt.Fprintf(stderr, "bus-post: delivered to %s (thread %s)\n", who, s.id)
	}
	return got, failed
}

// sendT3 hands text to a T3 Code thread: `t3 thread send <id> -` with the text on stdin (as outbox does for Crius's own
// thread). t3 is the link in the Crius bin folder when there is one: nvm keeps t3 off the PATH of launchd jobs and of
// other shells.
func sendT3(home, id, text string) error {
	t3 := t3CLI
	if p := filepath.Join(home, "bin", "t3"); fileThere(p) {
		t3 = p
	}
	ctx, cancel := context.WithTimeout(context.Background(), t3Timeout)
	defer cancel()
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, t3, "thread", "send", id, "-")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = strings.NewReader(text), &out, &out
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil && !(errors.Is(err, exec.ErrWaitDelay) && ctx.Err() == nil) {
		said, _, _ := strings.Cut(strings.TrimSpace(out.String()), "\n") // a Node stack trace after it says no more
		if ctx.Err() != nil {
			err = fmt.Errorf("no answer in %s", t3Timeout)
		}
		return fmt.Errorf("t3 thread send: %v: %s", err, said)
	}
	return nil
}

// fileThere says p is there, a link that points nowhere too: running it then fails under its own name.
func fileThere(p string) bool { _, err := os.Lstat(p); return err == nil }
