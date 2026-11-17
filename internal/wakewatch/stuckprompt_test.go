package wakewatch

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/testutil"
)

const cortexSID = "b3e71163-d5e6-4c3d-952d-84d9ea65a6a1"

// permWait is the line the Notification hook (~/.claude/hooks/wait-log.sh) writes to waits.log for a permission prompt.
func permWait(at time.Time, sid string) string {
	return fmt.Sprintf("%s WAIT waits:cortex -> crius — wait permission_prompt \"cortex\" cwd=/Users/me/Projects/cortex sid=%s at=%s — Claude needs your permission to use Bash\n",
		stamp(at), sid[:8], at.Format("15:04:05"))
}

// stuckHome is a home whose waits.log holds one permission prompt from the Cortex lead's session at `at`, with
// crius-hook's state file for that session: state and its time, in T3 thread th-cortex (thread "" for none).
func stuckHome(t *testing.T, at time.Time, state string, stateAt time.Time, thread string) string {
	t.Helper()
	home := setup(t, map[string]string{"waits.log": stamp(at.Add(-time.Hour)) + " WAIT waits:x -> crius — wait idle_prompt \"x\" cwd=/x sid=0000aaaa at=" +
		at.Add(-time.Hour).Format("15:04:05") + " — waiting\n" + permWait(at, cortexSID)})
	if state != "" {
		writeSession(t, home, cortexSID, state, stateAt, thread)
	}
	return home
}

func writeSession(t *testing.T, home, sid, state string, at time.Time, thread string) {
	t.Helper()
	testutil.Write(t, filepath.Join(home, "sessions", sid+".json"), fmt.Sprintf(
		`{"session_id":%q,"state":%q,"event":"Notification","detail":"permission_prompt","time":%q,"cwd":"/Users/me/Projects/cortex","project":"cortex","role":"unknown","t3_thread":%q}`,
		sid, state, at.Format(time.RFC3339Nano), thread))
}

func promptWakes(f *fake) []string {
	var out []string
	for _, s := range f.sent {
		if strings.Contains(s, "permission prompt") {
			out = append(out, s)
		}
	}
	return out
}

// 11-17: the Cortex lead (a T3 thread, role "unknown", so no role check looked at it) sat on a permission prompt
// 07:02-09:05; the hook's WAIT line went to the 09:00/18:00 digest and nothing woke Crius. A prompt that T3 still holds
// open 10 min after its WAIT wakes Crius once, naming the project, cwd, session and how long it has waited.
func TestAPermissionPromptStuckTenMinWakesCriusOnce(t *testing.T) {
	at := t0.Add(9 * time.Second)
	home := stuckHome(t, at, "waiting", at, "th-cortex")
	f := &fake{threads: map[string]T3Thread{"th-cortex": {Status: "running", Active: true, Pending: 1, Updated: at}}}
	runOnce(t, home, f, t0.Add(9*time.Minute))
	if w := promptWakes(f); len(w) != 0 {
		t.Fatalf("woke before 10 min: %q", w)
	}
	runOnce(t, home, f, t0.Add(11*time.Minute))
	w := promptWakes(f)
	if len(w) != 1 {
		t.Fatalf("want one wake for the stuck prompt, got %q (all sent: %q)", w, f.sent)
	}
	for _, want := range []string{"cortex", "/Users/me/Projects/cortex", "b3e71163", "10 min", "th-cortex"} {
		if !strings.Contains(w[0], want) {
			t.Errorf("wake %q lacks %q", w[0], want)
		}
	}
	for _, m := range []time.Duration{12, 31, 55, 125} {
		runOnce(t, home, f, t0.Add(m*time.Minute))
	}
	if w := promptWakes(f); len(w) != 1 {
		t.Fatalf("one stuck prompt woke Crius %d times: %q", len(w), w)
	}
	// the next prompt in the same session is a new one: it wakes again
	at2 := t0.Add(3 * time.Hour)
	log := filepath.Join(home, "bus", "waits.log")
	testutil.Write(t, log, testutil.Read(t, log)+permWait(at2, cortexSID))
	writeSession(t, home, cortexSID, "waiting", at2, "th-cortex")
	runOnce(t, home, f, at2.Add(12*time.Minute))
	if w := promptWakes(f); len(w) != 2 || !strings.Contains(w[1], "12 min") {
		t.Fatalf("a second prompt: %q", w)
	}
}

// A prompt answered at minute 3 wakes nobody: crius-hook saw the session move on, and T3 holds no open request.
func TestAPermissionPromptAnsweredAtMinuteThreeWakesNobody(t *testing.T) {
	at := t0.Add(9 * time.Second)
	home := stuckHome(t, at, "running", at.Add(3*time.Minute), "th-cortex")
	f := &fake{threads: map[string]T3Thread{"th-cortex": {Status: "running", Active: true, Updated: at.Add(3 * time.Minute)}}}
	for _, m := range []time.Duration{11, 30, 90} {
		runOnce(t, home, f, t0.Add(m*time.Minute))
	}
	if w := promptWakes(f); len(w) != 0 {
		t.Fatalf("an answered prompt woke Crius: %q", w)
	}
}

// An approved tool that still runs leaves crius-hook's state on "waiting" (approving fires no hook); T3 holds no open
// request then, so it wakes nobody.
func TestAnApprovedToolStillRunningWakesNobody(t *testing.T) {
	at := t0.Add(9 * time.Second)
	home := stuckHome(t, at, "waiting", at, "th-cortex")
	f := &fake{threads: map[string]T3Thread{"th-cortex": {Status: "running", Active: true, Updated: at.Add(time.Minute)}}}
	runOnce(t, home, f, t0.Add(15*time.Minute))
	if w := promptWakes(f); len(w) != 0 {
		t.Fatalf("an approved, running tool woke Crius: %q", w)
	}
}

// A session that is no T3 thread (a cmux tab or a terminal): crius-hook's state still on the dialog decides.
func TestAStuckPromptOutsideT3WakesFromTheHookState(t *testing.T) {
	at := t0.Add(9 * time.Second)
	home := stuckHome(t, at, "waiting", at, "")
	f := &fake{}
	runOnce(t, home, f, t0.Add(14*time.Minute))
	if w := promptWakes(f); len(w) != 1 || !strings.Contains(w[0], "b3e71163") || !strings.Contains(w[0], "13 min") {
		t.Fatalf("want one wake from the hook state: %q", w)
	}
}

// "Could not tell" is loud: no state file for the session, or a T3 thread that cannot be read, wakes Crius once
// saying it could not tell whether the prompt was answered.
func TestAPromptWhoseStateCannotBeReadWakesSayingSo(t *testing.T) {
	at := t0.Add(9 * time.Second)
	for name, c := range map[string]struct {
		state  string
		thread string
		err    error
		why    string
	}{
		"no state file":  {why: "no state file"},
		"t3 cannot read": {state: "waiting", thread: "th-cortex", err: fmt.Errorf("t3 thread show: exit status 1: boom"), why: "boom"},
		"thread gone":    {state: "waiting", thread: "th-gone", why: "no longer available"},
	} {
		t.Run(name, func(t *testing.T) {
			home := stuckHome(t, at, c.state, at, c.thread)
			f := &fake{threads: map[string]T3Thread{}, threadErr: c.err}
			runOnce(t, home, f, t0.Add(11*time.Minute))
			runOnce(t, home, f, t0.Add(40*time.Minute))
			w := promptWakes(f)
			if len(w) != 1 {
				t.Fatalf("want one could-not-tell wake, got %q (all sent: %q)", w, f.sent)
			}
			for _, want := range []string{"could not tell", "b3e71163", c.why} {
				if !strings.Contains(w[0], want) {
					t.Errorf("wake %q lacks %q", w[0], want)
				}
			}
		})
	}
}

// A session that ended, and a prompt older than the 12 h window, wake nobody.
func TestAnEndedSessionOrAnOldPromptWakesNobody(t *testing.T) {
	at := t0.Add(9 * time.Second)
	home := stuckHome(t, at, "ended", at.Add(5*time.Minute), "th-cortex")
	f := &fake{threads: map[string]T3Thread{"th-cortex": {Pending: 1, Updated: at}}}
	runOnce(t, home, f, t0.Add(20*time.Minute))
	home = stuckHome(t, at, "waiting", at, "th-cortex")
	runOnce(t, home, f, t0.Add(13*time.Hour))
	if w := promptWakes(f); len(w) != 0 {
		t.Fatalf("woke Crius: %q", w)
	}
}

// waits.log runs to megabytes: a prompt more than one read back from the end, still inside 12 h, is found.
func TestAStuckPromptDeepInALongWaitsLogIsFound(t *testing.T) {
	at := t0.Add(9 * time.Second)
	old := strings.Repeat(stamp(t0.Add(-20*time.Hour))+" WAIT waits:x -> crius — wait idle_prompt \"x\" cwd=/x sid=0000aaaa at=02:00:00 — "+strings.Repeat("o", 80)+"\n", 3000)
	recent := strings.Repeat(stamp(t0.Add(5*time.Minute))+" WAIT waits:y -> crius — wait idle_prompt \"y\" cwd=/y sid=0000bbbb at=22:05:00 — "+strings.Repeat("r", 80)+"\n", 2000)
	home := setup(t, map[string]string{"waits.log": old + permWait(at, cortexSID) + recent})
	writeSession(t, home, cortexSID, "waiting", at, "th-cortex")
	f := &fake{threads: map[string]T3Thread{"th-cortex": {Status: "running", Active: true, Pending: 1, Updated: at}}}
	runOnce(t, home, f, t0.Add(11*time.Minute))
	if w := promptWakes(f); len(w) != 1 {
		t.Fatalf("want one wake for a prompt %d bytes from the end, got %q", len(recent), w)
	}
}

// A team role's thread that t3Idle already raises for its open request is not raised twice.
func TestAStuckPromptInARoleThreadWakesOnce(t *testing.T) {
	at := t0.Add(9 * time.Second)
	home := setup(t, map[string]string{"cortex.log": "", "waits.log": permWait(at, cortexSID)})
	testutil.Write(t, filepath.Join(home, "roles", "cortex.lead.json"),
		`{"session_id":"`+cortexSID+`","project":"cortex","role":"lead","time":"2026-10-21T21:00:00Z"}`)
	writeSession(t, home, cortexSID, "waiting", at, "th-cortex")
	f := &fake{threads: map[string]T3Thread{"th-cortex": {Status: "completed", Pending: 1, Updated: at, LastReply: "asking ⏳"}}}
	for _, m := range []time.Duration{11, 12, 40} {
		runOnce(t, home, f, t0.Add(m*time.Minute))
	}
	var w []string
	for _, s := range f.sent {
		if strings.Contains(s, "th-cortex") {
			w = append(w, s)
		}
	}
	if len(w) != 1 || !strings.Contains(w[0], "open request") {
		t.Fatalf("want one wake for the thread's open request, got %q", w)
	}
}

// writeState writes crius-hook's state file for sid from the given JSON fields.
func writeState(t *testing.T, home, sid, fields string) {
	t.Helper()
	testutil.Write(t, filepath.Join(home, "sessions", sid+".json"), `{"session_id":"`+sid+`",`+fields+`}`)
}

// The thread's own update time moved after the prompt (a queued message, a subagent): t3Idle's clock starts later than
// the prompt's, and still one stuck prompt wakes Crius once (review of the first version: two wakes).
func TestAStuckPromptInARoleThreadUpdatedLaterWakesOnce(t *testing.T) {
	at := t0.Add(9 * time.Second)
	home := setup(t, map[string]string{"cortex.log": "", "waits.log": permWait(at, cortexSID)})
	testutil.Write(t, filepath.Join(home, "roles", "cortex.lead.json"),
		`{"session_id":"`+cortexSID+`","project":"cortex","role":"lead","time":"2026-10-21T21:00:00Z"}`)
	writeSession(t, home, cortexSID, "waiting", at, "th-cortex")
	f := &fake{threads: map[string]T3Thread{"th-cortex": {Status: "completed", Pending: 1, Updated: at.Add(2 * time.Minute), LastReply: "asking ⏳"}}}
	for _, m := range []time.Duration{11, 12, 13, 40} {
		runOnce(t, home, f, t0.Add(m*time.Minute))
	}
	var w []string
	for _, s := range f.sent {
		if strings.Contains(s, "th-cortex") {
			w = append(w, s)
		}
	}
	if len(w) != 1 {
		t.Fatalf("want one wake for the thread's stuck prompt, got %q", w)
	}
}

// A cmux tab's dialog is the prompt watch's (prompts.go): this check does not wake for it too. Crius's own thread is
// left out.
func TestAPromptInACmuxTabOrCriusThreadIsLeftOut(t *testing.T) {
	at := t0.Add(9 * time.Second)
	home := stuckHome(t, at, "", at, "")
	writeState(t, home, cortexSID, `"state":"waiting","event":"Notification","time":"`+at.Format(time.RFC3339)+`","cwd":"/Users/me/Projects/cortex","surface":"9F1C-UUID"`)
	f := &fake{}
	runOnce(t, home, f, t0.Add(15*time.Minute))
	home = stuckHome(t, at, "waiting", at, "th-crius")
	testutil.Write(t, filepath.Join(home, "state", "crius", "t3-thread"), "th-crius\n")
	f.threads = map[string]T3Thread{"th-crius": {Pending: 1, Updated: at}}
	runOnce(t, home, f, t0.Add(15*time.Minute))
	if w := promptWakes(f); len(w) != 0 {
		t.Fatalf("woke Crius: %q", w)
	}
}

// A dialog that opened after the line (a subagent's, which logs no line of its own) is timed and keyed from its own
// start in crius-hook's Opened, not the line's (review round 2): the line's prompt answered at minute 2 and a subagent's
// dialog opened at minute 30 wakes nobody at minute 32 and wakes once at minute 41, aged 11 min. In a role thread whose
// earlier prompt already woke Crius, the later dialog wakes again (P1: both checks were silent).
func TestALaterDialogWithNoLineIsTimedFromItsOwnStart(t *testing.T) {
	at := t0.Add(9 * time.Second)
	opened := func(home string, state string, since time.Time, waits string) {
		writeState(t, home, cortexSID, `"state":"`+state+`","event":"PermissionRequest","time":"`+since.Format(time.RFC3339)+
			`","cwd":"/Users/me/Projects/cortex","project":"cortex","t3_thread":"th-cortex","waits":["`+waits+`"],"opened":{"`+waits+`":"`+since.Format(time.RFC3339)+`"}`)
	}
	home := stuckHome(t, at, "", at, "")
	opened(home, "waiting", t0.Add(30*time.Minute), "agent-1")
	f := &fake{threads: map[string]T3Thread{"th-cortex": {Status: "running", Active: true, Pending: 1, Updated: t0.Add(30 * time.Minute)}}}
	runOnce(t, home, f, t0.Add(32*time.Minute))
	if w := promptWakes(f); len(w) != 0 {
		t.Fatalf("a dialog open 2 min woke Crius: %q", w)
	}
	runOnce(t, home, f, t0.Add(41*time.Minute))
	runOnce(t, home, f, t0.Add(55*time.Minute))
	if w := promptWakes(f); len(w) != 1 || !strings.Contains(w[0], "11 min") || !strings.Contains(w[0], "22:30") || !strings.Contains(w[0], "no notification line") {
		t.Fatalf("want one wake for the later dialog, aged from 22:30: %q", w)
	}

	home = setup(t, map[string]string{"cortex.log": "", "waits.log": permWait(at, cortexSID)})
	testutil.Write(t, filepath.Join(home, "roles", "cortex.lead.json"),
		`{"session_id":"`+cortexSID+`","project":"cortex","role":"lead","time":"2026-10-21T21:00:00Z"}`)
	opened(home, "waiting", at, "main")
	f = &fake{threads: map[string]T3Thread{"th-cortex": {Status: "running", Active: true, Pending: 1, Updated: at}}}
	runOnce(t, home, f, t0.Add(11*time.Minute))
	opened(home, "waiting", t0.Add(60*time.Minute), "agent-1") // the first was answered; a subagent's dialog opens at 60
	for _, m := range []time.Duration{61, 75, 90, 150} {
		runOnce(t, home, f, t0.Add(m*time.Minute))
	}
	var w []string
	for _, s := range f.sent {
		if strings.Contains(s, "th-cortex") {
			w = append(w, s)
		}
	}
	if len(w) != 2 || !strings.Contains(w[1], "23:00") {
		t.Fatalf("want a wake for each dialog, the second from 23:00: %q", w)
	}
}

// The main agent's dialog that the line logged stays the one in question while a subagent works (its tool events move
// the state's time, not the dialog's start).
func TestASubagentWorkingDoesNotRestartTheLinesDialog(t *testing.T) {
	at := t0.Add(9 * time.Second)
	home := stuckHome(t, at, "", at, "")
	writeState(t, home, cortexSID, `"state":"waiting","event":"PostToolUse","time":"`+t0.Add(8*time.Minute).Format(time.RFC3339)+
		`","cwd":"/Users/me/Projects/cortex","t3_thread":"th-cortex","waits":["main"],"opened":{"main":"`+t0.Format(time.RFC3339)+`"}`)
	f := &fake{threads: map[string]T3Thread{"th-cortex": {Status: "running", Active: true, Pending: 1, Updated: t0.Add(8 * time.Minute)}}}
	runOnce(t, home, f, t0.Add(11*time.Minute))
	if w := promptWakes(f); len(w) != 1 || !strings.Contains(w[0], "10 min") || !strings.Contains(w[0], "22:00") {
		t.Fatalf("want one wake aged from the line: %q", w)
	}
}

// The state file shows a state older than the prompt that is not "waiting": crius-hook missed the dialog, so the check
// cannot tell. Two state files sharing the 8-character prefix: the cwd picks one; two with the same cwd cannot be told
// apart. A line with no session id cannot be looked up.
func TestAPromptItCannotPlaceSaysCouldNotTell(t *testing.T) {
	at := t0.Add(9 * time.Second)
	other := "b3e71163-0000-4000-8000-000000000000"
	for name, c := range map[string]struct {
		setup func(home string)
		log   string
		why   string // "" wants no wake
	}{
		"state older than the prompt": {setup: func(home string) {
			writeSession(t, home, cortexSID, "running", at.Add(-time.Minute), "th-cortex")
		}, why: "did not record the dialog"},
		"cwd picks the session": {setup: func(home string) {
			writeSession(t, home, cortexSID, "running", at.Add(3*time.Minute), "th-cortex")
			writeState(t, home, other, `"state":"waiting","time":"`+at.Format(time.RFC3339)+`","cwd":"/Users/me/Projects/other","t3_thread":"th-other"`)
		}},
		"two with that cwd": {setup: func(home string) {
			writeSession(t, home, cortexSID, "waiting", at, "th-cortex")
			writeSession(t, home, other, "waiting", at, "th-other")
		}, why: "2 crius-hook state files"},
		"no session id": {setup: func(string) {}, log: strings.Replace(permWait(at, cortexSID), "sid=b3e71163", "sid=?", 1), why: "no session id"},
	} {
		t.Run(name, func(t *testing.T) {
			home := stuckHome(t, at, "", at, "")
			if c.log != "" {
				testutil.Write(t, filepath.Join(home, "bus", "waits.log"), c.log)
			}
			c.setup(home)
			f := &fake{threads: map[string]T3Thread{"th-cortex": {Pending: 1, Updated: at}, "th-other": {Pending: 1, Updated: at}}}
			if c.why == "no session id" || c.why == "did not record the dialog" { // the hook's record: said at once
				runOnce(t, home, f, t0.Add(11*time.Minute))
			} else {
				runLooked(t, f, f.deps(home, t0.Add(11*time.Minute)))
			}
			w := promptWakes(f)
			if c.why == "" {
				if len(w) != 0 {
					t.Fatalf("woke Crius: %q", w)
				}
				return
			}
			if len(w) != 1 || !strings.Contains(w[0], "could not tell") || !strings.Contains(w[0], c.why) {
				t.Fatalf("want one could-not-tell wake naming %q, got %q", c.why, w)
			}
		})
	}
}

// A newer line stamped ahead of now (a clock set back) does not hide the prompt that is stuck now.
func TestALineAheadOfNowDoesNotHideAStuckPrompt(t *testing.T) {
	at := t0.Add(9 * time.Second)
	home := stuckHome(t, at, "waiting", at, "th-cortex")
	log := filepath.Join(home, "bus", "waits.log")
	testutil.Write(t, log, testutil.Read(t, log)+permWait(t0.Add(time.Hour), cortexSID))
	f := &fake{threads: map[string]T3Thread{"th-cortex": {Status: "running", Active: true, Pending: 1, Updated: at}}}
	runOnce(t, home, f, t0.Add(11*time.Minute))
	if w := promptWakes(f); len(w) != 1 {
		t.Fatalf("want one wake, got %q", w)
	}
}

// When t3 hangs on one thread, the next is not read on that run (each call waits 10 s); both prompts say so, on the
// second run in a row (looks.go).
func TestAHungT3IsReadOncePerRun(t *testing.T) {
	at := t0.Add(9 * time.Second)
	second := "c4f82274-d5e6-4c3d-952d-84d9ea65a6a1"
	home := stuckHome(t, at, "waiting", at, "th-cortex")
	log := filepath.Join(home, "bus", "waits.log")
	testutil.Write(t, log, testutil.Read(t, log)+permWait(at, second))
	writeSession(t, home, second, "waiting", at, "th-second")
	f := &fake{threadErr: fmt.Errorf("%w (10s)", ErrT3NoAnswer)}
	runLooked(t, f, f.deps(home, t0.Add(11*time.Minute)))
	w := promptWakes(f)
	if len(w) != 2 || !strings.Contains(w[1], "t3 gave no answer for an earlier thread") {
		t.Fatalf("want two could-not-tell wakes, the second not read: %q", w)
	}
	var reads int
	for _, id := range f.read {
		if id == "th-cortex" || id == "th-second" {
			reads++
		}
	}
	if reads != lookMisses { // one read a run
		t.Fatalf("t3 read %d times in %d runs after it hung: %q", reads, lookMisses, f.read)
	}
}

// tailSince stops reading back once it holds a line older than since: the old start of a long log is not read.
func TestTailSinceStopsAtTheFirstOldLine(t *testing.T) {
	old := strings.Repeat(stamp(t0.Add(-20*time.Hour))+" WAIT waits:x -> crius — old "+strings.Repeat("o", 80)+"\n", 3000)
	recent := strings.Repeat(stamp(t0)+" WAIT waits:y -> crius — new "+strings.Repeat("r", 80)+"\n", 100)
	path := filepath.Join(t.TempDir(), "waits.log")
	testutil.Write(t, path, old+recent)
	lines, err := tailSince(path, t0.Add(-12*time.Hour), t0)
	if err != nil {
		t.Fatal(err)
	}
	if total := strings.Count(old+recent, "\n") + 1; len(lines) >= total || len(lines) < 101 {
		t.Fatalf("read %d of %d lines; want the 101 recent ones and only part of the old", len(lines), total)
	}
}

// A dialog known only from its notification is stamped a moment after its line (which has whole seconds): within the
// minute it is the line's own dialog, aged and keyed from the line.
func TestADialogOpenedJustAfterItsLineIsTheLines(t *testing.T) {
	at := t0.Add(9 * time.Second)
	home := stuckHome(t, at, "", at, "")
	writeState(t, home, cortexSID, `"state":"waiting","event":"Notification","time":"`+at.Add(30*time.Second).Format(time.RFC3339)+
		`","cwd":"/Users/me/Projects/cortex","t3_thread":"th-cortex","waits":["notify"],"opened":{"notify":"`+at.Add(30*time.Second).Format(time.RFC3339)+`"}`)
	f := &fake{threads: map[string]T3Thread{"th-cortex": {Status: "running", Active: true, Pending: 1, Updated: at}}}
	runOnce(t, home, f, t0.Add(10*time.Minute+20*time.Second))
	if w := promptWakes(f); len(w) != 1 || !strings.Contains(w[0], "Claude needs your permission to use Bash") {
		t.Fatalf("want one wake aged from the line: %q", w)
	}
}
