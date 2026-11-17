package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orzazade/crius/internal/testutil"
)

// No test reaches the real T3 Code CLI or names the thread it runs in: the default t3 is a path that does not exist,
// so a test with no fake fails under that name, and T3CODE_THREAD_ID is the test's own to set.
func TestMain(m *testing.M) {
	t3CLI = filepath.Join(os.TempDir(), "bus-post-test-no-real-t3")
	os.Unsetenv("T3CODE_THREAD_ID")
	// No test lists this Mac's live claudes or reads its T3 store: the user home of the look is an empty folder.
	empty, err := os.MkdirTemp("", "bus-post-test-home")
	if err != nil {
		panic(err)
	}
	userHome = func() (string, error) { return empty, nil }
	procList = func() (string, error) { return "", errors.New("a test gives its own process list") }
	code := m.Run()
	os.RemoveAll(empty)
	os.Exit(code)
}

// fakeT3 puts a t3 in <home>/bin, where bus-post looks first, that keeps each call's arguments in dir/calls and its
// stdin in dir/stdin (one block per call), and exits 1 printing dir/fail when that file is there.
func fakeT3(t *testing.T, home string) (dir string) {
	t.Helper()
	dir = t.TempDir()
	testutil.Write(t, filepath.Join(home, "bin", "t3"), "#!/bin/sh\n"+
		"d="+dir+"\n"+
		"echo \"$*\" >> \"$d/calls\"\n"+
		"{ cat; echo; echo '--'; } >> \"$d/stdin\"\n"+
		"if [ -f \"$d/fail\" ]; then cat \"$d/fail\" >&2; exit 1; fi\n")
	if err := os.Chmod(filepath.Join(home, "bin", "t3"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func readOr(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return string(b)
}

// roles files crius:lead (Crius's own thread), crius:build (a cmux tab), and crius:qa, crius:ms and crius:dev as T3
// threads th-qa, th-ms and th-dev. As crius-hook leaves them for a session that runs on, qa's and ms's role files name no
// thread and their session files do; dev's session file is gone, so its role file's thread counts. Crius's own thread
// file names th-crius.
func roles(t *testing.T, home string) {
	t.Helper()
	dir := filepath.Join(home, "roles")
	testutil.Write(t, filepath.Join(dir, "crius.lead.json"), `{"session_id":"c","project":"crius","role":"lead","t3_thread":"th-crius"}`)
	testutil.Write(t, filepath.Join(dir, "crius.build.json"), `{"session_id":"b","project":"crius","role":"build","workspace":"ws","surface":"sf"}`)
	for _, r := range []string{"qa", "ms"} {
		testutil.Write(t, filepath.Join(dir, "crius."+r+".json"), `{"session_id":"s-`+r+`","project":"crius","role":"`+r+`"}`)
		testutil.Write(t, filepath.Join(home, "sessions", "s-"+r+".json"), `{"session_id":"s-`+r+`","t3_thread":"th-`+r+`"}`)
	}
	testutil.Write(t, filepath.Join(dir, "crius.dev.json"), `{"session_id":"s-dev","project":"crius","role":"dev","t3_thread":"th-dev"}`)
	testutil.Write(t, filepath.Join(home, "state", "crius", "t3-thread"), "th-crius\n")
}

// 11-15: AXI qa sat idle 20 min with a NEED addressed to it, and api-split did not get qa's FAIL of 04:07; Crius relayed
// bus lines into threads by hand 22 times in 10 days. A line that asks a thread role to act now goes to its thread too,
// with `t3 thread send <id> -` and the line on stdin.
func TestALineToAThreadRoleIsDeliveredToTheThread(t *testing.T) {
	home, log := setup(t)
	roles(t, home)
	fake := fakeT3(t, home)
	rc, errs := post(t, at("2026-11-15T04:07"), log, "FAIL", "crius:lead", "crius:qa, crius:build", "F1 smoke: 2 red")
	want := "2026-11-15T04:07 FAIL crius:lead -> crius:qa, crius:build — F1 smoke: 2 red"
	if rc != 0 || busText(t, log) != want+"\n" {
		t.Fatalf("rc %d: %s\nbus: %q", rc, errs, busText(t, log))
	}
	if calls := readOr(t, filepath.Join(fake, "calls")); calls != "thread send th-qa -\n" {
		t.Fatalf("t3 calls %q", calls)
	}
	if in := readOr(t, filepath.Join(fake, "stdin")); !strings.HasPrefix(in, want+"\n") || !strings.Contains(in, log) {
		t.Errorf("stdin %q: want the line, then the bus it is on", in)
	}
	if !strings.Contains(errs, "delivered to crius:qa (thread th-qa)") || strings.Contains(errs, "send it to the thread too") ||
		strings.Contains(errs, "crius:build") {
		t.Errorf("stderr %q", errs)
	}
}

// Every type that asks the reader to act is delivered; an INFO, ACK or START is not.
func TestOnlyTypesThatAskToActAreDelivered(t *testing.T) {
	home, log := setup(t)
	roles(t, home)
	fake := fakeT3(t, home)
	for i, typ := range []string{"DONE", "NEED", "ASK", "FAIL", "PASS", "DECISION", "CORRECTION", "CORRECTED", "ANSWER",
		"ORDER", "OWNER", "READY", "GO", "PAUSE"} {
		os.Remove(filepath.Join(fake, "calls"))
		rc, errs := post(t, at("2026-11-15T04:10"), log, typ, "crius:dev", "crius:qa", "x "+string(rune('a'+i)))
		if rc != 0 || readOr(t, filepath.Join(fake, "calls")) != "thread send th-qa -\n" {
			t.Errorf("%s: rc %d, calls %q: %s", typ, rc, readOr(t, filepath.Join(fake, "calls")), errs)
		}
	}
	os.Remove(filepath.Join(fake, "calls"))
	for _, typ := range []string{"INFO", "ACK", "START"} {
		if rc, errs := post(t, at("2026-11-15T04:11"), log, typ, "crius:dev", "crius:qa", "y"); rc != 0 || errs != "" {
			t.Errorf("%s: rc %d: %q", typ, rc, errs)
		}
	}
	if calls := readOr(t, filepath.Join(fake, "calls")); calls != "" {
		t.Errorf("INFO, ACK and START went to the thread: %q", calls)
	}
}

// The poster's own thread never gets its own line, by role or by the thread it runs in (T3CODE_THREAD_ID), and
// Crius's thread never gets one from this path: crius:lead has its own waiter.
func TestThePostersOwnThreadAndCriusAreSkipped(t *testing.T) {
	home, log := setup(t)
	roles(t, home)
	fake := fakeT3(t, home)
	t.Setenv("T3CODE_THREAD_ID", "th-qa") // a qa thread posting under the dev role
	testutil.Write(t, filepath.Join(home, "roles", "crius.ops.json"), `{"session_id":"o","project":"crius","role":"ops","t3_thread":"th-crius"}`)
	rc, errs := post(t, at("2026-11-15T04:12"), log, "DONE", "crius:dev", "crius:dev, crius:qa, crius:ms, crius, crius:ops", "z")
	if calls := readOr(t, filepath.Join(fake, "calls")); rc != 0 || calls != "thread send th-ms -\n" {
		t.Fatalf("rc %d, calls %q: %s", rc, calls, errs)
	}
}

// A t3 that fails is loud: the line stays on the bus (it is the record), stderr says who did not get it and why, and
// bus-post exits 8, so the sender sends it by hand. A filed thread id that is no id (it would be read as a flag) is not
// sent and fails the same way.
func TestAFailedDeliveryExitsEightWithTheLinePosted(t *testing.T) {
	home, log := setup(t)
	roles(t, home)
	fake := fakeT3(t, home)
	testutil.Write(t, filepath.Join(fake, "fail"), "Thread th-qa is no longer available.\n")
	rc, errs := post(t, at("2026-11-15T04:13"), log, "NEED", "crius:dev", "crius:qa, crius:ms", "qa: F1")
	if rc != rcNotDelivered || !strings.Contains(busText(t, log), "NEED crius:dev -> crius:qa, crius:ms — qa: F1") ||
		!strings.Contains(errs, "not delivered to crius:qa (thread th-qa)") || !strings.Contains(errs, "no longer available") {
		t.Fatalf("rc %d: %s\nbus: %q", rc, errs, busText(t, log))
	}
	if calls := readOr(t, filepath.Join(fake, "calls")); strings.Count(calls, "thread send") != 2 {
		t.Errorf("want one call per thread, got %q", calls)
	}

	os.Remove(filepath.Join(fake, "fail"))
	os.Remove(filepath.Join(fake, "calls"))
	testutil.Write(t, filepath.Join(home, "roles", "crius.odd.json"), `{"session_id":"o","project":"crius","role":"odd","t3_thread":"--wait"}`)
	rc, errs = post(t, at("2026-11-15T04:14"), log, "DONE", "crius:dev", "crius:odd", "x")
	if rc != rcNotDelivered || !strings.Contains(errs, `not delivered to crius:odd: its filed thread id "--wait" is no thread id`) ||
		readOr(t, filepath.Join(fake, "calls")) != "" {
		t.Fatalf("rc %d: %s", rc, errs)
	}
}

// Two roles in one thread get one send, and a failure names both.
func TestTwoRolesInOneThreadGetOneSend(t *testing.T) {
	home, log := setup(t)
	roles(t, home)
	fake := fakeT3(t, home)
	testutil.Write(t, filepath.Join(home, "roles", "crius.qa2.json"), `{"session_id":"s-qa","project":"crius","role":"qa2"}`)
	testutil.Write(t, filepath.Join(fake, "fail"), "down\n")
	rc, errs := post(t, at("2026-11-15T04:16"), log, "PASS", "crius:dev", "crius:qa, crius:qa2", "x")
	if calls := readOr(t, filepath.Join(fake, "calls")); rc != rcNotDelivered || calls != "thread send th-qa -\n" ||
		!strings.Contains(errs, "not delivered to crius:qa, crius:qa2 (thread th-qa)") {
		t.Fatalf("rc %d, calls %q: %s", rc, calls, errs)
	}
}

// A role with no tab and no thread (a session outside cmux and T3 Code, or one that has run no hook since the hook
// began to file threads) is not known to be a thread: posted, said as before, exit 0.
func TestANoTabRoleWithNoThreadIsSaidNotFailed(t *testing.T) {
	home, log := setup(t)
	roles(t, home)
	fake := fakeT3(t, home)
	testutil.Write(t, filepath.Join(home, "roles", "crius.old.json"), `{"session_id":"o","project":"crius","role":"old"}`)
	rc, errs := post(t, at("2026-11-15T04:17"), log, "DONE", "crius:dev", "crius:old", "x")
	if rc != 0 || !strings.Contains(errs, "crius:old has no tab and no T3 Code thread filed") ||
		readOr(t, filepath.Join(fake, "calls")) != "" {
		t.Fatalf("rc %d: %s", rc, errs)
	}
}

// A copy asks nothing of its reader: a thread named only after "(cc" or "cc" gets no send.
func TestACopiedThreadIsNotDelivered(t *testing.T) {
	home, log := setup(t)
	roles(t, home)
	fake := fakeT3(t, home)
	for _, to := range []string{"crius:build (cc crius:qa)", "crius:build, cc crius:qa"} {
		if rc, errs := post(t, at("2026-11-15T04:18"), log, "DONE", "crius:dev", to, "x"); rc != 0 || errs != "" {
			t.Errorf("%s: rc %d: %q", to, rc, errs)
		}
	}
	if calls := readOr(t, filepath.Join(fake, "calls")); calls != "" {
		t.Errorf("calls %q", calls)
	}
}

// A role with a cmux tab, a role with no file and a whole team are left as they were: no t3 call, nothing said.
func TestTabRolesAreUnchanged(t *testing.T) {
	home, log := setup(t)
	roles(t, home)
	fake := fakeT3(t, home)
	if rc, errs := post(t, at("2026-11-15T04:15"), log, "DONE", "crius:dev", "crius:build, crius:nobody, crius:all", "x"); rc != 0 || errs != "" {
		t.Fatalf("rc %d: %q", rc, errs)
	}
	if calls := readOr(t, filepath.Join(fake, "calls")); calls != "" {
		t.Errorf("calls %q", calls)
	}
}
