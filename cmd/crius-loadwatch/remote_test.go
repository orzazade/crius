package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// A hung job of a team whose bus log here is a copy of a bus on another machine: the line cannot go on that bus, so it
// goes to Crius as for a project with no bus, once, and the run is not a failure.
func TestAHungHolderOfACopiedBusGoesToCrius(t *testing.T) {
	r := newRig(t, "2")
	bus := r.projectBus(t, "far")
	if err := os.WriteFile(bus+".remote", []byte("use /opt/wrap/bus-post-far (live log is on another machine)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.holder(t, 1, 100, 101, r.now.Add(-50*time.Minute), "tok1")
	r.procs = job(1.2, 0.4, 7.5)
	if rc := r.minutes(16, 10, 20); rc != 0 {
		t.Fatalf("rc %d, out %s", rc, r.out.String())
	}
	r.minutes(5, 10, 20)
	if b := lines(t, bus); b != nil {
		t.Errorf("the copy was written: %q", b)
	}
	got := r.bus(t)
	if len(got) != 1 || !strings.Contains(got[0], "INFO crius:loadwatch -> crius:lead — for far:lead: heavy slot 1 looks hung") {
		t.Fatalf("want one INFO to Crius, got %q (out %s)", got, r.out.String())
	}
	t.Logf("crius.log: %s", got[0])
}

// Round-2 review: the line to Crius names the wrapper that reaches the team's bus, and the job's last output (the end
// of the line) stays inside the bus's 300-rune cut.
func TestAHungHolderOfACopiedBusNamesTheWrapperAndKeepsItsTail(t *testing.T) {
	r := newRig(t, "2")
	bus := r.projectBus(t, "far")
	if err := os.WriteFile(bus+".remote", []byte("use /opt/wrap/bus-post-far (live log is on another machine)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r.holder(t, 1, 100, 101, r.now.Add(-30*time.Minute), "tok1")
	r.procs = job(1.2, 0.4, 7.5)
	r.logFile(t, r.now.Add(-time.Hour))
	r.minutes(16, 10, 20)
	got := r.bus(t)
	if len(got) != 1 || !strings.Contains(got[0], "last output 02:00 (job.log)") || !strings.Contains(got[0], "/opt/wrap/bus-post-far") {
		t.Fatalf("want one line to Crius with the job's last output and the wrapper, got %q", got)
	}
	t.Logf("crius.log: %s", got[0])
}
