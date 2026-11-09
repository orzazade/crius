package quota

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// A push longer than a bus line (300 runes, store.MaxLen) is cut at the end. Every variable part of a push is
// bounded, so even at the largest values (long names, $ in billions, Max $ unpriced) each push
// fits and keeps what it must say, with no digit cut off a $ and no NaN.
func TestPushesFitABusLine(t *testing.T) {
	now := time.Date(2026, 10, 18, 9, 41, 0, 0, time.Local)
	long := strings.Repeat("x", 200)
	for _, c := range []struct {
		max, ent, unknown float64
		ent2, share       string
	}{
		{9999999, 9999999, 9999999, "Enterprise this week ≥ $10.0M + unknown $10.0M", "xxxxxxxxx… 100%"},
		{1e9, 0, 1e9, "Enterprise this week unknown $1.0B", "xxxxxxxxx… 100%"},
		{1e9, 1e9, 0, "Enterprise this week $1.0B", "xxxxxxxxx… 100%"},
		{0, 1e15, 1e15, "Enterprise this week ≥ >$999T + unknown >$999T", "xxxxxxxxx…"}, // all Max requests unpriced
	} {
		usd := c.max
		r := &Report{Now: now, Reset: now.Add(4*24*time.Hour + 19*time.Hour), SevenPct: 99.4, Day: 3, Passed: 0.31,
			AvgPerDay: 9.7, MaxUSD: c.max, UnknownUSD: c.unknown, EntWeekUSD: c.ent,
			Shares: []Share{{long + "a", usd}, {long + "b", 0}}, EntWeek: []Share{{long, c.ent}}}
		for name, p := range map[string]struct {
			text string
			must []string
		}{
			"pace":    {r.PaceText(), []string{"quota pace high: 7d 99% on day 3", "faster than the 7-day average 9.7%/day", "top burners: " + c.share, "suggestion: slow xxxxxxxxx…", c.ent2}},
			"runout":  {r.RunoutText(), []string{"quota runs out ~", "7d 99% day 3", "(7-day avg 9.7)", "top burner " + c.share + ", slow it", "at the limit use the next Max account, never the enterprise account; all Max spent means all work stops until a reset", c.ent2}},
			"used up": {r.UsedUpText(), []string{"quota used up: 7d 99%", "Move its sessions to the next Max account (soonest reset under 95%), never the enterprise account; all Max spent means all work stops until a reset.", c.ent2}},
		} {
			if n := utf8.RuneCountInString(p.text); n > 300 || strings.Contains(p.text, "NaN") || strings.Contains(p.text, "may continue") ||
				strings.Contains(p.text, "Appxite work") {
				t.Errorf("%s push is %d runes: %s", name, n, p.text)
			}
			for _, m := range p.must {
				if !strings.Contains(p.text, m) {
					t.Errorf("%s push lacks %q: %s", name, m, p.text)
				}
			}
		}
	}
}

// The runout push fires from day 2 with the week under 100% (RunsOut), so its widest figures are a week that prints as
// "100", %/day and the 7-day average as "100.0" (day 2 has passed more than a seventh of the week), a burner at 100%
// and 7-rune $ ("$999.9k"; review of the Max-only fix: it was 302 runes and cut the unknown $).
func TestRunoutFitsABusLineAtItsWidest(t *testing.T) {
	now := time.Date(2026, 10, 18, 9, 41, 0, 0, time.Local)
	long := strings.Repeat("x", 200)
	r := &Report{Now: now, Reset: now.Add(5*24*time.Hour - time.Minute), SevenPct: 99.99, Day: 2, Passed: 1.0001 / 7,
		AvgPerDay: 99.96, MaxUSD: 999949, UnknownUSD: 999949, EntWeekUSD: 999949,
		Shares: []Share{{long, 999949}}, EntWeek: []Share{{long, 999949}}}
	if !r.RunsOut() {
		t.Fatalf("the widest case must be one where the runout push fires")
	}
	txt := r.RunoutText()
	for _, m := range []string{"7d 100% day 2, 100.0%/day (7-day avg 100.0)", "top burner xxxxxxxxx… 100%, slow it", MaxOnly,
		"Enterprise this week ≥ $999.9k + unknown $999.9k."} {
		if !strings.Contains(txt, m) {
			t.Errorf("runout push lacks %q: %s", m, txt)
		}
	}
	if n := utf8.RuneCountInString(txt); n > 300 {
		t.Errorf("runout push is %d runes (a bus line holds 300): %s", n, txt)
	}
}

// The guard and stale alerts are bus lines too: a long project, tag or persona name cannot push out what to do.
func TestGuardAndStaleAlertsFitABusLine(t *testing.T) {
	long := strings.Repeat("y", 200)
	for _, c := range []struct{ text, must string }{
		{Violation{Session: long, Project: long, Kind: "s1", Source: long}.AlertText(), "Stop it or move it to the Max account."},
		{Violation{Session: long, Kind: "unmapped", Source: long}.AlertText(), "never the enterprise account (owner 11-07): stop it or move it to a Max account."},
		{Violation{Session: long, Project: long, Kind: "s1", Source: long, Ended: time.Now()}.AlertText(), "Run this project on the Max account."},
		{Violation{Session: long, Kind: "unmapped", Source: long, Ended: time.Now()}.AlertText(), "never the enterprise account (owner 11-07): run its work on a Max account."},
		{Violation{Session: long, Project: long, Kind: "mismatch", Source: "tag " + long + ", statusline max"}.AlertText(), "account mismatch in session yyyyyyyy"},
		{StaleText(long + ": no statusline line yet"), "is crius-statusline in that persona's statusLine and working?"},
	} {
		if n := utf8.RuneCountInString(c.text); n > 300 || !strings.Contains(c.text, c.must) {
			t.Errorf("%d runes, want %q in: %s", n, c.must, c.text)
		}
	}
}

// Codex round 10: only the alert is clipped; the `quota` report names the project in full.
func TestReportNamesViolationsInFull(t *testing.T) {
	long := strings.Repeat("y", 200)
	if txt := (Violation{Session: "s", Project: long, Kind: "s1", Source: "statusline"}).Text(); !strings.Contains(txt, long) {
		t.Fatal(txt)
	}
}
