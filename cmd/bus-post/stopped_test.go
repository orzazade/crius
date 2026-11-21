package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/orzazade/crius/internal/testutil"
)

// stoppedBus gives a quotes bus log holding lines; it returns the log path.
func stoppedBus(t *testing.T, lines ...string) string {
	_, home := testutil.Temp(t)
	log := filepath.Join(home, "bus", "quotes.log")
	testutil.Write(t, log, strings.Join(append(lines, ""), "\n"))
	return log
}

// 11-21: three DECISIONs went to quotes:lead, whose last line was a STOPPED of 11-11; nobody acted on them for 8 h and
// bus-post said nothing.
func TestALineToAStoppedRoleIsPostedAndWarned(t *testing.T) {
	log := stoppedBus(t,
		"2026-11-11T01:20 ANSWER quotes:lead -> quotes:qa — yes",
		"2026-11-11T01:25 STOPPED quotes:lead -> crius:lead — clean, bus waiter stopped.")
	rc, errs := post(t, at("2026-11-21T03:01"), log, "DECISION", "crius:lead", "quotes:lead", "heavy slots 1 -> 2")
	if rc != 0 || !strings.Contains(busText(t, log), "DECISION crius:lead -> quotes:lead — heavy slots") {
		t.Fatal(rc, errs, busText(t, log))
	}
	if !strings.Contains(errs, "quotes:lead is STOPPED (its last line, 2026-11-11T01:25, is a STOPPED)") {
		t.Fatalf("no STOPPED warning: %q", errs)
	}
}

// 11-11 17:18: quotes:dev, quotes:builder-2 and quotes:qa stopped with an INFO whose text opens with STOPPED.
func TestAnInfoThatOpensWithStoppedCounts(t *testing.T) {
	log := stoppedBus(t, "2026-11-21T02:00 INFO quotes:qa -> crius:lead — STOPPED. 8090 PASS; next steps in QA-STATE.md.")
	rc, errs := post(t, at("2026-11-21T03:01"), log, "ASK", "crius:lead", "quotes:qa", "walk 8091?")
	if (rc != 0 && rc != rcNoSession) || !strings.Contains(errs, "quotes:qa is STOPPED (its last line, 2026-11-21T02:00") {
		t.Fatal(rc, errs)
	}
}

func TestARoleSilentForOver24hIsWarned(t *testing.T) {
	log := stoppedBus(t, "2026-11-20T01:00 DONE quotes:dev -> crius:lead — 8094 pushed")
	rc, errs := post(t, at("2026-11-21T07:30"), log, "DECISION", "crius:lead", "quotes:dev", "take 8100")
	if rc != 0 || !strings.Contains(errs, "quotes:dev has posted no line for 30 h (its last line is 2026-11-20T01:00)") {
		t.Fatal(rc, errs)
	}
}

func TestARoleThatPostedAfterItsStoppedOrWithin24hIsQuiet(t *testing.T) {
	log := stoppedBus(t,
		"2026-11-20T01:25 STOPPED quotes:lead -> crius:lead — clean",
		"2026-11-20T09:00 RESUMED quotes:lead -> crius:lead — back",
		"2026-11-21T03:00 INFO quotes:qa -> crius:lead — walking 8091")
	rc, errs := post(t, at("2026-11-21T03:01"), log, "DECISION", "crius:lead", "quotes:lead,quotes:qa", "x")
	if rc != 0 || errs != "" {
		t.Fatal(rc, errs)
	}
}

// A to list is each of its names: a bare name is a role on the bus's own project, a cc'd name counts, and a whole team
// or cc is no one.
func TestEachNameInAToListIsChecked(t *testing.T) {
	log := stoppedBus(t,
		"2026-11-11T01:25 STOPPED quotes:lead -> crius:lead — clean",
		"2026-11-21T03:00 INFO quotes:qa -> crius:lead — walking",
		"2026-11-11T01:25 STOPPED quotes:fin -> crius:lead — clean")
	rc, errs := post(t, at("2026-11-21T03:01"), log, "DECISION", "crius:lead", "lead, qa, quotes:all (cc quotes:fin)", "x")
	if rc != 0 || !strings.Contains(errs, "quotes:lead is STOPPED") || !strings.Contains(errs, "quotes:fin is STOPPED") ||
		strings.Contains(errs, "quotes:qa") || strings.Contains(errs, "quotes:all") {
		t.Fatal(rc, errs)
	}
}

// A role posts on its own project's bus too: a line to quotes:lead on the cortex bus reads quotes.log as well.
func TestARolesOwnProjectLogCounts(t *testing.T) {
	quotes := stoppedBus(t, "2026-11-11T01:25 STOPPED quotes:lead -> crius:lead — clean")
	cortex := filepath.Join(filepath.Dir(quotes), "cortex.log")
	testutil.Write(t, cortex, "2026-11-21T02:00 INFO cortex:lead -> crius:lead — up\n")
	rc, errs := post(t, at("2026-11-21T03:01"), cortex, "DECISION", "crius:lead", "quotes:lead,cortex:lead", "x")
	if rc != 0 || !strings.Contains(errs, "quotes:lead is STOPPED") || strings.Contains(errs, "cortex:lead") {
		t.Fatal(rc, errs)
	}
	// and a later line of it on another bus is a sign of life
	testutil.Write(t, cortex, busText(t, cortex)+"2026-11-21T02:30 INFO quotes:lead -> cortex:lead — back\n")
	if rc, errs := post(t, at("2026-11-21T03:02"), cortex, "DECISION", "crius:lead", "quotes:lead", "y"); rc != 0 || errs != "" {
		t.Fatal(rc, errs)
	}
}

// Crius reads every bus and posts on few of them: its silence on one bus is no stop. A role with no line on the bus yet
// is a new one, not a stopped one.
func TestCriusSilenceAndANewRoleAreQuiet(t *testing.T) {
	log := stoppedBus(t, "2026-11-01T10:00 DECISION crius:lead -> quotes:lead — go")
	rc, errs := post(t, at("2026-11-21T03:01"), log, "DONE", "quotes:new", "crius,quotes:other", "x")
	if rc != 0 || errs != "" {
		t.Fatal(rc, errs)
	}
}
