package quota

import (
	"testing"
	"time"
	"unicode/utf8"
)

// A push longer than a bus line (300 runes, store.MaxLen) is cut, which would drop its end: the Appxite rule or the
// enterprise $. With long but real values, each push fits.
func TestPushesFitABusLine(t *testing.T) {
	now := time.Date(2026, 10, 18, 9, 41, 0, 0, time.Local)
	r := &Report{Now: now, Reset: now.Add(4*24*time.Hour + 19*time.Hour), SevenPct: 62.4, Day: 3, Passed: 0.31,
		AvgPerDay: 9.7, MaxUSD: 1234, UnknownUSD: 2628, EntWeekUSD: 1234,
		Shares:  []Share{{"petclub-az", 1012}, {"crius", 222}, {"m2az", 10}},
		EntWeek: []Share{{"quotes", 1100}, {"axi", 134}}}
	ok := map[string]bool{"axi": true, "quotes": true}
	for name, s := range map[string]string{"pace": r.PaceText(), "runout": r.RunoutText(ok), "used up": r.UsedUpText(ok)} {
		if n := utf8.RuneCountInString(s); n > 300 {
			t.Errorf("%s push is %d runes: %s", name, n, s)
		}
	}
}
