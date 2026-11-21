package main

// A T3 Code thread has no cmux tab and no waiter, so a bus line alone does not wake it: Crius relayed lines into threads
// by hand 22 times in 10 days, and on 11-15 AXI qa sat idle 20 min on a NEED addressed to it. A line that asks a thread
// role to act is handed to its thread as well, with `t3 thread send <id> -` (the line on stdin; a busy thread takes it as
// its next turn). The thread is the one crius-hook filed in the role's file from the session's T3CODE_THREAD_ID.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/orzazade/crius/internal/sessionstate"
	"github.com/orzazade/crius/internal/store"
	"github.com/orzazade/crius/internal/t3cli"
	"github.com/orzazade/crius/internal/wakewatch"
)

// t3CLI is the T3 Code CLI used when the Crius home has no bin/t3 link. Tests replace it.
var t3CLI = "t3"

// t3Timeout bounds one t3 call: it takes about 1 s; a hung server must not hold the sender.
const t3Timeout = 10 * time.Second

// userHome is the home whose ~/.claude*/sessions list the live claudes, and whose T3 Code store (t3Store, read only)
// names the session T3 resumes for a thread. Tests replace it.
var userHome = os.UserHomeDir

func t3Store(home string) string { return filepath.Join(home, ".t3", "userdata", "statev2.sqlite") }

// actTypes are the line types that ask the reader to act; only these go to a thread. An INFO, ACK or START waits for the
// thread's next read of the bus.
var actTypes = map[string]bool{"DONE": true, "NEED": true, "ASK": true, "FAIL": true, "PASS": true, "DECISION": true,
	"CORRECTION": true, "CORRECTED": true, "ANSWER": true, "ORDER": true, "OWNER": true, "READY": true, "GO": true,
	"PAUSE": true}

// threadRole is a main recipient. With thread set it is filed with no tab (no workspace, no surface), and id is the T3
// Code thread its current session runs in, "" when none is known: a session outside both cmux and T3 Code, or one that
// has run no hook since crius-hook began to file T3CODE_THREAD_ID. Else bus says how it reads the bus instead.
type threadRole struct {
	role, id string
	thread   bool
	bus      string
}

// mainRecipients gives the main recipients of to as project:role, once each, in order: a copy asks nothing of its reader,
// as wakewatch's addressed has it, and a whole team or "cc" is no one. The split is store.Names, the bus parser's.
func mainRecipients(to, project string) []string {
	var out []string
	main, _, _ := strings.Cut(to, "(")
	for _, n := range store.Names(main) {
		if strings.EqualFold(n, "cc") {
			break
		}
		if n = busRole(n, project); n != "" && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

// threadRecipients gives each main recipient of to, marked a thread when crius-hook filed its current session with no
// tab. The thread comes from that session's own file, which the hook rewrites on every event; the role's file is
// rewritten only when the role changes session. crius:lead is no thread: its own waiter reads the bus. A role with no
// file is not known to be a thread.
func threadRecipients(to, project, home string) []threadRole {
	var th []threadRole
	for _, n := range mainRecipients(to, project) {
		if n == "crius:lead" {
			th = append(th, threadRole{role: n, bus: "on the bus (its own waiter reads it)"})
			continue
		}
		p, r, _ := strings.Cut(n, ":")
		s, err := sessionstate.ReadRole(filepath.Join(home, "roles"), p, r)
		switch {
		case err != nil:
			th = append(th, threadRole{role: n, bus: "on the bus only (no role file, so no thread is known)"})
			continue
		case s.Workspace != "" || s.Surface != "":
			th = append(th, threadRole{role: n, bus: "on the bus (tab)"})
			continue
		}
		id := s.Thread
		if cur, err := sessionstate.Read(filepath.Join(home, "sessions"), s.SessionID); err == nil {
			id = cur.Thread
		}
		th = append(th, threadRole{role: n, id: id, thread: true})
	}
	return th
}

// outcomes is what happened to each main recipient of an act-type line, in the order to names them. Its last line on
// stderr names them all: a sender that keeps only the last line (`2>&1 | tail -1`, Crius 11-21 09:52 and 10:20) saw
// one "delivered to" line of two and read it as a partial delivery.
type outcomes struct {
	roles  []string
	what   map[string]string
	thread bool // one of them is a thread role; with none, nothing else is said and neither is the summary
}

func (o *outcomes) set(role, what string) {
	if o.what == nil {
		o.what = map[string]string{}
	}
	if _, ok := o.what[role]; !ok {
		o.roles = append(o.roles, role)
	}
	o.what[role] = what
}

// add appends more to what happened to role, when role is one of them.
func (o *outcomes) add(role, more string) {
	if w, ok := o.what[role]; ok {
		o.what[role] = w + "; " + more
	}
}

// addAll appends more to what happened to every role.
func (o *outcomes) addAll(more string) {
	for _, r := range o.roles {
		o.add(r, more)
	}
}

// print writes the summary line, for two or more roles of which one is a thread (one role's own line already names it;
// tabs alone are read from the bus and nothing else is said).
func (o *outcomes) print(stderr io.Writer) {
	if len(o.roles) < 2 || !o.thread {
		return
	}
	parts := make([]string, len(o.roles))
	for i, r := range o.roles {
		parts[i] = r + " " + o.what[r]
	}
	fmt.Fprintf(stderr, "bus-post: to %d roles: %s\n", len(o.roles), strings.Join(parts, "; "))
}

// notSent says on stderr that a line of type typ, which is not sent to threads, reached the thread roles of to only on the
// bus. Silence read as "delivered": on 11-20 two INFO lines that needed action sat unread in axi:api-split's and
// cortex:lead's threads. It names only the roles deliver would send an action type to, and says nothing when there are
// none: not the poster's own role or thread, not Crius's thread, not a filed id that is no thread id.
func notSent(home, log, typ, from, to, own string, stderr io.Writer) {
	crius, _ := wakewatch.CriusThread(home) // unreadable: no role is left out for it; deliver reports the error on its path
	var who []string
	for _, t := range threadRecipients(to, logProject(log), home) {
		if t.thread && t.id != "" && t.id != own && t.id != crius && !strings.HasPrefix(t.id, "-") &&
			t.role != strings.ToLower(from) {
			who = append(who, t.role)
		}
	}
	if len(who) == 0 {
		return
	}
	sees := "sees it only when it next reads"
	if len(who) > 1 {
		sees = "see it only when they next read"
	}
	var types []string
	for k := range actTypes {
		types = append(types, k)
	}
	slices.Sort(types)
	fmt.Fprintf(stderr, "bus-post: posted; %s is not sent to threads (only %s): %s %s the bus; "+
		"if it must act now, post it as one of those types or send it with crius-send\n", typ, strings.Join(types, ", "),
		strings.Join(who, ", "), sees)
}

// deliver hands line, of type typ from from, to the thread of every main recipient it asks to act, one t3 call per
// thread, all at once. It never sends to the poster's own thread (its role, or the thread it runs in) nor to Crius's
// thread, nor to a thread whose send would start a second session for a role whose live claude reads the bus through its
// own waiter (twin). It says on stderr who got it and who did not, and gives the roles that got it or are read by such a
// live claude, whether a send failed, and what happened to each main recipient (nil for a type not sent to threads),
// which the caller prints last.
func deliver(home, log, typ, from, to, line string, stderr io.Writer) (got []string, failed bool, out *outcomes) {
	own := os.Getenv("T3CODE_THREAD_ID")
	if !actTypes[typ] {
		notSent(home, log, typ, from, to, own, stderr)
		return nil, false, nil
	}
	out = &outcomes{}
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
		if t.thread {
			out.thread = true
		}
		i := slices.IndexFunc(sends, func(s *send) bool { return s.id == t.id })
		switch {
		case t.role == strings.ToLower(from):
			out.set(t.role, "not sent (the poster)")
		case !t.thread:
			out.set(t.role, t.bus)
		case t.id != "" && t.id == own:
			out.set(t.role, "not sent (the poster's own thread)")
		case t.id != "" && t.id == crius:
			out.set(t.role, "not sent (Crius's thread, which reads the bus)")
		case t.id == "":
			out.set(t.role, "not woken (no tab and no T3 Code thread filed; on the bus only)")
			fmt.Fprintf(stderr, "bus-post: posted, but %s has no tab and no T3 Code thread filed: a bus line does not "+
				"wake it; send it to its session too\n", t.role)
		case strings.HasPrefix(t.id, "-"):
			failed = true
			out.set(t.role, fmt.Sprintf("NOT delivered (filed thread id %q is no thread id)", t.id))
			fmt.Fprintf(stderr, "bus-post: posted, but not delivered to %s: its filed thread id %q is no thread id; send "+
				"it to the thread by hand (t3_thread_send, mode auto)\n", t.role, t.id)
		case i >= 0: // two roles in one thread: one send
			out.set(t.role, "")
			sends[i].roles = append(sends[i].roles, t.role)
		default:
			out.set(t.role, "")
			sends = append(sends, &send{id: t.id, roles: []string{t.role}})
		}
	}
	if len(sends) > 0 {
		look := lookLive(home)
		if look.err != nil {
			fmt.Fprintf(stderr, "bus-post: could not list the live claudes, so a send may start a second claude for a role "+
				"that has one: %v; delivered anyway\n", look.err)
		}
		kept := sends[:0]
		for _, s := range sends {
			if why, live := look.twin(s.id, s.roles, log, stderr); why != "" {
				got = append(got, live...) // its live claude reads the bus: no "no live session" warning for it
				for _, r := range s.roles {
					if slices.Contains(live, r) {
						out.set(r, "reads the bus (its live session's own waiter; thread "+s.id+" not sent)")
					} else {
						out.set(r, "not sent (thread "+s.id+" is shared with a role whose live session reads the bus)")
					}
				}
				fmt.Fprintf(stderr, "bus-post: posted, not delivered to %s (thread %s): %s\n", strings.Join(s.roles, ", "),
					s.id, why)
				continue
			}
			kept = append(kept, s)
		}
		sends = kept
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
			for _, r := range s.roles {
				out.set(r, fmt.Sprintf("NOT delivered (thread %s): %v", s.id, s.err))
			}
			fmt.Fprintf(stderr, "bus-post: posted, but not delivered to %s (thread %s): %v; send it to the thread by "+
				"hand (t3_thread_send, mode auto)\n", who, s.id, s.err)
			continue
		}
		got = append(got, s.roles...)
		for _, r := range s.roles {
			out.set(r, "delivered (thread "+s.id+")")
		}
		fmt.Fprintf(stderr, "bus-post: delivered to %s (thread %s)\n", who, s.id)
	}
	return got, failed, out
}

// liveLook is the live claudes of each role, found as wakewatch's twins check finds them (by the claude's own name, else
// crius-hook's record of its session), read once per post. err is a failed look.
type liveLook struct {
	by     map[string][]wakewatch.Session
	states []sessionstate.State
	err    error
}

func lookLive(home string) liveLook {
	uh, err := userHome()
	if err != nil {
		return liveLook{err: err}
	}
	ss, err := wakewatch.LiveSessions(uh)
	if err != nil {
		return liveLook{err: err}
	}
	states, err := sessionstate.List(filepath.Join(home, "sessions"))
	if err != nil && !(errors.Is(err, fs.ErrNotExist) && states == nil) {
		return liveLook{err: fmt.Errorf("crius-hook's session records: %w", err)}
	}
	return liveLook{by: wakewatch.RoleSessions(ss, states), states: states}
}

// twin says why a send to thread for roles would start a second session for a role whose live claude reads the bus
// through its own waiter: T3 starts a claude for the session it resumes for the thread unless that session's claude
// runs (11-17 19:08 and 19:43: axi:ms-flow ran as session 6376280f, pid 77924, with its team-wait.sh on bus/axi.log; T3
// resumed 05473ed1 for its thread beside it). It gives the roles with a live claude too. "" means send: no live claude
// for the roles, one on the session T3 resumes, a live one neither in a turn nor with a waiter on log (it would not read
// the line), or a failed look; the last two are said and sent anyway (an undelivered line to an idle session is worse
// than a rare twin).
func (l liveLook) twin(thread string, roles []string, log string, stderr io.Writer) (why string, liveRoles []string) {
	if l.err != nil {
		return "", nil
	}
	var live []wakewatch.Session
	for _, r := range roles {
		if len(l.by[r]) > 0 {
			live = append(live, l.by[r]...)
			liveRoles = append(liveRoles, r)
		}
	}
	if len(live) == 0 {
		return "", nil
	}
	who, them := strings.Join(liveRoles, ", "), wakewatch.Describe(live, l.states)
	resumes, err := t3Resumes(thread)
	if err != nil {
		fmt.Fprintf(stderr, "bus-post: %s has %s, but could not read which session T3 resumes for thread %s, so this "+
			"send may start a second one: %v; delivered anyway\n", who, them, thread, err)
		return "", nil
	}
	if slices.ContainsFunc(live, func(s wakewatch.Session) bool { return s.ID == resumes }) {
		return "", nil
	}
	short := resumes[:min(8, len(resumes))]
	ps, err := procList()
	if err != nil {
		fmt.Fprintf(stderr, "bus-post: %s has %s while T3 resumes session %s for thread %s, but could not list processes "+
			"to find its bus waiter: %v; delivered anyway, so T3 may start a second session\n", who, them, short, thread, err)
		return "", nil
	}
	if w := busWaiter(ps, log, live); w != 0 {
		return them + " runs for it, and T3 would resume session " + short + " for the thread as a second session; the " +
			"live session reads the bus through its own waiter (pid " + strconv.Itoa(w) + ")", liveRoles
	}
	// team-wait.sh exits when it wakes on a line, and the claude starts it again in the turn that handles the line: in a
	// burst of posts the role has no waiter while it is busy (review 11-17).
	if slices.ContainsFunc(live, func(s wakewatch.Session) bool { return s.Status == "busy" }) {
		return them + " runs for it, in a turn now, and T3 would resume session " + short + " for the thread as a second " +
			"session; the live session reads the bus through its own waiter, which it starts again after a wake-up and " +
			"which reads on from its cursor", liveRoles
	}
	fmt.Fprintf(stderr, "bus-post: %s has %s while T3 resumes session %s for thread %s, and no bus waiter of it reads "+
		"%s: delivered, so T3 may start a second session (wakewatch's twins check says so if it stays)\n", who, them,
		short, thread, shown(log))
	return "", nil
}

// procList gives every process as `ps -axww -o pid=,ppid=,command=` prints it (read only). Tests replace it.
var procList = func() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), t3Timeout)
	defer cancel()
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, "/bin/ps", "-axww", "-o", "pid=,ppid=,command=")
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ps -axww -o pid=,ppid=,command=: %v: %s", err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// busWaiter gives the pid of a team-wait.sh process that reads log (named as an argument, or its folder as --watch-dir)
// and runs under one of the live claudes (a child of it, or of a child, ...), 0 for none. Only a process that runs the
// script counts: its first word, or the second after an interpreter (bash team-wait.sh), as crius-wakewatch reads them.
// A relative path in a waiter's command is read from bus-post's folder, not the waiter's: such a waiter is missed and the
// line delivered, said (waiters are started with absolute paths).
func busWaiter(ps, log string, live []wakewatch.Session) int {
	parent := map[int]int{}
	var waiters []int
	for _, line := range strings.Split(ps, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		parent[pid] = ppid
		w := f[2:]
		at := slices.IndexFunc(w, func(s string) bool { return filepath.Base(s) == "team-wait.sh" })
		if at < 0 || at > 1 {
			continue
		}
		for i := at + 1; i < len(w); i++ {
			dir, isDir := strings.CutPrefix(w[i], "--watch-dir=")
			if w[i] == "--watch-dir" && i+1 < len(w) {
				dir, isDir = w[i+1], true
			}
			if samePath(w[i], log) || isDir && samePath(dir, filepath.Dir(log)) {
				waiters = append(waiters, pid)
				break
			}
		}
	}
	for _, w := range waiters {
		for p, n := parent[w], 0; p > 1 && n < 64; p, n = parent[p], n+1 {
			if slices.ContainsFunc(live, func(s wakewatch.Session) bool { return s.PID == p }) {
				return w
			}
		}
	}
	return 0
}

// samePath says a and b name one file: the same absolute path, or the same one once links are followed.
func samePath(a, b string) bool {
	a1, err1 := filepath.Abs(a)
	b1, err2 := filepath.Abs(b)
	if err1 != nil || err2 != nil {
		return false
	}
	if a1 == b1 {
		return true
	}
	a2, err1 := filepath.EvalSymlinks(a1)
	b2, err2 := filepath.EvalSymlinks(b1)
	return err1 == nil && err2 == nil && a2 == b2
}

// resumeQuery gives the Claude session T3 resumes for a thread: the thread's active provider thread, whose payload names
// the session (nativeThreadRef.nativeId). A thread keeps its older provider threads (11-17: axi:ms-flow's held one on
// 6376280f, runs 1-46, and the active one on 05473ed1, runs 37-49); T3 sent to the active one. `t3 thread show` does
// not print it, and the --resume argument of a thread's claude names the session it started on, not the next one.
const resumeQuery = `SELECT coalesce(json_extract(p.payload_json, '$.nativeThreadRef.nativeId'), '') AS session
FROM orchestration_v2_projection_threads t
JOIN orchestration_v2_projection_provider_threads p ON p.provider_thread_id = t.active_provider_thread_id
WHERE t.thread_id = '%s'`

// t3Resumes reads the Claude session T3 resumes for thread from T3's store with sqlite3 (mode=ro: the T3 server keeps
// it open in WAL mode, as crius-wakewatch's provider-error sweep reads it).
func t3Resumes(thread string) (string, error) {
	uh, err := userHome()
	if err != nil {
		return "", err
	}
	db := t3Store(uh)
	if _, err := os.Stat(db); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), t3Timeout)
	defer cancel()
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, "/usr/bin/sqlite3", "-json", "-cmd", ".timeout 5000", "file:"+db+"?mode=ro",
		fmt.Sprintf(resumeQuery, strings.ReplaceAll(thread, "'", "''")))
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("sqlite3 %s: %v: %s", db, err, strings.TrimSpace(errb.String()))
	}
	var rows []struct{ Session string }
	if b := bytes.TrimSpace(out.Bytes()); len(b) > 0 {
		if err := json.Unmarshal(b, &rows); err != nil {
			return "", fmt.Errorf("sqlite3 %s: %v", db, err)
		}
	}
	switch {
	case len(rows) == 0:
		return "", fmt.Errorf("T3's store names no active provider thread for %s", thread)
	case rows[0].Session == "":
		return "", fmt.Errorf("T3's store names no Claude session for %s", thread)
	}
	return rows[0].Session, nil
}

// sendT3 hands text to a T3 Code thread: `t3 thread send <id> -` with the text on stdin (as outbox does for Crius's own
// thread). t3 is the link in the Crius bin folder when there is one: nvm keeps t3 off the PATH of launchd jobs and of
// other shells.
func sendT3(home, id, text string) error {
	if out, errOut, err := t3cli.Run(t3cli.Bin(home, t3CLI), t3Timeout, text, "thread", "send", id, "-"); err != nil {
		return fmt.Errorf("t3 thread send: %v: %s", err, t3cli.FirstLine(out, errOut))
	}
	return nil
}
