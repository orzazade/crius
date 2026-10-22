package launch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const tree = `window window:1 [current] ◀ active
├── workspace workspace:1 "Crius" [selected] ◀ active
│   └── pane pane:1 [focused] ◀ active
│       └── surface surface:10 [terminal] "crius" tty=ttys015
├── workspace workspace:2 "Quotes"
│   └── pane pane:2 [focused]
│       ├── surface surface:20 [terminal] "quotes" [selected] tty=ttys008
│       └── surface surface:21 [terminal] "quotes: rig" tty=ttys000
└── workspace workspace:3 "Quotes archive"
    └── pane pane:3 [focused]
        └── surface surface:30 [terminal] "old" tty=ttys001
`

// fake is cmux with one new Claude tab: the screens it draws before the input box (boot: shells, dialogs), then the
// box with what was typed and the status bar's model, then work once a prompt is sent.
type fake struct {
	calls   []string
	trees   []string // answers to "tree --all", in order; the last one repeats
	list    string   // answer to "workspace list"
	persona string   // from projects.toml; "" means work
	boot    []string // screens before Claude is up, one per read; a dialog is drawn from dialog instead
	dialog  []string // options of an open dialog, cursor at pick
	pick    int
	model   string // status bar model; "" means the one the command asked for
	cmd     string
	draft   string
	sent    bool
	noWork  bool   // the prompt is sent but the screen never shows work
	fast    bool   // the prompt is answered before a read can see work, as live 10-22 ("ready" in 5 s)
	apiErr  string // the line under an emptied box after the first turn, as "⎿  API Error: 529 Overloaded"
	asks    string // the dialog question; "" means the folder-trust one
	linger  int    // reads an answered dialog stays drawn, cursor where it was
	shown   []string
	tip     string // a line under the banner
	exited  bool   // the dialog was answered No
}

const statusBar = "  %s │ main │ ██░░░░░░░░ ctx 2%%\n"

func (f *fake) deps() Deps {
	return Deps{
		Lock: filepath.Join(os.TempDir(), "crius-launch-test.lock"),
		Cmux: func(args ...string) (string, error) {
			f.calls = append(f.calls, strings.Join(args, " "))
			switch args[0] {
			case "tree":
				t := f.trees[0]
				if len(f.trees) > 1 {
					f.trees = f.trees[1:]
				}
				return t, nil
			case "workspace":
				return f.list, nil
			case "new-surface":
				f.cmd = args[len(args)-1]
				return "OK surface:99 pane:2 workspace:2", nil
			case "new-workspace":
				f.cmd = args[len(args)-1]
				return "OK workspace:9", nil
			case "send":
				// As live 10-22: cmux send refuses a box that holds text, unless --force.
				if f.draft != "" && !slices.Contains(args, "--force") {
					return "", errors.New("the agent prompt holds text someone is typing, so nothing was sent")
				}
				f.draft += args[len(args)-1]
			case "send-key":
				switch key := args[len(args)-1]; {
				case key == "down" && f.dialog != nil:
					f.pick++
				case key == "enter" && f.dialog != nil:
					f.exited = !strings.HasPrefix(f.dialog[f.pick], "Yes")
					f.shown, f.dialog = f.dialog, nil
				case key == "enter" && strings.HasSuffix(f.draft, `\`):
					f.draft = strings.TrimSuffix(f.draft, `\`) + "\n"
				case key == "enter" && f.draft != "":
					f.sent = true
				}
			case "read-screen":
				lines := strings.Split(f.screen(), "\n")
				if i := slices.Index(args, "--lines"); i >= 0 {
					n, _ := strconv.Atoi(args[i+1])
					lines = lines[max(0, len(lines)-n):]
				}
				return strings.Join(lines, "\n"), nil
			}
			return "OK", nil
		},
		Place: func(project string) (string, string, string, error) {
			if project == "falcon" {
				return "", "", "", errors.New("projects.toml has no project falcon")
			}
			if f.persona == "" {
				return "/Users/o/Projects/" + project, "max", "work", nil
			}
			return "/Users/o/Projects/" + project, "max", f.persona, nil
		},
	}
}

func (f *fake) screen() string {
	switch {
	case f.exited:
		return "o@mac openrig % \n"
	case f.dialog != nil || f.linger > 0 && f.shown != nil:
		opts := f.dialog
		if opts == nil {
			f.linger--
			opts = f.shown
		}
		q := f.asks
		if q == "" {
			q = "Do you trust the files in this folder?"
		}
		// As live 10-22: the options carry no numbers.
		s := " " + q + "\n Security guide\n\n"
		for i, o := range opts {
			mark := "  "
			if i == f.pick {
				mark = "❯ "
			}
			s += fmt.Sprintf(" %s%s\n", mark, o)
		}
		return s + "\n Enter to confirm · Esc to cancel\n"
	case len(f.boot) > 0:
		s := f.boot[0]
		f.boot = f.boot[1:]
		return s
	}
	model := f.model
	if model == "" && strings.Contains(f.cmd, "claude-sonnet-5-5") {
		model = "Sonnet 5.5"
	} else if model == "" {
		model = "Opus 5.5"
	}
	// As live 10-22: the banner names the model; the status bar does only after the first reply.
	body := " ▐▛███▜▌   Claude Code v2.1.308\n▝▜█████▛▘  " + model + " · Claude Max\n  ▘▘ ▝▝    ~/Projects/openrig\n\n"
	if f.tip != "" {
		body += f.tip + "\n\n"
	}
	bar := "  quota: no response yet\n"
	if f.sent {
		bar = fmt.Sprintf(statusBar, model)
	}
	if f.sent && f.apiErr != "" {
		body += "╭──────────╮\n│ " + strings.ReplaceAll(f.draft, "\n", "\n│ ") + "\n╰──────────╯\n  " + f.apiErr + "\n\n"
	} else if f.sent && f.fast {
		body += "╭──────────╮\n│ " + strings.ReplaceAll(f.draft, "\n", "\n│ ") + "\n╰──────────╯\n\n✦ ready\n\n── Baked for 5s · 13:56 · $0.10 ──\n" + strings.Repeat("\n", 40) // live 10-22: a 54-line tab, the reply at its top
	} else if f.sent && !f.noWork {
		body += "❯ " + f.draft + "\n\n✻ Reading the brief… (12s · esc to interrupt)\n\n"
	}
	draft := f.draft
	if f.sent {
		draft = ""
	}
	return body + "──────────────────────────────── openrig · lead ─\n❯ " + strings.ReplaceAll(draft, "\n", "\n  ") +
		"\n────────────────────────────────\n" + bar
}

func (f *fake) called(prefix string) []string {
	var out []string
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

// Crius 10-21 23:07: cortex:lead and crius:build were launched by hand and broke the one-workspace-per-project rule.
func TestLaunchingIntoAnExistingProjectAddsATabNeverAWorkspace(t *testing.T) {
	f := &fake{trees: []string{tree}}
	ws, surface, tag, err := Run(Spawn{Project: "quotes", Role: "qa-api", Account: "", Prompt: "Read your brief."}, f.deps())
	if err != nil {
		t.Fatal(err)
	}
	if ws != "workspace:2" || surface != "surface:99" {
		t.Fatal(ws, surface)
	}
	if n := f.called("new-workspace"); len(n) != 0 {
		t.Fatal("made a workspace:", n)
	}
	ns := f.called("new-surface")
	if len(ns) != 1 || !strings.Contains(ns[0], "--workspace workspace:2 --pane pane:2 ") ||
		!strings.Contains(ns[0], "--working-directory /Users/o/Projects/quotes ") {
		t.Fatal(ns)
	}
	if r := f.called("rename-tab"); len(r) != 1 || r[0] != "rename-tab --workspace workspace:2 --surface surface:99 -- quotes · qa-api" {
		t.Fatal(r)
	}
	if c := f.called("workspace-action"); len(c) != 1 || !strings.Contains(c[0], "--workspace workspace:2 --action set-color --color Blue") {
		t.Fatal(c)
	}
	if tag != "[crius account=max project=quotes role=qa-api]" {
		t.Fatal(tag)
	}
}

func TestAProjectWithNoWorkspaceGetsOneNamedAndColored(t *testing.T) {
	newTree := tree + `└── workspace workspace:9 "Cortex"
    └── pane pane:9 [focused]
        └── surface surface:90 [terminal] "zsh" tty=ttys005
`
	// Live run 10-22: the tree read just after new-workspace did not list it yet.
	f := &fake{trees: []string{tree, tree, tree, newTree}}
	ws, surface, _, err := Run(Spawn{Project: "cortex", Role: "lead", Account: "enterprise", Prompt: "Read your brief."}, f.deps())
	if err != nil {
		t.Fatal(err)
	}
	if ws != "workspace:9" || surface != "surface:90" {
		t.Fatal(ws, surface)
	}
	nw := f.called("new-workspace")
	if len(nw) != 1 || !strings.Contains(nw[0], "--name Cortex --cwd /Users/o/Projects/cortex ") || len(f.called("new-surface")) != 0 {
		t.Fatal(f.calls)
	}
	if c := f.called("workspace-action"); len(c) != 1 || !strings.Contains(c[0], "--workspace workspace:9 --action set-color --color Navy") {
		t.Fatal(c)
	}
	if r := f.called("rename-tab"); len(r) != 1 || !strings.Contains(r[0], "--surface surface:90 -- cortex · lead") {
		t.Fatal(r)
	}
}

func TestClaudeStartsThroughAitchWithTheRoleModelAndName(t *testing.T) {
	for role, model := range map[string]string{"lead": "claude-opus-5-5", "builder": "claude-opus-5-5",
		"qa-api": "claude-sonnet-5-5", "devops": "claude-sonnet-5-5", "security": "claude-opus-5-5"} {
		f := &fake{trees: []string{tree}}
		if _, _, _, err := Run(Spawn{Project: "quotes", Role: role, Account: "", Prompt: "Read your brief."}, f.deps()); err != nil {
			t.Fatal(err)
		}
		ns := f.called("new-surface")[0]
		want := "--command aitch launch claude -- --model " + model +
			` --permission-mode bypassPermissions --settings '{"autoCompactWindow":300000}' -n 'quotes · ` + role + `'`
		if !strings.HasSuffix(ns, want) {
			t.Fatalf("%s:\n got %s\nwant suffix %s", role, ns, want)
		}
	}
}

func TestBadInputIsRefusedBeforeCmuxIsTouched(t *testing.T) {
	for _, c := range []struct {
		s    Spawn
		want string
	}{
		{Spawn{Project: "falcon", Role: "lead"}, "no project falcon"},
		{Spawn{Project: "quotes", Role: "qa api"}, "bad role"},
		{Spawn{Project: "quotes", Role: "lead'; rm x"}, "bad role"},
		{Spawn{Project: "Open Rig", Role: "lead"}, "bad project"},
		{Spawn{Project: "quotes", Role: "lead", Account: "work"}, "bad account"},
		{Spawn{Project: "quotes", Role: "lead", Parent: "x y]"}, "bad parent"},
		{Spawn{Project: "quotes", Role: "lead", Item: "quotes-41"}, "bad item"},
		{Spawn{Project: "quotes", Role: "lead", Prompt: ""}, "no first prompt"},
		{Spawn{Project: "quotes", Role: "lead", Prompt: `read it\nthen go`}, "backslash"},
		{Spawn{Project: "quotes", Role: "lead", Prompt: "read it\nthen go"}, "control"},
		{Spawn{Project: "sideproj", Role: "lead", Prompt: "go"}, "persona"},
	} {
		f := &fake{trees: []string{tree}}
		if c.s.Prompt == "" && c.want != "no first prompt" {
			c.s.Prompt = "Read your brief."
		}
		if c.s.Project == "sideproj" {
			f.persona = "guest"
		}
		_, _, _, err := Run(c.s, f.deps())
		if err == nil || !strings.Contains(err.Error(), c.want) || len(f.calls) != 0 {
			t.Fatal(c, err, f.calls)
		}
	}
}

// Review of 91854cf, P2: two first launches for one project at once both saw no workspace and made two. The
// find-or-create step holds a file lock, so the second one finds the first one's workspace.
func TestTwoLaunchesAtOnceMakeOneWorkspace(t *testing.T) {
	var mu sync.Mutex
	made := 0
	lock := filepath.Join(t.TempDir(), "launch.lock")
	var wg sync.WaitGroup
	for _, role := range []string{"lead", "qa-api"} {
		f := &fake{persona: "personal"}
		d := f.deps()
		own := d.Cmux
		d.Lock = lock
		d.Cmux = func(args ...string) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			switch args[0] {
			case "tree":
				time.Sleep(50 * time.Millisecond) // cmux is slow; both first launches read the tree before either creates
				if made == 0 {
					return tree, nil
				}
				return tree + "├── workspace workspace:9 \"Falcon\"\n│   └── pane pane:9\n│       └── surface surface:90 [terminal] \"x\"\n", nil
			case "new-workspace":
				made++
			}
			return own(args...)
		}
		d.Place = func(string) (string, string, string, error) {
			return "/Users/o/Projects/falcon", "max", "personal", nil
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, _, err := Run(Spawn{Project: "falcon", Role: role, Prompt: "Read your brief."}, d); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if made != 1 {
		t.Fatalf("%d workspaces made, want 1", made)
	}
}

// ORDER 10-22 13:36 (third time): the openrig workspace opened with no name and no colour, because the launcher knew
// only the projects in its table. A project outside it gets its slug as the name and a colour of its group.
func TestAProjectOutsideTheTableGetsItsNameAndAColourOfItsGroup(t *testing.T) {
	newTree := tree + "└── workspace workspace:9 \"Openrig\"\n    └── pane pane:9\n        └── surface surface:90 [terminal] \"zsh\"\n"
	for persona, palette := range map[string][]string{"personal": {"Olive", "Amber", "Brown"}, "work": {"Indigo", "Teal"}} {
		f := &fake{trees: []string{tree, newTree}, persona: persona}
		if _, _, _, err := Run(Spawn{Project: "openrig", Role: "lead", Prompt: "Read your brief."}, f.deps()); err != nil {
			t.Fatal(err)
		}
		if nw := f.called("new-workspace"); len(nw) != 1 || !strings.Contains(nw[0], "--name Openrig ") {
			t.Fatal(f.calls)
		}
		c := f.called("workspace-action")
		if len(c) != 1 || !strings.Contains(c[0], "--workspace workspace:9 --action set-color --color ") {
			t.Fatal(persona, c)
		}
		if color := c[0][strings.LastIndex(c[0], " ")+1:]; !slices.Contains(palette, color) {
			t.Fatalf("%s project got %s, want one of %v", persona, color, palette)
		}
	}
}

// The owner named the openrig workspace "OpenRig" and coloured it by hand: a spawn finds it and leaves its colour, and
// colours it only when it has none.
func TestAnExistingWorkspaceIsFoundWhateverItsCaseAndKeepsItsColour(t *testing.T) {
	rig := tree + "└── workspace workspace:5 \"OpenRig\"\n    └── pane pane:5\n        └── surface surface:50 [terminal] \"zsh\"\n"
	for color, want := range map[string]int{"#7D6608": 0, "": 1} {
		f := &fake{trees: []string{rig}, persona: "personal",
			list: `{"window_ref":"window:1","workspaces":[{"ref":"workspace:5","title":"OpenRig","has_custom_title":true,"custom_color":"` + color + `"}]}`}
		ws, _, _, err := Run(Spawn{Project: "openrig", Role: "lead", Prompt: "Read your brief."}, f.deps())
		if err != nil || ws != "workspace:5" || len(f.called("new-workspace")) != 0 {
			t.Fatal(err, ws, f.calls)
		}
		if c := f.called("workspace-action"); len(c) != want {
			t.Fatalf("colour %q: %d set-color calls, want %d: %v", color, len(c), want, c)
		}
	}
}

// SKILL.md §5: on the "Do you trust this folder?" screen a bare enter picks "No, exit"; the Yes option is picked.
func TestATrustDialogIsAnsweredYes(t *testing.T) {
	for _, opts := range [][]string{{"No, exit", "Yes, I accept"}, {"Yes, proceed", "No, exit"}} {
		f := &fake{trees: []string{tree}, dialog: opts}
		if _, _, _, err := Run(Spawn{Project: "quotes", Role: "lead", Prompt: "Read your brief."}, f.deps()); err != nil {
			t.Fatal(opts, err, f.calls)
		}
		if f.exited || !f.sent {
			t.Fatal(opts, "dialog answered No or prompt never sent:", f.calls)
		}
	}
}

// SKILL.md §5: the first prompt's first line is the provenance tag alone on its line (the reader's tag pattern ends
// the line at "]"), then the prompt; it is typed only into an empty box and sent with enter.
func TestTheTagLineAndThePromptAreTypedAndSent(t *testing.T) {
	f := &fake{trees: []string{tree}}
	_, _, tag, err := Run(Spawn{Project: "quotes", Role: "qa-api", Parent: "00000000-0000-0000-0000-000000000001",
		Item: "quotes#41", Prompt: "You are qa-api. Read docs/build/briefs/qa-api.md and follow it."}, f.deps())
	if err != nil {
		t.Fatal(err)
	}
	want := "[crius account=max project=quotes role=qa-api parent=00000000-0000-0000-0000-000000000001 item=quotes#41]"
	if tag != want {
		t.Fatal(tag)
	}
	if f.draft != want+"\nYou are qa-api. Read docs/build/briefs/qa-api.md and follow it." || !f.sent {
		t.Fatalf("typed %q, sent %v", f.draft, f.sent)
	}
	for _, c := range f.called("send") {
		if !strings.Contains(c, "--workspace workspace:2 --surface surface:99 ") {
			t.Fatal("typed outside the new tab:", c)
		}
	}
}

// SKILL.md §5: check the status bar model too. A lead that came up on Sonnet is reported and given no prompt.
func TestAWrongModelOnTheStatusBarFails(t *testing.T) {
	f := &fake{trees: []string{tree}, model: "Sonnet 5.5"}
	_, _, _, err := Run(Spawn{Project: "quotes", Role: "lead", Prompt: "Read your brief."}, f.deps())
	if err == nil || !strings.Contains(err.Error(), "Sonnet 5.5") || !strings.Contains(err.Error(), "Opus 5.5") {
		t.Fatal(err)
	}
	if len(f.called("send ")) != 0 {
		t.Fatal("typed into a tab on the wrong model:", f.calls)
	}
}

// Silent success is the expensive bug: a tab that never shows Claude's box, or never shows work, fails loudly.
func TestASpawnThatNeverComesUpOrNeverWorksFails(t *testing.T) {
	f := &fake{trees: []string{tree}, dialog: []string{"No, exit"}}
	_, _, _, err := Run(Spawn{Project: "quotes", Role: "lead", Prompt: "Read your brief."}, f.deps())
	if err == nil || !strings.Contains(err.Error(), "no input box") || len(f.called("send ")) != 0 {
		t.Fatal(err, f.calls)
	}
	f = &fake{trees: []string{tree}, noWork: true}
	_, _, _, err = Run(Spawn{Project: "quotes", Role: "lead", Prompt: "Read your brief."}, f.deps())
	if err == nil || !strings.Contains(err.Error(), "esc to interrupt") {
		t.Fatal(err)
	}
}

// A launch with no lock file named is refused, not run unlocked.
func TestALaunchWithNoLockIsRefused(t *testing.T) {
	d := (&fake{trees: []string{tree}}).deps()
	d.Lock = ""
	if _, _, _, err := Run(Spawn{Project: "quotes", Role: "lead", Account: "", Prompt: "Read your brief."}, d); err == nil || !strings.Contains(err.Error(), "lock") {
		t.Fatal(err)
	}
}

// Live 10-22: a short prompt was answered in 5 s, so "esc to interrupt" never showed; the sent prompt above an empty
// box is the proof it was taken.
func TestAPromptAnsweredBeforeAReadStillCountsAsTaken(t *testing.T) {
	f := &fake{trees: []string{tree}, fast: true}
	if _, _, _, err := Run(Spawn{Project: "quotes", Role: "lead", Prompt: "Reply ready."}, f.deps()); err != nil {
		t.Fatal(err)
	}
}

// Review 10-22: only the folder-trust dialog is answered; Yes to any other (say, "use this API key?") could bill
// the wrong account, so spawn waits and fails loudly instead.
func TestOnlyTheFolderTrustDialogIsAnswered(t *testing.T) {
	f := &fake{trees: []string{tree}, asks: "Do you want to use this API key?", dialog: []string{"No (recommended)", "Yes"}}
	_, _, _, err := Run(Spawn{Project: "quotes", Role: "lead", Prompt: "Read your brief."}, f.deps())
	if err == nil || !strings.Contains(err.Error(), "no input box") || len(f.called("send-key")) != 0 {
		t.Fatal(err, f.calls)
	}
}

// Review 10-22: a trust dialog still drawn a moment after its Enter must not get a second Enter, which would land on
// whatever comes next.
func TestADialogIsAnsweredOnceEvenWhileItLingers(t *testing.T) {
	f := &fake{trees: []string{tree}, dialog: []string{"No, exit", "Yes, I trust this folder"}, linger: 2}
	if _, _, _, err := Run(Spawn{Project: "quotes", Role: "lead", Prompt: "Read your brief."}, f.deps()); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, c := range f.calls {
		if strings.HasPrefix(c, "send ") {
			break
		}
		if strings.HasPrefix(c, "send-key") {
			keys = append(keys, c[strings.LastIndex(c, " ")+1:])
		}
	}
	if strings.Join(keys, " ") != "down enter" {
		t.Fatal(keys)
	}
}

// Review 10-22: the model is read from the banner; a tip naming another model is not the session's model.
func TestAModelNamedOutsideTheBannerIsNotTheSessionModel(t *testing.T) {
	f := &fake{trees: []string{tree}, tip: "Tip: Opus 5.5 is now available, try /model"}
	if _, _, _, err := Run(Spawn{Project: "quotes", Role: "scout", Prompt: "Read your brief."}, f.deps()); err != nil {
		t.Fatal(err)
	}
}

// Review 10-22: a first turn that fails at once empties the box too; that is not a started session.
func TestAFirstTurnThatFailsAtOnceFails(t *testing.T) {
	f := &fake{trees: []string{tree}, apiErr: "⎿  API Error: 529 Overloaded"}
	_, _, _, err := Run(Spawn{Project: "quotes", Role: "lead", Prompt: "Read your brief."}, f.deps())
	if err == nil || !strings.Contains(err.Error(), "API Error: 529") {
		t.Fatal(err)
	}
}

// Review 10-22: a new workspace is coloured as soon as it exists, not after the tree shows its tab, so wakewatch never
// sees it uncoloured.
func TestANewWorkspaceIsColouredBeforeWaitingForItsTab(t *testing.T) {
	newTree := tree + `└── workspace workspace:9 "Cortex"
    └── pane pane:9 [focused]
        └── surface surface:90 [terminal] "zsh" tty=ttys005
`
	f := &fake{trees: []string{tree, tree, tree, newTree}}
	if _, _, _, err := Run(Spawn{Project: "cortex", Role: "lead", Prompt: "Read your brief."}, f.deps()); err != nil {
		t.Fatal(err)
	}
	if i := slices.IndexFunc(f.calls, func(c string) bool { return strings.HasPrefix(c, "workspace-action") }); i != 2 {
		t.Fatal(i, f.calls)
	}
}

// Review 10-22: only Claude Code's own failure line counts; a prompt that names "API Error" started fine.
func TestAPromptThatNamesAnAPIErrorStillStarts(t *testing.T) {
	f := &fake{trees: []string{tree}, fast: true}
	if _, _, _, err := Run(Spawn{Project: "quotes", Role: "lead", Prompt: "Fix the API Error in checkout retries."}, f.deps()); err != nil {
		t.Fatal(err)
	}
}

// Review 10-22: Claude Code retries an overloaded API by itself; a retry line is not a failed first turn.
func TestARetryingAPIErrorIsNotAFailure(t *testing.T) {
	f := &fake{trees: []string{tree}, apiErr: "⎿  API Error (529 Overloaded) · Retrying in 2 seconds… (attempt 1/10)"}
	if _, _, _, err := Run(Spawn{Project: "quotes", Role: "lead", Prompt: "Read your brief."}, f.deps()); err != nil {
		t.Fatal(err)
	}
}
