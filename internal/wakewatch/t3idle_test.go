package wakewatch

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
		if strings.Contains(s, "T3 thread idle check failed to look") {
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
