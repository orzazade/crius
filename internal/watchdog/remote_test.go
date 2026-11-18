package watchdog

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/testutil"
)

// PostOn (wakewatch, loadwatch, dispatch) refuses a copy of a bus on another machine: the line would reach no one and the
// next pull overwrites it. The error names the wrapper the marker gives; the copy is left as it was.
func TestPostOnRefusesACopyOfARemoteBus(t *testing.T) {
	_, home := testutil.Temp(t)
	log := filepath.Join(home, "bus", "demo.log")
	old := "2026-11-18T09:00 INFO demo:lead -> crius — the last pulled line\n"
	testutil.Write(t, log, old)
	testutil.Write(t, log+".remote", "use /opt/wrap/bus-post-other (live log is on another machine)\n")
	now := time.Date(2026, 11, 19, 1, 0, 0, 0, time.Local)
	err := PostOn(home, "demo", "ALERT", "crius:wakewatch", "demo:lead", now, "a role died")
	if got := testutil.Read(t, log); got != old {
		t.Errorf("PostOn wrote into the copy of a remote bus:\n%s", got)
	}
	if err == nil || !strings.Contains(err.Error(), "is a copy of a bus on another machine, nothing posted") ||
		!strings.Contains(err.Error(), "/opt/wrap/bus-post-other") {
		t.Errorf("want a refusal that names the wrapper, got %v", err)
	} else {
		t.Logf("refused: %v", err)
	}
}

// Crius's own bus has no marker: PostOn writes it as before, a new log included.
func TestPostOnWritesALogWithNoMarker(t *testing.T) {
	_, home := testutil.Temp(t)
	testutil.Write(t, filepath.Join(home, "bus", ".keep"), "")
	now := time.Date(2026, 11, 19, 1, 0, 0, 0, time.Local)
	if err := PostOn(home, "crius", "ALERT", "crius:watchdog", "crius:lead", now, "disk low"); err != nil {
		t.Fatal(err)
	}
	got := testutil.Read(t, filepath.Join(home, "bus", "crius.log"))
	if want := "2026-11-19T01:00 ALERT crius:watchdog -> crius:lead — disk low\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	} else {
		t.Logf("written: %q", got)
	}
}
