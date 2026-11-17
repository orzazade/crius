package wakewatch

import (
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/orzazade/crius/internal/sessionstate"
)

// twinsAfter: a role must show more than one live claude on this many runs in a row before Crius hears of it. A
// hand-over (crius-spawn -move, a restart) can show the old claude for a moment beside the new one.
const twinsAfter = lookMisses

// roleOf gives the bus name ("axi:ms-flow") a live session works as: its own name now (BusName, from its claude's status
// file), else crius-hook's filing of its session id (seldom another answer: crius-hook files by that same name on each
// event); "" for none. crius-hook
// files a session whose title names no role as "unknown": that is no role. The name comes first: crius-hook's record is
// as of its last event, and it can file a child claude run in a tab under the tab's role.
func roleOf(s Session, filed map[string]sessionstate.State) string {
	if r := BusName(s.Name); r != "" {
		return r
	}
	if st, ok := filed[s.ID]; ok && st.Project != "" && st.Role != "" && st.Role != "unknown" {
		return strings.ToLower(st.Project + ":" + st.Role)
	}
	return ""
}

// byRole groups live sessions by roleOf, each role's sorted by pid; sessions of no role or no pid are left out.
func byRole(ss []Session, states []sessionstate.State) map[string][]Session {
	filed := map[string]sessionstate.State{}
	for _, st := range states {
		filed[st.SessionID] = st
	}
	out := map[string][]Session{}
	for _, s := range ss {
		if r := roleOf(s, filed); r != "" && s.PID > 0 {
			out[r] = append(out[r], s)
		}
	}
	for _, list := range out {
		slices.SortFunc(list, func(a, b Session) int { return a.PID - b.PID })
	}
	return out
}

// describe names each session: "claude pid 73035 (session 05473ed1, T3 thread mcp:…)".
func describe(list []Session, states []sessionstate.State) string {
	thread := map[string]string{}
	for _, st := range states {
		thread[st.SessionID] = st.Thread
	}
	var parts []string
	for _, s := range list {
		p := "claude pid " + strconv.Itoa(s.PID) + " (session " + s.ID[:min(8, len(s.ID))]
		if th := thread[s.ID]; th != "" {
			p += ", T3 thread " + th
		}
		parts = append(parts, p+")")
	}
	return strings.Join(parts, ", ")
}

// twins wakes Crius when one role has more than one live claude process (11-17, three times: a fresh T3 send during a
// turn left the old claude running; a bus delivery made T3 resume a stored session beside the live one). Both post on
// the bus and run the same work. The key names the role and its pids, so a lasting pair is said once (Run keeps it in
// Typed while it lasts) and a new pair is said again. Sessions that could not be read are readHooks' "sessions-failed";
// the counts then wait for a good look. crius-hook's records that could not be read are its "states-failed"; the
// sessions' own names still give their roles.
func twins(d Deps, st *state, ss []Session, sesErr error) [][2]string {
	if sesErr != nil {
		return nil
	}
	states, _ := d.States()
	seen := newMisses(st.Twins) // runs in a row each pair was seen
	var out [][2]string
	by := byRole(ss, states)
	for _, role := range slices.Sorted(maps.Keys(by)) {
		list := by[role]
		if len(list) < 2 {
			continue
		}
		var pids []string
		for _, s := range list {
			pids = append(pids, strconv.Itoa(s.PID))
		}
		if n, _ := seen.fail("twins:" + role + ":" + strings.Join(pids, ",")); n < twinsAfter {
			continue
		}
		out = append(out, [2]string{"twins:" + role + ":" + strings.Join(pids, ","), strconv.Itoa(len(list)) +
			" live sessions for one role, " + role + ": " + describe(list, states) +
			". Both may post on the bus and run the same work. Keep one: crius-wakewatch restart " + role +
			" says what to stop; stop a claude by its pid, never pkill -f"})
	}
	st.Twins = seen.counts()
	return out
}
