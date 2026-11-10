package wakewatch

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/sessionstate"
	"github.com/orzazade/crius/internal/watchdog"
)

// deadAfter: a role whose session has had no live Claude this long wakes its lead and Crius (crius:lead ORDER 11-08
// 01:30: six quotes roles had no Claude since 11-06/07, and QA asks went to empty tabs).
const deadAfter = 15 * time.Minute

// roleSeen is when a run last saw a role's session live, and the session whose death its lead was told of.
type roleSeen struct {
	Live time.Time
	Told string `json:",omitempty"`
}

// deadRoles names each role in home/roles whose current session has had no live Claude (ss) for deadAfter, once per
// session: typed into Crius's tab, and posted as an ALERT to the project's leads on its bus log (a dead lead tells Crius
// alone; so does one whose leads projects.toml cannot name). Only a death seen happen counts: the role was live on a run, or started after the last run. A role that was
// already dead when the check began stays quiet, as do roles closed on purpose (crius-spawn -close) and roles of a
// parked or unwatched team. Crius's line stays typed while the role stays dead (Run never ages a dead: key out). With no session
// list it cannot look: readHooks says so, and every role keeps what it last had.
func deadRoles(d Deps, st *state, ss []Session, sesErr error, watch projects, park parked) [][2]string {
	if sesErr != nil {
		return nil
	}
	dir := filepath.Join(d.Home, "roles")
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return [][2]string{{"roles-failed", "the dead-role check could not list " + dir + ": " + err.Error()}}
	}
	live := map[string]bool{}
	for _, s := range ss {
		live[s.ID] = true
	}
	var leads map[string]bool // read once a dead role needs its leads
	var leadErr error
	var out [][2]string
	var failed []string
	seen := map[string]bool{}
	for _, f := range files {
		project, role, _ := strings.Cut(strings.TrimSuffix(filepath.Base(f), ".json"), ".")
		k := project + "." + role
		seen[k] = true // a file that does not read for one run keeps what it had (review P3-2)
		// a parked or unwatched team wakes no one, as in every other check; it starts afresh once back (review P2-2)
		if !watch.has(project) || park.has(project+":"+role, project) {
			delete(st.Roles, k)
			continue
		}
		r, err := sessionstate.ReadRole(dir, project, role)
		if err != nil {
			failed = append(failed, err.Error())
			continue
		}
		if live[r.SessionID] {
			st.Roles[k] = roleSeen{Live: d.Now}
			continue
		}
		if closed, err := sessionstate.ClosedRole(dir, project, role); err != nil {
			failed = append(failed, err.Error())
			continue
		} else if closed == r.SessionID {
			delete(st.Roles, k)
			continue
		}
		e, ok := st.Roles[k]
		if !ok { // started and ended between two runs; on the first run nothing is known to have died
			if st.RolesAt.IsZero() || !r.Time.After(st.RolesAt) {
				continue
			}
			e = roleSeen{Live: r.Time}
		}
		if d.Now.Sub(e.Live) < deadAfter {
			st.Roles[k] = e
			continue
		}
		since := e.Live.Local().Format("15:04")
		what := fmt.Sprintf("%s · %s (session %s) has had no live Claude since %s: crius-spawn -resume %s %s, or crius-spawn -close %s %s if it ended on purpose",
			project, role, r.SessionID, since, project, role, project, role)
		if leads == nil {
			_, _, _, leads, leadErr = finishedLeads(d.Home)
		}
		var told []string
		if leadErr == nil && !leads[project+":"+role] { // with no projects.toml no lead is known: Crius alone (review P3-3)
			to := leadsOf(leads, project)
			if e.Told != r.SessionID {
				if ok, err := tellLeads(d, project, to, what); err != nil {
					failed = append(failed, err.Error())
				} else if ok {
					e.Told = r.SessionID
				}
			}
			if e.Told == r.SessionID {
				told = to
			}
		}
		if len(told) > 0 {
			what += ". Told " + strings.Join(told, ", ")
		}
		st.Roles[k] = e
		out = append(out, [2]string{"dead:" + k + ":" + r.SessionID, what})
	}
	for k := range st.Roles {
		if !seen[k] {
			delete(st.Roles, k)
		}
	}
	if leadErr != nil {
		failed = append(failed, "projects.toml: "+leadErr.Error())
	}
	if failed != nil {
		out = append(out, [2]string{"roles-failed", "the dead-role check failed to look: " + strings.Join(failed, "; ")})
		return out
	}
	st.RolesAt = d.Now
	return out
}

// leadsOf gives a project's leads as projects.toml names them (leads=), sorted; <project>:lead when it names none.
func leadsOf(leads map[string]bool, project string) []string {
	var out []string
	for _, l := range slices.Sorted(maps.Keys(leads)) {
		if strings.HasPrefix(l, project+":") {
			out = append(out, l)
		}
	}
	if out == nil {
		out = []string{project + ":lead"}
	}
	return out
}

// tellLeads posts what to the leads on the project's bus log, where a lead's waiter wakes on it, and says whether it
// did. A project with no bus log has no lead waiting on one: Crius alone is told.
func tellLeads(d Deps, project string, to []string, what string) (bool, error) {
	if _, err := os.Stat(filepath.Join(d.Home, "bus", project+".log")); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return true, watchdog.PostOn(d.Home, project, "ALERT", "crius:wakewatch", strings.Join(to, ", "), d.Now, what)
}

// closedOnPurpose says whether the role "project:role" names has its current session closed with crius-spawn -close.
// A name that is no role, or a role with no file, is not closed: the idle check goes on as before.
// Known gap: a closed session given new work in its own tab (cmux send) stays out of the idle check until -resume or a
// new session of the role clears the mark.
func closedOnPurpose(home, who string) bool {
	project, role, ok := strings.Cut(strings.ToLower(who), ":")
	if !ok {
		return false
	}
	dir := filepath.Join(home, "roles")
	r, err := sessionstate.ReadRole(dir, project, role)
	if err != nil {
		return false
	}
	closed, err := sessionstate.ClosedRole(dir, project, role)
	return err == nil && closed != "" && closed == r.SessionID
}
