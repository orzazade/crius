package wakewatch

import (
	"errors"
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
