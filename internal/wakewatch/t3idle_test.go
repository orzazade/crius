package wakewatch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/sessionstate"
	"github.com/orzazade/crius/internal/testutil"
)

// threadHome is a home with quotes' bus log and, for each role, a role file and a session file s-<role> that names its
// T3 Code thread th-<role> (the role file names none, as crius-hook leaves it for a session that runs on). quotes:lead
// runs in Crius's own thread.
func threadHome(t *testing.T, roles ...string) string {
	t.Helper()
	home := setup(t, map[string]string{"quotes.log": ""})
	for _, r := range roles {
		testutil.Write(t, filepath.Join(home, "roles", "quotes."+r+".json"),
			`{"session_id":"s-`+r+`","project":"quotes","role":"`+r+`","time":"2026-10-21T21:00:00Z"}`)
		testutil.Write(t, filepath.Join(home, "sessions", "s-"+r+".json"), `{"session_id":"s-`+r+`","t3_thread":"th-`+r+`"}`)
	}
	testutil.Write(t, filepath.Join(home, "state", "crius", "t3-thread"), "th-lead\n")
	return home
}

func t3Wakes(f *fake, has ...string) []string {
	var out []string
	for _, s := range f.sent {
		if strings.Contains(s, "T3 thread") && (has == nil || strings.Contains(s, has[0])) {
			out = append(out, s)
		}
	}
	return out
}

func runOnce(t *testing.T, home string, f *fake, now time.Time) {
	t.Helper()
	if err := Run(f.deps(home, now)); err != nil {
		t.Fatal(err)
	}
}

const longReply = "I posted my status to Crius on the bus. The G150 suite is green on 1c6e705 and the PR is open. Next is O9. ✅"

// 11-15: AXI api-split, ms-flow and qa, each a T3 Code thread, ended their turns and sat idle 13 to 50 min while they
// owed work; nothing watches a thread, so Crius found them by hand at 04:05 and 05:48. A thread that is not running,
// idle over 10 min, whose last reply ends on neither ⏳ nor ❓ wakes Crius once per idle spell. A thread that runs, one
// that waits on its own named job (⏳, under 45 min) and Crius's own thread stay quiet.
func TestAnIdleT3ThreadWakesCriusOncePerIdleSpell(t *testing.T) {
	home := threadHome(t, "api", "ms", "qa", "lead")
	f := &fake{threads: map[string]T3Thread{
		"th-api":  {Status: "completed", Updated: t0.Add(-20 * time.Minute), LastReply: longReply},
		"th-ms":   {Status: "completed", Updated: t0.Add(-20 * time.Minute), LastReply: "G150 runs as job bmjw2a8vk. ⏳"},
		"th-qa":   {Status: "running", Active: true, Updated: t0.Add(-30 * time.Minute), LastReply: "done ✅"},
		"th-lead": {Status: "completed", Updated: t0.Add(-time.Hour), LastReply: "done ✅"},
	}}
	runOnce(t, home, f, t0)
	w := t3Wakes(f)
	if len(w) != 1 {
		t.Fatalf("want one T3 wake, got %q", w)
	}
	tail := string([]rune(longReply)[len([]rune(longReply))-80:])
	for _, want := range []string{"quotes · api", "T3 thread th-api", "idle 20 min", tail} {
		if !strings.Contains(w[0], want) {
			t.Errorf("wake %q lacks %q", w[0], want)
		}
	}
	if strings.Contains(w[0], longReply[:20]) {
		t.Errorf("wake %q quotes more than the reply's last 80 chars", w[0])
	}
	for _, id := range f.read {
		if id == "th-lead" {
			t.Errorf("read Crius's own thread")
		}
	}
	runOnce(t, home, f, t0.Add(5*time.Minute))
	runOnce(t, home, f, t0.Add(30*time.Minute))
	if w := t3Wakes(f, "th-api"); len(w) != 1 {
		t.Fatalf("one idle spell woke Crius %d times: %q", len(w), w)
	}
	// it worked again and stopped again: a new spell
	f.threads["th-api"] = T3Thread{Status: "completed", Updated: t0.Add(40 * time.Minute), LastReply: "next one done ✅"}
	runOnce(t, home, f, t0.Add(55*time.Minute))
	if w := t3Wakes(f, "th-api"); len(w) != 2 || !strings.Contains(w[1], "idle 15 min") {
		t.Fatalf("a second idle spell: %q", w)
	}
}

// A thread under 10 min idle, one that asks the owner (❓) for an hour, and a role closed on purpose stay quiet.
func TestAT3ThreadThatAsksOrIsYoungOrClosedStaysQuiet(t *testing.T) {
	home := threadHome(t, "api", "ms", "qa")
	testutil.Write(t, filepath.Join(home, "roles", "quotes.qa.closed"), "s-qa\n")
	f := &fake{threads: map[string]T3Thread{
		"th-api": {Status: "completed", Updated: t0.Add(-9 * time.Minute), LastReply: "done ✅"},
		"th-ms":  {Status: "completed", Updated: t0.Add(-time.Hour), LastReply: "Which tenant do I use? ❓"},
		"th-qa":  {Status: "completed", Updated: t0.Add(-time.Hour), LastReply: "all suites green ✅"},
	}}
	runOnce(t, home, f, t0)
	if w := t3Wakes(f); len(w) != 0 {
		t.Fatalf("woke Crius: %q", w)
	}
}

// RULES §3.5: ⏳ means the thread's own named job runs. Past 45 min idle with no bus line from the role since, the job
// is likely gone: Crius is woken. A bus line from it in that time says it is still at work.
func TestAT3ThreadOnAnHourglassWakesCriusAfter45MinWithNoBusLine(t *testing.T) {
	home := threadHome(t, "ms", "qa")
	f := &fake{threads: map[string]T3Thread{
		"th-ms": {Status: "completed", Updated: t0.Add(-46 * time.Minute), LastReply: "G150 runs as job bmjw2a8vk. ⏳"},
		"th-qa": {Status: "completed", Updated: t0.Add(-46 * time.Minute), LastReply: "suite 3 of 5 runs. ⏳"},
	}}
	testutil.Write(t, filepath.Join(home, "bus", "quotes.log"),
		stamp(t0.Add(-10*time.Minute))+" INFO quotes:qa -> quotes:lead — suite 4 of 5 green\n")
	runOnce(t, home, f, t0.Add(-2*time.Minute)) // 44 min: still within its job's time
	if w := t3Wakes(f); len(w) != 0 {
		t.Fatalf("woke before 45 min: %q", w)
	}
	runOnce(t, home, f, t0)
	w := t3Wakes(f)
	if len(w) != 1 || !strings.Contains(w[0], "quotes · ms") || !strings.Contains(w[0], "idle 46 min") ||
		!strings.Contains(w[0], "⏳") || !strings.Contains(w[0], "no bus line") {
		t.Fatalf("want one wake for ms: %q", w)
	}
}

// "Failed to look" is not "found nothing": a t3 call that fails wakes Crius with the role and the error.
func TestAT3ReadFailureIsLoud(t *testing.T) {
	home := threadHome(t, "api")
	f := &fake{threadErr: errors.New("t3 thread show: exit status 1: Thread th-api is no longer available.")}
	runOnce(t, home, f, t0)
	var hit []string
	for _, s := range f.sent {
		if strings.Contains(s, "T3 thread idle check failed to look at") {
			hit = append(hit, s)
		}
	}
	if len(hit) != 1 || !strings.Contains(hit[0], "quotes · api") || !strings.Contains(hit[0], "no longer available") {
		t.Fatalf("want one loud failure: %q (all: %q)", hit, f.sent)
	}
}

// 11-15 06:0x, live: wakewatch said "cortex · lead (session 9d490483…) has had no live Claude since 05:38:
// crius-spawn -resume cortex lead". Cortex lead is a T3 thread: no Claude runs between its turns, and -resume would open
// a cmux tab. A role whose current session runs in a T3 thread gets the thread check, not the dead-role check.
func TestAT3ThreadRoleIsNotADeadRole(t *testing.T) {
	home := roleHome(t, role("lead", "s1", t0.Add(-time.Hour)))
	testutil.Write(t, filepath.Join(home, "sessions", "s1.json"), `{"session_id":"s1","t3_thread":"th-lead"}`)
	f := &fake{threads: map[string]T3Thread{"th-lead": {Status: "running", Active: true, Updated: t0}}}
	deadAt(t, home, f, func(m int) []Session {
		if m == 0 {
			return []Session{{Name: "quotes · lead", ID: "s1"}}
		}
		return nil
	}, 0, 5, 16, 30)
	if w := deadWakes(f); len(w) != 0 {
		t.Fatalf("a T3 thread role was called dead: %q", w)
	}
	if l := leadLines(t, home); len(l) != 0 {
		t.Fatalf("its lead was told: %q", l)
	}
}

// Review of 595471e: a message to the thread after its last reply that got no answer is owed work whatever that reply's
// emoji, and a request (an approval or a question) open over 10 min stops a thread that still shows a run.
func TestAT3ThreadWithAnUnansweredMessageOrAnOpenRequestWakesCrius(t *testing.T) {
	home := threadHome(t, "api", "ms")
	f := &fake{threads: map[string]T3Thread{
		"th-api": {Status: "completed", Updated: t0.Add(-20 * time.Minute), LastReply: "Which tenant? ❓", Unanswered: true},
		"th-ms":  {Status: "running", Active: true, Pending: 1, Updated: t0.Add(-20 * time.Minute), LastReply: "running the suite"},
	}}
	runOnce(t, home, f, t0)
	if w := t3Wakes(f, "th-api"); len(w) != 1 || !strings.Contains(w[0], "a message to it after its last reply has no answer") {
		t.Errorf("unanswered: %q", w)
	}
	if w := t3Wakes(f, "th-ms"); len(w) != 1 || !strings.Contains(w[0], "waits on 1 open request(s)") {
		t.Errorf("open request: %q", w)
	}
}

// Review of 595471e: a bus line long ago does not keep a ⏳ thread quiet for ever; its 45 min count from that line.
func TestAnOldBusLineDoesNotSilenceAnHourglassThread(t *testing.T) {
	home := threadHome(t, "ms")
	f := &fake{threads: map[string]T3Thread{
		"th-ms": {Status: "completed", Updated: t0.Add(-4 * time.Hour), LastReply: "G150 runs as job bmjw2a8vk. ⏳"},
	}}
	testutil.Write(t, filepath.Join(home, "bus", "quotes.log"),
		stamp(t0.Add(-230*time.Minute))+" INFO quotes:ms -> quotes:lead — G150 3 of 5 green\n")
	runOnce(t, home, f, t0)
	if w := t3Wakes(f, "th-ms"); len(w) != 1 || !strings.Contains(w[0], "idle 240 min") || !strings.Contains(w[0], "no bus line from it for 230 min") {
		t.Fatalf("got %q", w)
	}
}

// Review of 595471e: a role whose WAIT still runs stays quiet, as in the tab check; one with no reply in the messages read
// says so.
func TestAT3ThreadOnAWaitStaysQuietAndAnEmptyReplyIsNamed(t *testing.T) {
	home := threadHome(t, "api", "ms")
	f := &fake{threads: map[string]T3Thread{
		"th-api": {Status: "completed", Updated: t0.Add(-30 * time.Minute), LastReply: "done ✅"},
		"th-ms":  {Status: "completed", Updated: t0.Add(-30 * time.Minute)},
	}}
	testutil.Write(t, filepath.Join(home, "bus", "quotes.log"),
		stamp(t0.Add(-31*time.Minute))+" WAIT quotes:api -> quotes:lead — waiting for the env004 deploy\n")
	runOnce(t, home, f, t0)
	if w := t3Wakes(f, "th-api"); len(w) != 0 {
		t.Errorf("a running WAIT woke Crius: %q", w)
	}
	if w := t3Wakes(f, "th-ms"); len(w) != 1 || !strings.Contains(w[0], "No reply in its latest messages.") {
		t.Errorf("empty reply: %q", w)
	}
}

// Review of 595471e: each role's failure has its own key, so one does not hold back another's, and once t3 hangs the
// other threads are not read on that run (each call would wait 10 s).
func TestAHungT3StopsTheRunsOtherReadsAndEachRoleFailsOnItsOwn(t *testing.T) {
	home := threadHome(t, "api", "ms")
	f := &fake{threadErr: fmt.Errorf("t3 thread show: %w (10s)", ErrT3NoAnswer)}
	runOnce(t, home, f, t0)
	if len(f.read) != 1 {
		t.Errorf("read %d threads after t3 hung: %q", len(f.read), f.read)
	}
	var hit []string
	for _, s := range f.sent {
		if strings.Contains(s, "T3 thread idle check failed to look at") {
			hit = append(hit, s)
		}
	}
	if len(hit) != 2 || !strings.Contains(strings.Join(hit, "\n"), "crius-spawn -close quotes") {
		t.Fatalf("want one failure per role: %q", hit)
	}
}

// Review of 595471e: a T3 lead whose GOAL says Done: 100% is finished, not idle, as in the tab check; one with work left
// still wakes Crius.
func TestAFinishedT3LeadStaysQuiet(t *testing.T) {
	for done, want := range map[string]int{"100": 0, "80": 1} {
		home := threadHome(t, "lead")
		os.Remove(filepath.Join(home, "state", "crius", "t3-thread")) // Crius runs elsewhere: quotes:lead is a team thread
		status := filepath.Join(home, "quotes-STATUS.md")
		testutil.Write(t, filepath.Join(home, "projects.toml"), "[projects.quotes]\nleads = { lead = \""+status+"\" }\n")
		testutil.Write(t, status, "# Quotes\n\n## GOAL (2026-10-21 09:00)\nGoal: quotes ship.\n- [x] B1 · 9c644de\n"+
			"Done: "+done+"% (1 of 1 milestones)\nNext: nothing\nNeeds owner: nothing\n")
		f := &fake{threads: map[string]T3Thread{"th-lead": {Status: "completed", Updated: t0.Add(-30 * time.Minute), LastReply: "done ✅"}}}
		runOnce(t, home, f, t0)
		if w := t3Wakes(f, "th-lead"); len(w) != want {
			t.Errorf("Done: %s%%: want %d wakes, got %q", done, want, w)
		}
	}
}

// 11-17 15:30 live: "cortex · lead (T3 thread mcp:launch:6c403f7c…) is idle 758 min". That thread was settled in T3 at
// 22:51 the night before (the owner or Crius is done with it); the role's file still named its session. A thread settled
// or archived in T3 never wakes Crius, however long it sits.
func TestASettledT3ThreadNeverWakesCrius(t *testing.T) {
	home := threadHome(t, "api", "ms")
	f := &fake{threads: map[string]T3Thread{
		"th-api": {Status: "completed", Updated: t0.Add(-758 * time.Minute), LastReply: "pushed the fix ✅", Settled: true},
		"th-ms":  {Status: "completed", Updated: t0.Add(-time.Hour), LastReply: "Which tenant? ❓", Unanswered: true, Settled: true},
	}}
	runOnce(t, home, f, t0)
	if w := t3Wakes(f); len(w) != 0 {
		t.Fatalf("a settled thread woke Crius: %q", w)
	}
}

// When two threads are filed under one role, only the newest session's thread speaks for it: an older thread of the
// role never wakes Crius, and a settled newest thread (the role is done) does not hand the role back to an older one.
// A role whose newest session runs in a cmux tab is left to the tab checks, whatever thread it ran in before (review P1).
func TestOnlyTheNewestSessionsThreadOfARoleCounts(t *testing.T) {
	idle := T3Thread{Status: "completed", Updated: t0.Add(-30 * time.Minute), LastReply: "done ✅"}
	settled := idle
	settled.Settled = true
	for _, c := range []struct {
		name          string
		old, new      T3Thread
		newThread     string
		wantOld, want int
	}{
		{"old settled, new idle", settled, idle, "th-api2", 0, 1},
		{"both idle", idle, idle, "th-api2", 0, 1},
		{"new settled, old idle", idle, settled, "th-api2", 0, 0},
		{"both settled", settled, settled, "th-api2", 0, 0},
		{"new session in a tab, old idle", idle, idle, "", 0, 0},
	} {
		home := threadHome(t, "api")
		// crius-hook's files: a later session of quotes:api (in another thread, or in a tab) while the role's file still
		// names s-api, in th-api; and a session of another role in a third thread that is not quotes:api's
		f := &fake{threads: map[string]T3Thread{"th-api": c.old, "th-api2": c.new}, states: []sessionstate.State{
			{SessionID: "s-api", Project: "quotes", Role: "api", Thread: "th-api", Time: t0.Add(-2 * time.Hour)},
			{SessionID: "s-api2", Project: "quotes", Role: "api", Thread: c.newThread, Time: t0.Add(-time.Hour)},
			{SessionID: "s-x", Project: "quotes", Role: "unknown", Thread: "th-x", Time: t0},
		}}
		runOnce(t, home, f, t0)
		if w := t3Wakes(f, "th-api)"); len(w) != c.wantOld {
			t.Errorf("%s: want %d wakes for the older thread, got %q", c.name, c.wantOld, w)
		}
		if w := t3Wakes(f, "th-api2)"); len(w) != c.want {
			t.Errorf("%s: want %d wakes for the newer thread, got %q", c.name, c.want, w)
		}
		for _, id := range f.read {
			if id != "th-api2" {
				t.Errorf("%s: read thread %s, not the role's newest", c.name, id)
			}
		}
	}
}
