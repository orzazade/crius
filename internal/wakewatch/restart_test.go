package wakewatch

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orzazade/crius/internal/sessionstate"
	"github.com/orzazade/crius/internal/testutil"
)

// restartFake is a home with axi:ms-flow running as a T3 thread and staffy:builder as a tmux tab on the ThinkPad.
type restartFake struct {
	f       *fake
	tabs    []RemoteTab
	screen  map[string]string // tab target -> its screen
	capErr  error
	procs   map[string]string // tab target -> Procs' answer: its pane pid, then the box's ps lines
	procErr error
	sendErr map[string]error // text -> the error crius-tsend gives it
	typed   []string         // target + " " + text of each send
	out     bytes.Buffer
}

func newRestartFake(t *testing.T) (*restartFake, string) {
	home := t.TempDir()
	testutil.Write(t, filepath.Join(home, "roles", "axi.ms-flow.json"),
		`{"session_id":"`+sesOld+`","project":"axi","role":"ms-flow","t3_thread":"`+msThread+`","time":"2026-11-17T13:48:00Z"}`)
	f := twinsFake()
	f.threads = map[string]T3Thread{msThread: {Status: "completed", Updated: t0}}
	return &restartFake{f: f, tabs: []RemoteTab{{"thinkpad", "staffy:builder"}}, screen: map[string]string{},
		procs: map[string]string{"staffy:builder": paneTree()}, sendErr: map[string]error{}}, home
}

func (r *restartFake) deps(home string) RestartDeps {
	return RestartDeps{Home: home, Out: &r.out,
		Sessions: func() ([]Session, error) { return r.f.ses, r.f.sesErr },
		States:   func() ([]sessionstate.State, error) { return r.f.states, r.f.stErr },
		Thread: func(id string) (T3Thread, error) {
			th, ok := r.f.threads[id]
			if !ok {
				return T3Thread{}, errors.New("no thread " + id)
			}
			return th, r.f.threadErr
		},
		Tabs:    func() ([]RemoteTab, error) { return r.tabs, nil },
		Capture: func(t RemoteTab) (string, error) { return r.screen[t.Target], r.capErr },
		Procs:   func(t RemoteTab) (string, error) { return r.procs[t.Target], r.procErr },
		SendTab: func(t RemoteTab, text string) error {
			if err := r.sendErr[text]; err != nil {
				return err
			}
			r.typed = append(r.typed, t.Target+" "+text)
			if text == "/clear" {
				r.screen[t.Target] = readyTab
			}
			return nil
		},
	}
}

var (
	readyTab = tab("staffy · builder", "Done. ✅", "", t0)
	busyTab  = tab("staffy · builder", "Bash(make test)\n\n✻ Running… (12s · esc to interrupt)", "  ⏵⏵ bypass permissions on\n", t0)
	jobsTab  = tab("staffy · builder", "Started the red run. ⏳", "  ⏵⏵ bypass permissions on · 1 shell\n", t0)
	// the bus waiter every idle session keeps: a monitor, which /clear does not wait for (11-17 18:57 and 20:08, staffy:qa)
	waiterTab = tab("staffy · builder", "READY-RESTART posted. ⏳", "  ⏵⏵ bypass permissions on · 1 monitor\n", t0)
	// a monitor watching a run beside the waiter: two monitors, so still busy
	watchTab = tab("staffy · builder", "Watching the red run. ⏳", "  ⏵⏵ bypass permissions on · 2 monitors\n", t0)
)

func wantOut(t *testing.T, r *restartFake, code, want int, has ...string) {
	t.Helper()
	if code != want {
		t.Errorf("exit %d, want %d; said %q", code, want, r.out.String())
	}
	for _, h := range has {
		if !strings.Contains(r.out.String(), h) {
			t.Errorf("said %q, lacks %q", r.out.String(), h)
		}
	}
}

// The 19:09 shape: the thread is idle, yet two claudes run for the role. The helper refuses to say ready, names each
// pid and session, and gives the exact next step: stop the thread, then run it again.
func TestRestartOfARoleWithTwoLiveClaudesSaysStopFirst(t *testing.T) {
	r, home := newRestartFake(t)
	code := Restart(r.deps(home), "axi:ms-flow", "Read BRIEF.md and go")
	wantOut(t, r, code, RestartLive, "LIVE axi:ms-flow", "pid 73035", "pid 77924", "05473ed1", "6376280f",
		msThread, "kill <pid>", "crius-wakewatch restart axi:ms-flow", "never pkill -f")
	if strings.Contains(r.out.String(), "t3_thread_send") {
		t.Errorf("said to send while claudes run: %q", r.out.String())
	}
}

// A thread with a turn running is refused before anything else: a fresh send now is queued behind the turn and leaves
// the old claude running (11-17 17:45).
func TestRestartOfABusyT3ThreadIsRefused(t *testing.T) {
	r, home := newRestartFake(t)
	r.f.threads[msThread] = T3Thread{Status: "running", Active: true, Updated: t0}
	wantOut(t, r, Restart(r.deps(home), "axi:ms-flow", ""), RestartBusy, "BUSY axi:ms-flow", "turn running")
	r.out.Reset()
	r.f.threads[msThread] = T3Thread{Status: "completed", Pending: 1, Updated: t0}
	wantOut(t, r, Restart(r.deps(home), "axi:ms-flow", ""), RestartBusy, "BUSY axi:ms-flow", "1 request")
}

// The role's claudes run in two threads (a delegate's, say): a turn running in either refuses.
func TestRestartChecksEveryThreadTheRolesClaudesRunIn(t *testing.T) {
	r, home := newRestartFake(t)
	r.f.states[0].Thread = "mcp:other"
	r.f.threads["mcp:other"] = T3Thread{Status: "running", Active: true, Updated: t0}
	wantOut(t, r, Restart(r.deps(home), "axi:ms-flow", ""), RestartBusy, "BUSY axi:ms-flow", "mcp:other")
}

// Idle, and no claude runs for the role: ready, with the exact fresh-context send and the brief.
func TestRestartOfAnIdleThreadWithNoClaudeIsReady(t *testing.T) {
	r, home := newRestartFake(t)
	r.f.ses = r.f.ses[2:]
	wantOut(t, r, Restart(r.deps(home), "axi:ms-flow", "Read BRIEF.md and go"), RestartReady, "READY axi:ms-flow",
		"t3_thread_send", msThread, "freshContext", "Read BRIEF.md and go")
}

// One claude left after the stop is still a claude: not ready.
func TestRestartWithOneClaudeLeftIsNotReady(t *testing.T) {
	r, home := newRestartFake(t)
	r.f.ses = r.f.ses[1:]
	wantOut(t, r, Restart(r.deps(home), "axi:ms-flow", ""), RestartLive, "LIVE axi:ms-flow", "pid 77924")
}

// A look that failed is never read as idle or gone.
func TestRestartThatCannotLookSaysSo(t *testing.T) {
	r, home := newRestartFake(t)
	r.f.threadErr = errors.New("t3 gave no answer in time")
	wantOut(t, r, Restart(r.deps(home), "axi:ms-flow", ""), RestartFailed, "could not", "t3 gave no answer")
	r, home = newRestartFake(t)
	r.f.sesErr = errors.New("sessions: permission denied")
	wantOut(t, r, Restart(r.deps(home), "axi:ms-flow", ""), RestartFailed, "could not", "permission denied")
	r, home = newRestartFake(t)
	wantOut(t, r, Restart(r.deps(home), "nosuch:role", ""), RestartFailed, "nosuch:role", "no T3 thread")
	r, home = newRestartFake(t) // no live claude names a thread, and the role file is damaged
	r.f.ses = r.f.ses[2:]
	testutil.Write(t, filepath.Join(home, "roles", "axi.ms-flow.json"), `{"session_id":`)
	wantOut(t, r, Restart(r.deps(home), "axi:ms-flow", ""), RestartFailed, "could not read its role file")
	r, home = newRestartFake(t)
	wantOut(t, r, Restart(r.deps(home), "axi", ""), RestartFailed, "<project>:<role>")
}

// A tmux tab mid-turn, or with background jobs still running, is refused and nothing is typed (11-17 18:3x: /clear and
// the brief sent to a busy quotes:builder-2 were queued behind its turn, so the brief ran in the old context).
func TestRestartOfABusyTmuxTabTypesNothing(t *testing.T) {
	watch := bgJob{cmd: "tail -F /w/red.log", kids: []string{"tail -F /w/red.log"}}
	for name, c := range map[string]struct {
		screen string
		jobs   []bgJob
	}{"turn": {busyTab, nil}, "jobs": {jobsTab, []bgJob{testRun}},
		"two monitors": {watchTab, []bgJob{waiters["a monitor: tail | grep (staffy:builder)"], watch}},
		// an agent runs inside claude, so no process shows it: the footer still refuses
		"an agent": {tab("staffy · builder", "Asked a reviewer. ⏳", "  ⏵⏵ bypass permissions on · 1 agent\n", t0), nil}} {
		r, home := newRestartFake(t)
		r.screen["staffy:builder"], r.procs["staffy:builder"] = c.screen, paneTree(c.jobs...)
		wantOut(t, r, Restart(r.deps(home), "staffy:builder", "Read BRIEF.md and go"), RestartBusy, "BUSY staffy:builder")
		if len(r.typed) != 0 {
			t.Errorf("%s: typed %q into a busy tab", name, r.typed)
		}
	}
}

// An idle tmux tab gets /clear, then, once it is idle again, the brief, each through crius-tsend.
func TestRestartOfAnIdleTmuxTabClearsThenSendsTheBrief(t *testing.T) {
	r, home := newRestartFake(t)
	r.screen["staffy:builder"] = readyTab
	wantOut(t, r, Restart(r.deps(home), "staffy:builder", "Read BRIEF.md and go"), RestartReady, "SENT staffy:builder")
	if want := []string{"staffy:builder /clear", "staffy:builder Read BRIEF.md and go"}; strings.Join(r.typed, "|") != strings.Join(want, "|") {
		t.Errorf("typed %q, want %q", r.typed, want)
	}
	// with no brief it only says the tab is ready
	r, home = newRestartFake(t)
	r.screen["staffy:builder"] = readyTab
	wantOut(t, r, Restart(r.deps(home), "staffy:builder", ""), RestartReady, "READY staffy:builder")
	if len(r.typed) != 0 {
		t.Errorf("typed %q with no brief", r.typed)
	}
}

// A tab whose only background job is its bus waiter (a monitor) is idle for a restart: the restart clears it and sends
// the brief (11-17 20:08: exit 3 on staffy:qa, idle at its prompt with "1 monitor", which every idle session shows).
func TestRestartOfATabWithOnlyItsBusWaiterSendsTheBrief(t *testing.T) {
	r, home := newRestartFake(t)
	r.screen["staffy:builder"] = waiterTab
	r.procs["staffy:builder"] = paneTree(waiters["a monitor: tail | grep (staffy:builder)"])
	wantOut(t, r, Restart(r.deps(home), "staffy:builder", "Read BRIEF.md and go"), RestartReady, "SENT staffy:builder")
	if want := []string{"staffy:builder /clear", "staffy:builder Read BRIEF.md and go"}; strings.Join(r.typed, "|") != strings.Join(want, "|") {
		t.Errorf("typed %q, want %q", r.typed, want)
	}
}

// A /clear that did not go stops the restart before the brief; so does a tab not idle after it.
func TestRestartStopsWhenTheClearFails(t *testing.T) {
	r, home := newRestartFake(t)
	r.screen["staffy:builder"] = readyTab
	r.sendErr["/clear"] = errors.New("crius-tsend: NOT SENT")
	wantOut(t, r, Restart(r.deps(home), "staffy:builder", "Read BRIEF.md and go"), RestartNotSent, "NOT SENT", "/clear")
	if len(r.typed) != 0 {
		t.Errorf("typed %q after a failed /clear", r.typed)
	}
	// crius-tsend MAYBE SENT (or no answer): the text may be in, so never NOT SENT, which invites a resend
	for _, said := range []string{"crius-tsend: exit status 3: MAYBE SENT thinkpad staffy:builder: maybe sent: look at the tab",
		"crius-tsend: no answer in 2m0s: "} {
		r, home = newRestartFake(t)
		r.screen["staffy:builder"] = readyTab
		r.sendErr["Read BRIEF.md and go"] = errors.New(said)
		wantOut(t, r, Restart(r.deps(home), "staffy:builder", "Read BRIEF.md and go"), RestartMaybe, "MAYBE SENT", "never send")
	}
	r, home = newRestartFake(t)
	r.screen["staffy:builder"] = readyTab
	d := r.deps(home)
	send := d.SendTab
	d.SendTab = func(tb RemoteTab, text string) error {
		err := send(tb, text)
		r.screen[tb.Target] = busyTab // something started a turn right after the /clear
		return err
	}
	wantOut(t, r, Restart(d, "staffy:builder", "Read BRIEF.md and go"), RestartBusy, "brief was not sent")
	if len(r.typed) != 1 {
		t.Errorf("typed %q, want only the /clear", r.typed)
	}
}

// paneTree is Procs' answer for a tab: pane pid 100 (a login bash), its claude (pid 200) with an MCP server, and one
// background shell per job, each with the processes under it, the way Claude Code starts them on the Legion (11-17).
func paneTree(jobs ...bgJob) string {
	lines := []string{"100", "  99     1 tmux new-session -d", " 100    99 -bash",
		" 200   100 claude --resume 58128bf4 --model claude-opus-5-5 --permission-mode bypassPermissions -n staffy · builder",
		" 210   200 node /home/crius/.claude-max2/plugins/cache/context-mode/context-mode/2.1.7/server.mjs --agent claude-code",
		" 999     1 /home/crius/.crius/bin/crius-wait"} // a waiter of another tab: not under this claude
	var raw []string // each job shell's /proc cmdline: NUL as \x01, newline as \x02
	for i, j := range jobs {
		pid := 300 + 100*i
		argv := []string{"/bin/bash", "-c", "source /home/crius/.claude-max2/shell-snapshots/snapshot-bash-1794-p3.sh 2>/dev/null || " +
			"true && eval '" + strings.ReplaceAll(j.cmd, "'", `'"'"'`) + "' < /dev/null && pwd -P >| /tmp/claude-81b6-cwd"}
		if j.shell != "" {
			pre, script, _ := strings.Cut(j.shell, " -c ")
			argv = append(strings.Fields(pre), "-c", script)
		}
		lines = append(lines, fmt.Sprintf("%d 200 %s", pid, strings.ReplaceAll(strings.Join(argv, " "), "\n", " ")))
		raw = append(raw, fmt.Sprintf("%d %s\x01", pid, strings.ReplaceAll(strings.Join(argv, "\x01"), "\n", "\x02")))
		parent := pid
		for k, kid := range j.kids { // each under the one before it, the way a script runs its sleep
			lines = append(lines, fmt.Sprintf("%d %d %s", pid+k+1, parent, kid))
			if !j.flat {
				parent = pid + k + 1
			}
		}
	}
	return strings.Join(append(append(lines, "--- cmdline"), raw...), "\n") + "\n"
}

// bgJob is one background shell: its command and what runs under it, each process under the one before (flat: all
// under the shell, as the parts of a pipe). shell, when set, is the shell's whole command line instead of Claude Code's.
type bgJob struct {
	cmd, shell string
	kids       []string
	flat       bool
}

// The bus waiters seen 11-17 under idle team tabs, in each form the teams use.
var waiters = map[string]bgJob{
	"waitbus.sh (quotes:qa, 20:40)": {cmd: "bash /tmp/claude-1000/q/scratchpad/waitbus.sh 15605",
		kids: []string{"bash /tmp/claude-1000/q/scratchpad/waitbus.sh 15605", "sleep 20"}},
	"buswait.sh (quotes:builder-2)": {cmd: "SP=/tmp/claude-1000/b/scratchpad; SP_DIR=$SP $SP/buswait.sh",
		kids: []string{"/bin/bash /tmp/claude-1000/b/scratchpad/buswait.sh", "sleep 20"}},
	"team-wait.sh (quotes:dev)": {cmd: "~/.claude-work/skills/cmux-team/scripts/team-wait.sh --role quotes:dev ~/.crius/bus/quotes.log",
		kids: []string{"/bin/bash /home/crius/.claude-work/skills/cmux-team/scripts/team-wait.sh --role quotes:dev /home/crius/.crius/bus/quotes.log", "sleep 10"}},
	"crius-wait (the Mac)": {cmd: "S=/tmp/s; sleep 1; ~/.crius/bin/crius-wait > $S/wait.out 2>&1",
		kids: []string{"/Users/orkhanrzazade/.crius/bin/crius-wait"}},
	"crius-wait, in its sleep first": {cmd: "S=/tmp/s; sleep 1; ~/.crius/bin/crius-wait > $S/wait.out 2>&1",
		kids: []string{"sleep 1"}},
	"bus-watch.ts (staffy:qa)": {cmd: "bun wt/qa/bus-watch.ts /home/crius/.crius/bus/staffy.log --me staffy:qa",
		kids: []string{"bun wt/qa/bus-watch.ts /home/crius/.crius/bus/staffy.log --me staffy:qa", "tail -c +320111 -F /home/crius/.crius/bus/staffy.log"}},
	"a monitor: tail | grep (staffy:builder)": {cmd: "tail -n 0 -F ~/.crius/bus/staffy.log | grep --line-buffered -E \"staffy:builder\"",
		kids: []string{"tail -n 0 -F /home/crius/.crius/bus/staffy.log", "/usr/bin/grep --line-buffered -E ^[^ ]+ [A-Z-]+ (staffy:builder|all)"}, flat: true},
	// a tail that reads only its pipe is a filter like cut (11-18: quotes:dev refused as BUSY for its "| tail -20")
	"team-wait.sh | tail -20 | cut (quotes:dev, 11-18)": {cmd: "~/.claude-work-max3/skills/cmux-team/scripts/team-wait.sh --role quotes:dev --cursor ~/.crius/wait-cursor-quotes-dev ~/.crius/bus/quotes.log 2>&1 | tail -20 | cut -c1-400",
		kids: []string{"/bin/bash /home/crius/.claude-work-max3/skills/cmux-team/scripts/team-wait.sh --role quotes:dev --cursor /home/crius/.crius/wait-cursor-quotes-dev /home/crius/.crius/bus/quotes.log",
			"tail -20", "cut -c1-400"}, flat: true},
	"crius-wait | tail -fn 20 (an option's value)": {cmd: "~/.crius/bin/crius-wait 2>&1 | tail -fn 20",
		kids: []string{"/bin/bash /home/crius/.crius/bin/crius-wait", "tail -fn 20"}, flat: true},
}

// testRun is a real job in a background shell: a test run through the heavy-slot wrapper (quotes:builder-2, 11-17).
var testRun = bgJob{cmd: "cd /w/b2-rc25 && bun scripts/heavy.ts run -- dotnet test tests/Quote.Worker.Tests",
	kids: []string{"bun scripts/heavy.ts run -- dotnet test tests/Quote.Worker.Tests", "dotnet test tests/Quote.Worker.Tests"}}

// shellTab is the Legion's quotes:qa at 20:40: idle at its prompt, its footer counting one background shell.
var shellTab = tab("staffy · builder", "READY-RESTART posted. ⏳", "  ⏵⏵ bypass permissions on · 1 shell\n", t0)

// (a) A tab whose only background shell is its bus waiter is idle: the footer's "1 shell" is that waiter, which the
// session re-arms within seconds, so it never ends (11-17 20:40, quotes:qa refused as BUSY). What runs under the tab's
// claude decides, in every waiter form the teams use, and the brief is sent.
func TestRestartOfATabWhoseOnlyShellIsItsBusWaiterSendsTheBrief(t *testing.T) {
	for name, w := range waiters {
		r, home := newRestartFake(t)
		r.screen["staffy:builder"] = shellTab
		r.procs["staffy:builder"] = paneTree(w)
		code := Restart(r.deps(home), "staffy:builder", "Read BRIEF.md and go")
		if code != RestartReady || !strings.Contains(r.out.String(), "SENT staffy:builder") {
			t.Errorf("%s: exit %d, said %q; want exit 0 and SENT", name, code, r.out.String())
		}
		if want := "staffy:builder /clear|staffy:builder Read BRIEF.md and go"; strings.Join(r.typed, "|") != want {
			t.Errorf("%s: typed %q, want %q", name, r.typed, want)
		}
	}
}

// (b) A background shell that runs a real job (a test run, a build) is busy, whatever the footer says, and the refusal
// names it. So is a waiter's shell that first runs a job, and a job beside the waiter.
func TestRestartOfATabRunningATestInABackgroundShellIsBusy(t *testing.T) {
	for name, c := range map[string]struct {
		footer string
		jobs   []bgJob
		names  string
	}{
		"one shell, a test run":    {"1 shell", []bgJob{testRun}, "dotnet test tests/Quote.Worker.Tests"},
		"no footer, a test run":    {"", []bgJob{testRun}, "dotnet test tests/Quote.Worker.Tests"},
		"a waiter and a test run":  {"2 shells", []bgJob{waiters["waitbus.sh (quotes:qa, 20:40)"], testRun}, "dotnet test"},
		"a test, then the waiter":  {"1 shell", []bgJob{{cmd: "go test ./... ; ~/.crius/bin/crius-wait", kids: []string{"go test ./..."}}}, "go test"},
		"a build not started yet":  {"1 shell", []bgJob{{cmd: "make build"}}, "make build"},
		"a monitor watching a run": {"1 monitor", []bgJob{{cmd: "tail -F /w/run.log | grep FAIL", kids: []string{"tail -F /w/run.log", "grep FAIL"}, flat: true}}, "run.log"},
		// a word in the arguments makes no waiter: the program does (review P2-1)
		"a test of crius-wait":                    {"1 shell", []bgJob{{cmd: "bats test/crius-wait.bats", kids: []string{"bats test/crius-wait.bats", "dotnet test big"}}}, "bats test/crius-wait.bats"},
		"a waiter-like script name":               {"1 shell", []bgJob{{cmd: "bash bus-wait-deploy.sh", kids: []string{"bash bus-wait-deploy.sh"}}}, "bus-wait-deploy.sh"},
		"a tail of a bus log, and a test":         {"1 shell", []bgJob{{cmd: "tail -F ~/.crius/bus/q.log & go test ./...", kids: []string{"tail -F /home/crius/.crius/bus/q.log", "go test ./..."}, flat: true}}, "go test"},
		"a sleep, then a deploy, then the waiter": {"1 shell", []bgJob{{cmd: "sleep 120; bun run deploy; ~/.crius/bin/crius-wait", kids: []string{"sleep 120"}}}, "bun run deploy"},
		// a tool shell in a shape not seen yet still counts (review P2-2)
		"a shell in a sandbox wrapper": {"", []bgJob{{shell: "bwrap --ro-bind / / -- /bin/bash -c source /home/crius/.claude/shell-snapshots/snapshot-bash-1.sh && eval 'go test ./...'", kids: []string{"go test ./..."}}}, "go test"},
		"a login shell with -c":        {"", []bgJob{{shell: "/bin/bash -l -c go test ./...", kids: []string{"go test ./..."}}}, "go test"},
		// the footer counts more jobs than run as shells: a kind not seen, so busy (review P2-3)
		"footer counts a job no process shows": {"2 background tasks", []bgJob{waiters["waitbus.sh (quotes:qa, 20:40)"]}, "only 1 shell(s) run under its claude"},
		// what a waiter starts, or a job chained after it, is a job (review r2 P2-A, P2-B, P3-C)
		"a test run under a waitbus.sh": {"1 shell", []bgJob{{cmd: "bash /tmp/s/waitbus.sh", kids: []string{"bash /tmp/s/waitbus.sh", "dotnet test big"}}}, "waitbus.sh"},
		"a bash -c that tails the bus, then tests": {"1 shell", []bgJob{{cmd: `bash -c "tail -n0 -F ~/.crius/bus/q.log | grep -m1 DONE; go test ./..."`,
			kids: []string{`bash -c tail -n0 -F ~/.crius/bus/q.log | grep -m1 DONE; go test ./...`, "go test ./..."}}}, "go test"},
		"a test chained after the waiter": {"1 shell", []bgJob{{cmd: "~/.crius/bin/crius-wait && go test ./...", kids: []string{"/bin/bash /home/crius/.crius/bin/crius-wait"}}}, "go test"},
		"a tail of the bus and a run log": {"1 shell", []bgJob{{cmd: "tail -F ~/.crius/bus/q.log /w/run.log", kids: []string{"tail -F /home/crius/.crius/bus/q.log /w/run.log"}}}, "run.log"},
		// ps shows a newline in a command as a space: a job on the line after the waiter hides in it (review r3 P1)
		"a test on the line after the waiter": {"1 shell", []bgJob{{cmd: "~/.crius/bin/crius-wait axi:qa\ngo test ./...", kids: []string{"/bin/bash /home/crius/.crius/bin/crius-wait axi:qa"}}}, "go test"},
		"a quote escaped inside quotes":       {"1 shell", []bgJob{{cmd: `~/.crius/bin/crius-wait "a\"b"; go test ./... # "`, kids: []string{"/bin/bash /home/crius/.crius/bin/crius-wait a\"b"}}}, "go test"},
		"a comment with an apostrophe":        {"1 shell", []bgJob{{cmd: "~/.crius/bin/crius-wait x  # builder's DONE\ngo test ./...", kids: []string{"/bin/bash /home/crius/.crius/bin/crius-wait x"}}}, "go test"},
		"an awk that starts a run on a line":  {"1 shell", []bgJob{{cmd: `tail -F ~/.crius/bus/q.log | awk '/DONE/{system("go test ./...")}'`, kids: []string{"tail -F /home/crius/.crius/bus/q.log", "awk /DONE/{system(\"go test ./...\")}"}, flat: true}}, "awk"},
		// a tail that names a file other than the bus watches that file, even in a waiter's pipe (11-18)
		"a waiter piped into a tail of a run log": {"1 shell", []bgJob{{cmd: "team-wait.sh --role q:dev ~/.crius/bus/q.log | tail -f /tmp/run.log",
			kids: []string{"/bin/bash /home/crius/.claude-work/skills/cmux-team/scripts/team-wait.sh --role q:dev /home/crius/.crius/bus/q.log", "tail -f /tmp/run.log"}, flat: true}}, "run.log"},
		"a tail of a file beside the waiter": {"1 shell", []bgJob{{cmd: "~/.crius/bin/crius-wait; tail -20 /tmp/x.log",
			kids: []string{"/bin/bash /home/crius/.crius/bin/crius-wait"}}}, "x.log"},
		"a tail of a redirected file beside the waiter": {"1 shell", []bgJob{{cmd: "~/.crius/bin/crius-wait | tail -20 < /tmp/x.log",
			kids: []string{"/bin/bash /home/crius/.crius/bin/crius-wait", "tail -20"}, flat: true}}, "x.log"},
		"a tail of a file under the waiter shell": {"1 shell", []bgJob{{cmd: "~/.crius/bin/crius-wait | tail -20",
			kids: []string{"/bin/bash /home/crius/.crius/bin/crius-wait", "tail -20 /tmp/x.log"}, flat: true}}, "crius-wait"},
		// a file hidden in an option word or a number, or after --, is still a file (tail filter review P1, P2)
		"a redirect glued to a tail option": {"1 shell", []bgJob{{cmd: "~/.crius/bin/crius-wait | tail -f -n0</tmp/run.log",
			kids: []string{"/bin/bash /home/crius/.crius/bin/crius-wait", "tail -f -n0"}, flat: true}}, "run.log"},
		"a tail of a file named by digits": {"1 shell", []bgJob{{cmd: "~/.crius/bin/crius-wait | tail -f 2026",
			kids: []string{"/bin/bash /home/crius/.crius/bin/crius-wait", "tail -f 2026"}, flat: true}}, "2026"},
		"a tail of a file after --": {"1 shell", []bgJob{{cmd: "~/.crius/bin/crius-wait | tail -f -- -run.log",
			kids: []string{"/bin/bash /home/crius/.crius/bin/crius-wait", "tail -f -- -run.log"}, flat: true}}, "-run.log"},
	} {
		r, home := newRestartFake(t)
		r.screen["staffy:builder"] = tab("staffy · builder", "Started the red run. ⏳", "  ⏵⏵ bypass permissions on · "+c.footer+"\n", t0)
		r.procs["staffy:builder"] = paneTree(c.jobs...)
		code := Restart(r.deps(home), "staffy:builder", "Read BRIEF.md and go")
		if code != RestartBusy || !strings.Contains(r.out.String(), "BUSY staffy:builder") || !strings.Contains(r.out.String(), c.names) {
			t.Errorf("%s: exit %d, said %q; want exit 3, BUSY naming %q", name, code, r.out.String(), c.names)
		}
		if len(r.typed) != 0 {
			t.Errorf("%s: typed %q into a busy tab", name, r.typed)
		}
	}
}

// (c) A process tree that cannot be read, or shows no claude in the tab's pane, is a failed look: exit 2, never idle,
// and nothing is typed.
func TestRestartThatCannotReadTheTabsProcessesFails(t *testing.T) {
	for name, c := range map[string]struct {
		out  string
		err  error
		says string
	}{
		"ssh failed":       {"", errors.New("ssh: connect to host 100.109.19.101 port 22: Operation timed out"), "Operation timed out"},
		"no answer":        {"", nil, "no pane pid"},
		"no claude":        {"100\n 100 99 -bash\n 101 100 vim notes.txt\n--- cmdline\n", nil, "no claude"},
		"pane not in ps":   {"4242\n 100 99 -bash\n--- cmdline\n", nil, "4242"},
		"no command lines": {"100\n 100 99 -bash\n 200 100 claude\n", nil, "no command lines"},
	} {
		r, home := newRestartFake(t)
		r.screen["staffy:builder"] = shellTab
		r.procs["staffy:builder"], r.procErr = c.out, c.err
		code := Restart(r.deps(home), "staffy:builder", "Read BRIEF.md and go")
		if code != RestartFailed || !strings.Contains(r.out.String(), "could not read the processes of tab") ||
			!strings.Contains(r.out.String(), c.says) {
			t.Errorf("%s: exit %d, said %q; want exit 2, a failed look naming %q", name, code, r.out.String(), c.says)
		}
		if len(r.typed) != 0 {
			t.Errorf("%s: typed %q after a failed look", name, r.typed)
		}
	}
}

// A job that starts between the /clear and the look after it stops the brief.
func TestRestartStopsWhenAJobStartsAfterTheClear(t *testing.T) {
	r, home := newRestartFake(t)
	r.screen["staffy:builder"] = shellTab
	r.procs["staffy:builder"] = paneTree(waiters["waitbus.sh (quotes:qa, 20:40)"])
	d := r.deps(home)
	send := d.SendTab
	d.SendTab = func(tb RemoteTab, text string) error {
		err := send(tb, text)
		r.screen[tb.Target] = shellTab
		r.procs[tb.Target] = paneTree(waiters["waitbus.sh (quotes:qa, 20:40)"], testRun)
		return err
	}
	wantOut(t, r, Restart(d, "staffy:builder", "Read BRIEF.md and go"), RestartBusy, "brief was not sent", "dotnet test")
	if strings.Join(r.typed, "|") != "staffy:builder /clear" {
		t.Errorf("typed %q, want only the /clear", r.typed)
	}
}
