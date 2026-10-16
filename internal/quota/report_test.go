package quota

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// A push longer than a bus line (300 runes, store.MaxLen) is cut at the end. Every variable part of a push is
// bounded, so even at the largest values (long names, many Appxite projects, $ in billions, Max $ unpriced) each push
// fits and keeps what it must say, with no digit cut off a $ and no NaN.
func TestPushesFitABusLine(t *testing.T) {
	now := time.Date(2026, 10, 18, 9, 41, 0, 0, time.Local)
	long := strings.Repeat("x", 200)
	ok := map[string]bool{}
	for i := 0; i < 20; i++ {
		ok[fmt.Sprintf("%s%02d", long, i)] = true
	}
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
			"runout":  {r.RunoutText(ok), []string{"quota runs out ~", "7d 99% day 3", "(7-day avg 9.7)", "top burner " + c.share + ", slow it", "only Appxite work (xxxxxxxxx…, xxxxxxxxx…, +18 more) may continue, on enterprise", "personal/scifi work waits until", c.ent2}},
			"used up": {r.UsedUpText(ok), []string{"quota used up: 7d 99%", "Only Appxite work (xxxxxxxxx…, xxxxxxxxx…, +18 more) may continue, on the Appxite enterprise account", "personal and scifi work waits", c.ent2}},
		} {
			if n := utf8.RuneCountInString(p.text); n > 300 || strings.Contains(p.text, "NaN") {
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

// The guard and stale alerts are bus lines too: a long project, tag or persona name cannot push out what to do.
func TestGuardAndStaleAlertsFitABusLine(t *testing.T) {
	long := strings.Repeat("y", 200)
	for _, c := range []struct{ text, must string }{
		{Violation{Session: long, Project: long, Kind: "s1", Source: long}.Text(), "Stop it or move it to the Max account."},
		{Violation{Session: long, Kind: "unmapped", Source: long}.Text(), "or stop it if it is not Appxite work."},
		{Violation{Session: long, Project: long, Kind: "mismatch", Source: "tag " + long + ", statusline max"}.Text(), "account mismatch in session yyyyyyyy"},
		{StaleText(long + ": no statusline line yet"), "is crius-statusline in that persona's statusLine and working?"},
	} {
		if n := utf8.RuneCountInString(c.text); n > 300 || !strings.Contains(c.text, c.must) {
			t.Errorf("%d runes, want %q in: %s", n, c.must, c.text)
		}
	}
}
