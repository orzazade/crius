package playbook

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/store"
	"github.com/orzazade/crius/internal/transcript"
)

// ReadStats says what a run looked at, so "found nothing" and "read nothing" differ.
type ReadStats struct {
	BusFiles, BusLines  int
	TxFiles, TxSkipped  int // transcripts parsed, and ones unchanged since the cursor
	TxTexts, OwnerTurns int
	TxCapped            int // replies past the per-session-day cap
	TxBadLines          int // transcript lines that were not valid JSON or had no time
	DocFiles            int
	ShownBad            int // shown.log lines that could not be read (skipped)
	Errors              []string
}

func (s ReadStats) String() string {
	return fmt.Sprintf("bus %d files %d new lines; transcripts %d read %d unchanged %d bad lines, %d assistant texts (%d past the cap) %d owner turns; docs %d; %d bad shown.log lines; %d read errors",
		s.BusFiles, s.BusLines, s.TxFiles, s.TxSkipped, s.TxBadLines, s.TxTexts, s.TxCapped, s.OwnerTurns, s.DocFiles, s.ShownBad, len(s.Errors))
}

// The rules. A strong phrase is enough; a weaker cause word needs a measurement
// beside it ("cut 702 s to 198 s"), since "because" alone is in every other reply.
var (
	strongRe  = regexp.MustCompile(`(?i)\b(root cause|works because|the fix (?:is|was)|turned out|lesson|gotcha|quirk|trap)\b`)
	causeRe   = regexp.MustCompile(`(?i)\b(the cause|the reason|fixed by|because|instead of|runs? past|cut|faster|slower|hangs?|split|cap|limit|from \S+ to)\b`)
	measureRe = regexp.MustCompile(`(?i)\b\d+(?:\.\d+)?\s?(?:ms|s|sec|min|h|%|x|gb|mb|kb|tests?)\b|\b\d+m\d+s\b`)
	// A standard Crius sets for a team.
	standardRe = regexp.MustCompile(`(?i)\b(make .{1,60} the standard|the standard way|from now on|as a rule|for every (?:team|lead|builder|brief))\b`)
	// The owner correcting the agent.
	correctionRe = regexp.MustCompile(`(?i)^(?:no[,.!]|nope\b|don'?t\b|never\b|stop\b|wrong\b|always\b|from now on\b)|\b(?:i said|i told you|i asked (?:you|for)|why did you|why are you|why do you|not what i asked|you should have|that's wrong|this is wrong|do not ever|never do|stop doing|from now on)\b`)
	fixRe        = regexp.MustCompile(`(?i)\bfix(?:ed|es)?\b`)
)

// finding returns why a text is a finding, or "".
func finding(text string) string {
	if strongRe.MatchString(text) {
		return "strong phrase"
	}
	if causeRe.MatchString(text) && measureRe.MatchString(text) {
		return "cause with a number"
	}
	return ""
}

// score ranks findings within one session: a strong phrase first, then the
// number of measurements. A transcript reply with a cause word needs two
// measurements ("702 s to 198 s"); one number is most often a status count.
func score(text string) int {
	n := len(measureRe.FindAllString(text, 8))
	switch {
	case strongRe.MatchString(text):
		return 10 + n
	case causeRe.MatchString(text) && n >= 2:
		return n
	}
	return 0
}

// perSessionDay is how many replies of one session and day may be candidates.
// Long build sessions write hundreds of status replies with numbers in them.
const perSessionDay = 6

const maxText = 1200

// excerpt keeps the paragraphs that hold the finding, cut to maxText runes.
func excerpt(text string) string {
	if len([]rune(text)) <= maxText {
		return text
	}
	var keep []string
	for _, p := range strings.Split(text, "\n\n") {
		if finding(p) != "" {
			keep = append(keep, p)
		}
	}
	out := strings.Join(keep, "\n\n")
	if out == "" {
		out = text
	}
	if r := []rune(out); len(r) > maxText {
		out = string(r[:maxText]) + "…"
	}
	return out
}

// Gather reads every source past the cursor and returns the candidates, oldest first.
// The cursor is moved in memory; the caller saves it.
func Gather(cfg Config, cur *Cursor, now time.Time) ([]Candidate, ReadStats, error) {
	var out []Candidate
	var st ReadStats
	for _, dir := range cfg.BusDirs {
		logs, err := filepath.Glob(filepath.Join(dir, "*.log"))
		if err != nil {
			return nil, st, err
		}
		if len(logs) == 0 {
			st.Errors = append(st.Errors, "no bus logs in "+dir)
		}
		for _, p := range logs {
			cs, n, err := readBus(p, cfg.Since, cur, now)
			st.BusFiles++
			st.BusLines += n
			if err != nil {
				st.Errors = append(st.Errors, err.Error())
			}
			out = append(out, cs...)
		}
	}
	// Seen ids are kept seenDays; a transcript resumed later must not offer its
	// older replies again, so nothing older than that window is read.
	txSince := cfg.Since
	if !cur.LastRun.IsZero() {
		if w := cur.LastRun.AddDate(0, 0, -(seenDays - 10)); w.After(txSince) {
			txSince = w
		}
	}
	if len(cfg.TxRoots) > 0 {
		files, err := transcript.Walk(cfg.TxRoots)
		if err != nil {
			st.Errors = append(st.Errors, err.Error())
		}
		for _, p := range files {
			info, err := os.Stat(p)
			if err != nil {
				st.Errors = append(st.Errors, err.Error())
				continue
			}
			mod := info.ModTime()
			if mod.Before(txSince) || !mod.After(cur.Tx[p]) {
				st.TxSkipped++
				continue
			}
			cs, err := readTranscript(p, txSince, cur, &st, now, cfg.ProjectOf)
			st.TxFiles++
			if err != nil {
				st.Errors = append(st.Errors, err.Error())
				continue
			}
			cur.Tx[p] = mod
			out = append(out, cs...)
		}
	}
	for _, p := range cfg.DocFiles {
		cs, err := readDoc(p, cur, now)
		if err != nil {
			st.Errors = append(st.Errors, err.Error())
			continue
		}
		st.DocFiles++
		out = append(out, cs...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	if len(st.Errors) > 0 && st.BusLines == 0 && st.TxFiles == 0 && st.DocFiles == 0 {
		return out, st, fmt.Errorf("read nothing: %s", strings.Join(st.Errors, "; "))
	}
	return out, st, nil
}

// OpenFail is a FAIL line whose fix has not been seen yet; it waits in the
// cursor up to failWait, so a fix posted after the night's read still pairs.
type OpenFail struct {
	Candidate Candidate `json:"candidate"`
	Project   string    `json:"project"`
}

const failWait = 24 * time.Hour

// readBus reads one log from the cursor's offset. A file shorter than the offset
// was rewritten, so it is read again from the start.
func readBus(path string, since time.Time, cur *Cursor, now time.Time) ([]Candidate, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	off := cur.Bus[path]
	st := cur.BusState[path] // dates undated lines from where the last read stopped
	if off > info.Size() {
		off, st = 0, store.State{}
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, 0, err
	}
	name := filepath.Base(path)
	r := bufio.NewReaderSize(f, 1<<16)
	pos := off
	n := 0
	var out []Candidate
	var readErr error
	open := cur.Fails[path]
	for {
		raw, err := r.ReadString('\n')
		if err != nil && err != io.EOF {
			readErr = fmt.Errorf("%s at byte %d: %w", path, pos, err)
			break
		}
		if !strings.HasSuffix(raw, "\n") {
			break // a half-written last line is read next time
		}
		at := pos
		pos += int64(len(raw))
		n++
		l := store.ParseLine(raw, &st, false)
		if l == nil {
			continue
		}
		t, err := time.ParseInLocation("2006-01-02T15:04", l.Date+"T"+l.Time, time.Local)
		if err != nil || t.Before(since) {
			continue
		}
		if l.Project == "playbook" && l.Role == "watch" {
			continue // the watcher's own report line
		}
		// A line that names a fix closes the oldest open FAIL of its project.
		if l.Type != "FAIL" && fixRe.MatchString(l.Text) {
			for i, of := range open {
				if of.Project == l.Project && t.Sub(of.Candidate.Time) <= failWait {
					c := of.Candidate
					c.Text = excerpt(c.Text + "\nTHEN " + l.Type + " " + l.Project + ":" + l.Role + " — " + l.Text)
					out = append(out, c)
					open = append(open[:i:i], open[i+1:]...)
					break
				}
			}
		}
		team := l.Project
		if team == "" {
			team = strings.TrimSuffix(name, ".log")
		}
		c := Candidate{ID: fmt.Sprintf("bus:%s:%d", path, at), Source: "bus", Team: team, Time: t,
			Ref: fmt.Sprintf("bus %s %sT%s @%d", name, l.Date, l.Time, at), Text: l.Type + " " + l.Project + ":" + l.Role + " — " + l.Text}
		switch {
		case l.Type == "LEARNED":
			c.Why = "LEARNED line"
		case l.Type == "CORRECTED" || l.Type == "CORRECTION":
			c.Why = "correction"
		case l.Type == "FAIL":
			c.Why = "fail then fix"
			open = append(open, OpenFail{Candidate: c, Project: l.Project})
			continue
		case l.Type == "DECISION" && l.Project == "crius" && standardRe.MatchString(l.Text):
			c.Why = "standard set by crius"
		default:
			if c.Why = finding(l.Text); c.Why == "" {
				continue
			}
		}
		c.Text = excerpt(c.Text)
		out = append(out, c)
	}
	var keep []OpenFail
	for _, of := range open {
		if now.Sub(of.Candidate.Time) <= failWait {
			keep = append(keep, of)
		}
	}
	cur.Fails[path] = keep
	cur.Bus[path] = pos
	cur.BusState[path] = st
	return out, n, readErr
}

// readTranscript picks findings from the assistant's text and corrections from
// the owner's turns, skipping ones a past run already picked.
func readTranscript(path string, since time.Time, cur *Cursor, st *ReadStats, now time.Time, projectOf func(string) string) ([]Candidate, error) {
	s, err := transcript.ParseFile(path)
	if err != nil {
		return nil, err
	}
	st.TxBadLines += s.BadLines + s.BadTimes
	team := teamOf(s.Cwd)
	if projectOf != nil {
		if p := projectOf(s.Cwd); p != "" {
			team = p // the projects.toml slug, as on the bus (the bus name, not the repo folder)
		}
	}
	var out []Candidate
	add := func(id, source, why, text string, t time.Time) {
		if t.Before(since) || cur.Seen[id] != "" {
			return
		}
		cur.Seen[id] = now.Format("2006-01-02")
		out = append(out, Candidate{ID: id, Source: source, Team: team, Time: t, Ref: path + "#" + strings.TrimPrefix(id, "tx:"),
			Text: excerpt(text), Why: why})
	}
	type ranked struct {
		m     transcript.Said
		score int
	}
	byDay := map[string][]ranked{}
	var days []string
	for _, m := range s.Says {
		st.TxTexts++
		if m.UUID == "" || m.Time.Before(since) || cur.Seen["tx:"+m.UUID] != "" {
			continue
		}
		if sc := score(m.Text); sc > 0 {
			d := m.Time.Local().Format("2006-01-02")
			if byDay[d] == nil {
				days = append(days, d)
			}
			byDay[d] = append(byDay[d], ranked{m, sc})
		}
	}
	for _, d := range days {
		rs := byDay[d]
		sort.SliceStable(rs, func(i, j int) bool { return rs[i].score > rs[j].score })
		for i, r := range rs {
			if i >= perSessionDay {
				cur.Seen["tx:"+r.m.UUID] = now.Format("2006-01-02") // ranked out: not offered again next night
				st.TxCapped++
				continue
			}
			add("tx:"+r.m.UUID, "transcript", finding(r.m.Text), r.m.Text, r.m.Time)
		}
	}
	for _, t := range s.Turns {
		if s.Headless {
			break // a scripted `claude -p` prompt is a brief, not the owner correcting the agent
		}
		st.OwnerTurns++
		if correctionRe.MatchString(t.Text) {
			text := t.Text
			if t.PrevAssistant != "" {
				text = "OWNER: " + t.Text + "\nAFTER ASSISTANT SAID: " + t.PrevAssistant
			}
			add("tx:"+t.UUID, "owner", "owner correction", text, t.Time)
		}
	}
	return out, nil
}

// teamOf names a session's project from its folder: the folder under Projects,
// past a grouping folder (Appxite, scifi, appxite-github, yolo).
func teamOf(cwd string) string {
	if cwd == "" {
		return "unknown"
	}
	_, rest, ok := strings.Cut(cwd, "/Projects/")
	if !ok {
		return filepath.Base(cwd)
	}
	parts := strings.Split(rest, "/")
	switch strings.ToLower(parts[0]) {
	case "appxite", "scifi", "appxite-github", "yolo":
		if len(parts) > 1 {
			return parts[1]
		}
	}
	return parts[0]
}

// Cursor is where the last run stopped: a byte offset and line-dating state per
// bus log, open FAILs waiting for their fix, the mtime per transcript, and the
// candidate ids already picked (dated by the day they were seen, kept seenDays).
type Cursor struct {
	Bus          map[string]int64       `json:"bus"`
	BusState     map[string]store.State `json:"bus_state,omitempty"`
	Fails        map[string][]OpenFail  `json:"fails,omitempty"`
	Tx           map[string]time.Time   `json:"tx"`
	Seen         map[string]string      `json:"seen"`
	Pending      []Candidate            `json:"pending,omitempty"` // got no model answer; offered again next run
	LastRun      time.Time              `json:"last_run,omitempty"`
	ShownChecked int64                  `json:"shown_checked,omitempty"` // shown.log bytes whose bad lines were already reported
}

const (
	cursorFile = "cursor.json"
	seenDays   = 60
)

func loadCursor(dir string) (*Cursor, error) {
	c := &Cursor{}
	data, err := os.ReadFile(filepath.Join(dir, cursorFile))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		if err := json.Unmarshal(data, c); err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Join(dir, cursorFile), err)
		}
	}
	if c.Bus == nil {
		c.Bus = map[string]int64{}
	}
	if c.BusState == nil {
		c.BusState = map[string]store.State{}
	}
	if c.Fails == nil {
		c.Fails = map[string][]OpenFail{}
	}
	if c.Tx == nil {
		c.Tx = map[string]time.Time{}
	}
	if c.Seen == nil {
		c.Seen = map[string]string{}
	}
	return c, nil
}

func (c *Cursor) save(dir string, now time.Time) error {
	cut := now.AddDate(0, 0, -seenDays).Format("2006-01-02")
	for id, d := range c.Seen {
		if d < cut {
			delete(c.Seen, id)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", " ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, cursorFile), data)
}

// readDoc makes one candidate per paragraph of a lesson file (a heading goes
// with the paragraph after it). Tables and short lines are left out.
func readDoc(path string, cur *Cursor, now time.Time) ([]Candidate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	name := filepath.Base(path)
	team := strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name)))
	var out []Candidate
	var block []string
	start, heading := 0, ""
	flush := func() {
		text := strings.TrimSpace(strings.Join(block, "\n"))
		block = nil
		if len(text) < 60 || strings.HasPrefix(text, "|") {
			return
		}
		if heading != "" {
			text = heading + "\n" + text
		}
		sum := sha256.Sum256([]byte(text))
		id := fmt.Sprintf("doc:%s:%x", name, sum[:6]) // by content, so editing the file elsewhere does not re-seed it
		if cur.Seen[id] != "" {
			return
		}
		cur.Seen[id] = now.Format("2006-01-02")
		out = append(out, Candidate{ID: id, Source: "doc", Team: team, Time: info.ModTime(),
			Ref: fmt.Sprintf("%s:%d", path, start), Text: excerpt(text), Why: "seed"})
	}
	for i, line := range strings.Split(string(data), "\n") {
		switch {
		case strings.HasPrefix(line, "#"):
			flush()
			heading = strings.TrimSpace(strings.TrimLeft(line, "#"))
		case strings.TrimSpace(line) == "":
			flush()
		default:
			if len(block) == 0 {
				start = i + 1
			}
			block = append(block, line)
		}
	}
	flush()
	return out, nil
}

// Started reports whether the nightly cursor holds a read position in the bus
// or the transcripts. A seed run writes a cursor too, but reads neither, so the
// first nightly run after a seed still needs its -since day.
func Started(dir string) (bool, error) {
	if _, err := os.Stat(filepath.Join(dir, cursorFile)); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	c, err := loadCursor(dir)
	if err != nil {
		return false, err
	}
	return len(c.Bus) > 0 || len(c.Tx) > 0, nil
}
