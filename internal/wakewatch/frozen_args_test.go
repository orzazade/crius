package wakewatch

import (
	"slices"
	"strings"
	"testing"
)

// The restart names the session with --resume, so whatever picked a session before goes; a start prompt would be sent
// again, so it goes too and is named; an option's value, a list option's values and --flag=value stay.
func TestResumeArgs(t *testing.T) {
	for _, c := range []struct{ in, out, dropped []string }{
		{[]string{"--resume", "old", "--model", "opus", "-n", "quotes · dev"}, []string{"--model", "opus", "-n", "quotes · dev"}, nil},
		{[]string{"--continue", "--dangerously-skip-permissions", "Read BRIEF.md"}, []string{"--dangerously-skip-permissions"}, []string{"Read BRIEF.md"}},
		{[]string{"--session-id=abc", "--fork-session", "-r", "--effort=high"}, []string{"--effort=high"}, nil},
		{[]string{"--add-dir", "/a", "/b", "--model", "opus"}, []string{"--add-dir", "/a", "/b", "--model", "opus"}, nil},
		{[]string{"--model", "opus", "--", "go", "on"}, []string{"--model", "opus"}, []string{"go", "on"}},
	} {
		out, dropped := resumeArgs(c.in)
		if !slices.Equal(out, c.out) || !slices.Equal(dropped, c.dropped) {
			t.Errorf("%q: out %q dropped %q; want %q, %q", c.in, out, dropped, c.out, c.dropped)
		}
	}
}

func fakeRestart(procs, info string) ProviderDeps {
	return ProviderDeps{Procs: func(RemoteTab) (string, error) { return procs, nil },
		Claude: func(RemoteTab, int) (string, error) { return info, nil },
		SSH:    func(t RemoteTab) ([]string, error) { return []string{"ssh", t.Box}, nil }}
}

// A claude that is the pane's own process, or that runs under a command which ends with it (bash -c), leaves no shell
// to type into: the pane is respawned with the resume. A claude under a wrapper in the pane's shell gets a note to
// check the pane shows a shell first. Flags read from ps (no /proc) get a note too. The config folder is always named,
// the default one too: the pane's shell may export another.
func TestFrozenRestartShapes(t *testing.T) {
	file := "/home/crius/.claude/sessions/200.json\n" + `{"sessionId":"s-1","cwd":"/w","status":"busy"}` + "\n\n"
	respawn := `ssh thinkpad 'tmux respawn-pane -k -t cortex:lead '\''cd /w && CLAUDE_CONFIG_DIR=/home/crius/.claude claude --resume s-1 --model opus'\'''`
	for _, c := range []struct{ procs, want, note string }{
		{"200\n 200 99 claude --model opus\n", respawn, "its flags come from ps"},
		{"100\n 100 99 bash -c claude --model opus\n 200 100 claude --model opus\n", respawn, "its flags come from ps"},
		{"100\n 100 99 -bash\n 150 100 aitch launch\n 200 150 claude --model opus\n", "kill -TERM 200",
			"claude does not run straight under the pane's shell"},
		{"100\n 100 99 -zsh\n 200 100 claude\n", "kill -TERM 200", "no flags show on its command line"},
	} {
		cmd, notes, err := frozenRestart(fakeRestart(c.procs, file), RemoteTab{"thinkpad", "cortex:lead"})
		joined := strings.Join(notes, "; ")
		if err != nil || !strings.Contains(cmd, c.want) || !strings.Contains(joined, c.note) || !strings.Contains(joined, "says busy") {
			t.Errorf("%q: %v\n%s\nnotes: %s", c.procs, err, cmd, joined)
		}
	}
}

// A control character read from the box (a Ctrl-U in a flag's value, which would wipe the start of the line the pane's
// shell is given; an ESC in the folder; a newline) builds no command; Crius is told why.
func TestFrozenRestartRefusesControlCharacters(t *testing.T) {
	for _, info := range []string{
		"/h/.claude/sessions/200.json\n" + `{"sessionId":"s-1","cwd":"/w"}` + "\nclaude\x01--append-system-prompt\x01x\x15; rm -rf ~\x01\n",
		"/h/.claude/sessions/200.json\n" + `{"sessionId":"s-1","cwd":"/w\u001b[2J"}` + "\n\n",
		"/h/.claude/sessions/200.json\n" + `{"sessionId":"s-1","cwd":"/w"}` + "\nclaude\x01--append-system-prompt\x01a\x02b\x01\n",
	} {
		cmd, _, err := frozenRestart(fakeRestart("100\n 100 99 -bash\n 200 100 claude\n", info), RemoteTab{"legion", "quotes:dev"})
		if err == nil || !strings.Contains(err.Error(), "control character") {
			t.Errorf("%q: built %q, %v", info, cmd, err)
		}
	}
}
