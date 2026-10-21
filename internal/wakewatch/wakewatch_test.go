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
