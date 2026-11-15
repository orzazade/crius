package wakewatch

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/orzazade/crius/internal/sessionstate"
)

// CriusThreadFile is the file, under the Crius home, that holds the id of the T3 Code thread Crius runs in (one line,
// e.g. mcp:5bba4b79-…). While it exists Crius has no cmux tab: outbox sends its messages with `t3 thread send`, and the
// checks of a cmux tab titled crius are left out.
func CriusThreadFile(home string) string { return filepath.Join(home, "state", "crius", "t3-thread") }

// CriusThread gives the id in CriusThreadFile; "" with no error when there is no such file. A file that cannot be read,
// or holds no single id (empty, two words, two lines, or a word starting with "-" that t3 would read as a flag), is an
// error: the file says Crius has moved, so its messages must not go to a cmux tab.
func CriusThread(home string) (string, error) {
	path := CriusThreadFile(home)
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	f := strings.Fields(string(b))
	if len(f) != 1 || strings.HasPrefix(f[0], "-") {
		return "", fmt.Errorf("%s holds %q, not one T3 Code thread id", path, strings.TrimSpace(string(b)))
	}
	return f[0], nil
}

// T3Thread is what `t3 thread show --json` tells of a thread: its status ("running", "completed", …), whether a run is
// open (activeRunId), when it last changed, and its newest assistant message ("" when the messages read hold none).
type T3Thread struct {
	Status    string
	Active    bool
	Updated   time.Time
	LastReply string
}

const (
	t3IdleAfter = 10 * time.Minute // a thread whose last reply ends on neither ⏳ nor ❓
	t3WaitAfter = 45 * time.Minute // a thread whose last reply ends on ⏳ and whose role posted no bus line since
	t3Tail      = 80               // how much of the last reply the alert quotes, in characters
)

// roleThread gives the T3 Code thread the role's current session runs in: from that session's own file, which crius-hook
// rewrites on every event, else from the role's file (as bus-post reads it); "" when it runs in none.
func roleThread(home string, r sessionstate.State) string {
	if cur, err := sessionstate.Read(filepath.Join(home, "sessions"), r.SessionID); err == nil && cur.Thread != "" {
		return cur.Thread
	}
	return r.Thread
}

// t3Idle wakes Crius for each team member that runs as a T3 Code thread and has stopped while it still owes work
// (11-15: AXI api-split, ms-flow and qa sat idle 13 to 50 min, found by hand at 04:05 and 05:48; nothing watched a
// thread). For each open role of a watched, unparked team whose current session runs in a thread other than Crius's own,
// it reads the thread: one that is not running is idle since its last update. Its last reply's newest status emoji
// decides (RULES §3.5): ❓ waits on the owner and stays quiet; ⏳ names its own running job and wakes Crius only after
// t3WaitAfter with no bus line from the role in that time; anything else wakes Crius after t3IdleAfter. The key holds
// the thread's update time, and Run says it once (Typed): one wake per idle spell. A role it cannot read, or a thread t3
// cannot show, is a loud "failed to look" alert, never a quiet skip.
func t3Idle(d Deps, watch projects, park parked) [][2]string {
	if d.Thread == nil {
		return nil
	}
	dir := filepath.Join(d.Home, "roles")
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return [][2]string{{"t3-failed", "the T3 thread idle check failed to look: " + err.Error()}}
	}
	var failed []string
	crius, err := CriusThread(d.Home)
	if err != nil {
		failed = append(failed, err.Error())
	}
	var out [][2]string
	for _, f := range files {
		project, role, _ := strings.Cut(strings.TrimSuffix(filepath.Base(f), ".json"), ".")
		who := project + ":" + role
		if !watch.has(project) || park.has(who, project) {
			continue
		}
		r, err := sessionstate.ReadRole(dir, project, role)
		if err != nil {
			failed = append(failed, err.Error())
			continue
		}
		id := roleThread(d.Home, r)
		if id == "" || id == crius {
			continue
		}
		name := fmt.Sprintf("%s · %s (T3 thread %s)", project, role, id)
		if closed, err := sessionstate.ClosedRole(dir, project, role); err != nil {
			failed = append(failed, err.Error())
			continue
		} else if closed == r.SessionID {
			continue
		}
		p, unparked, err := parkedOnBus(d.Home, who, d.Now)
		if err != nil {
			failed = append(failed, name+": its bus: "+err.Error())
			continue
		} else if p {
			continue
		}
		th, err := d.Thread(id)
		if err != nil {
			failed = append(failed, name+": "+err.Error())
			continue
		}
		if th.Status == "running" || th.Active {
			continue
		}
		from := th.Updated
		if unparked.After(from) { // an unpark after its last turn starts the idle spell afresh
			from = unparked
		}
		idle := d.Now.Sub(from)
		reply := []rune(strings.Join(strings.Fields(th.LastReply), " "))
		tail := string(reply[max(0, len(reply)-t3Tail):])
		key := "t3idle:" + who + ":" + id + ":" + from.UTC().Format(time.RFC3339Nano)
		switch statusMark(th.LastReply) {
		case "❓":
			continue
		case "⏳":
			if idle <= t3WaitAfter {
				continue
			}
			last, _, err := lastFrom(d.Home, who, "", d.Now)
			if err != nil {
				failed = append(failed, name+": its bus: "+err.Error())
				continue
			}
			if !last.Before(from) {
				continue
			}
			out = append(out, [2]string{key, fmt.Sprintf("%s is idle %d min after a ⏳ reply, with no bus line from it since: its job may be gone. Last reply: %s",
				name, int(idle.Minutes()), tail)})
		default:
			if idle <= t3IdleAfter {
				continue
			}
			out = append(out, [2]string{key, fmt.Sprintf("%s is idle %d min: not running, and its last reply ends on neither ⏳ nor ❓. Last reply: %s",
				name, int(idle.Minutes()), tail)})
		}
	}
	if failed != nil {
		out = append(out, [2]string{"t3-failed", "the T3 thread idle check failed to look: " + strings.Join(failed, "; ")})
	}
	return out
}

// statusMark gives the newest of ❓, ✅ and ⏳ in text, "" when it has none.
func statusMark(text string) string {
	j := strings.LastIndexAny(text, "❓✅⏳")
	if j < 0 {
		return ""
	}
	r, _ := utf8.DecodeRuneInString(text[j:])
	return string(r)
}
