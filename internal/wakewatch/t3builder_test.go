package wakewatch

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/testutil"
)

// The processes under a T3 builder's claude (pid 100) as ps listed them on 11-23 (paths and eval text cut): its tool
// shells, each with what runs in it.
const snap = "/bin/zsh -c source /h/.claude-max2-personal/shell-snapshots/snapshot-zsh-1.sh 2>/dev/null || true && eval '"

var (
	busWaiterShell = []Proc{
		{PID: 110, PPID: 100, Command: snap + "bash /h/.claude/skills/cmux-team/scripts/team-wait.sh --role api /h/.crius/bus/quotes.log | tail -15 | cut -c1-400' < /dev/null"},
		{PID: 111, PPID: 110, Command: "bash /h/.claude/skills/cmux-team/scripts/team-wait.sh --role api /h/.crius/bus/quotes.log"},
		{PID: 112, PPID: 111, Command: "sleep 5"},
		{PID: 113, PPID: 110, Command: "tail -15"},
		{PID: 114, PPID: 110, Command: "cut -c1-400"},
	}
	gateShell = []Proc{
		{PID: 120, PPID: 100, Command: snap + "bun /h/scripts/heavy.ts run -- bash /h/run.sh; echo rc=$?' < /dev/null"},
		{PID: 121, PPID: 120, Command: "bun /h/scripts/heavy.ts run -- bash /h/run.sh"},
	}
	pollShell = []Proc{ // api-split's third job: a loop on a file another job writes
		{PID: 130, PPID: 100, Command: snap + "until /usr/bin/grep -q \"^all rc=\" /h/sum.txt; do sleep 15; done' < /dev/null"},
		{PID: 131, PPID: 130, Command: "sleep 15"},
	}
	waiterTask = T3Task{ID: "ba2lk59g5", Kind: "command", Description: "Re-arm the bus waiter"}
	gateTask   = T3Task{ID: "bvbrnwu8a", Kind: "command", Description: "Run the merge check suites"}
	pollTask   = T3Task{ID: "bzgrdek24", Kind: "command", Description: "Wait for sum.txt"}
)

// builderHome is a home that names quotes:api as a T3 builder; builderFake its thread th-api, whose latest run ended idle
// ago on reply, with tasks listed in T3 and shells running under its claude.
func builderHome(t *testing.T) string {
	home := threadHome(t, "api")
	testutil.Write(t, filepath.Join(home, "wakewatch-projects"), "quotes\nquotes:api # its builder threads\n")
	return home
}

func builderFake(idle time.Duration, status, reply string, tasks []T3Task, shells ...[]Proc) *fake {
	f := &fake{
		threads: map[string]T3Thread{"th-api": {Status: status, Run: "run:1", Updated: t0.Add(-idle), LastReply: reply, Tasks: tasks}},
		ses:     []Session{{ID: "s-api", PID: 100, Status: "idle"}},
		procs: []Proc{{PID: 1, PPID: 0, Command: "/sbin/launchd"}, {PID: 100, PPID: 1, Command: "/h/.local/bin/claude --output-format stream-json"},
			{PID: 105, PPID: 100, Command: "bun /h/mcp/server.ts"}}, // an MCP server is no job
	}
	for _, task := range tasks {
		f.threads["th-api"] = withBackground(f.threads["th-api"], task.ID+" ("+task.Description+")")
	}
	for _, s := range shells {
		f.procs = append(f.procs, s...)
	}
	return f
}

func withBackground(th T3Thread, job string) T3Thread {
	th.Background = append(th.Background, job)
	return th
}

const (
	mergeReply = "Waiting on ms-flow's merge. Background job ba2lk59g5 is watching for new team messages. ⏳"
	doneReply  = "8571 landed, nothing left to build. ✅"
)

// wantWakes runs wakewatch at each offset from t0 and says how many T3 alerts Crius got in all, and the last one.
func wantWakes(t *testing.T, home string, f *fake, want int, at ...time.Duration) string {
	t.Helper()
	for _, d := range at {
		runOnce(t, home, f, t0.Add(d))
	}
	w := t3Wakes(f)
	if len(w) != want {
		t.Fatalf("want %d alert(s) in this idle stretch, got %d: %q", want, len(w), w)
	}
	if want == 0 {
		return ""
	}
	return w[len(w)-1]
}

func has(t *testing.T, text string, words ...string) {
	t.Helper()
	for _, w := range words {
		if !strings.Contains(text, w) {
			t.Errorf("alert %q lacks %q", text, w)
		}
	}
}

const stretch = 60 // minutes of 5-min runs in one idle stretch

func runs(from int) (at []time.Duration) {
	for m := from; m <= stretch; m += 5 {
		at = append(at, time.Duration(m)*time.Minute)
	}
	return at
}

// 11-23: "AXI · api-split" was 'waiting' over 25 min with only its bus waiter running, and no watch said so: T3 listed the
// waiter as its background job, which held the alert for 2 h.
func TestABuilderWaitingOnOnlyItsBusWaiterWakesCriusOnceAfter10Min(t *testing.T) {
	home := builderHome(t)
	f := builderFake(9*time.Minute, "waiting", mergeReply, []T3Task{waiterTask}, busWaiterShell)
	wantWakes(t, home, f, 0, 0) // 9 min
	w := wantWakes(t, home, f, 1, runs(2)...)
	has(t, w, "quotes · api", "T3 thread th-api", "idle 11 min", "status waiting", "team-wait.sh (pid 111)", "watching for new team messages")
}

// The old case, with what T3 does 30 min later (11-23: axi qa-2's updatedAt moved 1799.985 s after its run ended, with no
// run): still one alert, and the second run in T3 is a new stretch.
func TestAFinishedBuilderWakesCriusOncePerStretchThoughT3MovesItsUpdateTime(t *testing.T) {
	home := builderHome(t)
	f := builderFake(11*time.Minute, "completed", doneReply, nil)
	has(t, wantWakes(t, home, f, 1, runs(0)[:4]...), "quotes · api", "idle 11 min", "status completed", "no background job", "nothing left to build")
	th := f.threads["th-api"]
	th.Updated = t0.Add(19 * time.Minute) // 30 min after the run ended
	f.threads["th-api"] = th
	wantWakes(t, home, f, 1, runs(20)...)
	th.Run, th.Updated = "run:2", t0.Add(70*time.Minute)
	f.threads["th-api"] = th
	has(t, wantWakes(t, home, f, 2, 85*time.Minute), "idle 15 min")
	wantWakes(t, home, f, 2, 14*time.Hour, 15*time.Hour) // past the 12 h a said key is kept: the same stretch, never said again
}

// Its first alert may come before and its last after the evidence changes (unknown, then the waiter alone): one stretch.
func TestOneStretchIsOneAlertWhateverTheEvidenceTurnsInto(t *testing.T) {
	home := builderHome(t)
	f := builderFake(21*time.Minute, "waiting", mergeReply, []T3Task{waiterTask, pollTask}, busWaiterShell, pollShell)
	has(t, wantWakes(t, home, f, 1, 0), "cannot tell")
	g := builderFake(21*time.Minute, "waiting", doneReply, []T3Task{waiterTask}, busWaiterShell)
	f.threads, f.procs = g.threads, g.procs
	wantWakes(t, home, f, 1, runs(5)...)
}

func TestABuilderThatAsksIsIdleToo(t *testing.T) {
	home := builderHome(t)
	f := builderFake(11*time.Minute, "completed", "Which of the two? ❓", nil, busWaiterShell)
	has(t, wantWakes(t, home, f, 1, runs(0)...), "❓", "team-wait.sh (pid 111)")
}

// usingCPU makes every process of f use 2 s of CPU between two looks, as a job that gets on does.
func usingCPU(f *fake) *fake {
	f.cpuStep = 2 * time.Second
	return f
}

func TestABuilderWithItsGateRunningIsBusy(t *testing.T) {
	for name, reply := range map[string]string{"on an hourglass": mergeReply, "on a check mark": doneReply, "on a question": "May I? ❓"} {
		t.Run(name, func(t *testing.T) {
			f := builderFake(40*time.Minute, "waiting", reply, []T3Task{waiterTask, gateTask}, busWaiterShell, gateShell)
			wantWakes(t, builderHome(t), usingCPU(f), 0, runs(0)...)
		})
	}
	t.Run("a gate T3 does not list", func(t *testing.T) {
		f := builderFake(40*time.Minute, "completed", doneReply, []T3Task{waiterTask}, busWaiterShell, gateShell)
		wantWakes(t, builderHome(t), usingCPU(f), 0, runs(0)...)
	})
	t.Run("a run its waiter's script started", func(t *testing.T) {
		f := builderFake(40*time.Minute, "waiting", mergeReply, []T3Task{waiterTask}, busWaiterShell, []Proc{{PID: 115, PPID: 111, Command: "go test ./..."}})
		wantWakes(t, builderHome(t), usingCPU(f), 0, runs(0)...)
	})
	t.Run("past 2 h", func(t *testing.T) {
		f := builderFake(119*time.Minute, "waiting", mergeReply, []T3Task{gateTask}, gateShell)
		has(t, wantWakes(t, builderHome(t), usingCPU(f), 1, runs(0)...), "heavy.ts", "pid 121", "cannot tell", "over 2 h")
	})
}

func TestABuilderInATurnOrYoungIsBusy(t *testing.T) {
	f := builderFake(50*time.Minute, "running", doneReply, []T3Task{waiterTask}, busWaiterShell)
	th := f.threads["th-api"]
	th.Active = true
	f.threads["th-api"] = th
	wantWakes(t, builderHome(t), f, 0, runs(0)...)

	f = builderFake(4*time.Minute, "waiting", mergeReply, []T3Task{waiterTask}, busWaiterShell)
	wantWakes(t, builderHome(t), f, 0, 0, 5*time.Minute)
}

// What runs does not say whether the builder works: never busy, said once past 20 min.
func TestABuilderWhoseEvidenceCannotBeReadIsUnknownAndLoudAfter20Min(t *testing.T) {
	for name, c := range map[string]struct {
		f    func() *fake
		want string
	}{
		"only a poll loop": {func() *fake {
			return builderFake(0, "waiting", mergeReply, []T3Task{pollTask}, pollShell)
		}, "only a poll loop or a log watch"},
		"a poll loop beside its waiter": {func() *fake {
			return builderFake(0, "waiting", mergeReply, []T3Task{waiterTask, pollTask}, busWaiterShell, pollShell)
		}, "sum.txt"},
		"T3 lists a gate no process runs": {func() *fake {
			return builderFake(0, "waiting", mergeReply, []T3Task{waiterTask, gateTask}, busWaiterShell)
		}, "T3 lists 2 background jobs"},
		"its claude is not found": {func() *fake {
			f := builderFake(0, "waiting", mergeReply, []T3Task{waiterTask}, busWaiterShell)
			f.ses = nil
			return f
		}, "no live claude"},
		"the process list cannot be read": {func() *fake {
			f := builderFake(0, "waiting", mergeReply, []T3Task{waiterTask}, busWaiterShell)
			f.procErr = errors.New("ps: exit status 1")
			return f
		}, "ps: exit status 1"},
		"the sessions cannot be read": {func() *fake {
			f := builderFake(0, "completed", doneReply, nil)
			f.sesErr = errors.New("sessions dir unreadable")
			return f
		}, "sessions dir unreadable"},
		"a subagent that stopped moving": {func() *fake {
			f := builderFake(0, "waiting", mergeReply, []T3Task{{ID: "aa5d", Kind: "subagent", Description: "Build F5", Child: "th-child"}}, busWaiterShell)
			f.threads["th-child"] = T3Thread{Status: "idle", Updated: t0.Add(-time.Hour)}
			return f
		}, "Build F5"},
		"a subagent whose thread cannot be read": {func() *fake {
			return builderFake(0, "waiting", mergeReply, []T3Task{{ID: "aa5d", Kind: "subagent", Description: "Build F5", Child: "th-gone"}}, busWaiterShell)
		}, "th-gone"},
		"a kind of job T3 has not shown yet": {func() *fake {
			return builderFake(0, "waiting", mergeReply, []T3Task{{ID: "x1", Kind: "workflow", Description: "Fan out"}}, busWaiterShell)
		}, "workflow"},
	} {
		t.Run(name, func(t *testing.T) {
			home, f := builderHome(t), c.f()
			wantWakes(t, home, f, 0, 5*time.Minute, 15*time.Minute, 20*time.Minute)
			has(t, wantWakes(t, home, f, 1, runs(21)...), "quotes · api", "idle 21 min", "cannot tell", c.want, "Last reply:")
		})
	}
}

// A subagent has no process of its own: a child that runs a turn, or that moved in the last 10 min, is work.
func TestABuilderWithAMovingSubagentIsBusy(t *testing.T) {
	task := []T3Task{{ID: "aa5d", Kind: "subagent", Description: "Build F5", Child: "th-child"}}
	for name, child := range map[string]T3Thread{
		"a native subagent that moved 3 min ago": {Status: "idle", Updated: t0.Add(27 * time.Minute)},
		"a delegate in a turn":                   {Status: "running", Active: true, Updated: t0.Add(-time.Hour)},
	} {
		t.Run(name, func(t *testing.T) {
			f := builderFake(5*time.Minute, "waiting", mergeReply, task, busWaiterShell)
			f.threads["th-child"] = child
			wantWakes(t, builderHome(t), f, 0, 0, 10*time.Minute, 30*time.Minute)
		})
	}
}

func TestABuilderThreadThatCannotBeReadIsLoud(t *testing.T) {
	home := builderHome(t)
	f := builderFake(11*time.Minute, "waiting", mergeReply, []T3Task{waiterTask}, busWaiterShell)
	f.threadErr = errors.New("t3: server closed")
	runOnce(t, home, f, t0)
	runOnce(t, home, f, t0.Add(5*time.Minute))
	w := strings.Join(f.sent, "\n")
	has(t, w, "failed to look at quotes · api", "t3: server closed")
}

// A builder the list names that has no thread to look at is never taken as fine.
func TestANamedBuilderWithNoThreadIsLoud(t *testing.T) {
	home := builderHome(t)
	testutil.Write(t, filepath.Join(home, "wakewatch-projects"), "quotes quotes:api quotes:ghost falcon:x")
	f := builderFake(5*time.Minute, "running", doneReply, nil)
	runOnce(t, home, f, t0)
	runOnce(t, home, f, t0.Add(time.Minute))
	w := strings.Join(f.sent, "\n")
	has(t, w, "quotes · ghost", "named a builder", "falcon · x")
	if strings.Contains(w, "no bus file") {
		t.Errorf("a role in the list was read as a project: %q", w)
	}
	testutil.Write(t, filepath.Join(home, "parked"), "quotes:ghost falcon")
	g := builderFake(5*time.Minute, "running", doneReply, nil)
	runOnce(t, home, g, t0.Add(time.Hour))
	runOnce(t, home, g, t0.Add(65*time.Minute))
	if w := strings.Join(g.sent, "\n"); strings.Contains(w, "named a builder") {
		t.Errorf("a parked builder is said: %q", w)
	}
}

// A QA role that is not named keeps its hold (11-19): it waits for others on purpose.
func TestARoleNotNamedABuilderKeepsItsHold(t *testing.T) {
	home := builderHome(t)
	testutil.Write(t, filepath.Join(home, "wakewatch-projects"), "quotes quotes:other")
	f := builderFake(0, "waiting", mergeReply, []T3Task{waiterTask}, busWaiterShell)
	runOnce(t, home, f, t0.Add(15*time.Minute))
	if w := t3Wakes(f, "th-api"); len(w) != 0 {
		t.Fatalf("a role not named a builder woke Crius at 15 min: %q", w)
	}
}

// A waiter in other clothes is still only a waiter.
func TestABuildersWaiterIsFoundUnderItsWrappers(t *testing.T) {
	for name, shell := range map[string][]Proc{
		"exec'd, with no shell left": {{PID: 110, PPID: 100, Command: "bash /h/team-wait.sh --role api /h/.crius/bus/quotes.log"}},
		"under timeout": {{PID: 110, PPID: 100, Command: snap + "timeout 7000 crius-wait api'"},
			{PID: 111, PPID: 110, Command: "timeout 7000 crius-wait api"}, {PID: 112, PPID: 111, Command: "bash /h/bin/crius-wait api"}},
		"work chained after it has not started": {{PID: 110, PPID: 100, Command: snap + "crius-wait api && bun run build'"},
			{PID: 111, PPID: 110, Command: "bash /h/bin/crius-wait api"}},
	} {
		t.Run(name, func(t *testing.T) {
			f := builderFake(11*time.Minute, "waiting", mergeReply, []T3Task{waiterTask}, shell)
			has(t, wantWakes(t, builderHome(t), f, 1, 0), "only its bus waiter runs")
		})
	}
}

// Another session's claude under the role's name (an old one beside the thread's) speaks neither way.
func TestAnotherClaudeOfTheRoleIsNotTheBuildersEvidence(t *testing.T) {
	home := builderHome(t)
	f := builderFake(11*time.Minute, "waiting", mergeReply, []T3Task{waiterTask}, busWaiterShell)
	f.ses = append(f.ses, Session{Name: "quotes: api", ID: "s-old", PID: 300, Status: "idle"})
	f.procs = append(f.procs, Proc{PID: 300, PPID: 1, Command: "claude"}, Proc{PID: 301, PPID: 300, Command: snap + "go test ./...'"},
		Proc{PID: 302, PPID: 301, Command: "go test ./..."})
	has(t, wantWakes(t, home, f, 1, 0), "only its bus waiter runs")
}

// crius-wakewatch waiting is the dry run: each builder's verdict and what would be said, with nothing sent or written.
func TestWaitingPrintsEachBuildersVerdictAndSendsNothing(t *testing.T) {
	for name, c := range map[string]struct {
		f    *fake
		want string
	}{
		"idle":    {builderFake(11*time.Minute, "waiting", mergeReply, []T3Task{waiterTask}, busWaiterShell), "quotes:api builder IDLE 11 min"},
		"busy":    {builderFake(50*time.Minute, "waiting", mergeReply, []T3Task{gateTask}, gateShell), "quotes:api builder BUSY 50 min"},
		"unknown": {builderFake(50*time.Minute, "waiting", mergeReply, []T3Task{pollTask}, pollShell), "quotes:api builder UNKNOWN 50 min"},
	} {
		t.Run(name, func(t *testing.T) {
			home := builderHome(t)
			var out bytes.Buffer
			Waiting(c.f.deps(home, t0), &out, nil)
			has(t, out.String(), c.want)
			if len(c.f.sent) > 0 {
				t.Errorf("the dry run sent %q", c.f.sent)
			}
			if _, err := os.Stat(filepath.Join(home, "state", "wakewatch.json")); err == nil {
				t.Error("the dry run wrote wakewatch's state")
			}
		})
	}
}

// The stretch's start survives a state that T3's later update time would move: "how long" is from the run's end.
func TestABuildersIdleTimeCountsFromTheRunsEnd(t *testing.T) {
	home := builderHome(t)
	f := builderFake(2*time.Minute, "waiting", mergeReply, []T3Task{waiterTask}, busWaiterShell)
	for m := 0; m <= 20; m += 5 { // T3 moves its update time on every look, with no run
		th := f.threads["th-api"]
		th.Updated = t0.Add(time.Duration(m-2) * time.Minute)
		f.threads["th-api"] = th
		runOnce(t, home, f, t0.Add(time.Duration(m)*time.Minute))
	}
	if w := t3Wakes(f); len(w) != 1 || !strings.Contains(w[0], fmt.Sprintf("idle %d min", 12)) {
		t.Fatalf("want one alert at 12 min, got %q", w)
	}
}
