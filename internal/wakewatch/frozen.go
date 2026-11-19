package wakewatch

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// A frozen tab draws what is typed into its box but never sends it: Enter and Ctrl-C do nothing (11-19 16:48-21:20:
// Legion quotes:dev, builder-2 and qa after a Claude Code auto-update, "Update installed · restart to apply"; about
// 4.5 h of three tabs lost). Its screen reads as idle, so nothing woke anyone. The provider-error sweep reads each
// remote tab every 5 min; a tab whose box holds text while no turn runs and no dialog is open, with the screen above
// the box unchanged at a look frozenAfter or more later, has not taken what it was given. Crius is told once per such
// spell, with the one command that restarts its claude in the same session, and the line that wakes it after.
//
// The sweep never runs that command itself: the screen cannot tell a frozen claude from a draft someone left in the box
// (both hold text, no turn runs, nothing above the box changes), and the restart stops a process on another box, which
// the sweep's restart (one typed line) and Restart (restart.go) never do. The update notice is shown as a hint only.

// frozenAfter is how long a box must hold unsent text, nothing above it changing, before Crius hears.
const frozenAfter = 4 * time.Minute

// FrozenText is what wakes a tab after its claude was restarted.
const FrozenText = "continue: your claude was restarted in the same session after it stopped taking input; carry on where you were and re-arm your waiter"

// frozenLook is a remote tab's box holding unsent text: the key of the screen above the box, when first seen so, and
// whether Crius was told.
type frozenLook struct {
	Key   string
	Since time.Time
	Said  bool
}

// updateRe is Claude Code's notice that an update waits for a restart.
var updateRe = regexp.MustCompile(`(?i)update installed[^\n]*?restart to apply|restart to apply`)

// Unsent gives the text a tab's input box holds while no turn runs and no dialog is open, and a key of the screen above
// the box. ok is false for a screen with no box, a working line, a dialog, or an empty box (a grey suggestion only,
// once dim text is taken out). The key is the last unsentLines lines above the box that are not blank: a turn that ran
// shows there, while a box that grows (lines typed into a frozen box) only pushes lines off the top of the screen. It
// leaves out white space, so a resize that rewraps keeps it, and each "✻" line, whose count of shells still running
// changes while the tab sits.
func Unsent(screen string) (typed, key string, ok bool) {
	body, _, boxed := split(screen)
	if !boxed || spinning(screen) || strings.Contains(screen, "esc to interrupt") || LivePrompt(screen) {
		return "", "", false
	}
	if typed, _ = Prompt(screen); typed == "" {
		return "", "", false
	}
	var kept []string
	for _, l := range strings.Split(body, "\n") {
		if l = strings.Join(strings.Fields(l), ""); l != "" && !strings.HasPrefix(l, "✻") {
			kept = append(kept, l)
		}
	}
	return typed, hash(strings.Join(kept[max(0, len(kept)-unsentLines):], "\n")), true
}

// unsentLines is how many lines above the box Unsent's key reads.
const unsentLines = 8

// frozenAlert is what Crius is told about a frozen tab: what sits in its box and since when, the update hint, and the
// restart, or why no restart command could be built.
func frozenAlert(d ProviderDeps, t RemoteTab, screen, typed string, f frozenLook) string {
	short := []rune(strings.Join(strings.Fields(typed), " "))
	if len(short) > 120 {
		short = append(short[:120], '…')
	}
	msg := fmt.Sprintf("tab %s stopped taking input: %q has sat unsent in its box for %d min (first seen %s), no turn runs "+
		"and nothing above the box changed", t, string(short), int(d.Now.Sub(f.Since).Minutes()), f.Since.Format("15:04"))
	if _, foot, _ := split(screen); updateRe.MatchString(foot) {
		msg += fmt.Sprintf("; its screen says %q", strings.TrimSpace(updateRe.FindString(foot)))
	}
	cmd, notes, err := frozenRestart(d, t)
	if err != nil {
		return msg + fmt.Sprintf(". No restart command: %v. By hand: kill -TERM its claude, then run claude --resume "+
			"<its session id> with the same flags in the same window, then crius-tsend %s %s %s", err, t.Box, t.Target, shq(FrozenText))
	}
	msg += ". Not restarted by the watchdog: a draft or a lost Enter looks the same. If a crius-tsend to it fails too, it is " +
		"frozen: restart it in the same session: " + cmd + " ; then, once its box shows: crius-tsend " + t.Box + " " + t.Target + " " + shq(FrozenText)
	if len(notes) > 0 {
		msg += ". Check first: " + strings.Join(notes, "; ")
	}
	return msg
}

// frozenRestart builds the command that restarts a tab's claude in the same session from the box: its pane's processes
// (Procs), and its claude's session file and command line (Claude). notes name what the command may get wrong.
func frozenRestart(d ProviderDeps, t RemoteTab) (cmd string, notes []string, err error) {
	out, err := d.Procs(t)
	if err != nil {
		return "", nil, fmt.Errorf("could not read the processes of the tab: %v", err)
	}
	top, claudes, _, _, err := paneProcs(out)
	if err != nil {
		return "", nil, err
	}
	if len(claudes) != 1 {
		return "", nil, fmt.Errorf("%d claude processes run in its pane", len(claudes))
	}
	c := claudes[0]
	info, err := d.Claude(t, c.pid)
	if err != nil {
		return "", nil, fmt.Errorf("could not read claude %d's session file: %v", c.pid, err)
	}
	s, argv, err := claudeInfo(info)
	if err != nil {
		return "", nil, fmt.Errorf("claude %d: %v", c.pid, err)
	}
	if s.Status == "busy" {
		notes = append(notes, "its session file says busy, so make sure no turn runs")
	}
	if argv == nil { // no /proc (macOS): ps joins the words with spaces and drops their quotes
		argv = strings.Fields(c.args)
		notes = append(notes, "its flags come from ps, which loses quotes: check a value with spaces")
	}
	if len(argv) == 1 {
		notes = append(notes, "no flags show on its command line: if it ran with flags, add them")
	}
	args, dropped := resumeArgs(argv[1:])
	for _, w := range dropped {
		notes = append(notes, fmt.Sprintf("left out %q, read as a start prompt", w))
	}
	// the config folder its session file lies in, always named: the pane's shell may export another one
	line := []string{"cd", shq(s.Cwd), "&&", "CLAUDE_CONFIG_DIR=" + shq(filepath.Dir(filepath.Dir(s.File))),
		shq(argv[0]), "--resume", shq(s.SessionID)}
	for _, a := range args {
		line = append(line, shq(a))
	}
	resume := strings.Join(line, " ")
	// A control character from the box (a tab, an ESC, a Ctrl-U in a flag's value or the folder name) would act as a key
	// where the command is typed: in Crius's tab, or by send-keys in the pane's shell, where Ctrl-U wipes the line start.
	if i := strings.IndexFunc(resume, unicode.IsControl); i >= 0 {
		return "", nil, fmt.Errorf("claude %d's folder, session id or flags hold a control character (%q), which would act as "+
			"a key where the command is typed", c.pid, resume[i:i+1])
	}
	pid := strconv.Itoa(c.pid)
	var remote string
	if c.pid == top.pid || !loginShell(top) {
		// claude is the pane's own process, or runs under a command (bash -c …) that ends with it: no shell is left to
		// type into, so the pane starts again with the resume
		remote = "tmux respawn-pane -k -t " + shq(t.Target) + " " + shq(resume)
	} else {
		if c.ppid != top.pid {
			notes = append(notes, "claude does not run straight under the pane's shell, so check the pane shows a shell prompt before the resume is typed")
		}
		// the resume is typed only once that claude is gone; one that outlives TERM by 10 s is said, and nothing is typed
		remote = "kill -TERM " + pid + " && for i in 1 2 3 4 5 6 7 8 9 10; do sleep 1; kill -0 " + pid + " 2>/dev/null || break; done" +
			" && if kill -0 " + pid + " 2>/dev/null; then echo 'claude " + pid + " still runs 10 s after TERM: not resumed'; exit 1; fi" +
			" && tmux send-keys -t " + shq(t.Target) + " -l " + shq(resume) + " && tmux send-keys -t " + shq(t.Target) + " Enter"
	}
	ssh, err := d.SSH(t)
	if err != nil {
		return "", nil, err
	}
	var words []string
	for _, w := range ssh {
		words = append(words, shq(w))
	}
	return strings.Join(words, " ") + " " + shq(remote), notes, nil
}

// loginShell says whether a pane's own process is an interactive shell (bash, -bash, zsh …), not one running a command.
func loginShell(p proc) bool {
	w := strings.Fields(p.args)
	return jobShells[strings.TrimPrefix(filepath.Base(w[0]), "-")] && !slices.Contains(w[1:], "-c")
}

// claudeSession is a claude's session file on its box (~/.claude*/sessions/<pid>.json) and where it lies.
type claudeSession struct {
	File      string
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	Status    string `json:"status"`
}

// claudeInfo reads Claude's answer: the session file's path, its JSON on one line, then the claude's raw command line
// from /proc (NUL as \x01, newline as \x02), empty where the box has no /proc (argv nil then).
func claudeInfo(out string) (s claudeSession, argv []string, err error) {
	lines := strings.SplitN(out, "\n", 3)
	if len(lines) < 2 || strings.TrimSpace(lines[0]) == "" {
		return s, nil, fmt.Errorf("no session file")
	}
	if err := json.Unmarshal([]byte(lines[1]), &s); err != nil {
		return s, nil, fmt.Errorf("its session file %s: %v", lines[0], err)
	}
	s.File = strings.TrimSpace(lines[0])
	if s.SessionID == "" || s.Cwd == "" {
		return s, nil, fmt.Errorf("its session file %s names no session id or folder", s.File)
	}
	if len(lines) == 3 {
		if raw := strings.TrimRight(lines[2], "\n"); raw != "" {
			for _, a := range strings.Split(strings.TrimSuffix(raw, "\x01"), "\x01") {
				argv = append(argv, strings.ReplaceAll(a, "\x02", "\n"))
			}
		}
	}
	return s, argv, nil
}

// resumeFlags name the session to open: the restart names it again with --resume, so these and their values go.
var resumeFlags = map[string]bool{"--resume": true, "-r": true, "--session-id": true}

// noResumeFlags open another session than the one named: they go.
var noResumeFlags = map[string]bool{"--continue": true, "-c": true, "--fork-session": true}

// valueFlags take the word after them; listFlags every word up to the next option, as Claude Code's parser reads them.
var (
	valueFlags = map[string]bool{"--model": true, "--permission-mode": true, "--append-system-prompt": true,
		"--system-prompt": true, "--settings": true, "--setting-sources": true, "--agent": true, "--agents": true,
		"--fallback-model": true, "--effort": true, "-n": true, "--name": true, "--output-format": true,
		"--input-format": true, "--plugin-dir": true, "--permission-prompt-tool": true, "--max-turns": true,
		"--max-budget-usd": true, "--json-schema": true, "--append-system-prompt-file": true, "--system-prompt-file": true}
	listFlags = map[string]bool{"--add-dir": true, "--mcp-config": true, "--allowedTools": true, "--allowed-tools": true,
		"--disallowedTools": true, "--disallowed-tools": true, "--tools": true, "--betas": true}
)

// resumeArgs gives a claude's arguments for a restart in the same session: the ones that pick a session out, and each
// word that is no option's value (a start prompt, which would be sent again), named in dropped.
func resumeArgs(in []string) (out, dropped []string) {
	list := false
	for i := 0; i < len(in); i++ {
		a := in[i]
		name, _, inline := strings.Cut(a, "=")
		switch {
		case a == "--":
			return out, append(dropped, in[i+1:]...)
		case resumeFlags[name]:
			list = false
			if !inline && i+1 < len(in) && !strings.HasPrefix(in[i+1], "-") {
				i++
			}
		case noResumeFlags[a]:
			list = false
		case strings.HasPrefix(a, "-"):
			out = append(out, a)
			list = listFlags[name] && !inline
			if valueFlags[name] && !inline && i+1 < len(in) {
				i++
				out = append(out, in[i])
			}
		case list:
			out = append(out, a)
		default:
			dropped = append(dropped, a)
		}
	}
	return out, dropped
}

// shellSafe words need no quotes in a POSIX shell.
var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_./:=@%+,-]+$`)

// shq quotes a word for a POSIX shell, leaving a safe one bare.
func shq(s string) string {
	if shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
