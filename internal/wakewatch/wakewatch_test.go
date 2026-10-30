package wakewatch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/orzazade/crius/internal/ownerqueue"
	"github.com/orzazade/crius/internal/sessionstate"
	"github.com/orzazade/crius/internal/testutil"
)

var t0 = time.Date(2026, 10, 21, 22, 0, 0, 0, time.Local)

func stamp(t time.Time) string { return t.Format("2006-01-02T15:04") }

type fake struct {
	sent    []string
	sendErr error
	tried   []string // every text given to Send, sent or not
	screens []Screen
	scrErr  error
	freeGB  float64 // free disk; 0 means a healthy 100
	swapGB  float64
	resErr  error
	load    float64
	loadErr error
	tools   map[string]error
	ses     []Session
	sesErr  error
	wss     []Workspace
	wsErr   error
	states  []sessionstate.State // crius-hook's session files
	stErr   error
	restore map[string][]string // surface -> the config folder of each Claude record cmux keeps for it
	rsErr   error
	waiters []string // the bus logs a live waiter covers; nil: every bus log in home
	waitErr error
}

func (f *fake) deps(home string, now time.Time) Deps {
	return Deps{Home: home, Now: now,
		Screens:    func() ([]Screen, error) { return f.screens, f.scrErr },
		Tools:      func() map[string]error { return f.tools },
		Sessions:   func() ([]Session, error) { return f.ses, f.sesErr },
		Workspaces: func() ([]Workspace, error) { return f.wss, f.wsErr },
		States:     func() ([]sessionstate.State, error) { return f.states, f.stErr },
		Restore:    func(surface string) ([]string, error) { return f.restore[surface], f.rsErr },
		Waiters: func() ([]string, error) {
			if f.waiters == nil {
				all, _ := filepath.Glob(filepath.Join(home, "bus", "*.log"))
				return all, f.waitErr
			}
			return f.waiters, f.waitErr
		},
		Load: func() (float64, error) { return f.load, f.loadErr },
		Resources: func() (float64, float64, error) {
			if f.freeGB == 0 {
				return 100, f.swapGB, f.resErr
			}
			return f.freeGB, f.swapGB, f.resErr
		},
		Send: func(text string) error {
			f.tried = append(f.tried, text)
			if f.sendErr != nil {
				return f.sendErr
			}
			f.sent = append(f.sent, text)
			return nil
		}}
}

func setup(t *testing.T, buses map[string]string) string {
	_, home := testutil.Temp(t)
	os.MkdirAll(filepath.Join(home, "state"), 0o700)
	testutil.Write(t, filepath.Join(home, "bus", "crius.log"), "")
	for name, text := range buses {
		testutil.Write(t, filepath.Join(home, "bus", name), text)
	}
	return home
}

func done(at time.Time) string {
	return stamp(at) + " DONE quotes:lead -> crius:lead — F-86 · 1c6e705 · clean-suite 923 OK\n"
}

func TestADoneWithNoCriusLineFor20MinAlerts(t *testing.T) {
	home := setup(t, map[string]string{"quotes.log": done(t0)})
	f := &fake{}
	if err := Run(f.deps(home, t0.Add(21*time.Minute))); err != nil {
		t.Fatal(err)
	}
	if len(f.sent) != 1 || !strings.Contains(f.sent[0], "quotes.log") || !strings.Contains(f.sent[0], "F-86") {
		t.Fatal(f.sent)
	}
}

func TestADoneYoungerThan20MinWaits(t *testing.T) {
	home := setup(t, map[string]string{"quotes.log": done(t0)})
	f := &fake{}
	if err := Run(f.deps(home, t0.Add(19*time.Minute))); err != nil || len(f.sent) != 0 {
		t.Fatal(err, f.sent)
	}
}

func TestALaterCriusLeadLineAnswersIt(t *testing.T) {
	for _, reply := range []string{
		"INFO crius:lead — fresh Crius up",
		"VERIFIED crius:lead -> quotes:lead — 1c6e705 · x",
		"ACK crius:lead -> quotes:lead — x",
		"ORDER crius:lead -> quotes:lead — x",
		"ANSWER crius:lead -> quotes:lead — x",
		"GO crius:lead -> quotes:lead — x",
		"OWNER crius:lead -> quotes:lead — owner: x", // Crius passing on the owner's words is awake too
	} {
		home := setup(t, map[string]string{"quotes.log": done(t0) + stamp(t0.Add(30*time.Minute)) + " " + reply + "\n"})
		f := &fake{}
		if err := Run(f.deps(home, t0.Add(40*time.Minute))); err != nil || len(f.sent) != 0 {
			t.Fatal(reply, err, f.sent)
		}
	}
}

// Crius 10-30 20:32: wakewatch re-woke Crius for cortex's 16:40 DONE and 18:30 NEED and falcon's two 20:07 ASKs, each
// answered within a minute by a crius:lead DECISION to that lead. Any crius:lead line sent to the asker after its line
// answers it, whatever its type; a line to someone else, or one before the ask, does not.
func TestACriusLeadLineToTheAskerAnswersIt(t *testing.T) {
	at := func(m int) string { return stamp(t0.Add(time.Duration(m) * time.Minute)) }
	home := setup(t, map[string]string{
		"cortex.log": at(0) + " DONE cortex:lead -> crius — STATUS GOAL block now in RULES 3.1 form at the top\n" +
			at(0) + " DECISION crius:lead -> cortex:lead — Deploy sha-eb557149e5bd now (Crius approves Cortex deploys)\n" +
			at(110) + " NEED cortex:lead -> crius — PR #26 fix(sync): a Sync request on a pending repo was probed last\n" +
			at(111) + " DECISION crius:lead -> cortex:lead — PR #26 approved by Crius: read ops/sync.ts diff\n",
		"falcon.log": at(207) + " ASK falcon:lead -> crius:lead — (2) has 2 readings. A: copy the pr object from the card\n" +
			at(207) + " ASK falcon:lead -> crius:lead — With B, PR reviews have one source: sense-pr cards. A or B?\n" +
			at(207) + " DECISION crius:lead -> falcon:lead — (2) = B: a PR link in mail starts no action\n",
		"quotes.log": at(90) + " NEED crius:lead -> quotes:qa1 — x\n" +
			at(100) + " NEED quotes:lead -> crius — still open: Crius wrote to someone else\n" +
			at(101) + " DECISION crius:lead -> quotes:b1 — not to the asker\n" +
			at(102) + " DECISION crius:lead -> axi:lead — nor this\n" +
			at(103) + " ASK quotes:qa1 -> crius:lead — asked after Crius wrote to it\n" +
			// review of a20321f: a copy (cc) answers nothing, nor does a line to Crius itself clear its own note
			at(104) + " NEED quotes:p2 -> crius — copied in only\n" +
			at(105) + " DECISION crius:lead -> quotes:b1 (cc quotes:p2) — b1 takes it\n" +
			at(106) + " NEED crius:lead -> crius:lead — remind me: check falcon\n" +
			at(107) + " DECISION crius:lead -> crius:lead, cortex:lead — x\n" +
			// review of 0f8aee6: the bus's usual copy forms "a, cc b" and "a cc b" answer nothing either
			at(108) + " NEED quotes:p3 -> crius — copied in with a bare cc\n" +
			at(109) + " DECISION crius:lead -> quotes:b1, cc quotes:p3 — b1 takes it\n" +
			at(108) + " NEED quotes:p4 -> crius — copied in with cc and no comma\n" +
			at(109) + " DECISION crius:lead -> quotes:b1 cc quotes:p4 — b1 takes it\n",
		// a bare role on its own team's bus names it, in any case; a line to the whole team answers no one member
		"axi.log": at(100) + " NEED axi:lead -> crius — which branch\n" + at(101) + " DECISION crius:lead -> lead — main\n" +
			at(100) + " NEED axi:p2 -> crius — which port\n" + at(101) + " DECISION crius:lead -> P2 — 8080\n" +
			at(102) + " ASK axi:qa -> crius — which env\n" + at(103) + " DECISION crius:lead -> axi:all — p3-test\n",
	})
	f := &fake{}
	if err := Run(f.deps(home, t0.Add(240*time.Minute))); err != nil {
		t.Fatal(err)
	}
	var woke []string
	for _, s := range f.sent {
		for _, l := range strings.Split(s, "\n") {
			if strings.Contains(l, ".log: ") {
				woke = append(woke, l)
			}
		}
	}
	all := strings.Join(woke, "\n")
	want := []string{"still open", "asked after", "copied in only", "remind me", "bare cc", "cc and no comma", "which env"}
	for _, w := range want {
		if !strings.Contains(all, w) {
			t.Errorf("no wake for %q", w)
		}
	}
	if len(woke) != len(want) {
		t.Fatalf("want %d wakes, woke %d:\n%s", len(want), len(woke), all)
	}
}

// Crius 10-23 09:45: wakewatch re-woke Crius for a digest DONE (a report, it needs no reply) and for the quota guard's
// NEED at 06:36 after Crius had parked quotes. A pause NEED is answered once its project, or every role it paused, is
// in ~/.crius/parked; a NEED with roles still unparked, or about something else, still wakes.
func TestADigestDoneAndAParkedPauseNeedNoReply(t *testing.T) {
	pause := func(text string) string {
		return stamp(t0) + " NEED crius:ingest -> crius:lead — quota guard: " + text + "\n"
	}
	bus := stamp(t0) + " DONE crius:digest -> crius:lead — goal report ~/.crius/state/digest/2026-10-23-0900.md\n" +
		pause("Max 7d at 75% (06:34) reached quotes's pause at 75%; PAUSE posted on quotes.log. Park quotes in ~/.crius/parked and stop its tabs") +
		pause("account max1 at 91% (13:00) reached tamada's pause at 90%; PAUSE posted to tamada:p1, tamada:p2; 0 tabs with no role on it") +
		pause("account max2 at 92% (13:00) reached falcon's pause at 90%; PAUSE posted to falcon:p1, falcon:p2; 0 tabs with no role on it") +
		pause("account max2 at 92% (13:00) reached axi's pause at 90%; no tab of axi has a role on it; 1 tab with no role on it") +
		pause("every Max account reached kappa's pause at 90% (max1 91%, max2 92%); PAUSE posted to kappa:all. Park kappa in ~/.crius/parked") +
		pause("account max1 at 91% (13:00) reached zeta's pause at 90%; PAUSE posted to zeta:p1; not paused, as a PAUSE would stop its tab with room too (stop its tab here by hand): p2 also runs on max2; 0 tabs with no role on it") +
		pause("account max1 at 91% (13:00) reached omega's pause at 90%; PAUSE posted to omega:p1; 1 tab with no role on it") +
		stamp(t0) + " NEED crius:ingest -> crius:lead — quota guard is off, no pause or quota alert runs: bad file\n"
	home := setup(t, map[string]string{"crius.log": bus})
	testutil.Write(t, filepath.Join(home, "parked"), "quotes\ntamada:p1\ntamada:p2\nfalcon:p1\nkappa\nzeta:p1\nomega:p1\n")
	f := &fake{}
	if err := Run(f.deps(home, t0.Add(25*time.Minute))); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(f.sent, "\n")
	for _, not := range []string{"goal report", "quotes's pause", "tamada's pause", "kappa's pause"} {
		if strings.Contains(all, not) {
			t.Error("woke for", not)
		}
	}
	for _, want := range []string{"falcon's pause", "axi's pause", "quota guard is off", "zeta's pause", "omega's pause"} {
		if !strings.Contains(all, want) {
			t.Error("no wake for", want, all)
		}
	}
}

func TestAnAnswerOnAnotherBusDoesNotCount(t *testing.T) {
	home := setup(t, map[string]string{"quotes.log": done(t0),
		"crius.log": stamp(t0.Add(5*time.Minute)) + " ORDER crius:lead -> crius:build — x\n"})
	f := &fake{}
	if err := Run(f.deps(home, t0.Add(25*time.Minute))); err != nil || len(f.sent) != 1 {
		t.Fatal(err, f.sent)
	}
}

func TestOnlyWakeTypesToCriusLeadCount(t *testing.T) {
	bus := stamp(t0) + " INFO quotes:lead -> crius:lead — progress\n" +
		stamp(t0) + " DONE quotes:b1 -> quotes:lead — not for Crius\n" +
		stamp(t0) + " NEED quotes:lead -> crius:lead, quotes:qa — needs an answer\n" +
		stamp(t0) + " CLEAR-READY crius:build -> crius:lead — at 200K\n" +
		stamp(t0) + " ASK axi:lead -> crius — which branch\n" +
		stamp(t0) + " ASK quotes:b2 -> quotes:qa crius:lead — spaces only\n" // the bus grammar allows "a b" as well as "a, b"
	home := setup(t, map[string]string{"quotes.log": bus})
	f := &fake{}
	if err := Run(f.deps(home, t0.Add(25*time.Minute))); err != nil || len(f.sent) != 4 {
		t.Fatal(err, f.sent)
	}
	all := strings.Join(f.sent, "\n")
	for _, want := range []string{"needs an answer", "at 200K", "which branch", "spaces only"} {
		if !strings.Contains(all, want) {
			t.Error("missing", want, all)
		}
	}
	for _, not := range []string{"progress", "not for Crius"} {
		if strings.Contains(all, not) {
			t.Error("alerted", not)
		}
	}
}

func TestAMissedLineAlertsOnce(t *testing.T) {
	home := setup(t, map[string]string{"quotes.log": done(t0)})
	f := &fake{}
	for _, m := range []time.Duration{21, 26, 31} {
		if err := Run(f.deps(home, t0.Add(m*time.Minute))); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.sent) != 1 {
		t.Fatal(f.sent)
	}
}

// A first run must not page the owner for last week's bus.
func TestLinesOlderThanTheWindowAreSkipped(t *testing.T) {
	home := setup(t, map[string]string{"quotes.log": done(t0)})
	f := &fake{}
	if err := Run(f.deps(home, t0.Add(13*time.Hour))); err != nil || len(f.sent) != 0 {
		t.Fatal(err, f.sent)
	}
}

// Silent success: a send that fails is not marked sent, retries next run, and says so on the bus once.
func TestASendFailureIsLoudAndRetried(t *testing.T) {
	home := setup(t, map[string]string{"quotes.log": done(t0)})
	f := &fake{sendErr: errors.New("cmux send: no surface:29")}
	// The reason goes on the bus once per failing streak; the wake itself keeps its 20 min rhythm there.
	for _, c := range []struct{ m, want int }{{21, 2}, {26, 2}, {41, 3}} {
		m, want := time.Duration(c.m), c.want
		if err := Run(f.deps(home, t0.Add(m*time.Minute))); (err == nil) != (m == 26) {
			t.Fatal(m, err)
		}
		if got := alertLines(t, home); len(got) != want || !strings.Contains(got[0], "no surface:29") {
			t.Fatal(m, got)
		}
	}
	f.sendErr = nil
	if err := Run(f.deps(home, t0.Add(61*time.Minute))); err != nil || len(f.sent) != 1 {
		t.Fatal(err, f.sent)
	}
}

func screen(title, text string) Screen { return Screen{Surface: "surface:7", Title: title, Text: text} }

const question = "Which branch should the fix go to, main or develop?\n\n❓\n"

func TestAQuestionScreenUnchangedFor10MinAlerts(t *testing.T) {
	home := setup(t, nil)
	f := &fake{screens: []Screen{screen("quotes: lead", question)}}
	for _, m := range []time.Duration{0, 5, 11} {
		if err := Run(f.deps(home, t0.Add(m*time.Minute))); err != nil {
			t.Fatal(err)
		}
		if m < 11 && len(f.sent) != 0 {
			t.Fatal(m, f.sent)
		}
	}
	if len(f.sent) != 1 || !strings.Contains(f.sent[0], "quotes: lead") {
		t.Fatal(f.sent)
	}
}

func TestADialogScreenAlerts(t *testing.T) {
	home := setup(t, nil)
	dialog := " Do you want to proceed?\n ❯ 1. Yes\n   2. No, and tell Claude what to do differently (esc)\n"
	f := &fake{screens: []Screen{screen("axi: lead", dialog)}}
	Run(f.deps(home, t0))
	Run(f.deps(home, t0.Add(11*time.Minute)))
	if len(f.sent) != 1 {
		t.Fatal(f.sent)
	}
}

func TestAChangingOrDoneScreenDoesNotAlert(t *testing.T) {
	home := setup(t, nil)
	f := &fake{}
	for i, text := range []string{question, "working…\n" + question, "all done\n✅\n"} {
		f.screens = []Screen{screen("quotes: lead", text)}
		Run(f.deps(home, t0.Add(time.Duration(i*6)*time.Minute)))
	}
	f.screens = []Screen{screen("quotes: lead", "all done\n✅\n")}
	Run(f.deps(home, t0.Add(30*time.Minute)))
	if len(f.sent) != 0 {
		t.Fatal(f.sent)
	}
}

// Crius's own tab asks the owner directly; the owner reads it there.
func TestCriusOwnTabIsSkipped(t *testing.T) {
	home := setup(t, nil)
	f := &fake{screens: []Screen{screen("crius", question)}}
	Run(f.deps(home, t0))
	Run(f.deps(home, t0.Add(11*time.Minute)))
	if len(f.sent) != 0 {
		t.Fatal(f.sent)
	}
}

// "Failed to look" is not "found nothing".
func TestAScreenReadFailureAlertsOnce(t *testing.T) {
	home := setup(t, nil)
	f := &fake{scrErr: errors.New("cmux: socket not found")}
	Run(f.deps(home, t0))
	Run(f.deps(home, t0.Add(5*time.Minute)))
	if len(f.sent) != 1 || !strings.Contains(f.sent[0], "socket not found") {
		t.Fatal(f.sent)
	}
}

// A crash mid-write once left bad JSON and every later run stopped at it. Now the run says so and starts afresh.
func TestACorruptStateIsReportedAndReset(t *testing.T) {
	home := setup(t, map[string]string{"quotes.log": done(t0)})
	testutil.Write(t, filepath.Join(home, "state", "wakewatch.json"), `{"Alerted":{"bus:`)
	f := &fake{}
	if err := Run(f.deps(home, t0.Add(21*time.Minute))); err != nil {
		t.Fatal(err)
	}
	if len(f.sent) != 1 {
		t.Fatal(f.sent)
	}
	b, _ := os.ReadFile(filepath.Join(home, "bus", "crius.log"))
	if !strings.Contains(string(b), "ALERT") || !strings.Contains(string(b), "wakewatch.json") {
		t.Fatal(string(b))
	}
	if err := Run(f.deps(home, t0.Add(26*time.Minute))); err != nil || len(f.sent) != 1 {
		t.Fatal(err, f.sent)
	}
}

// The state is replaced whole (temp file + rename), never truncated and rewritten in place: a read-only old file
// shows which, since only a rename can replace it.
func TestTheStateIsReplacedNotRewritten(t *testing.T) {
	home := setup(t, nil)
	path := filepath.Join(home, "state", "wakewatch.json")
	testutil.Write(t, path, `{}`)
	os.Chmod(path, 0o400)
	if err := Run((&fake{}).deps(home, t0)); err != nil {
		t.Fatal(err)
	}
	if left, _ := filepath.Glob(filepath.Join(home, "state", "wakewatch.json?*")); len(left) != 0 {
		t.Fatal("temp files left:", left)
	}
}

// A line still being written has no newline yet; it is not read until it is whole.
func TestAnUnterminatedLastLineIsNotRead(t *testing.T) {
	home := setup(t, map[string]string{"quotes.log": strings.TrimSuffix(done(t0), "\n")})
	f := &fake{}
	if err := Run(f.deps(home, t0.Add(21*time.Minute))); err != nil || len(f.sent) != 0 {
		t.Fatal(err, f.sent)
	}
}

// One tab that cannot be read is an alert for that tab; the other tabs are still checked.
func TestOneTabReadFailureAlertsForThatTabOnly(t *testing.T) {
	home := setup(t, nil)
	f := &fake{screens: []Screen{screen("quotes: lead", question), {Surface: "surface:9", Title: "axi: lead", Err: "surface gone"}}}
	for _, m := range []time.Duration{0, 5, 11} {
		if err := Run(f.deps(home, t0.Add(m*time.Minute))); err != nil {
			t.Fatal(err)
		}
	}
	all := strings.Join(f.sent, "\n")
	if strings.Count(all, "surface gone") != 1 || !strings.Contains(all, "waits on a question") {
		t.Fatal(f.sent)
	}
}

// Once a tab stops asking, the same question asked again later alerts again.
func TestAResolvedScreenCanAlertAgain(t *testing.T) {
	home := setup(t, nil)
	f := &fake{}
	for _, step := range []struct {
		m    time.Duration
		text string
	}{{0, question}, {11, question}, {15, "all done\n✅\n"}, {20, question}, {31, question}} {
		f.screens = []Screen{screen("quotes: lead", step.text)}
		if err := Run(f.deps(home, t0.Add(step.m*time.Minute))); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.sent) != 2 {
		t.Fatal(f.sent)
	}
}

// The newest status emoji on the line decides: a question that a later ✅ on the same line closed is not asking.
func TestTheLastEmojiOnALineDecides(t *testing.T) {
	if asks("asked which branch ❓ answered: main ✅\n") {
		t.Error("❓ then ✅ counted as asking")
	}
	if !asks("done with step 1 ✅ now: main or develop? ❓\n") {
		t.Error("✅ then ❓ not counted as asking")
	}
}

// A secret in an over-long alert is redacted before the cut: a cut just before "@" would leave the password bare.
func TestALongAlertIsRedactedBeforeItIsCut(t *testing.T) {
	head := stamp(t0) + " DONE quotes:lead -> crius:lead — "
	pad := strings.Repeat("x", MaxLine-len([]rune("quotes.log: "+head+"postgres://user:hunter2")))
	home := setup(t, map[string]string{"quotes.log": head + pad + "postgres://user:hunter2@db.internal/app tail\n"})
	f := &fake{}
	if err := Run(f.deps(home, t0.Add(21*time.Minute))); err != nil || len(f.sent) != 1 {
		t.Fatal(err, len(f.sent))
	}
	if strings.Contains(f.sent[0], "hunter2") {
		t.Fatal("password sent:", f.sent[0][len(f.sent[0])-80:])
	}
}

// While the whole tab check fails, a tab already reported as unreadable is not reported again once the check is back.
func TestAWholeCheckFailureKeepsPerTabFailures(t *testing.T) {
	home := setup(t, nil)
	bad := Screen{Surface: "surface:9", Title: "axi: lead", Err: "surface gone"}
	f := &fake{screens: []Screen{bad}}
	Run(f.deps(home, t0))
	f.scrErr = errors.New("cmux: socket not found")
	Run(f.deps(home, t0.Add(5*time.Minute)))
	f.scrErr = nil
	Run(f.deps(home, t0.Add(10*time.Minute)))
	if n := strings.Count(strings.Join(f.sent, "\n"), "surface gone"); n != 1 || len(f.sent) != 2 {
		t.Fatal(n, f.sent)
	}
}

// tab draws a Claude Code screen: the last reply, the input box with the session name in its border, and a footer
// whose quota countdown changes every minute, as the personal status line does.
func tab(name, reply, footer string, at time.Time) string {
	left := t0.Add(3 * time.Hour).Sub(at)
	return "⏺ " + reply + "\n                                         8% until auto-compact\n" +
		"────────────────────────────── " + name + " ─\n❯ \n──────────────────────────────\n" +
		fmt.Sprintf("  Opus 5.5 │ main │ 7d 97%% (resets in %dh %dm)\n", int(left.Hours()), int(left.Minutes())%60) + footer
}

func busFrom(from string, at time.Time) string {
	return stamp(at) + " INFO " + from + " -> crius:lead — f169 run going\n"
}

func alertLines(t *testing.T, home string) []string {
	b, err := os.ReadFile(filepath.Join(home, "bus", "crius.log"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.Contains(l, " ALERT crius:watchdog -> crius — ") {
			out = append(out, l)
		}
	}
	return out
}

// runAt runs at each minute after t0 with the screens drawn for that minute.
func runAt(t *testing.T, home string, f *fake, draw func(at time.Time) []Screen, minutes ...int) {
	t.Helper()
	for _, m := range minutes {
		at := t0.Add(time.Duration(m) * time.Minute)
		f.screens = draw(at)
		if err := Run(f.deps(home, at)); err != nil && f.sendErr == nil {
			t.Fatal(m, err)
		}
	}
}

// Case 10-21 23:30: quotes:lead finished a turn, posted nothing and sat idle; the owner found it, Crius did not.
// Owner (ORDER 10-22 13:13): any idle tab of a team alerts after 10 min.
func TestAnIdleLeadAlertsAfter10MinAndWakesCrius(t *testing.T) {
	home := setup(t, map[string]string{"quotes.log": busFrom("quotes:lead", t0)})
	f := &fake{}
	draw := func(at time.Time) []Screen {
		return []Screen{screen("quotes", tab("quotes: lead", "The f177 rerun is on :7090.", "  ⏵⏵ bypass permissions on\n", at))}
	}
	runAt(t, home, f, draw, 0, 5, 9)
	if len(f.sent) != 0 || len(alertLines(t, home)) != 0 {
		t.Fatal(f.sent, alertLines(t, home))
	}
	runAt(t, home, f, draw, 11)
	if len(f.sent) != 1 || !strings.Contains(f.sent[0], "quotes:lead") || !strings.Contains(f.sent[0], "idle") {
		t.Fatal(f.sent)
	}
	if a := alertLines(t, home); len(a) != 0 {
		t.Fatal("woke Crius on the bus, not in its tab:", a)
	}
}

func TestABusyLeadIsNotIdle(t *testing.T) {
	for _, c := range []struct{ reply, footer string }{
		{"✻ Running tests… (2m 10s · esc to interrupt)", "  ⏵⏵ bypass permissions on\n"},
		{"done for now", "  ⏵⏵ bypass permissions on · 1 shell\n"},
		{"done for now", "  ⏵⏵ bypass permissions on · 1 shell, 1 monitor\n"},
		{"done for now", "  ⏵⏵ bypass permissions on · 2 background tasks\n"},
		{"done for now", "  ⏵⏵ bypass permissions on\n\n  ⏺ main\n  ◯ general-purpose  Writing docs/briefs/README.md      7m 11s\n"},
	} {
		home := setup(t, map[string]string{"quotes.log": busFrom("quotes:lead", t0)})
		f := &fake{}
		runAt(t, home, f, func(at time.Time) []Screen {
			return []Screen{screen("quotes", tab("quotes: lead", c.reply, c.footer, at))}
		}, 0, 16, 30)
		if len(f.sent) != 0 || len(alertLines(t, home)) != 0 {
			t.Fatal(c, f.sent)
		}
	}
}

func TestOnlyALineFromTheLeadItselfResetsIdle(t *testing.T) {
	bus := busFrom("quotes:lead", t0) + busFrom("quotes:lead", t0.Add(10*time.Minute)) +
		busFrom("quotes:qa", t0.Add(20*time.Minute)) + stamp(t0.Add(20*time.Minute)) + " ACK crius:lead -> quotes:lead — ok\n"
	home := setup(t, map[string]string{"quotes.log": bus})
	f := &fake{}
	draw := func(at time.Time) []Screen {
		return []Screen{screen("quotes", tab("quotes: lead", "waiting", "", at))}
	}
	runAt(t, home, f, draw, 0, 16, 19)
	if len(f.sent) != 0 {
		t.Fatal(f.sent)
	}
	runAt(t, home, f, draw, 21)
	if len(f.sent) != 1 {
		t.Fatal(f.sent)
	}
}

func TestOnlyTabsWithABusAreCheckedForIdle(t *testing.T) {
	home := setup(t, map[string]string{"quotes.log": busFrom("quotes:lead", t0)})
	f := &fake{}
	runAt(t, home, f, func(at time.Time) []Screen {
		return []Screen{
			{Surface: "surface:8", Title: "first-order · lead", Text: tab("first-order · lead", "done", "", at)},
			{Surface: "surface:9", Title: "zsh", Text: "~ % \n"},
		}
	}, 0, 16, 30)
	if len(f.sent) != 0 {
		t.Fatal(f.sent)
	}
}

// Crius 10-22 12:09 ORDER: Quotes builders sat idle after a turn with nothing running (10:33: 6 of 8 since 06:36-09:56;
// 12:09: P3 since its READY at 12:00), and only Crius's own round found them. Any tab of a watched team counts, after
// 10 min (owner, ORDER 10-22 13:13). Builders post as "P3", not "quotes:p3", so their screen alone tells how long they have sat.
func TestAnIdleBuilderAlertsAfter10Min(t *testing.T) {
	for _, c := range []struct{ title, bus, reply string }{
		{"quotes · p6", stamp(t0.Add(-4*time.Hour)) + " READY P6 -> quotes:lead — F-226 on stream/l1\n", "Waiting for another stream on main.\n\n⏳"},
		{"quotes · p3", stamp(t0) + " READY P3 -> quotes:lead — F-141 on stream/d\n", "READY posted; QA2 checks it next.\n\n⏳"},
		{"quotes · qa1", busFrom("quotes:lead", t0), "J1 passed on main.\n\n✅"},
	} {
		home := setup(t, map[string]string{"quotes.log": c.bus})
		f := &fake{}
		draw := func(at time.Time) []Screen { return []Screen{screen(c.title, tab(c.title, c.reply, "", at))} }
		runAt(t, home, f, draw, 0, 5, 9)
		if len(f.sent) != 0 {
			t.Fatal(c.title, "alerted before 10 min:", f.sent)
		}
		runAt(t, home, f, draw, 11, 20)
		who := strings.Replace(c.title, " · ", ":", 1)
		if len(f.sent) != 1 || !strings.Contains(f.sent[0], who+" (tab") || !strings.Contains(f.sent[0], "is idle") {
			t.Fatal(c.title, f.sent)
		}
		// Review 897b5c1 P3: no line is sent as quotes:p6, so only the screen's age is known.
		if !strings.Contains(f.sent[0], "screen unchanged for 11 min, no bus line from "+who) {
			t.Fatal(c.title, f.sent)
		}
	}
}

func TestABusyBuilderIsNotIdle(t *testing.T) {
	for _, footer := range []string{
		"  ⏵⏵ bypass permissions on · 1 shell\n",
		"  ⏵⏵ bypass permissions on · 2 background tasks\n",
		"  ⏵⏵ bypass permissions on · 1 agent\n",
		"  ⏵⏵ bypass permissions on\n\n  ⏺ main\n  ◯ general-purpose  Writing docs/briefs/README.md      7m 11s\n",
	} {
		home := setup(t, map[string]string{"quotes.log": busFrom("quotes:lead", t0)})
		f := &fake{}
		runAt(t, home, f, func(at time.Time) []Screen {
			return []Screen{screen("quotes · p3", tab("quotes · p3", "Waiting for QA2.\n\n⏳", footer, at))}
		}, 0, 21, 40)
		if len(f.sent) != 0 {
			t.Fatal(footer, f.sent)
		}
	}
	// The same tab with nothing under its box does alert, so the cases above are quiet for their footer alone.
	home := setup(t, map[string]string{"quotes.log": busFrom("quotes:lead", t0)})
	f := &fake{}
	runAt(t, home, f, func(at time.Time) []Screen {
		return []Screen{screen("quotes · p3", tab("quotes · p3", "Waiting for QA2.\n\n⏳", "  ⏵⏵ bypass permissions on\n", at))}
	}, 0, 21, 40)
	if len(f.sent) != 1 {
		t.Fatal("positive control:", f.sent)
	}
}

func TestATabTitleNamesTheSessionWhenTheBorderDoesNot(t *testing.T) {
	home := setup(t, map[string]string{"cortex.log": busFrom("cortex:lead", t0)})
	f := &fake{}
	runAt(t, home, f, func(at time.Time) []Screen {
		return []Screen{screen("cortex · lead", strings.Replace(tab("x", "done", "", at), " x ─", "─", 1))}
	}, 0, 16)
	if len(f.sent) != 1 || !strings.Contains(f.sent[0], "cortex:lead") {
		t.Fatal(f.sent)
	}
}

// Crius 10-21 23:32: the word CLEAR-READY on a screen counts like the bus line, for any role.
func TestCLEARREADYOnAScreenAlertsForAnyRole(t *testing.T) {
	home := setup(t, map[string]string{"crius.log": busFrom("crius:build", t0)})
	f := &fake{}
	runAt(t, home, f, func(at time.Time) []Screen {
		return []Screen{screen("crius · build", tab("crius · build", "CLEAR-READY: item 3 done, next item 4.\n\n⏳", "", at))}
	}, 0, 16)
	if len(f.sent) != 1 || !strings.Contains(f.sent[0], "CLEAR-READY") || !strings.Contains(f.sent[0], "crius:build") {
		t.Fatal(f.sent)
	}
}

// Every role waits 10 min now (owner, ORDER 10-22 13:13), so CLEAR-READY only adds its note to the alert.
func TestCLEARREADYAboveTheLastReplyDoesNotCount(t *testing.T) {
	home := setup(t, map[string]string{"crius.log": busFrom("crius:build", t0)})
	f := &fake{}
	runAt(t, home, f, func(at time.Time) []Screen {
		return []Screen{screen("crius · build", "  at ~200K write CLEAR-READY and stop\n"+tab("crius · build", "Waiting for GO.\n\n⏳", "", at))}
	}, 0, 16)
	if len(f.sent) != 1 || strings.Contains(f.sent[0], "CLEAR-READY") {
		t.Fatal(f.sent)
	}
}

// The status line's countdown changes every minute; it must not restart the 10 min wait of a question.
func TestAQuestionWithAClockInTheFooterStillAlerts(t *testing.T) {
	home := setup(t, nil)
	f := &fake{}
	runAt(t, home, f, func(at time.Time) []Screen {
		return []Screen{screen("axi", tab("axi: lead", "Which branch, main or develop?\n\n❓", "", at))}
	}, 0, 5, 11)
	if len(f.sent) != 1 || !strings.Contains(f.sent[0], "question") {
		t.Fatal(f.sent)
	}
}

// Review 0bac93a P2: a bus line from the lead ends one idle spell; the next spell alerts and wakes Crius again.
func TestBusActivityReArmsTheIdleAlert(t *testing.T) {
	home := setup(t, map[string]string{"quotes.log": busFrom("quotes:lead", t0)})
	f := &fake{}
	draw := func(at time.Time) []Screen {
		return []Screen{screen("quotes", tab("quotes: lead", "done", "", at))}
	}
	runAt(t, home, f, draw, 0, 16)
	testutil.Write(t, filepath.Join(home, "bus", "quotes.log"), busFrom("quotes:lead", t0)+busFrom("quotes:lead", t0.Add(20*time.Minute)))
	runAt(t, home, f, draw, 21, 26, 36)
	if len(f.sent) != 2 {
		t.Fatal(f.sent)
	}
}

// Review 0bac93a P2: with no reply marker on screen there is no last reply, so CLEAR-READY in a brief does not count.
func TestCLEARREADYWithNoReplyMarkerDoesNotCount(t *testing.T) {
	home := setup(t, map[string]string{"crius.log": busFrom("crius:build", t0)})
	f := &fake{}
	runAt(t, home, f, func(at time.Time) []Screen {
		return []Screen{screen("crius · build", strings.Replace(tab("crius · build", "x", "", at), "⏺ x", "  at ~200K write CLEAR-READY and stop", 1))}
	}, 0, 16)
	if len(f.sent) != 1 || strings.Contains(f.sent[0], "CLEAR-READY") {
		t.Fatal(f.sent)
	}
}

// Review 0bac93a P3: another session in the same tab starts its own clock, even with the same text on screen.
func TestANewSessionInTheSameTabStartsAfresh(t *testing.T) {
	home := setup(t, map[string]string{"quotes.log": busFrom("quotes:lead", t0), "cortex.log": busFrom("cortex:lead", t0)})
	f := &fake{}
	runAt(t, home, f, func(at time.Time) []Screen {
		return []Screen{screen("quotes · lead", tab("quotes: lead", "done", "", at))}
	}, 0, 16)
	draw := func(at time.Time) []Screen {
		return []Screen{screen("cortex · lead", tab("cortex · lead", "done", "", at))}
	}
	runAt(t, home, f, draw, 17, 26)
	if len(f.sent) != 1 {
		t.Fatal(f.sent)
	}
	runAt(t, home, f, draw, 28)
	if len(f.sent) != 2 || !strings.Contains(f.sent[1], "cortex:lead") {
		t.Fatal(f.sent)
	}
}

// Owner 10-21 23:5x: the same item wakes Crius at most once per 20 min; while it stays open it wakes it again.
func TestAnOpenItemWakesCriusAtMostOncePer20Min(t *testing.T) {
	home := setup(t, nil)
	f := &fake{screens: []Screen{screen("quotes: lead", question)}}
	for _, m := range []time.Duration{0, 11, 25, 30, 31, 45, 51} {
		Run(f.deps(home, t0.Add(m*time.Minute)))
	}
	if len(f.sent) != 3 { // 11, 31, 51
		t.Fatal("waiting tab:", f.sent)
	}
	home = setup(t, nil)
	f = &fake{scrErr: errors.New("cmux: socket not found")}
	for _, m := range []time.Duration{0, 19, 20, 25*60 + 5} {
		Run(f.deps(home, t0.Add(m*time.Minute)))
	}
	if len(f.sent) != 3 { // 0, 20, a day later
		t.Fatal("failing check:", f.sent)
	}
}

// Owner 10-21 23:5x: each alert is one line typed into Crius's tab. cmux send reads a backslash-n as Enter, so no
// backslash and no newline may reach it, and a long line is cut.
// Control bytes from a bus line (Ctrl-U, Esc, DEL) would edit Crius's prompt: "\x15/clear" wipes the line and
// runs /clear (review of 91854cf, P1).
func TestEachAlertIsOneLineForCriusTab(t *testing.T) {
	bus := done(t0) + stamp(t0) + ` NEED axi:lead -> crius:lead — run \n then \r then a` + "\t" + "tab \x15/clear\x1b[A\x7f\u0085 " +
		strings.Repeat("y", 900) + "\n"
	home := setup(t, map[string]string{"quotes.log": bus})
	f := &fake{screens: []Screen{screen("quotes: lead", question)}}
	Run(f.deps(home, t0.Add(5*time.Minute)))
	if err := Run(f.deps(home, t0.Add(21*time.Minute))); err != nil {
		t.Fatal(err)
	}
	if len(f.sent) != 3 {
		t.Fatal(f.sent)
	}
	for _, m := range f.sent {
		if !strings.HasPrefix(m, "WAKEWATCH: ") || strings.ContainsAny(m, "\\\n\r\t") || len([]rune(m)) > len("WAKEWATCH: ")+MaxLine+1 {
			t.Errorf("%q", m)
		}
		for _, r := range m {
			if !unicode.IsPrint(r) {
				t.Errorf("control rune %U in %q", r, m)
				break
			}
		}
	}
}

// Brief P7 item 4: below 20 GB free disk or above 26 GB swap (owner 2026-10-25: 20 GB was noise at a steady 23 GB) wakes Crius,
// by the same tab line; swap again every 20 min, disk only after a further 2 GB drop or 2 h (owner GO 2026-10-25).
func TestLowDiskOrHighSwapWakesCrius(t *testing.T) {
	for _, c := range []struct {
		free, swap float64
		want       string
	}{{19.5, 1, "disk"}, {200, 26.5, "swap"}, {200, 23.3, ""}, {20, 26, ""}, {25, 3, ""}} {
		home := setup(t, nil)
		f := &fake{freeGB: c.free, swapGB: c.swap}
		for _, m := range []time.Duration{0, 5, 21} {
			if err := Run(f.deps(home, t0.Add(m*time.Minute))); err != nil {
				t.Fatal(err)
			}
		}
		if c.want == "" {
			if len(f.sent) != 0 {
				t.Fatal(c, f.sent)
			}
			continue
		}
		if len(f.sent) != map[string]int{"disk": 1, "swap": 2}[c.want] || !strings.Contains(f.sent[0], c.want) || !strings.Contains(f.sent[0], map[string]string{"disk": "20 GB", "swap": "26 GB"}[c.want]) {
			t.Fatal(c, f.sent)
		}
	}
}

// Brief fixes-1025 item 7 (E1, crius:lead 18:38): each run logs the 1-minute load for the experiment scorecard; a
// load read that fails wakes Crius, as a disk read that fails does.
func TestEachRunLogsTheLoad(t *testing.T) {
	home := setup(t, nil)
	f := &fake{load: 6.5}
	for _, m := range []time.Duration{0, 5} {
		if err := Run(f.deps(home, t0.Add(m*time.Minute))); err != nil {
			t.Fatal(err)
		}
	}
	want := t0.Format(time.RFC3339) + " 6.50\n" + t0.Add(5*time.Minute).Format(time.RFC3339) + " 6.50\n"
	if got := testutil.Read(t, filepath.Join(home, "logs", "load.log")); got != want {
		t.Fatalf("%q, want %q", got, want)
	}
	f = &fake{loadErr: errors.New("sysctl: exit status 1")}
	if err := Run(f.deps(home, t0.Add(10*time.Minute))); err != nil {
		t.Fatal(err)
	}
	if len(f.sent) != 1 || !strings.Contains(f.sent[0], "the load check failed to look: sysctl: exit status 1") {
		t.Fatal(f.sent)
	}
}

// Failed to look is not found nothing: a disk or swap read that fails wakes Crius too.
func TestADiskOrSwapReadFailureIsAnAlert(t *testing.T) {
	home := setup(t, nil)
	f := &fake{resErr: errors.New("sysctl: exit status 1")}
	if err := Run(f.deps(home, t0)); err != nil {
		t.Fatal(err)
	}
	if len(f.sent) != 1 || !strings.Contains(f.sent[0], "sysctl: exit status 1") {
		t.Fatal(f.sent)
	}
}

// Brief P7 item 4: a tool the team needs (cmux, gh, az, bun, docker) that fails or does not answer wakes Crius, one
// alert per tool, on the same 20 min rhythm; a tool that answers again stops it.
func TestAToolThatDoesNotAnswerWakesCrius(t *testing.T) {
	home := setup(t, nil)
	f := &fake{tools: map[string]error{"gh": errors.New("gh auth status: exit status 1: token invalid"), "bun": nil,
		"docker": errors.New("no answer in 10s")}}
	for _, m := range []int{0, 5, 21} {
		if err := Run(f.deps(home, t0.Add(time.Duration(m)*time.Minute))); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.sent) != 4 {
		t.Fatalf("want gh and docker at 0 and 21 min: %q", f.sent)
	}
	all := strings.Join(f.sent, "\n")
	for _, want := range []string{"gh", "token invalid", "docker", "no answer in 10s"} {
		if !strings.Contains(all, want) {
			t.Errorf("no %q in %q", want, f.sent)
		}
	}
	f.sent, f.tools = nil, map[string]error{"gh": nil, "bun": nil, "docker": nil}
	Run(f.deps(home, t0.Add(45*time.Minute)))
	if len(f.sent) != 0 {
		t.Fatalf("tools answer again: %q", f.sent)
	}
}

func freshSent(f *fake) []string {
	var out []string
	for _, m := range f.sent {
		if strings.Contains(m, "no fresh start") || strings.Contains(m, "fresh-start check") {
			out = append(out, m)
		}
	}
	return out
}

// Crius 10-21 23:58 (cmux-team SKILL §5, fresh context per item): a lead's DONE with no fresh start of that lead (a
// /clear or a relaunch, so a new session) within 20 min wakes Crius. Case 10-21: quotes:lead ran 00:57-00:00 through
// six DONEs in one session.
func TestALeadDONEWithNoFreshStartWakesCrius(t *testing.T) {
	bus := done(t0) + stamp(t0.Add(time.Minute)) + " ACK crius:lead -> quotes:lead — seen\n"
	old := Session{Name: "quotes: lead", ID: "18c04af5", Started: t0.Add(-2 * time.Hour)}
	for name, c := range map[string]struct {
		ses  []Session
		want int
	}{
		"same session":             {[]Session{old}, 2},
		"name with a dot":          {[]Session{{Name: "quotes · lead", ID: "1", Started: t0.Add(-time.Hour)}}, 2},
		"cleared after the DONE":   {[]Session{{Name: "quotes: lead", ID: "2", Started: t0.Add(5 * time.Minute)}}, 0},
		"old and a new one":        {[]Session{old, {Name: "quotes: lead", ID: "3", Started: t0.Add(3 * time.Minute)}}, 0},
		"new, nothing written yet": {[]Session{{Name: "quotes: lead", ID: "4"}}, 0},
		"no lead session":          {[]Session{{Name: "quotes: qa-api", ID: "5", Started: t0.Add(-time.Hour)}}, 0},
		"no session":               {nil, 0},
	} {
		home := setup(t, map[string]string{"quotes.log": bus})
		f := &fake{ses: c.ses}
		for _, m := range []int{10, 21, 30, 41} {
			if err := Run(f.deps(home, t0.Add(time.Duration(m)*time.Minute))); err != nil {
				t.Fatal(err)
			}
		}
		got := freshSent(f)
		if len(got) != c.want {
			t.Errorf("%s: want %d, got %q", name, c.want, got)
		}
		for _, m := range got {
			if !strings.Contains(m, "quotes:lead") || !strings.Contains(m, stamp(t0)) {
				t.Errorf("%s: %q", name, m)
			}
		}
	}
}

// Crius 10-22 01:07: Crius's ORDER to the lead after its DONE closes the alert (a send-back is a decision); an ACK
// does not (above), nor an ORDER before the DONE or to another lead.
func TestCriusOrderAfterTheDONEClosesTheFreshStartAlert(t *testing.T) {
	old := []Session{{Name: "quotes: lead", ID: "1", Started: t0.Add(-2 * time.Hour)}}
	order := func(at time.Time, to string) string {
		return stamp(at) + " ORDER crius:lead -> " + to + " — fix review P2 on the same item\n"
	}
	for name, c := range map[string]struct {
		bus  string
		want int
	}{
		"order after the DONE":  {done(t0) + order(t0.Add(2*time.Minute), "quotes:lead"), 0},
		"order before the DONE": {order(t0.Add(-time.Minute), "quotes:lead") + done(t0), 2},
		// Review of 24f04c9: the file's order counts, not the minute; a line may name several recipients.
		"order in the DONE's minute":        {done(t0) + order(t0, "quotes:lead"), 0},
		"order before, in the same minute":  {order(t0, "quotes:lead") + done(t0), 2},
		"order to several":                  {done(t0) + order(t0.Add(2*time.Minute), "crius:build, quotes:lead"), 0},
		"order to several, not to the lead": {done(t0) + order(t0.Add(2*time.Minute), "crius:build, quotes:qa-api"), 2},
		"order to several, spaces only":     {done(t0) + order(t0.Add(2*time.Minute), "crius:build quotes:lead"), 0},
		"order to another lead":             {done(t0) + order(t0.Add(2*time.Minute), "quotes:qa-api"), 2},
		"order from someone else":           {done(t0) + stamp(t0.Add(2*time.Minute)) + " ORDER quotes:qa-api -> quotes:lead — x\n", 2},
	} {
		home := setup(t, map[string]string{"quotes.log": c.bus})
		f := &fake{ses: old}
		for _, m := range []int{10, 21, 30, 41} {
			if err := Run(f.deps(home, t0.Add(time.Duration(m)*time.Minute))); err != nil {
				t.Fatal(err)
			}
		}
		if got := freshSent(f); len(got) != c.want {
			t.Errorf("%s: want %d, got %q", name, c.want, got)
		}
	}
}

// A DONE older than the 12 h window, or from a role that is not a lead, does not count; a failed session read is loud.
func TestTheFreshStartCheckSkipsOldAndNonLeadDONEs(t *testing.T) {
	old := Session{Name: "quotes: lead", ID: "1", Started: t0.Add(-20 * time.Hour)}
	home := setup(t, map[string]string{"quotes.log": done(t0.Add(-13*time.Hour)) +
		stamp(t0.Add(-1*time.Hour)) + " DONE quotes:qa-api -> crius:lead — F-87 · 1c6e705 · 12 OK\n"})
	f := &fake{ses: []Session{old, {Name: "quotes: qa-api", ID: "2", Started: t0.Add(-20 * time.Hour)}}}
	Run(f.deps(home, t0))
	if got := freshSent(f); len(got) != 0 {
		t.Fatalf("%q", got)
	}
	f.sesErr = errors.New("open ~/.claude/sessions: permission denied")
	Run(f.deps(home, t0.Add(time.Minute)))
	if got := freshSent(f); len(got) != 1 || !strings.Contains(got[0], "permission denied") {
		t.Fatalf("%q", got)
	}
}

// Crius ORDER 10-22 13:23: quotes:lead at ctx 21% with 2 monitors, landing train after train, was told it had no fresh
// start. A lead whose own tab shows ctx under 50% in the status line below its input box has room left: no alert.
func TestALeadWithContextUnder50PercentNeedsNoFreshStart(t *testing.T) {
	old := []Session{{Name: "quotes: lead", ID: "1", Started: t0.Add(-2 * time.Hour)}}
	status := func(ctx string) string {
		return "  Opus 5.5 ENT │ main │ $22.97 │ ██░░░░░░░░ " + ctx + " │ spend $0 of $600\n"
	}
	lead := func(ctx string) func(time.Time) []Screen {
		return func(at time.Time) []Screen {
			return []Screen{screen("quotes", tab("quotes: lead", "landed", status(ctx)+"  ⏵⏵ bypass permissions on · 2 monitors\n", at))}
		}
	}
	for name, c := range map[string]struct {
		draw func(time.Time) []Screen
		want int
	}{
		"ctx 21%":      {lead("ctx 21%"), 0},
		"ctx 49%":      {lead("ctx 49%"), 0},
		"ctx 50%":      {lead("ctx 50%"), 2},
		"ctx 87%":      {lead("ctx 87%"), 2},
		"no ctx shown": {lead("ctx –"), 2},
		"no lead tab":  {func(time.Time) []Screen { return nil }, 2},
		"another lead's tab": {func(at time.Time) []Screen {
			return []Screen{screen("axi", tab("axi: lead", "landed", status("ctx 21%"), at))}
		}, 2},
		"a builder's tab": {func(at time.Time) []Screen {
			return []Screen{screen("quotes · p3", tab("quotes: p3", "landed", status("ctx 21%"), at))}
		}, 2},
		"ctx typed in the box": {func(at time.Time) []Screen {
			text := strings.Replace(tab("quotes: lead", "landed", status("ctx 87%"), at), "❯ \n", "❯ it said ctx 21%\n", 1)
			return []Screen{screen("quotes", text)}
		}, 2},
		// Review: a dash line in a draft is not the box's bottom border, and a second tab of the same lead decides too.
		"ctx typed under a dash line": {func(at time.Time) []Screen {
			text := strings.Replace(tab("quotes: lead", "landed", status("ctx 87%"), at), "❯ \n", "❯ see\n  ───\n  ctx 21%\n", 1)
			return []Screen{screen("quotes", text)}
		}, 2},
		"two tabs of the lead": {func(at time.Time) []Screen {
			return append(lead("ctx 21%")(at), screen("quotes", tab("quotes: lead", "landed", status("ctx 87%"), at)))
		}, 2},
	} {
		home := setup(t, map[string]string{"quotes.log": done(t0), "axi.log": ""})
		f := &fake{ses: old}
		runAt(t, home, f, c.draw, 10, 21, 30, 41)
		if got := freshSent(f); len(got) != c.want {
			t.Errorf("%s: want %d, got %q", name, c.want, got)
		}
	}
}

// projectAlerts runs the watch over two projects, quotes and axi, each with a missed DONE, a tab waiting on a question,
// an idle lead tab and a lead with no fresh start, and gives the lines sent for each project.
func projectAlerts(t *testing.T, list string) (quotes, axi []string) {
	t.Helper()
	axiDone := strings.Replace(done(t0), "quotes:lead", "axi:lead", 1)
	home := setup(t, map[string]string{"quotes.log": done(t0), "axi.log": axiDone})
	if list != "" {
		testutil.Write(t, filepath.Join(home, "wakewatch-projects"), list)
	}
	f := &fake{ses: []Session{{Name: "quotes: lead", ID: "1", Started: t0.Add(-time.Hour)},
		{Name: "axi: lead", ID: "2", Started: t0.Add(-time.Hour)}}}
	draw := func(at time.Time) []Screen {
		return []Screen{{Surface: "surface:1", Title: "quotes: qa", Text: question},
			{Surface: "surface:2", Title: "quotes", Text: tab("quotes: lead", "Rerun is on.", "", at)},
			{Surface: "surface:3", Title: "axi: qa", Text: question},
			{Surface: "surface:4", Title: "axi", Text: tab("axi: lead", "Rerun is on.", "", at)}}
	}
	runAt(t, home, f, draw, 0, 11, 16, 21, 30)
	for _, m := range f.sent {
		if strings.Contains(m, "axi") {
			axi = append(axi, m)
		} else {
			quotes = append(quotes, m)
		}
	}
	return quotes, axi
}

// kinds names the alert kinds among lines: a missed wake, a waiting tab, an idle lead, a lead with no fresh start.
func kinds(lines []string) []string {
	var out []string
	for _, k := range []string{".log: ", "waits on a question", "is idle", "no fresh start"} {
		for _, m := range lines {
			if strings.Contains(m, k) {
				out = append(out, k)
				break
			}
		}
	}
	return out
}

// Owner 10-22 02:2x (Crius ORDER 02:16): only the projects in ~/.crius/wakewatch-projects are watched, so wakes about
// other teams do not fill Crius's context. Disk, swap and tools belong to the Mac and are always watched.
func TestOnlyTheListedProjectsAreWatched(t *testing.T) {
	all := []string{".log: ", "waits on a question", "is idle", "no fresh start"}
	quotes, axi := projectAlerts(t, "")
	if fmt.Sprint(kinds(quotes)) != fmt.Sprint(all) || fmt.Sprint(kinds(axi)) != fmt.Sprint(all) {
		t.Fatalf("with no list every project is watched: quotes %q, axi %q", quotes, axi)
	}
	for _, list := range []string{"quotes\ncrius\n", "# owner 10-22\nquotes, crius", "crius quotes # Quotes and Crius"} {
		quotes, axi := projectAlerts(t, list)
		if len(axi) != 0 {
			t.Errorf("%q: axi is not listed, yet: %q", list, axi)
		}
		if fmt.Sprint(kinds(quotes)) != fmt.Sprint(all) {
			t.Errorf("%q: quotes is listed, got %q", list, quotes)
		}
	}
}

// A list that names no project would silence every team: it is an alert of its own, and everything stays watched.
func TestAnEmptyProjectListIsAnAlert(t *testing.T) {
	quotes, axi := projectAlerts(t, "# nothing yet\n")
	if len(axi) == 0 {
		t.Fatal("an empty list watched nothing")
	}
	var said bool
	for _, m := range quotes {
		said = said || strings.Contains(m, "wakewatch-projects") && strings.Contains(m, "names no project")
	}
	if !said {
		t.Fatal(quotes)
	}
}

// A dialog hides the named input box, and a lead tab's title is often the bare project ("quotes"): the title's first
// word still names its project, so a listed project's dialog wakes Crius and another project's stays quiet.
func TestATabTitledOnlyByItsProjectIsWatched(t *testing.T) {
	home := setup(t, map[string]string{"quotes.log": ""})
	testutil.Write(t, filepath.Join(home, "wakewatch-projects"), "quotes crius\n")
	dialog := " Do you want to proceed?\n ❯ 1. Yes\n   2. No, and tell Claude what to do differently (esc)\n"
	f := &fake{screens: []Screen{{Surface: "surface:1", Title: "quotes", Text: dialog},
		{Surface: "surface:2", Title: "axi", Text: dialog}}}
	Run(f.deps(home, t0))
	Run(f.deps(home, t0.Add(11*time.Minute)))
	if len(f.sent) != 1 || !strings.Contains(f.sent[0], "surface:1") {
		t.Fatal(f.sent)
	}
}

// A tab that cannot be read is named by its title too: another project's stays quiet, a listed project's alerts, and
// one with no title still alerts, as nothing says whose it is.
func TestATabReadFailureOfAnUnlistedProjectStaysQuiet(t *testing.T) {
	home := setup(t, map[string]string{"quotes.log": ""})
	testutil.Write(t, filepath.Join(home, "wakewatch-projects"), "quotes crius\n")
	f := &fake{screens: []Screen{{Surface: "surface:1", Title: "quotes: qa", Err: "surface gone"},
		{Surface: "surface:2", Title: "axi: lead", Err: "surface gone"}, {Surface: "surface:3", Err: "surface gone"},
		{Surface: "surface:4", Title: "✳ Quotes: lead", Err: "surface gone"}, {Surface: "surface:5", Title: "❓", Err: "surface gone"}}}
	if err := Run(f.deps(home, t0)); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(f.sent, "\n")
	for _, want := range []string{"surface:1", "surface:3", "surface:4", "surface:5"} {
		if !strings.Contains(got, want) {
			t.Fatal(want, f.sent)
		}
	}
	if strings.Contains(got, "surface:2") {
		t.Fatal(f.sent)
	}
}

// A wake goes by its sender's team as well as its bus: an unlisted team's DONE on a listed bus stays quiet. A sender
// with no team ("QA2" on quotes.log) goes by its bus.
func TestAnUnlistedSenderOnAListedBusStaysQuiet(t *testing.T) {
	axiDone := strings.Replace(done(t0), "quotes:lead", "axi:lead", 1)
	qaDone := strings.Replace(done(t0), "quotes:lead", "QA2", 1)
	home := setup(t, map[string]string{"quotes.log": qaDone, "crius.log": axiDone})
	testutil.Write(t, filepath.Join(home, "wakewatch-projects"), "quotes crius\n")
	f := &fake{}
	if err := Run(f.deps(home, t0.Add(21*time.Minute))); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(f.sent, "\n")
	if !strings.Contains(got, "quotes.log: ") || !strings.Contains(got, "QA2") || strings.Contains(got, "axi") {
		t.Fatal(f.sent)
	}
}

// A listed name with no bus file (a capital, a typo, "quotes.log") would silence that team with no word: it is an
// alert that names it. Names match bus files whatever their case.
func TestAListedNameWithNoBusIsAnAlert(t *testing.T) {
	all := []string{".log: ", "waits on a question", "is idle", "no fresh start"}
	quotes, axi := projectAlerts(t, "Quotes, qoutes\n")
	if len(axi) != 0 || fmt.Sprint(kinds(quotes)) != fmt.Sprint(all) {
		t.Fatalf("Quotes is listed: quotes %q, axi %q", quotes, axi)
	}
	var said bool
	for _, m := range quotes {
		said = said || strings.Contains(m, "qoutes") && strings.Contains(m, "no bus")
	}
	if !said {
		t.Fatal(quotes)
	}
}

// S2 (10-22 11:37-12:42): wakewatch could not type into Crius's tab 8 times and dropped each wake; the bus said only
// why. Crius's waiter wakes on a bus line, so a wake that cannot be typed goes there, every one, not only the reason.
func TestAnAlertThatCannotBeTypedGoesOnTheBus(t *testing.T) {
	second := stamp(t0) + " DONE quotes:p3 -> crius:lead — F-275 · 9a1b2c3 · suite 88 OK\n"
	home := setup(t, map[string]string{"quotes.log": done(t0) + second})
	f := &fake{sendErr: errors.New("Crius's prompt holds a draft that is not wakewatch's: nothing typed")}
	if err := Run(f.deps(home, t0.Add(21*time.Minute))); err == nil {
		t.Fatal("no error")
	}
	got := strings.Join(alertLines(t, home), "\n")
	for _, want := range []string{"holds a draft", "F-86 · 1c6e705", "F-275 · 9a1b2c3"} {
		if !strings.Contains(got, want) {
			t.Errorf("crius.log has no ALERT with %q:\n%s", want, got)
		}
	}
	if n := len(alertLines(t, home)); n != 3 {
		t.Fatalf("%d ALERT lines, want the reason and the two wakes:\n%s", n, got)
	}
}

// ORDER 10-22 13:36, the owner's third complaint: the openrig workspace opened with no name and no colour, and its
// tab kept a default name; nothing said so. A workspace with either missing, and a team tab whose name does not
// start with its project, alert.
func TestAWorkspaceWithNoNameOrNoColourAlerts(t *testing.T) {
	home := setup(t, nil)
	f := &fake{wss: []Workspace{
		{Ref: "workspace:1", Title: "Crius", Named: true, Color: "#6A1B9A"},
		{Ref: "workspace:5", Title: "~/Projects/openrig", Named: false, Color: ""},
		{Ref: "workspace:6", Title: "Tamada", Named: true, Color: ""},
	}}
	if err := Run(f.deps(home, t0)); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(f.sent, "\n")
	if len(f.sent) != 2 || !strings.Contains(all, `workspace:5 "~/Projects/openrig" has no name and no colour`) ||
		!strings.Contains(all, `workspace:6 "Tamada" has no colour`) || strings.Contains(all, "Crius") {
		t.Fatal(f.sent)
	}
}

func TestATeamTabWithADefaultNameAlerts(t *testing.T) {
	home := setup(t, nil)
	f := &fake{screens: []Screen{
		{Surface: "surface:7", Title: "claude", Text: tab("openrig: lead", "working", "", t0)},
		{Surface: "surface:8", Title: "OpenRig · lead", Text: tab("openrig: lead", "working", "", t0)},
		{Surface: "surface:9", Title: "quotes: rig", Text: tab("quotes: rig", "working", "", t0)},
		{Surface: "surface:10", Title: "zsh", Text: "o@mac ~ % \n"},
	}}
	if err := Run(f.deps(home, t0)); err != nil {
		t.Fatal(err)
	}
	if len(f.sent) != 1 || !strings.Contains(f.sent[0], `tab surface:7 "claude" runs openrig:lead`) {
		t.Fatal(f.sent)
	}
}

// Failing to look is not finding nothing.
func TestAFailedWorkspaceReadAlerts(t *testing.T) {
	home := setup(t, nil)
	f := &fake{wsErr: errors.New("cmux workspace list: socket closed")}
	if err := Run(f.deps(home, t0)); err != nil {
		t.Fatal(err)
	}
	if len(f.sent) != 1 || !strings.Contains(f.sent[0], "failed to look: cmux workspace list: socket closed") {
		t.Fatal(f.sent)
	}
}

// OpenRig parts item 3 (owner brief 10-22 14:27): wakewatch reads crius-hook's session state first; the screen is the
// fallback and the cross-check. ~/.crius/wakewatch-mode says which decides: "observe" (the default, for the first day
// after the hook goes in) keeps the screen deciding; "decide" lets the state decide. Either way each disagreement goes
// to ~/.crius/logs/wakewatch-state.log.

const sfID = "6F1C0A2E-surface" // the tab's cmux surface id, as CMUX_SURFACE_ID gives it to the hook

func withID(s Screen) Screen { s.ID = sfID; return s }

func hookState(id, state, project, role string) sessionstate.State {
	return sessionstate.State{SessionID: id, State: state, Time: t0, Surface: sfID, Project: project, Role: role}
}

func setMode(t *testing.T, home, mode string) {
	testutil.Write(t, filepath.Join(home, "wakewatch-mode"), mode+"\n")
}

func stateLog(t *testing.T, home string) string {
	b, err := os.ReadFile(filepath.Join(home, "logs", "wakewatch-state.log"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

// Case 10-22 14:10: crius-build's tab showed its own test text, a dialog quoted in code, while it worked; the screen
// read it as a dialog waiting on a person. The hook knows the session is running, or idle after its turn.
func TestADialogQuotedOnScreenIsNotADialogWhenTheHookSaysOtherwise(t *testing.T) {
	quoted := "the fixture:\n Do you want to proceed?\n ❯ 1. Yes\n   2. No, and tell Claude what to do differently (esc)"
	quotedTab := func(at time.Time) []Screen {
		return []Screen{withID(screen("quotes · p3", tab("quotes: p3", quoted, "  ⏵⏵ bypass permissions on\n", at)))}
	}
	spinning := func(at time.Time) []Screen { // the 14:10 case: a running tool printed the text, the spinner under it
		return []Screen{withID(screen("quotes · p3", tab("quotes: p3", quoted+"\n\n✽ Garnishing… (3m 14s · ↓ 3.4k tokens)", "  ⏵⏵ bypass permissions on\n", at)))}
	}
	// re-review 10-22: Claude Code 2.1.308's verbs include Razzle-dazzling, Topsy-turvying and Beboppin'
	spinningHyphen := func(at time.Time) []Screen {
		return []Screen{withID(screen("quotes · p3", tab("quotes: p3", quoted+"\n\n✻ Razzle-dazzling… (4s · ↓ 1.2k tokens)", "  ⏵⏵ bypass permissions on\n", at)))}
	}
	spinningApos := func(at time.Time) []Screen {
		return []Screen{withID(screen("quotes · p3", tab("quotes: p3", quoted+"\n\n✻ Beboppin'… (1m 2s · ↓ 9k tokens)", "  ⏵⏵ bypass permissions on\n", at)))}
	}
	// review 10-22: a real dialog under tool output that quotes the spinner's words
	quotesSpinner := func(at time.Time) []Screen {
		out := "⏺ Bash(grep -n interrupt wakewatch.go)\n  ⎿ 849: busy when the text has \"esc to interrupt\"\n"
		return []Screen{withID(screen("quotes · p3", tab("quotes: p3", out+quoted, "  ⏵⏵ bypass permissions on\n", at)))}
	}
	plainTab := func(at time.Time) []Screen {
		return []Screen{withID(screen("quotes · p3", tab("quotes: p3", "ok", "  ⏵⏵ bypass permissions on\n", at)))}
	}
	runningTab := func(at time.Time) []Screen { // a tool approved in its dialog runs: no hook says so
		return []Screen{withID(screen("quotes · p3", tab("quotes: p3", "Bash(make test)\n\n✻ Running… (12s · esc to interrupt)", "  ⏵⏵ bypass permissions on\n", at)))}
	}
	live := []Session{{Name: "quotes: p3", ID: "s1", Started: t0.Add(-time.Hour)}}
	for name, c := range map[string]struct {
		mode   string
		draw   func(time.Time) []Screen
		states []sessionstate.State
		ses    []Session
		want   string // "" no alert, "dialog" a waiting alert, "idle" an idle alert
		logged bool
	}{
		"running, decide":          {"decide", spinning, []sessionstate.State{hookState("s1", sessionstate.Running, "quotes", "p3")}, live, "", true},
		"running, hyphenated verb": {"decide", spinningHyphen, []sessionstate.State{hookState("s1", sessionstate.Running, "quotes", "p3")}, live, "", true},
		"running, verb with '":     {"decide", spinningApos, []sessionstate.State{hookState("s1", sessionstate.Running, "quotes", "p3")}, live, "", true},
		"running, no spinner":      {"decide", quotedTab, []sessionstate.State{hookState("s1", sessionstate.Running, "quotes", "p3")}, live, "dialog", false},
		"waiting, phrase quoted":   {"decide", quotesSpinner, []sessionstate.State{hookState("s1", sessionstate.Waiting, "quotes", "p3")}, live, "dialog", false},
		"idle, decide":             {"decide", quotedTab, []sessionstate.State{hookState("s1", sessionstate.Idle, "quotes", "p3")}, live, "idle", true},
		"running, observe":         {"observe", spinning, []sessionstate.State{hookState("s1", sessionstate.Running, "quotes", "p3")}, live, "dialog", true},
		"no mode file is observe":  {"", spinning, []sessionstate.State{hookState("s1", sessionstate.Running, "quotes", "p3")}, live, "dialog", true},
		"no state: screen decides": {"decide", quotedTab, nil, live, "dialog", false},
		"dead session's state":     {"decide", quotedTab, []sessionstate.State{hookState("s1", sessionstate.Running, "quotes", "p3")}, nil, "dialog", false},
		"ended session's state":    {"decide", quotedTab, []sessionstate.State{hookState("s1", sessionstate.Ended, "quotes", "p3")}, live, "dialog", false},
		"waiting with no dialog":   {"decide", plainTab, []sessionstate.State{hookState("s1", sessionstate.Waiting, "quotes", "p3")}, live, "dialog", true},
		"waiting, observe":         {"observe", plainTab, []sessionstate.State{hookState("s1", sessionstate.Waiting, "quotes", "p3")}, live, "idle", true},
		"waiting, tool approved":   {"decide", runningTab, []sessionstate.State{hookState("s1", sessionstate.Waiting, "quotes", "p3")}, live, "", false},
		"agree: no log":            {"observe", plainTab, []sessionstate.State{hookState("s1", sessionstate.Idle, "quotes", "p3")}, live, "idle", false},
		"another tab's state":      {"decide", quotedTab, []sessionstate.State{{SessionID: "s1", State: sessionstate.Running, Surface: "other"}}, live, "dialog", false},
	} {
		home := setup(t, map[string]string{"quotes.log": stamp(t0) + " INFO quotes:p3 -> quotes:lead — on it\n"})
		if c.mode != "" {
			setMode(t, home, c.mode)
		}
		f := &fake{states: c.states, ses: c.ses}
		runAt(t, home, f, c.draw, 0, 5, 11)
		got := ""
		for _, m := range f.sent {
			switch {
			case strings.Contains(m, "waits on a question or dialog"):
				got += "dialog"
			case strings.Contains(m, "is idle"):
				got += "idle"
			default:
				t.Errorf("%s: other alert %q", name, m)
			}
		}
		if got != c.want {
			t.Errorf("%s: want %q, got %q", name, c.want, f.sent)
		}
		if l := stateLog(t, home); (l != "") != c.logged || c.logged && !strings.Contains(l, "screen=") {
			t.Errorf("%s: state log %q", name, l)
		}
	}
}

// A child claude run inside a tab (codex-ask's, qa1's quotes-demo-3d 10-22 15:36) shares the tab's surface id and
// writes its own state file. Its newer "running" must not hide the parent's open dialog (review 10-22).
func TestAChildSessionInTheTabDoesNotHideItsParentsDialog(t *testing.T) {
	quoted := "Do you want to proceed?\n ❯ 1. Yes\n   2. No, and tell Claude what to do differently (esc)"
	draw := func(at time.Time) []Screen {
		return []Screen{withID(screen("quotes · p3", tab("quotes: p3", quoted, "  ⏵⏵ bypass permissions on\n", at)))}
	}
	live := []Session{{Name: "quotes: p3", ID: "s1", Started: t0.Add(-time.Hour)}, {Name: "child", ID: "s2", Started: t0.Add(-time.Minute)}}
	parent := hookState("s1", sessionstate.Waiting, "quotes", "p3")
	child := hookState("s2", sessionstate.Running, "appxite", "unknown")
	child.Time = parent.Time.Add(time.Second)
	home := setup(t, map[string]string{"quotes.log": stamp(t0) + " INFO quotes:p3 -> quotes:lead — on it\n"})
	setMode(t, home, "decide")
	f := &fake{states: []sessionstate.State{child, parent}, ses: live}
	runAt(t, home, f, draw, 0, 5, 11)
	if len(f.sent) != 1 || !strings.Contains(f.sent[0], "waits on a question or dialog") {
		t.Errorf("want one dialog alert, got %q (state log %q)", f.sent, stateLog(t, home))
	}
}

// Case 10-22 13:23 (Crius ORDER): "quotes:lead DONE, no fresh start in 23 min" while the lead was running, landing
// train after train. A lead whose hook state says it is working gets no fresh-start alert.
func TestABusyLeadByItsHookStateNeedsNoFreshStart(t *testing.T) {
	old := []Session{{Name: "quotes: lead", ID: "1", Started: t0.Add(-2 * time.Hour)}, {Name: "quotes-demo-3d", ID: "2", Started: t0.Add(-time.Hour)}}
	for name, c := range map[string]struct {
		mode   string
		states []sessionstate.State
		want   int
		logged bool
	}{
		"running, decide":        {"decide", []sessionstate.State{hookState("1", sessionstate.Running, "quotes", "lead")}, 0, true},
		"waiting, decide":        {"decide", []sessionstate.State{hookState("1", sessionstate.Waiting, "quotes", "lead")}, 0, true},
		"idle, decide":           {"decide", []sessionstate.State{hookState("1", sessionstate.Idle, "quotes", "lead")}, 2, false},
		"running, observe":       {"observe", []sessionstate.State{hookState("1", sessionstate.Running, "quotes", "lead")}, 2, true},
		"another role running":   {"decide", []sessionstate.State{hookState("2", sessionstate.Running, "quotes", "p3")}, 2, false},
		"a dead session running": {"decide", []sessionstate.State{hookState("9", sessionstate.Running, "quotes", "lead")}, 2, false},
		"no state":               {"decide", nil, 2, false},
		// review 10-22: the lead's own session by its id, not any session that names the lead's project and role
		"the lead's, no role": {"decide", []sessionstate.State{hookState("1", sessionstate.Running, "quotes", "")}, 0, true},
		"a child with its role": {"decide", []sessionstate.State{hookState("1", sessionstate.Idle, "quotes", "lead"),
			hookState("2", sessionstate.Running, "quotes", "lead")}, 2, false},
	} {
		home := setup(t, map[string]string{"quotes.log": done(t0)})
		setMode(t, home, c.mode)
		f := &fake{ses: old, states: c.states}
		runAt(t, home, f, func(time.Time) []Screen { return nil }, 10, 21, 30, 41)
		if got := freshSent(f); len(got) != c.want {
			t.Errorf("%s: want %d, got %q", name, c.want, got)
		}
		if l := stateLog(t, home); (l != "") != c.logged || c.logged && !strings.Contains(l, "quotes:lead") {
			t.Errorf("%s: state log %q", name, l)
		}
	}
}

// crius-hook logs its own errors to ~/.crius/logs/crius-hook.log and exits 0, so a session is never blocked: the log
// growing is how anyone hears of them. Each growth alerts once, with its last line.
func TestTheHookLogGrowingAlertsOnce(t *testing.T) {
	home := setup(t, nil)
	f := &fake{}
	p := filepath.Join(home, "logs", "crius-hook.log")
	Run(f.deps(home, t0))
	testutil.Write(t, p, "2026-10-22T15:00:00Z crius-hook: Stop s1: open sessions/.lock: permission denied\n")
	Run(f.deps(home, t0.Add(time.Minute)))
	Run(f.deps(home, t0.Add(2*time.Minute)))
	if len(f.sent) != 1 || !strings.Contains(f.sent[0], "crius-hook") || !strings.Contains(f.sent[0], "permission denied") {
		t.Fatalf("%q", f.sent)
	}
	b, _ := os.ReadFile(p)
	testutil.Write(t, p, string(b)+"2026-10-22T15:03:00Z crius-hook: unreadable hook input: EOF\n")
	Run(f.deps(home, t0.Add(3*time.Minute)))
	if len(f.sent) != 2 || !strings.Contains(f.sent[1], "unreadable hook input") {
		t.Fatalf("%q", f.sent)
	}
}

// A mode file that says neither, a state read that fails, or a decide mode with no state folder at all (the hook is
// not installed) is loud; the screen decides. In observe mode a missing folder is expected (before the install).
func TestABadModeOrAFailedStateReadIsLoud(t *testing.T) {
	for name, c := range map[string]struct {
		mode string
		err  error
		want string
	}{
		"unknown mode":       {"decides", nil, "wakewatch-mode"},
		"read fails":         {"observe", errors.New("open sessions/x.json: damaged session file"), "damaged"},
		"no folder, decide":  {"decide", fmt.Errorf("open sessions: %w", os.ErrNotExist), "does not exist"},
		"no folder, observe": {"observe", fmt.Errorf("open sessions: %w", os.ErrNotExist), ""},
	} {
		home := setup(t, nil)
		setMode(t, home, c.mode)
		f := &fake{stErr: c.err}
		Run(f.deps(home, t0))
		if c.want == "" && len(f.sent) != 0 || c.want != "" && (len(f.sent) != 1 || !strings.Contains(f.sent[0], c.want)) {
			t.Errorf("%s: %q", name, f.sent)
		}
	}
}

// OpenRig parts item 4: Send stores each message before it types it, so every alert of a run goes to Send even after
// one failed (a draft holds them all); each is stored and typed once the box is empty, and still goes on the bus.
func TestEveryAlertGoesToSendAfterAFailure(t *testing.T) {
	second := stamp(t0) + " DONE quotes:p3 -> crius:lead — F-275 · 9a1b2c3 · suite 88 OK\n"
	home := setup(t, map[string]string{"quotes.log": done(t0) + second})
	f := &fake{sendErr: errors.New("Crius's prompt holds a draft (\"owner\"): held")}
	if err := Run(f.deps(home, t0.Add(21*time.Minute))); err == nil {
		t.Fatal("no error")
	}
	if len(f.tried) != 2 {
		t.Fatalf("Send got %d alerts, want both: %q", len(f.tried), f.tried)
	}
}

// Crius 10-22 20:40: P4 was parked on purpose, and Quotes pauses whole at 70% of the weekly quota. A role or project
// named in ~/.crius/parked fires no idle or question wake; the roles not named still do, and a removed name wakes again.
func TestAParkedRoleOrPausedProjectFiresNoWake(t *testing.T) {
	bus := busFrom("quotes:lead", t0.Add(-time.Hour)) + strings.Replace(busFrom("quotes:lead", t0.Add(-time.Hour)), "quotes:lead", "axi:lead", 1)
	draw := func(at time.Time) []Screen {
		return []Screen{{Surface: "surface:1", Title: "quotes · p4", Text: tab("quotes · p4", "Parked until F-90 lands.", "", at)},
			{Surface: "surface:2", Title: "quotes · p5", Text: tab("quotes · p5", "Waiting for QA2.", "", at)},
			{Surface: "surface:3", Title: "quotes · qa1", Text: question},
			{Surface: "surface:4", Title: "axi · p1", Text: tab("axi · p1", "Waiting for review.", "", at)}}
	}
	for _, c := range []struct {
		parked string
		want   []string // the tabs that wake Crius
	}{
		{"", []string{"quotes:p4", "quotes:p5", "quotes · qa1", "axi:p1"}},
		{"quotes:p4\n", []string{"quotes:p5", "quotes · qa1", "axi:p1"}},
		{"# Crius 20:40\nQuotes:P4, quotes:qa1", []string{"quotes:p5", "axi:p1"}},
		{"quotes # 70% weekly PAUSE", []string{"axi:p1"}},
	} {
		home := setup(t, map[string]string{"quotes.log": bus, "axi.log": bus})
		if c.parked != "" {
			testutil.Write(t, filepath.Join(home, "parked"), c.parked)
		}
		f := &fake{}
		runAt(t, home, f, draw, 0, 11)
		var got []string
		for _, who := range []string{"quotes:p4", "quotes:p5", "quotes · qa1", "axi:p1"} {
			for _, m := range f.sent {
				if strings.Contains(m, who) {
					got = append(got, who)
					break
				}
			}
		}
		if fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("parked %q: woke for %q, want %q (sent %q)", c.parked, got, c.want, f.sent)
		}
	}
	// Crius resumes P4 by removing its name: P4's idle spell starts afresh and wakes after 10 min.
	home := setup(t, map[string]string{"quotes.log": bus, "axi.log": bus})
	testutil.Write(t, filepath.Join(home, "parked"), "quotes:p4")
	f := &fake{}
	runAt(t, home, f, draw, 0, 11)
	testutil.Write(t, filepath.Join(home, "parked"), "")
	runAt(t, home, f, draw, 12)
	if strings.Contains(strings.Join(f.sent, "\n"), "quotes:p4") {
		t.Fatal("P4 woke at once on resume:", f.sent)
	}
	runAt(t, home, f, draw, 23)
	if !strings.Contains(strings.Join(f.sent, "\n"), "quotes:p4") {
		t.Fatal("P4 resumed and idle 11 min, no wake:", f.sent)
	}
}

// Crius 10-23 00:26 (and ORDER 10-22 01:49, cortex:lead parked by an ORDER): a role whose newest bus line, from it or
// to it by name, parks it fires no idle wake until a newer line from it or to it. A line that only mentions parking
// ("then park" after work, another role parked) does not park it: a role told to work first still wakes when idle.
func TestARoleParkedOnTheBusFiresNoIdleWake(t *testing.T) {
	before := func(min int) string { return stamp(t0.Add(time.Duration(min-60) * time.Minute)) }
	for _, c := range []struct {
		lines []string // after the stamp, oldest first
		wake  bool
	}{
		{[]string{"ANSWER quotes:lead -> quotes:p5 — thanks, QA judges it next."}, true},
		{[]string{"ANSWER quotes:lead -> quotes:p5 — thanks. Park now, waiter armed."}, false},
		{[]string{"ANSWER quotes:lead -> quotes:p5 — thanks; stay parked, waiter armed."}, false},
		{[]string{"ORDER crius:lead -> quotes:p5 — parked: no work until the ticket is done. Keep STATUS current."}, false},
		{[]string{"ANSWER quotes:lead -> quotes:p5, quotes:p6 — QA judges it now. Park."}, false},
		{[]string{"ANSWER quotes:lead -> quotes:p5 — nothing for you. Park now (one STATUS line). A FAIL unparks you."}, false},
		{[]string{"ANSWER quotes:lead -> quotes:p5 — QA judges it next. Stay parked for now."}, false},
		{[]string{"INFO quotes:lead -> quotes:p5 — parked (Crius ORDER, quota): your brief is done."}, false},
		{[]string{"ANSWER quotes:lead -> quotes:p5 — Parked pending QA."}, false},
		{[]string{"ORDER quotes:lead -> quotes:p4, quotes:p5 — P4: parked; P5: fix item 4."}, true}, // review (codex)
		{[]string{"ORDER quotes:lead -> quotes:p5, quotes:p6 — P6: fix item 4; P5: stay parked."}, false},
		{[]string{"ANSWER quotes:lead -> quotes:p5 — Park now.", "INFO P5 -> quotes:lead — starting item 5"}, true},
		{[]string{"ANSWER quotes:lead -> quotes:p5 — Park now.", "ORDER quotes:lead -> quotes:p4 (cc quotes:p5) — unpark; fix item 4."}, true},
		{[]string{"INFO P5 -> crius — item 2 is READY. Parked until QA judges it."}, false},
		{[]string{"PARKED quotes:lead -> quotes:p5 — until item 3 lands"}, false},
		{[]string{"PAUSE crius:lead -> quotes:p5 — weekly quota"}, false},
		{[]string{"INFO quotes:p5 -> quotes:lead — item 2 is READY. Parked until QA judges it."}, false},
		{[]string{"ANSWER quotes:lead -> quotes:p5 — fix item 4, one READY, then park."}, true},
		{[]string{"ANSWER quotes:lead -> quotes:p5 — OK to edit that file (P2 is parked)."}, true},
		{[]string{"ORDER crius:lead -> quotes:p5 — you are parked, but no READY is on the bus: post it."}, true},
		{[]string{"ORDER quotes:lead -> quotes:p5 — done. Park it in refs/wip/p5, then fix item 2."}, true},
		{[]string{"ANSWER quotes:lead -> quotes:p6 — thanks. Park now."}, true},
		{[]string{"PAUSE crius:lead -> quotes:all — 70% weekly"}, true}, // a whole project is the parked file's job
		{[]string{"ANSWER quotes:lead -> quotes:p5 — Park now.", "INFO quotes:p5 -> quotes:lead — starting item 5"}, true},
		{[]string{"ANSWER quotes:lead -> quotes:p5 — Park now.", "ORDER quotes:lead -> quotes:p5 — unpark for the FAIL on item 3."}, true},
		{[]string{"ANSWER quotes:lead -> quotes:p5 — Park now.", "INFO quotes:lead -> quotes:all — queue depth 1"}, false},
	} {
		bus := busFrom("quotes:lead", t0.Add(-2*time.Hour))
		for i, l := range c.lines {
			bus += before(i) + " " + l + "\n"
		}
		home := setup(t, map[string]string{"quotes.log": bus})
		f := &fake{}
		draw := func(at time.Time) []Screen {
			return []Screen{screen("quotes · p5", tab("quotes · p5", "Waiting.", "", at))}
		}
		runAt(t, home, f, draw, 0, 11)
		if woke := strings.Contains(strings.Join(f.sent, "\n"), "quotes:p5"); woke != c.wake {
			t.Errorf("%q: woke %v, want %v (sent %q)", c.lines, woke, c.wake, f.sent)
		}
	}
}

// Review (codex) of the test above: a park goes down, from crius:lead to a project's lead to its other roles, or up
// from the role it parks. The lead that parks a worker, or hears a worker say it is parked, still wakes when idle.
func TestAParkParksOnlyTheRoleBelowOrTheSenderReportingUp(t *testing.T) {
	before := func(min int) string { return stamp(t0.Add(time.Duration(min-60) * time.Minute)) }
	for _, c := range []struct {
		line string
		wake bool
	}{
		{"ANSWER quotes:lead -> quotes:p5 — thanks. Park now.", true},
		{"INFO quotes:p5 -> quotes:lead — item 2 is READY. Parked until QA judges it.", true},
		{"INFO crius:lead -> quotes:lead — ack p4 idle 30 min: parked (lead 20:40), no action.", true},
		{"ORDER quotes:p5 -> quotes:lead — Park now.", true},
		{"ANSWER quotes:lead -> quotes:p5, crius:lead — thanks. Park now.", true}, // review (codex) round 3
		{"ORDER crius:lead -> quotes:lead — parked: no work until the DevOps ticket is done.", false},
	} {
		home := setup(t, map[string]string{"quotes.log": before(0) + " " + c.line + "\n"})
		f := &fake{}
		draw := func(at time.Time) []Screen {
			return []Screen{screen("quotes · lead", tab("quotes · lead", "Waiting.", "", at))}
		}
		runAt(t, home, f, draw, 0, 11)
		if woke := strings.Contains(strings.Join(f.sent, "\n"), "quotes:lead (tab"); woke != c.wake {
			t.Errorf("%q: lead woke %v, want %v (sent %q)", c.line, woke, c.wake, f.sent)
		}
	}
}

// Review (codex): a park and an unpark that both fall between two runs left the screen's idle spell as it was, so the
// run after the unpark woke the role at once. The spell starts afresh at the line that unparked it.
func TestARoleUnparkedBetweenRunsGetsAFreshIdleSpell(t *testing.T) {
	home := setup(t, map[string]string{"quotes.log": busFrom("quotes:lead", t0.Add(-time.Hour))})
	f := &fake{}
	draw := func(at time.Time) []Screen {
		return []Screen{screen("quotes · p5", tab("quotes · p5", "Waiting.", "", at))}
	}
	runAt(t, home, f, draw, 0, 5)
	bus := filepath.Join(home, "bus", "quotes.log")
	b, err := os.ReadFile(bus)
	if err != nil {
		t.Fatal(err)
	}
	testutil.Write(t, bus, string(b)+stamp(t0.Add(6*time.Minute))+" ANSWER quotes:lead -> quotes:p5 — thanks. Park now.\n"+
		stamp(t0.Add(9*time.Minute))+" ORDER quotes:lead -> quotes:p5 — unpark for the FAIL on item 3.\n")
	runAt(t, home, f, draw, 10)
	if len(f.sent) != 0 {
		t.Fatal("woke 1 min after the unpark:", f.sent)
	}
	runAt(t, home, f, draw, 19)
	if !strings.Contains(strings.Join(f.sent, "\n"), "quotes:p5") {
		t.Fatal("no wake 10 min after the unpark:", f.sent)
	}
}

// A parked list that cannot be read parks nothing, loudly: silence there would hide a stuck team.
func TestAnUnreadableParkedListIsAnAlert(t *testing.T) {
	home := setup(t, map[string]string{"quotes.log": busFrom("quotes:lead", t0.Add(-time.Hour))})
	if err := os.Mkdir(filepath.Join(home, "parked"), 0o700); err != nil {
		t.Fatal(err)
	}
	f := &fake{}
	draw := func(at time.Time) []Screen {
		return []Screen{screen("quotes · p4", tab("quotes · p4", "Parked.", "", at))}
	}
	runAt(t, home, f, draw, 0, 11)
	all := strings.Join(f.sent, "\n")
	if !strings.Contains(all, "parked list") || !strings.Contains(all, "quotes:p4") {
		t.Fatal(f.sent)
	}
}

// A lead's tab may be titled with the project alone ("✳ quotes"); with a dialog up the screen names no one, so the
// title says it is the lead (review round 13).
func TestAParkedLeadTitledWithItsProjectFiresNoWake(t *testing.T) {
	bus := busFrom("quotes:lead", t0.Add(-time.Hour))
	draw := func(time.Time) []Screen {
		return []Screen{{Surface: "surface:1", Title: "✳ quotes", Text: question},
			{Surface: "surface:2", Title: "quotes · qa1", Text: question}}
	}
	for _, c := range []struct {
		parked string
		want   []string
	}{
		{"quotes:p4", []string{"✳ quotes", "quotes · qa1"}},
		{"quotes:lead", []string{"quotes · qa1"}},
	} {
		home := setup(t, map[string]string{"quotes.log": bus})
		testutil.Write(t, filepath.Join(home, "parked"), c.parked)
		f := &fake{}
		runAt(t, home, f, draw, 0, 11)
		var got []string
		for _, title := range []string{"✳ quotes", "quotes · qa1"} {
			for _, m := range f.sent {
				if strings.Contains(m, fmt.Sprintf("%q", title)) || strings.Contains(m, title) {
					got = append(got, title)
					break
				}
			}
		}
		if fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("parked %q: woke for %q, want %q (sent %q)", c.parked, got, c.want, f.sent)
		}
	}
}

// OF6 (O3 re-review 10-22): a run that could not read the screens, the sessions or one tab dropped the open
// disagreement, so the next run logged it again as new; and a disagreeing tab that went away got no closing line, so
// crius-rec statelog judged it open until the tab's next line. A run that could not look keeps what it last saw; a
// run that looked and found the pair gone closes it with one "gone" line.
func TestTheStateLogKeepsWhatItCouldNotSeeAndClosesWhatWent(t *testing.T) {
	home := setup(t, map[string]string{"quotes.log": busFrom("quotes:p3", t0)})
	setMode(t, home, "observe")
	quoted := "the fixture:\n Do you want to proceed?\n ❯ 1. Yes\n   2. No, and tell Claude what to do differently (esc)"
	f := &fake{states: []sessionstate.State{hookState("s1", sessionstate.Idle, "quotes", "p3")},
		ses: []Session{{Name: "quotes: p3", ID: "s1", Started: t0.Add(-time.Hour)}}}
	draw := func(at time.Time) []Screen {
		return []Screen{withID(screen("quotes · p3", tab("quotes: p3", quoted, "  ⏵⏵ bypass permissions on\n", at)))}
	}
	lines := func() int { return strings.Count(stateLog(t, home), "\n") }
	runAt(t, home, f, draw, 0)
	if lines() != 1 {
		t.Fatalf("want one line for the disagreement:\n%s", stateLog(t, home))
	}
	blind := map[string]func(){
		"screens":  func() { f.scrErr = errors.New("cmux: no answer") },
		"sessions": func() { f.sesErr = errors.New("sessions: no answer") },
		"states":   func() { f.stErr = errors.New("sessions/x.json: damaged") },
		"one tab": func() {
			draw = func(at time.Time) []Screen {
				s := withID(screen("quotes · p3", ""))
				s.Err = "cmux read-screen: no answer"
				return []Screen{s}
			}
		},
	}
	m := 0
	for _, name := range []string{"screens", "sessions", "states", "one tab"} {
		old := draw
		blind[name]()
		m += 5
		runAt(t, home, f, draw, m)
		f.scrErr, f.sesErr, f.stErr, draw = nil, nil, nil, old
		m += 5
		runAt(t, home, f, draw, m)
		if lines() != 1 {
			t.Fatalf("after a run blind to %s, the open disagreement was logged again:\n%s", name, stateLog(t, home))
		}
	}
	gone := func(at time.Time) []Screen {
		return []Screen{screen("quotes · p4", tab("quotes: p4", "ok", "  ⏵⏵ bypass permissions on\n", at))}
	}
	runAt(t, home, f, gone, m+5, m+10)
	if log := stateLog(t, home); lines() != 2 || !strings.Contains(log, `tab:surface:7 "" screen=gone state=gone decides=screen`) {
		t.Fatalf("want one gone line for the tab that went:\n%s", log)
	}
}

// OF6 review 2: a tab that could not be read and whose title names a team not watched (a shell's "zsh") was skipped
// before its pair was kept, so the run closed with a "gone" line a disagreement it never saw.
func TestAnUnreadTabUnderAnotherTitleKeepsItsPair(t *testing.T) {
	home := setup(t, map[string]string{"quotes.log": busFrom("quotes:p3", t0)})
	testutil.Write(t, filepath.Join(home, "wakewatch-projects"), "quotes\n")
	setMode(t, home, "observe")
	quoted := "the fixture:\n Do you want to proceed?\n ❯ 1. Yes\n   2. No, and tell Claude what to do differently (esc)"
	f := &fake{states: []sessionstate.State{hookState("s1", sessionstate.Idle, "quotes", "p3")},
		ses: []Session{{Name: "quotes: p3", ID: "s1", Started: t0.Add(-time.Hour)}}}
	draw := func(at time.Time) []Screen {
		return []Screen{withID(screen("quotes · p3", tab("quotes: p3", quoted, "  ⏵⏵ bypass permissions on\n", at)))}
	}
	failed := func(time.Time) []Screen {
		s := withID(screen("zsh", ""))
		s.Err = "cmux read-screen: no answer"
		return []Screen{s}
	}
	runAt(t, home, f, draw, 0)
	runAt(t, home, f, failed, 5)
	runAt(t, home, f, draw, 10)
	if log := stateLog(t, home); strings.Count(log, "\n") != 1 {
		t.Fatalf("want the one line for the disagreement:\n%s", log)
	}
}

// OF6 review 2: a run that could not read the screens still judged the lead's fresh start from no screen text, so the
// pair flipped and was logged, then flipped back on the next run. A blind run logs nothing and keeps every pair.
func TestARunBlindToTheScreensKeepsTheFreshPair(t *testing.T) {
	home := setup(t, map[string]string{"quotes.log": done(t0)})
	setMode(t, home, "observe")
	f := &fake{ses: []Session{{Name: "quotes: lead", ID: "1", Started: t0.Add(-2 * time.Hour)}},
		states: []sessionstate.State{hookState("1", sessionstate.Idle, "quotes", "lead")}}
	asks := func(at time.Time) []Screen {
		return []Screen{withID(screen("quotes · lead", tab("quotes: lead", "Which branch should the fix go to, main or develop?\n\n❓",
			"  ⏵⏵ bypass permissions on\n", at)))}
	}
	runAt(t, home, f, asks, 21)
	if log := stateLog(t, home); strings.Count(log, "\n") != 1 || !strings.Contains(log, "fresh:quotes:lead") {
		t.Fatalf("want one fresh line:\n%s", log)
	}
	f.scrErr = errors.New("cmux: no answer")
	runAt(t, home, f, func(time.Time) []Screen { return nil }, 26)
	f.scrErr = nil
	runAt(t, home, f, asks, 31)
	if log := stateLog(t, home); strings.Count(log, "\n") != 1 {
		t.Fatalf("a run blind to the screens logged the fresh pair:\n%s", log)
	}
}

// OF6 review 3: one lead tab that could not be read, while cmux answered for the rest, still judged the fresh start
// from no text, so the pair flipped and was logged, then flipped back. The lead's pair stays as last seen.
func TestAnUnreadLeadTabKeepsTheFreshPair(t *testing.T) {
	home := setup(t, map[string]string{"quotes.log": done(t0)})
	setMode(t, home, "observe")
	f := &fake{ses: []Session{{Name: "quotes: lead", ID: "1", Started: t0.Add(-2 * time.Hour)}},
		states: []sessionstate.State{hookState("1", sessionstate.Idle, "quotes", "lead")}}
	asks := func(at time.Time) []Screen {
		return []Screen{withID(screen("quotes · lead", tab("quotes: lead", "Which branch should the fix go to, main or develop?\n\n❓",
			"  ⏵⏵ bypass permissions on\n", at)))}
	}
	runAt(t, home, f, asks, 21)
	runAt(t, home, f, func(at time.Time) []Screen {
		s := asks(at)
		s[0].Text, s[0].Err = "", "surface not readable"
		return s
	}, 26)
	runAt(t, home, f, asks, 31)
	if log := stateLog(t, home); strings.Count(log, "\n") != 1 {
		t.Fatalf("an unread lead tab changed the fresh pair:\n%s", log)
	}
}

// OF6 review 3: a run that could not write the state log still saved its pairs, so a disagreement that went while the
// log was unwritable never got its "gone" line and statelog judged it open for ever. The next run logs it.
func TestAGoneLineLostToAnUnwritableLogIsWrittenNextRun(t *testing.T) {
	home := setup(t, map[string]string{"quotes.log": done(t0)})
	setMode(t, home, "observe")
	f := &fake{ses: []Session{{Name: "quotes: lead", ID: "1", Started: t0.Add(-2 * time.Hour)}},
		states: []sessionstate.State{hookState("1", sessionstate.Idle, "quotes", "lead")}}
	asks := func(at time.Time) []Screen {
		return []Screen{withID(screen("quotes · lead", tab("quotes: lead", "Which branch should the fix go to, main or develop?\n\n❓",
			"  ⏵⏵ bypass permissions on\n", at)))}
	}
	runAt(t, home, f, asks, 21)
	p := filepath.Join(home, "logs", "wakewatch-state.log")
	if err := os.Rename(p, p+".kept"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(p, 0o700); err != nil { // a folder in its place: the append fails
		t.Fatal(err)
	}
	f.ses, f.states = nil, nil // the lead went: its open pair is gone
	runAt(t, home, f, func(time.Time) []Screen { return nil }, 26)
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(p+".kept", p); err != nil {
		t.Fatal(err)
	}
	runAt(t, home, f, func(time.Time) []Screen { return nil }, 31)
	if log := stateLog(t, home); strings.Count(log, "\n") != 2 || !strings.Contains(log, "gone") {
		t.Fatalf("want the fresh line and its gone line:\n%s", log)
	}
}

// OF6 review 2: the last line came from the last 64 KB, so a longer line lost its start: the time and the session.
func TestTheLastLineOfTheHookLogComesFromItsStart(t *testing.T) {
	_, home := testutil.Temp(t)
	p := filepath.Join(home, "crius-hook.log")
	head := "2026-10-22T15:03:00Z crius-hook: Stop s1: "
	testutil.Write(t, p, "first\n"+head+strings.Repeat("x", 70<<10)+"\n")
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	n, last, err := countLines(p, 0, fi.Size())
	if err != nil || n != 2 || !strings.HasPrefix(last, head) || len(last) > 64<<10 {
		t.Errorf("n=%d err=%v last starts %.40q (%d bytes), want 2 lines and the last from its start", n, err, last, len(last))
	}
}

// OF6: the first run after a start read all of crius-hook.log, however big, to give its last line. It reads the tail.
func TestABigHookLogIsNotReadWhole(t *testing.T) {
	home := setup(t, nil)
	line := "2026-10-22T15:00:00Z crius-hook: Stop s1: open sessions/.lock: permission denied\n"
	testutil.Write(t, filepath.Join(home, "logs", "crius-hook.log"), strings.Repeat(line, 100000)+"2026-10-22T15:03:00Z crius-hook: the last\n")
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	out := hookLog(home, &state{})
	runtime.ReadMemStats(&after)
	if len(out) != 1 || !strings.Contains(out[0][1], "100001 new error line(s)") || !strings.Contains(out[0][1], "the last") {
		t.Errorf("%q", out)
	}
	if n := after.TotalAlloc - before.TotalAlloc; n > 1<<20 {
		t.Errorf("read %d bytes for an %d-byte log, want its tail", n, 100001*len(line))
	}
}

// OF6: wakewatch-state.log grew without end. Past stateLogMax it moves to .1, and crius-rec statelog reads both.
func TestTheStateLogRotates(t *testing.T) {
	home := setup(t, nil)
	p := filepath.Join(home, "logs", "wakewatch-state.log")
	testutil.Write(t, p, strings.Repeat("x", stateLogMax)+"\n")
	if err := appendLines(p, []string{"new"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "new\n" {
		t.Errorf("state log holds %d bytes, want the new line alone", len(b))
	}
	if fi, err := os.Stat(p + ".1"); err != nil || fi.Size() != int64(stateLogMax+1) {
		t.Errorf("%s.1: %v, want the old log", p, err)
	}
}

// 10-25 morning: the same disk alarm woke Crius every few minutes. It wakes on crossing the line, then again only after
// a further 2 GB drop or 2 h; disk back over the line by less than 2 GB is no new crossing.
func TestTheDiskAlarmWakesOnCrossingThenOnA2GBDropOrAfter2h(t *testing.T) {
	home := setup(t, nil)
	f := &fake{}
	runAt := func(min int, free float64) int {
		t.Helper()
		f.freeGB, f.sent = free, nil
		if err := Run(f.deps(home, t0.Add(time.Duration(min)*time.Minute))); err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, s := range f.sent {
			if strings.Contains(s, "disk:") {
				n++
			}
		}
		return n
	}
	for _, c := range []struct {
		min  int
		free float64
		want int
		why  string
	}{
		{0, 19.4, 1, "crossing"},
		{5, 19.0, 0, "0.4 GB lower"},
		{25, 18.0, 0, "20 min on, 1.4 GB lower: the old 20 min resend"},
		{30, 20.5, 0, "back over by 0.5 GB"},
		{35, 19.7, 0, "under again: no new crossing"},
		{40, 17.3, 1, "2.1 GB below the last alarm"},
		{100, 16.0, 0, "1.3 GB lower"},
		{160, 16.0, 1, "2 h after the last alarm"},
		{165, 23.0, 0, "back over by 3 GB"},
		{170, 19.9, 1, "a new crossing"},
		{175, 17.5, 1, "2.4 GB lower 5 min on: the 20 min resend must not hold it (review of c7daa87)"},
	} {
		if got := runAt(c.min, c.free); got != c.want {
			t.Errorf("min %d, %.1f GB (%s): %d disk alarms, want %d", c.min, c.free, c.why, got, c.want)
		}
	}
}

// Crius 10-29 00:39: the same quotes.log 23:54 DONE was typed into Crius's tab 3 times, the last after a cmux restart:
// an open bus line was typed again every 20 min until crius:lead answered on that same log. A bus line typed once is
// never typed again, across runs (a restart is a new run on the same state file), and also when it drops out for a run
// (its key went with it) and comes back.
func TestABusLineTypedOnceIsNeverTypedAgain(t *testing.T) {
	home := setup(t, map[string]string{"quotes.log": done(t0)})
	f := &fake{}
	for _, m := range []time.Duration{21, 41, 61, 120} {
		if err := Run(f.deps(home, t0.Add(m*time.Minute))); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.sent) != 1 {
		t.Fatalf("typed %d times, want once: %q", len(f.sent), f.sent)
	}
	testutil.Write(t, filepath.Join(home, "bus", "quotes.log"), "")
	if err := Run(f.deps(home, t0.Add(130*time.Minute))); err != nil {
		t.Fatal(err)
	}
	testutil.Write(t, filepath.Join(home, "bus", "quotes.log"), done(t0))
	if err := Run(f.deps(home, t0.Add(150*time.Minute))); err != nil {
		t.Fatal(err)
	}
	if len(f.sent) != 1 {
		t.Fatalf("typed again after it dropped out for a run: %q", f.sent)
	}
	// A new line is typed.
	testutil.Write(t, filepath.Join(home, "bus", "quotes.log"), done(t0)+done(t0.Add(2*time.Hour)))
	if err := Run(f.deps(home, t0.Add(150*time.Minute))); err != nil {
		t.Fatal(err)
	}
	if len(f.sent) != 2 {
		t.Fatalf("a new line: %q", f.sent)
	}
}

// Review of the above: a send the outbox holds (or set aside after its Enter may have gone) hands the line over; the
// outbox types it, so the 20 min resend must not.
func TestABusLineTheOutboxHoldsIsNotTypedAgain(t *testing.T) {
	home := setup(t, map[string]string{"quotes.log": done(t0)})
	held := errors.New("held")
	f := &fake{sendErr: fmt.Errorf("box not empty: %w", held)}
	for _, m := range []time.Duration{21, 41, 61} {
		d := f.deps(home, t0.Add(m*time.Minute))
		d.HandedOver = func(err error) bool { return errors.Is(err, held) }
		if err := Run(d); err != nil && !errors.Is(err, held) {
			t.Fatal(err)
		}
		f.sendErr = nil
	}
	if len(f.tried) != 1 {
		t.Fatalf("given to Send %d times, want once: %q", len(f.tried), f.tried)
	}
}

// Item 8 (Crius 10-29 00:28): cmux restored sessions with no CLAUDE_CONFIG_DIR, so they ran on the default folder,
// another account, and nothing said so. A live session in another folder than its role's start filed wakes Crius once,
// naming the fix; one in its own folder (or a link to it), or no longer its role's session, does not.
func TestASessionRestoredOnTheDefaultFolderIsCaught(t *testing.T) {
	home := setup(t, nil)
	u := t.TempDir()
	max2, def, link := filepath.Join(u, ".claude-max2"), filepath.Join(u, ".claude"), filepath.Join(u, ".claude-personal")
	for _, d := range []string{max2, def} {
		os.MkdirAll(d, 0o700)
	}
	if err := os.Symlink(def, link); err != nil {
		t.Fatal(err)
	}
	file := func(filed string) {
		role := hookState("s1", sessionstate.Idle, "quotes", "p3")
		role.Config, role.Filed = def, filed
		if err := sessionstate.WriteRole(filepath.Join(home, "roles"), role, true); err != nil {
			t.Fatal(err)
		}
	}
	file(max2)
	live := []Session{{Name: "quotes: p3", ID: "s1", Started: t0.Add(-time.Hour)}, {Name: "quotes: p4", ID: "s2", Started: t0}}
	running := hookState("s1", sessionstate.Idle, "quotes", "p3")
	running.Config = def
	other := hookState("s2", sessionstate.Idle, "quotes", "p3") // not the role's session: a child claude run in a tab
	other.Config = def
	f := &fake{ses: live, states: []sessionstate.State{running, other}}
	for _, m := range []time.Duration{0, 30, 60} {
		if err := Run(f.deps(home, t0.Add(m*time.Minute))); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.sent) != 1 || !strings.Contains(f.sent[0], "quotes · p3") || !strings.Contains(f.sent[0], "runs in "+def+",") ||
		!strings.Contains(f.sent[0], max2) || !strings.Contains(f.sent[0], "crius-spawn -resume quotes p3") {
		t.Fatalf("sent %q, want one wake naming quotes · p3, both folders and the fix", f.sent)
	}
	// A run that cannot read the role file did not see it go (review): no second wake once it reads again.
	testutil.Write(t, filepath.Join(home, "roles", "quotes.p3.json"), "{")
	if err := Run(f.deps(home, t0.Add(90*time.Minute))); err != nil {
		t.Fatal(err)
	}
	file(max2)
	if err := Run(f.deps(home, t0.Add(100*time.Minute))); err != nil {
		t.Fatal(err)
	}
	wakes := 0
	for _, m := range f.sent {
		if strings.Contains(m, " runs in ") {
			wakes++
		}
	}
	if wakes != 1 || !strings.Contains(f.sent[len(f.sent)-1], "could not read its role file") {
		t.Fatalf("sent %q, want the first wake and the unread role file only", f.sent)
	}
	f.sent = nil
	for _, filed := range []string{def, link + "/"} {
		file(filed)
		if err := Run(f.deps(home, t0.Add(2*time.Hour))); err != nil {
			t.Fatal(err)
		}
		if len(f.sent) != 0 {
			t.Fatalf("filed in %s, running in %s: sent %q", filed, def, f.sent)
		}
	}
	// Restored on the wrong folder again (review): it wakes Crius again.
	file(max2)
	if err := Run(f.deps(home, t0.Add(3*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if len(f.sent) != 1 {
		t.Fatalf("restored on the wrong folder again: sent %q, want one wake", f.sent)
	}
}

// Item A (crius:lead 10-29 05:56): before a restart, a live role session whose cmux tab would restore it in another
// folder than its role's start filed, or in none (claude then begins on ~/.claude, another account: 00:28), wakes Crius
// once with the tab and the fix. Its own folder (or a link to it), or a tab that keeps no Claude, does not; a record
// cmux cannot give says so once.
func TestATabThatWouldRestoreOnAnotherFolderIsCaught(t *testing.T) {
	home := setup(t, nil)
	u := t.TempDir()
	max2, def, link := filepath.Join(u, ".claude-max2"), filepath.Join(u, ".claude"), filepath.Join(u, ".claude-max2-link")
	for _, d := range []string{max2, def} {
		os.MkdirAll(d, 0o700)
	}
	if err := os.Symlink(max2, link); err != nil {
		t.Fatal(err)
	}
	role := hookState("s1", sessionstate.Idle, "quotes", "p3")
	role.Config, role.Filed, role.Workspace = max2, max2, "W1"
	if err := sessionstate.WriteRole(filepath.Join(home, "roles"), role, true); err != nil {
		t.Fatal(err)
	}
	f := &fake{ses: []Session{{Name: "quotes: p3", ID: "s1", Started: t0.Add(-time.Hour)}}, states: []sessionstate.State{role}}
	at := t0
	run := func(restore map[string][]string, err error) []string {
		f.sent, f.restore, f.rsErr = nil, restore, err
		at = at.Add(30 * time.Minute)
		if err := Run(f.deps(home, at)); err != nil {
			t.Fatal(err)
		}
		return f.sent
	}
	for _, c := range []struct {
		restore map[string][]string
		err     error
		want    []string // each a part of the one wake; nil: no wake
	}{
		{map[string][]string{sfID: {""}}, nil, []string{"quotes · p3", sfID, "~/.claude", max2, "crius-spawn -resume quotes p3"}},
		{map[string][]string{sfID: {""}}, nil, nil}, // once
		{map[string][]string{sfID: {def}}, nil, []string{"quotes · p3", def, max2, "crius-spawn -resume quotes p3"}},
		{map[string][]string{sfID: {max2}}, nil, nil},
		// cmux keeps a resume binding and a restore record and does not say which a restart uses (review of fae23e6).
		{map[string][]string{sfID: {max2, def}}, nil, []string{"quotes · p3", def, max2}},
		{map[string][]string{sfID: {link + "/", max2}}, nil, nil},
		{map[string][]string{}, nil, nil},
		{nil, errors.New("cmux: socket gone"), []string{"could not read", "socket gone"}},
		{map[string][]string{sfID: {def}}, nil, []string{"quotes · p3", def}}, // wrong again after a right run: again
	} {
		sent := run(c.restore, c.err)
		if c.want == nil {
			if len(sent) != 0 {
				t.Fatalf("restore %q, err %v: sent %q, want nothing", c.restore, c.err, sent)
			}
			continue
		}
		if len(sent) != 1 {
			t.Fatalf("restore %q, err %v: sent %q, want one wake", c.restore, c.err, sent)
		}
		for _, w := range c.want {
			if !strings.Contains(sent[0], w) {
				t.Errorf("restore %q, err %v: %q lacks %q", c.restore, c.err, sent[0], w)
			}
		}
	}
}

// Crius 10-30 13:22: a Cortex NEED sat ~20 min unseen, as no bus waiter of Crius's covered cortex.log (Falcon missed two
// lines the same way on 10-29 16:23). A bus log no waiter covers for 10 min wakes Crius once; a shorter gap (Crius
// re-arms after each wake) does not, and a gap after the waiter came back alerts again.
func TestABusLogWithNoWaiterFor10MinWakesCriusOnce(t *testing.T) {
	home := setup(t, map[string]string{"cortex.log": "", "falcon.log": "", "quotes.log": "", "waits.log": ""})
	f := &fake{}
	logs := func(names ...string) []string {
		out := []string{}
		for _, n := range names {
			out = append(out, filepath.Join(home, "bus", n+".log"))
		}
		return out
	}
	some, all := logs("crius", "quotes"), logs("crius", "quotes", "cortex", "falcon")
	for _, c := range []struct {
		min     int
		covered []string
		want    string // a part of the one wake; "": none
	}{
		{0, some, ""},
		{5, all, ""}, // a 5 min gap
		{10, some, ""},
		{15, some, ""},
		{20, some, "no bus waiter covers cortex, falcon for 10+ min"},
		{25, some, ""},
		{45, some, ""}, // said once, not every 20 min
		{50, logs("crius", "quotes", "falcon"), ""},
		{55, some, ""}, // falcon's new gap starts afresh; cortex was said
		{65, some, "no bus waiter covers falcon for 10+ min"},
		{70, all, ""},
		{75, some, ""},
		{85, some, "cortex, falcon"},
	} {
		f.sent, f.waiters = nil, c.covered
		if err := Run(f.deps(home, t0.Add(time.Duration(c.min)*time.Minute))); err != nil {
			t.Fatal(c.min, err)
		}
		var got []string
		for _, s := range f.sent {
			if strings.Contains(s, "waiter") {
				got = append(got, s)
			}
		}
		if c.want == "" && len(got) != 0 || c.want != "" && (len(got) != 1 || !strings.Contains(got[0], c.want)) {
			t.Fatalf("min %d: sent %q, want %q", c.min, got, c.want)
		}
	}
}

// Crius 10-30 23:19: a team whose every lead is parked (GOAL Next: PARKED …) needs no waiter, so its bus log with none
// stays quiet, until a NEED-OWNER line lands in it: that wakes Crius as any gap does. A team with a lead at work still
// needs one.
func TestAParkedTeamNeedsNoWaiterUntilItAsksTheOwner(t *testing.T) {
	home := setup(t, map[string]string{"axi.log": busFrom("axi:leader", t0), "falcon.log": ""})
	testutil.Write(t, filepath.Join(home, "projects.toml"), "[projects.axi]\nleads = { leader = \""+home+"/axi.md\" }\n"+
		"[projects.falcon]\nleads = { lead = \""+home+"/falcon.md\" }\n")
	goalWith := func(next string) string {
		return "## GOAL (2026-10-30 18:04)\nGoal: AXI orders.\n- [~] B101 · x\nDone: 17% (1 of 6 milestones)\nNext: " + next +
			"\nNeeds owner: nothing\n"
	}
	testutil.Write(t, filepath.Join(home, "axi.md"), goalWith("PARKED by owner (2026-10-30 22:02)."))
	testutil.Write(t, filepath.Join(home, "falcon.md"), goalWith("item 2"))
	f := &fake{waiters: []string{filepath.Join(home, "bus", "crius.log")}}
	runAt(t, home, f, func(time.Time) []Screen { return nil }, 0, 11)
	if len(f.sent) != 1 || !strings.Contains(f.sent[0], "no bus waiter covers falcon for") {
		t.Fatalf("sent %q, want one wake for falcon alone", f.sent)
	}
	f.sent = nil
	testutil.Write(t, filepath.Join(home, "bus", "axi.log"), busFrom("axi:leader", t0)+stamp(t0.Add(12*time.Minute))+
		" NEED-OWNER axi:leader -> crius:lead — owner: log in to the F3 look\n")
	runAt(t, home, f, func(time.Time) []Screen { return nil }, 13, 24)
	if len(f.sent) != 1 || !strings.Contains(f.sent[0], "no bus waiter covers axi for") {
		t.Fatalf("sent %q, want one wake for axi after its NEED-OWNER", f.sent)
	}
	// a NEED-OWNER in the gap's first minute counts: bus stamps are to the minute, the gap's start is not
	home = setup(t, map[string]string{"axi.log": ""})
	testutil.Write(t, filepath.Join(home, "projects.toml"), "[projects.axi]\nleads = { leader = \""+home+"/axi.md\" }\n")
	testutil.Write(t, filepath.Join(home, "axi.md"), goalWith("PARKED by owner (2026-10-30 22:02)."))
	f = &fake{waiters: []string{filepath.Join(home, "bus", "crius.log")}}
	if err := Run(f.deps(home, t0.Add(30*time.Second))); err != nil { // the gap starts at 22:00:30
		t.Fatal(err)
	}
	testutil.Write(t, filepath.Join(home, "bus", "axi.log"), stamp(t0)+" NEED-OWNER axi:leader -> crius:lead — owner: log in\n")
	if err := Run(f.deps(home, t0.Add(11*time.Minute+30*time.Second))); err != nil {
		t.Fatal(err)
	}
	if len(f.sent) != 1 || !strings.Contains(f.sent[0], "no bus waiter covers axi for") {
		t.Fatalf("sent %q, want the NEED-OWNER of the gap's first minute to wake Crius", f.sent)
	}
}

// Only the watched projects need a waiter, and a waiter check that cannot look is loud, never "all covered".
func TestTheWaiterCheckWatchesTheListedProjectsAndFailsLoud(t *testing.T) {
	home := setup(t, map[string]string{"cortex.log": "", "quotes.log": ""})
	testutil.Write(t, filepath.Join(home, "wakewatch-projects"), "quotes\n")
	f := &fake{waiters: []string{filepath.Join(home, "bus", "crius.log")}}
	runAt(t, home, f, func(time.Time) []Screen { return nil }, 0, 11)
	if len(f.sent) != 1 || !strings.Contains(f.sent[0], "no bus waiter covers quotes for") {
		t.Fatalf("sent %q, want one wake for quotes alone", f.sent)
	}
	f.sent, f.waitErr = nil, errors.New("ps: exit status 1")
	runAt(t, home, f, func(time.Time) []Screen { return nil }, 12)
	if len(f.sent) != 1 || !strings.Contains(f.sent[0], "the waiter check failed to look: ps: exit status 1") {
		t.Fatalf("sent %q, want the failed look", f.sent)
	}
}

// Falcon 10-30: its goal was done (Done: 100%) and it drew IDLE alerts again and again. A lead whose GOAL block says
// Done: 100% is finished, not idle; a lead with work left, or another role of that team, still alerts.
//
// Crius 10-30 23:19: axi:leader, parked by the owner at 22:02 (GOAL "Next: PARKED by owner …"), drew a second idle
// alert. A lead whose GOAL Next starts with PARKED has no work to be idle on either; another role of its team still
// alerts, and so does a Next that only names the word later.
func TestAnIdleLeadWhoseGoalIsDoneDoesNotAlert(t *testing.T) {
	parked := "PARKED by owner (2026-10-30 22:02). On resume read the RESUME block below, then item 1 (B101 qa)."
	for _, c := range []struct {
		done, who, next string
		idle            bool
	}{
		{"100", "falcon: lead", "nothing", false},
		{"80", "falcon: lead", "nothing", true},
		{"100", "falcon: qa", "nothing", true},
		{"17", "falcon: lead", parked, false},
		{"17", "falcon: qa", parked, true},
		{"17", "falcon: lead", "item 1, then PARKED until the owner logs in", true},
	} {
		home := setup(t, map[string]string{"falcon.log": busFrom("falcon:lead", t0) + busFrom("falcon:qa", t0)})
		status := filepath.Join(home, "falcon-STATUS.md")
		testutil.Write(t, filepath.Join(home, "projects.toml"), "[projects.falcon]\npersona = \"personal\"\ncwd = [\"/x\"]\n"+
			"leads = { lead = \""+status+"\" }\n\n[projects.crius]\nleads = { build = \"git:/nowhere#origin/main:STATUS.md\" }\n")
		testutil.Write(t, status, "# Falcon\n\n## GOAL (2026-10-30 09:00)\nGoal: Falcon is Crius's senses.\n"+
			"- [x] Sources flow · 9c644de\nDone: "+c.done+"% (1 of 1 milestones)\nNext: "+c.next+"\nNeeds owner: nothing\n")
		f := &fake{}
		draw := func(at time.Time) []Screen {
			return []Screen{screen("falcon", tab(c.who, "All milestones verified.", "  ⏵⏵ bypass permissions on\n", at))}
		}
		runAt(t, home, f, draw, 0, 5, 11)
		if idle := len(f.sent) == 1 && strings.Contains(f.sent[0], "is idle"); idle != c.idle || len(f.sent) > 1 {
			t.Errorf("%s, Done: %s%%, Next: %s: sent %q, want idle %v", c.who, c.done, c.next, f.sent, c.idle)
		}
	}
}

// Review of c898a43: crius-wait names the bus by its real path (pwd -P), the check by Home; reached through a link, every
// log looked uncovered and the check said so every 10 min.
func TestAWaiterOnTheRealPathOfTheBusCoversItsLogs(t *testing.T) {
	home := setup(t, map[string]string{"cortex.log": ""})
	real, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "home-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	f := &fake{waiters: []string{filepath.Join(real, "bus", "crius.log"), filepath.Join(real, "bus", "cortex.log")}}
	for _, min := range []int{0, 15} {
		f.sent = nil
		if err := Run(f.deps(link, t0.Add(time.Duration(min)*time.Minute))); err != nil {
			t.Fatal(err)
		}
		for _, s := range f.sent {
			if strings.Contains(s, "waiter") {
				t.Fatalf("min %d: a covered log was said uncovered: %q", min, s)
			}
		}
	}
}

// Crius 10-30 15:33: appxite-map:uploads and cortex:impact-tester drew an IDLE every 20 min for hours after a DONE that
// was VERIFIED, with nothing sent to them since (their real lines below). A role under a lead whose newest line is a
// DONE has handed its work back: its DONE woke Crius, and the idle alert says once that it waits for its next item. A
// line that gives it work (not a VERIFIED or ACK of the DONE) starts the usual idle alerts again; a lead idle after a
// DONE still alerts every 20 min (RULES 3.5).
func TestARoleIdleAfterItsDoneAlertsOnce(t *testing.T) {
	for _, c := range []struct {
		name, log, title, bus, toml string
		repeats, failRead           bool
	}{
		{"appxite-map:uploads (real lines)", "appxite-map.log", "appxite-map · uploads",
			stamp(t0.Add(-29*time.Hour)) + " INFO appxite-map:uploads -> appxite-map:lead — UPLOADS.md draft written (19 live flows, 26 items)\n" +
				stamp(t0.Add(-28*time.Hour)) + " DONE appxite-map:uploads -> appxite-map:lead — UPLOADS.md @28bb0fb (local main). 27 items\n" +
				stamp(t0.Add(-28*time.Hour)) + " VERIFIED crius:lead -> appxite-map:uploads — 28bb0fb · UPLOADS.md file and counts match\n", "", false, false},
		{"cortex:impact-tester (real lines)", "cortex.log", "cortex · impact-tester",
			stamp(t0.Add(-15*time.Hour)) + " VERIFIED crius:lead -> cortex:impact-tester — 57ca002 · PR #18 head: clean checkout 1087/0\n" +
				stamp(t0.Add(-4*time.Hour)) + " DONE cortex:impact-tester -> crius — #20 head f425134 (draft, mergeable). CI green 1116 pass 0 fail\n" +
				stamp(t0.Add(-1*time.Hour)) + " INFO crius:lead -> cortex:lead — Wakewatch flags cortex:impact-tester idle 255 min; give it the next item or tell it to stand down\n", "", false, false},
		{"work sent after the DONE", "cortex.log", "cortex · impact-tester",
			stamp(t0.Add(-4*time.Hour)) + " DONE cortex:impact-tester -> crius — #20 head f425134\n" +
				stamp(t0.Add(-3*time.Hour)) + " INFO cortex:lead -> cortex:impact-tester — next: deep review of #22 on 8730ead\n", "", true, false},
		{"a lead idle after its DONE", "cortex.log", "cortex · lead",
			stamp(t0.Add(-4*time.Hour)) + " DONE cortex:lead -> crius:lead — #22 merged\n", "", true, false},
		// Review of the fix, P1: a lead whose role is not named lead (axi = { leader }, crius = { build }) is a lead.
		{"axi:leader idle after its DONE", "axi.log", "axi · leader",
			stamp(t0.Add(-4*time.Hour)) + " DONE axi:leader -> crius:lead — M4 order flow on main 1a2b3c4\n" +
				stamp(t0.Add(-4*time.Hour)) + " VERIFIED crius:lead -> axi:leader — 1a2b3c4 · clean checkout 212/0\n",
			"[projects.axi]\npersona = \"work\"\nleads = { leader = \"/nowhere/STATUS.md\" }\n", true, false},
		// Review round 2, P2: with projects.toml unreadable no role is known to be a lead: none is handed back, and it is said.
		{"axi:leader with projects.toml unreadable", "axi.log", "axi · leader",
			stamp(t0.Add(-4*time.Hour)) + " DONE axi:leader -> crius:lead — M4 order flow on main 1a2b3c4\n" +
				stamp(t0.Add(-4*time.Hour)) + " VERIFIED crius:lead -> axi:leader — 1a2b3c4 · clean checkout 212/0\n",
			"[projects.axi\npersona = \"work\"\n", true, false},
		// Review of the fix, P1: Quotes gives work through quotes:all (961 lines), with short names (P3).
		{"work sent to the whole team", "quotes.log", "quotes · p3",
			stamp(t0.Add(-4*time.Hour)) + " DONE P3 -> quotes:lead — F-141 on stream/d\n" +
				stamp(t0.Add(-3*time.Hour)) + " ORDER quotes:lead -> quotes:all,crius:lead — next round. P3: take AC78\n", "", true, false},
		// Review of the fix, P2: a QA PASS (or the lead's LANDED) answers the DONE and gives no work.
		{"a QA PASS of the DONE", "quotes.log", "quotes · p3",
			stamp(t0.Add(-4*time.Hour)) + " DONE P3 -> quotes:lead — F-141 on stream/d\n" +
				stamp(t0.Add(-3*time.Hour)) + " PASS quotes:qa1 -> quotes:lead,quotes:p3 — F-141 J1-J4 pass\n", "", false, false},
		// Review of the fix, P2: one failed read of the tab does not make the once-said alert be said again.
		{"a failed read between runs", "appxite-map.log", "appxite-map · uploads",
			stamp(t0.Add(-28*time.Hour)) + " DONE appxite-map:uploads -> appxite-map:lead — UPLOADS.md bb0fb\n", "", false, true},
	} {
		home := setup(t, map[string]string{c.log: c.bus})
		toml := c.toml
		if toml == "" { // the real home always has one: its lead is named lead
			toml = "[projects." + strings.TrimSuffix(c.log, ".log") + "]\nleads = { lead = \"/nowhere/STATUS.md\" }\n"
		}
		testutil.Write(t, filepath.Join(home, "projects.toml"), toml)
		f := &fake{}
		draw := func(at time.Time) []Screen {
			s := screen(c.title, tab(c.title, "Done. Waiting for the next item.\n\n⏳", "", at))
			if c.failRead && at.Equal(t0.Add(15*time.Minute)) {
				s.Text, s.Err = "", "read-screen: exit status 1"
			}
			return []Screen{s}
		}
		idle := func() (out []string) { // the unanswered DONE of a case wakes Crius too: only idle alerts count
			for _, s := range f.sent {
				if strings.Contains(s, "is idle") {
					out = append(out, s)
				}
			}
			return out
		}
		runAt(t, home, f, draw, 0, 5, 11)
		if len(idle()) != 1 {
			t.Fatalf("%s: sent %q, want one idle alert by min 11", c.name, f.sent)
		}
		if strings.HasPrefix(c.toml, "[projects.axi\n") && !strings.Contains(idle()[0], "projects.toml cannot be read") {
			t.Errorf("%s: %q does not say projects.toml cannot be read", c.name, idle()[0])
		}
		if !c.repeats && !strings.Contains(idle()[0], "next item or park it") {
			t.Errorf("%s: %q does not say it waits for its next item", c.name, idle()[0])
		}
		runAt(t, home, f, draw, 15, 31, 51, 71)
		if got := len(idle()) > 1; got != c.repeats {
			t.Errorf("%s: sent %q, want repeats %v", c.name, f.sent, c.repeats)
		}
	}
}

// Crius 10-30 15:33: appxite-map:lead drew an IDLE every 20 min though its STATUS says its phases are done: its GOAL
// block (the real one below) is not in the RULES 3.1 form, so it was never seen as finished, and nothing said why. The
// alert names the unreadable block; the same block in the RULES form, Done: 100%, ends the alerts.
func TestALeadWhoseGoalCannotBeReadSaysWhy(t *testing.T) {
	real := "## GOAL (2026-10-29 13:4x)\nGoal: an evidence-based Appxite health map\n" +
		"- [x] M1 Inventory: MAP.md · b3aaeea\n- [x] M5 Verdicts: VERDICTS.md · cb6dfb4 · VERIFIED crius 12:47\n" +
		"Done: 100% of phase 1 (5 of 5 milestones verified: M1-M5)\n" +
		"- [x] P4 Start: START.md · 2c3eb59 · DONE posted (awaiting VERIFIED)\n" +
		"Next: phases 2-4 done and VERIFIED (crius 22:29); waiting for the owner walk-through (NEED-OWNER posted)\n"
	fixed := "## GOAL (2026-10-30 15:50)\nGoal: an evidence-based Appxite health map\n" +
		"- [x] M1 Inventory: MAP.md · b3aaeea\n- [x] M5 Verdicts: VERDICTS.md · cb6dfb4 · VERIFIED crius 12:47\n" +
		"- [x] P4 Start: START.md · 2c3eb59 · VERIFIED crius 22:29\n" +
		"Done: 100% (phases 1-4: 3 of 3 milestones verified)\nNext: owner walk-through\nNeeds owner: walk-through\n"
	for _, c := range []struct {
		name, block string
		idle        bool
	}{{"real block", real, true}, {"RULES form", fixed, false}} {
		home := setup(t, map[string]string{"appxite-map.log": stamp(t0.Add(-22*time.Hour)) +
			" INFO appxite-map:lead -> crius:lead — Mirror deleted; phases 2-4 VERIFIED\n"})
		status := filepath.Join(home, "appxite-map-STATUS.md")
		testutil.Write(t, filepath.Join(home, "projects.toml"), "[projects.appxite-map]\npersona = \"work\"\n"+
			"leads = { lead = \""+status+"\" }\n")
		testutil.Write(t, status, c.block)
		f := &fake{}
		draw := func(at time.Time) []Screen {
			return []Screen{screen("appxite-map · lead", tab("appxite-map · lead", "Waiting for the owner walk-through.\n\n⏳", "", at))}
		}
		runAt(t, home, f, draw, 0, 5, 11)
		if idle := len(f.sent) == 1 && strings.Contains(f.sent[0], "is idle"); idle != c.idle || len(f.sent) > 1 {
			t.Fatalf("%s: sent %q, want idle %v", c.name, f.sent, c.idle)
		}
		if c.idle && !strings.Contains(f.sent[0], "GOAL block unreadable: line 1: heading needs (YYYY-MM-DD HH:MM)") {
			t.Errorf("%s: %q does not say why its goal is not seen as done", c.name, f.sent[0])
		}
	}
}

// Crius 10-30 17:15: the marketplace lead asked the owner for A1, A2 and "go" in its own tab, more than once, with no
// NEED-OWNER line, and the questions waited unseen. A lead tab idle 10 min whose last reply asks becomes a queue row;
// the screen is the one captured at 14:58, cleaned.
func TestALeadThatAsksOnItsScreenBecomesAQueueRow(t *testing.T) {
	asked, err := os.ReadFile(filepath.Join("testdata", "marketplace-asks-1458.screen"))
	if err != nil {
		t.Fatal(err)
	}
	idle := func(reply string) func(time.Time) string {
		return func(at time.Time) string {
			return tab("marketplace · lead", reply, "  ⏵⏵ bypass permissions on\n", at)
		}
	}
	still := func(time.Time) string { return string(asked) }
	// the upload-central lead's shape (live tabs, 10-30 18:00): a harness notice under the reply that asks
	box := strings.Index(string(asked), "\n───")
	noticed := func(time.Time) string {
		return string(asked[:box]) + "\n✻ Worked for 27s · done 1:57 PM\n\n⏺ Background shell command didn't finish before the previous session ended\n" + string(asked[box:])
	}
	headless := func(time.Time) string { return string(asked[strings.Index(string(asked), "  Three things"):]) }
	// review round 2 of 2bca850 (P2-2): the same above the "✻ Worked for" line a long turn leaves
	worked := func(time.Time) string {
		return string(asked[strings.Index(string(asked), "  Three things"):box]) + "\n✻ Worked for 27s · done 1:57 PM\n" + string(asked[box:])
	}
	unseen := func(time.Time) string {
		return "⏺ Compacted while idle, before the prompt cache expired\n" + string(asked[box:])
	}
	dialog := func(time.Time) string { return string(asked) + "\n ❯ 1. Yes\n   2. No\n" }
	for _, c := range []struct {
		name  string
		bus   string
		tab   string
		draw  []func(time.Time) string // the screen at 0, 5, 11 and 16 min
		state string                   // the screen row's state after the last run; "" for none
	}{
		{"the captured ask", "", "marketplace · lead", []func(time.Time) string{still, still, still, still}, ownerqueue.Open},
		{"asked on the bus too", stamp(t0) + " NEED-OWNER marketplace:lead -> crius:lead — A1, A2 and go on BUILD-PLAN.md\n",
			"marketplace · lead", []func(time.Time) string{still, still, still, still}, ""},
		{"nothing asked", "", "marketplace · lead", []func(time.Time) string{idle("Build plan committed. Nothing needs you here. ✅"),
			idle("Build plan committed. Nothing needs you here. ✅"), idle("Build plan committed. Nothing needs you here. ✅"),
			idle("Build plan committed. Nothing needs you here. ✅")}, ""},
		{"a notice under the ask", "", "marketplace · lead", []func(time.Time) string{noticed, noticed, noticed, noticed}, ownerqueue.Open},
		{"no reply in sight", "", "marketplace · lead", []func(time.Time) string{still, still, still, unseen}, ownerqueue.Open},
		// review round 1 of 2bca850 (P1): a reply that asks nothing does not mean the owner answered; the row stays
		{"a reply that asks nothing later", "", "marketplace · lead", []func(time.Time) string{still, still, still,
			idle("Holding until the owner answers A1. ⏳")}, ownerqueue.Open},
		// review round 1 of 2bca850 (P2): a long ask whose "⏺" line has scrolled off
		{"the head scrolled off", "", "marketplace · lead", []func(time.Time) string{headless, headless, headless, headless}, ownerqueue.Open},
		{"the head scrolled off above a Worked-for line", "", "marketplace · lead", []func(time.Time) string{worked, worked, worked, worked}, ownerqueue.Open},
		{"a dialog", "", "marketplace · lead", []func(time.Time) string{dialog, dialog, dialog, dialog}, ""},
	} {
		home := setup(t, map[string]string{"marketplace.log": busFrom("marketplace:lead", t0.Add(-time.Hour)) + c.bus})
		status := filepath.Join(home, "marketplace-STATUS.md")
		testutil.Write(t, filepath.Join(home, "projects.toml"), "[projects.marketplace]\nname = \"Azure Marketplace listing\"\n"+
			"persona = \"work\"\ncwd = [\"/x\"]\nleads = { lead = \""+status+"\" }\n")
		testutil.Write(t, status, "# Marketplace\n\n## GOAL (2026-10-30 09:00)\nGoal: list the product on the Azure Marketplace.\n"+
			"- [ ] Build · plan\nDone: 10% (0 of 1 milestones)\nNext: build\nNeeds owner: nothing\n")
		if err := ownerqueue.Init(home, stamp(t0.Add(-2*time.Hour))); err != nil {
			t.Fatal(err)
		}
		f := &fake{}
		var rows []ownerqueue.Row
		for i, m := range []int{0, 5, 11, 16}[:len(c.draw)] {
			runAt(t, home, f, func(at time.Time) []Screen {
				return []Screen{{Surface: "surface:7", Title: "marketplace", Text: c.draw[i](at)}}
			}, m)
			q, err := ownerqueue.List(home, t0.Add(time.Duration(m)*time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			rows = nil
			for _, r := range q.Rows {
				if r.Screen != "" {
					rows = append(rows, r)
				}
			}
			if m < 11 && len(rows) != 0 {
				t.Fatalf("%s: a row at %d min, before the screen waited 10 min: %+v", c.name, m, rows)
			}
		}
		if c.state == "" {
			if len(rows) != 0 {
				t.Errorf("%s: want no screen row, got %+v", c.name, rows)
			}
			continue
		}
		if len(rows) != 1 || rows[0].State != c.state {
			t.Fatalf("%s: want one %s screen row, got %+v", c.name, c.state, rows)
		}
		r := rows[0]
		if r.From != "marketplace" || r.Screen != "marketplace:lead" ||
			r.Title != "Azure Marketplace listing: Three things still need you before the build starts" {
			t.Errorf("%s: row %+v", c.name, r)
		}
		for _, want := range []string{"1. A1, the sign-in app", "2. A2, where the listing fields live", `3. "Go" on docs/build/BUILD-PLAN.md`,
			`"go, A1 and A2 as proposed" is enough`} {
			if !strings.Contains(r.Do, want) {
				t.Errorf("%s: the row does not carry %q:\n%s", c.name, want, r.Do)
			}
		}
	}
}

// A projects.toml that cannot be read names no lead, so no tab is read for what it asks: that is said, never a quiet
// "nothing asked" (review round 2 of 2bca850, P3-5).
func TestScreenAsksUnreadForWantOfProjectsTomlAreSaid(t *testing.T) {
	asked, err := os.ReadFile(filepath.Join("testdata", "marketplace-asks-1458.screen"))
	if err != nil {
		t.Fatal(err)
	}
	home := setup(t, map[string]string{"marketplace.log": busFrom("marketplace:lead", t0.Add(-time.Hour))})
	testutil.Write(t, filepath.Join(home, "projects.toml"), "[projects.marketplace\n")
	if err := ownerqueue.Init(home, stamp(t0.Add(-2*time.Hour))); err != nil {
		t.Fatal(err)
	}
	f := &fake{}
	runAt(t, home, f, func(time.Time) []Screen {
		return []Screen{{Surface: "surface:7", Title: "marketplace", Text: string(asked)}}
	}, 0, 5, 11)
	if !strings.Contains(strings.Join(f.sent, "\n"), "no lead tab was read for what it asks the owner: projects.toml cannot be read") {
		t.Fatalf("not said: %q", f.sent)
	}
}

// A tool's output whose "⏺" call line scrolled off is no reply: its last line may end on "?" (review round 2 of
// 2bca850, P3-4: "⎿ Interrupted · What should Claude do instead?" was read as the lead's ask).
func TestToolOutputWithItsCallScrolledOffIsNoReply(t *testing.T) {
	for _, body := range []string{
		"  ⎿  (no output)\n  ⎿  Interrupted · What should Claude do instead?\n",
		"     lines 1-40 of the log\n  ⎿  Push to origin main?\n\n✻ Worked for 3s\n",
	} {
		if r := lastReply(body); r != "" {
			t.Errorf("lastReply(%q) = %q, want none", body, r)
		}
	}
}

// The watch list (wakewatch-projects) quiets a team's alerts, not what its lead asks the owner: live 10-30 18:27, the
// marketplace and upload-central leads, the request's own cases, are not on it and their asks were never read.
func TestAnUnwatchedLeadsScreenAskStillReachesTheQueue(t *testing.T) {
	asked, err := os.ReadFile(filepath.Join("testdata", "marketplace-asks-1458.screen"))
	if err != nil {
		t.Fatal(err)
	}
	home := setup(t, map[string]string{"marketplace.log": busFrom("marketplace:lead", t0.Add(-time.Hour)), "quotes.log": ""})
	testutil.Write(t, filepath.Join(home, "wakewatch-projects"), "quotes\n")
	status := filepath.Join(home, "marketplace-STATUS.md")
	testutil.Write(t, filepath.Join(home, "projects.toml"), "[projects.marketplace]\nname = \"Azure Marketplace listing\"\n"+
		"persona = \"work\"\ncwd = [\"/x\"]\nleads = { lead = \""+status+"\" }\n")
	testutil.Write(t, status, "# Marketplace\n\n## GOAL (2026-10-30 09:00)\nGoal: list it.\n- [ ] Build · plan\nDone: 10% (0 of 1 milestones)\nNext: build\nNeeds owner: nothing\n")
	if err := ownerqueue.Init(home, stamp(t0.Add(-2*time.Hour))); err != nil {
		t.Fatal(err)
	}
	f := &fake{}
	runAt(t, home, f, func(time.Time) []Screen {
		return []Screen{{Surface: "surface:7", Title: "marketplace", Text: string(asked)}}
	}, 0, 5, 11, 16)
	q, err := ownerqueue.List(home, t0.Add(16*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	open := q.OpenRows()
	if len(open) != 1 || open[0].Screen != "marketplace:lead" {
		t.Fatalf("want the unwatched lead's ask as one open row, got %+v", open)
	}
	if len(f.sent) != 0 {
		t.Errorf("a team not watched raises no alert: %q", f.sent)
	}
}
