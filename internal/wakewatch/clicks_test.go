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
