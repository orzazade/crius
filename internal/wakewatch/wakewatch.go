// Package wakewatch tells the owner when Crius missed a wake: a lead's DONE, NEED, CLEAR-READY or ASK to Crius with no
// crius:lead line after it on the same bus for 20 min, a lead tab waiting on a question or dialog for 10 min, or a
// lead tab idle for 15 min (or any tab whose last reply says CLEAR-READY), and the Mac short of disk or deep in swap.
// It runs as its own launchd job, outside
// any chat, and wakes Crius by typing one line into Crius's own tab: Crius then acts, and sends the owner a push
// notification when the owner is needed (owner 10-21 23:5x: no Telegram). Like the watchdog it imports nothing from
// the ingest or the store.
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
	"unicode"

	"github.com/orzazade/crius/internal/redact"
	"github.com/orzazade/crius/internal/watchdog"
)

const (
	wakeAfter   = 20 * time.Minute
	screenAfter = 10 * time.Minute
	idleAfter   = 15 * time.Minute
	window      = 12 * time.Hour   // older lines are history: a first run must not wake Crius for them
	resend      = 20 * time.Minute // an item still open wakes Crius again, never more often
	minFreeGB   = 20               // less free disk than this wakes Crius
	maxSwapGB   = 20               // more swap in use than this wakes Crius
	maxLine     = 500

	Prefix        = "WAKEWATCH: "
	screensFailed = "screens-failed"
)

// wakeTypes ask Crius to act; replyTypes from crius:lead show it woke.
var (
	wakeTypes  = map[string]bool{"DONE": true, "NEED": true, "CLEAR-READY": true, "ASK": true}
	replyTypes = map[string]bool{"ORDER": true, "ANSWER": true, "VERIFIED": true, "ACK": true, "INFO": true, "GO": true}
	lineRe     = regexp.MustCompile(`^(\d{4}-\d\d-\d\dT\d\d:\d\dZ?) ([A-Z][A-Z-]*) (\S+)(?: -> (.*?))? — `)
	// A Claude Code permission or choice dialog: a numbered option under the cursor.
	dialogRe = regexp.MustCompile(`(?m)^\s*❯\s*1\.\s`)
	// The input box's top border carries the session name: "──── quotes: lead ─".
	borderRe  = regexp.MustCompile(`^─+(?: (.+?) ─+)?$`)
	sessionRe = regexp.MustCompile(`^([A-Za-z0-9][\w.-]*)\s*[:·]\s*([\w.-]+)$`)
	// Under the input box: background shells, monitors, tasks or agents still running.
	jobsRe = regexp.MustCompile(`\b\d+ (?:shells?|monitors?|background tasks?|agents?)\b|(?m)^\s*◯ `)
	leadRe = regexp.MustCompile(`^lead(?:er)?$`)
)

// Screen is one tab. Err is set when its read failed; Text is then empty.
type Screen struct{ Surface, Title, Text, Err string }

type Deps struct {
	Home      string
	Now       time.Time
	Screens   func() ([]Screen, error)
	Send      func(text string) error
	Resources func() (freeDiskGB, swapUsedGB float64, err error)
}

type seenScreen struct {
	Hash  string
	Since time.Time
	Idle  bool // its idle alert is raised; a bus line from it lowers it, so the next idle spell alerts again
}

type state struct {
	Alerted     map[string]time.Time // key -> when last sent
	Screens     map[string]seenScreen
	SendFailing bool
}

// Run checks every bus log and every tab once and sends each alert that is new, or still open 20 min after it was
// last sent, as one line. A line that fails is not marked sent, so the next run tries again; the first failure in a
// row also goes to Crius's bus.
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
	alerts = append(alerts, resources(d)...)

	active := map[string]bool{}
	var sendErr error
	for _, a := range alerts {
		active[a[0]] = true
		if last, ok := st.Alerted[a[0]]; sendErr != nil || ok && d.Now.Sub(last) < resend {
			continue
		}
		// Redact first: a cut could split a secret so the redactor no longer sees it, and redaction can grow the text.
		// The line is typed: cmux send reads a backslash-n or -r as Enter, so no backslash and no line break reach it.
		text, _ := redact.Redact(a[1])
		// cmux reads \n as Enter, and control bytes (Ctrl-U, Esc) would edit Crius's prompt: neither may pass.
		text = strings.Join(strings.Fields(strings.Map(printable, text)), " ")
		if r := []rune(text); len(r) > maxLine {
			text = string(r[:maxLine]) + "…"
		}
		if sendErr = d.Send(Prefix + text); sendErr == nil {
			st.Alerted[a[0]] = d.Now
		}
	}
	if sendErr != nil && !st.SendFailing {
		if err := watchdog.Post(d.Home, d.Now, "wakewatch cannot reach Crius's tab: "+sendErr.Error()); err != nil {
			return err
		}
	}
	st.SendFailing = sendErr != nil
	// A key goes once its alert is no longer raised (line answered or past the window, tab stopped asking, check
	// works again), so the same thing alerts at once when it comes back; a lasting one keeps its 20 min rhythm.
	for surface, s := range st.Screens {
		active[screenKey(surface, s.Hash)] = true
		if s.Idle {
			active[idleKey(surface, s.Hash)] = true
		}
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

// waiting gives [key, text] for each tab whose screen has waited on a question or dialog, unchanged, for 10 min, and
// for each idle tab: unchanged and with no bus line of its own for 15 min. A screen counts as unchanged by what is
// above its input box: the status line below it holds a clock. A screen read that fails is an alert of its own: the
// whole check, or one tab, which keeps its place meanwhile.
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
		if s.Title == "crius" {
			continue
		}
		body, foot, boxed := split(s.Text)
		ask := asks(s.Text)
		var who string
		var clear bool
		var lastLine time.Time
		if !ask {
			if !boxed || strings.Contains(s.Text, "esc to interrupt") || jobsRe.MatchString(foot) {
				continue
			}
			var role string
			if who, role = session(s.Text, s.Title); who == "" {
				continue
			}
			clear = clearReady(body)
			if !leadRe.MatchString(role) && !clear {
				continue
			}
			var found bool
			var err error
			if lastLine, found, err = lastFrom(d.Home, who); err != nil {
				live[s.Surface] = true
				out = append(out, [2]string{"screen-failed:" + s.Surface, fmt.Sprintf("tab %q (%s): idle check could not read its bus: %v", s.Title, s.Surface, err)})
				continue
			} else if !found {
				continue // no bus for this project: not a team Crius runs
			}
		}
		live[s.Surface] = true
		name, _ := session(s.Text, s.Title) // another session in the same tab starts afresh
		h := hash(name + "\n" + body)
		seen, ok := st.Screens[s.Surface]
		if !ok || seen.Hash != h {
			st.Screens[s.Surface] = seenScreen{Hash: h, Since: d.Now}
			continue
		}
		if ask {
			if wait := d.Now.Sub(seen.Since); wait >= screenAfter {
				out = append(out, [2]string{screenKey(s.Surface, h),
					fmt.Sprintf("tab %q (%s) waits on a question or dialog for %d min", s.Title, s.Surface, int(wait.Minutes()))})
			}
			continue
		}
		from := seen.Since
		if lastLine.After(from) {
			from = lastLine
		}
		seen.Idle = d.Now.Sub(from) >= idleAfter
		st.Screens[s.Surface] = seen
		if wait := d.Now.Sub(from); seen.Idle {
			text := fmt.Sprintf("%s (tab %q, %s) is idle: not working, nothing running, no bus line from it for %d min", who, s.Title, s.Surface, int(wait.Minutes()))
			if clear {
				text += "; its screen says CLEAR-READY"
			}
			out = append(out, [2]string{idleKey(s.Surface, h), text})
		}
	}
	for k := range st.Screens {
		if !live[k] {
			delete(st.Screens, k)
		}
	}
	return out
}

// resources gives an alert when free disk is below 20 GB or swap in use above 20 GB, or when they could not be read.
func resources(d Deps) [][2]string {
	free, swap, err := d.Resources()
	if err != nil {
		return [][2]string{{"resources-failed", "the disk and swap check failed to look: " + err.Error()}}
	}
	var out [][2]string
	if free < minFreeGB {
		out = append(out, [2]string{"disk", fmt.Sprintf("disk: %.1f GB free, below %d GB", free, minFreeGB)})
	}
	if swap > maxSwapGB {
		out = append(out, [2]string{"swap", fmt.Sprintf("swap: %.1f GB in use, above %d GB", swap, maxSwapGB)})
	}
	return out
}

func screenKey(surface, hash string) string { return "screen:" + surface + ":" + hash }
func idleKey(surface, hash string) string   { return "idle:" + surface + ":" + hash }

func printable(r rune) rune {
	if r == '\\' {
		return '/'
	}
	if !unicode.IsPrint(r) {
		return ' '
	}
	return r
}

// Prompt gives the text in a Claude Code screen's input box, and whether the screen has one.
func Prompt(screen string) (string, bool) {
	_, foot, boxed := split(screen)
	if !boxed {
		return "", false
	}
	line := strings.SplitN(foot, "\n", 2)[0]
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "❯")), true
}

// split cuts a Claude Code screen at its input box: what is above it, what is below it, and whether it has one.
func split(text string) (body, foot string, boxed bool) {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 2; i >= 0; i-- {
		if borderRe.MatchString(strings.TrimSpace(lines[i])) && strings.HasPrefix(strings.TrimSpace(lines[i+1]), "❯") {
			return strings.Join(lines[:i], "\n"), strings.Join(lines[i+1:], "\n"), true
		}
	}
	return text, "", false
}

// session gives the bus name ("quotes:lead") and role of a tab, from the input box border or else the tab title.
func session(text, title string) (who, role string) {
	name := title
	lines := strings.Split(text, "\n")
	for i := len(lines) - 2; i >= 0; i-- {
		if m := borderRe.FindStringSubmatch(strings.TrimSpace(lines[i])); m != nil && m[1] != "" &&
			strings.HasPrefix(strings.TrimSpace(lines[i+1]), "❯") {
			name = m[1]
			break
		}
	}
	m := sessionRe.FindStringSubmatch(strings.TrimSpace(name))
	if m == nil {
		return "", ""
	}
	return m[1] + ":" + m[2], m[2]
}

// clearReady reports whether the last reply above the input box says CLEAR-READY. A screen with no reply marker has
// no last reply to read.
func clearReady(body string) bool {
	lines := strings.Split(body, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "⏺") {
			return strings.Contains(strings.Join(lines[i:], "\n"), "CLEAR-READY")
		}
	}
	return false
}

// lastFrom gives the time of the newest whole line sent by who ("quotes:lead") on its project's bus. found is false
// when the project has no bus.
func lastFrom(home, who string) (last time.Time, found bool, err error) {
	b, err := os.ReadFile(filepath.Join(home, "bus", strings.SplitN(who, ":", 2)[0]+".log"))
	if os.IsNotExist(err) {
		return last, false, nil
	} else if err != nil {
		return last, false, err
	}
	for _, line := range strings.Split(string(b[:bytes.LastIndexByte(b, '\n')+1]), "\n") {
		m := lineRe.FindStringSubmatch(line)
		if m == nil || m[3] != who {
			continue
		}
		if at, err := lineTime(m[1]); err == nil && at.After(last) {
			last = at
		}
	}
	return last, true, nil
}

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
