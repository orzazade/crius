package quota

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// A push longer than a bus line (300 runes, store.MaxLen) is cut at the end. Every variable part of a push is
// bounded, so even at the largest values (long names, many Appxite projects, 7-digit $) each push fits and keeps
// what it must say.
func TestPushesFitABusLine(t *testing.T) {
	now := time.Date(2026, 10, 18, 9, 41, 0, 0, time.Local)
	long := strings.Repeat("x", 200)
	ok := map[string]bool{}
	for i := 0; i < 20; i++ {
		ok[fmt.Sprintf("%s%02d", long, i)] = true
	}
	r := &Report{Now: now, Reset: now.Add(4*24*time.Hour + 19*time.Hour), SevenPct: 99.4, Day: 3, Passed: 0.31,
		AvgPerDay: 9.7, MaxUSD: 9999999, UnknownUSD: 9999999, EntWeekUSD: 9999999,
		Shares: []Share{{long + "a", 9999998}, {long + "b", 1}}, EntWeek: []Share{{long, 9999999}}}
	ent := "Enterprise this week ≥ $9999999 + unknown $9999999"
	for name, c := range map[string]struct {
		text string
		must []string
	}{
		"pace":    {r.PaceText(), []string{"quota pace high: 7d 99% on day 3", "faster than the 7-day average 9.7%/day", "top burners: xxxxxxxxx…", "suggestion: slow xxxxxxxxx…", ent}},
		"runout":  {r.RunoutText(ok), []string{"quota runs out ~", "7d 99% day 3", "(7-day avg 9.7)", "top burner xxxxxxxxx… 100%, slow it", "only Appxite work (xxxxxxxxx…, xxxxxxxxx…, +18 more) may continue, on enterprise", "personal/scifi work waits until", ent}},
		"used up": {r.UsedUpText(ok), []string{"quota used up: 7d 99%", "Only Appxite work (xxxxxxxxx…, xxxxxxxxx…, +18 more) may continue, on the Appxite enterprise account", "personal and scifi work waits", ent}},
	} {
		if n := utf8.RuneCountInString(c.text); n > 300 {
			t.Errorf("%s push is %d runes: %s", name, n, c.text)
		}
		for _, m := range c.must {
			if !strings.Contains(c.text, m) {
				t.Errorf("%s push lacks %q: %s", name, m, c.text)
			}
		}
	}
}
