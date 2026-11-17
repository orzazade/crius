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
// open (activeRunId), how many requests (an approval or a question) wait on someone, when it last changed, its newest
// assistant message ("" when the messages read hold none), and whether a user message came after that reply.
type T3Thread struct {
	Status     string
	Active     bool
	Pending    int
	Updated    time.Time
	LastReply  string
	Unanswered bool
}

// ErrT3NoAnswer marks a t3 call that timed out: the T3 server hangs, so the other threads are not read on this run.
var ErrT3NoAnswer = errors.New("t3 gave no answer in time")

const (
	t3IdleAfter = 10 * time.Minute // a thread whose last reply ends on neither ⏳ nor ❓, or that waits on a request
	t3WaitAfter = 45 * time.Minute // a thread whose last reply ends on ⏳, with no bus line from its role in that time
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
// thread). It looks at each open role of a watched team whose current session runs in a thread other than Crius's own,
// and leaves out, as the tab check does, a parked role, a lead whose goal is finished, and a role whose WAIT on the bus
// still runs. A thread with no open run is idle since its last update (or since the line that unparked it or ended its
// WAIT, when later). It wakes Crius when:
//   - a request (an approval or a question) has waited on someone for over t3IdleAfter;
//   - a message to it after its last reply has no answer, after t3IdleAfter;
//   - its last reply's newest status emoji (RULES §3.5) is ⏳, its own named job, and neither the thread nor a bus line
//     from the role has moved for over t3WaitAfter;
//   - it is anything but ❓ (which waits on the owner), after t3IdleAfter.
//
// The key holds the idle spell's start, and Run says it once (Typed): one wake per idle spell. A role that cannot be
// read, or a thread t3 cannot show, is a loud "failed to look" alert of its own, said every 20 min while it lasts.
// A thread in prompted waits on a permission prompt stuckPrompts watches: its open requests are left to that check.
func t3Idle(d Deps, watch projects, park parked, prompted map[string]bool) [][2]string {
	if d.Thread == nil {
		return nil
	}
	dir := filepath.Join(d.Home, "roles")
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return [][2]string{{"t3-failed", "the T3 thread idle check failed to look: " + err.Error()}}
	}
	var out [][2]string
	crius, err := CriusThread(d.Home)
	if err != nil {
		out = append(out, [2]string{"t3-failed", "the T3 thread idle check failed to look: " + err.Error()})
	}
	var done map[string]bool // read once a thread is idle
	hung := false
	for _, f := range files {
		project, role, _ := strings.Cut(strings.TrimSuffix(filepath.Base(f), ".json"), ".")
		who := project + ":" + role
		if !watch.has(project) || park.has(who, project) {
			continue
		}
		failed := func(why string) {
			out = append(out, [2]string{"t3-failed:" + who, fmt.Sprintf("the T3 thread idle check failed to look at %s · %s: %s", project, role, why)})
		}
		r, err := sessionstate.ReadRole(dir, project, role)
		if err != nil {
			failed(err.Error())
			continue
		}
		id := roleThread(d.Home, r)
		if id == "" || id == crius {
			continue
		}
		name := fmt.Sprintf("%s · %s (T3 thread %s)", project, role, id)
		if closed, err := sessionstate.ClosedRole(dir, project, role); err != nil {
			failed(err.Error())
			continue
		} else if closed == r.SessionID {
			continue
		}
		p, unparked, err := parkedOnBus(d.Home, who, d.Now)
		if err != nil {
			failed("its bus: " + err.Error())
			continue
		} else if p {
			continue
		}
		w, err := waitOnBus(d.Home, who, d.Now)
		if err != nil {
			failed("its bus: " + err.Error())
			continue
		} else if w.silent {
			continue
		}
		if hung {
			failed("not read: t3 gave no answer for an earlier thread on this run")
			continue
		}
		th, err := d.Thread(id)
		if err != nil {
			hung = errors.Is(err, ErrT3NoAnswer)
			failed(fmt.Sprintf("thread %s: %v (if the role ended on purpose: crius-spawn -close %s %s)", id, err, project, role))
			continue
		}
		if (th.Status == "running" || th.Active) && th.Pending == 0 || th.Pending > 0 && prompted[id] {
			continue
		}
		from := th.Updated
		for _, t := range []time.Time{unparked, w.from} { // a line that unparked it or ended its WAIT starts the spell afresh
			if t.After(from) {
				from = t
			}
		}
		if d.Now.Sub(from) <= t3IdleAfter {
			continue
		}
		if done == nil {
			done, _, _, _, _ = finishedLeads(d.Home) // with no projects.toml no lead is known to be finished: it is checked
		}
		if done[strings.ToLower(who)] {
			continue
		}
		reply := []rune(strings.Join(strings.Fields(th.LastReply), " "))
		tail := "Last reply: " + string(reply[max(0, len(reply)-t3Tail):])
		if len(reply) == 0 {
			tail = "No reply in its latest messages."
		}
		key := "t3idle:" + who + ":" + id + ":" + from.UTC().Format(time.RFC3339Nano)
		idle := int(d.Now.Sub(from).Minutes())
		switch mark := statusMark(th.LastReply); {
		case th.Pending > 0:
			out = append(out, [2]string{key, fmt.Sprintf("%s waits on %d open request(s), an approval or a question, idle %d min. %s", name, th.Pending, idle, tail)})
		case th.Unanswered:
			out = append(out, [2]string{key, fmt.Sprintf("%s is idle %d min: not running, and a message to it after its last reply has no answer. %s", name, idle, tail)})
		case mark == "❓":
		case mark == "⏳":
			last, _, err := lastFrom(d.Home, who, "", d.Now)
			if err != nil {
				failed("its bus: " + err.Error())
				continue
			}
			moved := from
			if last.After(moved) {
				moved = last
			}
			if quiet := d.Now.Sub(moved); quiet > t3WaitAfter {
				out = append(out, [2]string{key, fmt.Sprintf("%s is idle %d min after a ⏳ reply, with no bus line from it for %d min: its job may be gone. %s",
					name, idle, int(quiet.Minutes()), tail)})
			}
		default:
			out = append(out, [2]string{key, fmt.Sprintf("%s is idle %d min: not running, and its last reply ends on neither ⏳ nor ❓. %s", name, idle, tail)})
		}
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
