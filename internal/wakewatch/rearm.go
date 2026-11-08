package wakewatch

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/sessionstate"
	"github.com/orzazade/crius/internal/watchdog"
)

// rearmAfter: a role's session with no team-wait waiter this long, its tab idle, gets its re-arm line (crius:lead DECISION
// 11-08: quotes qa-backend-2 sat idle 02:34-05:26 after its waiter died, and nothing noticed).
const rearmAfter = 10 * time.Minute

// roleWait is what rearm knows of a role's session: the command of the last team-wait.sh seen under it, since when
// none runs, and whether that spell's re-arm line went out.
type roleWait struct {
	Session string
	Cmd     string    `json:",omitempty"`
	Since   time.Time `json:",omitzero"`
	Said    bool      `json:",omitempty"`
}

// rearm types a re-arm line into the tab of each live, not closed role of a watched, not parked team whose session had a
// team-wait.sh waiter and has had none for rearmAfter while its tab is idle (crius-hook's state), once per spell, and
// posts an INFO naming the role to its leads (to Crius when the project has no bus log). A waiter belongs to the session
// whose Claude it runs under. A session never seen with a waiter is left alone: crius:build and crius:ops take typed
// orders, not a waiter. What it cannot look at is said to Crius, never read as "no waiter".
func rearm(d Deps, st *state, ss []Session, sesErr error, screens []Screen, scrErr error, watch projects, park parked) [][2]string {
	if sesErr != nil { // readHooks says so
		return nil
	}
	procs, err := d.Procs()
	if err != nil {
		return [][2]string{{"rearm-failed", "the waiter re-arm check failed to look: " + err.Error()}}
	}
	waiting := waitersBySession(procs, ss)
	dir := filepath.Join(d.Home, "roles")
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return [][2]string{{"rearm-failed", "the waiter re-arm check could not list " + dir + ": " + err.Error()}}
	}
	live := map[string]bool{}
	for _, s := range ss {
		live[s.ID] = true
	}
	var states []sessionstate.State
	var stErr error
	read := false
	var leads map[string]bool
	var leadErr error
	var failed []string
	seen := map[string]bool{}
	for _, f := range files {
		project, role, _ := strings.Cut(strings.TrimSuffix(filepath.Base(f), ".json"), ".")
		k := project + "." + role
		if !watch.has(project) || park.has(project+":"+role, project) {
			continue
		}
		r, err := sessionstate.ReadRole(dir, project, role)
		if err != nil {
			failed = append(failed, err.Error())
			seen[k] = true // keeps what it had
			continue
		}
		if !live[r.SessionID] { // deadRoles says so; what was seen of its waiter stays for a resume (review P2-1)
			seen[k] = true
			continue
		}
		if closed, err := sessionstate.ClosedRole(dir, project, role); err != nil {
			failed = append(failed, err.Error())
			seen[k] = true
			continue
		} else if closed == r.SessionID {
			continue
		}
		seen[k] = true
		w := st.Waits[k]
		if w.Session != r.SessionID {
			w = roleWait{Session: r.SessionID}
		}
		if cmd, ok := waiting[r.SessionID]; ok {
			st.Waits[k] = roleWait{Session: r.SessionID, Cmd: cmd}
			continue
		}
		if w.Cmd != "" && w.Since.IsZero() {
			w.Since = d.Now
		}
		st.Waits[k] = w
		if w.Cmd == "" || w.Said || d.Now.Sub(w.Since) < rearmAfter {
			continue
		}
		if !read {
			states, stErr = d.States()
			read = true
		}
		if stErr != nil {
			failed = append(failed, "crius-hook states: "+stErr.Error())
			continue
		}
		if i := slices.IndexFunc(states, func(s sessionstate.State) bool { return s.SessionID == r.SessionID }); i < 0 || states[i].State != sessionstate.Idle {
			continue // busy, or a dialog open: the line waits for the tab to be idle
		}
		if scrErr != nil {
			failed = append(failed, "cannot read the tabs to type into "+k+": "+scrErr.Error())
			continue
		}
		i := slices.IndexFunc(screens, func(s Screen) bool { return r.Surface != "" && s.ID == r.Surface })
		if i < 0 {
			failed = append(failed, fmt.Sprintf("no tab of surface %q for %s", r.Surface, k))
			continue
		}
		since := w.Since.Local().Format("15:04")
		text := Prefix + Clean(fmt.Sprintf("no team-wait waiter has run for %s · %s since %s and this tab is idle. Re-arm it in the background, then go on with your next step: %s",
			project, role, since, w.Cmd))
		if err := d.SendTab(screens[i].Title, text); err != nil && (d.HandedOver == nil || !d.HandedOver(err)) {
			failed = append(failed, fmt.Sprintf("re-arm line for %s not typed: %v", k, err))
			continue
		}
		w.Said = true
		st.Waits[k] = w
		if leads == nil && leadErr == nil {
			_, _, _, leads, leadErr = finishedLeads(d.Home)
		}
		to := []string{project + ":lead"}
		if leadErr == nil {
			to = leadsOf(leads, project)
		}
		info := fmt.Sprintf("%s:%s had no team-wait waiter since %s (%d+ min, tab idle): sent the re-arm line to its tab",
			project, role, since, int(rearmAfter.Minutes()))
		if err := postInfo(d, project, to, info); err != nil {
			failed = append(failed, err.Error())
		}
	}
	for k := range st.Waits {
		if !seen[k] {
			delete(st.Waits, k)
		}
	}
	if failed != nil {
		return [][2]string{{"rearm-failed", "the waiter re-arm check failed to look: " + strings.Join(failed, "; ")}}
	}
	return nil
}

// postInfo posts an INFO from wakewatch to the leads on the project's bus log; with no bus log, to Crius on crius.log.
func postInfo(d Deps, project string, to []string, text string) error {
	if _, err := os.Stat(filepath.Join(d.Home, "bus", project+".log")); errors.Is(err, fs.ErrNotExist) {
		return watchdog.PostOn(d.Home, "crius", "INFO", "crius:wakewatch", "crius:lead", d.Now, text)
	} else if err != nil {
		return err
	}
	return watchdog.PostOn(d.Home, project, "INFO", "crius:wakewatch", strings.Join(to, ", "), d.Now, text)
}

// waitersBySession gives, for each session, the command of a team-wait.sh process running under its Claude (pid). Only a
// process that runs the script counts: its first word, or the second after an interpreter; the shell that started it
// shows the same words further on.
func waitersBySession(procs []Proc, ss []Session) map[string]string {
	session := map[int]string{}
	for _, s := range ss {
		if s.PID > 0 {
			session[s.PID] = s.ID
		}
	}
	parent := map[int]int{}
	for _, p := range procs {
		parent[p.PID] = p.PPID
	}
	out := map[string]string{}
	for _, p := range procs {
		w := strings.Fields(p.Command)
		if at := slices.IndexFunc(w, func(s string) bool { return filepath.Base(s) == "team-wait.sh" }); at < 0 || at > 1 {
			continue
		}
		for pid, n := p.PPID, 0; pid > 1 && n < 64; pid, n = parent[pid], n+1 {
			if id, ok := session[pid]; ok {
				out[id] = p.Command
				break
			}
		}
	}
	return out
}
