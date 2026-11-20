package ingest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/testutil"
)

// Night 11-21: Pick read the other machines' spools and counted an account with no reading this week as no room, but the
// PAUSE guard read the Mac's store alone and counted it as room (max3's newest Mac reading was from 11-13 while it ran at
// 96% on the Legion). The guard reads quota as Pick does: with the Legion's copy max2 is at its limit, so with max1 at
// its limit too the whole team pauses; and an account no line measured, or with no reading since its week reset, is not
// room.
func TestQuotaGuardReadsTheOtherMachinesAndAnUnknownAccountIsNoRoom(t *testing.T) {
	setup := func(t *testing.T) (*ic, string, string) {
		clock = func() time.Time { return qNow }
		t.Cleanup(func() { clock = time.Now })
		x := newIC(t, cfg{extra: strings.Replace(guardedProject, "pause_at = 70", "pause_at = 90", 1)})
		d1, d2 := filepath.Join(x.home, "cfg1"), filepath.Join(x.home, "cfg2")
		l1, l2 := x.signIn(d1, "u-1"), x.signIn(d2, "u-2")
		testutil.Write(t, filepath.Join(x.home, "accounts.toml"), "[[account]]\nname = \"max1\"\nplan = \"max\"\ndirs = [\""+
			d1+"\"]\n[[account]]\nname = \"max2\"\nplan = \"max\"\ndirs = [\""+d2+"\"]\n")
		s1 := "aaaaaaaa-0000-0000-0000-000000000001"
		x.session("work", s1, "/work/quotes", "[crius account=max project=quotes role=p1]", qNow.Add(-20*time.Minute))
		l := sampleAt(s1, qNow.Add(-10*time.Minute), 92, qNow.Add(4*24*time.Hour))
		l.Login, l.Dir, l.Persona = l1, d1, "work"
		x.spool(l)
		return x, l2, d2
	}

	t.Run("the Legion measured max2 at its limit", func(t *testing.T) {
		x, l2, d2 := setup(t)
		// The Mac's store: max2 last seen in a week that has since reset, at 40%.
		old := sampleAt("m2", qNow.Add(-8*24*time.Hour), 40, qNow.Add(-24*time.Hour))
		old.Login, old.Dir, old.Persona = l2, d2, "work"
		x.spool(old)
		yes := true
		l := sampleAt("legion-m2", qNow.Add(-30*time.Minute), 96, qNow.Add(50*time.Hour))
		l.Login, l.Dir, l.Persona, l.Responded = l2, "~/.claude-work-max2", "work", &yes
		j, err := json.Marshal(l)
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(x.home, "spool", "hosts", "legion.jsonl")
		testutil.Write(t, p, string(append(j, '\n')))
		if err := os.Chtimes(p, qNow.Add(-3*time.Minute), qNow.Add(-3*time.Minute)); err != nil {
			t.Fatal(err)
		}
		x.run()
		x.run()
		if p := grepAlerts(x.bus("quotes"), " -> quotes:all — "); len(p) != 1 || !strings.Contains(p[0], "max2 96%") {
			t.Fatalf("max1 at 92%% here and max2 at 96%% on the Legion: want one PAUSE to quotes:all naming max2 96%%, got %q", x.bus("quotes"))
		}
		if n := grepAlerts(x.alerts(), "account max1 at 92%"); len(n) != 1 || !strings.Contains(n[0], "Room: none") {
			t.Fatalf("want the max1 NEED to say Room: none (max2 is at 96%% on the Legion), got %q", x.alerts())
		}
	})

	t.Run("max2 not measured anywhere", func(t *testing.T) {
		x, _, _ := setup(t)
		x.run()
		x.run()
		n := grepAlerts(x.alerts(), "account max1 at 92%")
		if len(n) != 1 || !strings.Contains(n[0], "Room: none") || !strings.Contains(n[0], "max2 not measured") {
			t.Fatalf("want the max1 NEED to say Room: none and name max2 as not measured apart, got %q", x.alerts())
		}
		// Not known to be full either: the whole team is not paused on a guess.
		if p := grepAlerts(x.bus("quotes"), " -> quotes:all — "); len(p) != 0 {
			t.Fatalf("max2 is unknown, not full: want no PAUSE to quotes:all, got %q", p)
		}
	})
}
