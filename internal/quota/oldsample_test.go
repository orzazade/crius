package quota

import (
	"strings"
	"testing"
	"time"
)

// 11-21: every role moved to T3 threads, which run no statusline, so the newest sample aged from 15:25 on while Room
// still read it as room (an old share under 90%), and Pick and the PAUSE guard decided on it for 6 h. A sample older than
// StaleAfter decides nothing: it is unknown, never room, and the reason names its age and the limit. A share measured at
// 100% stays full at any age (a share only rises within its week).
func TestAnOldSampleIsUnknownNeverRoom(t *testing.T) {
	now := time.Date(2026, 11, 21, 21, 34, 0, 0, time.UTC)
	reset := now.Add(50 * time.Hour)
	sample := func(age time.Duration, seven float64) *Report {
		return &Report{Now: now, Accounts: []AcctUse{{Login: "u", Name: "max3", FiveKnown: true, SevenAt: now.Add(-age),
			FiveAt: now.Add(-age), SevenPct: seven, Reset: reset}}}
	}
	for _, c := range []struct {
		name     string
		age      time.Duration
		seven    float64
		room     string // Room's verdict
		standing string // Standing at Limit
		why      string
	}{
		{"at the limit is fresh", StaleAfter, 40, "room", "room", "7d 40%"},
		{"one minute over", StaleAfter + time.Minute, 40, "unknown", "unsure", "sample 46 min old (over the 45 min limit: unknown)"},
		{"tonight, 6 h old", 6*time.Hour + 9*time.Minute, 12, "unknown", "unsure", "sample 6 h old (over the 45 min limit: unknown)"},
		{"old at 89%", 3 * time.Hour, 89, "unknown", "unsure", "over the 45 min limit"},
		{"old but full", 6 * time.Hour, 100, "full", "full", "7d 100%"},
	} {
		r := sample(c.age, c.seven)
		v := r.Account("u")
		if verdict, reason := Room(v); verdict != c.room || c.room != "full" && !strings.Contains(reason, c.why) {
			t.Errorf("%s: Room says %s %q, want %s %q", c.name, verdict, reason, c.room, c.why)
		}
		if st, why, _ := Standing(r, "u", Limit); st != c.standing || !strings.Contains(why, c.why) {
			t.Errorf("%s: Standing says %s %q, want %s %q", c.name, st, why, c.standing, c.why)
		}
	}
}
