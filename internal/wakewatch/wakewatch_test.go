package wakewatch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/orzazade/crius/internal/testutil"
)

var t0 = time.Date(2026, 10, 21, 22, 0, 0, 0, time.Local)

func stamp(t time.Time) string { return t.Format("2006-01-02T15:04") }

type fake struct {
	sent    []string
	sendErr error
	screens []Screen
	scrErr  error
	freeGB  float64 // free disk; 0 means a healthy 100
	swapGB  float64
	resErr  error
	tools   map[string]error
	ses     []Session
	sesErr  error
}

func (f *fake) deps(home string, now time.Time) Deps {
	return Deps{Home: home, Now: now,
		Screens:  func() ([]Screen, error) { return f.screens, f.scrErr },
		Tools:    func() map[string]error { return f.tools },
		Sessions: func() ([]Session, error) { return f.ses, f.sesErr },
		Resources: func() (float64, float64, error) {
			if f.freeGB == 0 {
				return 100, f.swapGB, f.resErr
			}
			return f.freeGB, f.swapGB, f.resErr
		},
		Send: func(text string) error {
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
	for _, m := range []time.Duration{21, 26} {
		if err := Run(f.deps(home, t0.Add(m*time.Minute))); err == nil {
			t.Fatal("no error")
		}
	}
	b, _ := os.ReadFile(filepath.Join(home, "bus", "crius.log"))
	if n := strings.Count(string(b), "ALERT"); n != 1 || !strings.Contains(string(b), "no surface:29") {
		t.Fatal(n, string(b))
	}
	f.sendErr = nil
	if err := Run(f.deps(home, t0.Add(31*time.Minute))); err != nil || len(f.sent) != 1 {
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
	pad := strings.Repeat("x", maxLine-len([]rune("quotes.log: "+head+"postgres://user:hunter2")))
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
func TestAnIdleLeadAlertsAfter15MinAndWakesCrius(t *testing.T) {
	home := setup(t, map[string]string{"quotes.log": busFrom("quotes:lead", t0)})
	f := &fake{}
	draw := func(at time.Time) []Screen {
		return []Screen{screen("quotes", tab("quotes: lead", "The f177 rerun is on :7090.", "  ⏵⏵ bypass permissions on\n", at))}
	}
	runAt(t, home, f, draw, 0, 5, 10, 14)
	if len(f.sent) != 0 || len(alertLines(t, home)) != 0 {
		t.Fatal(f.sent, alertLines(t, home))
	}
	runAt(t, home, f, draw, 16, 21, 26)
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
	runAt(t, home, f, draw, 0, 16, 24)
	if len(f.sent) != 0 {
		t.Fatal(f.sent)
	}
	runAt(t, home, f, draw, 26)
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
// 20 min. Builders post as "P3", not "quotes:p3", so their screen alone tells how long they have sat.
func TestAnIdleBuilderAlertsAfter20Min(t *testing.T) {
	for _, c := range []struct{ title, bus, reply string }{
		{"quotes · p6", stamp(t0.Add(-4*time.Hour)) + " READY P6 -> quotes:lead — F-226 on stream/l1\n", "Waiting for another stream on main.\n\n⏳"},
		{"quotes · p3", stamp(t0) + " READY P3 -> quotes:lead — F-141 on stream/d\n", "READY posted; QA2 checks it next.\n\n⏳"},
		{"quotes · qa1", busFrom("quotes:lead", t0), "J1 passed on main.\n\n✅"},
	} {
		home := setup(t, map[string]string{"quotes.log": c.bus})
		f := &fake{}
		draw := func(at time.Time) []Screen { return []Screen{screen(c.title, tab(c.title, c.reply, "", at))} }
		runAt(t, home, f, draw, 0, 10, 19)
		if len(f.sent) != 0 {
			t.Fatal(c.title, "alerted before 20 min:", f.sent)
		}
		runAt(t, home, f, draw, 21, 30)
		who := strings.Replace(c.title, " · ", ":", 1)
		if len(f.sent) != 1 || !strings.Contains(f.sent[0], who+" (tab") || !strings.Contains(f.sent[0], "is idle") {
			t.Fatal(c.title, f.sent)
		}
		// Review 897b5c1 P3: no line is sent as quotes:p6, so only the screen's age is known.
		if !strings.Contains(f.sent[0], "screen unchanged for 21 min, no bus line from "+who) {
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

func TestCLEARREADYAboveTheLastReplyDoesNotCount(t *testing.T) {
	home := setup(t, map[string]string{"crius.log": busFrom("crius:build", t0)})
	f := &fake{}
	runAt(t, home, f, func(at time.Time) []Screen {
		return []Screen{screen("crius · build", "  at ~200K write CLEAR-READY and stop\n"+tab("crius · build", "Waiting for GO.\n\n⏳", "", at))}
	}, 0, 16)
	if len(f.sent) != 0 {
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
	if len(f.sent) != 0 {
		t.Fatal(f.sent)
	}
}

// Review 0bac93a P3: another session in the same tab starts its own clock, even with the same text on screen.
func TestANewSessionInTheSameTabStartsAfresh(t *testing.T) {
	home := setup(t, map[string]string{"quotes.log": busFrom("quotes:lead", t0), "cortex.log": busFrom("cortex:lead", t0)})
	f := &fake{}
	runAt(t, home, f, func(at time.Time) []Screen {
		return []Screen{screen("x", tab("quotes: lead", "done", "", at))}
	}, 0, 16)
	draw := func(at time.Time) []Screen { return []Screen{screen("x", tab("cortex · lead", "done", "", at))} }
	runAt(t, home, f, draw, 17, 30)
	if len(f.sent) != 1 {
		t.Fatal(f.sent)
	}
	runAt(t, home, f, draw, 33)
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
		if !strings.HasPrefix(m, "WAKEWATCH: ") || strings.ContainsAny(m, "\\\n\r\t") || len([]rune(m)) > len("WAKEWATCH: ")+maxLine+1 {
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

// Brief P7 item 4: below 20 GB free disk or above 20 GB swap wakes Crius, by the same tab line and 20 min rhythm.
func TestLowDiskOrHighSwapWakesCrius(t *testing.T) {
	for _, c := range []struct {
		free, swap float64
		want       string
	}{{19.5, 1, "disk"}, {200, 20.5, "swap"}, {20, 20, ""}, {25, 3, ""}} {
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
		if len(f.sent) != 2 || !strings.Contains(f.sent[0], c.want) || !strings.Contains(f.sent[0], "20 GB") {
			t.Fatal(c, f.sent)
		}
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
