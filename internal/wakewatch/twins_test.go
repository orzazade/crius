package wakewatch

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/sessionstate"
)

// The 19:09 shape (11-17): a bus-post delivery to axi:ms-flow made T3 start the thread's stored session 05473ed1 (claude
// pid 73035) while another T3-spawned claude of the same role, session 6376280f (pid 77924), still worked. Both claude
// processes write their own status file named "AXI · ms-flow", and crius-hook files both sessions under axi/ms-flow.
const (
	msThread = "mcp:launch:9981e1aa"
	sesOld   = "6376280f-b263-47d9-91fb-bb90c886399a"
	sesNew   = "05473ed1-b762-4445-93c2-e2618272f625"
)

func twinsFake() *fake {
	return &fake{
		ses: []Session{
			{Name: "AXI · ms-flow", ID: sesNew, PID: 73035},
			{Name: "AXI · ms-flow", ID: sesOld, PID: 77924},
			{Name: "AXI · qa", ID: "b5eb7552-0000", PID: 51532},
			// crius-hook files a session with no role in its title as "unknown": four such are no twins
			{Name: "crius-0d", ID: "ade32539-0000", PID: 98788},
			{Name: "crius-7c", ID: "8a015dd7-0000", PID: 78521},
			{Name: "Crius", ID: "8149e2ae-0000", PID: 17903},
		},
		states: []sessionstate.State{
			{SessionID: sesNew, Project: "axi", Role: "ms-flow", Thread: msThread},
			{SessionID: sesOld, Project: "axi", Role: "ms-flow", Thread: msThread},
			{SessionID: "b5eb7552-0000", Project: "axi", Role: "qa"},
			{SessionID: "ade32539-0000", Project: "crius", Role: "unknown"},
			{SessionID: "8a015dd7-0000", Project: "crius", Role: "unknown"},
			{SessionID: "8149e2ae-0000", Project: "crius", Role: "unknown"},
		},
	}
}

func twinWakes(f *fake) []string {
	var out []string
	for _, s := range f.sent {
		if strings.Contains(s, "live sessions for one role") {
			out = append(out, s)
		}
	}
	return out
}

// Two live claude processes for one role are said once, on the second run in a row that sees them, naming each pid and
// session; the same pair is never said again while it lasts, and a new pair is.
func TestTwoLiveSessionsForOneRoleWakeCriusOnce(t *testing.T) {
	home := setup(t, map[string]string{"axi.log": ""})
	f := twinsFake()
	runOnce(t, home, f, t0)
	if w := twinWakes(f); len(w) != 0 {
		t.Fatalf("one run's sight of two sessions woke Crius: %q", w)
	}
	runOnce(t, home, f, t0.Add(5*time.Minute))
	w := twinWakes(f)
	if len(w) != 1 {
		t.Fatalf("want one twin wake on the second run in a row, got %q (all sent: %q)", w, f.sent)
	}
	for _, want := range []string{"axi:ms-flow", "pid 73035", "pid 77924", "05473ed1", "6376280f", msThread, "crius-wakewatch restart axi:ms-flow"} {
		if !strings.Contains(w[0], want) {
			t.Errorf("wake %q lacks %q", w[0], want)
		}
	}
	for _, not := range []string{"crius:unknown", "axi:qa"} {
		if strings.Contains(w[0], not) {
			t.Errorf("wake %q names %q", w[0], not)
		}
	}
	for _, at := range []time.Duration{10 * time.Minute, 40 * time.Minute, 13 * time.Hour} {
		runOnce(t, home, f, t0.Add(at))
	}
	// a run that could not list the sessions or read crius-hook's records forgets nothing: the pair is not said again
	for _, fail := range []func(){func() { f.sesErr = errors.New("sessions: busy") }, func() { f.stErr = errors.New("states: busy") }} {
		fail()
		runOnce(t, home, f, t0.Add(13*time.Hour+time.Minute))
		f.sesErr, f.stErr = nil, nil
		runOnce(t, home, f, t0.Add(13*time.Hour+2*time.Minute))
	}
	if w := twinWakes(f); len(w) != 1 {
		t.Fatalf("one lasting pair woke Crius %d times: %q", len(w), w)
	}
	// Crius stopped pid 73035; T3 later started another claude for the role: a new pair is said again
	f.ses = append([]Session{}, f.ses[1:]...)
	runOnce(t, home, f, t0.Add(13*time.Hour+5*time.Minute))
	f.ses = append(f.ses, Session{Name: "AXI · ms-flow", ID: "1111aaaa-0000", PID: 80001})
	runOnce(t, home, f, t0.Add(13*time.Hour+10*time.Minute))
	runOnce(t, home, f, t0.Add(13*time.Hour+15*time.Minute))
	if w := twinWakes(f); len(w) != 2 || !strings.Contains(w[1], "pid 80001") {
		t.Fatalf("a new pair for the role: %q", w)
	}
}

// A pair seen on one run only (a hand-over: the old claude exits a moment after the new one starts) never wakes Crius.
func TestAPairSeenOnOneRunOnlyDoesNotWakeCrius(t *testing.T) {
	home := setup(t, map[string]string{"axi.log": ""})
	f := twinsFake()
	runOnce(t, home, f, t0)
	f.ses = f.ses[1:]
	runOnce(t, home, f, t0.Add(5*time.Minute))
	runOnce(t, home, f, t0.Add(10*time.Minute))
	if w := twinWakes(f); len(w) != 0 {
		t.Fatalf("a pair seen once woke Crius: %q", w)
	}
}

// The role comes from crius-hook's record of the session, so a resumed session with no name still counts; with no
// record, the session's own name gives it.
func TestTwinsAreFoundByHookRecordOrByName(t *testing.T) {
	home := setup(t, map[string]string{"axi.log": ""})
	f := &fake{
		ses: []Session{
			{Name: "", ID: sesNew, PID: 73035},                   // resumed without -n: crius-hook's record names the role
			{Name: "AXI · ms-flow", ID: "no-record", PID: 77924}, // no record: its name does
			{Name: "AXI · qa", ID: "qa-1", PID: 51532},
			{Name: "AXI · qa-2", ID: "qa-2", PID: 69726},
		},
		states: []sessionstate.State{{SessionID: sesNew, Project: "axi", Role: "ms-flow", Thread: msThread}},
	}
	runOnce(t, home, f, t0)
	runOnce(t, home, f, t0.Add(5*time.Minute))
	w := twinWakes(f)
	if len(w) != 1 || !strings.Contains(w[0], "axi:ms-flow") || !strings.Contains(w[0], "pid 73035") || !strings.Contains(w[0], "pid 77924") {
		t.Fatalf("want one axi:ms-flow twin wake, got %q", w)
	}
}
