package wakewatch

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/orzazade/crius/internal/sessionstate"
)

// Exit codes of crius-wakewatch restart.
const (
	RestartReady   = 0 // T3: idle and no claude runs for the role, send the brief now; tmux: sent, or idle when no brief was given
	RestartFailed  = 2 // a look failed or the role is unknown: nothing is known, nothing was sent
	RestartBusy    = 3 // a turn, a background job, a dialog or a request is open: wait for it, then run again
	RestartLive    = 4 // T3: claude processes still run for the role: stop them, then run again
	RestartNotSent = 5 // tmux: crius-tsend said NOT SENT for /clear or the brief
	RestartMaybe   = 6 // tmux: /clear or the brief may have gone (crius-tsend MAYBE SENT, or no answer): look, never send again
)

// RestartDeps is what Restart reads and does. Tabs lists the remote tmux tabs (provider-tabs.txt), whose session:window
// is the role's bus name; Capture reads one with dim text taken out; Procs gives the tab's pane pid on its first line,
// then its box's process list (`ps -ww -A -o pid=,ppid=,args=`, the same on Linux and macOS), then, where the box has
// /proc, a "--- cmdline" line and one "<pid> <argv>" line per tool shell with its raw cmdline (NUL as \x01, newline as
// \x02: ps shows a newline as a space); SendTab types one line with crius-tsend.
type RestartDeps struct {
	Home     string
	Out      io.Writer
	Sessions func() ([]Session, error)
	States   func() ([]sessionstate.State, error)
	Thread   func(id string) (T3Thread, error)
	Tabs     func() ([]RemoteTab, error)
	Capture  func(RemoteTab) (string, error)
	Procs    func(RemoteTab) (string, error)
	SendTab  func(t RemoteTab, text string) error
}

// Restart is the one safe fresh restart of a role (11-17, three times: a fresh send or /clear given while a turn ran was
// queued behind it; the old claude ran on beside the new one, or the brief ran in the old context). It never stops a
// process itself. It refuses while the role has a turn open, and says ready only once nothing runs:
//
//   - a T3 thread: refuse while a run or a request is open on any thread the role's claudes run in; then list every live
//     claude of the role (its claude status file, by name or crius-hook's record) and say ready only when none is left.
//     T3 keeps a thread's claude running between turns, and its stop does nothing on an idle thread (no_active_run), so
//     a claude left is stopped by its pid. The t3 CLI cannot send with a fresh context either: Restart prints the exact
//     MCP call for the send, and is run again after each step.
//   - a tmux tab on another box: refuse unless the tab is idle (no working line, no background job, no dialog); with a
//     brief, type /clear, check the tab is idle again, then type the brief. /clear keeps the same claude process.
func Restart(d RestartDeps, role, brief string) int {
	say := func(code int, format string, a ...any) int {
		fmt.Fprintf(d.Out, format+"\n", a...)
		return code
	}
	role = strings.ToLower(strings.TrimSpace(role))
	project, name, ok := strings.Cut(role, ":")
	if !ok || project == "" || name == "" {
		return say(RestartFailed, "FAILED: give the role as <project>:<role> (axi:ms-flow), not %q", role)
	}
	tabs, err := d.Tabs()
	if err != nil {
		return say(RestartFailed, "FAILED %s: could not read the remote tab list: %v", role, err)
	}
	for _, t := range tabs {
		if strings.EqualFold(t.Target, role) {
			return restartTab(d, say, role, t, brief)
		}
	}
	ss, err := d.Sessions()
	if err != nil {
		return say(RestartFailed, "FAILED %s: could not list the live claude sessions: %v", role, err)
	}
	states, err := d.States()
	if err != nil {
		return say(RestartFailed, "FAILED %s: could not read crius-hook's session records: %v", role, err)
	}
	live := byRole(ss, states)[role]
	var threads []string // the threads the role's live claudes run in, each once
	for _, s := range live {
		for _, st := range states {
			if st.SessionID == s.ID && st.Thread != "" && !slices.Contains(threads, st.Thread) {
				threads = append(threads, st.Thread)
			}
		}
	}
	if len(threads) == 0 {
		r, err := sessionstate.ReadRole(filepath.Join(d.Home, "roles"), project, name)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return say(RestartFailed, "FAILED %s: could not read its role file: %v", role, err)
		}
		if err == nil {
			if th := newestThread(d.Home, project, name, r, states); th != "" {
				threads = append(threads, th)
			}
		}
	}
	if len(threads) == 0 {
		return say(RestartFailed, "FAILED %s: no T3 thread or remote tab known for it (no live claude names one, its role file "+
			"names none, and provider-tabs.txt lists no such tab)", role)
	}
	for _, thread := range threads {
		th, err := d.Thread(thread)
		if err != nil {
			return say(RestartFailed, "FAILED %s: could not read T3 thread %s, so it is not known to be idle: %v", role, thread, err)
		}
		if th.Active || th.Status == "running" {
			return say(RestartBusy, "BUSY %s: T3 thread %s has a turn running (status %s). A fresh send now is queued behind it "+
				"and leaves the old claude running. Wait for the turn to end, then run crius-wakewatch restart %s again.", role, thread, th.Status, role)
		}
		if th.Pending > 0 {
			return say(RestartBusy, "BUSY %s: T3 thread %s has %d request(s) waiting (an approval or a question). Answer or "+
				"decline them, then run crius-wakewatch restart %s again.", role, thread, th.Pending, role)
		}
	}
	thread := threads[0]
	if len(live) > 0 {
		return say(RestartLive, "LIVE %s: %s still run, while T3 thread %s is idle (t3_thread_interrupt has no turn to stop). "+
			"Next: stop each by its pid (kill <pid>; never pkill -f), then run crius-wakewatch restart %s again.",
			role, describe(live, states), strings.Join(threads, ", "), role)
	}
	msg := "<the brief>"
	if brief != "" {
		msg = brief
	}
	return say(RestartReady, "READY %s: T3 thread %s is idle and no claude runs for the role. Next: t3_thread_send {threadId: %q, "+
		"freshContext: true, message: %q}. Then run crius-wakewatch restart %s once more: it must list exactly one claude.",
		role, thread, thread, msg, role)
}

// restartTab restarts a role in a remote tmux tab: refused unless idle, then /clear, idle again, then the brief.
func restartTab(d RestartDeps, say func(int, string, ...any) int, role string, t RemoteTab, brief string) int {
	// busy says why the tab is not idle ("" when it is): its screen (a turn, a dialog, an agent), then what runs under its
	// claude. A look that failed is an error, never idle.
	busy := func() (string, error) {
		text, err := d.Capture(t)
		if err != nil {
			return "", fmt.Errorf("could not read tab %s: %w", t, err)
		}
		if v := restartView(text); v != "idle" {
			return busyWhy(v), nil
		}
		out, err := d.Procs(t)
		var shells int
		var jobs []string
		if err == nil {
			shells, jobs, err = tabJobs(out)
		}
		if err != nil {
			return "", fmt.Errorf("could not read the processes of tab %s: %w", t, err)
		}
		if len(jobs) > 0 {
			return "runs background jobs besides its bus waiter (" + strings.Join(jobs, "; ") + ")", nil
		}
		if n := footJobs(text); n > shells {
			return fmt.Sprintf("counts %d background job(s) in its footer, but only %d shell(s) run under its claude: a job "+
				"of a kind not seen", n, shells), nil
		}
		return "", nil
	}
	why, err := busy()
	if err != nil {
		return say(RestartFailed, "FAILED %s: %v, so it is not known to be idle", role, err)
	}
	if why != "" {
		return say(RestartBusy, "BUSY %s: tab %s %s. /clear and a brief sent now are queued behind it, and the brief runs in "+
			"the old context. Wait, then run crius-wakewatch restart %s again.", role, t, why, role)
	}
	if brief == "" {
		return say(RestartReady, "READY %s: tab %s is idle. Next: crius-wakewatch restart %s <brief…> types /clear, then the brief.", role, t, role)
	}
	if err := d.SendTab(t, "/clear"); err != nil {
		if code := sendCode(err); code == RestartMaybe {
			return say(code, "MAYBE SENT %s: /clear to %s may have gone (%v); the brief was not sent. Look at the tab; do not "+
				"run this again until it shows an idle, empty box.", role, t, err)
		}
		return say(RestartNotSent, "NOT SENT %s: /clear to %s did not go (%v); the brief was not sent.", role, t, err)
	}
	if why, err := busy(); err != nil {
		return say(RestartFailed, "FAILED %s: the /clear went, but %v after it; the brief was not sent.", role, err)
	} else if why != "" {
		return say(RestartBusy, "BUSY %s: the /clear went, but tab %s %s after it; the brief was not sent. Send it with "+
			"crius-tsend %s %s once the tab is idle.", role, t, why, t.Box, t.Target)
	}
	if err := d.SendTab(t, brief); err != nil {
		if code := sendCode(err); code == RestartMaybe {
			return say(code, "MAYBE SENT %s: the /clear went, the brief to %s may have gone (%v). Look at the tab; never send "+
				"it again unseen.", role, t, err)
		}
		return say(RestartNotSent, "NOT SENT %s: the /clear went, the brief to %s did not (%v).", role, t, err)
	}
	return say(RestartReady, "SENT %s: /clear, then the brief, to %s.", role, t)
}

// sendCode reads a failed crius-tsend: NOT SENT (its exits 1 and 2, whose message says so) is RestartNotSent; anything
// else, its MAYBE SENT (exit 3) or no answer in time, may have typed the text, so it is RestartMaybe.
func sendCode(err error) int {
	if strings.Contains(err.Error(), "NOT SENT") {
		return RestartNotSent
	}
	return RestartMaybe
}

// restartJobsRe is the part of jobsRe that no process shows: an agent runs inside claude. Shells and monitors (and the
// older "background tasks") are left to tabJobs, since the footer counts the bus waiter too, which every idle session
// keeps and re-arms within seconds (11-17 20:08 as "1 monitor" on staffy:qa, 20:40 as "1 shell" on quotes:qa).
var restartJobsRe = regexp.MustCompile(`\b\d+ agents?\b|(?m)^\s*◯ `)

// restartView is screenView for a restart: the same "asks" and turn checks, but only an agent counts as a job on screen.
func restartView(text string) string {
	_, foot, boxed := split(text)
	switch {
	case asks(text):
		return "asks"
	case !boxed || strings.Contains(text, "esc to interrupt") || restartJobsRe.MatchString(foot):
		return "busy"
	}
	return "idle"
}

// footJobsRe counts the background jobs the footer shows that are processes under claude: shells, monitors and the
// older "background tasks". tabJobs must find at least that many tool shells, or the footer counts a job it cannot see.
var footJobsRe = regexp.MustCompile(`\b(\d+) (?:shells?|monitors?|background tasks?)\b`)

// footJobs is the number of shells, monitors and background tasks a screen's footer counts.
func footJobs(text string) int {
	_, foot, _ := split(text)
	n := 0
	for _, m := range footJobsRe.FindAllStringSubmatch(foot, -1) {
		k, _ := strconv.Atoi(m[1])
		n += k
	}
	return n
}

// waiterProgRe is a bus waiter's program, in each form the teams run one (11-17): crius-wait, team-wait.sh, a scratchpad
// waitbus.sh or buswait.sh, or bus-watch.ts. A tail of bus logs only (under ~/.crius/bus/) is one too, a monitor's.
var waiterProgRe = regexp.MustCompile(`^(?:crius-wait|team-wait\.sh|(?:bus-?wait|wait-?bus)\.sh|bus-watch\.(?:ts|sh))$`)

// interpreters run the script named by their first argument (bash team-wait.sh, bun bus-watch.ts). With an option
// first (bash -c …, bun run …) the program is the interpreter itself, never a waiter.
var interpreters = map[string]bool{"sh": true, "bash": true, "zsh": true, "bun": true, "node": true}

// jobShells start a tool's shell command: Claude Code runs each Bash or Monitor command as "<shell> -c …" under claude.
var jobShells = map[string]bool{"sh": true, "bash": true, "zsh": true}

// waiterSteps may run under a tool shell beside a live waiter: the filters of a waiter's pipe (a monitor's tail | grep,
// crius-wait | cut | tee), or a sleep (crius-wait & sleep 5; what follows it afterWaiter reads).
var waiterSteps = map[string]bool{"cd": true, "sleep": true, "true": true, "grep": true, "cat": true, "head": true,
	"cut": true, "tr": true, "tee": true} // no awk or sed: their system() and e can start a run on a bus line

// waiterHelpers are what a waiter may run under itself: the commands team-wait.sh and crius-wait use (11-17). Anything
// else under a waiter (a QA run a waitbus.sh starts on a DONE line) is a job.
var waiterHelpers = map[string]bool{"sleep": true, "tail": true, "grep": true, "sed": true, "awk": true, "cat": true,
	"head": true, "cut": true, "tr": true, "date": true, "flock": true, "basename": true, "dirname": true, "wc": true,
	"mv": true, "cp": true, "touch": true, "rm": true, "mkdir": true, "jq": true, "ps": true, "printf": true, "kill": true,
	"perl": true, "stat": true, "pgrep": true, "crius-wakewatch": true, "cmux": true}

// loaderVarRe is a setting that makes a program load and run other code before its own.
var loaderVarRe = regexp.MustCompile(`^(BASH_ENV|ENV|ZDOTDIR|LD_[A-Z_]*|DYLD_[A-Z_]*)=`)

// assignRe is a leading VAR=value of a simple command.
var assignRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// progOf is the program a command's words run: past any VAR=value, its base name, or the script an interpreter runs.
func progOf(w []string) string {
	for len(w) > 0 && assignRe.MatchString(w[0]) {
		w = w[1:]
	}
	if len(w) == 0 {
		return ""
	}
	if interpreters[filepath.Base(w[0])] && len(w) > 1 && !strings.HasPrefix(w[1], "-") {
		return filepath.Base(w[1])
	}
	return filepath.Base(w[0])
}

// isWaiter says whether a command's words run a bus waiter, by its program, never by a word in its arguments (a test
// of bin/crius-wait is a job). A tail is one only when every file it reads is a bus log.
func isWaiter(w []string) bool {
	for _, a := range w {
		if !assignRe.MatchString(a) {
			break
		}
		if loaderVarRe.MatchString(a) {
			return false // BASH_ENV=./x bash waitbus.sh runs ./x first (review r3 P2)
		}
	}
	switch prog := progOf(w); {
	case waiterProgRe.MatchString(prog):
		return true
	case prog != "tail":
		return false
	}
	bus := false
	for _, a := range w[slices.IndexFunc(w, func(s string) bool { return filepath.Base(s) == "tail" })+1:] {
		switch {
		case strings.Contains(a, "/.crius/bus/"):
			bus = true
		case !strings.HasPrefix(a, "-") && !numRe.MatchString(a):
			return false // another file: a tail of a run log beside the bus is a watch on that run
		}
	}
	return bus
}

// numRe is an option's number (tail -n 0, -c +320111).
var numRe = regexp.MustCompile(`^\+?\d+$`)

// tailOptRe is a tail option word with nothing else in it (-20, -fn5, --pid=12, --follow=name): a redirect glued on
// (-n0</tmp/run.log) or a path fails it.
var tailOptRe = regexp.MustCompile(`^(-[A-Za-z0-9+]+|--[a-z-]+(=(\+?\d+|[a-z]+))?)$`)

// tailValueRe is a tail option that takes the next word as its number (-n 20, -fn 20, --pid 12).
var tailValueRe = regexp.MustCompile(`^(-[A-Za-z]*[bcns]|--(lines|bytes|pid|sleep-interval|max-unchanged-stats))$`)

// stdinTail says whether a command's words run a tail that names no file, so it reads only its pipe: a filter like head
// or cut (team-wait.sh … | tail -20, 11-18). Every word is an option, or the number an option takes; any other word (a
// file, even one named by digits or after --, or a redirect) makes it a job unless isWaiter.
func stdinTail(w []string) bool {
	if progOf(w) != "tail" {
		return false
	}
	args := w[slices.IndexFunc(w, func(s string) bool { return filepath.Base(s) == "tail" })+1:]
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case !tailOptRe.MatchString(a):
			return false
		case tailValueRe.MatchString(a):
			if i++; i == len(args) || !numRe.MatchString(args[i]) {
				return false
			}
		}
	}
	return true
}

type proc struct {
	pid, ppid int
	args      string
}

func (p proc) prog() string { return progOf(strings.Fields(p.args)) }
func (p proc) waiter() bool { return isWaiter(strings.Fields(p.args)) }

// toolShell says whether a child of claude is a tool's shell: Claude Code's shell-snapshot marker (in whatever wrapper),
// or a shell given -c among its first options (bash -c, bash -l -c).
func (p proc) toolShell() bool {
	if strings.Contains(p.args, "/shell-snapshots/snapshot-") {
		return true
	}
	w := strings.Fields(p.args)
	return jobShells[filepath.Base(w[0])] && slices.Contains(w[1:min(len(w), 3)], "-c")
}

// shellText is the command a tool shell runs, from its raw argv joined (or its ps line): within Claude Code's wrapper
// the text it evals, its quotes put back.
func shellText(args string) string {
	if j := strings.Index(args, "eval '"); j >= 0 {
		args = args[j+len("eval '"):]
		if k := strings.LastIndex(args, "' < /dev/null"); k >= 0 { // the last: the command may hold the text too
			args = args[:k]
		}
		args = strings.NewReplacer(`'"'"'`, "'", `'\''`, "'").Replace(args)
	}
	return args
}

// afterSafe are what may follow a waiter in its command: steps that end at once and start nothing (crius-wait || true,
// waitbus.sh; echo "woke $?"), given no substitution in them. No printf: printf -v takes a name whose array index the
// shell evaluates, which can run a command (review r3).
var afterSafe = map[string]bool{"true": true, "false": true, ":": true, "echo": true, "cat": true, "head": true,
	"wc": true, "date": true, "exit": true}

// openers start a compound command or a group a waiter may run in (if crius-wait; then …), and runners run the command
// that follows them (env A=1 crius-wait): both are passed to find the program a step runs.
var openers = map[string]bool{"(": true, "{": true, "!": true, "if": true, "then": true, "elif": true, "else": true,
	"while": true, "until": true, "do": true, "time": true}
var runners = map[string]bool{"env": true, "nice": true, "exec": true, "command": true, "nohup": true}

// runsWaiter says whether a step's words run a waiter named in progs (isWaiter), past a function's head, openers,
// runners and their options and settings, rather than only name it (cat > /tmp/s/waitbus.sh, chmod +x …/waitbus.sh, review r2 P2).
func runsWaiter(w []string, progs []string) bool {
	if len(w) >= 3 && w[1] == "(" && w[2] == ")" { // a function's body: w() { crius-wait; }
		w = w[3:]
	}
	for len(w) > 0 && openers[w[0]] {
		w = w[1:]
	}
	for len(w) > 0 && runners[filepath.Base(w[0])] {
		w = w[1:]
		for len(w) > 0 && (strings.HasPrefix(w[0], "-") || assignRe.MatchString(w[0])) {
			w = w[1:]
		}
	}
	return isWaiter(w) && slices.Contains(progs, progOf(w))
}

// closers end a group or a compound command a waiter sits in: ( crius-wait ), { crius-wait; }.
var closers = map[string]bool{")": true, "}": true, "fi": true, "done": true, "esac": true}

// afterWaiter says what a shell's command chains after the waiter now running, which has not started, so no process
// shows it (review 11-18: `waitbus.sh && bun run deploy` read as idle, and the deploy would start under the new brief).
// It finds the first step that runs one of progs, the running waiters' programs (runsWaiter), else the first that names
// one as a word of its own (a message or a here-doc that names it is neither), and gives the first step after it that
// is not in afterSafe, closers left out; "" when none. The steps before that one are never judged (the misreads of
// 11-18). ok is false when no step names a waiter (one run through a function or a variable): then what follows it is
// not known.
func afterWaiter(cmd string, progs []string) (step string, ok bool) {
	steps := shellSteps(cmd)
	at := slices.IndexFunc(steps, func(st shellStep) bool { return runsWaiter(st.words, progs) })
	if at < 0 {
		at = slices.IndexFunc(steps, func(st shellStep) bool {
			return slices.ContainsFunc(st.words, func(w string) bool { return slices.Contains(progs, filepath.Base(w)) })
		})
	}
	if at < 0 {
		return "", false
	}
	rest := steps[at+1:]
	for len(rest) > 0 && rest[0].piped { // the waiter's own pipe runs now: its processes are judged
		rest = rest[1:]
	}
	for _, st := range rest {
		w := st.words
		for len(w) > 0 && closers[w[0]] {
			w = w[1:]
		}
		if len(w) == 0 {
			continue
		}
		if !afterSafe[w[0]] || st.odd {
			return strings.TrimSpace(st.text), true
		}
	}
	return "", true
}

// shellStep is one simple command of a shell command: its text, its words with their quotes taken out, whether it
// holds a substitution or an expansion that can run a command ($(…), `…`, <(…), >(…), $[…], ${x:…}: plainParam), and
// whether a pipe joins it to the step before.
type shellStep struct {
	text  string
	words []string
	odd   bool
	piped bool
}

// shellSteps splits a shell command into its simple commands where the shell does: at ; & && || ;; and newlines outside
// quotes ('…', "…", $'…'), escapes and # comments, with here-doc bodies left out, and at a pipe (| or |&, not >|),
// whose next step is marked piped. ( and ) are words of their own. A quote left open makes the rest one odd step.
func shellSteps(cmd string) []shellStep {
	const ansi = 1 // quote: inside $'…', where a backslash escapes the next byte
	var steps []shellStep
	var text, word strings.Builder
	var words, heredocs []string
	quote, odd, inWord, piped := byte(0), false, false, false
	endWord := func() {
		if inWord {
			words, inWord = append(words, word.String()), false
			word.Reset()
		}
	}
	endStep := func(pipe bool) {
		endWord()
		steps = append(steps, shellStep{text.String(), words, odd, piped})
		text.Reset()
		words, odd, piped = nil, false, pipe
	}
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		switch {
		case quote != 0 && c == '\\' && quote != '\'' && i+1 < len(cmd):
			text.WriteByte(c)
			i++
			text.WriteByte(cmd[i])
			word.WriteByte(cmd[i])
			continue
		case quote != 0:
			if c == quote || quote == ansi && c == '\'' {
				quote = 0
			} else {
				word.WriteByte(c)
			}
			if quote == '"' && (c == '`' || c == '$' && !plainParam(cmd[i+1:])) {
				odd = true
			}
		case c == '\\' && i+1 < len(cmd):
			text.WriteByte(c)
			i++
			if cmd[i] == '$' { // \$ is a plain $, which opens no $'…' (ansiQuote)
				word.WriteByte('\\')
			}
			if cmd[i] != '\n' { // a backslash-newline joins two lines
				word.WriteByte(cmd[i])
				inWord = true
			}
			text.WriteByte(cmd[i])
			continue
		case c == '#' && !inWord:
			for i+1 < len(cmd) && cmd[i+1] != '\n' {
				i++
			}
			continue
		case c == '\'' && ansiQuote(word.String()):
			quote, inWord = ansi, true
			s := word.String()
			word.Reset()
			word.WriteString(s[:len(s)-1]) // the $ of $'…' is no part of the word
		case c == '\'' || c == '"':
			quote, inWord = c, true
		case c == '`' || (c == '<' || c == '>') && i+1 < len(cmd) && cmd[i+1] == '(' || c == '$' && !plainParam(cmd[i+1:]):
			odd = true
			word.WriteByte(c)
			inWord = true
		case strings.HasPrefix(cmd[i:], "<<<"): // a here-string, no here-doc (review r2 P2)
			endWord()
			text.WriteString("<<<")
			i += 2
			continue
		case c == '<' && strings.HasPrefix(cmd[i:], "<<"):
			// a here-doc: its delimiter is the next word; its body, from the next line, is left out
			j := i + 2
			if j < len(cmd) && cmd[j] == '-' {
				j++
			}
			for j < len(cmd) && (cmd[j] == ' ' || cmd[j] == '\t') {
				j++
			}
			k := j
			for k < len(cmd) && !strings.ContainsRune(" \t\n;&|<>()", rune(cmd[k])) {
				k++
			}
			heredocs = append(heredocs, strings.Trim(cmd[j:k], `'"\`))
			endWord()
			text.WriteString(cmd[i:k])
			i = k - 1
			continue
		case c == '\n' && len(heredocs) > 0:
			endStep(false)
			for _, delim := range heredocs { // skip each body up to its delimiter line
				for i < len(cmd) {
					line, _, _ := strings.Cut(cmd[i+1:], "\n")
					i += len(line) + 1
					if strings.TrimLeft(line, "\t") == delim {
						break
					}
				}
			}
			heredocs = nil
			continue
		case c == ';' || c == '\n' || c == '|' && i+1 < len(cmd) && cmd[i+1] == '|' ||
			c == '&' && (i == 0 || cmd[i-1] != '>') && (i+1 == len(cmd) || cmd[i+1] != '>'):
			if i+1 < len(cmd) && cmd[i+1] == c { // && || ;;
				i++
			}
			endStep(false)
			continue
		case c == '|' && (i == 0 || cmd[i-1] != '>'):
			if i+1 < len(cmd) && cmd[i+1] == '&' { // |& pipes stderr too
				i++
			}
			endStep(true)
			continue
		case c == ' ' || c == '\t':
			endWord()
		case c == '(' || c == ')':
			endWord()
			words = append(words, string(c))
		default:
			word.WriteByte(c)
			inWord = true
		}
		text.WriteByte(c)
	}
	if quote != 0 {
		odd, words = true, append(words, "") // what follows an open quote is not known
	}
	endStep(false)
	return steps
}

// ansiQuote says whether a ' after a word's text opens a $'…' quote: the text ends in a $ the shell reads as a $, an
// odd number of them unescaped ($$ is the shell's pid, \$ a plain $).
func ansiQuote(text string) bool {
	n := len(text) - len(strings.TrimRight(text, "$"))
	if n > 0 && n < len(text) && text[len(text)-n-1] == '\\' {
		n--
	}
	return n%2 == 1
}

// plainParam says whether text after a $ makes a plain parameter expansion that runs nothing: $name, ${name}, $1,
// ${10}, a special one ($?, $$ …), or the opening of a $'…' or $"…" quote. Anything else ($(…), $[x], ${y:x}, ${!x},
// $~x) can run a command a variable holds.
func plainParam(rest string) bool {
	if rest == "" {
		return true
	}
	if c := rest[0]; c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte(`$?#!@*-'"`, c) >= 0 {
		return true
	}
	inner, _, ok := strings.Cut(strings.TrimPrefix(rest, "{"), "}")
	return rest[0] == '{' && ok && paramNameRe.MatchString(inner)
}

// paramNameRe is the name inside a plain ${…}.
var paramNameRe = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*|[0-9]+|[?#!@*$-])$`)

// maxNamed is how many live processes a refusal names per shell; a test run's tree can hold hundreds.
const maxNamed = 3

// tabJobs reads Procs' answer (the pane pid, ps lines, then optionally a "--- cmdline" line and one "<pid> <argv>" line
// per tool shell) and gives the number of tool shells of the claude in that pane and the ones among them that run a
// job. It judges by what is alive under each shell, never by the steps of its command (11-18 17:12: a `sed -i` long
// finished before its waitbus.sh read as a job four times; the text check misread here-docs, $(…) and settings both
// ways). A shell is quiet only while a bus waiter runs under it, and all else alive under it is a waiter, a pipe
// filter beside one (waiterSteps, a stdinTail) or, under a waiter, one of waiterHelpers. A step that ran before the
// waiter has ended and shows nowhere; a lane runner, a test or a build alive under the shell is a job. Work chained
// after the waiter has not started, so afterWaiter reads it from the raw command line (ps shows a newline as a space),
// or from the ps line when the box gave none (macOS). Work a step detached from the shell before the waiter shows
// nowhere: a known limit. MCP servers, which claude starts without a shell, are no shells. A pane with no claude is an
// error, never idle.
func tabJobs(out string) (shells int, jobs []string, err error) {
	first, rest, _ := strings.Cut(strings.TrimLeft(out, " \t\n"), "\n")
	pane, err := strconv.Atoi(strings.TrimSpace(first))
	if err != nil {
		return 0, nil, fmt.Errorf("no pane pid in its answer %.60q", first)
	}
	rest, cmdlines, _ := strings.Cut(rest, "--- cmdline\n")
	argv := map[int]string{} // a tool shell's raw argv, joined by spaces, its newlines kept; "" where /proc gave none
	for _, l := range strings.Split(cmdlines, "\n") {
		id, a, _ := strings.Cut(l, " ")
		if pid, err := strconv.Atoi(id); err == nil {
			argv[pid] = strings.ReplaceAll(strings.ReplaceAll(strings.TrimSuffix(a, "\x01"), "\x02", "\n"), "\x01", " ")
		}
	}
	byPid := map[int]proc{}
	kids := map[int][]proc{}
	for _, l := range strings.Split(rest, "\n") {
		f := strings.Fields(l)
		if len(f) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		p := proc{pid, ppid, strings.Join(f[2:], " ")}
		byPid[pid] = p
		kids[ppid] = append(kids[ppid], p)
	}
	top, ok := byPid[pane]
	if !ok {
		return 0, nil, fmt.Errorf("its pane pid %d is not in the process list", pane)
	}
	// the claudes in the pane: the pane's process or its nearest descendants named claude
	var claudes []proc
	for queue := []proc{top}; len(queue) > 0; queue = queue[1:] {
		if p := queue[0]; filepath.Base(strings.Fields(p.args)[0]) == "claude" {
			claudes = append(claudes, p)
		} else {
			queue = append(queue, kids[p.pid]...)
		}
	}
	if len(claudes) == 0 {
		return 0, nil, fmt.Errorf("no claude process runs in its pane (pid %d, %.40s)", pane, top.args)
	}
	// odd collects what runs under pid that is not in allowed nor a waiter, nor under a waiter one of waiterHelpers
	var odd func(pid int, allowed map[string]bool, found []proc) []proc
	odd = func(pid int, allowed map[string]bool, found []proc) []proc {
		for _, k := range kids[pid] {
			switch {
			case k.waiter():
				found = odd(k.pid, waiterHelpers, found)
			case allowed[k.prog()] || stdinTail(strings.Fields(k.args)):
				found = odd(k.pid, allowed, found)
			default:
				found = append(odd(k.pid, allowed, found), k)
			}
		}
		return found
	}
	for _, c := range claudes {
		for _, k := range kids[c.pid] {
			if !k.toolShell() {
				continue
			}
			shells++
			text := strings.ReplaceAll(k.args, `\012`, "\n") // no /proc (macOS): its ps shows a newline as \012
			if a, ok := argv[k.pid]; !ok && len(argv) > 0 {
				// the box lists command lines, but not this shell's: one started after the list, or one pgrep's marker
				// misses, whose newlines the ps line hides
				jobs = append(jobs, fmt.Sprintf("pid %d: %.100s (its command line could not be read)", k.pid, k.args))
				continue
			} else if a != "" {
				text = a
			}
			text = shellText(text)
			job := fmt.Sprintf("pid %d: %.100s", k.pid, strings.ReplaceAll(text, "\n", "; "))
			var progs []string // the programs of the waiters running under the shell
			for _, w := range kids[k.pid] {
				if w.waiter() {
					progs = append(progs, w.prog())
				}
			}
			var live []proc
			if len(progs) == 0 {
				live = descendants(kids, k.pid) // no waiter runs under it: all it runs is its work
			} else {
				live = odd(k.pid, waiterSteps, nil)
			}
			switch {
			case len(live) > 0:
				var names []string
				for _, p := range live[:min(len(live), maxNamed)] {
					names = append(names, fmt.Sprintf("pid %d %.80s", p.pid, p.args))
				}
				if len(live) > maxNamed {
					names = append(names, fmt.Sprintf("+%d more", len(live)-maxNamed))
				}
				jobs = append(jobs, job+", running "+strings.Join(names, ", "))
			case len(progs) == 0:
				jobs = append(jobs, job+", no bus waiter runs under it")
			default:
				if step, ok := afterWaiter(text, progs); !ok {
					jobs = append(jobs, job+", its command names no waiter that runs under it, so what follows the waiter is not known")
				} else if step != "" {
					jobs = append(jobs, fmt.Sprintf("%s, chained after its waiter: %.80s", job, strings.ReplaceAll(step, "\n", "; ")))
				}
			}
		}
	}
	return shells, jobs, nil
}

// descendants is every process under pid, each before what runs under it.
func descendants(kids map[int][]proc, pid int) []proc {
	var all []proc
	for _, k := range kids[pid] {
		all = append(append(all, k), descendants(kids, k.pid)...)
	}
	return all
}

// busyWhy says what a screen view other than idle means for a restart.
func busyWhy(view string) string {
	if view == "asks" {
		return "shows a dialog or a question"
	}
	return "is working or runs a background agent"
}
