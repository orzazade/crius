package wakewatch

import (
	"os"
	"path/filepath"
	"regexp"
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
	f := &fake{boot: boot, ses: []Session{{Name: "team · lead", ID: "s1"}},
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
	f := &fake{bootErr: os.ErrPermission, ses: []Session{{Name: "team · lead", ID: "s1"}}}
	if err := Run(f.deps(home, t0)); err != nil {
		t.Fatal(err)
	}
	if s := strings.Join(f.sent, "\n"); !strings.Contains(s, "the rig check failed to look: boot time: permission denied") ||
		!strings.Contains(s, "rig folder: ") {
		t.Fatalf("sent %q", f.sent)
	}
}

// Review of 3d81460: a boot alert while no team runs (a reboot ends the sessions) is said again to the team's next
// session; services being started one by one stay quiet; a clock step of microseconds is no boot; 17 names fit the bus.
func TestARigAlertReachesTheTeamThatStartsAfterTheBoot(t *testing.T) {
	home := setup(t, map[string]string{"team.log": ""})
	rig := filepath.Join(home, "rig-run")
	testutil.Write(t, filepath.Join(home, "projects.toml"), "[projects.team]\nleads = { lead = \"/nowhere/STATUS.md\" }\nrig = \""+rig+"\"\n")
	boot := time.Date(2026, 11, 7, 19, 45, 50, 0, time.Local)
	before := boot.Add(-48 * time.Hour)
	var names []string
	for i := 0; i < 17; i++ {
		name := "service-number-" + strconv.Itoa(10+i)
		names = append(names, name)
		pidFile(t, rig, name, 500+i, before)
	}
	now := boot.Add(time.Minute)
	f := &fake{boot: boot}
	run := func() {
		t.Helper()
		if err := Run(f.deps(home, now)); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Minute)
	}
	run() // no team runs: said after the boot, to nobody running
	got := rigLines(t, home)
	if len(got) != 1 || !strings.Contains(got[0], "since the boot at 11-07 19:45, 17 of the pid files") {
		t.Fatalf("after the boot: %q", got)
	}
	text := got[0][strings.Index(got[0], " — ")+len(" — "):]
	if n := len([]rune(text)); n > 300 || !strings.Contains(text, ": service-number-10") || !regexp.MustCompile(` \+\d+ more$`).MatchString(text) {
		t.Fatalf("17 names in %d runes: %q", n, text)
	}
	f.boot = boot.Add(3 * time.Microsecond) // a clock step moves kern.boottime a little
	run()
	if got := rigLines(t, home); len(got) != 1 {
		t.Fatalf("a clock step read as a boot: %q", got)
	}
	f.ses = []Session{{Name: "team · lead", ID: "s1"}}
	run() // the lead resumes hours later: it hears the same set
	if got := rigLines(t, home); len(got) != 2 || !strings.Contains(got[1], "while the team runs, 17 of") {
		t.Fatalf("the team that starts after the boot: %q", got)
	}
	for i, name := range names[:5] { // the lead starts them one by one
		pidFile(t, rig, name, 900+i, now)
		f.procs = append(f.procs, Proc{PID: 900 + i, PPID: 1, Command: name})
		run()
	}
	if got := rigLines(t, home); len(got) != 2 {
		t.Fatalf("services being started: %q", got)
	}
	f.procs = f.procs[1:] // one it started dies again: a new one down
	run()
	if got := rigLines(t, home); len(got) != 3 || !strings.Contains(got[2], "13 of the pid files") {
		t.Fatalf("a started service down again: %q", got)
	}
	testutil.Write(t, filepath.Join(rig, names[0]+".pid"), "") // being rewritten (echo $! > x.pid empties it first)
	f.procs = append(f.procs, Proc{PID: 900, PPID: 1, Command: names[0]})
	run()
	if got := rigLines(t, home); len(got) != 3 {
		t.Fatalf("a pid file being written read as down: %q", got)
	}
}

// With no project naming a rig, the boot time is never read, so a failing read says nothing.
func TestNoRigNoBootRead(t *testing.T) {
	home := setup(t, map[string]string{"team.log": ""})
	testutil.Write(t, filepath.Join(home, "projects.toml"), "[projects.team]\nleads = { lead = \"/nowhere/STATUS.md\" }\n")
	f := &fake{bootErr: os.ErrPermission, ses: []Session{{Name: "team · lead", ID: "s1"}}}
	if err := Run(f.deps(home, t0)); err != nil {
		t.Fatal(err)
	}
	if s := strings.Join(f.sent, "\n"); strings.Contains(s, "rig check") {
		t.Fatalf("sent %q", f.sent)
	}
}
