package quota

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Owner 11-19: nothing moves to or starts on max1 until max2 and max3 are both at their limit. max1 resets soonest
// here, so the old rule would take it; every other Max row, also one with no folder in this persona, must be full first.
func TestPickHoldsMax1UntilMax2AndMax3AreAtTheLimit(t *testing.T) {
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
	now := time.Now()
	use := func(login string, seven float64, in time.Duration) AcctUse {
		return AcctUse{Login: login, SevenPct: seven, FivePct: 10, SevenAt: now.Add(-time.Minute),
			FiveAt: now.Add(-time.Minute), Reset: now.Add(in), FiveReset: now.Add(time.Hour), FiveKnown: true}
	}
	r := func(one, two, three float64) *Report {
		return &Report{Now: now, Accounts: []AcctUse{use(l1, one, time.Hour), use(l2, two, 48*time.Hour),
			use(l3, three, 72*time.Hour)}}
	}
	for _, c := range []struct {
		name, root, want string
		r                *Report
		pick, dir        string
		why              []string
		err              string
	}{
		{"max1 resets soonest, max2 has room", work, "max", r(10, 50, 20), "max2", m2, []string{"max1 held back"}, ""},
		{"max2 full, max3 has room", work, "max", r(10, 96, 20), "max3", m3, []string{"max1 held back"}, ""},
		{"max2 and max3 full", work, "max", r(10, 96, 97), "max1", wm, []string{"every other Max account is at its limit (max2 7d 96%, max3 7d 97%)"}, ""},
		// 11-24: max1 held for max3 means max3 takes the session, in its work folder when it has no personal one.
		{"personal: max3 has room but no folder here", personal, "max", r(10, 96, 20), "max3", m3,
			[]string{"7d 20%", "passed over: max2 7d 96%", "no other Max account has a folder under " + personal, "max1 held back"}, ""},
		{"personal: max3 unmeasured", personal, "max", &Report{Now: now, Accounts: []AcctUse{use(l1, 10, time.Hour),
			use(l2, 96, time.Hour)}}, "max3", m3, []string{PickedBlind, "max1 held back"}, ""},
		{"personal: both full", personal, "max", r(10, 96, 99), "max1", own, nil, ""},
		{"max1 by name while max2 has room", work, "max1", r(10, 50, 99), "", "", nil,
			"max1 is held back (owner 11-19) until every other Max account is at its limit: max2 7d 50%"},
		{"max1 by name once both are full", work, "max1", r(10, 96, 99), "max1", wm, []string{"as asked"}, ""},
		{"max1 full too", work, "max", r(99, 96, 97), "", "", nil, "every Max account is at its limit"},
		// review: a project's lower pause_at pauses that project, it does not put the others at their limit
		{"pause_at under the others' shares", work, "max", r(10, 50, 60), "", "", nil,
			"max1 is held back (owner 11-19) until every other Max account is at its limit: max2 7d 50%, max3 7d 60%; every other one is at its limit"},
	} {
		pauseAt := 0
		if strings.HasPrefix(c.name, "pause_at") {
			pauseAt = 40
		}
		name, dir, why, err := Pick(c.r, as, c.root, c.want, pauseAt)
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
	// No row has a folder in this persona: no pick and no error, as before (launch.Run refuses it); only max1 has one:
	// the others take it in a folder of another persona, the soonest reset first (11-24).
	if name, dir, _, err := Pick(r(10, 50, 20), as, filepath.Join(root, "none", "projects"), "max", 0); name != "" || dir != "" || err != nil {
		t.Errorf("no folder here: %q %q %v", name, dir, err)
	}
	only := &Accounts{Rows: []Acct{as.Rows[0], {Name: "max2", Plan: "max", Login: l2, Dirs: []string{m2}}, as.Rows[2]}}
	if name, dir, why, err := Pick(r(10, 50, 20), only, personal, "max", 0); err != nil || name != "max2" || dir != m2 ||
		!strings.Contains(why, "max1 held back") {
		t.Errorf("only max1 has a folder here: picked %q %q (%s), err %v; want max2 %s", name, dir, why, err, m2)
	}
}

// Playbook 11-24 06:00: with max2 gone (it held the only other personal folder), crius (persona personal) has a folder
// only on max1, the reserve; max3, at 44%, has only a work folder. Pick refused with "max1 is held back ... max3 7d 44%":
// it held max1 for max3 yet would not start on max3. Max1 held means another Max row takes the session, in its own
// persona's folder first, else in another persona's.
func TestPickTakesAnotherPersonasFolderBeforeTheReserve(t *testing.T) {
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
		if err := os.WriteFile(filepath.Join(d, ".claude.json"), []byte(`{"oauthAccount":{"accountUuid":"`+id+`"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if p := filepath.Join(d, "projects"); p != persona {
			if err := os.Symlink(persona, p); err != nil {
				t.Fatal(err)
			}
		}
		return d
	}
	own, wm := folder(".claude", "acct-1", personal), folder(".claude-work-max", "acct-1", work)
	m3, ent := folder(".claude-work-max3", "acct-3", work), folder(".claude-work", "acct-ent", work)
	l1, _ := LoginOf(wm)
	l3, _ := LoginOf(m3)
	le, _ := LoginOf(ent)
	as := &Accounts{Rows: []Acct{
		{Name: "ent", Plan: "enterprise", Login: le, Dirs: []string{ent}}, // never picked (owner 11-07), even with room
		{Name: "max1", Plan: "max", Login: l1, Dirs: []string{own, wm}},
		{Name: "max3", Plan: "max", Login: l3, Dirs: []string{m3}},
	}}
	now := time.Now()
	use := func(login string, seven float64, in time.Duration) AcctUse {
		return AcctUse{Login: login, SevenPct: seven, FivePct: 10, SevenAt: now.Add(-time.Minute),
			FiveAt: now.Add(-time.Minute), Reset: now.Add(in), FiveReset: now.Add(time.Hour), FiveKnown: true}
	}
	r := func(one, three float64) *Report {
		return &Report{Now: now, Accounts: []AcctUse{use(le, 1, time.Minute), use(l1, one, time.Hour), use(l3, three, 72*time.Hour)}}
	}
	name, dir, why, err := Pick(r(10, 44), as, personal, "max", 0)
	if err != nil || name != "max3" || dir != m3 {
		t.Fatalf("personal, only max1 has a folder here, max3 7d 44%%: picked %q %q (%s), err %v; want max3 %s", name, dir, why, err, m3)
	}
	for _, w := range []string{"max1 held back", "no other Max account has a folder under " + personal + " with room"} {
		if !strings.Contains(why, w) {
			t.Errorf("why %q lacks %q", why, w)
		}
	}
	// max3 at its limit: max1 is free, in the persona's own folder.
	if name, dir, _, err := Pick(r(10, 96), as, personal, "max", 0); err != nil || name != "max1" || dir != own {
		t.Errorf("max3 full: picked %q %q, err %v; want max1 %s", name, dir, err, own)
	}
	// A project whose persona has no folder anywhere is still not placed (launch.Run refuses it).
	if name, dir, _, err := Pick(r(10, 44), as, filepath.Join(root, "none", "projects"), "max", 0); name != "" || dir != "" || err != nil {
		t.Errorf("no folder here: %q %q %v", name, dir, err)
	}
}
