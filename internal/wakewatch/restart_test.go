package wakewatch

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orzazade/crius/internal/sessionstate"
	"github.com/orzazade/crius/internal/testutil"
)

// restartFake is a home with axi:ms-flow running as a T3 thread and staffy:builder as a tmux tab on the ThinkPad.
type restartFake struct {
	f       *fake
	tabs    []RemoteTab
	screen  map[string]string // tab target -> its screen
	capErr  error
	sendErr map[string]error // text -> the error crius-tsend gives it
	typed   []string         // target + " " + text of each send
	out     bytes.Buffer
}

func newRestartFake(t *testing.T) (*restartFake, string) {
	home := t.TempDir()
	testutil.Write(t, filepath.Join(home, "roles", "axi.ms-flow.json"),
		`{"session_id":"`+sesOld+`","project":"axi","role":"ms-flow","t3_thread":"`+msThread+`","time":"2026-11-17T13:48:00Z"}`)
	f := twinsFake()
	f.threads = map[string]T3Thread{msThread: {Status: "completed", Updated: t0}}
	return &restartFake{f: f, tabs: []RemoteTab{{"thinkpad", "staffy:builder"}}, screen: map[string]string{},
		sendErr: map[string]error{}}, home
}

func (r *restartFake) deps(home string) RestartDeps {
	return RestartDeps{Home: home, Out: &r.out,
		Sessions: func() ([]Session, error) { return r.f.ses, r.f.sesErr },
		States:   func() ([]sessionstate.State, error) { return r.f.states, r.f.stErr },
		Thread: func(id string) (T3Thread, error) {
			th, ok := r.f.threads[id]
			if !ok {
				return T3Thread{}, errors.New("no thread " + id)
			}
			return th, r.f.threadErr
		},
		Tabs:    func() ([]RemoteTab, error) { return r.tabs, nil },
		Capture: func(t RemoteTab) (string, error) { return r.screen[t.Target], r.capErr },
		SendTab: func(t RemoteTab, text string) error {
			if err := r.sendErr[text]; err != nil {
				return err
			}
			r.typed = append(r.typed, t.Target+" "+text)
			if text == "/clear" {
				r.screen[t.Target] = readyTab
			}
			return nil
		},
	}
}

var (
	readyTab = tab("staffy · builder", "Done. ✅", "", t0)
	busyTab  = tab("staffy · builder", "Bash(make test)\n\n✻ Running… (12s · esc to interrupt)", "  ⏵⏵ bypass permissions on\n", t0)
	jobsTab  = tab("staffy · builder", "Started the red run. ⏳", "  ⏵⏵ bypass permissions on · 1 shell\n", t0)
	// the bus waiter every idle session keeps: a monitor, which /clear does not wait for (11-17 18:57 and 20:08, staffy:qa)
	waiterTab = tab("staffy · builder", "READY-RESTART posted. ⏳", "  ⏵⏵ bypass permissions on · 1 monitor\n", t0)
	// a monitor watching a run beside the waiter: two monitors, so still busy
	watchTab = tab("staffy · builder", "Watching the red run. ⏳", "  ⏵⏵ bypass permissions on · 2 monitors\n", t0)
)

func wantOut(t *testing.T, r *restartFake, code, want int, has ...string) {
	t.Helper()
	if code != want {
		t.Errorf("exit %d, want %d; said %q", code, want, r.out.String())
	}
	for _, h := range has {
		if !strings.Contains(r.out.String(), h) {
			t.Errorf("said %q, lacks %q", r.out.String(), h)
		}
	}
}

// The 19:09 shape: the thread is idle, yet two claudes run for the role. The helper refuses to say ready, names each
// pid and session, and gives the exact next step: stop the thread, then run it again.
func TestRestartOfARoleWithTwoLiveClaudesSaysStopFirst(t *testing.T) {
	r, home := newRestartFake(t)
	code := Restart(r.deps(home), "axi:ms-flow", "Read BRIEF.md and go")
	wantOut(t, r, code, RestartLive, "LIVE axi:ms-flow", "pid 73035", "pid 77924", "05473ed1", "6376280f",
		msThread, "kill <pid>", "crius-wakewatch restart axi:ms-flow", "never pkill -f")
	if strings.Contains(r.out.String(), "t3_thread_send") {
		t.Errorf("said to send while claudes run: %q", r.out.String())
	}
}

// A thread with a turn running is refused before anything else: a fresh send now is queued behind the turn and leaves
// the old claude running (11-17 17:45).
func TestRestartOfABusyT3ThreadIsRefused(t *testing.T) {
	r, home := newRestartFake(t)
	r.f.threads[msThread] = T3Thread{Status: "running", Active: true, Updated: t0}
	wantOut(t, r, Restart(r.deps(home), "axi:ms-flow", ""), RestartBusy, "BUSY axi:ms-flow", "turn running")
	r.out.Reset()
	r.f.threads[msThread] = T3Thread{Status: "completed", Pending: 1, Updated: t0}
	wantOut(t, r, Restart(r.deps(home), "axi:ms-flow", ""), RestartBusy, "BUSY axi:ms-flow", "1 request")
}

// The role's claudes run in two threads (a delegate's, say): a turn running in either refuses.
func TestRestartChecksEveryThreadTheRolesClaudesRunIn(t *testing.T) {
	r, home := newRestartFake(t)
	r.f.states[0].Thread = "mcp:other"
	r.f.threads["mcp:other"] = T3Thread{Status: "running", Active: true, Updated: t0}
	wantOut(t, r, Restart(r.deps(home), "axi:ms-flow", ""), RestartBusy, "BUSY axi:ms-flow", "mcp:other")
}

// Idle, and no claude runs for the role: ready, with the exact fresh-context send and the brief.
func TestRestartOfAnIdleThreadWithNoClaudeIsReady(t *testing.T) {
	r, home := newRestartFake(t)
	r.f.ses = r.f.ses[2:]
	wantOut(t, r, Restart(r.deps(home), "axi:ms-flow", "Read BRIEF.md and go"), RestartReady, "READY axi:ms-flow",
		"t3_thread_send", msThread, "freshContext", "Read BRIEF.md and go")
}

// One claude left after the stop is still a claude: not ready.
func TestRestartWithOneClaudeLeftIsNotReady(t *testing.T) {
	r, home := newRestartFake(t)
	r.f.ses = r.f.ses[1:]
	wantOut(t, r, Restart(r.deps(home), "axi:ms-flow", ""), RestartLive, "LIVE axi:ms-flow", "pid 77924")
}

// A look that failed is never read as idle or gone.
func TestRestartThatCannotLookSaysSo(t *testing.T) {
	r, home := newRestartFake(t)
	r.f.threadErr = errors.New("t3 gave no answer in time")
	wantOut(t, r, Restart(r.deps(home), "axi:ms-flow", ""), RestartFailed, "could not", "t3 gave no answer")
	r, home = newRestartFake(t)
	r.f.sesErr = errors.New("sessions: permission denied")
	wantOut(t, r, Restart(r.deps(home), "axi:ms-flow", ""), RestartFailed, "could not", "permission denied")
	r, home = newRestartFake(t)
	wantOut(t, r, Restart(r.deps(home), "nosuch:role", ""), RestartFailed, "nosuch:role", "no T3 thread")
	r, home = newRestartFake(t) // no live claude names a thread, and the role file is damaged
	r.f.ses = r.f.ses[2:]
	testutil.Write(t, filepath.Join(home, "roles", "axi.ms-flow.json"), `{"session_id":`)
	wantOut(t, r, Restart(r.deps(home), "axi:ms-flow", ""), RestartFailed, "could not read its role file")
	r, home = newRestartFake(t)
	wantOut(t, r, Restart(r.deps(home), "axi", ""), RestartFailed, "<project>:<role>")
}

// A tmux tab mid-turn, or with background jobs still running, is refused and nothing is typed (11-17 18:3x: /clear and
// the brief sent to a busy quotes:builder-2 were queued behind its turn, so the brief ran in the old context).
func TestRestartOfABusyTmuxTabTypesNothing(t *testing.T) {
	for name, screen := range map[string]string{"turn": busyTab, "jobs": jobsTab, "two monitors": watchTab} {
		r, home := newRestartFake(t)
		r.screen["staffy:builder"] = screen
		wantOut(t, r, Restart(r.deps(home), "staffy:builder", "Read BRIEF.md and go"), RestartBusy, "BUSY staffy:builder")
		if len(r.typed) != 0 {
			t.Errorf("%s: typed %q into a busy tab", name, r.typed)
		}
	}
}

// An idle tmux tab gets /clear, then, once it is idle again, the brief, each through crius-tsend.
func TestRestartOfAnIdleTmuxTabClearsThenSendsTheBrief(t *testing.T) {
	r, home := newRestartFake(t)
	r.screen["staffy:builder"] = readyTab
	wantOut(t, r, Restart(r.deps(home), "staffy:builder", "Read BRIEF.md and go"), RestartReady, "SENT staffy:builder")
	if want := []string{"staffy:builder /clear", "staffy:builder Read BRIEF.md and go"}; strings.Join(r.typed, "|") != strings.Join(want, "|") {
		t.Errorf("typed %q, want %q", r.typed, want)
	}
	// with no brief it only says the tab is ready
	r, home = newRestartFake(t)
	r.screen["staffy:builder"] = readyTab
	wantOut(t, r, Restart(r.deps(home), "staffy:builder", ""), RestartReady, "READY staffy:builder")
	if len(r.typed) != 0 {
		t.Errorf("typed %q with no brief", r.typed)
	}
}

// A tab whose only background job is its bus waiter (a monitor) is idle for a restart: the restart clears it and sends
// the brief (11-17 20:08: exit 3 on staffy:qa, idle at its prompt with "1 monitor", which every idle session shows).
func TestRestartOfATabWithOnlyItsBusWaiterSendsTheBrief(t *testing.T) {
	r, home := newRestartFake(t)
	r.screen["staffy:builder"] = waiterTab
	wantOut(t, r, Restart(r.deps(home), "staffy:builder", "Read BRIEF.md and go"), RestartReady, "SENT staffy:builder")
	if want := []string{"staffy:builder /clear", "staffy:builder Read BRIEF.md and go"}; strings.Join(r.typed, "|") != strings.Join(want, "|") {
		t.Errorf("typed %q, want %q", r.typed, want)
	}
}

// A /clear that did not go stops the restart before the brief; so does a tab not idle after it.
func TestRestartStopsWhenTheClearFails(t *testing.T) {
	r, home := newRestartFake(t)
	r.screen["staffy:builder"] = readyTab
	r.sendErr["/clear"] = errors.New("crius-tsend: NOT SENT")
	wantOut(t, r, Restart(r.deps(home), "staffy:builder", "Read BRIEF.md and go"), RestartNotSent, "NOT SENT", "/clear")
	if len(r.typed) != 0 {
		t.Errorf("typed %q after a failed /clear", r.typed)
	}
	// crius-tsend MAYBE SENT (or no answer): the text may be in, so never NOT SENT, which invites a resend
	for _, said := range []string{"crius-tsend: exit status 3: MAYBE SENT thinkpad staffy:builder: maybe sent: look at the tab",
		"crius-tsend: no answer in 2m0s: "} {
		r, home = newRestartFake(t)
		r.screen["staffy:builder"] = readyTab
		r.sendErr["Read BRIEF.md and go"] = errors.New(said)
		wantOut(t, r, Restart(r.deps(home), "staffy:builder", "Read BRIEF.md and go"), RestartMaybe, "MAYBE SENT", "never send")
	}
	r, home = newRestartFake(t)
	r.screen["staffy:builder"] = readyTab
	d := r.deps(home)
	send := d.SendTab
	d.SendTab = func(tb RemoteTab, text string) error {
		err := send(tb, text)
		r.screen[tb.Target] = busyTab // something started a turn right after the /clear
		return err
	}
	wantOut(t, r, Restart(d, "staffy:builder", "Read BRIEF.md and go"), RestartBusy, "brief was not sent")
	if len(r.typed) != 1 {
		t.Errorf("typed %q, want only the /clear", r.typed)
	}
}
