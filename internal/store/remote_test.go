package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orzazade/crius/internal/testutil"
)

// The marker's first line is shown as one line: control characters in it become spaces; an empty one says so.
func TestRemoteCopyShowsTheMarkerAsOneLine(t *testing.T) {
	_, home := testutil.Temp(t)
	log := filepath.Join(home, "x.log")
	if _, ok, err := RemoteCopy(log); ok || err != nil {
		t.Fatalf("no marker: want not remote, got %v %v", ok, err)
	}
	testutil.Write(t, log+".remote", "use a\rb\x1b[2Jc\r\nsecond\n")
	if use, ok, err := RemoteCopy(log); !ok || err != nil || use != "use a b [2Jc" {
		t.Errorf("got %q %v %v", use, ok, err)
	}
	testutil.Write(t, log+".remote", "\n")
	if use, ok, _ := RemoteCopy(log); !ok || !strings.Contains(use, "names no wrapper") {
		t.Errorf("empty marker: got %q %v", use, ok)
	}
}

// copyBus makes <home>/bus/<project>.log holding one line, and, when marked, the <log>.remote marker of a copy of a bus on
// another machine. It gives the log's path and its text.
func copyBus(t *testing.T, home, project string, marked bool) (string, string) {
	t.Helper()
	log := filepath.Join(home, "bus", project+".log")
	old := "2026-11-18T09:00 INFO demo:lead -> crius — the last pulled line\n"
	testutil.Write(t, log, old)
	if marked {
		testutil.Write(t, log+".remote", "use /opt/wrap/bus-post-other (live log is on another machine)\nsecond line\n")
	}
	return log, old
}

// A line appended to a copy of a bus on another machine reaches no one and the next pull overwrites it: AppendLine (used by
// crius-rec bus and ingest's PAUSE lines) refuses it, says the wrapper the marker names, and writes nothing.
func TestAppendLineRefusesACopyOfARemoteBus(t *testing.T) {
	_, home := testutil.Temp(t)
	log, old := copyBus(t, home, "demo", true)
	err := AppendLine(home, "demo", "2026-11-19T01:00 INFO crius:ingest -> demo:lead — PAUSE")
	if got := testutil.Read(t, log); got != old {
		t.Errorf("AppendLine wrote into the copy of a remote bus:\n%s", got)
	}
	if err == nil || !strings.Contains(err.Error(), "is a copy of a bus on another machine, nothing posted") ||
		!strings.Contains(err.Error(), "/opt/wrap/bus-post-other") {
		t.Errorf("want a refusal that names the wrapper, got %v", err)
	} else {
		t.Logf("refused: %v", err)
	}
}

// A log with no marker is written as before.
func TestAppendLineWritesALogWithNoMarker(t *testing.T) {
	_, home := testutil.Temp(t)
	log, old := copyBus(t, home, "demo", false)
	if err := AppendLine(home, "demo", "2026-11-19T01:00 INFO crius:lead -> demo:lead — go"); err != nil {
		t.Fatal(err)
	}
	if got, want := testutil.Read(t, log), old+"2026-11-19T01:00 INFO crius:lead -> demo:lead — go\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	} else {
		t.Logf("written: %q", got)
	}
}

// A decision on a team whose bus is a copy is refused before DECISIONS.md is touched: a saved decision whose bus line then
// fails is half done ("post the bus line by hand").
func TestRecordDecisionOnACopyOfARemoteBusRecordsNothing(t *testing.T) {
	c := newDecCase(t)
	log, old := copyBus(t, c.home, "demo", true)
	_, err := c.rec("use the new queue", DecisionInput{})
	if err == nil || !strings.Contains(err.Error(), "is a copy of a bus on another machine, nothing recorded") {
		t.Errorf("want a refusal, got %v", err)
	} else {
		t.Logf("refused: %v", err)
	}
	if _, serr := os.Stat(filepath.Join(c.home, "state", "demo", "DECISIONS.md")); !os.IsNotExist(serr) {
		t.Errorf("DECISIONS.md was written for a refused decision (stat: %v)", serr)
	}
	if got := testutil.Read(t, log); got != old {
		t.Errorf("the copy was written:\n%s", got)
	}
}

// The refusal is errors.Is ErrRemoteCopy, so a caller can tell it from a failed write, and still a refusal for crius-rec.
func TestTheCopyRefusalIsErrRemoteCopy(t *testing.T) {
	_, home := testutil.Temp(t)
	copyBus(t, home, "demo", true)
	err := AppendLine(home, "demo", "2026-11-19T01:00 INFO crius:lead -> demo:lead — go")
	if !errors.Is(err, ErrRemoteCopy) || !IsRefused(err) {
		t.Errorf("want errors.Is ErrRemoteCopy and a refusal, got %v (Is %v, refused %v)", err, errors.Is(err, ErrRemoteCopy),
			IsRefused(err))
	}
}
