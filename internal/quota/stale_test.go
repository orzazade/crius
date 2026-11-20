package quota

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Night 11-20/21 on the Mac: max3's newest reading was from the week that reset Sat 11-14 14:00, and Pick called it "7d 0%,
// a new week", so room. The Legion saw max3 at 96% that week. A reading past its reset says nothing about this week: the
// account is unknown, never room. Pick takes a measured account with room before it, tries it only when no measured one
// has room, and max1 stays held (owner 11-19) while it is unknown, as for a row never measured.
func TestPickTreatsAReadingFromAnEarlierWeekAsUnknown(t *testing.T) {
	root := t.TempDir()
	work, personal := filepath.Join(root, ".claude-work", "projects"), filepath.Join(root, ".claude", "projects")
	for _, d := range []string{work, personal} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	folder := func(name, id, persona string) string {
		d := filepath.Join(root, name)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, ".claude.json"),
			[]byte(`{"oauthAccount":{"accountUuid":"`+id+`"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if p := filepath.Join(d, "projects"); p != persona {
			if err := os.Symlink(persona, p); err != nil {
				t.Fatal(err)
			}
		}
		return d
	}
	own := folder(".claude", "acct-1", personal)
	wm := folder(".claude-work-max", "acct-1", work)
	m2 := folder(".claude-max2", "acct-2", work)
	m2p := folder(".claude-max2-personal", "acct-2", personal)
	m3 := folder(".claude-work-max3", "acct-3", work) // max3 has no personal folder, as on the Mac
	login := func(d string) string {
		l, err := LoginOf(d)
		if err != nil || l == "" {
			t.Fatal(d, l, err)
		}
		return l
	}
	l1, l2, l3 := login(wm), login(m2), login(m3)
	as := &Accounts{Rows: []Acct{
		{Name: "max1", Plan: "max", Login: l1, Dirs: []string{own, wm}},
		{Name: "max2", Plan: "max", Login: l2, Dirs: []string{m2, m2p}},
		{Name: "max3", Plan: "max", Login: l3, Dirs: []string{m3}},
	}}
	at := func(s string) time.Time {
		t1, err := time.ParseInLocation("2006-01-02 15:04", s, time.Local)
		if err != nil {
			t.Fatal(err)
		}
		return t1
	}
	now := at("2026-11-21 01:30")
	use := func(login string, seven float64, reset string) AcctUse {
		return AcctUse{Login: login, SevenPct: seven, FivePct: 10, SevenAt: now.Add(-time.Minute),
			FiveAt: now.Add(-time.Minute), Reset: at(reset), FiveReset: now.Add(time.Hour), FiveKnown: true}
	}
	// As Build reports it: the newest sample's week reset Sat 11-14 14:00, so its next reset (Sat 11-21 14:00) is the soonest.
	stale := AcctUse{Login: l3, Ended: at("2026-11-14 14:00")}
	tonight := &Report{Now: now, Accounts: []AcctUse{use(l1, 3, "2026-11-26 02:00"), use(l2, 95, "2026-11-22 09:00"), stale}}
	max2Room := &Report{Now: now, Accounts: []AcctUse{use(l1, 3, "2026-11-26 02:00"), use(l2, 50, "2026-11-22 09:00"), stale}}
	bothFull := &Report{Now: now, Accounts: []AcctUse{use(l1, 3, "2026-11-26 02:00"), use(l2, 96, "2026-11-22 09:00"),
		use(l3, 97, "2026-11-28 14:00")}}
	for _, c := range []struct {
		name, root string
		r          *Report
		pick, dir  string
		why        []string // each in why (or in err)
		not        []string // none in why or err
		err        string
	}{
		{"tonight, personal: max3 unknown holds max1, no 0% room", personal, tonight, "", "",
			[]string{"max3 quota unknown (no reading since its week reset Sat 11-14 14:00)", "max2 7d 95%"},
			[]string{"7d 0%", "new week"}, "max1 is held back"},
		{"tonight, work: max3 is the only one not full, tried as unknown", work, tonight, "max3", m3,
			[]string{"quota unsure", "no reading since its week reset Sat 11-14 14:00", "picked as no measured account has room",
				"max2 7d 95%", "max1 held back"},
			[]string{"7d 0%", "the soonest under"}, ""},
		{"a measured one with room goes before an unknown one that resets sooner", work, max2Room, "max2", m2,
			[]string{"7d 50%", "quota unknown: max3", "max1 held back"}, []string{"7d 0%"}, ""},
		{"max2 and max3 measured full: max1", work, bothFull, "max1", wm,
			[]string{"every other Max account is at its limit (max2 7d 96%, max3 7d 97%)"}, nil, ""},
		{"named: max3 asked for by name is started, said unknown", work, tonight, "max3", m3,
			[]string{"account max3: quota unsure (no reading since its week reset Sat 11-14 14:00)"},
			[]string{"7d 0%", "as asked: 7d"}, ""},
	} {
		want := "max"
		if strings.HasPrefix(c.name, "named: ") {
			want = "max3"
		}
		name, dir, why, err := Pick(c.r, as, c.root, want, 0)
		text := why
		if err != nil {
			text = err.Error()
		}
		switch {
		case c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)):
			t.Errorf("%s: err %v, want %q", c.name, err, c.err)
		case c.err == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case name != c.pick || dir != c.dir:
			t.Errorf("%s: picked %q %q (%s), want %q %q", c.name, name, dir, why, c.pick, c.dir)
		}
		for _, w := range c.why {
			if !strings.Contains(text, w) {
				t.Errorf("%s: %q lacks %q", c.name, text, w)
			}
		}
		for _, w := range c.not {
			if strings.Contains(text, w) {
				t.Errorf("%s: %q says %q", c.name, text, w)
			}
		}
	}
}
