package wakewatch

import (
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/sessionstate"
)

// waiterProcs is a session's Claude (pid 500) with a team-wait.sh under a shell, as the Bash tool runs it, and one
// team-wait.sh of another session (pid 900).
func waiterProcs(alive bool) []Proc {
	ps := []Proc{{PID: 500, PPID: 1, Command: "claude"}, {PID: 900, PPID: 1, Command: "claude"},
		{PID: 901, PPID: 900, Command: "/bin/zsh -c source snap.sh && bash team-wait.sh --role other BUILD-LOG.md"},
		{PID: 902, PPID: 901, Command: "/bin/bash /x/skills/cmux-team/scripts/team-wait.sh --role other BUILD-LOG.md"}}
	if alive {
		ps = append(ps, Proc{PID: 16710, PPID: 500, Command: "/bin/zsh -c source snap.sh && bash team-wait.sh --role qa-backend-2"},
			Proc{PID: 16713, PPID: 16710, Command: "/bin/bash /x/skills/cmux-team/scripts/team-wait.sh --role qa-backend-2 --cursor c BUILD-LOG.md"})
	}
	return ps
}

func rearmLines(t *testing.T, home string) []string {
	var out []string
	for _, l := range leadLinesOf(t, home, " INFO crius:wakewatch -> ") {
		out = append(out, l)
	}
	return out
}

// crius:lead DECISION 11-08: quotes qa-backend-2 sat idle 02:34-05:26 because its team-wait (pid 16713) died and
// nothing noticed. A live, not closed role whose session had a waiter and has had none for 10 min, with its tab idle,
// gets one re-arm line typed into its tab (with the waiter's own command) and one INFO naming it to its lead.
func TestARoleWhoseWaiterDiedIsRearmedOnce(t *testing.T) {
	home := roleHome(t, role("qa-backend-2", "s2", t0.Add(-time.Hour)))
	f := &fake{ses: []Session{{Name: "quotes · qa-backend-2", ID: "s2", PID: 500}},
		states:  []sessionstate.State{{SessionID: "s2", State: sessionstate.Idle}},
		screens: []Screen{{Surface: "surface:7", ID: "SURF-2", Title: "quotes · qa-backend-2", Text: "❯ \n"}}}
	r, _ := sessionstate.ReadRole(home+"/roles", "quotes", "qa-backend-2")
	r.Surface = "SURF-2"
	fileRole(t, home, r)
	at := func(m int, alive bool) {
		t.Helper()
		f.procs = waiterProcs(alive)
		if err := Run(f.deps(home, t0.Add(time.Duration(m)*time.Minute))); err != nil {
			t.Fatal(m, err)
		}
	}
	at(0, true)
	at(5, false) // died between 0 and 5
	at(14, false)
	if len(f.tabs) != 0 || len(rearmLines(t, home)) != 0 {
		t.Fatalf("typed before 10 min: %q %q", f.tabs, rearmLines(t, home))
	}
	at(15, false)
	since := t0.Add(5 * time.Minute).Local().Format("15:04")
	if len(f.tabs) != 1 || f.tabs[0][0] != "quotes · qa-backend-2" ||
		!strings.HasPrefix(f.tabs[0][1], "WAKEWATCH: no team-wait waiter has run for quotes · qa-backend-2 since "+since) ||
		!strings.HasSuffix(f.tabs[0][1], "/x/skills/cmux-team/scripts/team-wait.sh --role qa-backend-2 --cursor c BUILD-LOG.md") {
		t.Fatalf("tabs %q", f.tabs)
	}
	if l := rearmLines(t, home); len(l) != 1 || !strings.Contains(l[0], "-> quotes:lead — quotes:qa-backend-2 had no team-wait waiter since "+since) {
		t.Fatalf("bus %q", l)
	}
	for _, m := range []int{20, 60, 172} { // 05:26 in the real case: still once
		at(m, false)
	}
	if len(f.tabs) != 1 || len(rearmLines(t, home)) != 1 {
		t.Fatalf("typed again: %q %q", f.tabs, rearmLines(t, home))
	}
	// re-armed, then dead again: a new spell, typed again
	at(180, true)
	at(185, false)
	at(195, false)
	if len(f.tabs) != 2 || len(rearmLines(t, home)) != 2 {
		t.Fatalf("second spell: %q %q", f.tabs, rearmLines(t, home))
	}
}

// A busy tab is not typed into: the line waits until the tab is idle.
func TestABusyTabIsRearmedOnceIdle(t *testing.T) {
	home := roleHome(t, role("qa-backend-2", "s2", t0.Add(-time.Hour)))
	f := &fake{ses: []Session{{Name: "quotes · qa-backend-2", ID: "s2", PID: 500}},
		states:  []sessionstate.State{{SessionID: "s2", State: sessionstate.Running}},
		screens: []Screen{{Surface: "surface:7", ID: "SURF-2", Title: "quotes · qa-backend-2", Text: "❯ \n"}}}
	r, _ := sessionstate.ReadRole(home+"/roles", "quotes", "qa-backend-2")
	r.Surface = "SURF-2"
	fileRole(t, home, r)
	for _, m := range []int{0, 5, 15, 25} {
		f.procs = waiterProcs(m == 0)
		if err := Run(f.deps(home, t0.Add(time.Duration(m)*time.Minute))); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.tabs) != 0 {
		t.Fatalf("typed into a busy tab: %q", f.tabs)
	}
	f.states[0].State = sessionstate.Idle
	if err := Run(f.deps(home, t0.Add(30*time.Minute))); err != nil {
		t.Fatal(err)
	}
	if len(f.tabs) != 1 {
		t.Fatalf("idle tab not typed: %q", f.tabs)
	}
}

// A session never seen with a waiter (crius:build and crius:ops take typed orders, not a waiter) and a closed role
// are left alone.
func TestARoleWithNoWaiterEverIsLeftAlone(t *testing.T) {
	home := roleHome(t, role("qa-backend-2", "s2", t0.Add(-time.Hour)))
	f := &fake{ses: []Session{{Name: "quotes · qa-backend-2", ID: "s2", PID: 500}},
		states:  []sessionstate.State{{SessionID: "s2", State: sessionstate.Idle}},
		screens: []Screen{{Surface: "surface:7", ID: "SURF-2", Title: "quotes · qa-backend-2", Text: "❯ \n"}},
		procs:   waiterProcs(false)}
	for _, m := range []int{0, 15, 30} {
		if err := Run(f.deps(home, t0.Add(time.Duration(m)*time.Minute))); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.tabs) != 0 {
		t.Fatalf("%q", f.tabs)
	}
}

// Failing to list the processes is loud to Crius, never "no waiter" (and never a re-arm line).
func TestFailingToListProcessesIsLoud(t *testing.T) {
	home := roleHome(t, role("qa-backend-2", "s2", t0.Add(-time.Hour)))
	f := &fake{ses: []Session{{Name: "quotes · qa-backend-2", ID: "s2", PID: 500}},
		states: []sessionstate.State{{SessionID: "s2", State: sessionstate.Idle}}, procs: waiterProcs(true)}
	if err := Run(f.deps(home, t0)); err != nil {
		t.Fatal(err)
	}
	f.procs, f.procErr = nil, errNoPS
	for _, m := range []int{5, 15, 25} {
		if err := Run(f.deps(home, t0.Add(time.Duration(m)*time.Minute))); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.tabs) != 0 || !strings.Contains(strings.Join(f.sent, "\n"), "the waiter re-arm check failed to look: ps failed") {
		t.Fatalf("tabs %q sent %q", f.tabs, f.sent)
	}
}

var errNoPS = errorString("ps failed")

type errorString string

func (e errorString) Error() string { return string(e) }

// A session that was down for a run or more (reboot, cmux restart, crius-spawn -resume keeps the id) keeps what was seen
// of its waiter, so it is re-armed once it is back with none (review P2-1).
func TestAResumedSessionIsRearmed(t *testing.T) {
	home := roleHome(t, role("qa-backend-2", "s2", t0.Add(-time.Hour)))
	up := []Session{{Name: "quotes · qa-backend-2", ID: "s2", PID: 500}}
	f := &fake{states: []sessionstate.State{{SessionID: "s2", State: sessionstate.Idle}},
		screens: []Screen{{Surface: "surface:7", ID: "SURF-2", Title: "quotes · qa-backend-2", Text: "❯ \n"}}}
	r, _ := sessionstate.ReadRole(home+"/roles", "quotes", "qa-backend-2")
	r.Surface = "SURF-2"
	fileRole(t, home, r)
	for _, m := range []int{0, 5, 10, 20, 25, 30} {
		f.ses, f.procs = up, waiterProcs(m == 0)
		if m == 5 || m == 10 { // the Mac rebooted: no Claude, no waiter
			f.ses, f.procs = nil, waiterProcs(false)[:1]
		}
		if err := Run(f.deps(home, t0.Add(time.Duration(m)*time.Minute))); err != nil {
			t.Fatal(m, err)
		}
	}
	if len(f.tabs) != 1 || !strings.Contains(f.tabs[0][1], "since "+t0.Add(20*time.Minute).Local().Format("15:04")) {
		t.Fatalf("tabs %q", f.tabs)
	}
}

// crius:lead 11-14: axi api-split runs as a T3 thread (no cmux tab, Surface ""), its waiter stopped at the 2 h harness
// limit, and every run then said "failed to look: no tab of surface". A role with no tab gets no typed line and no
// failure: one INFO to its lead names it, since only a thread message wakes it.
func TestARoleWithNoTabIsNamedOnceNotAFailure(t *testing.T) {
	home := roleHome(t, role("qa-backend-2", "s2", t0.Add(-time.Hour)))
	f := &fake{ses: []Session{{Name: "quotes · qa-backend-2", ID: "s2", PID: 500}},
		states:  []sessionstate.State{{SessionID: "s2", State: sessionstate.Idle}},
		screens: []Screen{{Surface: "surface:7", ID: "SURF-2", Title: "quotes · other", Text: "❯ \n"}}}
	at := func(m int, alive bool) {
		t.Helper()
		f.procs = waiterProcs(alive)
		if err := Run(f.deps(home, t0.Add(time.Duration(m)*time.Minute))); err != nil {
			t.Fatal(m, err)
		}
	}
	at(0, true)
	for _, m := range []int{5, 15, 25, 60} {
		at(m, false)
	}
	if len(f.tabs) != 0 || strings.Contains(strings.Join(f.sent, "\n"), "failed to look") {
		t.Fatalf("tabs %q sent %q", f.tabs, f.sent)
	}
	if l := rearmLines(t, home); len(l) != 1 || !strings.Contains(l[0], "quotes:qa-backend-2 has no tab to type into") {
		t.Fatalf("bus %q", l)
	}
}
