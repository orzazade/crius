package playbook

import (
	"bufio"
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
	Errors              []string
}

func (s ReadStats) String() string {
	return fmt.Sprintf("bus %d files %d new lines; transcripts %d read %d unchanged, %d assistant texts (%d past the cap) %d owner turns; %d read errors",
		s.BusFiles, s.BusLines, s.TxFiles, s.TxSkipped, s.TxTexts, s.TxCapped, s.OwnerTurns, len(s.Errors))
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
func Gather(cfg Config, cur *Cursor) ([]Candidate, ReadStats, error) {
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
			cs, n, err := readBus(p, cfg.Since, cur)
			st.BusFiles++
			st.BusLines += n
			if err != nil {
				st.Errors = append(st.Errors, err.Error())
			}
			out = append(out, cs...)
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
			if mod.Before(cfg.Since) || !mod.After(cur.Tx[p]) {
				st.TxSkipped++
				continue
			}
			cs, err := readTranscript(p, cfg.Since, cur, &st)
			st.TxFiles++
			if err != nil {
				st.Errors = append(st.Errors, err.Error())
				continue
			}
			cur.Tx[p] = mod
			out = append(out, cs...)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	if len(st.Errors) > 0 && st.BusLines == 0 && st.TxFiles == 0 {
		return out, st, fmt.Errorf("read nothing: %s", strings.Join(st.Errors, "; "))
	}
	return out, st, nil
}

// readBus reads one log from the cursor's offset. A file shorter than the offset
// was rewritten, so it is read again from the start.
func readBus(path string, since time.Time, cur *Cursor) ([]Candidate, int, error) {
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
	if off > info.Size() {
		off = 0
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, 0, err
	}
	name := filepath.Base(path)
	var lines []*store.Line
	var offs []int64
	st := &store.State{}
	r := bufio.NewReaderSize(f, 1<<16)
	pos := off
	n := 0
	for {
		raw, err := r.ReadString('\n')
		if err != nil && !strings.HasSuffix(raw, "\n") {
			break // a half-written last line is read next time
		}
		at := pos
		pos += int64(len(raw))
		n++
		if l := store.ParseLine(raw, st, false); l != nil {
			lines = append(lines, l)
			offs = append(offs, at)
		}
	}
	cur.Bus[path] = pos
	var out []Candidate
	for i, l := range lines {
		t, err := time.ParseInLocation("2006-01-02T15:04", l.Date+"T"+l.Time, time.Local)
		if err != nil || t.Before(since) {
			continue
		}
		team := l.Project
		if team == "" {
			team = strings.TrimSuffix(name, ".log")
		}
		c := Candidate{ID: fmt.Sprintf("bus:%s:%d", name, offs[i]), Source: "bus", Team: team, Time: t,
			Ref: fmt.Sprintf("bus %s %sT%s", name, l.Date, l.Time), Text: l.Type + " " + l.Project + ":" + l.Role + " — " + l.Text}
		switch {
		case l.Type == "LEARNED":
			c.Why = "LEARNED line"
		case l.Type == "CORRECTED" || l.Type == "CORRECTION":
			c.Why = "correction"
		case l.Type == "FAIL":
			fix := pairFix(lines[i+1:], l, t)
			if fix == nil {
				continue
			}
			c.Why = "fail then fix"
			c.Text += "\nTHEN " + fix.Type + " " + fix.Project + ":" + fix.Role + " — " + fix.Text
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
	return out, n, nil
}

// pairFix finds the first later line from the same project, within a day, that names a fix.
func pairFix(rest []*store.Line, fail *store.Line, at time.Time) *store.Line {
	for _, l := range rest {
		t, err := time.ParseInLocation("2006-01-02T15:04", l.Date+"T"+l.Time, time.Local)
		if err != nil || t.Sub(at) > 24*time.Hour {
			return nil
		}
		if l.Project == fail.Project && l.Type != "FAIL" && fixRe.MatchString(l.Text) {
			return l
		}
	}
	return nil
}

// readTranscript picks findings from the assistant's text and corrections from
// the owner's turns, skipping ones a past run already picked.
func readTranscript(path string, since time.Time, cur *Cursor, st *ReadStats) ([]Candidate, error) {
	s, err := transcript.ParseFile(path)
	if err != nil {
		return nil, err
	}
	team := teamOf(s.Cwd)
	var out []Candidate
	add := func(id, source, why, text string, t time.Time) {
		if t.Before(since) || cur.Seen[id] != "" {
			return
		}
		cur.Seen[id] = t.Local().Format("2006-01-02")
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
				cur.Seen["tx:"+r.m.UUID] = d // ranked out: not offered again next night
				st.TxCapped++
				continue
			}
			add("tx:"+r.m.UUID, "transcript", finding(r.m.Text), r.m.Text, r.m.Time)
		}
	}
	for _, t := range s.Turns {
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

// Cursor is where the last run stopped: a byte offset per bus log, the mtime per
// transcript, and the candidate ids already picked (kept 60 days).
type Cursor struct {
	Bus  map[string]int64     `json:"bus"`
	Tx   map[string]time.Time `json:"tx"`
	Seen map[string]string    `json:"seen"`
}

const cursorFile = "cursor.json"

func loadCursor(dir string) (*Cursor, error) {
	c := &Cursor{Bus: map[string]int64{}, Tx: map[string]time.Time{}, Seen: map[string]string{}}
	data, err := os.ReadFile(filepath.Join(dir, cursorFile))
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Join(dir, cursorFile), err)
	}
	if c.Bus == nil {
		c.Bus = map[string]int64{}
	}
	if c.Tx == nil {
		c.Tx = map[string]time.Time{}
	}
	if c.Seen == nil {
		c.Seen = map[string]string{}
	}
	return c, nil
}

func (c *Cursor) save(dir string) error {
	cut := time.Now().AddDate(0, 0, -60).Format("2006-01-02")
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
