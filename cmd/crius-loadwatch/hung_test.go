package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const start = "Sun Nov  8 02:30:00 2026"

// holder writes slot-<n>.json beside the slots file, as the rig's gate writes it, taken at the given time.
func (r *rig) holder(t *testing.T, slot, pid, child int, at time.Time, token string) {
	t.Helper()
	b := fmt.Sprintf(`{"pid":%d,"pidStart":%q,"child":%d,"childStart":%q,"cmd":"/work/jobs/a1/go.sh","cwd":"/work/repo","at":%q,"token":%q,"zone":"machine"}`,
		pid, start, child, start, at.UTC().Format("2006-01-02T15:04:05.000Z"), token)
	if err := os.WriteFile(filepath.Join(filepath.Dir(r.slotPath), fmt.Sprintf("slot-%d.json", slot)), []byte(b), 0o644); err != nil {
		t.Fatal(err)
	}
}

// job is a wrapper (pid 100) whose job group 101 has one more process, with the given CPU times.
func job(wrapper, leader, worker float64) []proc {
	return []proc{
		{pid: 1, ppid: 0, pgid: 1, start: "Sat Nov  7 09:00:00 2026", cmd: "/sbin/launchd"},
		{pid: 100, ppid: 1, pgid: 99, cpu: wrapper, start: "Sun Nov  8 02:30:00 2026", cmd: "bun gate.ts run -- /work/jobs/a1/go.sh"},
		{pid: 101, ppid: 100, pgid: 101, cpu: leader, start: "Sun Nov  8 02:30:00 2026", cmd: "/bin/bash /work/jobs/a1/go.sh"},
		{pid: 102, ppid: 101, pgid: 101, cpu: worker, start: "Sun Nov  8 02:31:00 2026", cmd: "node build.js"},
	}
}

// logFile is the job's output file, last written at t.
func (r *rig) logFile(t *testing.T, at time.Time) string {
	t.Helper()
	p := filepath.Join(filepath.Dir(r.slotPath), "job.log")
	if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
	r.outFiles = []string{p}
	return p
}

func (r *rig) projectBus(t *testing.T, project string) string {
	t.Helper()
	r.project = project
	p := filepath.Join(r.d.home, "bus", project+".log")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func lines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if s := strings.TrimSpace(string(b)); s != "" {
		return strings.Split(s, "\n")
	}
	return nil
}

// A slot held 45 min by a job that used no CPU for 15 min and wrote nothing for 15 min gets one INFO to its project's
// lead (crius:lead 11-08: a hung job holds a heavy slot and nobody notices); later runs do not post it again.
func TestAHungHolderIsPostedOnce(t *testing.T) {
	r := newRig(t, "2")
	bus := r.projectBus(t, "proj")
	r.holder(t, 1, 100, 101, r.now.Add(-30*time.Minute), "tok1")
	r.procs = job(1.2, 0.4, 7.5)
	r.logFile(t, r.now.Add(-time.Hour))
	if rc := r.minutes(15, 10, 20); rc != 0 || lines(t, bus) != nil {
		t.Fatalf("held 44 min, idle 14: rc %d, bus %q, out %s", rc, lines(t, bus), r.out.String())
	}
	if rc := r.minutes(1, 10, 20); rc != 0 {
		t.Fatalf("rc %d, out %s", rc, r.out.String())
	}
	want := "2026-11-08T03:15 INFO crius:loadwatch -> proj:lead — heavy slot 1 looks hung: pid 100 (go.sh) held 45 min, no CPU in its job for 15 min; check it or stop its group 101; last output 02:00 (job.log)"
	if b := lines(t, bus); len(b) != 1 || b[0] != want {
		t.Fatalf("bus %q\nwant %q", b, want)
	}
	r.minutes(30, 10, 20)
	if b := lines(t, bus); len(b) != 1 {
		t.Fatalf("posted again: %q", b)
	}
}

// A job whose CPU time grows, or whose output file is fresh, is working, however long it holds its slot.
func TestABusyHolderIsLeftAlone(t *testing.T) {
	for _, c := range []string{"cpu", "output"} {
		t.Run(c, func(t *testing.T) {
			r := newRig(t, "2")
			bus := r.projectBus(t, "proj")
			r.holder(t, 0, 100, 101, r.now.Add(-50*time.Minute), "tok1")
			worker := 7.5
			for i := 0; i < 40; i++ {
				if c == "cpu" {
					worker += 1
					r.logFile(t, r.now.Add(-time.Hour))
				} else {
					r.logFile(t, r.now.Add(-14*time.Minute))
				}
				r.procs = job(1.2, 0.4, worker)
				if rc := r.minutes(1, 10, 20); rc != 0 {
					t.Fatalf("rc %d, out %s", rc, r.out.String())
				}
			}
			if b := lines(t, bus); b != nil {
				t.Fatalf("a working job was posted: %q", b)
			}
		})
	}
}

// The wrapper's own CPU (its timers and signal passing) does not make a stuck job look busy.
func TestTheWrapperCPUIsNotTheJobs(t *testing.T) {
	r := newRig(t, "2")
	bus := r.projectBus(t, "proj")
	r.holder(t, 0, 100, 101, r.now.Add(-50*time.Minute), "tok1")
	r.logFile(t, r.now.Add(-time.Hour))
	for i := 0; i < 16; i++ {
		r.procs = job(1.2+float64(i), 0.4, 7.5)
		r.minutes(1, 10, 20)
	}
	if b := lines(t, bus); len(b) != 1 || !strings.Contains(b[0], "no CPU in its job for 15 min") {
		t.Fatalf("bus %q, out %s", b, r.out.String())
	}
}

// A holder under 45 min is left alone, idle or not.
func TestAYoungHolderIsLeftAlone(t *testing.T) {
	r := newRig(t, "2")
	bus := r.projectBus(t, "proj")
	r.holder(t, 0, 100, 101, r.now, "tok1")
	r.procs = job(1.2, 0.4, 7.5)
	r.logFile(t, r.now.Add(-time.Hour))
	if rc := r.minutes(45, 10, 20); rc != 0 || lines(t, bus) != nil {
		t.Fatalf("held under 45 min: rc %d bus %q", rc, lines(t, bus))
	}
}

// A holder whose wrapper pid is gone or reused (another start time) and whose job group is gone is stale: said as
// such, once, not as a hung job.
func TestAStaleHolderSaysStale(t *testing.T) {
	r := newRig(t, "2")
	bus := r.projectBus(t, "proj")
	r.holder(t, 1, 100, 101, r.now.Add(-50*time.Minute), "tok1")
	r.procs = []proc{{pid: 100, ppid: 1, pgid: 100, start: "Sun Nov  8 02:55:00 2026", cmd: "/usr/bin/other"}}
	r.minutes(3, 10, 20)
	want := "2026-11-08T03:00 INFO crius:loadwatch -> proj:lead — heavy slot 1 holder pid 100 (go.sh) is gone or its pid reused, yet its slot file stays (held 50 min): a stale holder"
	if b := lines(t, bus); len(b) != 1 || b[0] != want {
		t.Fatalf("bus %q\nwant %q", b, want)
	}
}

// A holder of no known project goes to Crius; an output it cannot see is said, with the CPU named as the only judge
// (review of 9227798 P2-4), never read as "no output".
func TestAnUnknownProjectGoesToCrius(t *testing.T) {
	r := newRig(t, "2")
	r.holder(t, 1, 100, 101, r.now.Add(-50*time.Minute), "tok1")
	r.procs = job(1.2, 0.4, 7.5)
	r.minutes(16, 10, 20)
	want := "2026-11-08T03:15 INFO crius:loadwatch -> crius:lead — heavy slot 1 looks hung: pid 100 (go.sh) held 65 min, no CPU in its job for 15 min; check it or stop its group 101; output unseen (stdout is no file): judged on CPU only"
	if b := r.bus(t); len(b) != 1 || b[0] != want {
		t.Fatalf("bus %q\nwant %q", b, want)
	}
}

// ps that fails is a failure to look, loud, never "no hung job"; the slot rules still run.
func TestFailingToListProcessesIsLoud(t *testing.T) {
	r := newRig(t, "2")
	r.holder(t, 1, 100, 101, r.now.Add(-50*time.Minute), "tok1")
	r.psErr = errors.New("ps: no answer")
	if rc := r.minutes(1, 30, 29); rc != 2 || r.slots(t) != "1" {
		t.Fatalf("rc %d slots %s", rc, r.slots(t))
	}
	if !strings.Contains(r.out.String(), "LOUD") || !strings.Contains(r.out.String(), "hung-holder check: ps: no answer") {
		t.Fatalf("out %s", r.out.String())
	}
}

// ps lines: pid, ppid, pgid, CPU time, the five words of lstart, the command.
func TestParsePS(t *testing.T) {
	ps, err := parsePS([]byte("    1     0     1   1:02:03.50 Sat Nov  7 09:00:00 2026     /sbin/launchd\n  100     1    99   12:34.56 Sun Nov  8 02:30:00 2026     bun gate.ts run -- a b\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 2 || ps[0].cpu != 3723.5 || ps[1].cpu != 754.56 || ps[1].pgid != 99 || ps[1].start != "Sun Nov 8 02:30:00 2026" || ps[1].cmd != "bun gate.ts run -- a b" {
		t.Fatalf("%+v", ps)
	}
	if _, err := parsePS([]byte("100 1 99 garbage\n")); err == nil {
		t.Fatal("a bad line parsed")
	}
	if _, err := parsePS(nil); err == nil {
		t.Fatal("no processes parsed")
	}
}

// A project lookup that fails still sends the line, to Crius, once: the failure is said, the line is not repeated.
func TestALookupFailurePostsOnce(t *testing.T) {
	r := newRig(t, "2")
	r.holder(t, 1, 100, 101, r.now.Add(-50*time.Minute), "tok1")
	r.procs = job(1.2, 0.4, 7.5)
	r.leadErr = errors.New("projects.toml: broken")
	r.minutes(30, 10, 20)
	var hungLines int
	for _, l := range r.bus(t) {
		if strings.Contains(l, "-> crius:lead — heavy slot 1 looks hung") {
			hungLines++
		}
	}
	if hungLines != 1 {
		t.Fatalf("hung lines %d, bus %q", hungLines, r.bus(t))
	}
	if !strings.Contains(r.out.String(), "hung-holder check: the project of /work/repo: projects.toml: broken") {
		t.Fatalf("out %s", r.out.String())
	}
}

// A gap in the runs (the Mac asleep) is not idle time: the clock starts again.
func TestASleepIsNotIdleTime(t *testing.T) {
	r := newRig(t, "2")
	bus := r.projectBus(t, "proj")
	r.holder(t, 1, 100, 101, r.now.Add(-50*time.Minute), "tok1")
	r.procs = job(1.2, 0.4, 7.5)
	r.logFile(t, r.now.Add(-time.Hour))
	r.minutes(5, 10, 20)
	r.now = r.now.Add(time.Hour)
	if r.minutes(15, 10, 20); lines(t, bus) != nil {
		t.Fatalf("a sleep counted as idle: %q", lines(t, bus))
	}
	if r.minutes(1, 10, 20); len(lines(t, bus)) != 1 {
		t.Fatalf("15 idle minutes after the wake: bus %q", lines(t, bus))
	}
}

// Review of 9227798 P2-1: a stuck lsof (a hung network mount) held the run, and with it the slot rules, forever.
func TestAStuckLookIsCut(t *testing.T) {
	stuck := filepath.Join(t.TempDir(), "lsof")
	if err := os.WriteFile(stuck, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	defer func(p string, d time.Duration) { lsofPath, lookTimeout = p, d }(lsofPath, lookTimeout)
	lsofPath, lookTimeout = stuck, 300*time.Millisecond
	t0 := time.Now()
	_, err := sysOutputs([]int{os.Getpid()})
	if err == nil || !strings.Contains(err.Error(), "lsof gave no answer in 300ms") || time.Since(t0) > 5*time.Second {
		t.Fatalf("err %v after %s", err, time.Since(t0))
	}
}

// Review of 9227798 P2-2: once the job is gone and only the wrapper holds the slot, the line named the job's group,
// which no longer exists and may name another job when the lead acts; it names the wrapper's pid.
func TestAWrapperAloneIsNamedByItsPid(t *testing.T) {
	r := newRig(t, "2")
	bus := r.projectBus(t, "proj")
	r.holder(t, 1, 100, 101, r.now.Add(-50*time.Minute), "tok1")
	r.procs = job(1.2, 0.4, 7.5)[:2]
	r.logFile(t, r.now.Add(-time.Hour))
	r.minutes(16, 10, 20)
	if b := lines(t, bus); len(b) != 1 || !strings.Contains(b[0], "; check it or stop pid 100 (its job ended; the wrapper holds the slot); last output") {
		t.Fatalf("bus %q", b)
	}
}

// Review of 9227798 P2-3: a failing hung-holder check raised "slots are not following the load" and, failing every
// run, kept the one Failed flag set, so a real slot-rule failure after it got no ALERT. It has its own words and flag.
func TestAFailingHungCheckIsNotASlotFailure(t *testing.T) {
	r := newRig(t, "2")
	r.holder(t, 1, 100, 101, r.now.Add(-50*time.Minute), "tok1")
	r.psErr = errors.New("ps: no answer")
	if rc := r.minutes(3, 10, 20); rc != 2 {
		t.Fatalf("rc %d", rc)
	}
	b := r.bus(t)
	if len(b) != 1 || !strings.Contains(b[0], "ALERT crius:loadwatch -> crius:lead — the hung-holder check failed to look (the slots still follow the load): hung-holder check: ps: no answer") {
		t.Fatalf("bus %q", b)
	}
	if err := os.WriteFile(r.slotPath, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.minutes(1, 10, 20)
	if b := r.bus(t); len(b) != 2 || !strings.Contains(b[1], "slots are not following the load") {
		t.Fatalf("a slot failure after a failing hung check: bus %q", b)
	}
}
