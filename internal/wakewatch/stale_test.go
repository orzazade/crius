package wakewatch

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// Crius 11-01 22:09: quotes · backend-orders sat dead on an API error and Crius missed it: its turn ended on the error
// and a waiter shell kept the tab looking busy. Such a tab is STOPPED at once, and again every 20 min while it stays so.
func TestATabStoppedOnAnAPIErrorAlertsAtOnce(t *testing.T) {
	stop := "API Error: Your computer went to sleep mid-response. The response above may be incomplete.\n\n✻ Brewed for 1h 13m 31s · done 10:15 PM · 1 shell still running"
	jobs := "  ⏵⏵ bypass permissions on · 1 shell\n"
	home := setup(t, map[string]string{"quotes.log": busFrom("quotes:lead", t0)})
	f := &fake{}
	draw := func(at time.Time) []Screen {
		return []Screen{screen("quotes · backend-orders", tab("quotes · backend-orders", stop, jobs, at))}
	}
	runAt(t, home, f, draw, 0)
	if len(f.sent) != 1 || !strings.Contains(f.sent[0], "quotes:backend-orders (tab \"quotes · backend-orders\", surface:7) STOPPED: its last turn ended on an API error: API Error: Your computer went to sleep mid-response") {
		t.Fatal(f.sent)
	}
	runAt(t, home, f, draw, 5, 19)
	if len(f.sent) != 1 {
		t.Fatal("said again within 20 min:", f.sent)
	}
	for name, c := range map[string]struct {
		session, reply string
		stopped        bool
	}{
		"still retrying":            {"quotes · backend-orders", "API Error (Request timed out.) · Retrying in 5 seconds… (attempt 3/10)\n\n✻ Brewing… (12s · esc to interrupt)", false},
		"an error, then it went on": {"quotes · backend-orders", "API Error: 500 Internal server error\n\n⏺ Picked up again: the checks pass.\n\n✻ Brewed for 2m · done 10:20 PM", false},
		"an ordinary end":           {"quotes · backend-orders", "PR opened, checks running.\n\n⏳", false},
		"a retry countdown alone":   {"quotes · backend-orders", "API Error (Overloaded) · Retrying in 3 seconds… (attempt 2/10)", false},
		"an error, working again":   {"quotes · backend-orders", "API Error: 529 Overloaded\n\n✻ Brewing… (4s · esc to interrupt)", false},
		// review of X5: words, not Claude Code's error line ("API Error: …" or "API Error (…)")
		"a reply about API errors": {"quotes · backend-orders", "API Error handling is fixed and tested.\n\n✅", false},
		// review of X5: a tab outside any team is no one's to restart
		"a session of no team": {"scratch", "API Error: 500 Internal server error", false},
		// review of X5: the owner's "go" to an earlier ❓, then the next turn died; the old ❓ is still on screen
		"an old question, then the error": {"quotes · backend-orders", "Shall I deploy it?\n\n❓\n\n> go\n\n⏺ API Error: 500 Internal server error", true},
		"an error in parentheses":         {"quotes · backend-orders", "API Error (Connection error.)", true},
		// review r2 of X5: a reply that opens with the words and ends on a status emoji is an ordinary turn
		"a question about an API error": {"quotes · backend-orders", "API Error (429) shows in the logs again. Shall I add a retry?\n\n  ❓", false},
		"a fix of API errors, done":     {"quotes · backend-orders", "API Error: 500 responses now retry three times.\n\n✅", false},
	} {
		home := setup(t, map[string]string{"quotes.log": busFrom("quotes:lead", t0)})
		f := &fake{}
		runAt(t, home, f, func(at time.Time) []Screen {
			return []Screen{screen(c.session, tab(c.session, c.reply, jobs, at))}
		}, 0, 5)
		var stopped bool
		for _, s := range f.sent {
			stopped = stopped || strings.Contains(s, "STOPPED")
		}
		if stopped != c.stopped {
			t.Errorf("%s: STOPPED %v: %q", name, stopped, f.sent)
		}
	}
}

// Crius 11-01 22:09: Crius relayed waits whose prompt was gone. A prompt is live while any rule sees one; only a tab
// read with none is gone, and a tab that cannot be read is not.
func TestAPromptIsGoneOnlyWhenTheTabShowsNone(t *testing.T) {
	menu := "⏺ Bash(rm -rf build)\n\n Do you want to proceed?\n   1. Yes\n ❯ 2. No, and tell Claude what to do differently\n\n Esc to cancel\n"
	idleBox := "───────── quotes · lead ─\n❯ \n─────────\n  Opus 5.5 │ main\n"
	for name, c := range map[string]struct {
		text string
		live bool
	}{
		"the owner-click dialog":             {screenFile(t, "owner-click-hook-1103.screen"), true},
		"a menu with the cursor on 2":        {menu, true},
		"a dropped prompt below the box":     {"⏺ done\n" + idleBox + "  ⎿  Prompt dropped by a hook: cache tax\n", true},
		"answered: the box is back":          {menu + "⏺ Not run.\n" + idleBox, false},
		"an idle tab":                        {"⏺ PR opened.\n\n✅\n" + idleBox, false},
		"a question in prose, no menu":       {"⏺ Should I merge it?\n\n❓\n" + idleBox, false},
		"a reply listing options, no cursor": {"⏺ Which one should I take?\n  1. Fix the test\n  2. Skip it\n\n✻ Brewing… (3s · esc to interrupt)\n", false},
		// review of X5: a no-break space where a space was drawn; perl's \s takes it, Go's \s does not
		"a menu drawn with no-break spaces": {"⏺ Bash(rm -rf build)\n\n Do you want to proceed?\n\u00a0\u00a0 1. Yes\n ❯\u00a02. No\n", true},
		// review r2 of X5: the idle input box is "❯" and a no-break space; a draft in it that starts with a number is no
		// dialog (testdata/marketplace-asks-1458.screen)
		"a numbered draft in the input box": {"⏺ PR opened.\n\n⏳\n" + strings.Replace(idleBox, "❯ \n", "❯\u00a01. fix the flaky test first\n", 1), false},
	} {
		if got := LivePrompt(c.text); got != c.live {
			t.Errorf("%s: live %v", name, got)
		}
	}
	if gone, err := Gone(func() (string, error) { return menu + "⏺ Not run.\n" + idleBox, nil }); !gone || err != nil {
		t.Error("an answered prompt is not gone:", gone, err)
	}
	if gone, err := Gone(func() (string, error) { return menu, nil }); gone || err != nil {
		t.Error("a live prompt is gone:", gone, err)
	}
	if gone, err := Gone(func() (string, error) { return "", errors.New("cmux read-screen: timeout") }); gone || err == nil {
		t.Error("a tab that cannot be read is gone:", gone, err)
	}
}
