package ingest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/quota"
	"github.com/orzazade/crius/internal/testutil"
)

// Night 11-21: the sign-in sweep (wakewatch, reading every machine's spool) said "week NEAR max3: 96%, resets 11-21
// 14:00" while PickAccount, reading the Mac's store alone (max3's newest row from 11-13, a week that has reset), called
// max3 unknown and kept max1 held, so crius on the Mac could start nothing. Same login, two answers. Pick reads the
// sweep's copy of the other machines' spools: max3 is at its limit, max2 is at 95%, so max1 is picked. A machine whose
// copy is missing or whose last read failed is said loudly in why.
func TestPickAccountReadsTheOtherMachinesWeekAsTheSweepDoes(t *testing.T) {
	setup := func(t *testing.T) (*ic, string) {
		x := quotaCase(t)
		folder := func(name, uuid string) (string, string) {
			d := filepath.Join(x.home, name)
			l := x.signIn(d, uuid)
			if err := os.Symlink(x.work, filepath.Join(d, "projects")); err != nil {
				t.Fatal(err)
			}
			return d, l
		}
		d1, l1 := folder("cfg1", "u-1")
		d2, l2 := folder("cfg2", "u-2")
		d3, l3 := folder("cfg3", "u-3")
		testutil.Write(t, filepath.Join(x.home, "accounts.toml"),
			"[[account]]\nname = \"max1\"\nplan = \"max\"\ndirs = [\""+d1+"\"]\n"+
				"[[account]]\nname = \"max2\"\nplan = \"max\"\ndirs = [\""+d2+"\"]\n"+
				"[[account]]\nname = \"max3\"\nplan = \"max\"\ndirs = [\""+d3+"\"]\n")
		on := func(l quota.Line, login, dir string) quota.Line {
			l.Login, l.Dir, l.Persona = login, dir, "work"
			return l
		}
		r1 := qNow.Add(2 * 24 * time.Hour)
		// The Mac's store: max3 last seen in a week that has since reset; max2 at 95%; max1 at 20%.
		x.spool(on(sampleAt("m3", qNow.Add(-8*24*time.Hour), 40, qNow.Add(-24*time.Hour)), l3, d3),
			on(sampleAt("m2", qNow.Add(-10*time.Minute), 95, r1), l2, d2),
			on(sampleAt("m1", qNow.Add(-10*time.Minute), 20, r1.Add(24*time.Hour)), l1, d1))
		x.run()
		return x, l3
	}
	copyOf := func(t *testing.T, x *ic, file string, body []byte, at time.Time) {
		p := filepath.Join(x.home, "spool", "hosts", file)
		testutil.Write(t, p, string(body))
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("legion measured max3 at 96%", func(t *testing.T) {
		x, l3 := setup(t)
		yes := true
		l := sampleAt("legion-m3", qNow.Add(-30*time.Minute), 96, qNow.Add(50*time.Hour))
		l.Login, l.Dir, l.Persona, l.Responded = l3, "~/.claude-work-max3", "work", &yes
		j, err := json.Marshal(l)
		if err != nil {
			t.Fatal(err)
		}
		copyOf(t, x, "legion.jsonl", append(j, '\n'), qNow.Add(-3*time.Minute))
		copyOf(t, x, "thinkpad.err", []byte("ssh: connect to host thinkpad: Operation timed out"), qNow.Add(-3*time.Minute))
		name, _, why, err := PickAccount(x.home, "axi", "max")
		if err != nil {
			t.Fatal(err)
		}
		if name != "max1" {
			t.Fatalf("max3 is at 96%% on the Legion and max2 at 95%%: want max1 (the reserve, every other account at its limit), got %s: %s", name, why)
		}
		for _, w := range []string{"max3 7d 96%", "max2 7d 95%", "legion spool read 3 min ago", "thinkpad spool NEVER READ", "UNREAD since", "Operation timed out"} {
			if !strings.Contains(why, w) {
				t.Errorf("why lacks %q: %s", w, why)
			}
		}
	})

	t.Run("no copy of any machine is loud", func(t *testing.T) {
		x, _ := setup(t)
		name, _, why, err := PickAccount(x.home, "axi", "max")
		if err != nil {
			t.Fatal(err)
		}
		if name == "max1" {
			t.Fatalf("max3 unknown: max1 stays held, got %s: %s", name, why)
		}
		for _, w := range []string{"legion spool NEVER READ", "thinkpad spool NEVER READ"} {
			if !strings.Contains(why, w) {
				t.Errorf("why lacks %q: %s", w, why)
			}
		}
	})
}

// Review of the fix: the limit check read the Mac's store alone, so a Mac session on max3 (its Mac lines repeat a week
// that has reset; the Legion measured 96% this week) never got the move NEED that PickAccount's answer calls for. The
// check reads the same merged weeks: one NEED, max3 at 96%, axi to max1. A move target picked with a machine's copy
// missing, old or unread says so in the NEED.
func TestQuotaLimitCheckReadsTheOtherMachinesWeek(t *testing.T) {
	x := quotaCase(t)
	folder := func(name, uuid string) (string, string) {
		d := filepath.Join(x.home, name)
		l := x.signIn(d, uuid)
		if err := os.Symlink(x.work, filepath.Join(d, "projects")); err != nil {
			t.Fatal(err)
		}
		return d, l
	}
	d1, l1 := folder("cfg1", "u-1")
	d2, l2 := folder("cfg2", "u-2")
	d3, l3 := folder("cfg3", "u-3")
	testutil.Write(t, filepath.Join(x.home, "accounts.toml"),
		"[[account]]\nname = \"max1\"\nplan = \"max\"\ndirs = [\""+d1+"\"]\n"+
			"[[account]]\nname = \"max2\"\nplan = \"max\"\ndirs = [\""+d2+"\"]\n"+
			"[[account]]\nname = \"max3\"\nplan = \"max\"\ndirs = [\""+d3+"\"]\n")
	on := func(l quota.Line, login, dir string) quota.Line {
		l.Login, l.Dir, l.Persona = login, dir, "work"
		return l
	}
	s3 := "aaaaaaaa-0000-0000-0000-000000000003"
	x.session("work", s3, "/work/axi", "[crius account=max project=axi role=p1]", qNow.Add(-20*time.Minute))
	r1, gone := qNow.Add(2*24*time.Hour), qNow.Add(-24*time.Hour)
	beat := on(sampleAt(s3, qNow.Add(-5*time.Minute), 40, gone), l3, d3)
	beat.Kind = "heartbeat"
	x.spool(on(sampleAt(s3, qNow.Add(-8*24*time.Hour), 40, gone), l3, d3), beat,
		on(sampleAt("m2", qNow.Add(-10*time.Minute), 95, r1), l2, d2),
		on(sampleAt("m1", qNow.Add(-10*time.Minute), 20, r1.Add(24*time.Hour)), l1, d1))
	yes := true
	l := sampleAt("legion-m3", qNow.Add(-30*time.Minute), 96, qNow.Add(50*time.Hour))
	l.Login, l.Persona, l.Responded = l3, "work", &yes
	j, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(x.home, "spool", "hosts", "legion.jsonl")
	testutil.Write(t, p, string(j)+"\n")
	if err := os.Chtimes(p, qNow.Add(-3*time.Minute), qNow.Add(-3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	x.run()
	n := grepAlerts(x.alerts(), "account max3 at 96%")
	if len(n) != 1 || !strings.Contains(n[0], "axi to max1") {
		t.Fatalf("want one NEED: max3 at 96%% (Legion), axi to max1; got %q", x.alerts())
	}
	if !strings.Contains(n[0], "; blind to thinkpad") {
		t.Fatalf("the ThinkPad was never copied: the move target says so, got %q", n[0])
	}
}
