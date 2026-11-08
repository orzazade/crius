package main

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestHelperSleeps is the process the tests below start as Claude Code: the test binary itself, as macOS hides the
// environment of its own (platform) binaries such as /bin/sleep but not of others, Claude Code included.
func TestHelperSleeps(t *testing.T) {
	if os.Getenv("CRIUS_HELPER_SLEEP") != "1" {
		t.Skip("run only as the helper process")
	}
	time.Sleep(30 * time.Second)
}

// The paid-usage check reads each Claude Code process's environment and arguments from the kernel: real processes
// started as "claude" and as a release under .../claude/versions/ (as the daemon resumes a session; review of the check
// P1), with an API key and a value holding spaces and "=", are listed with exactly that environment; one not named so is
// not listed.
func TestClaudesReadsEachClaudeProcessEnvironment(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"CRIUS_HELPER_SLEEP": "1", "ANTHROPIC_API_KEY": "sk-test",
		"CLAUDE_CONFIG_DIR": "/Users/x/.claude-work", "NOTE": "a b=c d", "HOME": "/Users/x"}
	start := func(argv0 string) *exec.Cmd {
		c := exec.Command(self, "-test.run=^TestHelperSleeps$")
		c.Args[0] = argv0
		for k, v := range want {
			c.Env = append(c.Env, k+"="+v)
		}
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Process.Kill(); c.Wait() })
		return c
	}
	named, release, other := start("claude"), start("/Users/x/.local/share/claude/versions/2.1.319"), start("helper")
	cs, err := claudes()
	if err != nil {
		t.Fatal(err)
	}
	found := map[int]bool{}
	for _, c := range cs {
		if c.PID == other.Process.Pid {
			t.Errorf("a process not named claude is listed: %+v", c)
		}
		if c.PID != named.Process.Pid && c.PID != release.Process.Pid {
			continue
		}
		found[c.PID] = true
		if c.Err != nil {
			t.Errorf("pid %d: %v", c.PID, c.Err)
		}
		for k, v := range want {
			if c.Env[k] != v {
				t.Errorf("%s = %q, want %q (env %v)", k, c.Env[k], v, c.Env)
			}
		}
		if len(c.Env) != len(want) || len(c.Args) != 2 || c.Args[1] != "-test.run=^TestHelperSleeps$" {
			t.Errorf("args %q env %v", c.Args, c.Env)
		}
	}
	if !found[named.Process.Pid] || !found[release.Process.Pid] {
		t.Fatalf("pids %d (claude), %d (release) listed: %v of %d", named.Process.Pid, release.Process.Pid, found, len(cs))
	}
}

func TestIsClaude(t *testing.T) {
	for cmd, want := range map[string]bool{
		"/Users/x/.local/bin/claude --session-id 1":                                       true,
		"claude bg-pty-host --bg-pty-host":                                                true,
		"/Users/x/.local/share/claude/ClaudeCode.app/Contents/MacOS/claude --bg-pty-host": true,
		"/Users/x/.local/share/claude/versions/2.1.319 --session-id 23d7a2d7 --resume x":  true,
		"node /usr/local/lib/node_modules/@anthropic-ai/claude-code/cli.js -p hi":         true,
		"/Applications/cmux.app/Contents/Resources/bin/cmux hooks claude":                 false,
		"/opt/homebrew/bin/node /x/versions/2.1.0":                                        false,
		"/usr/bin/python3 /x/claude/versions.py":                                          false,
	} {
		if got := isClaude(splitWords(cmd)); got != want {
			t.Errorf("%q: %v", cmd, got)
		}
	}
}

func splitWords(s string) []string {
	var out []string
	cur := ""
	for _, r := range s + " " {
		if r == ' ' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		cur += string(r)
	}
	return out
}

func TestParseProcArgsRefusesACutBuffer(t *testing.T) {
	for _, b := range [][]byte{nil, {2, 0, 0, 0}, append([]byte{3, 0, 0, 0}, "/bin/sleep\x00\x00sleep\x0030\x00"...)} {
		if _, _, err := parseProcArgs(b); err == nil {
			t.Errorf("%q: no error", b)
		}
	}
}

// A claude that exits between the ps listing and the read of its arguments is exiting, then a zombie until its parent
// reaps it: its pid still answers, its arguments do not. It has ended, so claudes leaves it out instead of failing the
// check (review of the check P2-1: the zombie test alone failed 1 run in 300, in the exiting window). ps shows a
// zombie as "(claude)", so the race is the only way in.
func TestAnExitingClaudeHasEnded(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c := exec.Command(self, "-test.run=^$")
	c.Args[0] = "claude"
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer c.Wait()
	for end := time.Now().Add(10 * time.Second); ; time.Sleep(time.Millisecond) {
		if _, _, err := procArgs(c.Process.Pid); err != nil { // exiting: its arguments are gone, its pid not yet
			break
		}
		if time.Now().After(end) {
			t.Fatal("the helper did not exit")
		}
	}
	if !ended(c.Process.Pid) {
		t.Errorf("an exiting claude (pid %d) reads as running", c.Process.Pid)
	}
}
