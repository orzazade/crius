package quota

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Owner 10-23: only a folder made by aitch link from the persona's folder counts (its projects/ is the persona's
// transcript root, so crius files the session), and only one signed in. An account at its limit or at the project's
// pause_at is passed over. A named account is checked the same way. Which of the rest wins is the soonest reset
// (TestPickTakesTheSoonestResetUnder95); here every reset is the same, so the first row with room does.
func TestPickTakesALinkedSignedInMaxAccount(t *testing.T) {
	root := t.TempDir()
	persona := filepath.Join(root, ".claude-work")
	transcripts := filepath.Join(persona, "projects")
	if err := os.MkdirAll(transcripts, 0o755); err != nil {
		t.Fatal(err)
	}
	folder := func(name, id string, linked bool) string {
		d := filepath.Join(root, name)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if id != "" {
			if err := os.WriteFile(filepath.Join(d, ".claude.json"),
				[]byte(`{"oauthAccount":{"accountUuid":"`+id+`"}}`), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if linked {
			if err := os.Symlink(transcripts, filepath.Join(d, "projects")); err != nil {
				t.Fatal(err)
			}
		}
		return d
	}
	own := folder(".claude", "acct-1", false) // max1's personal folder: not a link of the work folder
	wm := folder(".claude-work-max", "acct-1", true)
	m2 := folder(".claude-max2", "acct-2", true)
	m3 := folder(".claude-max3", "acct-3", true)
	m4 := folder(".claude-max4", "", true)        // linked, not signed in yet
	m5 := folder(".claude-max5", "acct-5", false) // signed in, not linked
	m6 := folder(".claude-max6", "acct-6", true)
	if err := os.WriteFile(filepath.Join(persona, ".claude.json"),
		[]byte(`{"oauthAccount":{"accountUuid":"acct-ent"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	login := func(d string) string {
		l, err := LoginOf(d)
		if err != nil || l == "" {
			t.Fatal(d, l, err)
		}
		return l
	}
	as := &Accounts{Rows: []Acct{
		{Name: "max1", Plan: "max", Login: login(wm), Dirs: []string{own, wm}},
		{Name: "max2", Plan: "max", Login: login(m2), Dirs: []string{m2}},
		{Name: "max3", Plan: "max", Login: login(m3), Dirs: []string{m3}},
		{Name: "max4", Plan: "max", Dirs: []string{m4}},
		{Name: "max5", Plan: "max", Login: login(m5), Dirs: []string{m5}},
		{Name: "max6", Plan: "max", Login: login(m6), Dirs: []string{m6}},
		{Name: "ent", Plan: "enterprise", Login: login(persona), Dirs: []string{persona}},
	}}
	now := time.Now()
	use := func(d string, seven float64) AcctUse {
		return AcctUse{Login: login(d), SevenPct: seven, FivePct: 10, SevenAt: now.Add(-time.Minute),
			FiveAt: now.Add(-time.Minute), Reset: now.Add(48 * time.Hour), FiveReset: now.Add(time.Hour), FiveKnown: true}
	}
	report := func(us ...AcctUse) *Report { return &Report{Now: now, Accounts: us} }
	ended := AcctUse{Login: login(m6), SevenPct: 97, Reset: now.Add(-time.Hour), Ended: now.Add(-time.Hour)}

	for _, c := range []struct {
		name    string
		r       *Report
		want    string
		pauseAt int
		pick    string // row name, "" for none
		dir     string
		err     string
	}{
		{"same reset: the first row", report(use(wm, 70), use(m2, 30), use(m3, 50), use(m6, 60)), "max", 90, "max1", wm, ""},
		{"unmeasured goes after a measured one", report(use(wm, 70), use(m2, 30), use(m3, 50)), "max", 90, "max1", wm, ""},
		{"ended week goes after a measured one", report(use(wm, 70), use(m2, 30), use(m3, 50), ended), "max", 90, "max1", wm, ""},
		{"ties go to the first row", report(use(wm, 30), use(m2, 30), use(m3, 50), use(m6, 60)), "max", 90, "max1", wm, ""},
		{"at pause_at is passed over", report(use(wm, 92), use(m2, 95), use(m3, 91), use(m6, 90)), "max", 90, "", "",
			"every Max account is at its limit: max1 7d 92%, max2 7d 95%, max3 7d 91%, max6 7d 90%"},
		{"full is passed over without pause_at", report(use(wm, 100), use(m2, 100), use(m3, 100), use(m6, 100)), "max", 0,
			"", "", "every Max account is at its limit"},
		{"named row", report(use(wm, 10), use(m2, 30)), "max2", 90, "max2", m2, ""},
		{"named row at the limit", report(use(wm, 10), use(m2, 95)), "max2", 90, "", "", "max2 is at its limit: 7d 95%"},
		{"named row with no linked folder", report(), "max5", 90, "", "", "max5 has no folder linked to "},
		{"named row unknown", report(), "max9", 90, "", "", "accounts.toml has no Max account max9"},
		{"named enterprise row", report(), "ent", 90, "", "", "accounts.toml has no Max account ent"},
	} {
		name, dir, _, err := Pick(c.r, as, transcripts, c.want, c.pauseAt)
		switch {
		case c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)):
			t.Errorf("%s: err %v, want %q", c.name, err, c.err)
		case c.err == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case name != c.pick || dir != c.dir:
			t.Errorf("%s: picked %q %q, want %q %q", c.name, name, dir, c.pick, c.dir)
		}
	}

	// No row with a linked folder: no pick, and the launch goes as before (the aitch route picks).
	if name, dir, _, err := Pick(report(), &Accounts{Rows: as.Rows[:1]}, filepath.Join(root, "other", "projects"), "max", 90); name != "" || dir != "" || err != nil {
		t.Errorf("no linked row: %q %q %v", name, dir, err)
	}
	if name, dir, _, err := Pick(report(), nil, transcripts, "max", 90); name != "" || dir != "" || err != nil {
		t.Errorf("no accounts file: %q %q %v", name, dir, err)
	}
}

// Owner 10-28 23:5x: quota left at a weekly reset is lost, so crius-spawn uses the Max account whose week resets
// soonest until its 7d share reaches 95%, then the next soonest; an account just reset goes last. Only accounts with a
// folder in the session's persona count, and a pick of an account with no measured quota says so.
func TestPickTakesTheSoonestResetUnder95(t *testing.T) {
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
	m3 := folder(".claude-work-max3", "acct-3", work)
	m4 := folder(".claude-work-max4", "acct-4", work)
	login := func(d string) string {
		l, err := LoginOf(d)
		if err != nil || l == "" {
			t.Fatal(d, l, err)
		}
		return l
	}
	rows := []Acct{
		{Name: "max1", Plan: "max", Login: login(wm), Dirs: []string{own, wm}},
		{Name: "max2", Plan: "max", Login: login(m2), Dirs: []string{m2, m2p}},
		{Name: "max3", Plan: "max", Login: login(m3), Dirs: []string{m3}},
	}
	as := &Accounts{Rows: rows}
	with4 := &Accounts{Rows: append(append([]Acct{}, rows...), Acct{Name: "max4", Plan: "max", Login: login(m4), Dirs: []string{m4}})}
	at := func(s string) time.Time {
		t1, err := time.ParseInLocation("2006-01-02 15:04", s, time.Local)
		if err != nil {
			t.Fatal(err)
		}
		return t1
	}
	use := func(now time.Time, d string, seven float64, reset string) AcctUse {
		return AcctUse{Login: login(d), SevenPct: seven, FivePct: 10, SevenAt: now.Add(-time.Minute),
			FiveAt: now.Add(-time.Minute), Reset: at(reset), FiveReset: now.Add(time.Hour), FiveKnown: true}
	}
	// The 10-28 23:50 numbers: max1 99% resets Thu 00:00, max3 46% Sat 12:00, max2 13% Sun 07:00.
	n1 := at("2026-10-28 23:50")
	r1 := &Report{Now: n1, Accounts: []AcctUse{use(n1, wm, 99, "2026-10-29 00:00"), use(n1, m3, 46, "2026-10-31 12:00"),
		use(n1, m2, 13, "2026-11-01 07:00")}}
	// max3 at 95%: the next soonest, max2.
	r2 := &Report{Now: n1, Accounts: []AcctUse{use(n1, wm, 99, "2026-10-29 00:00"), use(n1, m3, 95, "2026-10-31 12:00"),
		use(n1, m2, 13, "2026-11-01 07:00")}}
	// Thu 00:10: max1's week ended with no sample since, so it is 0% until Thu 11-05: last.
	n2 := at("2026-10-29 00:10")
	ended := AcctUse{Login: login(wm), Reset: time.Time{}, Ended: at("2026-10-29 00:00")}
	r3 := &Report{Now: n2, Accounts: []AcctUse{ended, use(n2, m3, 46, "2026-10-31 12:00"), use(n2, m2, 13, "2026-11-01 07:00")}}
	r4 := &Report{Now: n2, Accounts: []AcctUse{ended, use(n2, m3, 96, "2026-10-31 12:00"), use(n2, m2, 97, "2026-11-01 07:00")}}
	// After the reset with a sample: max1 resets Thu 11-05, after the others.
	r5 := &Report{Now: n2, Accounts: []AcctUse{use(n2, wm, 1, "2026-11-05 00:00"), use(n2, m3, 46, "2026-10-31 12:00"),
		use(n2, m2, 13, "2026-11-01 07:00")}}
	// Review of item 0: max3 at 94% by a sample 2 h old may be past 95% now: unsure, so max2.
	old := use(n1, m3, 94, "2026-10-31 12:00")
	old.SevenAt, old.FiveAt = n1.Add(-2*time.Hour), n1.Add(-2*time.Hour)
	r6 := &Report{Now: n1, Accounts: []AcctUse{use(n1, wm, 99, "2026-10-29 00:00"), old, use(n1, m2, 13, "2026-11-01 07:00")}}
	for _, c := range []struct {
		name    string
		r       *Report
		as      *Accounts
		root    string
		want    string
		pauseAt int
		pick    string
		dir     string
		why     []string // each in why
		err     string
	}{
		{"10-28 23:50 work", r1, as, work, "max", 0, "max3", m3, []string{"max3", "resets Sat 10-31 12:00", "7d 46%", "max1 7d 99%"}, ""},
		{"10-28 23:50 personal", r1, as, personal, "max", 0, "max2", m2p, []string{"max2", "resets Sun 11-01 07:00", "7d 13%", "max1 7d 99%"}, ""},
		{"at 95% the next soonest", r2, as, work, "max", 0, "max2", m2, []string{"max3 7d 95%"}, ""},
		{"a lower pause_at is the limit", r1, as, work, "max", 40, "max2", m2, []string{"max3 7d 46%"}, ""},
		{"pause_at 100 keeps 95", r2, as, work, "max", 100, "max2", m2, nil, ""},
		{"an ended week goes last", r3, as, work, "max", 0, "max3", m3, nil, ""},
		{"an ended week is used when the rest are full", r4, as, work, "max", 0, "max1", wm,
			[]string{"no sample since its reset Thu 10-29 00:00"}, ""},
		{"a reset week goes last", r5, as, work, "max", 0, "max3", m3, nil, ""},
		{"unmeasured is not picked while a measured one has room", r1, with4, work, "max", 0, "max3", m3,
			[]string{"quota unknown: max4"}, ""},
		{"unmeasured is picked last and said", r2, with4, work, "max", 10, "max4", m4,
			[]string{"max4", "quota not measured"}, ""},
		{"all full", &Report{Now: n1, Accounts: []AcctUse{use(n1, wm, 99, "2026-10-29 00:00"), use(n1, m3, 96, "2026-10-31 12:00"),
			use(n1, m2, 97, "2026-11-01 07:00")}}, as, personal, "max", 0, "", "", nil, "every Max account is at its limit: max1"},
		{"named row says why", r1, as, work, "max2", 0, "max2", m2, []string{"max2", "as asked", "7d 13%"}, ""},
		{"an old sample at 90% or more is unsure", r6, as, work, "max", 0, "max2", m2, []string{"quota unknown: max3 (7d 94%"}, ""},
		{"a named row may spend its last 5%", r2, as, work, "max3", 0, "max3", m3, []string{"as asked", "7d 95%"}, ""},
	} {
		name, dir, why, err := Pick(c.r, c.as, c.root, c.want, c.pauseAt)
		switch {
		case c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)):
			t.Errorf("%s: err %v, want %q", c.name, err, c.err)
		case c.err == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case name != c.pick || dir != c.dir:
			t.Errorf("%s: picked %q %q (%s), want %q %q", c.name, name, dir, why, c.pick, c.dir)
		}
		for _, w := range c.why {
			if !strings.Contains(why, w) {
				t.Errorf("%s: why %q lacks %q", c.name, why, w)
			}
		}
	}
}
