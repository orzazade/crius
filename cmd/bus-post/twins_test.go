package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/orzazade/crius/internal/testutil"
)

// fakeUserHome points the live-claude look and T3's store at an empty user home of the test's own.
func fakeUserHome(t *testing.T) string {
	t.Helper()
	uhome := t.TempDir()
	old := userHome
	userHome = func() (string, error) { return uhome, nil }
	t.Cleanup(func() { userHome = old })
	return uhome
}

// liveClaude files a live claude as Claude Code does, in a config folder named like the Max ones (~/.claude-max3): its
// pid is the test's own process or its parent, so nothing is started or stopped. Its status is "idle", or the one given.
func liveClaude(t *testing.T, uhome string, pid int, name, session string, status ...string) {
	t.Helper()
	st := append(status, "idle")[0]
	testutil.Write(t, filepath.Join(uhome, ".claude-max3", "sessions", strconv.Itoa(pid)+".json"),
		fmt.Sprintf(`{"pid":%d,"sessionId":%q,"name":%q,"procStart":%q,"status":%q}`, pid, session, name,
			testutil.ProcStart(t, pid), st))
}

// storeResumes writes T3 Code's store as T3 keeps it: each thread row names its active provider thread, whose payload names
// the Claude session T3 resumes for the thread (nativeThreadRef.nativeId). The thread's older provider thread is there
// too, on the session the live claude ran on 11-17 (6376280f).
func storeResumes(t *testing.T, uhome string, sessions map[string]string) {
	t.Helper()
	db := t3Store(uhome)
	if err := os.MkdirAll(filepath.Dir(db), 0o700); err != nil {
		t.Fatal(err)
	}
	sql := `CREATE TABLE orchestration_v2_projection_threads (thread_id TEXT PRIMARY KEY, title TEXT NOT NULL,
 active_provider_thread_id TEXT, payload_json TEXT NOT NULL);
CREATE TABLE orchestration_v2_projection_provider_threads (provider_thread_id TEXT PRIMARY KEY, thread_id TEXT,
 provider TEXT NOT NULL, status TEXT NOT NULL, payload_json TEXT NOT NULL);`
	for th, s := range sessions {
		old, cur := "pt:"+th+":1", "pt:"+th+":37"
		sql += fmt.Sprintf(`
INSERT INTO orchestration_v2_projection_threads VALUES ('%s', 'x', '%s', '{}');
INSERT INTO orchestration_v2_projection_provider_threads VALUES ('%s', '%s', 'claudeAgent', 'idle',
 '{"driver":"claudeAgent","nativeThreadRef":{"driver":"claudeAgent","nativeId":"6376280f-b263-47d9-91fb-bb90c886399a","strength":"strong"}}');
INSERT INTO orchestration_v2_projection_provider_threads VALUES ('%s', '%s', 'claudeAgent', 'idle',
 '{"driver":"claudeAgent","nativeThreadRef":{"driver":"claudeAgent","nativeId":"%s","strength":"strong"}}');`,
			th, cur, old, th, cur, th, s)
	}
	if out, err := exec.Command("/usr/bin/sqlite3", db, sql).CombinedOutput(); err != nil {
		t.Fatalf("sqlite3: %v: %s", err, out)
	}
}

// fakePS gives the process list a test sees: each line "pid ppid command", as ps -o pid=,ppid=,command= prints it.
func fakePS(t *testing.T, lines ...string) {
	t.Helper()
	old := procList
	procList = func() (string, error) { return strings.Join(lines, "\n") + "\n", nil }
	t.Cleanup(func() { procList = old })
}

// 11-17 19:08 and 19:43: a DONE to axi:ms-flow was delivered to its T3 thread while its live claude (pid 77924, session
// 6376280f, waiting on its own bus waiter) ran; T3 resumed its stored session 05473ed1 as a second claude for the role.
// A role whose live claude is on another session than the one T3 resumes for the thread is not delivered to.
func TestALiveClaudeOnAnotherSessionIsNotStartedTwice(t *testing.T) {
	for _, waiter := range []string{
		"bash /Users/x/.claude/skills/cmux-team/scripts/team-wait.sh --role ms --alias crius:ms --cursor ms/.c %[1]s",
		"/bin/bash team-wait.sh --mode crius --watch-dir %[2]s",
	} {
		home, log := setup(t)
		roles(t, home)
		fake := fakeT3(t, home)
		uhome := fakeUserHome(t)
		pid := os.Getpid()
		liveClaude(t, uhome, pid, "crius · ms", "6376280f-b263-47d9-91fb-bb90c886399a")
		storeResumes(t, uhome, map[string]string{"th-ms": "05473ed1-b762-4445-93c2-e2618272f625"})
		// As on 11-17: the claude runs a zsh that runs the waiter (77924 > 45017 > 45020), beside other children.
		fakePS(t, fmt.Sprintf("70001 %d /bin/zsh -c source snapshot.sh && eval 'team-wait.sh …'", pid),
			"70002 70001 "+fmt.Sprintf(waiter, log, filepath.Dir(log)),
			fmt.Sprintf("70003 %d npm exec @azure-devops/mcp", pid))
		rc, errs := post(t, at("2026-11-17T19:08"), log, "DONE", "crius:dev", "crius:ms, crius:qa", "B7 merged")
		if calls := readOr(t, filepath.Join(fake, "calls")); rc != 0 || calls != "thread send th-qa -\n" {
			t.Fatalf("%s: rc %d, t3 calls %q: want only th-qa (crius:ms has a live claude on another session)\n%s",
				waiter, rc, calls, errs)
		}
		for _, want := range []string{"not delivered to crius:ms (thread th-ms)", "claude pid " + strconv.Itoa(pid) +
			" (session 6376280f", "T3 would resume session 05473ed1", "reads the bus through its own waiter (pid 70002)"} {
			if !strings.Contains(errs, want) {
				t.Errorf("%s: stderr lacks %q:\n%s", waiter, want, errs)
			}
		}
		// One warning line, then the summary line that names every role (lastLine).
		if n := strings.Count(strings.TrimSuffix(errs, lastLine(errs)+"\n"), "crius:ms"); n != 1 {
			t.Errorf("%s: crius:ms named on %d lines before the summary, want one:\n%s", waiter, n, errs)
		}
		if l := lastLine(errs); !strings.Contains(l, "crius:ms reads the bus (its live session's own waiter") ||
			!strings.Contains(l, "crius:qa delivered (thread th-qa)") {
			t.Errorf("%s: last line %q", waiter, l)
		}
	}
}

// A live claude with no bus waiter on this log does not read the line: it is delivered, and the possible second session
// is said. A waiter on another bus, or one that runs under another process, is no waiter of it.
func TestALiveClaudeWithNoBusWaiterIsDeliveredAndSaid(t *testing.T) {
	home, log := setup(t)
	roles(t, home)
	fake := fakeT3(t, home)
	uhome := fakeUserHome(t)
	liveClaude(t, uhome, os.Getpid(), "crius · ms", "6376280f-b263-47d9-91fb-bb90c886399a")
	storeResumes(t, uhome, map[string]string{"th-ms": "05473ed1-b762-4445-93c2-e2618272f625"})
	fakePS(t, fmt.Sprintf("70002 %d bash team-wait.sh --role ms %s", os.Getpid(), filepath.Join(filepath.Dir(log), "other.log")),
		"70004 1 bash team-wait.sh --role ms "+log,
		fmt.Sprintf("70005 %d /bin/zsh -c 'bash team-wait.sh %s'", os.Getpid(), log))
	rc, errs := post(t, at("2026-11-17T19:12"), log, "DONE", "crius:dev", "crius:ms", "x")
	if calls := readOr(t, filepath.Join(fake, "calls")); rc != 0 || calls != "thread send th-ms -\n" {
		t.Fatalf("rc %d, t3 calls %q: want delivered\n%s", rc, calls, errs)
	}
	if !strings.Contains(errs, "no bus waiter of it reads") || !strings.Contains(errs, "T3 may start a second session") ||
		!strings.Contains(errs, "delivered to crius:ms (thread th-ms)") {
		t.Errorf("stderr:\n%s", errs)
	}
}

// A burst: the role's waiter woke on the line before and exited, and the claude is in the turn that handles it (status
// busy). It starts its waiter again after the turn and reads on from its cursor, so the line is not sent to the thread.
func TestABusyLiveClaudeWithItsWaiterGoneIsNotStartedTwice(t *testing.T) {
	home, log := setup(t)
	roles(t, home)
	fake := fakeT3(t, home)
	uhome := fakeUserHome(t)
	liveClaude(t, uhome, os.Getpid(), "crius · ms", "6376280f-b263-47d9-91fb-bb90c886399a", "busy")
	storeResumes(t, uhome, map[string]string{"th-ms": "05473ed1-b762-4445-93c2-e2618272f625"})
	fakePS(t, fmt.Sprintf("70003 %d npm exec @azure-devops/mcp", os.Getpid()))
	rc, errs := post(t, at("2026-11-17T19:13"), log, "NEED", "crius:dev", "crius:ms", "x")
	if calls := readOr(t, filepath.Join(fake, "calls")); rc != 0 || calls != "" {
		t.Fatalf("rc %d, t3 calls %q: want none\n%s", rc, calls, errs)
	}
	if !strings.Contains(errs, "not delivered to crius:ms (thread th-ms)") || !strings.Contains(errs, "in a turn now") ||
		!strings.Contains(errs, "T3 would resume session 05473ed1") {
		t.Errorf("stderr:\n%s", errs)
	}
}

// A live claude on the session T3 resumes for the thread is the thread's own: T3 hands it the line, no new process.
// So is a role with a second live claude beside it (twins already, which wakewatch says): the send starts none.
func TestALiveClaudeOnTheThreadsSessionIsDelivered(t *testing.T) {
	home, log := setup(t)
	roles(t, home)
	fake := fakeT3(t, home)
	uhome := fakeUserHome(t)
	liveClaude(t, uhome, os.Getpid(), "crius · ms", "05473ed1-b762-4445-93c2-e2618272f625")
	liveClaude(t, uhome, os.Getppid(), "crius · ms", "6376280f-b263-47d9-91fb-bb90c886399a")
	storeResumes(t, uhome, map[string]string{"th-ms": "05473ed1-b762-4445-93c2-e2618272f625"})
	rc, errs := post(t, at("2026-11-17T19:09"), log, "DONE", "crius:dev", "crius:ms", "x")
	if calls := readOr(t, filepath.Join(fake, "calls")); rc != 0 || calls != "thread send th-ms -\n" ||
		errs != "bus-post: delivered to crius:ms (thread th-ms)\n" {
		t.Fatalf("rc %d, t3 calls %q: %q", rc, calls, errs)
	}
}

// With no live claude for the role, the line is delivered and T3's store is not read: a missing store says nothing.
func TestNoLiveClaudeIsDeliveredWithoutReadingT3(t *testing.T) {
	home, log := setup(t)
	roles(t, home)
	fake := fakeT3(t, home)
	uhome := fakeUserHome(t)
	liveClaude(t, uhome, os.Getpid(), "crius · qa", "s-qa-live") // another role's claude
	rc, errs := post(t, at("2026-11-17T19:10"), log, "DONE", "crius:dev", "crius:ms", "x")
	if calls := readOr(t, filepath.Join(fake, "calls")); rc != 0 || calls != "thread send th-ms -\n" ||
		errs != "bus-post: delivered to crius:ms (thread th-ms)\n" {
		t.Fatalf("rc %d, t3 calls %q: %q", rc, calls, errs)
	}
}

// A failed look is said, with its reason, and the line is still delivered: an undelivered line to an idle session is
// worse than a rare twin.
func TestAFailedLookIsSaidAndStillDelivered(t *testing.T) {
	for _, c := range []struct {
		name, want string
		set        func(t *testing.T, uhome string)
	}{
		{"session list unreadable", "could not list the live claudes", func(t *testing.T, uhome string) {
			testutil.Write(t, filepath.Join(uhome, ".claude", "sessions", "1.json"), "{not json")
		}},
		{"no T3 store", "could not read which session T3 resumes for thread th-ms", func(t *testing.T, uhome string) {
			liveClaude(t, uhome, os.Getpid(), "crius · ms", "s-live")
		}},
		{"thread not in T3's store", "names no active provider thread for th-ms", func(t *testing.T, uhome string) {
			liveClaude(t, uhome, os.Getpid(), "crius · ms", "s-live")
			storeResumes(t, uhome, map[string]string{"th-other": "s-x"})
		}},
		{"provider thread names no session", "names no Claude session for th-ms", func(t *testing.T, uhome string) {
			liveClaude(t, uhome, os.Getpid(), "crius · ms", "s-live")
			storeResumes(t, uhome, map[string]string{"th-ms": ""})
		}},
		{"process list unreadable", "could not list processes to find its bus waiter", func(t *testing.T, uhome string) {
			liveClaude(t, uhome, os.Getpid(), "crius · ms", "s-live")
			storeResumes(t, uhome, map[string]string{"th-ms": "s-stored"})
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			home, log := setup(t)
			roles(t, home)
			fake := fakeT3(t, home)
			uhome := fakeUserHome(t)
			c.set(t, uhome)
			rc, errs := post(t, at("2026-11-17T19:11"), log, "DONE", "crius:dev", "crius:ms", "x")
			if calls := readOr(t, filepath.Join(fake, "calls")); rc != 0 || calls != "thread send th-ms -\n" {
				t.Fatalf("rc %d, t3 calls %q: want delivered anyway\n%s", rc, calls, errs)
			}
			if !strings.Contains(errs, c.want) || !strings.Contains(errs, "delivered anyway") ||
				!strings.Contains(errs, "delivered to crius:ms (thread th-ms)") {
				t.Errorf("stderr lacks %q, \"delivered anyway\" or the delivery:\n%s", c.want, errs)
			}
		})
	}
}
