package wakewatch

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/sessionstate"
	"github.com/orzazade/crius/internal/testutil"
)

// 11-17 23:10 and 23:14: two QA threads drew "idle 47 min after a ⏳ reply … its job may be gone" while each had a live
// bus waiter under its claude and the item it named was in another role's heavy run. Such a role is waiting, not idle:
// no alert until t3WaitingFor, then one alert that names what it waits on. These fixtures are made up.

const waitReply = "R3.C4 is ready on build's side but won't be pushed until 5521 lands. ⏳"

// waitingFake is quotes:qa's thread idle 50 min on waitReply, its claude (pid 100) with a tool shell running a
// buswait.sh, and quotes:build's heavy run of 5521.
func waitingFake(idle time.Duration) *fake {
	return &fake{
		threads: map[string]T3Thread{"th-qa": {Status: "completed", Updated: t0.Add(-idle), LastReply: waitReply}},
		ses:     []Session{{Name: "", ID: "s-qa", PID: 100, Status: "idle"}},
		procs: []Proc{
			{PID: 1, PPID: 0, Command: "/sbin/launchd"},
			{PID: 100, PPID: 1, Command: "claude --output-format stream-json"},
			{PID: 101, PPID: 100, Command: "/bin/zsh -c source /h/.claude/shell-snapshots/snapshot-zsh-1.sh 2>/dev/null || true && eval 'bash /tmp/s/buswait.sh'"},
			{PID: 102, PPID: 101, Command: "bash /tmp/s/buswait.sh"},
			{PID: 103, PPID: 102, Command: "tail -n 0 -F /h/.crius/bus/quotes.log"},
		},
	}
}

var heavyRun = Proc{PID: 200, PPID: 1, Command: "bash /work/docs/qa/heavy-run.sh build 5521 record suites 1/6 0a1b2c3 -- go test ./..."}

func TestAWaitingRoleWithAWaiterAndAnItemInAHeavyRunIsNotIdle(t *testing.T) {
	home := threadHome(t, "qa")
	f := waitingFake(50 * time.Minute)
	f.procs = append(f.procs, heavyRun)
	runOnce(t, home, f, t0)
	if w := t3Wakes(f); len(w) != 0 {
		t.Fatalf("a role waiting on 5521 in build's heavy run woke Crius at 50 min: %q", w)
	}
	// past t3WaitingFor (2 h): one alert that says what it waits on, said once
	f.threads["th-qa"] = T3Thread{Status: "completed", Updated: t0.Add(-121 * time.Minute), LastReply: waitReply}
	runOnce(t, home, f, t0)
	runOnce(t, home, f, t0.Add(5*time.Minute))
	runOnce(t, home, f, t0.Add(30*time.Minute))
	w := t3Wakes(f)
	if len(w) != 1 {
		t.Fatalf("want one alert past 2 h, got %q", w)
	}
	for _, want := range []string{"quotes · qa", "WAITING ON 5521", "build's heavy run", "pid 200", "buswait.sh"} {
		if !strings.Contains(w[0], want) {
			t.Errorf("alert %q lacks %q", w[0], want)
		}
	}
}

// A heavy run's job id is often the row id in lower case without its dots (r3c4 for R3.C4), or with a prefix (q2-r3c4).
func TestAWaitingRoleMatchesARowIdInAHeavyRunJobId(t *testing.T) {
	home := threadHome(t, "qa")
	f := waitingFake(50 * time.Minute)
	f.threads["th-qa"] = T3Thread{Status: "completed", Updated: t0.Add(-50 * time.Minute), LastReply: "R3.C4 needs build's record first. ⏳"}
	f.procs = append(f.procs, Proc{PID: 201, PPID: 1, Command: "bash /work/docs/qa/heavy-run.sh qa-b q2-r3c4 n4 (lane 2) -- bash /work/jobs/q2-r3c4/s.sh"})
	runOnce(t, home, f, t0)
	if w := t3Wakes(f); len(w) != 0 {
		t.Fatalf("a role waiting on R3.C4 (heavy run q2-r3c4) woke Crius: %q", w)
	}
}

// A newer bus line from another role about the item says it is in flight; a DONE or VERIFIED about it says it landed,
// so the role should be at work: the alert stays.
func TestAWaitingRoleCountsANewerBusLineFromAnotherRole(t *testing.T) {
	home := threadHome(t, "qa", "build")
	f := waitingFake(50 * time.Minute)
	f.threads["th-build"] = T3Thread{Status: "running", Active: true, Updated: t0}
	bus := filepath.Join(home, "bus", "quotes.log")
	testutil.Write(t, bus, stamp(t0.Add(-60*time.Minute))+" INFO quotes:qa -> quotes:lead — waiting on 5521\n"+
		stamp(t0.Add(-55*time.Minute))+" INFO quotes:build -> quotes:lead — 5521 record run started, suites 1/6\n")
	runOnce(t, home, f, t0)
	if w := t3Wakes(f); len(w) != 0 {
		t.Fatalf("a role waiting on 5521, which build posted about since, woke Crius: %q", w)
	}

	home = threadHome(t, "qa", "build")
	f = waitingFake(50 * time.Minute)
	f.threads["th-build"] = T3Thread{Status: "running", Active: true, Updated: t0}
	testutil.Write(t, filepath.Join(home, "bus", "quotes.log"), stamp(t0.Add(-60*time.Minute))+" INFO quotes:qa -> quotes:lead — waiting on 5521\n"+
		stamp(t0.Add(-55*time.Minute))+" DONE quotes:build -> quotes:qa — 5521 landed at 0a1b2c3\n")
	runOnce(t, home, f, t0)
	if w := t3Wakes(f); len(w) != 1 || !strings.Contains(w[0], "its job may be gone") || !strings.Contains(w[0], "DONE") {
		t.Fatalf("5521 landed (DONE): want today's alert naming the DONE, got %q", w)
	}
}

// A waiter, but nothing it named is in flight (11-19): its live waiter still holds it, on its bus, until 2 h quiet.
func TestAWaiterWithNoItemInFlightHoldsItOnItsBus(t *testing.T) {
	home := threadHome(t, "qa")
	f := waitingFake(50 * time.Minute)
	runOnce(t, home, f, t0)
	if w := t3Wakes(f); len(w) != 0 {
		t.Fatalf("a role with a live waiter was alerted: %q", w)
	}
}

// The item is in a heavy run, but no waiter runs under the role's claude: nothing will wake it, so the alert stays.
func TestAnItemInFlightWithNoWaiterStillAlerts(t *testing.T) {
	home := threadHome(t, "qa")
	f := waitingFake(50 * time.Minute)
	f.procs = append(slices.DeleteFunc(f.procs, func(p Proc) bool { return p.PID >= 101 }), heavyRun)
	runOnce(t, home, f, t0)
	w := t3Wakes(f)
	if len(w) != 1 || !strings.Contains(w[0], "its job may be gone") || !strings.Contains(w[0], "no bus waiter") {
		t.Fatalf("want today's alert saying no waiter runs: %q", w)
	}
	// a waiter that runs under another session is not this role's
	f = waitingFake(50 * time.Minute)
	f.ses = append(f.ses, Session{ID: "s-other", PID: 300})
	for i := range f.procs {
		if f.procs[i].PID == 101 {
			f.procs[i].PPID = 300
		}
	}
	f.procs = append(f.procs, Proc{PID: 300, PPID: 1, Command: "claude"}, heavyRun)
	home = threadHome(t, "qa")
	runOnce(t, home, f, t0)
	if w := t3Wakes(f); len(w) != 1 || !strings.Contains(w[0], "no bus waiter") {
		t.Fatalf("another session's waiter held the alert: %q", w)
	}
}

// Unreadable is not waiting: a process list or session list that cannot be read keeps today's alert, and says why.
func TestAnUnreadableProcessTreeStillAlerts(t *testing.T) {
	home := threadHome(t, "qa")
	f := waitingFake(50 * time.Minute)
	f.procs = append(f.procs, heavyRun)
	f.procErr = errors.New("ps: exit status 1")
	runOnce(t, home, f, t0)
	w := t3Wakes(f)
	if len(w) != 1 || !strings.Contains(w[0], "its job may be gone") || !strings.Contains(w[0], "ps: exit status 1") {
		t.Fatalf("want today's alert with the read error: %q", w)
	}

	home = threadHome(t, "qa")
	f = waitingFake(50 * time.Minute)
	f.procs = append(f.procs, heavyRun)
	f.sesErr = errors.New("sessions dir unreadable")
	runOnce(t, home, f, t0)
	if w := t3Wakes(f); len(w) != 1 || !strings.Contains(w[0], "sessions dir unreadable") {
		t.Fatalf("want today's alert with the session read error: %q", w)
	}
}

func TestItemsInNamesPRNumbersAndRowIds(t *testing.T) {
	got := itemsIn(waitReply+" Suite 3 of 5, 47 min, P1=0, S1, see F-118 and #377 and B105. 2026-11-17 23:10",
		"Step #2 next; 1532 passed on port 5432, pid 48211; UTF-8, SHA-256, TS-1 and Go1.22 are fine.")
	want := []string{"R3.C4", "5521", "F-118", "377", "B105"}
	if !slices.Equal(got, want) {
		t.Fatalf("itemsIn = %q, want %q", got, want)
	}
}

// crius-wakewatch waiting prints the state of each role on ⏳: what it waits on, or why it is not held.
func TestWaitingPrintsWhatARoleWaitsOn(t *testing.T) {
	home := threadHome(t, "qa", "build")
	f := waitingFake(50 * time.Minute)
	f.threads["th-build"] = T3Thread{Status: "running", Active: true, Updated: t0}
	f.procs = append(f.procs, heavyRun)
	var b strings.Builder
	if code := Waiting(f.deps(home, t0), &b, nil); code != 0 {
		t.Fatalf("exit %d: %s", code, b.String())
	}
	want := "quotes:qa WAITING ON 5521 (build's heavy run (pid 200)), bus waiter buswait.sh (pid 102); idle 50 min after ⏳, held until 120 min (T3 thread th-qa)\n"
	if b.String() != want {
		t.Fatalf("got %q, want %q", b.String(), want)
	}
}

// Review P2-1: a line to the waiting role after its newest line should have woken it through its waiter. A thread that
// has not moved since is what the alert is for, so the hold ends, whatever the line's type.
func TestALineToTheWaitingRoleEndsTheHold(t *testing.T) {
	home := threadHome(t, "qa")
	f := waitingFake(50 * time.Minute)
	f.procs = append(f.procs, heavyRun)
	testutil.Write(t, filepath.Join(home, "bus", "quotes.log"), stamp(t0.Add(-60*time.Minute))+" INFO quotes:qa -> quotes:lead — waiting on 5521\n"+
		stamp(t0.Add(-30*time.Minute))+" FAIL quotes:build -> quotes:qa — 5521 build broke, over to you\n")
	runOnce(t, home, f, t0)
	if w := t3Wakes(f); len(w) != 1 || !strings.Contains(w[0], "FAIL line to it") {
		t.Fatalf("a FAIL to qa after its newest line held the alert: %q", w)
	}
}

// Review P3-7: the hold counts from the role's own newest bus line (quiet), not from its last reply (idle): a role
// idle 130 min that posted 60 min ago is still held; past 2 h quiet, one alert.
func TestTheHoldCountsFromTheRolesOwnNewestBusLine(t *testing.T) {
	home := threadHome(t, "qa")
	f := waitingFake(130 * time.Minute)
	f.procs = append(f.procs, heavyRun)
	testutil.Write(t, filepath.Join(home, "bus", "quotes.log"), stamp(t0.Add(-60*time.Minute))+" INFO quotes:qa -> quotes:lead — still waiting on 5521\n")
	runOnce(t, home, f, t0)
	if w := t3Wakes(f); len(w) != 0 {
		t.Fatalf("quiet 60 min, held: woke %q", w)
	}
	runOnce(t, home, f, t0.Add(61*time.Minute))
	if w := t3Wakes(f); len(w) != 1 || !strings.Contains(w[0], "WAITING ON 5521") || !strings.Contains(w[0], "for 121 min") {
		t.Fatalf("quiet 121 min: want one alert naming 5521: %q", w)
	}
}

// The waiter is found under a session crius-hook filed under the role, not only under the role file's session.
func TestTheWaiterIsFoundUnderASessionFiledUnderTheRole(t *testing.T) {
	home := threadHome(t, "qa")
	f := waitingFake(50 * time.Minute)
	f.procs = append(f.procs, heavyRun)
	f.ses = []Session{{ID: "s-qa-new", PID: 100}}
	f.states = []sessionstate.State{{SessionID: "s-qa-new", Project: "quotes", Role: "qa", Thread: "th-qa", Time: t0}}
	runOnce(t, home, f, t0)
	if w := t3Wakes(f); len(w) != 0 {
		t.Fatalf("the waiter under s-qa-new (filed as quotes:qa) was not found: %q", w)
	}
}

// Review P3-3: a line about the item from days before says nothing of now.
func TestAStaleLineAboutTheItemDoesNotHold(t *testing.T) {
	home := threadHome(t, "qa")
	f := waitingFake(50 * time.Minute)
	testutil.Write(t, filepath.Join(home, "bus", "quotes.log"), stamp(t0.Add(-72*time.Hour))+" INFO quotes:build -> quotes:lead — 5521 record run started\n")
	runOnce(t, home, f, t0.Add(71*time.Minute))
	if w := t3Wakes(f); len(w) != 1 || !strings.Contains(w[0], "WAITING ON ITS BUS") {
		t.Fatalf("a 3-day-old line made it wait on 5521: %q", w)
	}
}

// Review P3-5: crius-wakewatch waiting exits 2 when a check could not be read.
func TestWaitingExitsTwoWhenAProcessListCannotBeRead(t *testing.T) {
	home := threadHome(t, "qa")
	f := waitingFake(50 * time.Minute)
	f.procErr = errors.New("ps: exit status 1")
	var b strings.Builder
	if code := Waiting(f.deps(home, t0), &b, []string{"quotes:qa"}); code != 2 || !strings.Contains(b.String(), "ps: exit status 1") {
		t.Fatalf("exit %d: %q", code, b.String())
	}
}

// A line to the role that came before its ⏳ reply was read in that turn: it does not end the hold.
func TestALineToTheRoleBeforeItsLastReplyKeepsTheHold(t *testing.T) {
	home := threadHome(t, "qa")
	f := waitingFake(50 * time.Minute)
	f.procs = append(f.procs, heavyRun)
	testutil.Write(t, filepath.Join(home, "bus", "quotes.log"), stamp(t0.Add(-90*time.Minute))+" INFO quotes:qa -> quotes:lead — waiting on 5521\n"+
		stamp(t0.Add(-55*time.Minute))+" DONE quotes:build -> quotes:qa — R3.C4 is yours\n")
	runOnce(t, home, f, t0)
	if w := t3Wakes(f); len(w) != 0 {
		t.Fatalf("a DONE to qa read before its last reply ended the hold: %q", w)
	}
}

// A quiet line to the role (an INFO that asks nothing) does not wake its team-wait waiter, so it does not end the hold;
// an INFO that asks it something does.
func TestOnlyALineThatWakesTheWaiterEndsTheHold(t *testing.T) {
	for _, c := range []struct {
		line string
		held bool
	}{
		{"INFO quotes:build -> quotes:qa — 5521 record run is at suite 3 of 6", true},
		{"ACK quotes:build -> quotes:qa — seen", true},
		{"INFO quotes:build -> quotes:qa — can you re-run R3.C4 now?", false},
		{"INFO quotes:lead -> quotes:all — qa: pick up R3.C4", false},
		{"NEED quotes:build -> quotes:qa — 5521 needs your fixture", false},
		// review r2 P2-1: lines team-wait.sh wakes qa for, which the first copy of its rule missed
		{"INFO crius:lead -> quotes:qa — re-run R3.C4 on 0a1b2c3", false},
		{"VERIFIED quotes:lead -> quotes:qa — row R3.C4", false},
		{"READY quotes:build -> quotes:lead — 5521 ready (cc qa)", false},
		{"NEED-QA quotes:build -> quotes:lead — 5521 ready", false},
		{"INFO quotes:build -> quotes:lead — 5521 record run is at suite 4 of 6", true},
	} {
		home := threadHome(t, "qa")
		f := waitingFake(50 * time.Minute)
		f.procs = append(f.procs, heavyRun)
		testutil.Write(t, filepath.Join(home, "bus", "quotes.log"), stamp(t0.Add(-90*time.Minute))+" INFO quotes:qa -> quotes:lead — waiting on 5521\n"+
			stamp(t0.Add(-20*time.Minute))+" "+c.line+"\n")
		runOnce(t, home, f, t0)
		if w := t3Wakes(f); (len(w) == 0) != c.held {
			t.Errorf("%q: held %v, want %v (%q)", c.line, len(w) == 0, c.held, w)
		}
	}
}

func TestWakesFollowsTeamWaitForALead(t *testing.T) {
	if !wakes("quotes", "quotes:lead", "INFO", "quotes:build", "quotes:lead", "suite 3 green") {
		t.Error("an INFO to a lead wakes it")
	}
	if !wakes("quotes", "quotes:lead", "BLOCKED", "quotes:build", "quotes:qa", "stuck on 5521") {
		t.Error("a lead wakes on a BLOCKED to anyone")
	}
	if wakes("quotes", "quotes:qa", "INFO", "quotes:build", "quotes:lead", "qa: done?") {
		t.Error("an INFO to another role that does not name qa as a target does not wake qa")
	}
	if wakes("quotes", "quotes:qa", "READY", "quotes:build", "quotes:lead", "5521 ready (cc qa-2)") {
		t.Error("a line naming qa-2 does not wake qa")
	}
}
