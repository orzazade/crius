package wakewatch

import (
	"strings"
	"testing"
	"time"
)

// The 11-21 shape: Crius's 02:50 DECISION to axi:ms-flow ("Same cause twice = fix now ... Build it next") had no line
// from ms-flow after it, while api-split answered its own GO. Only the silent role wakes Crius, once.
func TestACriusDecisionNobodyActedOnWakesCriusOnce(t *testing.T) {
	bus := stamp(t0.Add(-time.Hour)) + " INFO axi:ms-flow -> crius:lead — on F5.002\n" +
		stamp(t0) + " DECISION crius:lead -> axi:ms-flow — Same cause twice = fix now. Build it next: masked copy\n" +
		stamp(t0) + " GO crius:lead -> axi:api-split — A: rename Valkey\n" +
		stamp(t0.Add(10*time.Minute)) + " ASK axi:api-split -> crius:lead — POC VM stopped before fetch\n" +
		stamp(t0.Add(11*time.Minute)) + " ANSWER crius:lead -> axi:api-split — go on\n"
	home := setup(t, map[string]string{"axi.log": bus})
	f := &fake{}
	if err := Run(f.deps(home, t0.Add(orderAfter+time.Minute))); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, s := range f.sent {
		if strings.Contains(s, "has posted nothing since") {
			got = append(got, s)
		}
	}
	if len(got) != 1 || !strings.Contains(got[0], "axi:ms-flow has posted nothing since") ||
		!strings.Contains(got[0], "Same cause twice") || !strings.Contains(got[0], "2 h ago") {
		t.Fatalf("want one wake naming ms-flow and the decision, got %q", f.sent)
	}
	f2 := &fake{}
	if err := Run(f2.deps(home, t0.Add(orderAfter+time.Hour))); err != nil {
		t.Fatal(err)
	}
	for _, s := range f2.sent {
		if strings.Contains(s, "has posted nothing since") {
			t.Fatalf("typed again: %q", s)
		}
	}
}

func TestAnOrderIsOpenOnlyUntilItsRoleSaysAnything(t *testing.T) {
	cases := []struct {
		name, bus string
		wakes     bool
	}{
		{"any line from the role closes it", stamp(t0) + " ORDER crius:lead -> quotes:dev — land 8453\n" +
			stamp(t0.Add(30*time.Minute)) + " INFO quotes:dev -> quotes:qa — landing\n", false},
		{"a bare role name on its own bus", stamp(t0) + " DECISION crius:lead -> dev — land 8453\n", true},
		{"a reply from another role does not close it", stamp(t0) + " DECISION crius:lead -> quotes:dev — land it\n" +
			stamp(t0.Add(30*time.Minute)) + " INFO quotes:qa -> crius:lead — pass\n", true},
		{"too young", stamp(t0.Add(time.Hour)) + " DECISION crius:lead -> quotes:dev — land it\n", false},
		{"past the window", stamp(t0.Add(-11*time.Hour)) + " DECISION crius:lead -> quotes:dev — land it\n", false},
		{"a whole team or a copy is no one", stamp(t0) + " DECISION crius:lead -> quotes:all (cc quotes:qa) — rule\n", false},
		{"another team's role on this bus", stamp(t0) + " DECISION crius:lead -> axi:qa — x\n", false},
		{"not from crius:lead", stamp(t0) + " DECISION quotes:lead -> quotes:dev — x\n", false},
		{"not an act type", stamp(t0) + " INFO crius:lead -> quotes:dev — fyi\n", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := setup(t, map[string]string{"quotes.log": spoke("quotes:dev") + c.bus})
			f := &fake{}
			if err := Run(f.deps(home, t0.Add(orderAfter+time.Minute))); err != nil {
				t.Fatal(err)
			}
			woke := strings.Contains(strings.Join(f.sent, "\n"), "has posted nothing since")
			if woke != c.wakes {
				t.Fatalf("woke %v, want %v: %q", woke, c.wakes, f.sent)
			}
		})
	}
}

func TestAParkedRoleOwesNoReply(t *testing.T) {
	home := setup(t, map[string]string{"quotes.log": spoke("quotes:dev") + stamp(t0) + " DECISION crius:lead -> quotes:dev — land it\n",
		"../parked": "quotes:dev\n"})
	f := &fake{}
	if err := Run(f.deps(home, t0.Add(orderAfter+time.Minute))); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(f.sent, "\n"), "has posted nothing since") {
		t.Fatalf("a parked role woke Crius: %q", f.sent)
	}
}

func TestARoleThatMissedSeveralLinesWakesCriusOnceForTheOldest(t *testing.T) {
	bus := spoke("quotes:b1") +
		stamp(t0) + " DECISION crius:lead -> quotes:b1 — first\n" +
		stamp(t0.Add(5*time.Minute)) + " ORDER crius:lead -> quotes:b1 — second\n" +
		stamp(t0.Add(6*time.Minute)) + " GO crius:lead -> quotes:b1 — third\n"
	home := setup(t, map[string]string{"quotes.log": bus})
	f := &fake{}
	if err := Run(f.deps(home, t0.Add(orderAfter+10*time.Minute))); err != nil {
		t.Fatal(err)
	}
	if len(f.sent) != 1 || !strings.Contains(f.sent[0], "(and 2 later line(s) to it)") || !strings.Contains(f.sent[0], "— first") {
		t.Fatalf("want one wake for the oldest line, got %q", f.sent)
	}
}

// spoke is a line from who before the orders, so the bus knows it as a role that posts.
func spoke(who string) string {
	return stamp(t0.Add(-3*time.Hour)) + " INFO " + who + " -> crius:lead — on it\n"
}

// Review P3: a group name ("builders") or a program never posts on the bus, so it can never answer.
func TestANameThatNeverPostedOnTheBusOwesNothing(t *testing.T) {
	home := setup(t, map[string]string{"quotes.log": spoke("quotes:dev") +
		stamp(t0) + " DECISION crius:lead -> quotes:builders — both of you: rebase\n"})
	f := &fake{}
	if err := Run(f.deps(home, t0.Add(orderAfter+time.Minute))); err != nil {
		t.Fatal(err)
	}
	if len(f.sent) != 0 {
		t.Fatalf("%q", f.sent)
	}
}

// Review P2-3: once the oldest open line of a silence leaves the window, its later lines do not wake Crius anew.
func TestASilenceWakesCriusOnceEvenAfterItsOldestLineAgesOut(t *testing.T) {
	bus := spoke("quotes:b1") + stamp(t0) + " DECISION crius:lead -> quotes:b1 — first\n" +
		stamp(t0.Add(time.Hour)) + " ORDER crius:lead -> quotes:b1 — second\n"
	home := setup(t, map[string]string{"quotes.log": bus})
	for _, h := range []time.Duration{3, 12, 13} {
		f := &fake{}
		if err := Run(f.deps(home, t0.Add(h*time.Hour+time.Minute))); err != nil {
			t.Fatal(err)
		}
		if woke := len(f.sent) > 0; woke != (h == 3) {
			t.Fatalf("at +%d h: %q", h, f.sent)
		}
	}
}
