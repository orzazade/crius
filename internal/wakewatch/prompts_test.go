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

// Owner 11-08 ("make sure you have a watchdog for your own sessions, and they have for the rest"): on 11-07 from 20:00
// crius:ops sat on its own permission prompt (click-check.sh: the post-guard could not read ops-pins.sh), so it answered
// nothing, and 10 tabs waited on prompts until the owner noticed. Nothing woke Crius: the CRIUS-CLICK lines went to the
// digest, and the tab check's dialog alert is muted for a prompt CRIUS-CLICK posted.

type promptFake struct {
	tabs     []Screen
	crius    []string         // typed into Crius's tab
	ops      []string         // "title: text" typed into another tab
	live     map[string]error // surface -> why it has no live Claude
	tabErr   error            // SendTab's error
	failOn   string           // SendTab fails (not handed over) for a line holding this text
	dead     map[string]bool  // tabs with no live Claude
	handed   bool             // SendTab's error still hands the text over
	at       time.Time
	logLine  func(string)
	pushes   []string // "surface title: body" pushed to the owner
	pushErr  error
	allowErr bool  // a run's error is kept in err, not fatal
	err      error // the last run's error
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
			if f.failOn != "" && strings.Contains(text, f.failOn) {
				return errors.New("cmux send: refused")
			}
			return f.tabErr
		},
		HandedOver: func(error) bool { return f.handed },
		Push: func(surface, title, body string) error {
			f.pushes = append(f.pushes, surface+" "+title+": "+body)
			return f.pushErr
		},
		Live: func(surface string) error {
			if f.dead[surface] {
				return errors.New("runs \"-zsh\" in front, not Claude")
			}
			return f.live[surface]
		},
	}
	f.err = Prompts(d)
	if f.err != nil && !f.allowErr {
		t.Fatal(f.err)
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
	home := promptHome(t)
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
	home := promptHome(t)
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
	home := promptHome(t)
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
	home := promptHome(t)
	f := &promptFake{tabs: []Screen{opsIdle()}, live: map[string]error{opsSurface: errors.New(`runs "-zsh" in front, not Claude`)}}
	// seen dead on one run only: one failed read or cmux call is no restart (review of 42281dd, P3 6)
	f.run(t, home, 0)
	if len(f.crius) != 0 {
		t.Fatalf("woke Crius on one look: %q", f.crius)
	}
	f.run(t, home, 1)
	if len(f.crius) != 1 || !strings.Contains(f.crius[0], `crius:ops has no live Claude (runs "-zsh" in front, not Claude)`) {
		t.Fatalf("want Crius woken on the second run, got %q", f.crius)
	}
	f.run(t, home, 2)
	if len(f.crius) != 1 {
		t.Fatalf("said again inside 30 min: %q", f.crius)
	}
	// a prompt elsewhere goes to Crius at once, not to the dead tab
	f.tabs = append(f.tabs, Screen{Surface: "surface:1000070005", Title: "ado-91671", Text: permission("ado-91671 · lead", "az repos pr update --id 7596", "")})
	f.run(t, home, 3)
	f.run(t, home, 5)
	if len(f.ops) != 0 || len(f.crius) != 2 || !strings.Contains(f.crius[1], "crius:ops cannot answer prompts now (it has no live Claude") ||
		!strings.Contains(f.crius[1], "ado-91671 · lead (surface:1000070005, 2 min)") {
		t.Fatalf("want the prompt sent to Crius, got %q %q", f.crius, f.ops)
	}
	f.run(t, home, 31)
	if len(f.crius) != 3 {
		t.Fatalf("not said again after 30 min: %q", f.crius)
	}
	// a read that fails once is not a dead ops
	home = promptHome(t)
	f = &promptFake{tabs: []Screen{{Surface: opsSurface, Title: "crius · ops", Err: "cmux read-screen: timeout"}}}
	f.run(t, home, 0)
	f.tabs = []Screen{opsIdle()}
	f.run(t, home, 1)
	f.tabs = []Screen{{Surface: opsSurface, Title: "crius · ops", Err: "cmux read-screen: timeout"}}
	f.run(t, home, 2)
	if len(f.crius) != 0 {
		t.Fatalf("one failed read woke Crius: %q", f.crius)
	}
	// no ops tab at all
	home = promptHome(t)
	f = &promptFake{tabs: []Screen{{Surface: buildSurface, Title: "crius · build", Text: idleTab("crius · build")}}}
	f.run(t, home, 0)
	f.run(t, home, 1)
	if len(f.crius) != 1 || !strings.Contains(f.crius[0], "crius:ops has no live Claude (no tab is named crius · ops)") {
		t.Fatalf("want Crius woken when no ops tab is open, got %q", f.crius)
	}
}

func TestTypingIntoOpsFailsWakesCriusAtOnce(t *testing.T) {
	home := promptHome(t)
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
	home := promptHome(t)
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

// promptHome is a Crius home whose projects.toml names the projects Crius runs: their Azure DevOps asks are Crius's to
// answer (CRIUS-CLICK), so crius:ops gets them.
func promptHome(t *testing.T) string {
	home := setup(t, nil)
	testutil.Write(t, filepath.Join(home, "projects.toml"), "[projects.quotes]\n[projects.ado-91671]\n[projects.angular-91260]\n[projects.crius]\n")
	return home
}

// Review of 42281dd, P2 1: an Azure DevOps ask in a session Crius does not run is the owner's alone (Clicks posts it as
// OWNER-CLICK): crius:ops is not told to answer it.
func TestAnOwnerOnlyPromptIsNotTypedIntoOps(t *testing.T) {
	home := promptHome(t)
	ask := " Hook PreToolUse:Bash requires confirmation for this command:\n Posts text to Azure DevOps under the owner's name: needs the owner's click. (az repos pr create --description)\n"
	f := &promptFake{tabs: []Screen{opsIdle(),
		{Surface: "surface:9", Title: "appx", Text: permission("appx · lead", "az repos pr create --title x", ask)},
		{Surface: "surface:1000070010", Title: "quotes", Text: permission("quotes · page-list", "az repos pr create --title y", ask)}}}
	f.run(t, home, 0)
	f.run(t, home, 2)
	f.run(t, home, 10)
	if len(f.ops) != 1 || strings.Contains(f.ops[0], "appx") || !strings.Contains(f.ops[0], "quotes · page-list") {
		t.Fatalf("want only the Crius-run tab's ask typed into ops, got %q", f.ops)
	}
	if len(f.crius) != 1 || strings.Contains(f.crius[0], "appx") {
		t.Fatalf("the owner's ask reached Crius through the prompt watch: %q", f.crius)
	}
}

// Review of 42281dd, P2 2: a dialog left on a tab whose Claude died is the last frame, not a prompt anyone can answer.
func TestADialogOnATabWithNoLiveClaudeIsNoPrompt(t *testing.T) {
	home := promptHome(t)
	f := &promptFake{tabs: []Screen{opsIdle(),
		{Surface: "surface:1000070008", Title: "quotes", Text: permission("quotes · lead", "bash pr-build.sh 7856", "")}},
		dead: map[string]bool{"surface:1000070008": true}}
	for _, min := range []int{0, 2, 10, 40} {
		f.run(t, home, min)
	}
	if len(f.ops)+len(f.crius) != 0 {
		t.Fatalf("a dead tab's dialog woke someone: %q %q", f.ops, f.crius)
	}
}

// Review of 42281dd, P3 3: when one of several lines fails, only its prompts are typed again; the others went.
func TestOnlyTheFailedOpsLineIsTypedAgain(t *testing.T) {
	home := promptHome(t)
	f := &promptFake{tabs: []Screen{opsIdle()}, failOn: "surface:1000070100"}
	for i := 0; i < 14; i++ {
		s := fmt.Sprintf("surface:10000701%02d", i)
		f.tabs = append(f.tabs, Screen{Surface: s, Title: "quotes", Text: permission(fmt.Sprintf("quotes · page-%02d-with-a-long-name", i), "bash pr-build.sh "+s, "")})
	}
	f.run(t, home, 0)
	f.run(t, home, 2)
	if len(f.ops) < 2 {
		t.Fatalf("want the prompts in two or more lines, got %q", f.ops)
	}
	first := len(f.ops)
	f.run(t, home, 3)
	again := f.ops[first:]
	if len(again) != 1 || !strings.Contains(again[0], "surface:1000070100") || strings.Contains(again[0], "surface:1000070113") {
		t.Fatalf("want only the failed line typed again, got %q", again)
	}
	all := strings.Join(f.crius, "\n")
	if len(f.crius) == 0 || strings.Count(all, "typing into its tab failed") != len(f.crius) || !strings.Contains(all, "surface:1000070106") || strings.Contains(all, "surface:1000070113") {
		t.Fatalf("want Crius told of the failed line's prompts only, got %q", f.crius)
	}
}

// Review of 42281dd, P3 5: Crius heard of a prompt while ops was blocked; once ops is told, the 10-min wake still comes
// at 10 min, not 30 min after the first.
func TestTheTenMinuteWakeCountsFromWhenOpsWasTold(t *testing.T) {
	home := promptHome(t)
	f := &promptFake{tabs: []Screen{
		{Surface: opsSurface, Title: "crius · ops", Text: permission("crius · ops", opsCmd, "")},
		{Surface: "surface:1000070008", Title: "quotes", Text: permission("quotes · lead", "bash pr-build.sh 7856", "")}}}
	f.run(t, home, 0)
	f.run(t, home, 2) // ops blocked: Crius told
	f.tabs[0] = opsIdle()
	f.run(t, home, 4) // ops back: ops told
	if len(f.ops) != 1 {
		t.Fatalf("ops not told once it was back: %q", f.ops)
	}
	n := len(f.crius)
	f.run(t, home, 10)
	if len(f.crius) != n+1 || !strings.Contains(f.crius[n], "quotes · lead (surface:1000070008, 10 min; crius:ops told 22:04)") {
		t.Fatalf("want the 10-min wake at 10 min, got %q", f.crius[n:])
	}
}

// Review of 42281dd, P3 11: only crius:ops and crius:build are Crius's own tabs; another crius tab goes to ops.
func TestAnotherCriusTabGoesToOps(t *testing.T) {
	home := promptHome(t)
	f := &promptFake{tabs: []Screen{opsIdle(), {Surface: "surface:1000070030", Title: "crius · research", Text: permission("crius · research", "ls", "")}}}
	f.run(t, home, 0)
	f.run(t, home, 2)
	if len(f.ops) != 1 || !strings.Contains(f.ops[0], "crius · research") {
		t.Fatalf("want ops told, got %q %q", f.ops, f.crius)
	}
}

// Review of 42281dd, P3 4, 7 and 9: a wake held for Crius's tab is not also posted as an ALERT; crius:ops is found by its
// title before a name another tab draws; a state that cannot be written stops the run before anything is typed.
func TestHeldWakesTitleFirstAndStateFirst(t *testing.T) {
	home := promptHome(t)
	lead := Screen{Surface: "surface:1000070008", Title: "quotes", Text: permission("quotes · lead", "bash pr-build.sh 7856", "")}
	decoy := Screen{Surface: "surface:1000070040", Title: "crius · notes", Text: permission("crius · ops", "cat x", "")}
	f := &promptFake{tabs: []Screen{decoy, opsIdle(), lead}}
	f.run(t, home, 0)
	f.run(t, home, 2)
	if len(f.ops) != 1 || !strings.HasPrefix(f.ops[0], "crius · ops: ") || !strings.Contains(f.ops[0], "quotes · lead") {
		t.Fatalf("want the line typed into the tab titled crius · ops, got %q", f.ops)
	}
	held := 0
	d := PromptDeps{Home: home, Now: t0.Add(10 * time.Minute), Screens: func() ([]Screen, error) { return f.tabs, nil },
		SendCrius:  func(string) error { held++; return errors.New("held: stored") },
		SendTab:    func(string, string) error { return nil },
		HandedOver: func(error) bool { return true },
		Live:       func(string) error { return nil }}
	if err := Prompts(d); err != nil || held == 0 {
		t.Fatal(err, held)
	}
	if bus := testutil.Read(t, filepath.Join(home, "bus", "crius.log")); strings.Contains(bus, "ALERT") {
		t.Fatalf("a held wake was also posted:\n%s", bus)
	}
	// the state folder cannot be written: nothing is typed
	home = promptHome(t)
	f = &promptFake{tabs: []Screen{opsIdle(), lead}}
	f.run(t, home, 0)
	if err := os.Chmod(filepath.Join(home, "state"), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(home, "state"), 0o700) })
	f.at = t0.Add(2 * time.Minute)
	d = PromptDeps{Home: home, Now: f.at, Screens: func() ([]Screen, error) { return f.tabs, nil },
		SendCrius: func(string) error { t.Error("typed into Crius's tab"); return nil },
		SendTab:   func(string, string) error { t.Error("typed into ops"); return nil },
		Live:      func(string) error { return nil }}
	if err := Prompts(d); err == nil {
		t.Fatal("a state that cannot be written gave no error")
	}
}

// Crius froze 4h10 on 11-10 with its own tab stuck: the watch left that tab out ("only the owner answers it") and its
// wakes were typed into it, where nobody read them. Crius's own tab stuck for 10 min pushes the owner, never a line
// typed into that tab; again every 30 min while it lasts.
func TestCriusOwnTabStuckTenMinPushesTheOwner(t *testing.T) {
	const crius = "surface:1000070000"
	for _, c := range []struct {
		name string
		tab  *Screen
		dead bool
		want string
	}{
		{"prompt", &Screen{Surface: crius, Title: "crius", Text: permission("crius", "date +%Y-%m-%d", "")}, false,
			"Crius's own tab (" + crius + ") has a prompt open: date +%Y-%m-%d Run the check: stuck 10 min"},
		{"no input box", &Screen{Surface: crius, Title: "crius", Text: "⏺ Reading the bus.\n  ⎿  (no output)\n"}, false,
			"(" + crius + ") shows no input box and no working line: stuck 10 min"},
		{"no live Claude", &Screen{Surface: crius, Title: "crius", Text: idleTab("crius")}, true,
			"(" + crius + ") has no live Claude (runs \"-zsh\" in front, not Claude): stuck 10 min"},
		{"unreadable", &Screen{Surface: crius, Title: "crius", Err: "cmux read-screen: timed out"}, false,
			"(" + crius + ") cannot be read (cmux read-screen: timed out): stuck 10 min"},
		{"no tab", nil, false, "Crius's own tab is not found (no cmux tab titled crius): stuck 10 min"},
	} {
		home := promptHome(t)
		f := &promptFake{tabs: []Screen{opsIdle()}, dead: map[string]bool{crius: c.dead}}
		if c.tab != nil {
			f.tabs = append(f.tabs, *c.tab)
		}
		for min := 0; min <= 9; min++ {
			f.run(t, home, min)
		}
		if len(f.pushes) != 0 {
			t.Fatalf("%s: pushed before 10 min: %q", c.name, f.pushes)
		}
		f.run(t, home, 10)
		if len(f.pushes) != 1 || !strings.Contains(f.pushes[0], c.want) {
			t.Fatalf("%s: want one push %q, got %q", c.name, c.want, f.pushes)
		}
		if c.tab != nil && !strings.HasPrefix(f.pushes[0], crius+" Crius is stuck: ") {
			t.Fatalf("%s: the push does not point at Crius's tab: %q", c.name, f.pushes[0])
		}
		for _, l := range f.crius {
			if strings.Contains(l, crius) {
				t.Fatalf("%s: typed into Crius's own tab: %q", c.name, f.crius)
			}
		}
		for min := 11; min < 40; min++ {
			f.run(t, home, min)
		}
		if len(f.pushes) != 1 {
			t.Fatalf("%s: pushed again inside 30 min: %q", c.name, f.pushes)
		}
		f.run(t, home, 40)
		if len(f.pushes) != 2 {
			t.Fatalf("%s: not pushed again after 30 min: %q", c.name, f.pushes)
		}
	}
}

// Crius's tab is found by its title: another tab's dialog quoted on Crius's screen names that tab on its border, and an
// idle Crius was pushed "not found" (review 2, P2-1).
func TestCriusOwnTabIsFoundByTitle(t *testing.T) {
	const crius = "surface:1000070000"
	home := promptHome(t)
	quoted := "⏺ Read the lead's tab:\n  ⎿  Pick one:\n     1. Yes\n     ──── quotes · lead ────\n     ❯\n" + idleTab("crius")
	f := &promptFake{tabs: []Screen{{Surface: crius, Title: "crius", Text: quoted}, opsIdle()}}
	for min := 0; min <= 15; min++ {
		f.run(t, home, min)
	}
	if len(f.pushes) != 0 {
		t.Fatalf("pushed for an idle Crius: %q", f.pushes)
	}
}

// Working is no freeze, with the box hidden too (the transcript view; today's working line has no "esc to interrupt":
// review P2-1), and neither is an idle input box. A freeze that ends and comes back is timed afresh; one that changes
// form (a prompt, a failed liveness read, the next prompt) is timed on (review P3-2).
func TestCriusOwnTabWorkingOrIdleIsNoFreeze(t *testing.T) {
	const crius = "surface:1000070000"
	home := promptHome(t)
	f := &promptFake{tabs: []Screen{{Surface: crius, Title: "crius", Text: "⏺ Bash(go test ./...)\n\n✻ Burrowing… (9m 43s · ↓ 17.1k tokens)\n"}, opsIdle()}}
	for min := 0; min <= 15; min++ {
		f.run(t, home, min)
	}
	f.tabs[0].Text = idleTab("crius")
	for min := 16; min <= 30; min++ {
		f.run(t, home, min)
	}
	if len(f.pushes) != 0 {
		t.Fatalf("pushed for a working or idle tab: %q", f.pushes)
	}
	f.tabs[0].Text = permission("crius", "git push", "")
	for min := 31; min <= 40; min++ {
		if min == 34 {
			f.live = map[string]error{crius: errors.New("cmux tree: no answer in 10s")}
		}
		if min == 35 {
			f.live = nil
			f.tabs[0].Text = permission("crius", "git push --tags", "")
		}
		f.run(t, home, min)
	}
	if len(f.pushes) != 0 {
		t.Fatalf("a prompt from 31 pushed before 41: %q", f.pushes)
	}
	f.run(t, home, 41)
	if len(f.pushes) != 1 || !strings.Contains(f.pushes[0], "git push --tags Run the check: stuck 10 min") {
		t.Fatalf("want one push at 41: %q", f.pushes)
	}
}

// A push that fails becomes a NEED-OWNER line (a row in the owner's pane; an ALERT would reach only the stuck Crius:
// review P2-2), and the other tabs' wakes of that run still go out (P2-3).
func TestAFailedPushIsAnOwnerRow(t *testing.T) {
	const crius = "surface:1000070000"
	home := promptHome(t)
	f := &promptFake{tabs: []Screen{{Surface: crius, Title: "crius", Text: permission("crius", "git push", "")}, opsIdle()},
		pushErr: errors.New("cmux notify: exit status 1")}
	for min := 0; min <= 10; min++ {
		f.run(t, home, min)
	}
	b, _ := os.ReadFile(filepath.Join(home, "bus", "crius.log"))
	if !strings.Contains(string(b), " NEED-OWNER crius:watchdog -> owner — desktop push failed (cmux notify: exit status 1): Crius's own tab ("+crius+") has a prompt open: git push Run the check: stuck 10 min; only you can free it") {
		t.Fatalf("bus %q", b)
	}
	for min := 11; min <= 75; min++ { // pushes at 40 and 70 fail too: still one row for the spell
		f.run(t, home, min)
	}
	b, _ = os.ReadFile(filepath.Join(home, "bus", "crius.log"))
	if n := strings.Count(string(b), " NEED-OWNER "); n != 1 || len(f.pushes) != 3 {
		t.Fatalf("want 1 row for 3 failed pushes, got %d rows, %d pushes", n, len(f.pushes))
	}
	f.tabs[0].Text = idleTab("crius") // freed: the next spell opens a row again
	f.run(t, home, 76)
	f.tabs[0].Text = permission("crius", "crius-bus-post DECISION crius:lead -> quotes:lead — [queue q3] owner: go", "")
	for min := 77; min <= 87; min++ {
		f.run(t, home, min)
	}
	b, _ = os.ReadFile(filepath.Join(home, "bus", "crius.log"))
	if n := strings.Count(string(b), " NEED-OWNER "); n != 2 || strings.Contains(string(b), "[queue q3]") || !strings.Contains(string(b), "(queue q3) owner: go") {
		t.Fatalf("want a second row with the queue tag defused: %q", b)
	}

	home = promptHome(t)
	build := Screen{Surface: buildSurface, Title: "crius · build", Text: permission("crius · build", "go vet ./...", "")}
	f = &promptFake{tabs: []Screen{{Surface: crius, Title: "crius", Text: permission("crius", "git push", "")}, opsIdle()},
		pushErr: errors.New("cmux notify: exit status 1"), allowErr: true}
	for min := 0; min <= 6; min++ {
		f.run(t, home, min)
	}
	f.tabs = append(f.tabs, build)
	for min := 7; min <= 9; min++ {
		f.run(t, home, min)
	}
	bus := filepath.Join(home, "bus", "crius.log") // the NEED-OWNER line cannot be written
	if err := os.WriteFile(bus, nil, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(bus, 0o400); err != nil {
		t.Fatal(err)
	}
	f.run(t, home, 10)
	if f.err == nil || !strings.Contains(f.err.Error(), "the NEED-OWNER line was not posted") {
		t.Fatalf("want the failed post said, got %v", f.err)
	}
	if len(f.crius) != 1 || !strings.Contains(f.crius[0], "crius · build ("+buildSurface+") has had a permission prompt open for 3 min") {
		t.Fatalf("the build tab's wake was dropped: %q", f.crius)
	}
}
