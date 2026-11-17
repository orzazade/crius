package wakewatch

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"

	"github.com/orzazade/crius/internal/sessionstate"
)

// Exit codes of crius-wakewatch restart.
const (
	RestartReady   = 0 // T3: idle and no claude runs for the role, send the brief now; tmux: sent, or idle when no brief was given
	RestartFailed  = 2 // a look failed or the role is unknown: nothing is known, nothing was sent
	RestartBusy    = 3 // a turn, a background job, a dialog or a request is open: wait for it, then run again
	RestartLive    = 4 // T3: claude processes still run for the role: stop them, then run again
	RestartNotSent = 5 // tmux: crius-tsend said NOT SENT for /clear or the brief
	RestartMaybe   = 6 // tmux: /clear or the brief may have gone (crius-tsend MAYBE SENT, or no answer): look, never send again
)

// RestartDeps is what Restart reads and does. Tabs lists the remote tmux tabs (provider-tabs.txt), whose session:window
// is the role's bus name; Capture reads one with dim text taken out; SendTab types one line with crius-tsend.
type RestartDeps struct {
	Home     string
	Out      io.Writer
	Sessions func() ([]Session, error)
	States   func() ([]sessionstate.State, error)
	Thread   func(id string) (T3Thread, error)
	Tabs     func() ([]RemoteTab, error)
	Capture  func(RemoteTab) (string, error)
	SendTab  func(t RemoteTab, text string) error
}

// Restart is the one safe fresh restart of a role (11-17, three times: a fresh send or /clear given while a turn ran was
// queued behind it; the old claude ran on beside the new one, or the brief ran in the old context). It never stops a
// process itself. It refuses while the role has a turn open, and says ready only once nothing runs:
//
//   - a T3 thread: refuse while a run or a request is open on any thread the role's claudes run in; then list every live
//     claude of the role (its claude status file, by name or crius-hook's record) and say ready only when none is left.
//     T3 keeps a thread's claude running between turns, and its stop does nothing on an idle thread (no_active_run), so
//     a claude left is stopped by its pid. The t3 CLI cannot send with a fresh context either: Restart prints the exact
//     MCP call for the send, and is run again after each step.
//   - a tmux tab on another box: refuse unless the tab is idle (no working line, no background job, no dialog); with a
//     brief, type /clear, check the tab is idle again, then type the brief. /clear keeps the same claude process.
func Restart(d RestartDeps, role, brief string) int {
	say := func(code int, format string, a ...any) int {
		fmt.Fprintf(d.Out, format+"\n", a...)
		return code
	}
	role = strings.ToLower(strings.TrimSpace(role))
	project, name, ok := strings.Cut(role, ":")
	if !ok || project == "" || name == "" {
		return say(RestartFailed, "FAILED: give the role as <project>:<role> (axi:ms-flow), not %q", role)
	}
	tabs, err := d.Tabs()
	if err != nil {
		return say(RestartFailed, "FAILED %s: could not read the remote tab list: %v", role, err)
	}
	for _, t := range tabs {
		if strings.EqualFold(t.Target, role) {
			return restartTab(d, say, role, t, brief)
		}
	}
	ss, err := d.Sessions()
	if err != nil {
		return say(RestartFailed, "FAILED %s: could not list the live claude sessions: %v", role, err)
	}
	states, err := d.States()
	if err != nil {
		return say(RestartFailed, "FAILED %s: could not read crius-hook's session records: %v", role, err)
	}
	live := byRole(ss, states)[role]
	var threads []string // the threads the role's live claudes run in, each once
	for _, s := range live {
		for _, st := range states {
			if st.SessionID == s.ID && st.Thread != "" && !slices.Contains(threads, st.Thread) {
				threads = append(threads, st.Thread)
			}
		}
	}
	if len(threads) == 0 {
		r, err := sessionstate.ReadRole(filepath.Join(d.Home, "roles"), project, name)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return say(RestartFailed, "FAILED %s: could not read its role file: %v", role, err)
		}
		if err == nil {
			if th := newestThread(d.Home, project, name, r, states); th != "" {
				threads = append(threads, th)
			}
		}
	}
	if len(threads) == 0 {
		return say(RestartFailed, "FAILED %s: no T3 thread or remote tab known for it (no live claude names one, its role file "+
			"names none, and provider-tabs.txt lists no such tab)", role)
	}
	for _, thread := range threads {
		th, err := d.Thread(thread)
		if err != nil {
			return say(RestartFailed, "FAILED %s: could not read T3 thread %s, so it is not known to be idle: %v", role, thread, err)
		}
		if th.Active || th.Status == "running" {
			return say(RestartBusy, "BUSY %s: T3 thread %s has a turn running (status %s). A fresh send now is queued behind it "+
				"and leaves the old claude running. Wait for the turn to end, then run crius-wakewatch restart %s again.", role, thread, th.Status, role)
		}
		if th.Pending > 0 {
			return say(RestartBusy, "BUSY %s: T3 thread %s has %d request(s) waiting (an approval or a question). Answer or "+
				"decline them, then run crius-wakewatch restart %s again.", role, thread, th.Pending, role)
		}
	}
	thread := threads[0]
	if len(live) > 0 {
		return say(RestartLive, "LIVE %s: %s still run, while T3 thread %s is idle (t3_thread_interrupt has no turn to stop). "+
			"Next: stop each by its pid (kill <pid>; never pkill -f), then run crius-wakewatch restart %s again.",
			role, describe(live, states), strings.Join(threads, ", "), role)
	}
	msg := "<the brief>"
	if brief != "" {
		msg = brief
	}
	return say(RestartReady, "READY %s: T3 thread %s is idle and no claude runs for the role. Next: t3_thread_send {threadId: %q, "+
		"freshContext: true, message: %q}. Then run crius-wakewatch restart %s once more: it must list exactly one claude.",
		role, thread, thread, msg, role)
}

// restartTab restarts a role in a remote tmux tab: refused unless idle, then /clear, idle again, then the brief.
func restartTab(d RestartDeps, say func(int, string, ...any) int, role string, t RemoteTab, brief string) int {
	idle := func() (string, error) {
		text, err := d.Capture(t)
		if err != nil {
			return "", err
		}
		return screenView(text), nil
	}
	v, err := idle()
	if err != nil {
		return say(RestartFailed, "FAILED %s: could not read tab %s, so it is not known to be idle: %v", role, t, err)
	}
	if v != "idle" {
		return say(RestartBusy, "BUSY %s: tab %s %s. /clear and a brief sent now are queued behind it, and the brief runs in "+
			"the old context. Wait, then run crius-wakewatch restart %s again.", role, t, busyWhy(v), role)
	}
	if brief == "" {
		return say(RestartReady, "READY %s: tab %s is idle. Next: crius-wakewatch restart %s <brief…> types /clear, then the brief.", role, t, role)
	}
	if err := d.SendTab(t, "/clear"); err != nil {
		if code := sendCode(err); code == RestartMaybe {
			return say(code, "MAYBE SENT %s: /clear to %s may have gone (%v); the brief was not sent. Look at the tab; do not "+
				"run this again until it shows an idle, empty box.", role, t, err)
		}
		return say(RestartNotSent, "NOT SENT %s: /clear to %s did not go (%v); the brief was not sent.", role, t, err)
	}
	if v, err := idle(); err != nil {
		return say(RestartFailed, "FAILED %s: the /clear went, but tab %s could not be read after it (%v); the brief was not sent.", role, t, err)
	} else if v != "idle" {
		return say(RestartBusy, "BUSY %s: the /clear went, but tab %s %s after it; the brief was not sent. Send it with "+
			"crius-tsend %s %s once the tab is idle.", role, t, busyWhy(v), t.Box, t.Target)
	}
	if err := d.SendTab(t, brief); err != nil {
		if code := sendCode(err); code == RestartMaybe {
			return say(code, "MAYBE SENT %s: the /clear went, the brief to %s may have gone (%v). Look at the tab; never send "+
				"it again unseen.", role, t, err)
		}
		return say(RestartNotSent, "NOT SENT %s: the /clear went, the brief to %s did not (%v).", role, t, err)
	}
	return say(RestartReady, "SENT %s: /clear, then the brief, to %s.", role, t)
}

// sendCode reads a failed crius-tsend: NOT SENT (its exits 1 and 2, whose message says so) is RestartNotSent; anything
// else, its MAYBE SENT (exit 3) or no answer in time, may have typed the text, so it is RestartMaybe.
func sendCode(err error) int {
	if strings.Contains(err.Error(), "NOT SENT") {
		return RestartNotSent
	}
	return RestartMaybe
}

// busyWhy says what a screen view other than idle means for a restart.
func busyWhy(view string) string {
	if view == "asks" {
		return "shows a dialog or a question"
	}
	return "is working or runs background jobs"
}
