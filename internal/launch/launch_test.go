package launch

import (
	"errors"
	"os"
	"path/filepath"
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

type fake struct {
	calls []string
	trees []string // answers to "tree --all", in order; the last one repeats
}

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
			case "new-surface":
				return "OK surface:99 pane:2 workspace:2", nil
			case "new-workspace":
				return "OK workspace:9", nil
			}
			return "OK", nil
		},
		Place: func(project string) (string, string, error) {
			if project == "falcon" {
				return "", "", errors.New("projects.toml has no project falcon")
			}
			return "/Users/o/Projects/" + project, "max", nil
		},
	}
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
	ws, surface, tag, err := Run("quotes", "qa-api", "", f.deps())
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
	f := &fake{trees: []string{tree, newTree}}
	ws, surface, _, err := Run("cortex", "lead", "enterprise", f.deps())
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
		if _, _, _, err := Run("quotes", role, "", f.deps()); err != nil {
			t.Fatal(err)
		}
		ns := f.called("new-surface")[0]
		want := "--command aitch launch claude -- --model " + model +
			` --permission-mode bypassPermissions --settings '{"autoCompactWindow":150000}' -n 'quotes · ` + role + `'`
		if !strings.HasSuffix(ns, want) {
			t.Fatalf("%s:\n got %s\nwant suffix %s", role, ns, want)
		}
	}
}

func TestBadInputIsRefusedBeforeCmuxIsTouched(t *testing.T) {
	for _, c := range []struct{ project, role, account, want string }{
		{"sideproj", "lead", "", "no workspace name and color"},
		{"falcon", "lead", "", "no project falcon"},
		{"quotes", "qa api", "", "bad role"},
		{"quotes", "lead'; rm x", "", "bad role"},
		{"quotes", "lead", "work", "bad account"},
	} {
		f := &fake{trees: []string{tree}}
		_, _, _, err := Run(c.project, c.role, c.account, f.deps())
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
	d := Deps{
		Lock: filepath.Join(t.TempDir(), "launch.lock"),
		Cmux: func(args ...string) (string, error) {
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
				return "OK workspace:9", nil
			case "new-surface":
				return "OK surface:91 pane:9 workspace:9", nil
			}
			return "OK", nil
		},
		Place: func(string) (string, string, error) { return "/Users/o/Projects/falcon", "max", nil },
	}
	var wg sync.WaitGroup
	for _, role := range []string{"lead", "qa-api"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, _, err := Run("falcon", role, "", d); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if made != 1 {
		t.Fatalf("%d workspaces made, want 1", made)
	}
}

// A launch with no lock file named is refused, not run unlocked.
func TestALaunchWithNoLockIsRefused(t *testing.T) {
	d := (&fake{trees: []string{tree}}).deps()
	d.Lock = ""
	if _, _, _, err := Run("quotes", "lead", "", d); err == nil || !strings.Contains(err.Error(), "lock") {
		t.Fatal(err)
	}
}
