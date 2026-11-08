package wakewatch

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/testutil"
)

// rigLines gives the ALERT lines on the team's bus.
func rigLines(t *testing.T, home string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, "bus", "team.log"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.Contains(l, " ALERT crius:wakewatch -> team:lead — rig services down") {
			out = append(out, l)
		}
	}
	return out
}

// pidFile writes <name>.pid in dir naming pid, last written at.
func pidFile(t *testing.T, dir, name string, pid int, at time.Time) {
	t.Helper()
	p := filepath.Join(dir, name+".pid")
	testutil.Write(t, p, strconv.Itoa(pid)+"\n")
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
}

// crius:lead DECISION 11-08: the Mac rebooted 11-07 19:45 and every rig service stayed down 11 hours, its pid files
// left from before the boot (one of their pids reused by another process), until a probe hit them. After a boot, and
// whenever a team that uses the rig runs, its leads get one ALERT naming the services that are down.
func TestRigServicesDownAfterTheBootAreSaidToTheTeam(t *testing.T) {
	home := setup(t, map[string]string{"team.log": ""})
	rig := filepath.Join(home, "rig-run")
	testutil.Write(t, filepath.Join(home, "projects.toml"), "[projects.team]\nleads = { lead = \"/nowhere/STATUS.md\" }\nrig = \""+rig+"\"\n")
	boot := time.Date(2026, 11, 7, 19, 45, 50, 0, time.Local)
	before := time.Date(2026, 11, 5, 19, 23, 0, 0, time.Local)
	pidFile(t, rig, "api", 501, before)                  // its pid now names another process
	pidFile(t, rig, "jobs", 502, before)                 // nothing runs it
	pidFile(t, rig, "gate", 503, boot.Add(11*time.Hour)) // restarted after the boot
	now := time.Date(2026, 11, 8, 6, 40, 0, 0, time.Local)
	f := &fake{boot: boot, ses: []Session{{Name: "team: lead", ID: "s1"}},
		procs: []Proc{{PID: 501, PPID: 1, Command: "/usr/libexec/other"}, {PID: 503, PPID: 1, Command: "python gate.py"}}}
	run := func() {
		t.Helper()
		if err := Run(f.deps(home, now)); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Minute)
	}
	run()
	got := rigLines(t, home)
	if len(got) != 1 || !strings.Contains(got[0], "rig services down since the boot at 11-07 19:45, 2 of the pid files in "+rig+"; start them before the team's next probe or test: api, jobs") {
		t.Fatalf("after the boot: %q", got)
	}
	run()
	if got := rigLines(t, home); len(got) != 1 {
		t.Fatalf("said again with nothing new: %q", got)
	}
	// started again: none down clears it; jobs going down later is said while the team runs
	pidFile(t, rig, "api", 601, now)
	pidFile(t, rig, "jobs", 602, now)
	f.procs = append(f.procs, Proc{PID: 601, PPID: 1, Command: "api"}, Proc{PID: 602, PPID: 1, Command: "jobs"})
	run()
	if got := rigLines(t, home); len(got) != 1 {
		t.Fatalf("all up: %q", got)
	}
	f.procs = f.procs[:3]
	run()
	if got := rigLines(t, home); len(got) != 2 || !strings.Contains(got[1], "rig services down while the team runs, 1 of the pid files in "+rig+"; start them before the team's next probe or test: jobs") {
		t.Fatalf("jobs down while the team runs: %q", got)
	}
	// with no team session, nothing is said until the next boot
	f.ses = nil
	pidFile(t, rig, "api", 701, now)
	run()
	if got := rigLines(t, home); len(got) != 2 {
		t.Fatalf("no team running, no boot: %q", got)
	}
	f.boot = now
	run()
	if got := rigLines(t, home); len(got) != 3 || !strings.Contains(got[2], "since the boot at") {
		t.Fatalf("a new boot with no team running: %q", got)
	}
}

// A boot time or process list it cannot read is said to Crius, never read as "all up".
func TestARigCheckThatCannotLookIsSaid(t *testing.T) {
	home := setup(t, map[string]string{"team.log": ""})
	testutil.Write(t, filepath.Join(home, "projects.toml"), "[projects.team]\nleads = { lead = \"/nowhere/STATUS.md\" }\nrig = \""+filepath.Join(home, "none")+"\"\n")
	f := &fake{bootErr: os.ErrPermission, ses: []Session{{Name: "team: lead", ID: "s1"}}}
	if err := Run(f.deps(home, t0)); err != nil {
		t.Fatal(err)
	}
	if s := strings.Join(f.sent, "\n"); !strings.Contains(s, "the rig check failed to look: boot time: permission denied") ||
		!strings.Contains(s, "rig folder: ") {
		t.Fatalf("sent %q", f.sent)
	}
}
