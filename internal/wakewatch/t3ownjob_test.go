package wakewatch

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/testutil"
)

// 11-20 01:39-02:31: AXI api-split ended its turn on ⏳ with its landing gate (reccode2.sh: suites, build, 25 test
// projects) running as its own Bash background task under its claude, not through heavy-run.sh. No other role posted
// about 8454 after its 01:39 ACK, so at 49 min Crius was told "its job may be gone ... none of 8454, 8452 is in flight";
// the gate ran until 02:31 and then 8454 landed. A role whose own background job still runs under its claude is at work.
// The reply and bus lines are the real ones (shortened); the pids are made up.

const ownJobReply = "QA passed my fix (PR 8454) again, this time on top of PR 8452, which merged first. My fix is not merged yet. " +
	"A new landing check is running first.\n\n**Next:** when the check is green, I merge with: `complete-pr.sh 8454 4f127bcf`\n\n" +
	"You don't need to do anything.\n\n**Still running:** the landing check (`bgid73z2f`) and the team chat watcher (`bu1chxqb9`).\n\n⏳"

// gateJob is the role's own background task: Claude Code's Bash wrapper (pid 110) running the gate script and its test run.
var gateJob = []Proc{
	{PID: 110, PPID: 100, Command: "/bin/zsh -c source /h/.claude-max2/shell-snapshots/snapshot-zsh-1795070358913-gj9uym.sh 2>/dev/null || true && eval 'bash /tmp/s/scratchpad/reccode2.sh' < /dev/null && pwd -P >| /tmp/claude-bf3f-cwd"},
	{PID: 111, PPID: 110, Command: "bash /tmp/s/scratchpad/reccode2.sh"},
	{PID: 112, PPID: 111, Command: "dotnet test Ingress.Orders.Tests.csproj"},
}

// ownJobFake is quotes:api-split's thread, T3 status "waiting" with no open run, idle since its ⏳ reply; its claude (pid
// 100) with Claude Code's own helpers (the stdin watchdog shell and an MCP server), its bus waiter, and jobs.
func ownJobFake(t *testing.T, idle time.Duration, jobs ...Proc) (string, *fake) {
	home := threadHome(t, "api-split", "qa")
	reply := t0.Add(-idle)
	testutil.Write(t, filepath.Join(home, "bus", "quotes.log"),
		stamp(reply.Add(-21*time.Minute))+" PASS quotes:qa -> quotes:api-split,crius:lead — PASS DOOR.DEADLINE.CODE 8454 4f127bcf\n"+
			stamp(reply.Add(-19*time.Minute))+" INFO quotes:qa -> crius:lead,quotes:api-split — INFO landing check 8452: envhub daf36a42. 8454 re-gate on new land tree\n"+
			stamp(reply.Add(-1*time.Minute))+" PASS quotes:qa -> quotes:api-split,crius:lead — PASS DOOR.DEADLINE.CODE 8454 re-gate on new land tree. Ready to land\n"+
			stamp(reply)+" ACK quotes:api-split -> quotes:qa,crius:lead — ACK re-gate PASS 8454. Full gate reccode2 running (suites, build, test), then complete-pr.sh 8454.\n")
	f := &fake{
		threads: map[string]T3Thread{
			"th-api-split": {Status: "waiting", Updated: reply, LastReply: ownJobReply},
			"th-qa":        {Status: "running", Active: true, Updated: t0},
		},
		ses: []Session{{ID: "s-api-split", PID: 100, Status: "idle"}},
		procs: append([]Proc{
			{PID: 1, PPID: 0, Command: "/sbin/launchd"},
			{PID: 100, PPID: 1, Command: "/Users/o/.local/bin/claude --output-format stream-json --verbose --input-format stream-json"},
			{PID: 101, PPID: 100, Command: "/bin/sh -c read -r _ <&4 || exit 1 exec 4<&- { trap '' TERM; read -r _ <&3; kill -TERM 0; sleep 5; kill -KILL 0; }"},
			{PID: 102, PPID: 100, Command: "node /Users/o/.claude/plugins/cache/context-mode/context-mode/2.1.7/server.mjs --agent claude-code"},
			{PID: 103, PPID: 100, Command: "/bin/zsh -c source /h/.claude-max2/shell-snapshots/snapshot-zsh-1795070358913-gj9uym.sh 2>/dev/null || true && eval 'cd ~/w && bash ~/.claude/skills/cmux-team/scripts/team-wait.sh --mode lead --role api-split ~/.crius/bus/quotes.log' < /dev/null"},
			{PID: 104, PPID: 103, Command: "bash /Users/o/.claude/skills/cmux-team/scripts/team-wait.sh --mode lead --role api-split --alias quotes:api-split ~/.crius/bus/quotes.log"},
		}, jobs...),
	}
	return home, f
}

func TestARoleWhoseOwnBackgroundJobRunsIsNotIdle(t *testing.T) {
	home, f := ownJobFake(t, 49*time.Minute, gateJob...)
	runOnce(t, home, f, t0)
	if w := t3Wakes(f); len(w) != 0 {
		t.Fatalf("api-split's own gate runs under its claude, yet Crius was told: %q", w)
	}
	// past t3WaitingFor (2 h) with the job still there: one alert that names it, as for any item in flight
	home, f = ownJobFake(t, 121*time.Minute, gateJob...)
	runOnce(t, home, f, t0)
	runOnce(t, home, f, t0.Add(5*time.Minute))
	w := t3Wakes(f)
	if len(w) != 1 {
		t.Fatalf("want one alert past 2 h, got %q", w)
	}
	for _, want := range []string{"quotes · api-split", "WAITING ON its own background job (pid 110)", "reccode2.sh", "team-wait.sh"} {
		if !strings.Contains(w[0], want) {
			t.Errorf("alert %q lacks %q", w[0], want)
		}
	}
}

// The job ended or died: only Claude Code's own helpers (the stdin watchdog shell, an MCP server) and the bus waiter are
// left under the claude. None of them is a job, so the alert stays and says that no job of its own was found.
func TestARoleWhoseOwnJobDiedStillAlerts(t *testing.T) {
	home, f := ownJobFake(t, 49*time.Minute)
	runOnce(t, home, f, t0)
	w := t3Wakes(f)
	if len(w) != 1 || !strings.Contains(w[0], "its job may be gone") || !strings.Contains(w[0], "none of 8454, 8452 is in flight") ||
		!strings.Contains(w[0], "no background job of its own runs under its claude") {
		t.Fatalf("want today's alert, saying no job of its own runs: %q", w)
	}
	// a job left behind by an earlier claude (reparented to launchd) is no job of this one
	home, f = ownJobFake(t, 49*time.Minute, gateJob...)
	f.procs[slices.IndexFunc(f.procs, func(p Proc) bool { return p.PID == 110 })].PPID = 1
	runOnce(t, home, f, t0)
	if w := t3Wakes(f); len(w) != 1 || !strings.Contains(w[0], "its job may be gone") {
		t.Fatalf("an orphaned job held the alert: %q", w)
	}
}
