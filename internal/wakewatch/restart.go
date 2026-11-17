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
// then its box's process list (`ps -ww -A -o pid=,ppid=,args=`), a "--- cmdline" line, and one "<pid> <argv>" line per
// tool shell with its raw /proc cmdline (NUL as \x01, newline as \x02: ps shows a newline as a space); SendTab types one
// line with crius-tsend.
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

// waiterSteps may stand beside a waiter in a waiter shell's command, and run under that shell: a cd or sleep before it,
// the filters of a monitor's tail | grep.
var waiterSteps = map[string]bool{"cd": true, "sleep": true, "true": true, "grep": true, "cat": true, "head": true,
	"cut": true, "tr": true} // no awk or sed: their system() and e can start a run on a bus line

// waiterHelpers are what a waiter may run under itself: the commands team-wait.sh and crius-wait use (11-17). Anything
// else under a waiter (a QA run a waitbus.sh starts on a DONE line) is a job.
var waiterHelpers = map[string]bool{"sleep": true, "tail": true, "grep": true, "sed": true, "awk": true, "cat": true,
	"head": true, "cut": true, "tr": true, "date": true, "flock": true, "basename": true, "dirname": true, "wc": true,
	"mv": true, "cp": true, "touch": true, "rm": true, "mkdir": true, "jq": true, "ps": true, "printf": true, "kill": true,
	"perl": true, "stat": true, "pgrep": true, "crius-wakewatch": true, "cmux": true}

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

// shellCommand is the command a tool shell runs, from its raw argv: the script after -c, or within it the text Claude
// Code evals, its quotes put back.
func shellCommand(argv []string) string {
	i := slices.Index(argv, "-c")
	if i < 0 || i+1 >= len(argv) {
		return strings.Join(argv, " ")
	}
	cmd := argv[i+1]
	if j := strings.Index(cmd, "eval '"); j >= 0 {
		cmd, _, _ = strings.Cut(cmd[j+len("eval '"):], "' < /dev/null")
		cmd = strings.NewReplacer(`'"'"'`, "'", `'\''`, "'").Replace(cmd)
	}
	return cmd
}

// waiterCommand says whether a shell command only waits on the bus: each of its simple commands (split at ; & | && ||
// and newlines outside quotes, # comments left out) is a waiter or one of waiterSteps, and one is a waiter. "crius-wait && go test" is a job
// even while it waits: its test starts later, under no session.
func waiterCommand(cmd string) bool {
	var steps []string
	var cur strings.Builder
	quote := byte(0)
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		switch {
		case quote == '"' && c == '\\' && i+1 < len(cmd):
			cur.WriteByte(c)
			i++
			c = cmd[i]
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '#' && (cur.Len() == 0 || strings.ContainsRune(" \t", rune(cmd[i-1]))):
			for i+1 < len(cmd) && cmd[i+1] != '\n' {
				i++
			}
			continue
		case c == '\'' || c == '"':
			quote = c
		case c == '\\' && i+1 < len(cmd):
			cur.WriteByte(c)
			i++
			c = cmd[i]
		case c == ';' || c == '\n' || (c == '|' || c == '&') && (i == 0 || cmd[i-1] != '>') && (i+1 == len(cmd) || cmd[i+1] != '>'):
			steps, cur = append(steps, cur.String()), strings.Builder{}
			continue
		}
		cur.WriteByte(c)
	}
	waiters := 0
	for _, st := range append(steps, cur.String()) {
		w := strings.Fields(st)
		switch prog := progOf(w); {
		case prog == "":
		case isWaiter(w):
			waiters++
		case !waiterSteps[prog]:
			return false
		}
	}
	return waiters > 0
}

// tabJobs reads Procs' answer (the pane pid, then ps lines) and gives the number of tool shells of the claude in that
// pane and the ones among them that are no bus waiter. A waiter shell's command only waits (waiterCommand), and nothing
// runs under it but waiters and waiterSteps, nor under a waiter but waiterHelpers. MCP servers, which claude starts
// without a shell, are no shells. A pane with no claude is an error, never idle.
func tabJobs(out string) (shells int, jobs []string, err error) {
	first, rest, _ := strings.Cut(strings.TrimLeft(out, " \t\n"), "\n")
	pane, err := strconv.Atoi(strings.TrimSpace(first))
	if err != nil {
		return 0, nil, fmt.Errorf("no pane pid in its answer %.60q", first)
	}
	rest, cmdlines, ok := strings.Cut(rest, "--- cmdline\n")
	if !ok {
		return 0, nil, errors.New("no command lines in its answer")
	}
	argv := map[int][]string{} // a tool shell's raw argv
	for _, l := range strings.Split(cmdlines, "\n") {
		id, a, _ := strings.Cut(l, " ")
		if pid, err := strconv.Atoi(id); err == nil && a != "" {
			argv[pid] = strings.Split(strings.TrimSuffix(strings.ReplaceAll(a, "\x02", "\n"), "\x01"), "\x01")
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
	// quiet: everything under pid is in allowed or a waiter, and so is everything under those (waiterHelpers for a waiter)
	var quiet func(pid int, allowed map[string]bool) bool
	quiet = func(pid int, allowed map[string]bool) bool {
		for _, k := range kids[pid] {
			switch {
			case k.waiter():
				if !quiet(k.pid, waiterHelpers) {
					return false
				}
			case !allowed[k.prog()] || !quiet(k.pid, allowed):
				return false
			}
		}
		return true
	}
	for _, c := range claudes {
		for _, k := range kids[c.pid] {
			if !k.toolShell() {
				continue
			}
			shells++
			a, ok := argv[k.pid]
			if !ok { // a shell started after the list, or one pgrep's marker misses: its command is not known
				jobs = append(jobs, fmt.Sprintf("pid %d: %.100s (its command line could not be read)", k.pid, k.args))
				continue
			}
			if cmd := shellCommand(a); !waiterCommand(cmd) || !quiet(k.pid, waiterSteps) {
				jobs = append(jobs, fmt.Sprintf("pid %d: %.100s", k.pid, strings.ReplaceAll(cmd, "\n", "; ")))
			}
		}
	}
	return shells, jobs, nil
}

// busyWhy says what a screen view other than idle means for a restart.
func busyWhy(view string) string {
	if view == "asks" {
		return "shows a dialog or a question"
	}
	return "is working or runs a background agent"
}
