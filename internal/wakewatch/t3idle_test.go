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
	runLooked(t, f, f.deps(home, t0))
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
	runLooked(t, f, f.deps(home, t0))
	if len(f.read) != lookMisses { // one thread a run
		t.Errorf("read %d threads in %d runs after t3 hung: %q", len(f.read), lookMisses, f.read)
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

// Slow t3 answers that use up the run's time for t3 are no hang: the reads stop for the run (the time is gone), and
// what each role says blames the slow answers, never a hang.
func TestASpentT3BudgetStopsTheReadsWithoutBlamingAHang(t *testing.T) {
	home := threadHome(t, "api", "ms")
	f := &fake{threadErr: fmt.Errorf("t3 thread show: %w (4m0s)", ErrT3BudgetSpent)}
	runLooked(t, f, f.deps(home, t0))
	if len(f.read) != lookMisses { // one thread a run
		t.Errorf("read %d threads in %d runs after the budget was spent: %q", len(f.read), lookMisses, f.read)
	}
	var hit []string
	for _, s := range f.sent {
		if strings.Contains(s, "T3 thread idle check failed to look at") {
			hit = append(hit, s)
		}
	}
	all := strings.Join(hit, "\n")
	if len(hit) != 2 || strings.Count(all, "slow t3 answers") != 2 || strings.Contains(all, "no answer") {
		t.Fatalf("want one failure per role, each blaming slow answers, none a hang: %q", hit)
	}
}

// 11-20 00:1x live: "WAKEWATCH: crius · crius-runlog (T3 thread thread:delegated-task:…) is idle 13 min: not running, and
// its last reply ends on neither ⏳ nor ❓. Last reply: … ✅". That thread was a child Crius started with T3's delegate_task
// (relationshipToParent "subagent"); its run completed on ✅ and T3 handed the result to Crius. A finished child is done,
// not idle. A child whose run failed, one with an open request or an unanswered message, and one past 45 min on ⏳ still
// wake Crius as any thread does; a running child and one that asks the owner (❓) stay quiet, as any thread does.
func TestAFinishedDelegateChildStaysQuietAndOthersStillWake(t *testing.T) {
	const runlog = "commit attribution line was dropped because your no-AI-credit hook blocks it. ✅"
	home := threadHome(t, "done", "nomark", "run", "fail", "ask", "req", "msg", "plain", "job")
	idle := t0.Add(-13 * time.Minute)
	f := &fake{threads: map[string]T3Thread{
		"th-done":   {Status: "completed", Child: true, Updated: idle, LastReply: runlog},
		"th-nomark": {Status: "completed", Child: true, Updated: idle, LastReply: "built it, committed on night/x."},
		"th-run":    {Status: "running", Child: true, Updated: idle, LastReply: runlog},
		"th-fail":   {Status: "failed", Child: true, Updated: idle, LastReply: runlog},
		"th-ask":    {Status: "completed", Child: true, Updated: idle, LastReply: "Which branch do I build on? ❓"},
		"th-req":    {Status: "waiting", Child: true, Pending: 1, Updated: idle, LastReply: runlog},
		"th-msg":    {Status: "completed", Child: true, Unanswered: true, Updated: idle, LastReply: runlog},
		"th-plain":  {Status: "completed", Updated: idle, LastReply: runlog},
		"th-job":    {Status: "completed", Child: true, Updated: t0.Add(-46 * time.Minute), LastReply: "CI runs as job b7. ⏳"},
	}}
	runOnce(t, home, f, t0)
	for _, id := range []string{"th-done", "th-nomark", "th-run", "th-fail", "th-ask", "th-req", "th-msg", "th-plain", "th-job"} {
		verdict := "quiet"
		if w := t3Wakes(f, "T3 thread "+id+")"); len(w) > 0 {
			verdict = "WAKES: " + w[0]
		}
		th := f.threads[id]
		t.Logf("%-9s child=%-5v status=%-9s → %s", id, th.Child, th.Status, verdict) // the morning demo reads these with -v
	}
	for _, quiet := range []string{"th-done", "th-nomark", "th-ask"} {
		if w := t3Wakes(f, "T3 thread "+quiet+")"); len(w) != 0 {
			t.Errorf("%s woke Crius: %q", quiet, w)
		}
	}
	// th-run is not idle: a running thread never wakes Crius, child or not (it alerts later, once it stops)
	for _, loud := range []string{"th-fail", "th-req", "th-msg", "th-plain", "th-job"} {
		if w := t3Wakes(f, "T3 thread "+loud+")"); len(w) != 1 {
			t.Errorf("%s: want one wake, got %q", loud, w)
		}
	}
	if w := t3Wakes(f, "T3 thread th-run)"); len(w) != 0 {
		t.Errorf("a running child woke Crius: %q", w)
	}
}

// The same 00:1x alert said the reply "ends on neither ⏳ nor ❓" while it ended on ✅: true, but it hid the mark it did end
// on. The alert names the mark the reply ends on, or says it has none.
func TestAnIdleT3AlertNamesTheMarkTheReplyEndsOn(t *testing.T) {
	home := threadHome(t, "api", "ms")
	f := &fake{threads: map[string]T3Thread{
		"th-api": {Status: "completed", Updated: t0.Add(-13 * time.Minute), LastReply: "all done ✅"},
		"th-ms":  {Status: "failed", Updated: t0.Add(-13 * time.Minute), LastReply: "the build broke"},
	}}
	runOnce(t, home, f, t0)
	if w := t3Wakes(f, "th-api"); len(w) != 1 || !strings.Contains(w[0], "its last reply ends on ✅") {
		t.Errorf("a ✅ reply: %q", w)
	}
	if w := t3Wakes(f, "th-ms"); len(w) != 1 || !strings.Contains(w[0], "its last reply ends on no status mark") {
		t.Errorf("a reply with no mark: %q", w)
	}
}

// finishedChild alone: only a child whose run completed and that asks nothing and waits on no job of its own is done.
func TestFinishedChildIsOnlyACompletedChildThatAsksNothing(t *testing.T) {
	done := T3Thread{Status: "completed", Child: true, LastReply: "built it ✅"}
	for name, c := range map[string]struct {
		th   T3Thread
		want bool
	}{
		"completed on ✅":        {done, true},
		"completed, no mark":    {T3Thread{Status: "completed", Child: true, LastReply: "built it"}, true},
		"✅ then ❓ last":         {T3Thread{Status: "completed", Child: true, LastReply: "built ✅. Which branch? ❓"}, false},
		"❓ then ✅ last":         {T3Thread{Status: "completed", Child: true, LastReply: "Asked? ❓ Settled, built ✅"}, true},
		"ends on ⏳":             {T3Thread{Status: "completed", Child: true, LastReply: "CI runs ⏳"}, false},
		"not a child":           {T3Thread{Status: "completed", LastReply: "built it ✅"}, false},
		"failed":                {T3Thread{Status: "failed", Child: true, LastReply: "built it ✅"}, false},
		"interrupted":           {T3Thread{Status: "interrupted", Child: true, LastReply: "built it ✅"}, false},
		"cancelled":             {T3Thread{Status: "cancelled", Child: true, LastReply: "built it ✅"}, false},
		"idle":                  {T3Thread{Status: "idle", Child: true, LastReply: "built it ✅"}, false},
		"open run":              {T3Thread{Status: "completed", Child: true, Active: true, LastReply: "built it ✅"}, false},
		"open request":          {T3Thread{Status: "completed", Child: true, Pending: 1, LastReply: "built it ✅"}, false},
		"message after its end": {T3Thread{Status: "completed", Child: true, Unanswered: true, LastReply: "built it ✅"}, false},
		"no reply read":         {T3Thread{Status: "completed", Child: true}, false},
	} {
		if got := finishedChild(c.th); got != c.want {
			t.Errorf("%s: finishedChild = %v, want %v", name, got, c.want)
		}
	}
}

// 11-20: axi qa-2 and qa woke Crius 4 times as "idle N min after a ⏳ reply ... its job may be gone" while T3 still ran a
// background job of theirs (pendingBackgroundTasks: a bus waiter re-armed for 2 h, an awaited test run), so T3 would wake
// them when it ended. A thread with its own background job running is held as waiting, as a named item holds one, and
// wakes Crius once past t3WaitingFor, naming the job. One with no background job still alerts past 45 min.
func TestAT3ThreadWithItsOwnBackgroundJobIsHeldAsWaiting(t *testing.T) {
	home := threadHome(t, "qa2", "qa", "ms")
	reply := "Bus waiter re-armed for 2 h; I pick up the next NEED-QA when it fires. ⏳"
	f := &fake{threads: map[string]T3Thread{
		"th-qa2": {Status: "completed", Updated: t0.Add(-50 * time.Minute), LastReply: reply,
			Background: []string{"bipblrp3r (Re-arm qa-2 bus waiter with 2h limit)"}},
		"th-qa": {Status: "waiting", Updated: t0.Add(-50 * time.Minute), LastReply: "G150 suite runs. ⏳",
			Background: []string{"b7x2 (Run the G150 suite)"}},
		"th-ms": {Status: "completed", Updated: t0.Add(-50 * time.Minute), LastReply: reply},
	}}
	runOnce(t, home, f, t0)
	for _, id := range []string{"th-qa2", "th-qa"} {
		if w := t3Wakes(f, "T3 thread "+id+")"); len(w) != 0 {
			t.Errorf("%s, its own job running in T3, woke Crius: %q", id, w)
		}
	}
	if w := t3Wakes(f, "T3 thread th-ms)"); len(w) != 1 || !strings.Contains(w[0], "its job may be gone") {
		t.Errorf("th-ms, no background job, ⏳ 50 min: want one 'job may be gone' wake, got %q", w)
	}
	runOnce(t, home, f, t0.Add(80*time.Minute)) // 130 min quiet: over t3WaitingFor
	w := t3Wakes(f, "T3 thread th-qa2)")
	if len(w) != 1 || !strings.Contains(w[0], "WAITING ON its own T3 background job(s) bipblrp3r (Re-arm qa-2 bus waiter") ||
		strings.Contains(w[0], "its job may be gone") {
		t.Fatalf("th-qa2 past %s: want one wake naming its job, got %q", t3WaitingFor, w)
	}
	if w := t3Wakes(f, "T3 thread th-qa)"); len(w) != 1 || !strings.Contains(w[0], "b7x2 (Run the G150 suite)") {
		t.Errorf("th-qa past %s: want one wake naming its job, got %q", t3WaitingFor, w)
	}
	runOnce(t, home, f, t0.Add(100*time.Minute)) // the same spell: said once
	if w := t3Wakes(f, "T3 thread th-qa2)"); len(w) != 1 {
		t.Errorf("one spell woke Crius %d times: %q", len(w), w)
	}
	var b strings.Builder
	Waiting(f.deps(home, t0), &b, []string{"quotes:qa2"})
	if !strings.Contains(b.String(), "quotes:qa2 WAITING ON its own T3 background job(s) bipblrp3r") {
		t.Errorf("crius-wakewatch waiting disagrees with the alert: %q", b.String())
	}
}
