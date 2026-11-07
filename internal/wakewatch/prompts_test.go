package wakewatch

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Owner 11-08 ("make sure you have a watchdog for your own sessions, and they have for the rest"): on 11-07 from 20:00
// crius:ops sat on its own permission prompt (click-check.sh: the post-guard could not read ops-pins.sh), so it answered
// nothing, and 10 tabs waited on prompts until the owner noticed. Nothing woke Crius: the CRIUS-CLICK lines went to the
// digest, and the tab check's dialog alert is muted for a prompt CRIUS-CLICK posted.

type promptFake struct {
	tabs    []Screen
	crius   []string         // typed into Crius's tab
	ops     []string         // "title: text" typed into another tab
	live    map[string]error // surface -> why it has no live Claude
	tabErr  error            // SendTab's error
	handed  bool             // SendTab's error still hands the text over
	at      time.Time
	logLine func(string)
}

func (f *promptFake) run(t *testing.T, home string, min int) {
	t.Helper()
	f.at = t0.Add(time.Duration(min) * time.Minute)
	d := PromptDeps{Home: home, Now: f.at,
		Screens:   func() ([]Screen, error) { return f.tabs, nil },
		SendCrius: func(text string) error { f.say("Crius", text); f.crius = append(f.crius, text); return nil },
		SendTab: func(title, text string) error {
			f.say(title, text)
			f.ops = append(f.ops, title+": "+text)
			return f.tabErr
		},
		HandedOver: func(error) bool { return f.handed },
		Live:       func(surface string) error { return f.live[surface] },
	}
	if err := Prompts(d); err != nil {
		t.Fatal(err)
	}
}

func (f *promptFake) say(to, text string) {
	if f.logLine != nil {
		f.logLine(fmt.Sprintf("%s -> %s: %s", f.at.Format("15:04"), to, text))
	}
}

// permission is a Claude Code permission dialog for a Bash command in the tab named name, with the hook's reason above
// "Do you want to proceed?".
func permission(name, cmd, reason string) string {
	return "⏺ Bash(" + cmd + ")\n" +
		"────────────────────────────────────────────────────────\n" +
		" Bash command\n\n" +
		"   " + cmd + "\n" +
		"   Run the check\n\n" +
		reason +
		" Do you want to proceed?\n" +
		" ❯ 1. Yes\n" +
		"   2. Yes, and don't ask again for this command\n" +
		"   3. No\n\n" +
		" Esc to cancel · Tab to amend\n" +
		"──────────────────────────────────────────── " + name + " ─\n"
}

func idleTab(name string) string {
	return "⏺ Waiting for the next line.\n──────────── " + name + " ─\n❯ \n────────────\n"
}

const (
	opsSurface   = "surface:1000070001"
	buildSurface = "surface:1000070002"
	opsCmd       = "~/.crius/state/crius/ops-bin/click-check.sh workspace:1000070003 surface:1000070012"
	opsReason    = " Hook PreToolUse:Bash requires confirmation for this command:\n Posts text to Azure DevOps under the owner's name: needs the owner's click. (a script the command or the script itself may change before it runs (/Users/x/.crius/state/crius/ops-pins.sh): the guard cannot read; /Users/x/.crius/state/crius/ops-bin/click-check.sh line 9)\n"
)

func opsIdle() Screen {
	return Screen{Surface: opsSurface, Title: "crius · ops", Text: idleTab("crius · ops")}
}

func TestAPromptInCriussOwnTabWakesCriusAfter3Min(t *testing.T) {
	home := setup(t, nil)
	f := &promptFake{tabs: []Screen{
		{Surface: "surface:1000070000", Title: "crius", Text: permission("crius", "git push", "")}, // only the owner answers Crius's own
		opsIdle(),
		{Surface: buildSurface, Title: "crius · build", Text: permission("crius · build", "go test ./internal/wakewatch/", "")},
	}}
	for min := 0; min <= 2; min++ {
		f.run(t, home, min)
	}
	if len(f.crius)+len(f.ops) != 0 {
		t.Fatalf("woke before 3 min: %q %q", f.crius, f.ops)
	}
	f.run(t, home, 3)
	if len(f.crius) != 1 || !strings.Contains(f.crius[0], "crius · build (surface:1000070002) has had a permission prompt open for 3 min") ||
		!strings.Contains(f.crius[0], "go test ./internal/wakewatch/") || !strings.HasPrefix(f.crius[0], Prefix) {
		t.Fatalf("want one wake naming the tab and the command, got %q", f.crius)
	}
	for min := 4; min < 33; min++ {
		f.run(t, home, min)
	}
	if len(f.crius) != 1 || len(f.ops) != 0 {
		t.Fatalf("said again inside 30 min, or told ops: %q %q", f.crius, f.ops)
	}
	f.run(t, home, 33)
	if len(f.crius) != 2 {
		t.Fatalf("not said again after 30 min: %q", f.crius)
	}
}

func TestAnotherTabsPromptWakesOpsAt2MinThenCriusAt10(t *testing.T) {
	home := setup(t, nil)
	lead := Screen{Surface: "surface:1000070008", Title: "quotes", Text: permission("quotes · lead", "bash ~/.crius/state/quotes/build/bin/pr-build.sh 7856", "")}
	f := &promptFake{tabs: []Screen{opsIdle(), lead}}
	f.run(t, home, 0)
	f.run(t, home, 1)
	if len(f.crius)+len(f.ops) != 0 {
		t.Fatalf("woke before 2 min: %q %q", f.crius, f.ops)
	}
	f.run(t, home, 2)
	if len(f.ops) != 1 || !strings.HasPrefix(f.ops[0], "crius · ops: "+Prefix) || !strings.Contains(f.ops[0], "quotes · lead (surface:1000070008, 2 min)") {
		t.Fatalf("want one line typed into the ops tab, got %q", f.ops)
	}
	for min := 3; min <= 9; min++ {
		f.run(t, home, min)
	}
	if len(f.crius) != 0 || len(f.ops) != 1 {
		t.Fatalf("woke again before 10 min: %q %q", f.crius, f.ops)
	}
	f.run(t, home, 10)
	if len(f.crius) != 1 || !strings.Contains(f.crius[0], "quotes · lead (surface:1000070008, 10 min; crius:ops told 22:02)") {
		t.Fatalf("want Crius woken at 10 min, got %q", f.crius)
	}
	f.run(t, home, 11)
	if len(f.crius) != 1 || len(f.ops) != 1 {
		t.Fatalf("said again: %q %q", f.crius, f.ops)
	}
	// the next prompt in that tab is new: its own 2 min
	f.tabs[1].Text = permission("quotes · lead", "bash ~/.crius/state/quotes/build/bin/merge.sh 7856", "")
	f.run(t, home, 12)
	f.run(t, home, 14)
	if len(f.ops) != 2 || !strings.Contains(f.ops[1], "quotes · lead (surface:1000070008, 2 min)") {
		t.Fatalf("a new prompt did not start afresh: %q", f.ops)
	}
	// answered: the input box is back, so it is no prompt
	f.tabs[1].Text = f.tabs[1].Text + "⏺ Merged.\n──── quotes · lead ─\n❯ \n────\n"
	f.run(t, home, 30)
	if len(f.crius) != 1 {
		t.Fatalf("an answered prompt woke Crius: %q", f.crius)
	}
}

func TestOpsOnAPromptOfItsOwnSendsTheOthersToCriusAtOnce(t *testing.T) {
	home := setup(t, nil)
	f := &promptFake{tabs: []Screen{
		{Surface: opsSurface, Title: "crius · ops", Text: permission("crius · ops", opsCmd, opsReason)},
		{Surface: "surface:1000070008", Title: "quotes", Text: permission("quotes · lead", "bash pr-build.sh 7856", "")},
	}}
	f.run(t, home, 0)
	f.run(t, home, 1)
	f.run(t, home, 2)
	if len(f.ops) != 0 {
		t.Fatalf("typed into the ops tab while it is on a prompt: %q", f.ops)
	}
	if len(f.crius) != 1 || !strings.Contains(f.crius[0], "crius:ops cannot answer prompts now (it is on a prompt of its own)") ||
		!strings.Contains(f.crius[0], "quotes · lead (surface:1000070008, 2 min)") {
		t.Fatalf("want Crius woken at once with the waiting tab, got %q", f.crius)
	}
	f.run(t, home, 3)
	if len(f.crius) != 2 || !strings.Contains(f.crius[1], "crius · ops (surface:1000070001) has had a permission prompt open for 3 min") ||
		!strings.Contains(f.crius[1], "click-check.sh") || !strings.Contains(f.crius[1], "ops-pins.sh") {
		t.Fatalf("want ops's own prompt said at 3 min with its command, got %q", f.crius)
	}
}

func TestOpsWithNoLiveClaudeWakesCriusAtOnce(t *testing.T) {
	home := setup(t, nil)
	f := &promptFake{tabs: []Screen{opsIdle()}, live: map[string]error{opsSurface: errors.New(`runs "-zsh" in front, not Claude`)}}
	f.run(t, home, 0)
	if len(f.crius) != 1 || !strings.Contains(f.crius[0], `crius:ops has no live Claude (runs "-zsh" in front, not Claude)`) {
		t.Fatalf("want Crius woken at once, got %q", f.crius)
	}
	f.run(t, home, 1)
	if len(f.crius) != 1 {
		t.Fatalf("said again inside 30 min: %q", f.crius)
	}
	// a prompt elsewhere goes to Crius at once, not to the dead tab
	f.tabs = append(f.tabs, Screen{Surface: "surface:1000070005", Title: "ado-91671", Text: permission("ado-91671 · lead", "az repos pr update --id 7596", "")})
	f.run(t, home, 2)
	f.run(t, home, 4)
	if len(f.ops) != 0 || len(f.crius) != 2 || !strings.Contains(f.crius[1], "crius:ops cannot answer prompts now (it has no live Claude") ||
		!strings.Contains(f.crius[1], "ado-91671 · lead (surface:1000070005, 2 min)") {
		t.Fatalf("want the prompt sent to Crius, got %q %q", f.crius, f.ops)
	}
	f.run(t, home, 30)
	if len(f.crius) != 3 {
		t.Fatalf("not said again after 30 min: %q", f.crius)
	}
	// no ops tab at all
	home = setup(t, nil)
	f = &promptFake{tabs: []Screen{{Surface: buildSurface, Title: "crius · build", Text: idleTab("crius · build")}}}
	f.run(t, home, 0)
	if len(f.crius) != 1 || !strings.Contains(f.crius[0], "crius:ops has no live Claude (no tab is named crius · ops)") {
		t.Fatalf("want Crius woken when no ops tab is open, got %q", f.crius)
	}
}

func TestTypingIntoOpsFailsWakesCriusAtOnce(t *testing.T) {
	home := setup(t, nil)
	f := &promptFake{tabs: []Screen{opsIdle(), {Surface: "surface:1000070008", Title: "quotes", Text: permission("quotes · lead", "bash pr-build.sh 7856", "")}},
		tabErr: errors.New("cmux send: refused")}
	f.run(t, home, 0)
	f.run(t, home, 2)
	if len(f.ops) != 1 || len(f.crius) != 1 || !strings.Contains(f.crius[0], "crius:ops cannot answer prompts now (typing into its tab failed: cmux send: refused)") {
		t.Fatalf("want Crius woken at once, got %q %q", f.crius, f.ops)
	}
	f.run(t, home, 3) // not stored: ops is tried again, Crius is not told again inside 30 min
	if len(f.ops) != 2 || len(f.crius) != 1 {
		t.Fatalf("%q %q", f.crius, f.ops)
	}
	f.handed, f.tabErr = true, errors.New("held: stored and typed by a later run")
	f.run(t, home, 4)
	f.run(t, home, 5)
	if len(f.ops) != 3 {
		t.Fatalf("a held line was typed again: %q", f.ops)
	}
}

// The replay of 11-07 20:00: crius:ops on its click-check.sh prompt, and 10 tabs on prompts (8 Quotes, the ado-91671 and
// angular-91260 leads). Run with -v to see each wake line.
func TestReplayOps1107OnAPromptAnd10TabsWaiting(t *testing.T) {
	home := setup(t, nil)
	tabs := []struct{ surface, name, cmd, reason string }{
		{"surface:1000070008", "quotes · lead", "bash /private/tmp/claude-501/x/scratchpad/lead/merge.sh 7804", " Hook PreToolUse:Bash requires confirmation for this command:\n Posts text to Azure DevOps under the owner's name: needs the owner's click. (a script or program the command makes before it runs)\n"},
		{"surface:1000070012", "quotes · orders-2", "bash ~/.crius/state/quotes/build/bin/pr-build.sh 7856", " Hook PreToolUse:Bash requires confirmation for this command:\n Posts text to Azure DevOps under the owner's name: needs the owner's click. (az pipelines build cancel; pr-build.sh line 28)\n"},
		{"surface:1000070016", "quotes · platform-db", "bash ~/.crius/state/quotes/build/bin/pr-build.sh 7857", ""},
		{"surface:1000070009", "quotes · qa-backend-2", "bash qa/tools/review-check.sh 7844", ""},
		{"surface:1000070010", "quotes · page-list", "az repos pr create --source-branch feature/96365-list --target-branch integration/96365-b1", ""},
		{"surface:1000070011", "quotes · files-2", "bash ~/.crius/state/quotes/build/bin/pass-post.sh 7856", ""},
		{"surface:1000070014", "quotes · core-2", "bun ~/.crius/state/quotes/build/bin/ado-suite.ts run 7860", ""},
		{"surface:1000070015", "quotes · page-quote-2", "git push origin feature/96365-quote", ""},
		{"surface:1000070005", "ado-91671 · lead", "az repos pr update --id 7596 --description \"…\"", ""},
		{"surface:1000070017", "angular-91260 · lead", "bash scripts/pr-build.sh 7612", ""},
	}
	f := &promptFake{tabs: []Screen{{Surface: opsSurface, Title: "crius · ops", Text: permission("crius · ops", opsCmd, opsReason)}},
		logLine: func(s string) { t.Log(s) }}
	for _, x := range tabs {
		f.tabs = append(f.tabs, Screen{Surface: x.surface, Title: strings.SplitN(x.name, " ", 2)[0], Text: permission(x.name, x.cmd, x.reason)})
	}
	at := map[int]int{} // minute -> lines to Crius by then
	for min := 0; min <= 12; min++ {
		f.run(t, home, min)
		at[min] = len(f.crius)
	}
	if len(f.ops) != 0 {
		t.Fatalf("typed into the ops tab while it was on a prompt: %q", f.ops)
	}
	if at[1] != 0 || at[2] == 0 {
		t.Fatalf("want the first wake at 2 min, got by minute: %v", at)
	}
	two := strings.Join(f.crius[:at[2]], "\n")
	for _, x := range tabs {
		if !strings.Contains(two, x.name+" ("+x.surface+", 2 min)") {
			t.Errorf("the 2-min wake does not name %s (%s)", x.name, x.surface)
		}
	}
	for _, l := range f.crius {
		if !strings.Contains(l, "crius:ops") && !strings.Contains(l, "crius · ops") || len([]rune(l)) > len([]rune(Prefix))+MaxLine {
			t.Errorf("a line that names no ops tab or is cut: %q", l)
		}
	}
	if at[3] != at[2]+1 || !strings.Contains(f.crius[at[2]], "crius · ops (surface:1000070001) has had a permission prompt open for 3 min") ||
		!strings.Contains(f.crius[at[2]], "click-check.sh") {
		t.Fatalf("want ops's own prompt at 3 min, got %q", f.crius[at[2]:])
	}
	if at[12] != at[3] {
		t.Fatalf("woke again before 30 min: %q", f.crius[at[3]:])
	}
}
