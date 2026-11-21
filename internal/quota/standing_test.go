package quota

import (
	"strings"
	"testing"
	"time"
)

// Crius 11-21: Pick, the PAUSE guard and quota --room read each login by Standing, and a login whose share Room cannot
// tell is unsure, never room (review of the change: a 5h at 93% an hour old, a reading ahead of the clock and a top share
// not measured read "room").
func TestStandingIsUnsureWheneverRoomCannotTell(t *testing.T) {
	now := time.Date(2026, 11, 21, 3, 0, 0, 0, time.UTC)
	reset, five := now.Add(50*time.Hour), now.Add(2*time.Hour)
	at := func(u AcctUse) *Report {
		u.Login = "u"
		return &Report{Now: now, Accounts: []AcctUse{u}}
	}
	for _, c := range []struct {
		name, want, why string
		r               *Report
	}{
		{"never measured", "unsure", "", &Report{Now: now}},
		{"week reset", "unsure", "no reading since its week reset", at(AcctUse{Ended: now.Add(-24 * time.Hour)})},
		{"old 5h at 93%", "unsure", "(over the 45 min limit: unknown)", at(AcctUse{FiveKnown: true, SevenAt: now.Add(-time.Hour),
			FiveAt: now.Add(-time.Hour), SevenPct: 50, FivePct: 93, Reset: reset, FiveReset: five})},
		{"ahead of the clock", "unsure", "ahead of the clock", at(AcctUse{FiveKnown: true, SevenAt: now.Add(time.Hour),
			FiveAt: now.Add(time.Hour), SevenPct: 50, Reset: reset, FiveReset: five})},
		{"top share not measured", "unsure", "top share not measured", at(AcctUse{SevenPct: 60, Reset: reset})},
		{"fresh 40%", "room", "7d 40%", at(AcctUse{FiveKnown: true, SevenAt: now.Add(-time.Minute),
			FiveAt: now.Add(-time.Minute), SevenPct: 40, Reset: reset, FiveReset: five})},
		{"fresh at the limit", "full", "7d 95%", at(AcctUse{FiveKnown: true, SevenAt: now.Add(-time.Minute),
			FiveAt: now.Add(-time.Minute), SevenPct: 95, Reset: reset, FiveReset: five})},
		{"5h full", "full", "7d 40%, 5h 100%", at(AcctUse{FiveKnown: true, SevenAt: now.Add(-time.Minute),
			FiveAt: now.Add(-time.Minute), SevenPct: 40, FivePct: 100, Reset: reset, FiveReset: five})},
	} {
		st, why, _ := Standing(c.r, "u", Limit)
		if st != c.want || c.why == "" && why != "" || !strings.Contains(why, c.why) {
			t.Errorf("%s: got %s %q, want %s %q", c.name, st, why, c.want, c.why)
		}
	}
}
