package wakewatch_test

import (
	"errors"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"

	"github.com/orzazade/crius/internal/wakewatch"
)

// legionPane is Procs' answer for Legion quotes:dev: pane pid 100 (a login bash) running claude (pid 200) with an MCP
// server, the way the tabs ran on 11-19; claudeFile is that claude's session file and its /proc command line.
const (
	legionPane = "100\n  99     1 tmux new-session -d\n 100    99 -bash\n" +
		" 200   100 claude --resume 58128bf4-0c1e-4c39-9d1f-6a0f3e1b2c7d --model claude-opus-5-5 --permission-mode bypassPermissions -n quotes · dev\n" +
		" 210   200 node /home/crius/.claude-max2/plugins/cache/context-mode/server.mjs\n--- cmdline\n"
	claudeFile = "/home/crius/.claude-max2/sessions/200.json\n" +
		`{"pid":200,"sessionId":"58128bf4-0c1e-4c39-9d1f-6a0f3e1b2c7d","cwd":"/home/crius/svc quote","status":"idle","version":"2.1.330"}` + "\n" +
		"claude\x01--resume\x0158128bf4-0c1e-4c39-9d1f-6a0f3e1b2c7d\x01--model\x01claude-opus-5-5\x01--permission-mode\x01bypassPermissions\x01-n\x01quotes · dev\x01\n"
)

func frozenSweep(t *testing.T, file string) (home string, f *fakeSweep) {
	home = t.TempDir()
	if err := os.WriteFile(wakewatch.ProviderTabsFile(home), []byte("legion quotes:dev\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return home, &fakeSweep{screens: map[string]string{"legion quotes:dev": pane(t, file)},
		procs: map[string]string{"legion quotes:dev": legionPane}, claude: map[string]string{"legion quotes:dev 200": claudeFile}}
}

// calls gives each simple command of a shell line as its words after quote removal, its VAR=value first.
func calls(t *testing.T, line string) [][]string {
	t.Helper()
	f, err := syntax.NewParser().Parse(strings.NewReader(line), "")
	if err != nil {
		t.Fatalf("not a shell line: %v\n%s", err, line)
	}
	var out [][]string
	syntax.Walk(f, func(n syntax.Node) bool {
		c, ok := n.(*syntax.CallExpr)
		if !ok {
			return true
		}
		var w []string
		for _, a := range c.Assigns {
			v, err := expand.Fields(nil, a.Value)
			if err != nil || len(v) != 1 {
				t.Fatalf("%v %q", err, v)
			}
			w = append(w, a.Name.Value+"="+v[0])
		}
		args, err := expand.Fields(nil, c.Args...)
		if err != nil {
			t.Fatal(err)
		}
		w = append(w, args...)
		out = append(out, w)
		return true
	})
	return out
}

// 11-19 16:48: Legion quotes:dev froze after a Claude Code auto-update ("Update installed · restart to apply"). Crius's
// line sat typed in its box, unsent, until 21:20; every sweep read the tab as idle. Two looks 5 min apart must tell Crius
// once, with the command that restarts its claude in the same session; a third look says nothing new.
func TestFrozenTabTellsCriusOnceWithTheRestartCommand(t *testing.T) {
	home, f := frozenSweep(t, "legion-frozen-update.txt")
	t0 := time.Date(2026, 11, 19, 16, 53, 0, 0, time.UTC)
	for i, now := range []time.Time{t0, t0.Add(5 * time.Minute), t0.Add(10 * time.Minute)} {
		if _, err := wakewatch.ProviderErrors(f.deps(home, now)); err != nil {
			t.Fatalf("look %d: %v", i+1, err)
		}
		if i == 0 && len(f.crius) != 0 {
			t.Fatalf("told Crius after one look: %q", f.crius)
		}
	}
	if len(f.crius) != 1 || len(f.posts) != 1 {
		t.Fatalf("want one alert after the second look, Crius heard %q, bus %q", f.crius, f.posts)
	}
	got := f.crius[0]
	for _, want := range []string{
		"tab legion quotes:dev stopped taking input",
		`"Crius: row Q-41 is yours; read the bus and go"`,
		"for 5 min (first seen 16:53)",
		`its screen says "Update installed · restart to apply"`,
		"crius-tsend legion quotes:dev '" + wakewatch.FrozenText + "'",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("alert lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Check first") {
		t.Errorf("a clean command line gave notes:\n%s", got)
	}
	if len(f.sentTab) != 0 {
		t.Errorf("typed into the tab: %q", f.sentTab)
	}
	// the command, read as a shell reads it: ssh to the box, kill the claude, wait for it to end, then type the resume
	// into the pane's shell
	_, after, ok := strings.Cut(got, "same session: ")
	cmd, _, ok2 := strings.Cut(after, " ; then")
	if !ok || !ok2 {
		t.Fatalf("no command in:\n%s", got)
	}
	top := calls(t, cmd)
	if len(top) != 1 || len(top[0]) != 3 || top[0][0] != "ssh" || top[0][1] != "legion" {
		t.Fatalf("want ssh legion <line>, got %q", top)
	}
	remote := calls(t, top[0][2])
	if !slices.Equal(remote[0], []string{"kill", "-TERM", "200"}) {
		t.Errorf("first step %q, want kill -TERM 200", remote[0])
	}
	var typed []string
	for _, c := range remote {
		if len(c) == 6 && c[0] == "tmux" && c[1] == "send-keys" && c[4] == "-l" {
			typed = append(typed, c[5])
			if c[3] != "quotes:dev" {
				t.Errorf("typed into %q", c[3])
			}
		}
	}
	if len(typed) != 1 || !slices.Equal(remote[len(remote)-1], []string{"tmux", "send-keys", "-t", "quotes:dev", "Enter"}) {
		t.Fatalf("want one resume typed, then Enter: %q", remote)
	}
	resume := calls(t, typed[0])
	want := [][]string{{"cd", "/home/crius/svc quote"}, {"CLAUDE_CONFIG_DIR=/home/crius/.claude-max2", "claude", "--resume",
		"58128bf4-0c1e-4c39-9d1f-6a0f3e1b2c7d", "--model", "claude-opus-5-5", "--permission-mode", "bypassPermissions",
		"-n", "quotes · dev"}}
	if len(resume) != 2 || !slices.Equal(resume[0], want[0]) || !slices.Equal(resume[1], want[1]) {
		t.Errorf("resume %q\nwant   %q", resume, want)
	}
}

// A tab working on a turn holds what was typed into its box until the turn ends: never frozen. An idle tab with an
// empty box (a grey suggestion only) has nothing unsent. Neither is said, however long it lasts.
func TestBusyOrEmptyTabIsNotFrozen(t *testing.T) {
	for _, file := range []string{"legion-busy-typed.txt", "legion-idle.txt"} {
		home, f := frozenSweep(t, file)
		t0 := time.Date(2026, 11, 19, 16, 53, 0, 0, time.UTC)
		for i := range 4 {
			if _, err := wakewatch.ProviderErrors(f.deps(home, t0.Add(time.Duration(i)*5*time.Minute))); err != nil {
				t.Fatal(err)
			}
		}
		if len(f.crius) != 0 || len(f.posts) != 0 {
			t.Errorf("%s: told Crius %q, bus %q", file, f.crius, f.posts)
		}
	}
}

// The screen above the box changing between looks (a turn ran, the text went) starts the timer again; a shell count in
// the "✻" line changing does not.
func TestUnsentKeyIsTheScreenAboveTheBox(t *testing.T) {
	s := pane(t, "legion-frozen-update.txt")
	typed, k1, ok := wakewatch.Unsent(s)
	if !ok || typed != "Crius: row Q-41 is yours; read the bus and go" {
		t.Fatalf("Unsent = %q, %v", typed, ok)
	}
	_, k2, _ := wakewatch.Unsent(strings.Replace(s, "2 shells still running", "1 shell still running", 1))
	_, k3, _ := wakewatch.Unsent(strings.Replace(s, "posted DONE on the bus.", "posted DONE on the bus again.", 1))
	_, k4, _ := wakewatch.Unsent(strings.Replace(s, "read the bus and go", "read the bus and go now", 1))
	if k1 != k2 || k1 == k3 || k1 != k4 {
		t.Errorf("keys %q %q %q %q: want a shell count and the typed text left out, the reply in", k1, k2, k3, k4)
	}
}

// A tab that cannot be read keeps its timer: a failed look is not "taken"; the next look that sees the same box says it.
func TestFrozenTimerSurvivesAFailedLook(t *testing.T) {
	home, f := frozenSweep(t, "legion-frozen-update.txt")
	screen := f.screens["legion quotes:dev"]
	t0 := time.Date(2026, 11, 19, 16, 53, 0, 0, time.UTC)
	steps := []bool{true, false, true} // read, ssh fails, read
	for i, read := range steps {
		if read {
			f.screens["legion quotes:dev"] = screen
		} else {
			delete(f.screens, "legion quotes:dev")
		}
		wakewatch.ProviderErrors(f.deps(home, t0.Add(time.Duration(i)*3*time.Minute))) // the failed look errs; checked below
	}
	n := 0
	for _, c := range f.crius {
		if strings.Contains(c, "stopped taking input") {
			n++
			if !strings.Contains(c, "for 6 min (first seen 16:53)") {
				t.Errorf("timer restarted: %s", c)
			}
		}
	}
	if n != 1 {
		t.Errorf("want one frozen alert, Crius heard %q", f.crius)
	}
}

// When the restart command cannot be built (the box's processes cannot be read), Crius still hears once, with why and
// the steps by hand.
func TestFrozenTabWithoutACommandStillTellsCrius(t *testing.T) {
	home, f := frozenSweep(t, "legion-frozen-update.txt")
	f.procs = nil
	t0 := time.Date(2026, 11, 19, 16, 53, 0, 0, time.UTC)
	for _, now := range []time.Time{t0, t0.Add(5 * time.Minute)} {
		wakewatch.ProviderErrors(f.deps(home, now))
	}
	if len(f.crius) != 1 || !strings.Contains(f.crius[0], "No restart command: could not read the processes of the tab") ||
		!strings.Contains(f.crius[0], "claude --resume <its session id>") {
		t.Errorf("Crius heard %q", f.crius)
	}
}

// The restart line run by a shell on the box, with kill, sleep and tmux faked: the resume is typed only once the frozen
// claude is gone; a claude that outlives TERM is said, and nothing is typed into it.
func TestFrozenRestartTypesOnlyOnceClaudeIsGone(t *testing.T) {
	home, f := frozenSweep(t, "legion-frozen-update.txt")
	t0 := time.Date(2026, 11, 19, 16, 53, 0, 0, time.UTC)
	for _, now := range []time.Time{t0, t0.Add(5 * time.Minute)} {
		if _, err := wakewatch.ProviderErrors(f.deps(home, now)); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.crius) != 1 {
		t.Fatalf("Crius heard %q", f.crius)
	}
	_, after, _ := strings.Cut(f.crius[0], "same session: ")
	cmd, _, _ := strings.Cut(after, " ; then")
	remote := calls(t, cmd)[0][2]
	fakes := `kill() { echo "kill $*"; if [ "$1" = -0 ]; then return $ALIVE; fi; }; sleep() { :; }; tmux() { echo "tmux $1 $2 $3 $4"; }; `
	for _, c := range []struct {
		alive, code int
		want, not   string
	}{
		{1, 0, "kill -TERM 200\nkill -0 200\nkill -0 200\ntmux send-keys -t quotes:dev -l\ntmux send-keys -t quotes:dev Enter\n", "still runs"},
		{0, 1, "claude 200 still runs 10 s after TERM: not resumed", "tmux"},
	} {
		sh := exec.Command("/bin/sh", "-c", fakes+remote)
		sh.Env = append(os.Environ(), "ALIVE="+strconv.Itoa(c.alive))
		out, err := sh.CombinedOutput()
		code := 0
		if err != nil {
			var ee *exec.ExitError
			if !errors.As(err, &ee) {
				t.Fatal(err)
			}
			code = ee.ExitCode()
		}
		if code != c.code || !strings.Contains(string(out), c.want) || strings.Contains(string(out), c.not) {
			t.Errorf("claude alive=%v: exit %d\n%s", c.alive == 0, code, out)
		}
	}
}

// Lines typed into a frozen box push lines off the top of a full screen: the key stays, so Crius is not told again.
func TestUnsentKeyKeepsAcrossAGrowingBox(t *testing.T) {
	var older []string
	for i := range 30 {
		older = append(older, "  older reply line "+strconv.Itoa(i))
	}
	s := strings.Join(older, "\n") + "\n" + pane(t, "legion-frozen-update.txt")
	_, k1, ok1 := wakewatch.Unsent(s)
	grown := strings.Join(older[1:], "\n") + "\n" + strings.Replace(pane(t, "legion-frozen-update.txt"),
		"read the bus and go", "read the bus and go\n  continue: the same row", 1)
	typed, k2, ok2 := wakewatch.Unsent(grown)
	if !ok1 || !ok2 || k1 != k2 || !strings.Contains(typed, "continue: the same row") {
		t.Errorf("keys %q %q (%v %v), typed %q", k1, k2, ok1, ok2, typed)
	}
}
