package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/testutil"
)

// setup gives a home with an empty bus log and a runs folder; it returns the log path.
func setup(t *testing.T) (home, log string) {
	_, home = testutil.Temp(t)
	log = filepath.Join(home, "bus", "crius.log")
	testutil.Write(t, log, "")
	os.MkdirAll(filepath.Join(home, "runs"), 0o700)
	return home, log
}

func at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02T15:04", s, time.Local)
	if err != nil {
		panic(err)
	}
	return t
}

func post(t *testing.T, now time.Time, args ...string) (int, string) {
	t.Helper()
	var errb strings.Builder
	rc := run(args, now, &errb)
	return rc, errb.String()
}

func busText(t *testing.T, log string) string {
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestALineIsStampedWithTheLocalClock(t *testing.T) {
	_, log := setup(t)
	rc, errs := post(t, at("2026-10-21T22:54"), log, "START", "crius:build", "crius:lead", "worktree up")
	want := "2026-10-21T22:54 START crius:build -> crius:lead — worktree up\n"
	if rc != 0 || busText(t, log) != want {
		t.Fatal(rc, errs, busText(t, log))
	}
}

// 10-21 12:48: crius:lead's ANSWER went out stamped 08:48, the UTC time, four hours behind the local clock.
func TestAUTCStampIsRefused(t *testing.T) {
	_, log := setup(t)
	rc, errs := post(t, at("2026-10-21T12:48"), "--at", "2026-10-21T08:48", log, "ANSWER", "crius:lead", "quotes:lead", "x")
	if rc != 2 || !strings.Contains(errs, "off the clock") || busText(t, log) != "" {
		t.Fatal(rc, errs, busText(t, log))
	}
}

// 10-20 23:45: quotes:lead typed 23:45 by hand on lines written at about 23:38-23:44.
func TestAHandTypedTimeAheadIsRefused(t *testing.T) {
	_, log := setup(t)
	rc, errs := post(t, at("2026-10-20T23:40"), "--at", "2026-10-20T23:45", log, "INFO", "quotes:lead", "crius:lead", "x")
	if rc != 2 || !strings.Contains(errs, "off the clock") || busText(t, log) != "" {
		t.Fatal(rc, errs, busText(t, log))
	}
}

func TestAGivenTimeWithin2MinIsKept(t *testing.T) {
	_, log := setup(t)
	rc, errs := post(t, at("2026-10-21T22:56"), "--at", "2026-10-21T22:54", log, "INFO", "crius:build", "crius:lead", "x")
	if rc != 0 || !strings.HasPrefix(busText(t, log), "2026-10-21T22:54 INFO") {
		t.Fatal(rc, errs, busText(t, log))
	}
}

// 10-21 14:54: crius:lead posted VERIFIED 3622eda before re-running its tests; no run log existed.
func TestAVerifiedWithoutARunLogIsRefused(t *testing.T) {
	_, log := setup(t)
	rc, errs := post(t, at("2026-10-21T14:54"), log, "VERIFIED", "crius:lead", "quotes:lead",
		"3622eda B4 follow-ups · on main; seller half proven per my CORRECTED line")
	if rc != 2 || !strings.Contains(errs, "run log") || busText(t, log) != "" {
		t.Fatal(rc, errs, busText(t, log))
	}
}

// 10-21 12:08: the B0 VERIFIED said 'live on 620c99f', but the only run was clean-suite on 1ea27a8.
func TestAVerifiedWhoseRunLogHoldsAnotherShaIsRefused(t *testing.T) {
	home, log := setup(t)
	runLog := filepath.Join(home, "runs", "clean-suite-1ea27a8.log")
	testutil.Write(t, runLog, "sha: 1ea27a8\npass: 936\n")
	rc, errs := post(t, at("2026-10-21T12:00"), log, "VERIFIED", "crius:lead", "quotes:lead",
		"B0 · live on 620c99f; "+runLog)
	if rc != 2 || !strings.Contains(errs, "620c99f") || busText(t, log) != "" {
		t.Fatal(rc, errs, busText(t, log))
	}
}

func TestAVerifiedWithARunLogWithoutAPassCountIsRefused(t *testing.T) {
	home, log := setup(t)
	runLog := filepath.Join(home, "runs", "r.log")
	testutil.Write(t, runLog, "sha: 3622eda\npass: 0\n")
	rc, errs := post(t, at("2026-10-21T15:10"), log, "VERIFIED", "crius:lead", "quotes:lead", "3622eda · "+runLog)
	if rc != 2 || !strings.Contains(errs, "pass") || busText(t, log) != "" {
		t.Fatal(rc, errs, busText(t, log))
	}
}

// A run log outside ~/.crius/runs is not Crius's own: a builder's log proves nothing about Crius's check.
func TestAVerifiedNamingALogOutsideRunsIsRefused(t *testing.T) {
	_, log := setup(t)
	other := filepath.Join(t.TempDir(), "r.log")
	testutil.Write(t, other, "sha: 3622eda\npass: 12\n")
	rc, errs := post(t, at("2026-10-21T15:10"), log, "VERIFIED", "crius:lead", "quotes:lead", "3622eda · "+other)
	if rc != 2 || !strings.Contains(errs, "runs") || busText(t, log) != "" {
		t.Fatal(rc, errs, busText(t, log))
	}
}

func TestAVerifiedWithItsRunLogIsPosted(t *testing.T) {
	home, log := setup(t)
	runLog := filepath.Join(home, "runs", "3622eda.log")
	testutil.Write(t, runLog, "sha: 3622eda9f0\ncmd: npm test\npass: 12\n")
	text := "3622eda B4 follow-ups · npm test 12 pass, " + runLog
	rc, errs := post(t, at("2026-10-21T15:10"), log, "VERIFIED", "crius:lead", "quotes:lead", text)
	if rc != 0 || !strings.HasSuffix(busText(t, log), "— "+text+"\n") {
		t.Fatal(rc, errs, busText(t, log))
	}
}

// Crius 10-24 00:11, twice: 'log <path>. Next' was refused as naming no run log. The path kept the sentence's '.' and
// was dropped without a word. Punctuation that ends a sentence is not part of the path, and a named path that does not
// exist is said.
func TestARunLogPathAtTheEndOfASentenceCounts(t *testing.T) {
	home, log := setup(t)
	runLog := filepath.Join(home, "runs", "crius-94598c5-2301.log")
	testutil.Write(t, runLog, "sha: 94598c5\npass: 767\n")
	for _, end := range []string{". Next: install.", ", then install", "; next", ")", ":"} {
		text := "94598c5 · clean clone 767 pass (log " + runLog + end
		if rc, errs := post(t, at("2026-10-24T00:11"), log, "VERIFIED", "crius:lead", "crius:build", text); rc != 0 {
			t.Errorf("%q: rc %d: %s", text, rc, errs)
		}
	}
	missing := filepath.Join(home, "runs", "crius-94598c5-2350.log")
	rc, errs := post(t, at("2026-10-24T00:11"), log, "VERIFIED", "crius:lead", "crius:build", "94598c5 · log "+missing+".")
	if rc != 2 || !strings.Contains(errs, missing+" does not exist") {
		t.Errorf("a missing run log: want it named, got rc %d: %s", rc, errs)
	}
}

// A typo in the log path would start a bus nobody reads: silent success.
func TestAMissingLogIsRefused(t *testing.T) {
	home, _ := setup(t)
	missing := filepath.Join(home, "bus", "crius.lgo")
	rc, errs := post(t, at("2026-10-21T22:54"), missing, "INFO", "crius:build", "crius:lead", "x")
	if _, err := os.Stat(missing); rc != 2 || !os.IsNotExist(err) {
		t.Fatal(rc, errs, err)
	}
}

func TestAnUnknownTypeIsRefused(t *testing.T) {
	_, log := setup(t)
	rc, errs := post(t, at("2026-10-21T22:54"), log, "QUEUE", "crius:build", "crius:lead", "x") // team-only type
	if rc != 2 || busText(t, log) != "" {
		t.Fatal(rc, errs)
	}
}

// Review P2: a short sha in the run log must not certify a longer sha the run never tested.
func TestALongerShaThanTheRunLogsIsRefused(t *testing.T) {
	home, log := setup(t)
	runLog := filepath.Join(home, "runs", "r.log")
	testutil.Write(t, runLog, "sha: abcdef1\npass: 3\n")
	rc, errs := post(t, at("2026-10-21T15:10"), log, "VERIFIED", "crius:lead", "x:lead", "abcdef12 · "+runLog)
	if rc != 2 || busText(t, log) != "" {
		t.Fatal(rc, errs)
	}
}

// Review P2: "invalid~/…/r.log" names no file; the real r.log inside it must not count.
func TestAPathInsideAWordIsNotARunLog(t *testing.T) {
	home, log := setup(t)
	runLog := filepath.Join(home, "runs", "r.log")
	testutil.Write(t, runLog, "sha: 3622eda\npass: 3\n")
	rc, errs := post(t, at("2026-10-21T15:10"), log, "VERIFIED", "crius:lead", "x:lead", "3622eda · invalid"+runLog)
	if rc != 2 || busText(t, log) != "" {
		t.Fatal(rc, errs)
	}
}

// Review P2: a sha glued inside a non-ASCII word is not the sha the line names.
func TestAShaInsideAUnicodeWordDoesNotCount(t *testing.T) {
	home, log := setup(t)
	runLog := filepath.Join(home, "runs", "r.log")
	testutil.Write(t, runLog, "sha: 3622eda\npass: 3\n")
	rc, errs := post(t, at("2026-10-21T15:10"), log, "VERIFIED", "crius:lead", "x:lead", "620c99f · ç3622edaç "+runLog)
	if rc != 2 || busText(t, log) != "" {
		t.Fatal(rc, errs)
	}
}

// Review P2: at 12:00:59 a stamp of 11:58 is 179 s old, over the 2 min limit.
func TestTheLimitCountsSeconds(t *testing.T) {
	_, log := setup(t)
	rc, errs := post(t, at("2026-10-21T12:00").Add(59*time.Second), "--at", "2026-10-21T11:58", log, "INFO", "crius:build", "crius:lead", "x")
	if rc != 2 || busText(t, log) != "" {
		t.Fatal(rc, errs)
	}
}

// Review P2: a log whose last line has no newline would swallow the new line into it.
func TestALogEndingMidLineIsRefused(t *testing.T) {
	_, log := setup(t)
	testutil.Write(t, log, "2026-10-21T22:50 INFO crius:lead -> crius:build — half")
	rc, errs := post(t, at("2026-10-21T22:54"), log, "INFO", "crius:build", "crius:lead", "x")
	if rc != 2 || busText(t, log) != "2026-10-21T22:50 INFO crius:lead -> crius:build — half" {
		t.Fatal(rc, errs, busText(t, log))
	}
}

// Review P3: a relative CRIUS_HOME still finds the runs folder.
func TestARelativeHomeFindsItsRuns(t *testing.T) {
	home, log := setup(t)
	runLog := filepath.Join(home, "runs", "r.log")
	testutil.Write(t, runLog, "sha: 3622eda\npass: 3\n")
	t.Chdir(filepath.Dir(home))
	t.Setenv("CRIUS_HOME", filepath.Base(home))
	rc, errs := post(t, at("2026-10-21T15:10"), log, "VERIFIED", "crius:lead", "x:lead", "3622eda · "+runLog)
	if rc != 0 {
		t.Fatal(rc, errs)
	}
}

// Review round 2: a sha hidden in a prefixed path or an underscore word must not stand in for the sha the line
// names. Every sha the line names must be one its run log ran.
func TestEveryNamedShaMustBeTheRunsSha(t *testing.T) {
	home, log := setup(t)
	runLog := filepath.Join(home, "runs", "r.log")
	testutil.Write(t, runLog, "sha: 3622eda\npass: 3\n")
	for _, text := range []string{
		"620c99f path=" + filepath.Join(home, "runs", "3622eda.log") + " " + runLog,
		"620c99f not_3622eda_checked " + runLog,
		"3622eda and 620c99f " + runLog,
	} {
		rc, errs := post(t, at("2026-10-21T15:10"), log, "VERIFIED", "crius:lead", "x:lead", text)
		if rc != 2 || busText(t, log) != "" {
			t.Fatal(text, rc, errs)
		}
	}
}

// Crius 10-22 00:49: OWNER records the owner's own words relayed by Crius; bus-post posts it.
func TestAnOwnerLineIsPosted(t *testing.T) {
	_, log := setup(t)
	rc, errs := post(t, at("2026-10-22T01:30"), log, "OWNER", "crius:lead", "crius:build", "no Telegram")
	if want := "2026-10-22T01:30 OWNER crius:lead -> crius:build — no Telegram\n"; rc != 0 || busText(t, log) != want {
		t.Fatal(rc, errs, busText(t, log))
	}
}
