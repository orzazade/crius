package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// rig is a scratch Crius home with a bus and a slots file, and a clock the test moves.
type rig struct {
	d        deps
	out      bytes.Buffer
	now      time.Time
	l5, l15  float64
	loadErr  error
	busPath  string
	slotPath string
}

func newRig(t *testing.T, slots string) *rig {
	t.Helper()
	dir := t.TempDir()
	r := &rig{now: time.Date(2026, 11, 8, 3, 0, 0, 0, time.Local)}
	if err := os.MkdirAll(filepath.Join(dir, "home", "bus"), 0o700); err != nil {
		t.Fatal(err)
	}
	r.busPath = filepath.Join(dir, "home", "bus", "crius.log")
	r.slotPath = filepath.Join(dir, "slots")
	if slots != "" {
		if err := os.WriteFile(r.slotPath, []byte(slots+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r.d = deps{home: filepath.Join(dir, "home"), slots: r.slotPath, state: filepath.Join(dir, "home", "state", "loadwatch.json"),
		now: func() time.Time { return r.now }, load: func() (float64, float64, error) { return r.l5, r.l15, r.loadErr }, out: &r.out}
	if err := os.MkdirAll(filepath.Dir(r.d.state), 0o700); err != nil {
		t.Fatal(err)
	}
	return r
}

// minutes runs the job once a minute for n minutes at the given loads and returns the last exit code.
func (r *rig) minutes(n int, l5, l15 float64) int {
	r.l5, r.l15 = l5, l15
	rc := 0
	for i := 0; i < n; i++ {
		rc = run(r.d)
		r.now = r.now.Add(time.Minute)
	}
	return rc
}

func (r *rig) slots(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(r.slotPath)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func (r *rig) bus(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(r.busPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// Owner 11-08: the 5-min load over 28 for 15 min drops the heavy slots to 1, with one bus line; 14 min over is not
// enough, and the count then stays 1 with no more lines.
func TestFifteenMinutesOverDropsToOneSlot(t *testing.T) {
	r := newRig(t, "2")
	if rc := r.minutes(15, 30, 29); rc != 0 || r.slots(t) != "2" || r.bus(t) != nil {
		t.Fatalf("after 14 min over: rc %d slots %s bus %q", rc, r.slots(t), r.bus(t))
	}
	if rc := r.minutes(1, 31.5, 29); rc != 0 || r.slots(t) != "1" {
		t.Fatalf("at 15 min over: rc %d slots %s", rc, r.slots(t))
	}
	r.minutes(10, 30, 29)
	want := "2026-11-08T03:15 INFO crius:loadwatch -> all — heavy slots 2 -> 1 (load5 31.50, load15 29.00)"
	if b := r.bus(t); len(b) != 1 || b[0] != want || r.slots(t) != "1" {
		t.Fatalf("bus %q, want one line %q; slots %s", b, want, r.slots(t))
	}
}

// The 15-min load under 28 gives 2 slots at once; between the two rules (load15 at 28 or over, load5 not over long
// enough) the count stays.
func TestFifteenMinuteLoadUnderTheLimitGivesTwo(t *testing.T) {
	r := newRig(t, "1")
	if r.minutes(5, 25, 28); r.slots(t) != "1" || r.bus(t) != nil {
		t.Fatalf("load15 28 is not under: slots %s bus %q", r.slots(t), r.bus(t))
	}
	r.minutes(1, 20, 27.9)
	if b := r.bus(t); r.slots(t) != "2" || len(b) != 1 || !strings.HasSuffix(b[0], "heavy slots 1 -> 2 (load5 20.00, load15 27.90)") {
		t.Fatalf("slots %s bus %q", r.slots(t), b)
	}
}

// Both rules hold (the 5-min load over 28 for 15 min while the 15-min load is still under 28): 1 wins, and it stays 1
// while the 5-min load stays over. Once it drops, 2 comes back.
func TestTheOverRuleWins(t *testing.T) {
	r := newRig(t, "2")
	r.minutes(16, 40, 27)
	if r.slots(t) != "1" {
		t.Fatalf("slots %s", r.slots(t))
	}
	r.minutes(5, 40, 27)
	if r.slots(t) != "1" || len(r.bus(t)) != 1 {
		t.Fatalf("slots %s bus %q", r.slots(t), r.bus(t))
	}
	r.minutes(1, 20, 27)
	if r.slots(t) != "2" || len(r.bus(t)) != 2 {
		t.Fatalf("slots %s bus %q", r.slots(t), r.bus(t))
	}
}

// A gap of more than 3 min between runs (the Mac slept) is not time over the limit: the 15 min start again.
func TestAGapBetweenRunsStartsTheCountAgain(t *testing.T) {
	r := newRig(t, "2")
	r.minutes(10, 30, 29)
	r.now = r.now.Add(20 * time.Minute)
	if r.minutes(15, 30, 29); r.slots(t) != "2" {
		t.Fatalf("a slept gap counted as over: slots %s", r.slots(t))
	}
	if r.minutes(1, 30, 29); r.slots(t) != "1" {
		t.Fatalf("slots %s", r.slots(t))
	}
}

// A value set by hand (not 1 or 2) is left alone, said in the log, and posts nothing.
func TestAHandSetValueIsLeftAlone(t *testing.T) {
	r := newRig(t, "3")
	if rc := r.minutes(20, 40, 40); rc != 0 || r.slots(t) != "3" || r.bus(t) != nil || !strings.Contains(r.out.String(), `holds "3", not 1 or 2: left alone`) {
		t.Fatalf("rc %d slots %s bus %q out %q", rc, r.slots(t), r.bus(t), r.out.String())
	}
}

// Failing to look or set is loud: exit 2 every run, one ALERT per spell, and another after it recovered and failed
// again. A missing slots file is such a failure, never "nothing to do".
func TestFailingToLookIsLoudOncePerSpell(t *testing.T) {
	r := newRig(t, "2")
	r.loadErr = errors.New("sysctl vm.loadavg: exit status 1")
	if rc := r.minutes(3, 0, 0); rc != 2 {
		t.Fatalf("rc %d", rc)
	}
	b := r.bus(t)
	if len(b) != 1 || !strings.Contains(b[0], " ALERT crius:loadwatch -> crius:lead — the heavy-slot load watcher failed") || !strings.Contains(b[0], "sysctl vm.loadavg") {
		t.Fatalf("bus %q", b)
	}
	r.loadErr = nil
	if rc := r.minutes(1, 10, 10); rc != 0 {
		t.Fatalf("recovered: rc %d out %s", rc, r.out.String())
	}
	if err := os.Remove(r.slotPath); err != nil {
		t.Fatal(err)
	}
	if rc := r.minutes(2, 10, 10); rc != 2 || len(r.bus(t)) != 2 || !strings.Contains(r.bus(t)[1], "slots") {
		t.Fatalf("rc %d bus %q", rc, r.bus(t))
	}
	if strings.Count(r.out.String(), "LOUD crius-loadwatch failed") != 5 {
		t.Fatalf("every failed run says so in the log: %s", r.out.String())
	}
}

// The slots file is replaced whole, keeps a mode others can read, and leaves no temp file behind.
func TestTheSlotsFileIsReplacedWhole(t *testing.T) {
	r := newRig(t, "1")
	r.minutes(1, 10, 10)
	fi, err := os.Stat(r.slotPath)
	if err != nil {
		t.Fatal(err)
	}
	left, _ := filepath.Glob(filepath.Join(filepath.Dir(r.slotPath), ".slots.*"))
	if b, _ := os.ReadFile(r.slotPath); string(b) != "2\n" || fi.Mode().Perm() != 0o644 || left != nil {
		t.Fatalf("%q %v %v", b, fi.Mode(), left)
	}
}

// An empty or broken slots file is a failure (ALERT), never a value set by hand (review P3-1).
func TestAnEmptySlotsFileIsLoud(t *testing.T) {
	for _, v := range []string{"", "two", "-1"} {
		r := newRig(t, "x")
		if err := os.WriteFile(r.slotPath, []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
		if rc := r.minutes(1, 10, 10); rc != 2 || len(r.bus(t)) != 1 || !strings.Contains(r.bus(t)[0], "not a slot count") {
			t.Errorf("%q: rc %d bus %q", v, rc, r.bus(t))
		}
	}
}
