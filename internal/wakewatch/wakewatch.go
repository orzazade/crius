// Package wakewatch tells the owner when Crius missed a wake: a lead's DONE, NEED, CLEAR-READY or ASK to Crius with no
// crius:lead line after it on the same bus for 20 min, or a lead tab waiting on a question or dialog for 10 min. It
// runs as its own launchd job, outside any chat, and sends to the owner directly (Telegram), because the session it
// watches is the one that missed the wake. Like the watchdog it imports nothing from the ingest or the store.
package wakewatch

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/redact"
	"github.com/orzazade/crius/internal/watchdog"
)

const (
	wakeAfter   = 20 * time.Minute
	screenAfter = 10 * time.Minute
	window      = 12 * time.Hour // older lines are history: a first run must not page the owner for them
	maxText     = 3500           // Telegram allows 4096; the header and one cut alert stay under it

	header        = "Crius missed a wake:\n"
	screensFailed = "screens-failed"
)

// wakeTypes ask Crius to act; replyTypes from crius:lead show it woke.
var (
	wakeTypes  = map[string]bool{"DONE": true, "NEED": true, "CLEAR-READY": true, "ASK": true}
	replyTypes = map[string]bool{"ORDER": true, "ANSWER": true, "VERIFIED": true, "ACK": true, "INFO": true, "GO": true}
	lineRe     = regexp.MustCompile(`^(\d{4}-\d\d-\d\dT\d\d:\d\dZ?) ([A-Z][A-Z-]*) (\S+)(?: -> (.*?))? — `)
	// A Claude Code permission or choice dialog: a numbered option under the cursor.
	dialogRe = regexp.MustCompile(`(?m)^\s*❯\s*1\.\s`)
)

// Screen is one tab. Err is set when its read failed; Text is then empty.
type Screen struct{ Surface, Title, Text, Err string }

type Deps struct {
	Home    string
	Now     time.Time
	Screens func() ([]Screen, error)
	Send    func(text string) error
}

type seenScreen struct {
	Hash  string
	Since time.Time
}

type state struct {
	Alerted     map[string]time.Time // key -> when sent
	Screens     map[string]seenScreen
	SendFailing bool
}

// Run checks every bus log and every tab once and sends whatever is new, in parts under Telegram's limit. A part that
// fails is not marked sent, so the next run tries again; the first failure in a row also goes to Crius's bus.
func Run(d Deps) error {
	path := filepath.Join(d.Home, "state", "wakewatch.json")
	st := state{}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &st); err != nil {
			// Starting afresh can repeat an alert; stopping at the bad file would silence every later one.
			st = state{}
			if err := watchdog.Post(d.Home, d.Now, fmt.Sprintf("wakewatch state %s was unreadable (%v); started afresh", path, err)); err != nil {
				return err
			}
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if st.Alerted == nil {
		st.Alerted = map[string]time.Time{}
	}
	if st.Screens == nil {
		st.Screens = map[string]seenScreen{}
	}

	alerts, err := missed(d.Home, d.Now)
	if err != nil {
		return err
	}
	alerts = append(alerts, waiting(&st, d)...)

	active := map[string]bool{}
	var parts [][][2]string // each part fits one message
	size := 0
	for _, a := range alerts {
		active[a[0]] = true
		if _, done := st.Alerted[a[0]]; done {
			continue
		}
		// Redact first: a cut could split a secret so the redactor no longer sees it, and redaction can grow the text.
		a[1], _ = redact.Redact(a[1])
		if r := []rune(a[1]); len(r) > maxText {
			a[1] = string(r[:maxText]) + "…"
		}
		if n := len([]rune(a[1])) + 1; len(parts) == 0 || size+n > maxText {
			parts, size = append(parts, nil), n
		} else {
			size += n
		}
		parts[len(parts)-1] = append(parts[len(parts)-1], a)
	}
	var sendErr error
	for _, p := range parts {
		texts := make([]string, len(p))
		for i, a := range p {
			texts[i] = a[1]
		}
		if sendErr = d.Send(header + strings.Join(texts, "\n")); sendErr != nil {
			break
		}
		for _, a := range p {
			st.Alerted[a[0]] = d.Now
		}
	}
	if sendErr != nil && !st.SendFailing {
		if err := watchdog.Post(d.Home, d.Now, "wakewatch cannot reach the owner: "+sendErr.Error()); err != nil {
			return err
		}
	}
	st.SendFailing = sendErr != nil
	// A key goes once its alert is no longer raised (line answered or past the window, tab stopped asking, check
	// works again), so the same thing can alert again later and a lasting one is never re-sent.
	for surface, s := range st.Screens {
		active[screenKey(surface, s.Hash)] = true
	}
	if active[screensFailed] { // no tab was read: a tab already reported unreadable has not recovered
		for k := range st.Alerted {
			if strings.HasPrefix(k, "screen-failed:") {
				active[k] = true
			}
		}
	}
	for k := range st.Alerted {
		if !active[k] {
			delete(st.Alerted, k)
		}
	}
	b, _ := json.Marshal(st)
	tmp, err := os.CreateTemp(filepath.Dir(path), "wakewatch.json.")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(b)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), path)
	}
	if werr != nil {
		os.Remove(tmp.Name())
		return werr
	}
	return sendErr
}

func hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:8])
}

// missed gives [key, text] for each wake line to Crius, 20 min to 12 h old, with no reply line after it in its file.
func missed(home string, now time.Time) ([][2]string, error) {
	logs, err := filepath.Glob(filepath.Join(home, "bus", "*.log"))
	if err != nil {
		return nil, err
	}
	sort.Strings(logs)
	var out [][2]string
	for _, log := range logs {
		b, err := os.ReadFile(log)
		if err != nil {
			return nil, err
		}
		// Only whole lines: a last line with no newline may still be being written.
		text := string(b[:bytes.LastIndexByte(b, '\n')+1])
		var open []string
		for _, line := range strings.Split(text, "\n") {
			m := lineRe.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			typ, from, to := m[2], m[3], m[4]
			if from == "crius:lead" && replyTypes[typ] {
				open = nil
				continue
			}
			if !wakeTypes[typ] || !toCrius(to) {
				continue
			}
			at, err := lineTime(m[1])
			if err != nil {
				continue
			}
			if age := now.Sub(at); age > wakeAfter && age <= window {
				open = append(open, line)
			}
		}
		for _, line := range open {
			out = append(out, [2]string{"bus:" + hash(log+"\n"+line), filepath.Base(log) + ": " + line})
		}
	}
	return out, nil
}

func toCrius(to string) bool {
	for _, r := range strings.Split(to, ",") {
		if r = strings.TrimSpace(r); r == "crius:lead" || r == "crius" {
			return true
		}
	}
	return false
}

func lineTime(s string) (time.Time, error) {
	if strings.HasSuffix(s, "Z") {
		return time.Parse("2006-01-02T15:04Z", s)
	}
	return time.ParseInLocation("2006-01-02T15:04", s, time.Local)
}

// waiting gives [key, text] for each tab whose screen has waited on a question or dialog, unchanged, for 10 min.
// A screen read that fails is an alert of its own: the whole check, or one tab, which keeps its place meanwhile.
func waiting(st *state, d Deps) [][2]string {
	screens, err := d.Screens()
	if err != nil {
		return [][2]string{{screensFailed, "the tab check failed to look: " + err.Error()}}
	}
	var out [][2]string
	live := map[string]bool{}
	for _, s := range screens {
		if s.Err != "" {
			live[s.Surface] = true
			out = append(out, [2]string{"screen-failed:" + s.Surface, fmt.Sprintf("tab %q (%s) could not be read: %s", s.Title, s.Surface, s.Err)})
			continue
		}
		if s.Title == "crius" || !asks(s.Text) {
			continue
		}
		live[s.Surface] = true
		h := hash(s.Text)
		seen, ok := st.Screens[s.Surface]
		if !ok || seen.Hash != h {
			st.Screens[s.Surface] = seenScreen{h, d.Now}
			continue
		}
		if wait := d.Now.Sub(seen.Since); wait >= screenAfter {
			out = append(out, [2]string{screenKey(s.Surface, h),
				fmt.Sprintf("tab %q (%s) waits on a question or dialog for %d min", s.Title, s.Surface, int(wait.Minutes()))})
		}
	}
	for k := range st.Screens {
		if !live[k] {
			delete(st.Screens, k)
		}
	}
	return out
}

func screenKey(surface, hash string) string { return "screen:" + surface + ":" + hash }

// asks reports whether a screen waits on someone: its newest status emoji is ❓, or a dialog offers numbered options.
func asks(text string) bool {
	if dialogRe.MatchString(text) {
		return true
	}
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if j := strings.LastIndexAny(lines[i], "❓✅⏳"); j >= 0 {
			return strings.HasPrefix(lines[i][j:], "❓")
		}
	}
	return false
}
