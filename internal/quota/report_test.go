package quota

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// A push longer than a bus line (300 runes, store.MaxLen) is cut, which would drop its end: the Appxite rule and the
// enterprise $. However long the project names, each push fits and keeps its end.
func TestPushesFitABusLine(t *testing.T) {
	now := time.Date(2026, 10, 18, 9, 41, 0, 0, time.Local)
	for _, top := range []string{"petclub-az", strings.Repeat("x", 61)} {
		r := &Report{Now: now, Reset: now.Add(4*24*time.Hour + 19*time.Hour), SevenPct: 62.4, Day: 3, Passed: 0.31,
			AvgPerDay: 9.7, MaxUSD: 1234, UnknownUSD: 2628, EntWeekUSD: 1234,
			Shares:  []Share{{top, 1012}, {strings.Repeat("y", 40), 222}, {"m2az", 10}},
			EntWeek: []Share{{"quotes", 1100}, {"axi", 134}}}
		ok := map[string]bool{"axi": true, "quotes": true}
		end := "Enterprise this week ≥ $1234 + unknown $2628."
		for name, s := range map[string]string{"pace": r.PaceText(), "runout": r.RunoutText(ok), "used up": r.UsedUpText(ok)} {
			if n := utf8.RuneCountInString(s); n > 300 || !strings.HasSuffix(s, end) {
				t.Errorf("%s push (%d runes) must fit and end with the enterprise $: %s", name, n, s)
			}
		}
		if s := r.RunoutText(ok); !strings.Contains(s, "only Appxite work (axi, quotes) may continue") {
			t.Errorf("runout push lost the Appxite rule: %s", s)
		}
	}
}
