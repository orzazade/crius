package wakewatch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/testutil"
)

var t0 = time.Date(2026, 10, 21, 22, 0, 0, 0, time.Local)

func stamp(t time.Time) string { return t.Format("2006-01-02T15:04") }

type fake struct {
	sent    []string
	sendErr error
	screens []Screen
	scrErr  error
}

func (f *fake) deps(home string, now time.Time) Deps {
	return Deps{Home: home, Now: now,
		Screens: func() ([]Screen, error) { return f.screens, f.scrErr },
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
		stamp(t0) + " ASK axi:lead -> crius — which branch\n"
	home := setup(t, map[string]string{"quotes.log": bus})
	f := &fake{}
	if err := Run(f.deps(home, t0.Add(25*time.Minute))); err != nil || len(f.sent) != 1 {
		t.Fatal(err, f.sent)
	}
	for _, want := range []string{"needs an answer", "at 200K", "which branch"} {
		if !strings.Contains(f.sent[0], want) {
			t.Error("missing", want, f.sent[0])
		}
	}
	for _, not := range []string{"progress", "not for Crius"} {
		if strings.Contains(f.sent[0], not) {
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
	f := &fake{sendErr: errors.New("no TELEGRAM_BOT_TOKEN")}
	for _, m := range []time.Duration{21, 26} {
		if err := Run(f.deps(home, t0.Add(m*time.Minute))); err == nil {
			t.Fatal("no error")
		}
	}
	b, _ := os.ReadFile(filepath.Join(home, "bus", "crius.log"))
	if n := strings.Count(string(b), "ALERT"); n != 1 || !strings.Contains(string(b), "TELEGRAM_BOT_TOKEN") {
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

// A long batch goes out in parts under Telegram's limit; no alert is marked sent that the owner did not get.
func TestALongBatchIsSentInPartsAndNothingIsLost(t *testing.T) {
	var bus strings.Builder
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&bus, "%s DONE quotes:lead -> crius:lead — F-%03d · %s\n", stamp(t0), i, strings.Repeat("x", 80))
	}
	home := setup(t, map[string]string{"quotes.log": bus.String()})
	f := &fake{}
	if err := Run(f.deps(home, t0.Add(21*time.Minute))); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(f.sent, "\n")
	for i := 0; i < 60; i++ {
		if !strings.Contains(all, fmt.Sprintf("F-%03d", i)) {
			t.Errorf("F-%03d never sent", i)
		}
	}
	for _, m := range f.sent {
		if n := len([]rune(m)); n > 4096 {
			t.Errorf("part of %d runes", n)
		}
	}
}

// A part that fails to send stays unsent: the next run sends exactly what is left.
func TestAFailedPartIsRetriedAndSentPartsAreNot(t *testing.T) {
	var bus strings.Builder
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&bus, "%s DONE quotes:lead -> crius:lead — F-%03d · %s\n", stamp(t0), i, strings.Repeat("x", 80))
	}
	home := setup(t, map[string]string{"quotes.log": bus.String()})
	calls := 0
	var sent []string
	d := Deps{Home: home, Now: t0.Add(21 * time.Minute), Screens: func() ([]Screen, error) { return nil, nil },
		Send: func(text string) error {
			if calls++; calls == 2 {
				return errors.New("telegram answered 502")
			}
			sent = append(sent, text)
			return nil
		}}
	if err := Run(d); err == nil {
		t.Fatal("no error")
	}
	d.Now = d.Now.Add(5 * time.Minute)
	if err := Run(d); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(sent, "\n")
	for i := 0; i < 60; i++ {
		if n := strings.Count(all, fmt.Sprintf("F-%03d", i)); n != 1 {
			t.Errorf("F-%03d sent %d times", i, n)
		}
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

// Old keys are pruned only once the thing they name has gone: a tab still waiting, or a check still failing, after
// a day is not sent again.
func TestAStillActiveAlertIsNotResentAfterADay(t *testing.T) {
	home := setup(t, nil)
	f := &fake{screens: []Screen{screen("quotes: lead", question)}}
	for _, m := range []time.Duration{0, 11, 25 * 60, 25*60 + 5} {
		Run(f.deps(home, t0.Add(m*time.Minute)))
	}
	if len(f.sent) != 1 {
		t.Fatal("waiting tab:", f.sent)
	}
	home = setup(t, nil)
	f = &fake{scrErr: errors.New("cmux: socket not found")}
	for _, m := range []time.Duration{0, 25 * 60, 25*60 + 5} {
		Run(f.deps(home, t0.Add(m*time.Minute)))
	}
	if len(f.sent) != 1 {
		t.Fatal("failing check:", f.sent)
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
	pad := strings.Repeat("x", maxText-len([]rune("quotes.log: "+head+"postgres://user:hunter2")))
	home := setup(t, map[string]string{"quotes.log": head + pad + "postgres://user:hunter2@db.internal/app tail\n"})
	f := &fake{}
	if err := Run(f.deps(home, t0.Add(21*time.Minute))); err != nil || len(f.sent) != 1 {
		t.Fatal(err, len(f.sent))
	}
	if strings.Contains(f.sent[0], "hunter2") {
		t.Fatal("password sent:", f.sent[0][len(f.sent[0])-80:])
	}
}

// Parts are measured after redaction, header included: text that grows when redacted still fits Telegram's limit.
func TestPartsFitAfterRedaction(t *testing.T) {
	var bus strings.Builder
	for i := 0; i < 9; i++ {
		fmt.Fprintf(&bus, "%s DONE quotes:lead -> crius:lead — F-%d %s\n", stamp(t0), i, strings.Repeat(".pwd='' ", 37))
	}
	home := setup(t, map[string]string{"quotes.log": bus.String()})
	f := &fake{}
	if err := Run(f.deps(home, t0.Add(21*time.Minute))); err != nil {
		t.Fatal(err)
	}
	for _, m := range f.sent {
		if n := len([]rune(m)); n > 4096 {
			t.Errorf("part of %d runes", n)
		}
	}
	for i := 0; i < 9; i++ {
		if !strings.Contains(strings.Join(f.sent, "\n"), fmt.Sprintf("F-%d ", i)) {
			t.Errorf("F-%d never sent", i)
		}
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
	if a := alertLines(t, home); len(a) != 1 || !strings.Contains(a[0], "quotes:lead") || !strings.Contains(a[0], "IDLE") {
		t.Fatal(a)
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

func TestOnlyLeadTabsWithABusAreCheckedForIdle(t *testing.T) {
	home := setup(t, map[string]string{"quotes.log": busFrom("quotes:lead", t0)})
	f := &fake{}
	runAt(t, home, f, func(at time.Time) []Screen {
		return []Screen{
			screen("quotes · qa", tab("quotes · qa", "checked", "", at)),
			{Surface: "surface:8", Title: "first-order · lead", Text: tab("first-order · lead", "done", "", at)},
			{Surface: "surface:9", Title: "zsh", Text: "~ % \n"},
		}
	}, 0, 16, 30)
	if len(f.sent) != 0 {
		t.Fatal(f.sent)
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

// Waking Crius does not wait for Telegram: the bus line goes even when the owner cannot be reached, and only once.
func TestAnIdleLeadWakesCriusWhenTelegramFails(t *testing.T) {
	home := setup(t, map[string]string{"quotes.log": busFrom("quotes:lead", t0)})
	f := &fake{sendErr: errors.New("telegram down")}
	runAt(t, home, f, func(at time.Time) []Screen {
		return []Screen{screen("quotes", tab("quotes: lead", "done", "", at))}
	}, 0, 16, 21)
	n := 0
	for _, l := range alertLines(t, home) {
		if strings.Contains(l, "IDLE") {
			n++
		}
	}
	if n != 1 {
		t.Fatal(alertLines(t, home))
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
	if len(f.sent) != 2 || len(alertLines(t, home)) != 2 {
		t.Fatal(f.sent, alertLines(t, home))
	}
}

// Review 0bac93a P2: a bus that cannot be written must not keep the owner's alerts from going out, and is loud.
func TestAFailedWakeStillSendsTelegram(t *testing.T) {
	home := setup(t, map[string]string{"quotes.log": busFrom("quotes:lead", t0) + done(t0)})
	f := &fake{}
	draw := func(at time.Time) []Screen {
		return []Screen{screen("quotes", tab("quotes: lead", "done", "", at))}
	}
	runAt(t, home, f, draw, 0)
	if err := os.Chmod(filepath.Join(home, "bus", "crius.log"), 0o400); err != nil {
		t.Fatal(err)
	}
	f.screens = draw(t0.Add(21 * time.Minute))
	if err := Run(f.deps(home, t0.Add(21*time.Minute))); err == nil {
		t.Fatal("a failed wake was silent")
	}
	if len(f.sent) != 1 || !strings.Contains(f.sent[0], "F-86") || !strings.Contains(f.sent[0], "idle") {
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
