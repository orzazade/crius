package quota

import (
	"strings"
	"testing"
	"time"
)

// Crius 22:36/22:43/22:44 (owner): Max has room until full on a fresh sample, under 90% on a stale one; a 5h window
// that has reset counts as 0 (Build already sets FivePct 0 then); 7d at 100% is full at any age; no sample, an ended
// week, or a stale share of 90-99% is unknown.
func TestRoom(t *testing.T) {
	now := time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)
	reset := time.Date(2026, 10, 21, 22, 0, 0, 0, time.UTC)
	five := now.Add(2 * time.Hour)
	for _, c := range []struct {
		name     string
		r        Report
		verdict  string
		inReason string
	}{
		{"fresh under 100", Report{LastSample: now.Add(-10 * time.Minute), SevenPct: 99, FivePct: 98, Reset: reset, FiveReset: five}, "room", "7d 99%, 5h 98%, reset "},
		{"fresh 7d full", Report{LastSample: now.Add(-time.Minute), SevenPct: 100, FivePct: 5, Reset: reset, FiveReset: five}, "full", "7d 100%"},
		{"fresh 5h full", Report{LastSample: now.Add(-time.Minute), SevenPct: 40, FivePct: 100, Reset: reset, FiveReset: five}, "full", "5h 100%"},
		{"stale under 90", Report{LastSample: now.Add(-3 * time.Hour), SevenPct: 89, FivePct: 0, Reset: reset}, "room", "sample 3 h old (under 90%)"},
		{"stale 5h 95", Report{LastSample: now.Add(-50 * time.Minute), SevenPct: 40, FivePct: 95, Reset: reset, FiveReset: five}, "unknown", "5h 95%"},
		{"stale 7d 90", Report{LastSample: now.Add(-50 * time.Minute), SevenPct: 90, Reset: reset}, "unknown", "50 min old"},
		{"stale 7d full", Report{LastSample: now.Add(-5 * time.Hour), SevenPct: 100, Reset: reset}, "full", "7d 100%"},
		{"stale 5h full unexpired", Report{LastSample: now.Add(-time.Hour), SevenPct: 40, FivePct: 100, Reset: reset, FiveReset: five}, "full", "5h 100%"},
		{"no sample", Report{}, "unknown", "no quota sample"},
		{"week ended", Report{LastSample: now.Add(-20 * time.Hour), Ended: now.Add(-time.Hour)}, "unknown", "week ended"},
	} {
		c.r.Now = now
		v, reason := Room(&c.r)
		if v != c.verdict || !strings.Contains(reason, c.inReason) || len(reason) > 100 || strings.Contains(reason, "\n") {
			t.Errorf("%s: %s %q", c.name, v, reason)
		}
	}
}
