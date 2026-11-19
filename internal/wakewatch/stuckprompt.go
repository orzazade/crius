package wakewatch

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/sessionstate"
)

// promptAfter is how long a permission prompt may wait on someone before Crius hears of it.
const promptAfter = 10 * time.Minute

// promptRe reads the line ~/.claude/hooks/wait-log.sh writes to waits.log for a permission prompt:
// <stamp> WAIT waits:<slug> -> crius — wait permission_prompt "<title>" cwd=<cwd> sid=<first 8 of the id> at=<HH:MM:SS> — <message>
// The hook writes sid=? when the notification named no session.
var promptRe = regexp.MustCompile(`^(\d{4}-\d\d-\d\dT\d\d:\d\d) WAIT waits:\S+ -> \S+ — wait permission_prompt "(.*?)" cwd=(.*?) sid=(\S+) at=(\d\d:\d\d):(\d\d) — (.*)$`)

// sidRe is a session id prefix that can name a state file.
var sidRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// permPrompt is one permission prompt the hook logged.
type permPrompt struct {
	at                    time.Time
	exact                 bool // at holds the second the hook named (else only its minute is known)
	title, cwd, sid, text string
}

// stuckPrompts wakes Crius once for each permission prompt still unanswered promptAfter after the hook logged it
// (11-17: the Cortex lead, a T3 thread with role "unknown", sat on one 07:02-09:05 and again 09:06-10:28, and AXI's
// first-order sessions on three overnight; their WAIT lines went only to the 09:00/18:00 digest). It reads the newest
// permission_prompt line of each session in waits.log from the last 12 h (window) and decides with crius-hook's state
// file of that session (by the 8-character id the line names):
//   - a session whose state moved on (running, idle, ended) after the prompt: answered;
//   - still "waiting" in a T3 Code thread: T3's own count of open requests decides, since an approved tool that still
//     runs also leaves the hook state on "waiting" (approving fires no hook); none open: answered;
//   - still "waiting" in a cmux tab: left to the prompt watch (prompts.go), which reads the tab's dialog;
//   - still "waiting" in neither (a plain terminal): stuck, by the hook state alone.
//
// While "waiting", the dialog in question is the line's, unless the state shows the open dialog started over a minute
// after it: then it is a later one that logged no line (a subagent's), timed and keyed from its own start (Opened).
//
// A stuck-prompt wake names the call the dialog waits on, read from the session's transcript (waitingCall, pendingcall.go),
// so Crius can tell a lead to redo it another way or ask the owner (11-19: "use Bash" alone named no command).
//
// Crius's own thread is left out; parked teams are not, as a prompt blocks a parked role too. A state that cannot be
// read, or a thread T3 cannot show, is a loud "could not tell" wake of its own (key prompt-unread:, held a run as every
// failed look is: looks.go; prompt-untold: when the hook's record cannot tell, said at once). Run says each key
// once (Typed); the 12 h lookback ends before Run forgets a key (window+resend), so a prompt wakes once. prompted names
// each thread whose newest prompt the hook state still shows open, at any age: t3Idle leaves their open requests to this
// check, so one stuck prompt wakes Crius once.
func stuckPrompts(d Deps) (out [][2]string, prompted map[string]bool) {
	path := filepath.Join(d.Home, "bus", "waits.log")
	lines, err := tailSince(path, d.Now.Add(-window), d.Now)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil // the hook has logged no notification yet
	}
	if err != nil {
		return [][2]string{{"prompts-failed", "the stuck permission prompt check failed to read " + path + ": " + err.Error()}}, nil
	}
	newest := map[string]permPrompt{}
	for _, l := range lines {
		p, ok := parsePrompt(l, d.Now)
		if !ok || p.at.After(d.Now) { // a line stamped ahead of now (a clock set back) is not the newest prompt yet
			continue
		}
		id := p.sid
		if !sidRe.MatchString(id) {
			id += "@" + p.at.Format(time.RFC3339) // a line with no session id is a session of its own
		}
		if old, seen := newest[id]; !seen || !p.at.Before(old.at) {
			newest[id] = p
		}
	}
	crius, err := CriusThread(d.Home)
	if err != nil {
		out = append(out, [2]string{"prompts-failed", "the stuck permission prompt check could not tell Crius's own thread: " + err.Error()})
	}
	prompted = map[string]bool{}
	stop := "" // why no more threads are read on this run
	for _, sid := range slices.Sorted(maps.Keys(newest)) {
		p := newest[sid]
		if d.Now.Sub(p.at) > window {
			continue
		}
		// the dialog in question: the line's, until the state shows a later one open
		asked, text := p.at, p.text
		// how long after asked the waiting call may have started: the dialog opens after its call is written, and the
		// hook's time is to the second (to the minute when it named no second of that minute)
		slack := 2 * time.Second
		if !p.exact {
			slack = time.Minute
		}
		key := func() string { return p.sid + ":" + asked.UTC().Format(time.RFC3339) }
		waited := func() time.Duration { return d.Now.Sub(asked) }
		name, about := fmt.Sprintf("%q", p.title), []string{"cwd " + p.cwd, "session " + p.sid}
		where := func() string { return name + " (" + strings.Join(about, ", ") + ")" }
		// unread says a prompt it could not tell about: kind prompt-unread for a look that may work next run (held a run,
		// looks.go), prompt-untold for a record a second look reads the same (said at once).
		unreadAs := func(kind, why string) {
			if waited() >= promptAfter {
				out = append(out, [2]string{kind + key(), fmt.Sprintf("permission prompt in %s, asked at %s (%d min ago), %q: could not tell whether it was answered: %s. Look at it by hand.",
					where(), asked.Format("15:04"), int(waited().Minutes()), text, why)})
			}
		}
		unread := func(why string) { unreadAs("prompt-unread:", why) }
		if !sidRe.MatchString(p.sid) {
			unreadAs("prompt-untold:", "the hook logged no session id (sid="+p.sid+")")
			continue
		}
		s, why := promptSession(d.Home, p)
		if why != "" {
			unread(why)
			continue
		}
		if s.Project != "" {
			name = s.Project + " " + name
		}
		if s.State != sessionstate.Waiting {
			if !s.Time.After(p.at) {
				unreadAs("prompt-untold:", fmt.Sprintf("crius-hook's state shows %q from %s, older than the prompt, so it did not record the dialog", s.State, s.Time.Format("15:04:05")))
			}
			continue // else it moved on after the prompt: answered
		}
		if s.Thread == "" && s.Surface != "" {
			continue // a cmux tab: the prompt watch reads its dialog
		}
		if s.Thread != "" && s.Thread == crius {
			continue
		}
		if s.Thread != "" {
			prompted[s.Thread] = true
		}
		// The dialog open now opened over a minute after the line (the notification comes seconds after the dialog):
		// the line's was answered and this is a later one that logged no line of its own (a subagent's). It is timed
		// and keyed from its own start. A file from before crius-hook kept Opened has no start: the line's time stands.
		if o, ok := s.OldestOpen(); ok && o.After(p.at.Add(time.Minute)) {
			asked, text, slack = o, "a later dialog that logged no notification line of its own", 2*time.Second
		}
		if waited() < promptAfter {
			continue
		}
		if s.Thread == "" || d.Thread == nil {
			out = append(out, [2]string{"prompt:" + key(), fmt.Sprintf("permission prompt unanswered for %d min in %s, waiting call %s; asked at %s, %q (%s); crius-hook's state still shows the dialog open (a long tool run after approval looks the same)",
				int(waited().Minutes()), name, waitingCall(s.Config, s.SessionID, dialogAgent(s, asked, slack), asked, slack), asked.Format("15:04"), text, strings.Join(about, ", "))})
			continue
		}
		about = append(about, "T3 thread "+s.Thread)
		if stop != "" {
			unread(stop)
			continue
		}
		th, err := d.Thread(s.Thread)
		if err != nil {
			stop = t3Stop(err)
			unread("t3 could not show its thread: " + err.Error())
			continue
		}
		if th.Pending == 0 {
			continue // no open request: answered, or the approved tool still runs
		}
		// The waiting call and T3's count go before the long parts (a T3 thread id runs to 150 characters), so the cut at
		// MaxLine takes those last.
		out = append(out, [2]string{"prompt:" + key(), fmt.Sprintf("permission prompt unanswered for %d min in %s, waiting call %s; T3 holds %d open request(s); asked at %s, %q (%s)",
			int(waited().Minutes()), name, waitingCall(s.Config, s.SessionID, dialogAgent(s, asked, slack), asked, slack), th.Pending, asked.Format("15:04"), text, strings.Join(about, ", "))})
	}
	return out, prompted
}

// dialogAgent names who holds the dialog in question, as crius-hook's state has it: the one whose dialog opened last by
// asked+slack ("main" or a subagent's id), as the dialog in question opened just before its notification line (or is the
// one opened at asked). A dialog left open from earlier is another one (deep review, 11-20). "" when it cannot tell: none
// open by then, a dialog only a notification opened ("notify", no agent named), or two open and one with no time.
func dialogAgent(s sessionstate.State, asked time.Time, slack time.Duration) string {
	who, last := "", time.Time{}
	for _, w := range s.Waits {
		t, ok := s.Opened[w]
		if !ok {
			if len(s.Waits) > 1 {
				return ""
			}
			who = w // a file from before crius-hook kept open times: its one dialog
			break
		}
		if !t.After(asked.Add(slack)) && (who == "" || t.After(last)) {
			who, last = w, t
		}
	}
	if who == "notify" {
		return ""
	}
	return who
}

// promptSession finds crius-hook's state file of the session the prompt names by its first 8 characters; when several
// ids start with them, the one whose cwd ends like the line's (the hook cuts a long cwd to "…" and its last 59). why
// says what was not found or read.
func promptSession(home string, p permPrompt) (sessionstate.State, string) {
	dir := filepath.Join(home, "sessions")
	files, err := filepath.Glob(filepath.Join(dir, p.sid+"*.json"))
	if err != nil {
		return sessionstate.State{}, err.Error()
	}
	var found []sessionstate.State
	for _, f := range files {
		s, err := sessionstate.Read(dir, strings.TrimSuffix(filepath.Base(f), ".json"))
		if err != nil {
			return sessionstate.State{}, "its crius-hook state file: " + err.Error()
		}
		if len(files) == 1 || strings.HasSuffix(s.Cwd, strings.TrimPrefix(p.cwd, "…")) {
			found = append(found, s)
		}
	}
	switch len(found) {
	case 0:
		return sessionstate.State{}, fmt.Sprintf("crius-hook keeps no state file for a session %s… in %s (is crius-hook a hook in that account?)", p.sid, dir)
	case 1:
		return found[0], ""
	}
	return sessionstate.State{}, fmt.Sprintf("%d crius-hook state files in %s start with %s and have that cwd", len(found), dir, p.sid)
}

// parsePrompt reads one permission_prompt line; ok is false for any other line.
func parsePrompt(line string, now time.Time) (permPrompt, bool) {
	m := promptRe.FindStringSubmatch(line)
	if m == nil {
		return permPrompt{}, false
	}
	t, err := lineTime(m[1], now)
	if err != nil {
		return permPrompt{}, false
	}
	exact := m[5] == m[1][len(m[1])-5:] // at= names the same minute as the stamp (the hook takes both from one clock read)
	if exact {
		sec, _ := strconv.Atoi(m[6])
		t = t.Add(time.Duration(sec) * time.Second)
	}
	return permPrompt{at: t, exact: exact, title: m[2], cwd: m[3], sid: m[4], text: m[7]}, true
}

// tailSince gives the lines at the end of the file from about since on: it reads back from the end, more each time,
// until a line stamped before since or the start of the file (waits.log is megabytes; a run needs its last hours). The
// first line may be older than since; a line cut by the read is left out.
func tailSince(path string, since, now time.Time) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := fi.Size()
	for n := int64(64 << 10); ; n *= 4 {
		off := max(0, size-n)
		buf := make([]byte, size-off)
		if _, err := f.ReadAt(buf, off); err != nil && err != io.EOF {
			return nil, err
		}
		lines := strings.Split(string(buf), "\n")
		if off > 0 {
			lines = lines[1:]
		}
		if off == 0 || startsBefore(lines, since, now) {
			return lines, nil
		}
	}
}

// startsBefore reports whether the first stamped line is older than since.
func startsBefore(lines []string, since, now time.Time) bool {
	for _, l := range lines {
		if len(l) < 16 {
			continue
		}
		if t, err := lineTime(l[:16], now); err == nil {
			return t.Before(since)
		}
	}
	return false
}
