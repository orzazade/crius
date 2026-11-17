package wakewatch

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/testutil"
)

// 11-18 01:29 to 03:xx: wakewatch said "its job may be gone … none of <item> is in flight" five times for two roles that
// were idle on purpose: each had a live team-wait.sh under its claude, and one had said on the bus that it had no work
// ("Idle: no <role> NEED/READY"), the other that it waited for a time it named ("The 08:00 check … is scheduled"). Such
// a role is reported once as IDLE, waiting on purpose, never as a lost job. These fixtures are made up.

const purposeReply = "Files re-read; history not rescanned yet.\n\n⏳ The 08:00 check of the history count is scheduled; the bus listener is running."

// onPurposeFake is quotes:<role>'s thread idle on reply, its claude (pid 100) with a tool shell running team-wait.sh.
func onPurposeFake(role, reply string, idle time.Duration) *fake {
	return &fake{
		threads: map[string]T3Thread{"th-" + role: {Status: "completed", Updated: t0.Add(-idle), LastReply: reply}},
		ses:     []Session{{ID: "s-" + role, PID: 100, Status: "idle"}},
		procs: []Proc{
			{PID: 1, PPID: 0, Command: "/sbin/launchd"},
			{PID: 100, PPID: 1, Command: "claude --output-format stream-json"},
			{PID: 101, PPID: 100, Command: "/bin/zsh -c source /h/.claude/shell-snapshots/snapshot-zsh-1.sh 2>/dev/null || true && eval 'bash /h/.crius/bin/team-wait.sh quotes " + role + "'"},
			{PID: 102, PPID: 101, Command: "bash /h/.crius/bin/team-wait.sh quotes " + role},
			{PID: 103, PPID: 102, Command: "tail -n 0 -F /h/.crius/bus/quotes.log"},
		},
	}
}

// leadHome is threadHome for quotes:lead in a thread of its own (Crius runs in th-crius), with its newest bus line naming
// a run (1306) that is in no heavy run and on no other role's line. A lead's waiter wakes on any line from another role.
func leadHome(t *testing.T) string {
	home := threadHome(t, "lead")
	testutil.Write(t, filepath.Join(home, "state", "crius", "t3-thread"), "th-crius\n")
	testutil.Write(t, filepath.Join(home, "bus", "quotes.log"),
		stamp(t0.Add(-65*time.Minute))+" INFO quotes:lead -> crius:lead — Ack. The 00:45 run should claim it. History count waits for the scan queue; 08:00 check.\n"+
			stamp(t0.Add(-52*time.Minute))+" INFO quotes:lead -> crius:lead — Sync run 1306 succeeded: files re-read, 147 secrets. History not rescanned yet.\n")
	return home
}

// Its ⏳ reply says it waits for a time it named. One IDLE report, and a later turn that re-arms its waiter with the same
// words says nothing new.
func TestARoleWaitingForATimeItNamedWithALiveWaiterIsIdleOnPurpose(t *testing.T) {
	home := leadHome(t)
	f := onPurposeFake("lead", purposeReply, 50*time.Minute)
	runOnce(t, home, f, t0)
	w := t3Wakes(f)
	if len(w) != 1 || strings.Contains(w[0], "its job may be gone") {
		t.Fatalf("want one IDLE report, not a lost job: %q", w)
	}
	for _, want := range []string{"quotes · lead", "IDLE, waiting on purpose", "The 08:00 check of the history count is scheduled", "team-wait.sh (pid 102)"} {
		if !strings.Contains(w[0], want) {
			t.Errorf("report %q lacks %q", w[0], want)
		}
	}
	// 11-18 03:xx: a turn that restarted its waiter ended on the same ⏳ words; that idle spell is not a second report
	f.threads["th-lead"] = T3Thread{Status: "completed", Updated: t0.Add(10 * time.Minute),
		LastReply: "The bus listener hit its time limit, so I started it again.\n\n⏳ The 08:00 check of the history count is scheduled; the bus listener is running."}
	runOnce(t, home, f, t0.Add(61*time.Minute))
	runOnce(t, home, f, t0.Add(90*time.Minute))
	if w := t3Wakes(f); len(w) != 1 {
		t.Fatalf("a second idle spell waiting for the same named check reported again: %q", w)
	}
}

// Its newest bus line says it has no work ("Idle: no build NEED/READY"); its ⏳ reply names nothing in flight.
func TestARoleThatSaidItHasNoWorkWithALiveWaiterIsIdleOnPurpose(t *testing.T) {
	home := threadHome(t, "qa")
	f := onPurposeFake("qa", "Resumed fresh. Next is the partners fix, once build posts it. ⏳", 50*time.Minute)
	testutil.Write(t, filepath.Join(home, "bus", "quotes.log"),
		stamp(t0.Add(-55*time.Minute))+" PASS quotes:qa -> quotes:build,crius:lead — PASS 8384 R27.E at bc56f6d8\n"+
			stamp(t0.Add(-50*time.Minute))+" INFO quotes:qa -> crius:lead — qa resumed fresh from NOTES-qa.md. Smoke OK (lane 2 runner 35059 idle 358/358). Idle: no build NEED/READY; 8385 R9.F8 is quotes:qa-b's. Bus waiter armed.\n")
	runOnce(t, home, f, t0)
	w := t3Wakes(f)
	if len(w) != 1 || strings.Contains(w[0], "its job may be gone") {
		t.Fatalf("want one IDLE report, not a lost job: %q", w)
	}
	for _, want := range []string{"quotes · qa", "IDLE, waiting on purpose", "Idle: no build NEED/READY", "team-wait.sh (pid 102)"} {
		if !strings.Contains(w[0], want) {
			t.Errorf("report %q lacks %q", w[0], want)
		}
	}
}

// Negative control: the same words with no live waiter under its claude. Nothing will wake it, so today's alert stays.
func TestAnIdleOnPurposeRoleWithNoWaiterStillAlerts(t *testing.T) {
	home := leadHome(t)
	f := onPurposeFake("lead", purposeReply, 50*time.Minute)
	f.procs = slices.DeleteFunc(f.procs, func(p Proc) bool { return p.PID >= 101 })
	runOnce(t, home, f, t0)
	w := t3Wakes(f)
	if len(w) != 1 || !strings.Contains(w[0], "its job may be gone") || !strings.Contains(w[0], "no bus waiter") {
		t.Fatalf("want today's alert saying no waiter runs: %q", w)
	}
}

// A line that should have woken its waiter still ends the hold: a role that said it was idle and was then handed work
// it did not pick up is what the alert is for.
func TestALineToAnIdleOnPurposeRoleStillAlerts(t *testing.T) {
	home := threadHome(t, "qa")
	f := onPurposeFake("qa", "Idle until build posts its next READY. ⏳", 50*time.Minute)
	testutil.Write(t, filepath.Join(home, "bus", "quotes.log"),
		stamp(t0.Add(-60*time.Minute))+" INFO quotes:qa -> crius:lead — Idle: no build NEED/READY.\n"+
			stamp(t0.Add(-30*time.Minute))+" READY quotes:build -> quotes:qa — 5521 ready\n")
	runOnce(t, home, f, t0)
	if w := t3Wakes(f); len(w) != 1 || !strings.Contains(w[0], "its job may be gone") || !strings.Contains(w[0], "READY line to it") {
		t.Fatalf("a READY to qa after it said it was idle was held: %q", w)
	}
}

// Words that only say it waits on an item (a clause that names one) are no purpose, nor is a clock time long past, nor
// "idle" or "need" about something else (review P1-1): an item that is not in flight keeps today's alert. t0 is 22:00,
// the reply came at 21:10.
func TestPurposeIn(t *testing.T) {
	for text, want := range map[string]string{
		purposeReply: "The 08:00 check of the history count is scheduled",
		"Smoke OK (lane 2 runner 35059 idle 358/358). Idle: no build NEED/READY; 8385 is qa-b's.": "Idle: no build NEED/READY",
		"History count waits for the scan queue; 08:00 check.":                                    "08:00 check",
		"Nothing to do until build posts. ⏳":                                                      "Nothing to do until build posts",
		"Waiting for the lead's reply on the fixture. ⏳":                                          "Waiting for the lead's reply on the fixture",
		"Idle until build posts its next READY. ⏳":                                                "Idle until build posts its next READY",
		"⏳ The 21:30 check of the history count is scheduled":                                     "The 21:30 check of the history count is scheduled",
		waitReply:                                                                 "",
		"waiting on 5521":                                                         "",
		"Suite 3 of 6 at 23:10. ⏳":                                                "",
		"Runner 35059 idle 358/358 ⏳":                                             "",
		"⏳ Waiting for 5521's suite to go green":                                  "",
		"⏳ Waiting on CI to go through":                                           "",
		"⏳ Waiting for the heavy run of 5521 (started 23:10)":                     "",
		"Resumed at 23:10. ⏳":                                                     "",
		"ETA 23:40 for the 5521 suite, I'll check then. ⏳":                        "",
		"Fixed the waiting_on.go 12:34 build error":                               "",
		"Review fixes pushed; nothing left from the review. ⏳ Waiting on CI":      "",
		"0 failures, no tasks skipped. ⏳ suite running":                           "",
		"Lane 2 is now idle; 5521 queued for it. ⏳":                               "",
		"Idle time was 3 min":                                                     "",
		"There is no further need to rebase 5521":                                 "",
		"There is no further need to rebase":                                      "",
		"⏳ The 21:00 check of the history count is scheduled (passed 15 min ago)": "",
		"No work was lost in the rebase":                                          "",
		"No work left; the partners fix waits on build. ⏳":                        "No work left",
	} {
		if got := purposeIn(text, t0.Add(-50*time.Minute), t0); got != want {
			t.Errorf("purposeIn(%q) = %q, want %q", text, got, want)
		}
	}
}

// A named time long past is no purpose: the check should have run and moved the role (review P2-2).
func TestAPassedNamedTimeIsNoPurpose(t *testing.T) {
	home := leadHome(t)
	f := onPurposeFake("lead", "Files re-read.\n\n⏳ The 21:00 check of the history count is scheduled; the bus listener is running.", 50*time.Minute)
	runOnce(t, home, f, t0)
	if w := t3Wakes(f); len(w) != 1 || !strings.Contains(w[0], "its job may be gone") || !strings.Contains(w[0], "none of 1306 is in flight") {
		t.Fatalf("a check named for 21:00, at 22:00 not run: want today's alert: %q", w)
	}
}

// An item in flight comes before purpose words: WAITING ON holds it with no alert until t3WaitingFor (review P2-3).
func TestAnItemInFlightComesBeforePurposeWords(t *testing.T) {
	home := threadHome(t, "qa")
	f := onPurposeFake("qa", "Build has 5521 in its heavy run. ⏳ Nothing to do until it lands.", 50*time.Minute)
	f.procs = append(f.procs, heavyRun)
	runOnce(t, home, f, t0)
	if w := t3Wakes(f); len(w) != 0 {
		t.Fatalf("a role waiting on 5521 in a heavy run was reported: %q", w)
	}
	var b strings.Builder
	Waiting(f.deps(home, t0), &b, []string{"quotes:qa"})
	if !strings.Contains(b.String(), "WAITING ON 5521") {
		t.Fatalf("waiting: %q", b.String())
	}
}

// An older "Idle" bus line does not speak for a later reply that took up an item (review P2-1), and one days old says
// nothing of now (review P3-1): today's alert in both.
func TestAnOldIdleBusLineIsNoPurpose(t *testing.T) {
	for _, c := range []struct {
		name, line, reply, want string
	}{
		{"older than a reply that names an item", stamp(t0.Add(-100 * time.Minute)), "Took 5521 from build. ⏳ Suite for 5521 running in the background.", "none of 5521 is in flight"},
		{"days old", stamp(t0.Add(-72 * time.Hour)), "Resumed fresh. Next is the partners fix, once build posts it. ⏳", "name no item"},
	} {
		home := threadHome(t, "qa")
		f := onPurposeFake("qa", c.reply, 50*time.Minute)
		testutil.Write(t, filepath.Join(home, "bus", "quotes.log"), c.line+" INFO quotes:qa -> crius:lead — Idle: no build NEED/READY. Bus waiter armed.\n")
		runOnce(t, home, f, t0)
		if w := t3Wakes(f); len(w) != 1 || !strings.Contains(w[0], "its job may be gone") || !strings.Contains(w[0], c.want) {
			t.Errorf("%s: want today's alert naming %q: %q", c.name, c.want, w)
		}
	}
}

// crius-wakewatch waiting prints a role idle on purpose with what it said and its waiter.
func TestWaitingPrintsARoleIdleOnPurpose(t *testing.T) {
	home := leadHome(t)
	f := onPurposeFake("lead", purposeReply, 50*time.Minute)
	var b strings.Builder
	if code := Waiting(f.deps(home, t0), &b, nil); code != 0 {
		t.Fatalf("exit %d: %s", code, b.String())
	}
	want := `quotes:lead IDLE on purpose: "The 08:00 check of the history count is scheduled", bus waiter team-wait.sh (pid 102); idle 50 min after ⏳, said once after 45 min (T3 thread th-lead)` + "\n"
	if b.String() != want {
		t.Fatalf("got %q, want %q", b.String(), want)
	}
}

// Review r2 P2-2: hours 08 and 09 are read as 8 and 9 (fmt.Sscan read them as octal 0). A reply at 01:29 naming the
// 08:00 check holds it until 08:45 and no longer.
func TestANamedTimeInHoursEightAndNineIsReadRight(t *testing.T) {
	since := time.Date(2026, 11, 18, 1, 29, 0, 0, time.Local)
	for _, c := range []struct {
		now  time.Time
		want string
	}{
		{since.Add(50 * time.Minute), "The 08:00 check is scheduled"},
		{time.Date(2026, 11, 18, 8, 44, 0, 0, time.Local), "The 08:00 check is scheduled"},
		{time.Date(2026, 11, 18, 9, 0, 0, 0, time.Local), ""},
		{time.Date(2026, 11, 18, 23, 0, 0, 0, time.Local), ""},
	} {
		if got := purposeIn("⏳ The 08:00 check is scheduled", since, c.now); got != c.want {
			t.Errorf("at %s: purposeIn = %q, want %q", c.now.Format("15:04"), got, c.want)
		}
	}
	if got := purposeIn("⏳ The 09:09 check is scheduled", since, time.Date(2026, 11, 18, 9, 30, 0, 0, time.Local)); got == "" {
		t.Error("09:09 at 09:30 is under 45 min past: still a purpose")
	}
}

// Review r2 P2-4: the persona's own format puts ⏳ alone in the last paragraph; the purpose sits in the one before it.
// A paragraph after the status ("Sources: …") does not hide it either.
func TestStatusPartFindsThePurposeBesideAnEmojiAlone(t *testing.T) {
	for _, reply := range []string{
		"Files re-read.\n\nThe 08:00 check of the history count is scheduled.\n\n⏳",
		"Files re-read.\n\n⏳ The 08:00 check of the history count is scheduled.\n\nSources: notes.md",
	} {
		if got := purposeIn(statusPart(reply), t0.Add(-50*time.Minute), t0); got != "The 08:00 check of the history count is scheduled" {
			t.Errorf("statusPart(%q): purpose %q", reply, got)
		}
	}
}

// Review r2 P3-a: a time in the bus line is read from when the line was posted, not from the later reply.
func TestABusLineTimeIsReadFromTheLine(t *testing.T) {
	home := threadHome(t, "qa")
	f := onPurposeFake("qa", "Resumed fresh. Next is the partners fix, once build posts it. ⏳", 50*time.Minute)
	testutil.Write(t, filepath.Join(home, "bus", "quotes.log"),
		stamp(t0.Add(-64*time.Minute))+" INFO quotes:qa -> crius:lead — The 20:00 check of the fixture is scheduled.\n")
	runOnce(t, home, f, t0)
	if w := t3Wakes(f); len(w) != 1 || !strings.Contains(w[0], "its job may be gone") {
		t.Fatalf("a 20:00 check named at 20:56 (read from the 21:10 reply it would be tomorrow), at 22:00 long past, was held: %q", w)
	}
}

// Review r3 P1: an item it waits on that landed (a DONE its waiter does not wake for) should have put it to work: purpose
// words in the reply do not hide today's alert naming the DONE.
func TestPurposeWordsDoNotHideALandedItem(t *testing.T) {
	home := threadHome(t, "qa", "build")
	f := onPurposeFake("qa", "Took 5521 from build.\n\n⏳ Waiting for 5521's suite. Nothing to do until then.", 50*time.Minute)
	f.threads["th-build"] = T3Thread{Status: "running", Active: true, Updated: t0}
	testutil.Write(t, filepath.Join(home, "bus", "quotes.log"), stamp(t0.Add(-40*time.Minute))+" DONE quotes:build -> crius:lead — 5521 suite green\n")
	runOnce(t, home, f, t0)
	if w := t3Wakes(f, "quotes · qa"); len(w) != 1 || !strings.Contains(w[0], "its job may be gone") || !strings.Contains(w[0], "landed: 5521: quotes:build posted DONE") {
		t.Fatalf("want today's alert naming the DONE: %q", w)
	}
}

// Review r3 P2: an "Idle" bus line from before the turn that ended on ⏳ does not speak for a reply that took up work
// but names no item.
func TestAnIdleLineFromAnEarlierTurnIsNoPurpose(t *testing.T) {
	home := threadHome(t, "qa")
	f := onPurposeFake("qa", "Started the full regression suite in the background; I will report when it ends. ⏳", 50*time.Minute)
	testutil.Write(t, filepath.Join(home, "bus", "quotes.log"), stamp(t0.Add(-80*time.Minute))+" INFO quotes:qa -> crius:lead — Idle: no build NEED/READY.\n")
	runOnce(t, home, f, t0)
	if w := t3Wakes(f); len(w) != 1 || !strings.Contains(w[0], "its job may be gone") {
		t.Fatalf("an Idle line 30 min before the reply held the alert: %q", w)
	}
}
