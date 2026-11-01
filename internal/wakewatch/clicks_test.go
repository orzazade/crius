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

func screenFile(t *testing.T, name string) string {
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Owner 11-01 19:1x: Quotes tabs waited on his click unseen. Today's screens: the post-guard's ask on quotes · frontend
// (az repos pr create, 18:2x) and on quotes · lead (a script it cannot read), and the Azure DevOps MCP ask on
// quotes · qa-frontend (a PR comment, live 19:2x).
func TestOwnerClickReadsTodaysPrompts(t *testing.T) {
	for _, c := range []struct{ file, what string }{
		{"owner-click-hook-1103.screen", `post-guard ask (az repos pr create --description); PR "feat(web): demo web app shell with runtime config, sign-in and health page"`},
		{"owner-click-hook-lead.screen", `post-guard ask (a shell script name the guard cannot read)`},
		{"owner-click-mcp-1105.screen", `Azure DevOps tool Repo Create Pull Request Thread; PR 7617 in svc_demo`},
	} {
		prompt, what, ok := OwnerClick(screenFile(t, c.file))
		if !ok || what != c.what || prompt == "" {
			t.Errorf("%s: %v %q", c.file, ok, what)
		}
	}
	hook := screenFile(t, "owner-click-hook-1103.screen")
	for name, text := range map[string]string{
		"a lead's own ask":          screenFile(t, "marketplace-asks-1458.screen"),
		"another dialog":            strings.ReplaceAll(hook, "needs the owner's click", "needs a look"),
		"answered, the box is back": hook + "\n⏺ PR 7612 opened.\n─────── quotes · frontend ─\n❯ \n───────\n",
		// review of the owner-click watch: a dialog that only quotes the words is no ask
		"a command quoting the reason": strings.Replace(hook, "Hook PreToolUse:Bash requires confirmation for this command:", "echo", 1),
		"an edit naming an ADO tool":   "───────\n Edit file\n │ \"allow\": [\"mcp__azure-devops__wit_get_work_item\"]\n Do you want to make this edit to settings.json?\n ❯ 1. Yes\n   2. No\n─── crius · build ─\n",
		// review r2 of X5: the idle input box is "❯" and a no-break space; a numbered draft in it is no dialog
		"a quoted ask and a numbered draft": "⏺ It said: Hook PreToolUse:Bash requires confirmation for this command: Posts text to Azure DevOps under the owner's name: needs the owner's click.\n─── quotes · frontend ─\n❯\u00a01. retry the post\n───────\n",
	} {
		if _, what, ok := OwnerClick(text); ok {
			t.Errorf("%s: flagged as %q", name, what)
		}
	}
	// the same prompt with the cursor on another option, or wrapped at another width, is the same prompt
	p1, _, _ := OwnerClick(hook)
	for name, text := range map[string]string{
		"cursor on 2": strings.Replace(hook, " ❯ 1. Yes\n   2. No", "   1. Yes\n ❯ 2. No", 1),
		"rewrapped":   strings.Replace(hook, "demo-channel\n │ s/00000000", "demo-chann\n │ els/00000000", 1),
	} {
		if text == hook {
			t.Fatalf("%s: the fixture did not change", name)
		}
		p2, _, ok := OwnerClick(text)
		if !ok || promptKey(p1) != promptKey(p2) {
			t.Errorf("%s: %v %q", name, ok, p2)
		}
	}
}

func clickLines(t *testing.T, home string) []string {
	var out []string
	for _, l := range strings.Split(testutil.Read(t, filepath.Join(home, "bus", "crius.log")), "\n") {
		if strings.Contains(l, " OWNER-CLICK ") {
			out = append(out, l)
		}
	}
	return out
}

func TestAnOwnerClickIsPostedAtOnceThenEvery30MinWhileItWaits(t *testing.T) {
	home := setup(t, nil)
	hook := Screen{Surface: "surface:1000050103", Title: "quotes · frontend", Text: screenFile(t, "owner-click-hook-1103.screen")}
	var tabs []Screen
	run := func(min int) {
		t.Helper()
		if err := Clicks(ClickDeps{Home: home, Now: t0.Add(time.Duration(min) * time.Minute), Screens: func() ([]Screen, error) { return tabs, nil }}); err != nil {
			t.Fatal(err)
		}
	}
	tabs = []Screen{hook, {Surface: "surface:1", Title: "quotes · lead", Text: "⏺ working\n─── quotes · lead ─\n❯ \n───\n"}}
	run(0)
	got := clickLines(t, home)
	if len(got) != 1 || !strings.Contains(got[0], " OWNER-CLICK crius:wakewatch -> crius:lead — quotes · frontend (surface:1000050103) waits on your click: post-guard ask (az repos pr create --description); PR \"feat(web)") {
		t.Fatal(got)
	}
	run(1)
	tabs[0].Text = strings.Replace(hook.Text, "demo-channel\n │ s/00000000", "demo-chann\n │ els/00000000", 1) // the pane got narrower
	run(2)
	// review round 2: the dialog's ╌ lines are as long as the pane is wide
	tabs[0].Text = strings.ReplaceAll(hook.Text, strings.Repeat("╌", 80), strings.Repeat("╌", 70))
	if tabs[0].Text == hook.Text {
		t.Fatal("the fixture has no ╌ line of 80")
	}
	run(3)
	tabs[0] = hook
	run(29)
	if got = clickLines(t, home); len(got) != 1 {
		t.Fatal("posted again within 30 min:", got)
	}
	run(30)
	if got = clickLines(t, home); len(got) != 2 || !strings.Contains(got[1], "waits on your click since "+t0.Format("15:04")+": post-guard ask") {
		t.Fatal(got)
	}
	tabs = []Screen{{Surface: hook.Surface, Title: hook.Title, Err: "cmux read-screen: timeout"}}
	run(31)
	tabs = []Screen{hook}
	run(32)
	if got = clickLines(t, home); len(got) != 2 {
		t.Fatal("a tab read again after a failed read posted again:", got)
	}
	tabs = []Screen{{Surface: hook.Surface, Title: hook.Title, Text: screenFile(t, "owner-click-mcp-1105.screen")}} // the next prompt
	run(32)
	if got = clickLines(t, home); len(got) != 3 || !strings.Contains(got[2], "quotes · qa-frontend (surface:1000050103) waits on your click: Azure DevOps tool Repo Create Pull Request Thread; PR 7617 in svc_demo") {
		t.Fatal(got)
	}
	tabs = nil // clicked: the tab shows no prompt
	run(33)
	tabs = []Screen{hook}
	run(34)
	if got = clickLines(t, home); len(got) != 4 || strings.Contains(got[3], "since") {
		t.Fatal("a prompt that came back is new:", got)
	}
}

func linesOf(t *testing.T, home, typ string) []string {
	var out []string
	for _, l := range strings.Split(testutil.Read(t, filepath.Join(home, "bus", "crius.log")), "\n") {
		if strings.Contains(l, " "+typ+" ") {
			out = append(out, l)
		}
	}
	return out
}

// Owner 11-01 22:4x: Crius answers the quotes tabs' Azure DevOps prompts, not the owner (AGENTS.md 1205fdc). A project
// whose projects.toml says clicks = "crius" gets CRIUS-CLICK, which Crius answers with no owner push; other projects
// keep OWNER-CLICK.
func TestAProjectWhoseClicksAreCriussGetsCriusClick(t *testing.T) {
	home := setup(t, nil)
	testutil.Write(t, filepath.Join(home, "projects.toml"), "[projects.quotes]\nclicks = \"crius\"\n[projects.tamada]\nname = \"Tamada\"\n")
	hook := screenFile(t, "owner-click-hook-1103.screen")
	quotes := Screen{Surface: "surface:1000050103", Title: "quotes · frontend", Text: hook}
	// another project's tab: its dialog border names its own session
	tamada := Screen{Surface: "surface:1000050094", Title: "tamada · lead", Text: strings.ReplaceAll(hook, "quotes · frontend", "tamada · lead")}
	if tamada.Text == hook {
		t.Fatal("the fixture names no quotes · frontend")
	}
	run := func(min int) error {
		return Clicks(ClickDeps{Home: home, Now: t0.Add(time.Duration(min) * time.Minute), Screens: func() ([]Screen, error) { return []Screen{quotes, tamada}, nil }})
	}
	if err := run(0); err != nil {
		t.Fatal(err)
	}
	crius, owner := linesOf(t, home, "CRIUS-CLICK"), linesOf(t, home, "OWNER-CLICK")
	if len(crius) != 1 || !strings.Contains(crius[0], " CRIUS-CLICK crius:wakewatch -> crius:lead — quotes · frontend (surface:1000050103) waits on Crius's click: post-guard ask (az repos pr create --description)") {
		t.Fatal("quotes:", crius)
	}
	if len(owner) != 1 || !strings.Contains(owner[0], "tamada · lead (surface:1000050094) waits on your click: ") {
		t.Fatal("tamada:", owner)
	}
	// it repeats every 30 min like OWNER-CLICK, and the waiter and wakewatch see it as flagged
	if err := run(30); err != nil {
		t.Fatal(err)
	}
	if got := linesOf(t, home, "CRIUS-CLICK"); len(got) != 2 || !strings.Contains(got[1], "waits on Crius's click since ") {
		t.Fatal(got)
	}
	if !ClickFlagged(home, t0.Add(30*time.Minute), quotes.Surface, func() (string, error) { return hook, nil }) {
		t.Error("a posted CRIUS-CLICK is not flagged")
	}
	// a projects.toml that cannot be read sends every click to the owner, and the run fails (one ALERT)
	home = setup(t, nil)
	testutil.Write(t, filepath.Join(home, "projects.toml"), "[projects.quotes\nclicks = \"crius\"\n")
	err := run(0)
	if err == nil || !strings.Contains(err.Error(), "projects.toml") {
		t.Fatal("no error for an unreadable projects.toml:", err)
	}
	if got := linesOf(t, home, "OWNER-CLICK"); len(got) != 2 || len(linesOf(t, home, "CRIUS-CLICK")) != 0 {
		t.Fatal("with projects.toml unread, the clicks did not all go to the owner:", got)
	}
	if got := linesOf(t, home, "ALERT"); len(got) != 1 || !strings.Contains(got[0], "projects.toml") {
		t.Fatal(got)
	}
	// review r2: a bus that cannot be opened keeps the projects.toml error beside its own
	home = setup(t, nil)
	testutil.Write(t, filepath.Join(home, "projects.toml"), "[projects.quotes\n")
	if err := os.Chmod(filepath.Join(home, "bus", "crius.log"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := run(0); err == nil || !strings.Contains(err.Error(), "projects.toml") || !strings.Contains(err.Error(), "crius.log") {
		t.Fatal(err)
	}
}

// Review of d6cb914: only the border right under the dialog names its session. An unnamed session (a plain border) whose
// history shows a quotes border is the owner's; a colon name is a quotes name too; a title with no role names no project.
func TestOnlyTheDialogsOwnBorderNamesItsProject(t *testing.T) {
	hook := screenFile(t, "owner-click-hook-1103.screen")
	border := strings.Repeat("─", 63) + " quotes · frontend ─"
	if !strings.Contains(hook, border) {
		t.Fatal("the fixture has no quotes border")
	}
	unnamed := border + "\n" + strings.Replace(hook, border, strings.Repeat("─", 83), 1) // the quotes border is history now
	for name, c := range map[string]struct {
		tab  Screen
		want string
	}{
		"an unnamed session with a quotes border in its history": {Screen{Surface: "surface:1", Title: "scratch", Text: unnamed}, " OWNER-CLICK crius:wakewatch -> crius:lead — scratch (surface:1) waits on your click: "},
		"a colon name":         {Screen{Surface: "surface:2", Title: "x", Text: strings.Replace(hook, " quotes · frontend ", " quotes: frontend ", 1)}, " CRIUS-CLICK crius:wakewatch -> crius:lead — quotes: frontend (surface:2) waits on Crius's click: "},
		"a title with no role": {Screen{Surface: "surface:3", Title: "quotes", Text: unnamed}, " OWNER-CLICK crius:wakewatch -> crius:lead — quotes (surface:3) waits on your click: "},
		"a named border below the dialog's plain one": {Screen{Surface: "surface:5", Title: "scratch", Text: unnamed + border + "\n"}, " OWNER-CLICK crius:wakewatch -> crius:lead — scratch (surface:5) waits on your click: "},
		// review r2: the tab title is the one name the session does not draw itself, so it never gives CRIUS-CLICK
		"an unnamed session in a quotes tab": {Screen{Surface: "surface:4", Title: "quotes · qa-frontend", Text: unnamed}, " OWNER-CLICK crius:wakewatch -> crius:lead — quotes · qa-frontend (surface:4) waits on your click: "},
	} {
		home := setup(t, nil)
		testutil.Write(t, filepath.Join(home, "projects.toml"), "[projects.quotes]\nclicks = \"crius\"\n")
		if err := Clicks(ClickDeps{Home: home, Now: t0, Screens: func() ([]Screen, error) { return []Screen{c.tab}, nil }}); err != nil {
			t.Fatal(err)
		}
		if got := testutil.Read(t, filepath.Join(home, "bus", "crius.log")); !strings.Contains(got, c.want) {
			t.Errorf("%s: %s", name, got)
		}
	}
}

func TestAnUnreadableOwnerClickStateAlertsAndStartsAfresh(t *testing.T) {
	home := setup(t, nil)
	testutil.Write(t, filepath.Join(home, "state", "owner-clicks.json"), "{")
	tabs := []Screen{{Surface: "surface:1000050103", Title: "quotes · frontend", Text: screenFile(t, "owner-click-hook-1103.screen")}}
	if err := Clicks(ClickDeps{Home: home, Now: t0, Screens: func() ([]Screen, error) { return tabs, nil }}); err != nil {
		t.Fatal(err)
	}
	bus := testutil.Read(t, filepath.Join(home, "bus", "crius.log"))
	if !strings.Contains(bus, "ALERT crius:watchdog -> crius — owner-click state") || len(clickLines(t, home)) != 1 {
		t.Fatal(bus)
	}
}

// Review of the owner-click watch: a run that fails is said once on the bus (its log is read by nobody), a bus that ends
// mid-line gets its own line, and an unreadable state is said once even while the screens cannot be read.
func TestAFailingOwnerClickRunAlertsOnceAndABrokenBusLineIsKeptApart(t *testing.T) {
	home := setup(t, nil)
	bus := filepath.Join(home, "bus", "crius.log")
	testutil.Write(t, filepath.Join(home, "state", "owner-clicks.json"), "{")
	// review round 2: cmux that prints nothing gives an error that ends in a space ("cmux tree: signal: killed: ")
	broken := func() ([]Screen, error) { return nil, errors.New("cmux tree: signal: killed: ") }
	for i := 0; i < 3; i++ {
		if err := Clicks(ClickDeps{Home: home, Now: t0.Add(time.Duration(i) * time.Minute), Screens: broken}); err == nil {
			t.Fatal("a failed screen read passed")
		}
	}
	got := testutil.Read(t, bus)
	if strings.Count(got, "ALERT crius:watchdog -> crius — owner-click watch failed: ") != 1 ||
		strings.Count(got, "owner-click state") != 1 {
		t.Fatal(got)
	}
	testutil.Write(t, bus, got+"2026-10-21T22:01 INFO quotes:lead -> crius:lead — cut off mid-li")
	tabs := []Screen{{Surface: "surface:1000050103", Title: "quotes · frontend", Text: screenFile(t, "owner-click-hook-1103.screen")}}
	if err := Clicks(ClickDeps{Home: home, Now: t0.Add(2 * time.Minute), Screens: func() ([]Screen, error) { return tabs, nil }}); err != nil {
		t.Fatal(err)
	}
	if l := clickLines(t, home); len(l) != 1 || !strings.HasPrefix(l[0], stamp(t0.Add(2*time.Minute))+" OWNER-CLICK ") {
		t.Fatal(l)
	}
	if _, err := os.Stat(filepath.Join(home, "state", "owner-clicks.error")); !os.IsNotExist(err) {
		t.Fatal("a run that worked kept the error:", err)
	}
}

// Crius 11-01 20:02: 9 repeat wakes 19:27-20:00 for 4 known Azure DevOps clicks. Once a prompt is flagged OWNER-CLICK, the
// dialog alert leaves it to OWNER-CLICK's own repeat; a prompt not flagged yet, or another dialog in that tab, alerts.
// Review X4: the tab is read again as deep as the clicks run reads it, so a dialog taller than the 30 lines of the main
// run is the same prompt; a state the clicks job stopped rewriting (older than 3 min) flags nothing.
func TestAFlaggedOwnerClickDrawsNoDialogAlert(t *testing.T) {
	hook := screenFile(t, "owner-click-hook-1103.screen")
	tall := strings.Replace(hook, " │ https://dev.azure.com", strings.Repeat(" │ --debug\n", 20)+" │ https://dev.azure.com", 1)
	lines := strings.Split(tall, "\n")
	tail := strings.Join(lines[len(lines)-30:], "\n") // what the main run's 30 lines show of it
	other := strings.ReplaceAll(hook, "needs the owner's click", "needs a look")
	mcp := screenFile(t, "owner-click-mcp-1105.screen")
	for name, c := range map[string]struct {
		text, deep string // the tab at 30 lines and at 80
		posted     string // what the clicks job saw at 80 lines and posted; "": it ran on no tab
		stopped    bool   // and then stopped rewriting its state
		busFails   bool   // its bus append failed
		alerts     int
	}{
		"flagged":     {hook, hook, hook, false, false, 0},
		"not flagged": {hook, hook, "", false, false, 1},
		// the clicks job has not seen it yet (it runs once a minute) or could not read the tab
		"another dialog in a flagged tab":             {other, other, hook, false, false, 1},
		"another owner-click dialog in a flagged tab": {mcp, mcp, hook, false, false, 1},
		"a tall flagged dialog":                       {tail, tall, tall, false, false, 0},
		"the clicks job stopped":                      {hook, hook, hook, true, false, 1},
		"the post did not reach the bus":              {hook, hook, hook, false, true, 1},
	} {
		home := setup(t, map[string]string{"quotes.log": stamp(t0) + " INFO quotes:frontend -> quotes:lead — on it\n"})
		if c.busFails {
			testutil.Write(t, filepath.Join(home, "bus", "crius.log"), "")
			if err := os.Chmod(filepath.Join(home, "bus", "crius.log"), 0o400); err != nil {
				t.Fatal(err)
			}
		}
		clicksAt := func(at time.Time) {
			if c.posted == "" {
				return
			}
			flaggedScreen := []Screen{screen("quotes · frontend", c.posted)}
			if err := Clicks(ClickDeps{Home: home, Now: at, Screens: func() ([]Screen, error) { return flaggedScreen, nil }}); err != nil && !c.busFails {
				t.Fatal(err)
			}
			if err := os.Chtimes(filepath.Join(home, "state", "owner-clicks.json"), at, at); err != nil {
				t.Fatal(err)
			}
		}
		clicksAt(t0)
		f := &fake{deep: map[string]string{"surface:7": c.deep}}
		runAt(t, home, f, func(at time.Time) []Screen {
			if !c.stopped {
				clicksAt(at)
			}
			return []Screen{screen("quotes · frontend", c.text)}
		}, 0, 5, 11, 15)
		n := 0
		for _, m := range f.sent {
			if strings.Contains(m, "waits on a question or dialog") {
				n++
			}
		}
		if n != c.alerts {
			t.Errorf("%s: %d dialog alerts, want %d: %q", name, n, c.alerts, f.sent)
		}
	}
}
