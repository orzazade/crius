package quota

import (
	"testing"
	"time"
)

func spoolLine(ts time.Time, kind, login string, seven float64, reset time.Time, responded bool) Line {
	r := reset.Unix()
	return Line{TS: ts.UTC().Format(time.RFC3339), Kind: kind, Login: login, RL: true, SevenPct: &seven, SevenReset: &r, Responded: &responded}
}

// The 11-15 case: max1 was measured at 100% on Nov 15; an idle session's heartbeats then kept repeating an old 12%. The
// window's share is its highest reading and its newest measurement is the 100% line, never the later heartbeat.
func TestWeeksKeepTheHighestShareOfTheWindowAndItsNewestMeasurement(t *testing.T) {
	now := time.Date(2026, 11, 16, 20, 0, 0, 0, time.UTC)
	reset := time.Date(2026, 11, 18, 22, 0, 0, 0, time.UTC)
	full := time.Date(2026, 11, 15, 13, 36, 0, 0, time.UTC)
	lines := []Line{
		spoolLine(full.Add(-2*time.Hour), "change", "max1hash0000", 74, reset, true),
		spoolLine(full, "change", "max1hash0000", 100, reset, true),
		spoolLine(now.Add(-10*time.Minute), "heartbeat", "max1hash0000", 12, reset, false),
		// another login whose only window has ended: no current share
		spoolLine(now.Add(-80*time.Hour), "change", "max3hash0000", 62, now.Add(-58*time.Hour), true),
		// a line with no rate limits measures nothing
		{TS: now.Format(time.RFC3339), Kind: "change", Login: "apihash00000"},
	}
	w := Weeks(lines, now)
	m1, ok := w["max1hash0000"]
	if !ok || m1.SevenPct != 100 || !m1.Reset.Equal(reset) || !m1.Measured.Equal(full) || !m1.Seen.Equal(now.Add(-10*time.Minute)) {
		t.Fatalf("max1: %+v", m1)
	}
	m3 := w["max3hash0000"]
	if !m3.Reset.IsZero() || m3.Ended.IsZero() || m3.SevenPct != 0 {
		t.Fatalf("max3: %+v", m3)
	}
	if _, ok := w["apihash00000"]; ok {
		t.Fatal("a line with no rate limits made a window")
	}
}

// ThinkPad 11-16: ~/.claude-max2 was signed into max3 under a running session. That idle session's heartbeats kept
// max2's window (13%, the later reset) under max3's login. The window is the one max3's own measurements show: 34%.
func TestWeeksIgnoreAnotherLoginsWindowInARepeatedLine(t *testing.T) {
	now := time.Date(2026, 11, 16, 20, 0, 0, 0, time.UTC)
	own := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	other := time.Date(2026, 11, 22, 5, 0, 0, 0, time.UTC)
	lines := []Line{
		spoolLine(now.Add(-2*time.Hour), "heartbeat", "max3hash0000", 13, other, false),
		spoolLine(now.Add(-11*time.Minute), "change", "max3hash0000", 34, own, true),
		spoolLine(now.Add(-time.Minute), "session", "max3hash0000", 13, other, false),
	}
	w := Weeks(lines, now)["max3hash0000"]
	if w.SevenPct != 34 || !w.Reset.Equal(own) || !w.Measured.Equal(now.Add(-11*time.Minute)) {
		t.Fatalf("%+v", w)
	}
}
