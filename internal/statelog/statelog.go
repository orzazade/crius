// Package statelog reads the logs wakewatch and promptwatch write while the screen decides (observe mode): each line
// is a tab whose screen and hook state disagreed, or agreed again. For each disagreement it says which side was right,
// from the transcripts of the tab's sessions at that time, for the switch to decide mode.
package statelog

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/sessionstate"
)

// Line is one line of a state log.
type Line struct {
	Time    time.Time // in the zone the log wrote
	Log     string    // wakewatch or promptwatch
	Key     string    // the tab as the log names it: tab:surface:N, or workspace:N/surface:N
	Tab     string    // its name, "quotes · p1"
	Screen  string    // idle, busy, prompt
	State   string    // the hook's: idle, busy, running, waiting
	Decides string
}

// Finding is one disagreement: until the tab's next line (zero: still open at the log's end) and which side was right
// (screen, state, neither or unknown), with why.
type Finding struct {
	Line
	Until      time.Time
	Right, Why string
}

var (
	wakeRe   = regexp.MustCompile(`^(\S+) (\S+) "([^"]*)" screen=(\S+) state=(\S+) decides=(\S+)$`)
	promptRe = regexp.MustCompile(`^(\S+) (\S+) (\S+) (\S+) screen=(\S+) state=(\S+) decides=(\S+)$`)
)

// Parse reads one log: "wakewatch" (RFC 3339 times, a quoted tab name) or "promptwatch" (date's %z, the tab name with
// "_" for spaces). A line in neither format is an error naming it.
func Parse(log string, r io.Reader) ([]Line, error) {
	var out []Line
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		var l Line
		var ts string
		var err error
		switch log {
		case "wakewatch":
			m := wakeRe.FindStringSubmatch(sc.Text())
			if m == nil {
				return nil, fmt.Errorf("%s line %d: not in the format", log, n)
			}
			ts, l = m[1], Line{Key: m[2], Tab: m[3], Screen: m[4], State: m[5], Decides: m[6]}
			l.Time, err = time.Parse(time.RFC3339, ts)
		case "promptwatch":
			m := promptRe.FindStringSubmatch(sc.Text())
			if m == nil {
				return nil, fmt.Errorf("%s line %d: not in the format", log, n)
			}
			// promptwatch --all writes each space of a tab's title as "_"; a title with a real "_" then finds no
			// session file and comes out unknown
			ts, l = m[1], Line{Key: m[3] + "/" + m[4], Tab: strings.ReplaceAll(m[2], "_", " "), Screen: m[5], State: m[6], Decides: m[7]}
			l.Time, err = time.Parse("2006-01-02T15:04:05-0700", ts)
		default:
			return nil, fmt.Errorf("no log %q", log)
		}
		if err != nil {
			return nil, fmt.Errorf("%s line %d: %w", log, n, err)
		}
		l.Log = log
		out = append(out, l)
	}
	return out, sc.Err()
}

// class puts both sides' words in one set: working, idle or waiting (a dialog).
func class(s string) string {
	switch s {
	case "busy", "running":
		return "working"
	case "prompt", "waiting":
		return "waiting"
	}
	return s
}

// Judge gives a finding for each line where the two sides disagree. A tab "<project> · <role>" is the sessions ss
// names for that role; what their main thread did at the line's time comes from their transcripts (the store has
// where they are): the last line before it and the first after. A prompt or a tool result last means a request was
// due, and a reply line before and after with the same request id means one was streaming: working. A turn that
// ended (end_turn) or an Esc ("[Request interrupted by user") means free. A tool call with its result after the line,
// or none yet, ran across it: working, unless a side says dialog, which the transcript cannot tell from a running
// tool (unknown). Subagent transcripts are not read: their work leaves the main thread free.
func Judge(db *sql.DB, ss []sessionstate.State, lines []Line) ([]Finding, error) {
	lines = slices.Clone(lines)
	slices.SortStableFunc(lines, func(a, b Line) int { return a.Time.Compare(b.Time) })
	read := map[string]transcriptRead{}
	var out []Finding
	for i, l := range lines {
		if class(l.Screen) == class(l.State) {
			continue
		}
		f := Finding{Line: l}
		for _, next := range lines[i+1:] {
			if next.Log == l.Log && next.Key == l.Key {
				f.Until = next.Time
				break
			}
		}
		var err error
		f.Right, f.Why, err = judge(db, ss, l, read)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

// event is one user or assistant line of a main transcript.
type event struct {
	at        time.Time
	user      bool
	interrupt bool   // the user line Esc writes
	stop      string // the assistant line's stop_reason
	request   string // its request id
	open      int    // tool calls of its transcript with no result yet, after it
}

type transcriptRead struct {
	es  []event
	err error
}

func judge(db *sql.DB, ss []sessionstate.State, l Line, read map[string]transcriptRead) (right, why string, err error) {
	project, role, ok := strings.Cut(l.Tab, " · ")
	if !ok {
		return "unknown", "not a project · role tab", nil
	}
	var ids []any
	for _, s := range ss {
		if s.Project == project && s.Role == role {
			ids = append(ids, s.SessionID)
		}
	}
	if len(ids) == 0 {
		return "unknown", fmt.Sprintf("no session file for %s %s", project, role), nil
	}
	rows, err := db.Query("select transcript_path, min(ts_first), max(ts_last) from request where session in (?"+
		strings.Repeat(", ?", len(ids)-1)+") and transcript_path is not null and transcript_path not like '%/subagents/%'"+
		" group by transcript_path order by 1", ids...)
	if err != nil {
		return "", "", err
	}
	type span struct {
		path        string
		first, last sql.NullString
	}
	var spans []span
	for rows.Next() {
		var s span
		if err := rows.Scan(&s.path, &s.first, &s.last); err != nil {
			rows.Close()
			return "", "", err
		}
		spans = append(spans, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", "", err
	}
	if len(spans) == 0 {
		return "unknown", fmt.Sprintf("no transcript in the store for %s %s", project, role), nil
	}
	var es []event
	var gone []span
	for _, s := range spans {
		if _, ok := read[s.path]; !ok {
			e, err := events(s.path)
			read[s.path] = transcriptRead{e, err}
		}
		switch r := read[s.path]; {
		case errors.Is(r.err, fs.ErrNotExist):
			gone = append(gone, s)
		case r.err != nil:
			return "unknown", "the transcript: " + r.err.Error(), nil
		default:
			es = append(es, r.es...)
		}
	}
	// each transcript's times only rise (events), so a stable sort keeps each one's order
	slices.SortStableFunc(es, func(a, b event) int { return a.at.Compare(b.at) })
	// the log cuts its time to the second: a line within that second is before it
	cut := l.Time.Add(time.Second)
	n := sort.Search(len(es), func(i int) bool { return !es[i].at.Before(cut) })
	// a transcript that is gone matters when its requests began before the line and ran past the last line read
	for _, s := range gone {
		first, ferr := time.Parse(time.RFC3339Nano, s.first.String)
		last, lerr := time.Parse(time.RFC3339Nano, s.last.String)
		if ferr != nil || lerr != nil || first.Before(cut) && (n == 0 || last.Add(time.Minute).After(es[n-1].at)) {
			return "unknown", "the transcript: " + read[s.path].err.Error(), nil
		}
	}
	hhmm := func(t time.Time) string { return t.In(l.Time.Location()).Format("15:04:05") }
	did, why := decide(es, n, hhmm)
	// a line in the log line's own second may be before or after it: it counts only when it does not change the answer
	for k := sort.Search(n, func(i int) bool { return !es[i].at.Before(l.Time) }); k < n; k++ {
		if d, _ := decide(es, k, hhmm); d != did {
			return "unknown", "a transcript line at " + hhmm(es[k].at) + ", in the log line's own second, changes the answer", nil
		}
	}
	if did == "" {
		return "unknown", why, nil
	}
	sc, st := class(l.Screen), class(l.State)
	switch {
	case did == "tool" && (sc == "waiting" || st == "waiting"):
		return "unknown", why + "; the transcript cannot tell a running tool from a permission dialog", nil
	case did != "free" && sc == "working", did == "free" && sc == "idle":
		return "screen", why, nil
	case did != "free" && st == "working", did == "free" && st == "idle":
		return "state", why, nil
	}
	return "neither", why, nil
}

// decide says what the main thread did after its first n events: "working", "free", or "tool" (a tool call open: a
// running tool or a dialog), and why; "" when there is no event yet.
func decide(es []event, n int, hhmm func(time.Time) string) (did, why string) {
	if n == 0 {
		return "", "no line of the main thread before it"
	}
	last := es[n-1]
	var next *event
	if n < len(es) {
		next = &es[n]
	}
	then := "nothing since"
	if next != nil {
		then = "next line at " + hhmm(next.at)
	}
	switch {
	case last.interrupt:
		did, why = "free", "Esc at "+hhmm(last.at)+", "+then
	case last.user && last.open > 0:
		did, why = "tool", fmt.Sprintf("%d tool call(s) still open after the tool result at %s, %s", last.open, hhmm(last.at), then)
	case last.user:
		did, why = "working", "a request due after the prompt or tool result at "+hhmm(last.at)+", "+then
	case next != nil && !next.user && next.request == last.request:
		did, why = "working", "a reply streaming from "+hhmm(last.at)+" to "+hhmm(next.at)
	case last.stop == "tool_use":
		did, why = "tool", "a tool call open since "+hhmm(last.at)+", no result yet"
		if next != nil {
			why = "a tool call open from " + hhmm(last.at) + " to " + hhmm(next.at)
		}
	default:
		did, why = "free", "the turn ended at "+hhmm(last.at)+" ("+last.stop+"), "+then
	}
	return did, why
}

// events reads the main thread's user and assistant lines of one transcript; meta and sidechain lines are not its own,
// and a compaction's summary is written after the turn with an older time. A line's time is the latest so far: a tool
// result can be stamped before its call. A tool call stays open until its result; a prompt or an Esc closes them all.
func events(path string) ([]event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []event
	var at time.Time
	open := map[string]bool{}
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var v struct {
				Type      string    `json:"type"`
				Timestamp time.Time `json:"timestamp"`
				Sidechain bool      `json:"isSidechain"`
				Meta      bool      `json:"isMeta"`
				Compact   bool      `json:"isCompactSummary"`
				RequestID string    `json:"requestId"`
				Message   struct {
					StopReason string          `json:"stop_reason"`
					Content    json.RawMessage `json:"content"`
				} `json:"message"`
			}
			if jerr := json.Unmarshal(line, &v); jerr != nil {
				if err == io.EOF { // the last line is still being written
					return out, nil
				}
				return nil, fmt.Errorf("%s: %w", path, jerr)
			}
			if (v.Type == "user" && !local(v.Message.Content) || v.Type == "assistant") && !v.Sidechain && !v.Meta && !v.Compact {
				if v.Timestamp.After(at) {
					at = v.Timestamp
				}
				e := event{at: at, user: v.Type == "user", interrupt: v.Type == "user" && interrupted(v.Message.Content),
					stop: v.Message.StopReason, request: v.RequestID}
				var blocks []struct {
					Type      string `json:"type"`
					ID        string `json:"id"`
					ToolUseID string `json:"tool_use_id"`
				}
				json.Unmarshal(v.Message.Content, &blocks)
				results := false
				for _, b := range blocks {
					switch {
					case b.Type == "tool_use" && b.ID != "":
						open[b.ID] = true
					case b.Type == "tool_result":
						results = true
						delete(open, b.ToolUseID)
					}
				}
				if e.user && !results {
					clear(open)
				}
				e.open = len(open)
				out = append(out, e)
			}
		}
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// local reports whether a user line's content is a ! command, its output, or a built-in /command and its output, none
// of which starts a request. A skill's line starts with <command-message> and does.
func local(content json.RawMessage) bool {
	var s string
	if json.Unmarshal(content, &s) != nil {
		return false
	}
	for _, p := range []string{"<bash-input>", "<bash-stdout>", "<bash-stderr>", "<local-command-stdout>", "<local-command-stderr>", "<command-name>"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// interrupted reports whether a user line's content is the text Esc writes, as a string or a text block.
func interrupted(content json.RawMessage) bool {
	const esc = "[Request interrupted by user"
	var s string
	if json.Unmarshal(content, &s) == nil {
		return strings.HasPrefix(s, esc)
	}
	var blocks []struct{ Type, Text string }
	json.Unmarshal(content, &blocks)
	for _, b := range blocks {
		if b.Type == "text" && strings.HasPrefix(b.Text, esc) {
			return true
		}
	}
	return false
}

var rights = []string{"state", "screen", "neither", "unknown"}

// Text is the report for people: per tab, its count by right side, then each disagreement.
func Text(fs []Finding) string {
	var b strings.Builder
	byTab := map[string][]Finding{}
	var tabs []string
	for _, f := range fs {
		if byTab[f.Tab] == nil {
			tabs = append(tabs, f.Tab)
		}
		byTab[f.Tab] = append(byTab[f.Tab], f)
	}
	slices.Sort(tabs)
	for _, tab := range tabs {
		fmt.Fprintf(&b, "%s: %s\n", tab, counts(byTab[tab]))
		for _, f := range byTab[tab] {
			right := f.Right + " right"
			if f.Right == "unknown" {
				right = "unknown"
			}
			lasted := "still open"
			if !f.Until.IsZero() {
				lasted = "for " + f.Until.Sub(f.Time).String()
			}
			fmt.Fprintf(&b, "  %s %s screen=%s state=%s %s: %s (%s)\n", f.Time.Format("01-02 15:04:05"), f.Log,
				f.Screen, f.State, lasted, right, f.Why)
		}
	}
	fmt.Fprintf(&b, "total: %s\n", counts(fs))
	return b.String()
}

func counts(fs []Finding) string {
	n := map[string]int{}
	for _, f := range fs {
		n[f.Right]++
	}
	var s []string
	for _, r := range rights {
		if n[r] > 0 && r == "unknown" {
			s = append(s, fmt.Sprintf("unknown %d", n[r]))
		} else if n[r] > 0 {
			s = append(s, fmt.Sprintf("%s right %d", r, n[r]))
		}
	}
	return fmt.Sprintf("%d disagreements: %s", len(fs), strings.Join(s, ", "))
}
