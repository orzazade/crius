package wakewatch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/orzazade/crius/internal/sessionstate"
)

// LiveSessions lists the live Claude Code sessions of every config folder (~/.claude*) under the user home: name, id,
// and the birth time of the session's transcript (zero when it has none yet). A linked folder lists a process again; it
// counts once.
func LiveSessions(home string) ([]Session, error) {
	files, err := filepath.Glob(filepath.Join(home, ".claude*", "sessions", "*.json"))
	if err != nil {
		return nil, err
	}
	var out []Session
	seen := map[int]bool{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if os.IsNotExist(err) { // the session ended after the listing
			continue
		}
		if err != nil {
			return nil, err
		}
		var s struct {
			PID       int    `json:"pid"`
			SessionID string `json:"sessionId"`
			Name      string `json:"name"`
			ProcStart string `json:"procStart"`
		}
		if err := json.Unmarshal(b, &s); err != nil {
			return nil, fmt.Errorf("%s: %v", f, err)
		}
		if seen[s.PID] {
			continue
		}
		// A file left by a session that ended, its pid gone or given to a later process.
		if live, err := sessionstate.Live(s.PID, s.ProcStart); err != nil {
			return nil, fmt.Errorf("%s: %v", f, err)
		} else if !live {
			continue
		}
		seen[s.PID] = true
		ses := Session{Name: s.Name, ID: s.SessionID}
		ts, err := filepath.Glob(filepath.Join(filepath.Dir(filepath.Dir(f)), "projects", "*", s.SessionID+".jsonl"))
		if err != nil {
			return nil, err
		}
		for _, t := range ts {
			var st syscall.Stat_t
			if err := syscall.Stat(t, &st); err != nil {
				return nil, err
			}
			ses.Started = time.Unix(st.Birthtimespec.Unix())
		}
		out = append(out, ses)
	}
	return out, nil
}

// BusName gives the bus name of a session name: "quotes · p6 · max2" is quotes:p6, Crius's own tab is crius:lead, and
// a name of no project and role is "".
func BusName(name string) string {
	name = strings.TrimSpace(name)
	if name == CriusTitle {
		return "crius:lead"
	}
	m := sessionRe.FindStringSubmatch(name)
	if m == nil {
		return ""
	}
	return strings.ToLower(m[1] + ":" + m[2])
}
