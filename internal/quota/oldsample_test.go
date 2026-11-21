package quota

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 11-21: every role moved to T3 threads, which run no statusline, so max2's newest sample aged from 15:25 on while those
// threads kept spending it, and Room still read it as room (an old share under 90%): Pick and the PAUSE guard decided on
// it for 6 h. A sample is unknown, never room, once a request that may be on its login (its own, a Max one naming no
// login, or one on no known account) ran over StaleAfter after it. An idle login's old sample keeps the old rule (room
// under 90%): nothing here spent it. A share measured at 100% stays full at any age.
func TestASampleSpentPastIsUnknownNeverRoom(t *testing.T) {
	now := time.Date(2026, 11, 21, 21, 34, 0, 0, time.UTC)
	reset := now.Add(50 * time.Hour)
	for _, c := range []struct {
		name     string
		age      time.Duration
		seven    float64
		by, any  time.Duration // how long ago a request ran on the login, on any login (0: none)
		room     string        // Room's verdict
		standing string        // Standing at Limit
		why      string
	}{
		{"fresh, in use", StaleAfter, 40, time.Second, 0, "room", "room", "7d 40%"},
		{"tonight: 6 h old, spent on its login", 6*time.Hour + 9*time.Minute, 12, 3 * time.Minute, 0, "unknown", "unsure",
			"sample 6 h old (in use since, unmeasured)"},
		{"spent by a request on no known login", 2 * time.Hour, 40, 0, time.Minute, "unknown", "unsure", "(in use since, unmeasured)"},
		{"one minute over, spent", StaleAfter + time.Minute, 40, 0, time.Second, "unknown", "unsure", "46 min old (in use since"},
		{"old, used only within the limit after it", 3 * time.Hour, 40, 3*time.Hour - StaleAfter, 0, "room", "room", "7d 40%"},
		{"idle since", 3 * time.Hour, 40, 4 * time.Hour, 0, "room", "room", "7d 40%"},
		{"idle at 92%", 3 * time.Hour, 92, 0, 0, "unknown", "unsure", "(90% or more)"},
		{"old but full", 6 * time.Hour, 100, time.Minute, 0, "full", "full", "7d 100%"},
	} {
		r := &Report{Now: now, Accounts: []AcctUse{{Login: "u", Name: "max2", FiveKnown: true, SevenAt: now.Add(-c.age),
			FiveAt: now.Add(-c.age), SevenPct: c.seven, Reset: reset}}, spentBy: map[string]time.Time{}}
		if c.by > 0 {
			r.spentBy["u"] = now.Add(-c.by)
		}
		if c.any > 0 {
			r.spentAny = now.Add(-c.any)
		}
		r.spentBy["other"] = now // another login's use spends nothing of u
		v := r.Account("u")
		if verdict, reason := Room(v); verdict != c.room || !strings.Contains(reason, c.why) && c.room != "room" {
			t.Errorf("%s: Room says %s %q, want %s %q", c.name, verdict, reason, c.room, c.why)
		}
		if st, why, _ := Standing(r, "u", Limit); st != c.standing || !strings.Contains(why, c.why) {
			t.Errorf("%s: Standing says %s %q, want %s %q", c.name, st, why, c.standing, c.why)
		}
	}
}

// Review of b0f87e2: with every sample spent past, every row is unsure, and Pick took the first row in accounts.toml
// order (max2, last read at 94%) over max3 (10%, resets sooner). Of unsure rows Pick still takes the soonest reset, with a
// row last read at 90% or more after the rest, and says it is a guess.
func TestPickOfUnsureRowsTakesTheSoonestResetUnder90(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, ".claude-work", "projects")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	folder := func(name, id string) string {
		d := filepath.Join(root, name)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, ".claude.json"), []byte(`{"oauthAccount":{"accountUuid":"`+id+`"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(work, filepath.Join(d, "projects")); err != nil {
			t.Fatal(err)
		}
		return d
	}
	m2, m3, m4 := folder(".claude-work-max2", "acct-2"), folder(".claude-work-max3", "acct-3"), folder(".claude-work-max4", "acct-4")
	l2, _ := LoginOf(m2)
	l3, _ := LoginOf(m3)
	l4, _ := LoginOf(m4)
	if l2 == "" || l3 == "" || l4 == "" {
		t.Fatal("no login read from the test folders")
	}
	as := &Accounts{Rows: []Acct{
		{Name: "max2", Plan: "max", Login: l2, Dirs: []string{m2}},
		{Name: "max3", Plan: "max", Login: l3, Dirs: []string{m3}},
		{Name: "max4", Plan: "max", Login: l4, Dirs: []string{m4}},
	}}
	now := time.Date(2026, 11, 21, 21, 34, 0, 0, time.UTC)
	old := now.Add(-time.Hour)
	r := &Report{Now: now, spentAny: now.Add(-time.Minute), Accounts: []AcctUse{
		{Login: l2, Name: "max2", SevenAt: old, SevenPct: 94, Reset: now.Add(10 * time.Hour)},
		{Login: l3, Name: "max3", SevenAt: old, SevenPct: 10, Reset: now.Add(20 * time.Hour)},
		{Login: l4, Name: "max4", SevenAt: old, SevenPct: 30, Reset: now.Add(90 * time.Hour)},
	}}
	name, _, why, err := Pick(r, as, work, "max", 0)
	if err != nil || name != "max3" || !strings.Contains(why, PickedBlind) || !strings.Contains(why, "in use since, unmeasured") {
		t.Fatalf("want max3 (soonest reset under 90%%) as a guess, got %q %q %v", name, why, err)
	}
}
