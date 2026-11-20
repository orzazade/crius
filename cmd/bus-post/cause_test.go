package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orzazade/crius/internal/testutil"
)

// 175 CORRECTED lines 11-01..11-21, 4 named a cause from RULES §2.5, so "same cause twice" (§2.6) was found by hand
// (11-21 02:50: axi:ms-flow's "Secret slip, 2nd one"). Harness2's watch put every correction under one cause and
// queued a fix when a cause repeated; here the poster names the cause, no model call.
func TestACorrectedWithNoCauseIsRefused(t *testing.T) {
	_, log := setup(t)
	rc, errs := post(t, at("2026-11-21T03:30"), log, "CORRECTED", "axi:ms-flow", "crius:lead", "my 02:40 line was wrong")
	if rc != 2 || !strings.Contains(errs, "cause: <one of") || !strings.Contains(errs, "stale context") ||
		busText(t, log) != "" {
		t.Fatalf("rc %d, %q, bus %q", rc, errs, busText(t, log))
	}
}

func TestACorrectedNamingACauseIsPosted(t *testing.T) {
	for _, text := range []string{"my 02:40 line was wrong; cause: stale context",
		"Cause: Harness or tool — the T3 CLI", "cause:missing test, no red test for it", "cause: tool (t3 0.1.9)"} {
		_, log := setup(t)
		if rc, errs := post(t, at("2026-11-21T03:30"), log, "CORRECTED", "axi:ms-flow", "crius:lead", text); rc != 0 ||
			strings.Contains(errs, "same cause twice") {
			t.Fatalf("%q: rc %d, %q", text, rc, errs)
		}
	}
}

func TestACauseWordOutsideTheListIsRefused(t *testing.T) {
	_, log := setup(t)
	if rc, errs := post(t, at("2026-11-21T03:30"), log, "CORRECTED", "axi:ms-flow", "crius:lead",
		"cause: sloppy"); rc != 2 || !strings.Contains(errs, "cause: <one of") {
		t.Fatalf("rc %d, %q", rc, errs)
	}
}

func TestTheSameCauseTwiceOnABusWakesCrius(t *testing.T) {
	home, _ := setup(t)
	log := filepath.Join(home, "bus", "axi.log")
	testutil.Write(t, log, "2026-11-20T10:00 CORRECTED axi:qa -> crius:lead — wrong tree; cause: Stale context\n"+
		"2026-11-20T11:00 CORRECTED axi:qa -> crius:lead — old line with no cause\n")
	rc, errs := post(t, at("2026-11-21T03:30"), log, "CORRECTED", "axi:ms-flow", "crius:lead",
		"secret slip 2nd one; cause: stale context")
	if rc != 0 || !strings.Contains(errs, "same cause twice") {
		t.Fatalf("rc %d, %q", rc, errs)
	}
	b, _ := os.ReadFile(filepath.Join(home, "bus", "crius.log"))
	alert := string(b)
	if !strings.Contains(alert, " ALERT crius:bus-post -> crius:lead — same cause twice on axi.log: stale context") ||
		!strings.Contains(alert, "2026-11-20T10:00 axi:qa") || !strings.Contains(alert, "RULES §2.6") {
		t.Fatalf("crius.log: %q", alert)
	}
}

func TestOtherCausesOtherBusesAndOldLinesAreNoRepeat(t *testing.T) {
	home, _ := setup(t)
	testutil.Write(t, filepath.Join(home, "bus", "quotes.log"),
		"2026-11-20T10:00 CORRECTED quotes:qa -> crius:lead — x; cause: stale context\n")
	log := filepath.Join(home, "bus", "axi.log")
	testutil.Write(t, log, "2026-11-20T10:00 CORRECTED axi:qa -> crius:lead — x; cause: bad data\n"+
		"2026-10-01T10:00 CORRECTED axi:qa -> crius:lead — x; cause: stale context\n")
	if rc, errs := post(t, at("2026-11-21T03:30"), log, "CORRECTED", "axi:ms-flow", "crius:lead",
		"y; cause: stale context"); rc != 0 || strings.Contains(errs, "same cause twice") {
		t.Fatalf("rc %d, %q", rc, errs)
	}
	if b, _ := os.ReadFile(filepath.Join(home, "bus", "crius.log")); len(b) != 0 {
		t.Fatalf("crius.log: %q", b)
	}
}
