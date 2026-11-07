package wakewatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/sessionstate"
	"github.com/orzazade/crius/internal/testutil"
)

// roleHome is a home with quotes' bus log, its lead in projects.toml and the given roles, each filed at its time.
func roleHome(t *testing.T, roles ...sessionstate.State) string {
	home := setup(t, map[string]string{"quotes.log": ""})
	testutil.Write(t, filepath.Join(home, "projects.toml"), "[projects.quotes]\nleads = { lead = \"/nowhere/STATUS.md\" }\n")
	for _, r := range roles {
		fileRole(t, home, r)
	}
	return home
}

func fileRole(t *testing.T, home string, r sessionstate.State) {
	t.Helper()
	if err := sessionstate.WriteRole(filepath.Join(home, "roles"), r, true); err != nil {
		t.Fatal(err)
	}
}

func role(name, id string, at time.Time) sessionstate.State {
	return sessionstate.State{SessionID: id, Project: "quotes", Role: name, Time: at, State: sessionstate.Idle}
}

// deadAt runs at each minute after t0, with the sessions live listed for that minute.
func deadAt(t *testing.T, home string, f *fake, live func(m int) []Session, minutes ...int) {
	t.Helper()
	for _, m := range minutes {
		f.ses = live(m)
		if err := Run(f.deps(home, t0.Add(time.Duration(m)*time.Minute))); err != nil {
			t.Fatal(m, err)
		}
	}
}

func deadWakes(f *fake) []string {
	var out []string
	for _, s := range f.sent {
		if strings.Contains(s, "no live Claude") {
			out = append(out, s)
		}
	}
	return out
}

func leadLines(t *testing.T, home string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, "bus", "quotes.log"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.Contains(l, " ALERT crius:wakewatch -> ") {
			out = append(out, l)
		}
	}
	return out
}

// crius:lead ORDER 11-08 01:30: six quotes roles had no Claude since 11-06/07, and QA asks went to empty tabs. A role
// whose session was live and then had no Claude for 15 min wakes Crius and its lead, once; a resume that dies again
// wakes them again.
func TestADeadRoleWakesItsLeadAndCriusOnce(t *testing.T) {
	home := roleHome(t, role("qa-frontend", "s1", t0.Add(-time.Hour)))
	f := &fake{}
	up := map[int]bool{0: true, 45: true}
	deadAt(t, home, f, func(m int) []Session {
		if up[m] {
			return []Session{{Name: "quotes · qa-frontend", ID: "s1"}}
		}
		return nil
	}, 0, 5, 14)
	if w := deadWakes(f); len(w) != 0 || len(leadLines(t, home)) != 0 {
		t.Fatalf("woke before 15 min: %q %q", w, leadLines(t, home))
	}
	deadAt(t, home, f, func(int) []Session { return nil }, 16)
	w, l := deadWakes(f), leadLines(t, home)
	if len(w) != 1 || !strings.Contains(w[0], "quotes · qa-frontend (session s1) has had no live Claude since 22:00") ||
		!strings.Contains(w[0], "crius-spawn -resume quotes qa-frontend") || !strings.Contains(w[0], "Told quotes:lead") {
		t.Fatalf("Crius: %q", w)
	}
	if len(l) != 1 || !strings.Contains(l[0], "ALERT crius:wakewatch -> quotes:lead — quotes · qa-frontend (session s1)") {
		t.Fatalf("lead: %q", l)
	}
	deadAt(t, home, f, func(m int) []Session {
		if up[m] {
			return []Session{{Name: "quotes · qa-frontend", ID: "s1"}}
		}
		return nil
	}, 21, 40, 45, 50, 59)
	if w, l := deadWakes(f), leadLines(t, home); len(w) != 1 || len(l) != 1 {
		t.Fatalf("woke again for one death: %q %q", w, l)
	}
	deadAt(t, home, f, func(int) []Session { return nil }, 61)
	if w, l := deadWakes(f), leadLines(t, home); len(w) != 2 || len(l) != 2 || !strings.Contains(w[1], "since 22:45") {
		t.Fatalf("a second death after the resume: %q %q", w, l)
	}
}

// The 83 roles already dead when the check began (finished teams never closed) must not flood Crius: only a death
// seen happen counts. A role started after a run and dead before the next one did die in view.
func TestARoleDeadBeforeTheCheckBeganStaysQuiet(t *testing.T) {
	home := roleHome(t, role("writer-a", "old", t0.Add(-48*time.Hour)))
	f := &fake{}
	none := func(int) []Session { return nil }
	deadAt(t, home, f, none, 0, 20, 60)
	if w := deadWakes(f); len(w) != 0 {
		t.Fatalf("an old death woke Crius: %q", w)
	}
	fileRole(t, home, role("p3", "quick", t0.Add(62*time.Minute)))
	deadAt(t, home, f, none, 65, 76)
	if w := deadWakes(f); len(w) != 0 {
		t.Fatalf("woke before 15 min: %q", w)
	}
	deadAt(t, home, f, none, 78)
	if w := deadWakes(f); len(w) != 1 || !strings.Contains(w[0], "quotes · p3 (session quick)") {
		t.Fatalf("%q", w)
	}
}

// Owner 11-08: rig-refresh and appinsights-e2e leads were closed on purpose. A closed session is skipped; another session
// of the role opens it again.
func TestAClosedRoleIsSkippedUntilAnotherSessionTakesIt(t *testing.T) {
	home := roleHome(t, role("rig", "s1", t0.Add(-time.Hour)))
	if err := sessionstate.CloseRole(filepath.Join(home, "roles"), "quotes", "rig", "s1"); err != nil {
		t.Fatal(err)
	}
	f := &fake{}
	deadAt(t, home, f, func(m int) []Session {
		if m == 0 {
			return []Session{{ID: "s1"}}
		}
		return nil
	}, 0, 20, 40)
	if w := deadWakes(f); len(w) != 0 {
		t.Fatalf("a closed role woke Crius: %q", w)
	}
	fileRole(t, home, role("rig", "s2", t0.Add(41*time.Minute)))
	deadAt(t, home, f, func(m int) []Session {
		if m == 42 {
			return []Session{{ID: "s2"}}
		}
		return nil
	}, 42, 60)
	if w := deadWakes(f); len(w) != 1 || !strings.Contains(w[0], "(session s2)") {
		t.Fatalf("the role's next session: %q", w)
	}
}

// A dead lead has no lead above it to wake: Crius alone is told, and the line names nobody told.
func TestADeadLeadTellsCriusAlone(t *testing.T) {
	home := roleHome(t, role("lead", "L", t0.Add(-time.Hour)))
	f := &fake{}
	deadAt(t, home, f, func(m int) []Session {
		if m == 0 {
			return []Session{{ID: "L"}}
		}
		return nil
	}, 0, 16)
	if w := deadWakes(f); len(w) != 1 || strings.Contains(w[0], "Told") || len(leadLines(t, home)) != 0 {
		t.Fatalf("%q %q", w, leadLines(t, home))
	}
}

// Failed to look is not found nothing: a role file that does not read is said to Crius.
func TestARoleFileThatDoesNotReadIsSaid(t *testing.T) {
	home := roleHome(t)
	testutil.Write(t, filepath.Join(home, "roles", "quotes.p3.json"), "{")
	f := &fake{}
	deadAt(t, home, f, func(int) []Session { return nil }, 0)
	found := false
	for _, s := range f.sent {
		found = found || strings.Contains(s, "the dead-role check failed to look") && strings.Contains(s, "quotes.p3.json")
	}
	if !found {
		t.Fatalf("%q", f.sent)
	}
}

// Review P2-1 of f616d39: Run aged every typed key out after 12 h 20 min, so a role left dead was typed again twice a
// day. A dead role's line is typed once for as long as it stays dead.
func TestALongDeadRoleIsTypedOnce(t *testing.T) {
	home := roleHome(t, role("qa", "s1", t0.Add(-time.Hour)))
	f := &fake{}
	var minutes []int
	for m := 0; m <= 25*60; m += 30 {
		minutes = append(minutes, m)
	}
	deadAt(t, home, f, func(m int) []Session {
		if m == 0 {
			return []Session{{ID: "s1"}}
		}
		return nil
	}, minutes...)
	if w, l := deadWakes(f), leadLines(t, home); len(w) != 1 || len(l) != 1 {
		t.Fatalf("over 25 h: %d wakes %q, %d lead lines", len(w), w, len(l))
	}
}

// Review P2-2: a parked or unwatched team wakes no one, as in every other check.
func TestAParkedOrUnwatchedTeamsDeadRoleWakesNoOne(t *testing.T) {
	for name, file := range map[string][2]string{"parked team": {"parked", "quotes"}, "parked role": {"parked", "quotes:qa"},
		"unwatched": {"wakewatch-projects", "cortex"}} {
		home := roleHome(t, role("qa", "s1", t0.Add(-time.Hour)))
		testutil.Write(t, filepath.Join(home, file[0]), file[1]+"\n")
		f := &fake{}
		deadAt(t, home, f, func(m int) []Session {
			if m == 0 {
				return []Session{{ID: "s1"}}
			}
			return nil
		}, 0, 20, 40)
		if w, l := deadWakes(f), leadLines(t, home); len(w) != 0 || len(l) != 0 {
			t.Errorf("%s: %q %q", name, w, l)
		}
	}
}

// Review P3-1: the ALERT to a lead is no work: a lead parked on the bus stays parked.
func TestTheDeadRoleAlertDoesNotUnparkALead(t *testing.T) {
	home := roleHome(t, role("qa", "s1", t0.Add(-time.Hour)))
	testutil.Write(t, filepath.Join(home, "bus", "quotes.log"), stamp(t0)+" PARKED crius:lead -> quotes:lead — Park now, nothing to do until the owner answers.\n")
	if p, _, err := parkedOnBus(home, "quotes:lead", t0); err != nil || !p {
		t.Fatalf("not parked before: %v %v", p, err)
	}
	f := &fake{}
	deadAt(t, home, f, func(m int) []Session {
		if m == 0 {
			return []Session{{ID: "s1"}}
		}
		return nil
	}, 0, 16)
	if len(leadLines(t, home)) != 1 {
		t.Fatalf("no ALERT to the lead: %q", leadLines(t, home))
	}
	if p, _, err := parkedOnBus(home, "quotes:lead", t0.Add(16*time.Minute)); err != nil || !p {
		t.Errorf("the ALERT unparked the lead: %v %v", p, err)
	}
}

// Review P3-2: a role file that does not read for one run keeps what the check knew: its death is still seen.
func TestARoleFileUnreadForOneRunKeepsItsDeath(t *testing.T) {
	r := role("qa", "s1", t0.Add(-time.Hour))
	home := roleHome(t, r)
	f := &fake{}
	none := func(m int) []Session {
		if m == 0 {
			return []Session{{ID: "s1"}}
		}
		return nil
	}
	deadAt(t, home, f, none, 0)
	testutil.Write(t, filepath.Join(home, "roles", "quotes.qa.json"), "{")
	deadAt(t, home, f, none, 5)
	fileRole(t, home, r)
	deadAt(t, home, f, none, 10, 30)
	if w := deadWakes(f); len(w) != 1 || !strings.Contains(w[0], "since 22:00") {
		t.Fatalf("%q", w)
	}
}

// Review P3-3: with no projects.toml no lead is known, so nothing goes on the bus (a dead lead was told of itself); Crius
// is told, and the failure is said.
func TestNoProjectsTomlTellsCriusAlone(t *testing.T) {
	home := roleHome(t, role("lead", "L", t0.Add(-time.Hour)))
	testutil.Write(t, filepath.Join(home, "projects.toml"), "[[[")
	f := &fake{}
	deadAt(t, home, f, func(m int) []Session {
		if m == 0 {
			return []Session{{ID: "L"}}
		}
		return nil
	}, 0, 16)
	failed := false
	for _, s := range f.sent {
		failed = failed || strings.Contains(s, "the dead-role check failed to look: projects.toml")
	}
	if w := deadWakes(f); len(w) != 1 || len(leadLines(t, home)) != 0 || !failed {
		t.Fatalf("%q %q", f.sent, leadLines(t, home))
	}
}
