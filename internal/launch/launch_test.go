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
	calls    []string
	trees    []string // answers to "tree --all", in order; the last one repeats
	list     string   // answer to "workspace list"
	persona  string   // from projects.toml; "" means work
	boot     []string // screens before Claude is up, one per read; a dialog is drawn from dialog instead
	dialog   []string // options of an open dialog, cursor at pick
	pick     int
	model    string // status bar model; "" means the one the command asked for
	cmd      string
	draft    string
	sent     bool
	noWork   bool   // the prompt is sent but the screen never shows work
	fast     bool   // the prompt is answered before a read can see work, as live 10-22 ("ready" in 5 s)
	apiErr   string // the line under an emptied box after the first turn, as "⎿  API Error: 529 Overloaded"
	asks     string // the dialog question; "" means the folder-trust one
	linger   int    // reads an answered dialog stays drawn, cursor where it was
	shown    []string
	tip      string // a line under the banner
	exited   bool   // the dialog was answered No
	front    string // what runs in front on a tab's tty; "" means zsh
	noConv   bool   // claude --resume finds no conversation and exits, as with the wrong config folder
	ghost    string // grey text in an empty box (Claude's placeholder), which read-screen gives as plain text
	typedErr error  // what cmux surface.input_state fails with
	sentErr  error  // what it fails with once the prompt is sent
	sentErrs int    // how many input-state reads after Enter fail with sentErr; 0: all
	failed   int
	noLaunch bool                                                     // projects.toml gives the project several cwd and no launch folder
	stale    int                                                      // reads after Enter that still show the typed prompt, as a read taken just before Claude takes it
	dead     int                                                      // a crashed tab: reads after Enter that still show the dead Claude's box with the shell line under it; -1: always
	echo     int                                                      // reads after a send that do not show the typed line yet
	ran      string                                                   // the shell line Enter ran in a crashed tab
	clip     int                                                      // a crashed tab's shell shows a line longer than this as ">...." and its last runes, as zsh does
	quote    bool                                                     // the resumed Claude shows the shell line it was started with above its box (a restored message)
	picked   func(project, want string) (name, dir string, err error) // the Max account picker; nil: none
}

const statusBar = "  %s │ main │ ██░░░░░░░░ ctx 2%%\n"

func (f *fake) deps() Deps {
	return Deps{
		Lock: filepath.Join(os.TempDir(), "crius-launch-test.lock"),
		Pick: f.picked,
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
				case key == "enter" && strings.HasPrefix(f.draft, "clear && "): // a shell runs the line: clear wipes it
					f.ran, f.draft, f.sent = f.draft, "", true
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
		Front: func(tty string) (string, error) {
			f.calls = append(f.calls, "ps "+tty)
			if f.front == "" {
				return "-zsh", nil
			}
			return f.front, nil
		},
		Typed: func(surface string) (bool, error) {
			f.calls = append(f.calls, "input_state "+surface)
			if f.sent && f.sentErr != nil && (f.sentErrs == 0 || f.failed < f.sentErrs) {
				f.failed++
				return false, f.sentErr
			}
			return !f.sent && f.draft != "", f.typedErr
		},
		Persona: func(project string) (string, error) {
			if f.persona == "" {
				return "work", nil
			}
			return f.persona, nil
		},
		Place: func(project string) (string, string, string, error) {
			if project == "falcon" {
				return "", "", "", errors.New("projects.toml has no project falcon")
			}
			if f.noLaunch {
				return "", "", "", errors.New("projects.toml: project " + project + " has 3 cwd folders; set launch to the folder its sessions start in")
			}
			if f.persona == "" {
				return "/Users/o/Projects/" + project, "max", "work", nil
			}
			return "/Users/o/Projects/" + project, "max", f.persona, nil
		},
	}
}

func (f *fake) screen() string {
	if f.dead != 0 {
		if f.sent && f.dead > 0 {
			f.dead--
		}
		line := f.draft + f.ran
		if f.echo > 0 {
			f.echo--
			line = ""
		}
		// As a crash leaves it: the dead Claude's box, then the shell's prompt and the line wrapped at the tab's width.
		s := " ▐▛███▜▌   Claude Code v2.1.308\n\n──────────────── quotes · lead ─\n❯ \n────────────────\n  quota: no response yet\n"
		shown := []rune("o@mac quotes % " + line)
		if f.clip > 0 && len(shown) > f.clip {
			shown = append([]rune(">...."), shown[len(shown)-f.clip+5:]...)
		}
		for r := shown; ; r = r[80:] {
			if len(r) <= 80 {
				return s + string(r) + "\n"
			}
			s += string(r[:80]) + "\n"
		}
	}
	if f.sent && f.stale > 0 { // drawn as before Enter; Typed already says the box is empty
		f.stale--
		f.sent = false
		defer func() { f.sent = true }()
	}
	switch {
	case f.noConv && f.sent:
		return "No conversation found with session ID: " + resumeID + "\no@mac quotes % \n"
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
	if f.quote && f.ran != "" {
		body += "⏺ It was started with: " + f.ran + "\n\n"
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
	if draft == "" {
		draft = f.ghost
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
	if err == nil || !strings.Contains(err.Error(), "esc to interrupt") || !strings.Contains(err.Error(), "do not rerun") {
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

// Review 10-22 (2): a read taken just before Claude takes the prompt shows it in the box, while the input state read
// after it says the box is empty. That stale screen is no proof the turn started: the API error drawn later counts.
func TestAStaleScreenWithTheTypedPromptDoesNotEndTheWait(t *testing.T) {
	f := &fake{trees: []string{tree}, apiErr: "⎿  API Error: 529 Overloaded", stale: 1}
	_, _, _, err := Run(Spawn{Project: "quotes", Role: "lead", Prompt: "Read your brief."}, f.deps())
	if err == nil || !strings.Contains(err.Error(), "API Error: 529") {
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

const resumeID = "6f1c2a9e-3b4d-4e5f-8a7b-9c0d1e2f3a4b"

// treeWith is tree plus a tab in the Quotes workspace.
func treeWith(tab string) string {
	return strings.Replace(tree, `"quotes: rig" tty=ttys000`, `"quotes: rig" tty=ttys000`+"\n│       └── surface surface:22 [terminal] \""+tab+"\" tty=ttys009", 1)
}

// Brief 10-22 item 5: after a crash the role's tab stays open with a shell in front; the role comes back there, in its
// own folder, with its own conversation.
func TestResumeTypesTheResumeCommandIntoACrashedTab(t *testing.T) {
	f := &fake{trees: []string{treeWith("quotes · lead")}}
	ws, surface, err := Resume("quotes", "lead", resumeID, "/Users/o/Projects/quotes/src", "", "", f.deps())
	if err != nil {
		t.Fatal(err)
	}
	if ws != "workspace:2" || surface != "surface:22" {
		t.Errorf("got %s %s, want the lead's tab workspace:2 surface:22", ws, surface)
	}
	want := "send --workspace workspace:2 --surface surface:22 -- clear && cd '/Users/o/Projects/quotes/src' && aitch launch claude -- " +
		`--model claude-opus-5-5 --permission-mode bypassPermissions --settings '{"autoCompactWindow":300000}' -n 'quotes · lead' --resume ` + resumeID
	if got := f.called("send "); len(got) != 1 || got[0] != want {
		t.Errorf("typed %q,\nwant %q", got, want)
	}
	if got := f.called("ps "); len(got) != 1 || got[0] != "ps ttys009" {
		t.Errorf("front checked on %q, want the tab's tty", got)
	}
	if got := f.called("new-"); len(got) != 0 {
		t.Errorf("made %q, want the old tab used", got)
	}
}

// Brief 10-22 item 5: after a reboot the tab is gone; the role comes back in a new tab of its project's workspace,
// named as before.
func TestResumeOpensANewTabWhenTheTabIsGone(t *testing.T) {
	f := &fake{trees: []string{tree}}
	ws, surface, err := Resume("quotes", "lead", resumeID, "/Users/o/Projects/quotes", "", "", f.deps())
	if err != nil {
		t.Fatal(err)
	}
	if ws != "workspace:2" || surface != "surface:99" {
		t.Errorf("got %s %s, want a new tab in Quotes", ws, surface)
	}
	if got := f.called("new-surface "); len(got) != 1 || !strings.Contains(got[0], "--working-directory /Users/o/Projects/quotes ") ||
		!strings.HasSuffix(got[0], "-n 'quotes · lead' --resume "+resumeID) {
		t.Errorf("new-surface %q", got)
	}
	if got := f.called("rename-tab "); len(got) != 1 || !strings.HasSuffix(got[0], "-- quotes · lead") {
		t.Errorf("rename-tab %q", got)
	}
}

// A role whose tab still runs Claude is not started twice.
func TestResumeLeavesARunningClaudeAlone(t *testing.T) {
	f := &fake{trees: []string{treeWith("quotes · lead")}, front: "claude"}
	_, _, err := Resume("quotes", "lead", resumeID, "/Users/o/Projects/quotes", "", "", f.deps())
	if err == nil || !strings.Contains(err.Error(), "runs claude") {
		t.Fatalf("err %v, want refused", err)
	}
	if got := f.called("send"); len(got) != 0 {
		t.Errorf("typed %q", got)
	}
}

// The id and the folder are typed into a shell: anything but a session id, or a folder that could end the quotes, is
// refused before cmux is called.
func TestResumeRefusesWhatCouldBreakTheShellLine(t *testing.T) {
	for name, c := range map[string][2]string{
		"id":       {"x; rm -rf ~", "/Users/o/Projects/quotes"},
		"quote":    {resumeID, "/Users/o/it's"},
		"none":     {resumeID, ""},
		"relative": {resumeID, "quotes"},
	} {
		f := &fake{trees: []string{treeWith("quotes · lead")}}
		if _, _, err := Resume("quotes", "lead", c[0], c[1], "", "", f.deps()); err == nil {
			t.Errorf("%s: no error", name)
		}
		if len(f.calls) != 0 {
			t.Errorf("%s: called %q", name, f.calls)
		}
	}
}

// Live risk: aitch may start claude on another account folder, where the id names no conversation; claude exits, and
// that must fail loudly with what the screen said.
func TestResumeFailsLoudlyWhenNoConversationIsFound(t *testing.T) {
	f := &fake{trees: []string{treeWith("quotes · lead")}, noConv: true}
	_, _, err := Resume("quotes", "lead", resumeID, "/Users/o/Projects/quotes", "", "", f.deps())
	if err == nil || !strings.Contains(err.Error(), "No conversation found") {
		t.Fatalf("err %v, want the screen's words", err)
	}
}

// Review 10-22 (item 5): a resumed session whose own screen quotes the failure (a crius session discussing this very
// check) has its input box up: it resumed, and is not reported failed.
func TestResumeWithTheFailureWordsOnAnUpScreenSucceeds(t *testing.T) {
	f := &fake{trees: []string{treeWith("quotes · lead")}, tip: "  grep for: No conversation found with session ID"}
	if _, _, err := Resume("quotes", "lead", resumeID, "/Users/o/Projects/quotes", "", "", f.deps()); err != nil {
		t.Fatalf("err %v, want resumed", err)
	}
}

// qa3 10-22 19:2x: a new tab's box showed Claude's grey placeholder 'Try "refactor <filepath>"', which read-screen gives
// as plain text, and crius-spawn stopped on "already holds text". cmux's input state keeps the faint style (O4).
func TestAGreyPlaceholderInTheNewBoxIsNotADraft(t *testing.T) {
	f := &fake{trees: []string{tree}, ghost: `Try "refactor <filepath>"`}
	if _, _, _, err := Run(Spawn{Project: "quotes", Role: "qa3", Prompt: "Read your brief."}, f.deps()); err != nil {
		t.Fatalf("err %v, want launched", err)
	}
	if !f.sent || len(f.called("input_state surface:99")) == 0 {
		t.Errorf("sent %v, input_state calls %q", f.sent, f.called("input_state"))
	}
}

func TestTypedTextInTheNewBoxStillStopsTheLaunch(t *testing.T) {
	f := &fake{trees: []string{tree}, draft: "hello"}
	_, _, _, err := Run(Spawn{Project: "quotes", Role: "qa3", Prompt: "Read your brief."}, f.deps())
	if err == nil || !strings.Contains(err.Error(), `already holds text: "hello"`) {
		t.Fatalf("err %v, want the draft named", err)
	}
}

func TestAnUnreadableInputStateFailsTheLaunchLoudly(t *testing.T) {
	f := &fake{trees: []string{tree}, ghost: `Try "refactor <filepath>"`, typedErr: errors.New("cmux rpc surface.input_state: no such method")}
	_, _, _, err := Run(Spawn{Project: "quotes", Role: "qa3", Prompt: "Read your brief."}, f.deps())
	if err == nil || !strings.Contains(err.Error(), "no such method") || f.sent {
		t.Fatalf("err %v sent %v, want the input-state failure and nothing sent", err, f.sent)
	}
}

// Review 10-22: -resume needs only the persona; the session's own folder is its cwd. A project with several cwd and no
// launch folder (trading, quotes before its launch line) still comes back after a reboot.
func TestResumeDoesNotNeedALaunchFolder(t *testing.T) {
	f := &fake{trees: []string{tree}, noLaunch: true}
	if _, _, err := Resume("quotes", "lead", resumeID, "/Users/o/Projects/axon", "", "", f.deps()); err != nil {
		t.Fatal(err)
	}
	if got := f.called("new-surface "); len(got) != 1 || !strings.Contains(got[0], "--working-directory /Users/o/Projects/axon ") {
		t.Errorf("new-surface %q", got)
	}
}

// Review 10-22 (3): an input state that cannot be read after Enter comes after the prompt was sent; the error says so
// and names the tab, so no one reruns the launch and sends the prompt twice.
func TestAnInputStateFailureAfterEnterSaysThePromptWasSent(t *testing.T) {
	f := &fake{trees: []string{tree}, fast: true, ghost: `Try "refactor <filepath>"`, sentErr: errors.New("cmux rpc surface.input_state: timeout")}
	_, _, _, err := Run(Spawn{Project: "quotes", Role: "qa3", Prompt: "Read your brief."}, f.deps())
	if err == nil || !f.sent || !strings.Contains(err.Error(), "the prompt was sent to workspace:2 surface:99") ||
		!strings.Contains(err.Error(), "timeout") {
		t.Fatalf("err %v sent %v, want the sent prompt and its tab named", err, f.sent)
	}
	// One failed read is no answer: the next read decides (review 10-22).
	f = &fake{trees: []string{tree}, fast: true, ghost: `Try "refactor <filepath>"`, sentErr: errors.New("cmux rpc surface.input_state: timeout"), sentErrs: 1}
	if _, _, _, err := Run(Spawn{Project: "quotes", Role: "qa3", Prompt: "Read your brief."}, f.deps()); err != nil || f.failed != 1 {
		t.Fatalf("err %v after %d failed reads, want nil after 1", err, f.failed)
	}
}

// OF9: a crashed tab still shows the dead Claude's input box above the shell. A line that never runs (clear never
// wipes that box) must fail, not pass as resumed.
func TestAResumeIntoACrashedTabTakesNoDeadBoxForANewOne(t *testing.T) {
	f := &fake{trees: []string{treeWith("quotes · lead")}, dead: -1}
	_, _, err := Resume("quotes", "lead", resumeID, "/Users/o/Projects/quotes/src", "", "", f.deps())
	if err == nil || !strings.Contains(err.Error(), "no Claude's input box after 120 reads") {
		t.Fatalf("err %v, want no box seen", err)
	}
}

// OF9: Enter goes only once the typed line shows (a read before the echo sees only the dead box), and the box counts
// only after clear wiped the line.
func TestAResumeIntoACrashedTabWaitsForTheLineThenForClear(t *testing.T) {
	f := &fake{trees: []string{treeWith("quotes · lead")}, dead: 3, echo: 2}
	if _, _, err := Resume("quotes", "lead", resumeID, "/Users/o/Projects/quotes/src", "", "", f.deps()); err != nil {
		t.Fatal(err)
	}
	enter := slices.IndexFunc(f.calls, func(c string) bool { return strings.HasPrefix(c, "send-key") })
	before, after := 0, 0
	for i, c := range f.calls {
		if strings.HasPrefix(c, "read-screen") && i < enter {
			before++
		} else if strings.HasPrefix(c, "read-screen") {
			after++
		}
	}
	if enter < 0 || before != 3 || after != 4 {
		t.Errorf("Enter at call %d after %d reads, then %d reads; want 3 reads (2 before the echo), Enter, 4 reads (3 before clear): %q",
			enter, before, after, f.calls)
	}
}

// Review of OF9 (10-22): zsh clips a line wider than the tab to ">...." and its end, so the whole line never shows;
// Enter goes once the line's end (--resume <id>) shows on the shell's line.
func TestAResumeIntoACrashedTabPressesEnterOnAClippedLine(t *testing.T) {
	f := &fake{trees: []string{treeWith("quotes · lead")}, dead: 2, clip: 70}
	if _, _, err := Resume("quotes", "lead", resumeID, "/Users/o/Projects/quotes/src", "", "", f.deps()); err != nil || f.ran == "" {
		t.Fatalf("err %v, ran %q: want the line run and the box seen", err, f.ran)
	}
}

// Review of OF9 (10-22): the resumed Claude may quote the line it was started with above its new box (a restored
// message); only the line under the last box is the shell's.
func TestAResumedClaudeQuotingItsCommandIsUp(t *testing.T) {
	f := &fake{trees: []string{treeWith("quotes · lead")}, dead: 1, quote: true}
	if _, _, err := Resume("quotes", "lead", resumeID, "/Users/o/Projects/quotes/src", "", "", f.deps()); err != nil {
		t.Fatal(err)
	}
}

// OF9: two resumes of one role at once (crius-spawn run twice): the second waits for the first, then finds Claude in
// front of the tab and leaves it alone. Without a lock both saw a shell and both typed.
func TestTwoResumesOfOneRoleAtOnceTypeOnce(t *testing.T) {
	var mu sync.Mutex
	f := &fake{trees: []string{treeWith("quotes · lead")}}
	d := f.deps()
	d.Lock = filepath.Join(t.TempDir(), "launch.lock")
	own, front := d.Cmux, d.Front
	d.Cmux = func(args ...string) (string, error) {
		if args[0] == "tree" || args[0] == "send" {
			time.Sleep(50 * time.Millisecond) // cmux is slow: both read the tree and see a shell before either types
		}
		mu.Lock()
		defer mu.Unlock()
		return own(args...)
	}
	d.Front = func(tty string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if f.sent { // the first resume's claude now runs in front
			return "claude", nil
		}
		return front(tty)
	}
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			_, _, err := Resume("quotes", "lead", resumeID, "/Users/o/Projects/quotes/src", "", "", d)
			errs <- err
		}()
	}
	var left int
	for range 2 {
		if err := <-errs; err != nil && strings.Contains(err.Error(), "runs claude in front") {
			left++
		} else if err != nil {
			t.Error(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if got := f.called("send "); len(got) != 1 || left != 1 {
		t.Errorf("typed %d times, %d left alone; want 1 and 1", len(got), left)
	}
}

// Owner 10-23: a new session starts on the Max account with the most room, through aitch's --primary for its folder,
// and the tab title names the account. The tag still says account=max: the row is a Max account.
func TestAPickedAccountStartsInItsFolderAndIsInTheTitle(t *testing.T) {
	var asked []string
	f := &fake{trees: []string{tree}, picked: func(project, want string) (string, string, error) {
		asked = append(asked, project+" "+want)
		return "max2", "/Users/o/.claude-max2", nil
	}}
	_, _, tag, err := Run(Spawn{Project: "quotes", Role: "lead", Prompt: "Read your brief."}, f.deps())
	if err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 || asked[0] != "quotes max" {
		t.Errorf("picker asked %q, want once for quotes on max", asked)
	}
	want := "--command aitch launch claude --primary '/Users/o/.claude-max2' -- --model claude-opus-5-5" +
		` --permission-mode bypassPermissions --settings '{"autoCompactWindow":300000}' -n 'quotes · lead · max2'`
	if ns := f.called("new-surface")[0]; !strings.HasSuffix(ns, want) {
		t.Errorf("new-surface\n got %s\nwant suffix %s", ns, want)
	}
	if got := f.called("rename-tab "); len(got) != 1 || !strings.HasSuffix(got[0], "-- quotes · lead · max2") {
		t.Errorf("rename-tab %q", got)
	}
	if !strings.HasPrefix(tag, "[crius account=max project=quotes role=lead") {
		t.Errorf("tag %q", tag)
	}

	// A named row is asked for by name; enterprise is never picked; no pick launches as before.
	for _, c := range []struct{ account, asked, suffix string }{
		{"max3", "quotes max3", "-n 'quotes · lead · max2'"},
		{"enterprise", "", "-n 'quotes · lead'"},
	} {
		asked = nil
		f := &fake{trees: []string{tree}, picked: f.picked}
		if _, _, _, err := Run(Spawn{Project: "quotes", Role: "lead", Account: c.account, Prompt: "go"}, f.deps()); err != nil {
			t.Fatal(c.account, err)
		}
		if strings.Join(asked, ",") != c.asked || !strings.HasSuffix(f.called("new-surface")[0], c.suffix) {
			t.Errorf("%s: asked %q, new-surface %s", c.account, asked, f.called("new-surface")[0])
		}
	}
	f = &fake{trees: []string{tree}, picked: func(string, string) (string, string, error) { return "", "", nil }}
	if _, _, _, err := Run(Spawn{Project: "quotes", Role: "lead", Prompt: "go"}, f.deps()); err != nil {
		t.Fatal(err)
	}
	if ns := f.called("new-surface")[0]; !strings.Contains(ns, "aitch launch claude -- --model") || !strings.HasSuffix(ns, "-n 'quotes · lead'") {
		t.Errorf("no pick: %s", ns)
	}
}

// A picker that says no, or gives a folder that could break the shell line, stops the launch before cmux is touched.
func TestAFailedPickIsRefusedBeforeCmuxIsTouched(t *testing.T) {
	for _, c := range []struct {
		name, dir, err, want string
	}{
		{"", "", "every Max account is at its limit: max1 7d 92%", "every Max account is at its limit"},
		{"max2", "/Users/o/.claude-max2'; rm x", "", "bad folder"},
		{"max2", ".claude-max2", "", "bad folder"},
		{"Max 2", "/Users/o/.claude-max2", "", "bad account name"},
	} {
		f := &fake{trees: []string{tree}, picked: func(string, string) (string, string, error) {
			if c.err != "" {
				return "", "", errors.New(c.err)
			}
			return c.name, c.dir, nil
		}}
		_, _, _, err := Run(Spawn{Project: "quotes", Role: "lead", Prompt: "go"}, f.deps())
		if err == nil || !strings.Contains(err.Error(), c.want) || len(f.calls) != 0 {
			t.Errorf("%v: err %v, calls %q", c, err, f.calls)
		}
	}
	// With no picker, an account other than max or enterprise is refused as before.
	f := &fake{trees: []string{tree}}
	if _, _, _, err := Run(Spawn{Project: "quotes", Role: "lead", Account: "max2", Prompt: "go"}, f.deps()); err == nil ||
		!strings.Contains(err.Error(), "bad account") {
		t.Errorf("no picker, max2: %v", err)
	}
}

// A tab opened on a picked account is titled "<project> · <role> · <account>": a resume still finds it as the role's.
func TestResumeFindsTheRoleTabThatNamesItsAccount(t *testing.T) {
	f := &fake{trees: []string{treeWith("quotes · lead · max2")}}
	ws, surface, err := Resume("quotes", "lead", resumeID, "/Users/o/Projects/quotes/src", "", "", f.deps())
	if err != nil || ws != "workspace:2" || surface != "surface:22" {
		t.Fatalf("got %s %s %v, want the lead's tab", ws, surface, err)
	}
	if got := f.called("new-"); len(got) != 0 {
		t.Errorf("made %q, want the old tab used", got)
	}
	// "quotes · leader" is another role's tab, not the lead's.
	f = &fake{trees: []string{treeWith("quotes · leader")}}
	if _, surface, err := Resume("quotes", "lead", resumeID, "/Users/o/Projects/quotes", "", "", f.deps()); err != nil || surface != "surface:99" {
		t.Errorf("leader's tab: %s %v, want a new tab", surface, err)
	}
}

// Owner 10-23: a session that ran on a picked Max account comes back on it, in the same folder (its transcript is
// there), titled with the account as before.
func TestResumeStartsInTheSessionsAccountFolder(t *testing.T) {
	f := &fake{trees: []string{treeWith("quotes · lead · max2")}}
	if _, _, err := Resume("quotes", "lead", resumeID, "/Users/o/Projects/quotes/src", "max2", "/Users/o/.claude-max2", f.deps()); err != nil {
		t.Fatal(err)
	}
	want := "send --workspace workspace:2 --surface surface:22 -- clear && cd '/Users/o/Projects/quotes/src' && aitch launch claude --primary '/Users/o/.claude-max2' -- " +
		`--model claude-opus-5-5 --permission-mode bypassPermissions --settings '{"autoCompactWindow":300000}' -n 'quotes · lead · max2' --resume ` + resumeID
	if got := f.called("send "); len(got) != 1 || got[0] != want {
		t.Errorf("typed %q,\nwant %q", got, want)
	}
	f = &fake{trees: []string{tree}}
	if _, _, err := Resume("quotes", "lead", resumeID, "/Users/o/Projects/quotes", "max2", "/Users/o/.claude-max2", f.deps()); err != nil {
		t.Fatal(err)
	}
	if got := f.called("new-surface "); len(got) != 1 || !strings.Contains(got[0], "--primary '/Users/o/.claude-max2' ") ||
		!strings.HasSuffix(got[0], "-n 'quotes · lead · max2' --resume "+resumeID) {
		t.Errorf("new-surface %q", got)
	}
	for _, c := range [][2]string{{"Max 2", "/Users/o/.claude-max2"}, {"max2", "/Users/o/.claude-max2'; rm x"}, {"max2", ""}} {
		f := &fake{trees: []string{tree}}
		if _, _, err := Resume("quotes", "lead", resumeID, "/Users/o/Projects/quotes", c[0], c[1], f.deps()); err == nil || len(f.calls) != 0 {
			t.Errorf("%q: err %v, calls %q: want refused before cmux", c, err, f.calls)
		}
	}
}

// Owner 10-23: a role on an account at its limit moves to one with room: a fresh session with the brief it is given, in
// the old session's folder (its worktree and branch), on the picked account. Its old tab, a shell once its Claude has
// stopped, is closed so the role has one tab.
func TestAMoveStartsTheRoleInItsFolderOnAnotherAccountAndClosesTheOldTab(t *testing.T) {
	to := func(string, string) (string, string, error) { return "max2", "/Users/o/.claude-max2", nil }
	f := &fake{trees: []string{treeWith("quotes · lead · max1")}, picked: to}
	s := Spawn{Project: "quotes", Role: "lead", Prompt: "Read your brief.", Move: &Moved{Cwd: "/Users/o/Projects/quotes/.worktrees/lead", From: "max1"}}
	if _, _, tag, err := Run(s, f.deps()); err != nil || tag != "[crius account=max project=quotes role=lead]" {
		t.Fatalf("%q %v", tag, err)
	}
	closed, opened := slices.Index(f.calls, "close-surface --workspace workspace:2 --surface surface:22"), -1
	for i, c := range f.calls {
		if strings.HasPrefix(c, "new-surface ") {
			opened = i
			if !strings.Contains(c, "--working-directory /Users/o/Projects/quotes/.worktrees/lead ") ||
				!strings.Contains(c, "--primary '/Users/o/.claude-max2' ") || !strings.HasSuffix(c, "-n 'quotes · lead · max2'") {
				t.Errorf("new-surface %q", c)
			}
		}
	}
	if closed < 0 || opened < closed {
		t.Errorf("calls %q: want the old tab closed, then the new one opened", f.calls)
	}
	// With no old tab open, the move just opens the new one.
	f = &fake{trees: []string{tree}, picked: to}
	if _, _, _, err := Run(s, f.deps()); err != nil || len(f.called("close-surface")) != 0 || len(f.called("new-surface")) != 1 {
		t.Errorf("%v, calls %q", err, f.calls)
	}
	// Refused before any tab is touched: no account to move to, the same account, a folder unfit for a shell line, and
	// an old tab with anything but a shell in front.
	for _, c := range []struct {
		f    *fake
		s    Spawn
		want string
	}{
		{&fake{trees: []string{tree}, picked: func(string, string) (string, string, error) { return "", "", nil }}, s, "no Max account"},
		{&fake{trees: []string{tree}, picked: func(string, string) (string, string, error) { return "max1", "/Users/o/.claude-work-max", nil }}, s, "on max1 already"},
		{&fake{trees: []string{tree}, picked: to}, Spawn{Project: "quotes", Role: "lead", Prompt: "go", Move: &Moved{Cwd: "rel/dir", From: "max1"}}, "bad folder"},
		{&fake{trees: []string{treeWith("quotes · lead · max1")}, picked: to, front: "claude"}, s, "left alone"},
	} {
		_, _, _, err := Run(c.s, c.f.deps())
		if err == nil || !strings.Contains(err.Error(), c.want) || len(c.f.called("close-surface")) != 0 || len(c.f.called("new-")) != 0 {
			t.Errorf("%q: err %v, calls %q", c.want, err, c.f.calls)
		}
	}
}
