package quota

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Owner 10-23: crius-spawn starts a session on the Max account with the most room. Only a folder made by aitch link
// from the persona's folder counts (its projects/ is the persona's transcript root, so crius files the session), and
// only one signed in. An account at its limit or at the project's pause_at is passed over; one no line measured yet
// counts 0%; a week that ended with no sample since counts 0%. A named account is checked the same way.
func TestPickTakesTheLinkedMaxAccountWithTheMostRoom(t *testing.T) {
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
		{"lowest share wins", report(use(wm, 70), use(m2, 30), use(m3, 50), use(m6, 60)), "max", 90, "max2", m2, ""},
		{"unmeasured counts 0%", report(use(wm, 70), use(m2, 30), use(m3, 50)), "max", 90, "max6", m6, ""},
		{"ended week counts 0%", report(use(wm, 70), use(m2, 30), use(m3, 50), ended), "max", 90, "max6", m6, ""},
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
		name, dir, err := Pick(c.r, as, transcripts, c.want, c.pauseAt)
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
	if name, dir, err := Pick(report(), &Accounts{Rows: as.Rows[:1]}, filepath.Join(root, "other", "projects"), "max", 90); name != "" || dir != "" || err != nil {
		t.Errorf("no linked row: %q %q %v", name, dir, err)
	}
	if name, dir, err := Pick(report(), nil, transcripts, "max", 90); name != "" || dir != "" || err != nil {
		t.Errorf("no accounts file: %q %q %v", name, dir, err)
	}
}
