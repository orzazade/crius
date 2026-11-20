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

// Review P2-1: a line another writer adds right after this one must not make bus-post read its own line as the first.
func TestALineAddedAfterThePostIsNotTakenForTheFirst(t *testing.T) {
	home, _ := setup(t)
	log := filepath.Join(home, "bus", "axi.log")
	mine := "2026-11-21T03:30 CORRECTED axi:ms-flow -> crius:lead — y; cause: stale context"
	testutil.Write(t, log, mine+"\n2026-11-21T03:30 INFO axi:qa -> crius:lead — someone else\n")
	if first, err := sameCause(log, mine, "stale context", at("2026-11-21T03:30")); err != nil || first != "" {
		t.Fatalf("first %q, err %v", first, err)
	}
	testutil.Write(t, log, "2026-11-21T01:00Z CORRECTED axi:qa -> crius:lead — x; cause: stale context\n"+mine+"\n")
	if first, err := sameCause(log, mine, "stale context", at("2026-11-21T03:30")); err != nil || first != "2026-11-21T01:00Z axi:qa" {
		t.Fatalf("a Z stamp: first %q, err %v", first, err)
	}
}

// Review P2-2: a long CORRECTED is posted cut; a cause only in the cut-off part would leave the bus line without it.
func TestALongCorrectedMustKeepItsCauseOnTheBusLine(t *testing.T) {
	_, log := setup(t)
	long := strings.Repeat("the old line named the wrong tree and the wrong sha ", 8)
	rc, errs := post(t, at("2026-11-21T03:30"), log, "CORRECTED", "axi:ms-flow", "crius:lead", long+"; cause: stale context")
	if rc != 2 || !strings.Contains(errs, "near the start") || busText(t, log) != "" {
		t.Fatalf("rc %d, %q", rc, errs)
	}
	if rc, errs := post(t, at("2026-11-21T03:30"), log, "CORRECTED", "axi:ms-flow", "crius:lead",
		"cause: stale context; "+long); rc != 0 || !strings.Contains(busText(t, log), "cause: stale context") {
		t.Fatalf("rc %d, %q", rc, errs)
	}
}

func TestAPluralCauseCounts(t *testing.T) {
	if c := causeOf("cause: missing tests"); c != "missing test" {
		t.Fatalf("%q", c)
	}
}
