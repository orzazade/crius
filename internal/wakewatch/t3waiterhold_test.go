package wakewatch

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/testutil"
)

// 11-19 19:51 and 20:57: two QA roles in T3 threads drew "idle N min after a ⏳ reply … its job may be gone" while
// each had a live team-wait.sh under its claude (Crius checked both by hand with ps):
//   - qa-2's reply and its newest bus line named no item and none of the purpose words, so its live waiter held nothing;
//   - qa's waiter started in the same minute as a VERIFIED line to it, which the turn before had read, yet the line was
//     read as one its waiter slept through (lines are stamped to the minute).
// A live waiter is what makes a role wait. These fixtures are made up in the 11-19 shape.

// holdFake is quotes:qa's thread idle `idle` on reply, its claude (pid 100) running team-wait.sh (pid 102) that started
// at waiterStart; with no waiterStart, no waiter runs (it died at its 2 h cap).
func holdFake(reply string, idle time.Duration, waiterStart time.Time) *fake {
	f := &fake{
		threads: map[string]T3Thread{"th-qa": {Status: "completed", Updated: t0.Add(-idle), LastReply: reply}},
		ses:     []Session{{ID: "s-qa", PID: 100, Status: "idle"}},
		procs: []Proc{
			{PID: 1, PPID: 0, Command: "/sbin/launchd"},
			{PID: 100, PPID: 1, Command: "claude --output-format stream-json --input-format stream-json"},
		},
	}
	if !waiterStart.IsZero() {
		f.procs = append(f.procs,
			Proc{PID: 101, PPID: 100, Start: waiterStart, Command: "/bin/zsh -c source /h/.claude/shell-snapshots/snapshot-zsh-1.sh 2>/dev/null || true && eval 'bash /h/skills/team-wait.sh --mode lead --role qa'"},
			Proc{PID: 102, PPID: 101, Start: waiterStart, Command: "bash /h/skills/team-wait.sh --mode lead --role qa --alias quotes:qa --boss crius:lead --cursor /w/qa/.wait-cursor /h/.crius/bus/quotes.log"})
	}
	return f
}

const busReply = "Posted that to Crius. ⏳ waiting on the bus for the next qa item (waiter pid 102)"

// qa-2 at 19:51: re-armed at its 2 h cap, said so on the bus, nothing to do; idle 49 min.
func TestT3WaiterHoldARoleWithALiveWaiterAndNoItemIsWaiting(t *testing.T) {
	home := threadHome(t, "qa")
	f := holdFake(busReply, 49*time.Minute, t0.Add(-51*time.Minute))
	testutil.Write(t, filepath.Join(home, "bus", "quotes.log"),
		stamp(t0.Add(-51*time.Minute))+" INFO quotes:qa -> crius:lead — Re 19:0x: team-wait re-armed (pid 102, task b7e). No item waits for qa: board has no unclaimed ready row, lane 2 empty. I re-arm at each 2 h cap.\n")
	runOnce(t, home, f, t0)
	w := t3Wakes(f)
	t.Logf("verdict: %q", w)
	if len(w) != 0 {
		t.Fatalf("a role waiting on its live bus waiter was alerted: %q", w)
	}
}

// qa at 20:57: its turn read a VERIFIED line to it, posted its own line, started its waiter and replied, all in one
// minute; idle 47 min.
func TestT3WaiterHoldALineFromBeforeTheWaiterStartedIsNotOneItSleptThrough(t *testing.T) {
	home := threadHome(t, "qa")
	reply := "Landing checked, nothing else for me to do. ⏳ Waiting on the bus watcher for the next READY or DONE."
	minute := t0.Add(-47 * time.Minute) // 21:13:00
	f := holdFake(reply, 47*time.Minute-40*time.Second, minute.Add(30*time.Second))
	testutil.Write(t, filepath.Join(home, "bus", "quotes.log"),
		stamp(minute)+" VERIFIED crius:lead -> quotes:build, quotes:qa — integration tip tree = QA land tree, 0 AI words\n"+
			stamp(minute)+" INFO quotes:qa -> quotes:build, crius:lead — LANDING OK: integration tip tree = qa PASS tree\n")
	runOnce(t, home, f, t0)
	w := t3Wakes(f)
	t.Logf("verdict: %q", w)
	if len(w) != 0 {
		t.Fatalf("a line from the minute the live waiter started was read as one it slept through: %q", w)
	}
}

// The waiter died (the harness stops a background task at 2 h) and nothing re-armed it: the alert stays.
func TestT3WaiterHoldADeadWaiterStillAlerts(t *testing.T) {
	home := threadHome(t, "qa")
	f := holdFake(busReply, 49*time.Minute, time.Time{})
	testutil.Write(t, filepath.Join(home, "bus", "quotes.log"),
		stamp(t0.Add(-51*time.Minute))+" INFO quotes:qa -> crius:lead — waiter re-armed. No item waits for qa.\n")
	runOnce(t, home, f, t0)
	w := t3Wakes(f)
	t.Logf("verdict: %q", w)
	if len(w) != 1 || !strings.Contains(w[0], "its job may be gone") || !strings.Contains(w[0], "no bus waiter runs under its claude") {
		t.Fatalf("want the alert naming the missing waiter: %q", w)
	}
}

// A line it wakes on, posted after the minute its waiter started, with the waiter still alive: the waiter is on the
// wrong log or role, so the alert stays.
func TestT3WaiterHoldALineAfterTheWaiterStartedThatShouldWakeItStillAlerts(t *testing.T) {
	home := threadHome(t, "qa")
	f := holdFake(busReply, 49*time.Minute, t0.Add(-50*time.Minute))
	testutil.Write(t, filepath.Join(home, "bus", "quotes.log"),
		stamp(t0.Add(-51*time.Minute))+" INFO quotes:qa -> crius:lead — waiter re-armed\n"+
			stamp(t0.Add(-49*time.Minute))+" READY quotes:build -> quotes:qa — R3.C4 is yours\n")
	runOnce(t, home, f, t0)
	w := t3Wakes(f)
	t.Logf("verdict: %q", w)
	if len(w) != 1 || !strings.Contains(w[0], "READY line to it") {
		t.Fatalf("want the alert naming the READY line its live waiter slept through: %q", w)
	}
}

// Waiting on the bus with no item is held as long as an item in flight: past t3WaitingFor quiet, one alert says so.
func TestT3WaiterHoldWaitingOnTheBusAlertsPastTwoHours(t *testing.T) {
	home := threadHome(t, "qa")
	f := holdFake(busReply, 121*time.Minute, t0.Add(-122*time.Minute))
	runOnce(t, home, f, t0)
	w := t3Wakes(f)
	t.Logf("verdict: %q", w)
	if len(w) != 1 || !strings.Contains(w[0], "WAITING ON ITS BUS") || !strings.Contains(w[0], "team-wait.sh (pid 102)") ||
		!strings.Contains(w[0], "for 121 min") {
		t.Fatalf("quiet 121 min on the bus: want one alert: %q", w)
	}
}

// crius-wakewatch waiting on the 11-19 shapes: the morning demo (go test ./internal/wakewatch -run TestT3WaiterHold -v).
func TestT3WaiterHoldWaitingPrintsTheVerdicts(t *testing.T) {
	minute := t0.Add(-47 * time.Minute)
	for _, c := range []struct {
		name string
		f    *fake
		bus  string
		want string
	}{
		{"qa-2 19:51, live waiter, no item", holdFake(busReply, 49*time.Minute, t0.Add(-51*time.Minute)),
			stamp(t0.Add(-51*time.Minute)) + " INFO quotes:qa -> crius:lead — Re 19:0x: team-wait re-armed. No item waits for qa.\n",
			"quotes:qa WAITING ON ITS BUS (no item in flight), bus waiter team-wait.sh (pid 102); idle 49 min after ⏳"},
		{"qa 20:57, VERIFIED in the waiter's first minute", holdFake("Landing checked. ⏳ Waiting on the bus watcher for the next READY or DONE.", 47*time.Minute-40*time.Second, minute.Add(30*time.Second)),
			stamp(minute) + " VERIFIED crius:lead -> quotes:qa — tip tree = QA land tree\n" + stamp(minute) + " INFO quotes:qa -> crius:lead — LANDING OK\n",
			"quotes:qa WAITING ON ITS BUS (no item in flight), bus waiter team-wait.sh (pid 102); idle 46 min after ⏳"},
		{"waiter died at its 2 h cap", holdFake(busReply, 49*time.Minute, time.Time{}), "",
			"quotes:qa not waiting: no bus waiter runs under its claude; idle 49 min after ⏳"},
		{"READY after the waiter started, waiter still alive", holdFake(busReply, 49*time.Minute, t0.Add(-50*time.Minute)),
			stamp(t0.Add(-49*time.Minute)) + " READY quotes:build -> quotes:qa — R3.C4 is yours\n",
			"quotes:qa not waiting: quotes:build's READY line to it at 21:11 came after its newest line, and its waiter has not moved it"},
	} {
		home := threadHome(t, "qa")
		testutil.Write(t, filepath.Join(home, "bus", "quotes.log"), c.bus)
		var b strings.Builder
		code := Waiting(c.f.deps(home, t0), &b, []string{"quotes:qa"})
		t.Logf("%-50s -> %s", c.name, strings.TrimSpace(b.String()))
		if code != 0 || !strings.HasPrefix(b.String(), c.want) {
			t.Errorf("%s: exit %d, got %q, want it to start %q", c.name, code, b.String(), c.want)
		}
	}
}

// Review P1-1: the 20:57 shape where the VERIFIED line and the role's own line name its row: a landed line its turn read
// before it started the waiter and replied is no sign it should have moved.
func TestT3WaiterHoldALandedLineReadBeforeTheReplyDoesNotEndTheHold(t *testing.T) {
	home := threadHome(t, "qa")
	minute := t0.Add(-47 * time.Minute)
	f := holdFake("Landing of row F-118 checked. ⏳ Waiting on the bus watcher for the next READY or DONE.", 47*time.Minute-40*time.Second, minute.Add(30*time.Second))
	testutil.Write(t, filepath.Join(home, "bus", "quotes.log"),
		stamp(minute)+" VERIFIED crius:lead -> quotes:build, quotes:qa — row F-118 · tip tree = QA land tree\n"+
			stamp(minute)+" INFO quotes:qa -> crius:lead — LANDING OK row F-118\n")
	runOnce(t, home, f, t0)
	w := t3Wakes(f)
	t.Logf("verdict: %q", w)
	if len(w) != 0 {
		t.Fatalf("a landed line read before the reply ended the hold: %q", w)
	}
	runOnce(t, home, f, t0.Add(74*time.Minute))
	if w := t3Wakes(f); len(w) != 1 || !strings.Contains(w[0], "WAITING ON ITS BUS (no item in flight; named F-118)") {
		t.Fatalf("quiet over 2 h: want the bus alert naming the row: %q", w)
	}
}

// Review r2 P1-A: the VERIFIED line about its row woke it, its turn re-armed the waiter and replied, with no bus line of
// its own after the VERIFIED: a landed line from before the waiter started is no sign it should have moved.
func TestT3WaiterHoldALandedLineThatWokeItBeforeTheWaiterStartedDoesNotEndTheHold(t *testing.T) {
	home := threadHome(t, "qa")
	minute := t0.Add(-47 * time.Minute)
	f := holdFake("Done with that. ⏳ Waiting on the bus for the next READY.", 47*time.Minute-40*time.Second, minute.Add(30*time.Second))
	testutil.Write(t, filepath.Join(home, "bus", "quotes.log"),
		stamp(minute.Add(-3*time.Minute))+" PASS quotes:qa -> crius:lead — row F-118 PASS\n"+
			stamp(minute)+" VERIFIED crius:lead -> quotes:qa — row F-118 · tip tree = QA land tree\n")
	runOnce(t, home, f, t0)
	w := t3Wakes(f)
	t.Logf("verdict: %q", w)
	if len(w) != 0 {
		t.Fatalf("a landed line from before the waiter started ended the hold: %q", w)
	}
	// with the waiter's start not known (a remote or old process list), today's alert stays
	f = holdFake("Done with that. ⏳ Waiting on the bus for the next READY.", 47*time.Minute-40*time.Second, minute.Add(30*time.Second))
	f.procs[len(f.procs)-1].Start, f.procs[len(f.procs)-2].Start = time.Time{}, time.Time{}
	runOnce(t, threadHomeWithBus(t, filepath.Join(home, "bus", "quotes.log")), f, t0)
	if w := t3Wakes(f); len(w) != 1 || !strings.Contains(w[0], "its job may be gone") || !strings.Contains(w[0], "VERIFIED") {
		t.Fatalf("start not known: want today's alert naming the VERIFIED line: %q", w)
	}
}

// threadHomeWithBus is threadHome for quotes:qa with the bus log copied from src.
func threadHomeWithBus(t *testing.T, src string) string {
	t.Helper()
	home := threadHome(t, "qa")
	testutil.Write(t, filepath.Join(home, "bus", "quotes.log"), testutil.Read(t, src))
	return home
}
