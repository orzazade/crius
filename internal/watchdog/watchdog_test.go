package watchdog

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/testutil"
)

type wc struct {
	t            *testing.T
	home, tx, hb string
	now          time.Time
}

func newWC(t *testing.T) *wc {
	root, home := testutil.Temp(t)
	for _, d := range []string{"bus", "state"} {
		os.MkdirAll(filepath.Join(home, d), 0o700)
	}
	txRoot := filepath.Join(root, "projects")
	testutil.Write(t, filepath.Join(home, "projects.toml"), "[transcripts]\npersonal = \""+txRoot+"\"\n")
	w := &wc{t: t, home: home, tx: filepath.Join(txRoot, "-x", "s.jsonl"), hb: filepath.Join(home, "heartbeat"),
		now: time.Now().Truncate(time.Second)}
	testutil.Write(t, w.tx, "{}\n")
	testutil.Write(t, w.hb, "x")
	return w
}

func (w *wc) age(path string, ago time.Duration) {
	ts := w.now.Add(-ago)
	if err := os.Chtimes(path, ts, ts); err != nil {
		w.t.Fatal(err)
	}
}

func (w *wc) watch(now, wake time.Time) []string {
	w.t.Helper()
	if err := Check(w.home, now, wake, 20*time.Minute); err != nil {
		w.t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(w.home, "bus", "crius.log"))
	if os.IsNotExist(err) {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func TestStaleHeartbeatWithGrowingTranscriptsAlertsOnce(t *testing.T) {
	w := newWC(t)
	w.age(w.hb, 21*time.Minute)
	w.age(w.tx, time.Minute)
	got := w.watch(w.now, time.Time{})
	if len(got) != 1 || !regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d ALERT crius:watchdog -> crius — ingest heartbeat 21 min old`).MatchString(got[0]) {
		t.Fatal(got)
	}
	if got := w.watch(w.now, time.Time{}); len(got) != 1 {
		t.Fatal(got)
	}
}

func TestFreshHeartbeatOrQuietTranscriptsDoNotAlert(t *testing.T) {
	w := newWC(t)
	w.age(w.hb, 10*time.Minute)
	w.age(w.tx, time.Minute)
	if got := w.watch(w.now, time.Time{}); got != nil {
		t.Fatal(got)
	}
	w.age(w.hb, time.Hour)
	w.age(w.tx, 2*time.Hour)
	if got := w.watch(w.now, time.Time{}); got != nil {
		t.Fatal(got)
	}
}

func TestRecentWakeFromSleepDoesNotAlert(t *testing.T) {
	w := newWC(t)
	w.age(w.hb, 8*time.Hour)
	w.age(w.tx, time.Minute)
	if got := w.watch(w.now, w.now.Add(-5*time.Minute)); got != nil {
		t.Fatal(got)
	}
}

func TestMissingHeartbeatAlerts(t *testing.T) {
	w := newWC(t)
	os.Remove(w.hb)
	w.age(w.tx, time.Minute)
	got := w.watch(w.now, time.Time{})
	if len(got) != 1 || !strings.Contains(got[0], "no ingest heartbeat") {
		t.Fatal(got)
	}
}

// Codex P2 on the Python version: the watchdog watched fixed folders, not the ones the ingest reads.
func TestTheWatchdogReadsTheTranscriptRootsFromProjectsToml(t *testing.T) {
	w := newWC(t)
	other := filepath.Join(filepath.Dir(w.home), "other-root")
	testutil.Write(t, filepath.Join(w.home, "projects.toml"), "[transcripts]\nwork = \""+other+"\"\n")
	tx := filepath.Join(other, "-y", "s.jsonl")
	testutil.Write(t, tx, "{}\n")
	w.age(w.hb, time.Hour)
	w.age(w.tx, 2*time.Hour)
	w.age(tx, time.Minute)
	if got := w.watch(w.now, time.Time{}); len(got) != 1 {
		t.Fatal(got)
	}
}

func TestNoTranscriptRootIsAnError(t *testing.T) {
	w := newWC(t)
	testutil.Write(t, filepath.Join(w.home, "projects.toml"), "[transcripts]\nwork = \"/nonexistent/crius-test\"\n")
	if err := Check(w.home, w.now, time.Time{}, 20*time.Minute); err == nil {
		t.Fatal("no error")
	}
}

// Design §9 step 2: stop the ingest -> alert in <= 30 min. The ingest stamps at t0 and stops; the watchdog runs
// every 5 min (launchd); sessions keep writing.
func TestAStoppedIngestIsFlaggedWithin30Minutes(t *testing.T) {
	w := newWC(t)
	t0 := w.now.Add(-time.Hour)
	w.age(w.hb, time.Hour)
	var first time.Duration
	for step := 1; step <= 12; step++ {
		at := t0.Add(time.Duration(step) * 5 * time.Minute)
		ts := at.Add(-30 * time.Second)
		os.Chtimes(w.tx, ts, ts)
		if got := w.watch(at, time.Time{}); got != nil && first == 0 {
			first = at.Sub(t0)
		}
	}
	if first == 0 || first > 30*time.Minute {
		t.Fatal(first)
	}
}

// Codex P1 on the Go version: a root that could not be read was dropped, so the watchdog stayed quiet.
func TestAnUnreadableRootIsAnError(t *testing.T) {
	w := newWC(t)
	locked := filepath.Join(filepath.Dir(w.home), "locked")
	os.MkdirAll(filepath.Join(locked, "projects"), 0o700)
	os.Chmod(locked, 0)
	t.Cleanup(func() { os.Chmod(locked, 0o700) })
	testutil.Write(t, filepath.Join(w.home, "projects.toml"), "[transcripts]\npersonal = \""+filepath.Dir(filepath.Dir(w.tx))+
		"\"\nwork = \""+filepath.Join(locked, "projects")+"\"\n")
	w.age(w.hb, time.Hour)
	w.age(w.tx, 2*time.Hour)
	if err := Check(w.home, w.now, time.Time{}, 20*time.Minute); err == nil {
		t.Fatal("no error")
	}
}

// Codex P2 on the Go version: after the clock moved back, a heartbeat or wake time in the future read as fresh.
func TestAFutureHeartbeatOrWakeIsAnError(t *testing.T) {
	w := newWC(t)
	w.age(w.hb, -2*time.Hour)
	if err := Check(w.home, w.now, time.Time{}, 20*time.Minute); err == nil {
		t.Fatal("future heartbeat: no error")
	}
	w.age(w.hb, time.Minute)
	if err := Check(w.home, w.now, w.now.Add(2*time.Hour), 20*time.Minute); err == nil {
		t.Fatal("future wake: no error")
	}
}

// Codex P2 on the Go version: a root that is a symlink was accepted but never walked.
func TestASymlinkedRootIsWalked(t *testing.T) {
	w := newWC(t)
	link := filepath.Join(filepath.Dir(w.home), "link")
	if err := os.Symlink(filepath.Dir(filepath.Dir(w.tx)), link); err != nil {
		t.Fatal(err)
	}
	testutil.Write(t, filepath.Join(w.home, "projects.toml"), "[transcripts]\npersonal = \""+link+"\"\n")
	w.age(w.hb, time.Hour)
	w.age(w.tx, time.Minute)
	if got := w.watch(w.now, time.Time{}); len(got) != 1 {
		t.Fatal(got)
	}
}

// Codex P2 on the Go version: with no heartbeat, activity during the wake grace fell out of the moving window.
func TestAMissingHeartbeatStillAlertsAfterTheWakeGrace(t *testing.T) {
	w := newWC(t)
	os.Remove(w.hb)
	w.age(w.tx, 26*time.Minute)
	if got := w.watch(w.now, w.now.Add(-30*time.Minute)); len(got) != 1 {
		t.Fatal(got)
	}
}

// Codex P2 on the Go version: after a recovery, a second missing heartbeat did not alert again.
func TestAFreshHeartbeatRearmsTheMissingAlert(t *testing.T) {
	w := newWC(t)
	os.Remove(w.hb)
	w.age(w.tx, time.Minute)
	w.watch(w.now, time.Time{})
	testutil.Write(t, w.hb, "x")
	w.age(w.hb, time.Minute)
	w.watch(w.now, time.Time{})
	os.Remove(w.hb)
	if got := w.watch(w.now, time.Time{}); len(got) != 2 {
		t.Fatal(got)
	}
}
