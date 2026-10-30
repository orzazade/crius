// Package ownerqueue is the one list of what waits on the owner (owner 10-30: "visually I lose what you expect from
// me"): home/state/owner-queue.jsonl, one JSON row per item, oldest first. The owner-queue mod draws it beside the Crius
// chat and crius-queue adds, answers and lists rows.
//
// Every NEED-OWNER line on a bus log (home/bus/*.log) is a row by itself: each read of the queue first reads what the
// logs gained since the last read (sync), so an empty list means nothing waits on the owner, never that Crius forgot to
// write a row. A line from Crius (DECISION, INFO, OWNER or ANSWER) whose text opens with "[queue qN]" answers row qN (a
// tag further on is a quote and answers nothing); a NEED-OWNER line with the tag is the row Crius already added. Lines
// dated before the queue's start (init) are history.
//
// A NEED-OWNER text may carry its parts as "why: …", "unblocks: …" and "options: A | B"; the rest is what to do, and
// its first clause the title.
//
// Every read and write holds an OS lock (home/state/owner-queue.lock) and replaces the file whole, so a pane, the CLI
// and Crius never lose each other's rows.
package ownerqueue

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

const (
	Open     = "open"
	Answered = "answered"
	stamp    = "2006-01-02T15:04"
)

var (
	tagRe = regexp.MustCompile(`\[queue (q\d+)\]`)
	// "[- ]2026-10-30T13:40 TYPE sender -> to — text": an optional list mark and date (T or space), then HH:MM.
	headRe = regexp.MustCompile(`^(?:[-*] )?(?:(\d{4}-\d\d-\d\d)[T ])?(\d\d:\d\d)(Z?) ([A-Z][A-Z-]*) (\S+)(?: (?:->|→) ([^—]*?))?(?: — (.*))?$`)
	partRe = regexp.MustCompile(`(?i)(?:^|[\s.;,])(why|unblocks|options):\s*`)
	// Crius's own senders: a line from them may answer a row.
	crius      = map[string]bool{"crius": true, "crius:lead": true}
	answerType = map[string]bool{"DECISION": true, "INFO": true, "OWNER": true, "ANSWER": true}
)

// Row is one item. Log and Line name the bus line it came from ("" for a row Crius added with Add).
type Row struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Do         string   `json:"do,omitempty"`
	Why        string   `json:"why,omitempty"`
	Unblocks   string   `json:"unblocks,omitempty"`
	Options    []string `json:"options,omitempty"`
	From       string   `json:"from"`
	Opened     string   `json:"opened"` // local time, 2006-01-02T15:04; "" for an undated bus line
	State      string   `json:"state"`
	Answer     string   `json:"answer,omitempty"`
	AnsweredAt string   `json:"answeredAt,omitempty"`
	AnsweredBy string   `json:"answeredBy,omitempty"`
	Log        string   `json:"log,omitempty"`
	Line       string   `json:"line,omitempty"`
}

// Queue is every row, in the order they were added.
type Queue struct{ Rows []Row }

// syncState is where the queue starts and how far each bus log was read (bytes, and the file's inode: a log replaced
// by another file is read again from the start; rows already there are not added twice).
type syncState struct {
	Since string               `json:"since"`
	Logs  map[string]logCursor `json:"logs"`
}

type logCursor struct {
	Offset int64  `json:"offset"`
	Ino    uint64 `json:"ino"`
}

func paths(home string) (rows, cursor, lock string) {
	d := filepath.Join(home, "state")
	return filepath.Join(d, "owner-queue.jsonl"), filepath.Join(d, "owner-queue.sync.json"), filepath.Join(d, "owner-queue.lock")
}

// Init sets the queue's start: NEED-OWNER lines dated before since are history ("" takes every line, undated ones too).
// The bus logs are read again from their start at the next sync. Rows already in the queue stay.
func Init(home, since string) error {
	if since != "" {
		if _, err := time.ParseInLocation(stamp, since, time.Local); err != nil {
			return fmt.Errorf("since %q is not YYYY-MM-DDTHH:MM", since)
		}
	}
	return locked(home, func() error {
		_, cur, _ := paths(home)
		return writeJSON(cur, syncState{Since: since, Logs: map[string]logCursor{}})
	})
}

// List syncs the bus logs into the queue and returns it.
func List(home string, now time.Time) (*Queue, error) {
	var q *Queue
	err := locked(home, func() (err error) {
		q, err = syncLocked(home)
		return err
	})
	return q, err
}

// Add adds a row Crius writes itself and returns its id. Title and From are needed.
func Add(home string, now time.Time, r Row) (string, error) {
	if strings.TrimSpace(r.Title) == "" || strings.TrimSpace(r.From) == "" {
		return "", errors.New("a row needs a title and a from")
	}
	var id string
	err := locked(home, func() error {
		q, err := syncLocked(home)
		if err != nil {
			return err
		}
		r.ID, r.State, r.Opened, r.Log, r.Line = next(q), Open, now.Format(stamp), "", ""
		q.Rows = append(q.Rows, r)
		id = r.ID
		return save(home, q)
	})
	return id, err
}

// Answer records text as row id's answer. A row already answered is refused: two presses never answer twice.
func Answer(home string, now time.Time, id, text, by string) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("an answer needs text")
	}
	return locked(home, func() error {
		q, err := syncLocked(home)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(q.Rows, func(r Row) bool { return r.ID == id })
		if i < 0 {
			return fmt.Errorf("no row %s", id)
		}
		if q.Rows[i].State == Answered {
			return fmt.Errorf("%s is already answered (%q at %s)", id, q.Rows[i].Answer, q.Rows[i].AnsweredAt)
		}
		answer(&q.Rows[i], text, now.Format(stamp), by)
		return save(home, q)
	})
}

// OpenRows are the rows still open, oldest first (undated ones first: they are the oldest).
func (q *Queue) OpenRows() []Row {
	var out []Row
	for _, r := range q.Rows {
		if r.State == Open {
			out = append(out, r)
		}
	}
	slices.SortStableFunc(out, func(a, b Row) int { return strings.Compare(a.Opened, b.Opened) })
	return out
}

// AnsweredOn are the rows answered on now's day, newest first.
func (q *Queue) AnsweredOn(now time.Time) []Row {
	day := now.Format("2006-01-02")
	var out []Row
	for _, r := range q.Rows {
		if r.State == Answered && strings.HasPrefix(r.AnsweredAt, day) {
			out = append(out, r)
		}
	}
	slices.SortStableFunc(out, func(a, b Row) int { return strings.Compare(b.AnsweredAt, a.AnsweredAt) })
	return out
}

func answer(r *Row, text, at, by string) {
	r.State, r.Answer, r.AnsweredAt, r.AnsweredBy = Answered, strings.TrimSpace(text), at, by
}

func next(q *Queue) string {
	n := 0
	for _, r := range q.Rows {
		if v, err := strconv.Atoi(strings.TrimPrefix(r.ID, "q")); err == nil && v > n {
			n = v
		}
	}
	return "q" + strconv.Itoa(n+1)
}

// syncLocked reads the rows and every bus log's new whole lines: first every new NEED-OWNER row, then every answer,
// so an answer on one log finds a row from another log of the same read.
func syncLocked(home string) (*Queue, error) {
	rowsPath, curPath, _ := paths(home)
	var cur syncState
	b, err := os.ReadFile(curPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("the owner queue was never set up (%s is missing): run crius-queue init", curPath)
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &cur); err != nil {
		return nil, fmt.Errorf("%s: %w", curPath, err)
	}
	if cur.Logs == nil {
		cur.Logs = map[string]logCursor{}
	}
	if _, err := os.Stat(rowsPath); errors.Is(err, os.ErrNotExist) {
		for _, c := range cur.Logs {
			if c.Offset > 0 { // the bus was read, so rows were saved: new ids would start again at q1
				return nil, fmt.Errorf("%s is missing but the bus was read already: the rows are lost; restore the file"+
					" (crius-queue init starts ids again at q1, and old [queue qN] lines would close new rows)", rowsPath)
			}
		}
	}
	q, err := load(rowsPath)
	if err != nil {
		return nil, err
	}
	logs, err := filepath.Glob(filepath.Join(home, "bus", "*.log"))
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, r := range q.Rows {
		if r.Log != "" {
			have[key(r.Log, r.Line)] = true
		}
	}
	type answerLine struct{ id, text, at, by string }
	var answers []answerLine
	changed := false
	for _, l := range logs {
		name := filepath.Base(l)
		lines, c, err := readNew(l, cur.Logs[name])
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", l, err)
		}
		if c != cur.Logs[name] {
			cur.Logs[name], changed = c, true
		}
		for _, line := range lines {
			m := headRe.FindStringSubmatch(strings.TrimSuffix(line, "\r"))
			if m == nil {
				continue
			}
			date, hm, utc, typ, sender, text := m[1], m[2], m[3], m[4], m[5], m[7]
			at := ""
			if date != "" {
				at = date + "T" + hm
			}
			if at != "" && utc == "Z" { // a UTC stamp: the queue keeps local time, like the bus
				if t, err := time.ParseInLocation(stamp, at, time.UTC); err == nil {
					at = t.In(time.Local).Format(stamp)
				}
			}
			tag := tagRe.FindStringSubmatch(text)
			switch {
			case typ == "NEED-OWNER" && tag == nil:
				if have[key(name, line)] || cur.Since != "" && (at == "" || at < cur.Since) {
					continue
				}
				have[key(name, line)] = true
				r := parse(text)
				r.ID, r.State, r.Opened, r.From, r.Log, r.Line = next(q), Open, at, from(sender), name, line
				q.Rows = append(q.Rows, r)
				changed = true
			case tag != nil && strings.HasPrefix(text, tag[0]) && crius[sender] && answerType[typ]: // the tag opens the line
				rest := strings.TrimLeft(text[len(tag[0]):], " :·—-")
				if rest == "" {
					rest = typ
				}
				answers = append(answers, answerLine{tag[1], rest, at, sender})
			}
		}
	}
	for _, a := range answers {
		for i := range q.Rows {
			if q.Rows[i].ID == a.id && q.Rows[i].State == Open {
				answer(&q.Rows[i], a.text, a.at, a.by)
				changed = true
			}
		}
	}
	if !changed {
		return q, nil
	}
	if err := save(home, q); err != nil {
		return nil, err
	}
	return q, writeJSON(curPath, cur)
}

// readNew reads the whole lines l gained after c; a line still being written waits for its newline.
func readNew(l string, c logCursor) ([]string, logCursor, error) {
	f, err := os.Open(l)
	if err != nil {
		return nil, c, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, c, err
	}
	var ino uint64
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		ino = st.Ino
	}
	if ino != c.Ino || fi.Size() < c.Offset { // another file, or cut: read it again; rows already there stay single
		c = logCursor{Ino: ino}
	}
	if fi.Size() == c.Offset {
		return nil, c, nil
	}
	if _, err := f.Seek(c.Offset, io.SeekStart); err != nil {
		return nil, c, err
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, c, err
	}
	end := bytes.LastIndexByte(b, '\n')
	if end < 0 {
		return nil, c, nil
	}
	c.Offset += int64(end + 1)
	return strings.Split(string(b[:end]), "\n"), c, nil
}

// parse splits a NEED-OWNER text into its parts.
func parse(text string) Row {
	text = strings.TrimSpace(text)
	var r Row
	locs := partRe.FindAllStringSubmatchIndex(text, -1)
	do := text
	if len(locs) > 0 {
		do = text[:locs[0][0]]
	}
	for i, l := range locs {
		end := len(text)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		v := strings.TrimSpace(text[l[1]:end])
		switch strings.ToLower(text[l[2]:l[3]]) {
		case "why":
			r.Why = v
		case "unblocks":
			r.Unblocks = v
		case "options":
			for _, o := range strings.Split(strings.TrimRight(v, "."), "|") {
				if o = strings.TrimSpace(o); o != "" {
					r.Options = append(r.Options, o)
				}
			}
		}
	}
	// "Options: (A, my pick) keep exit 0 …" in prose is no button list: a button holds a few words.
	if slices.ContainsFunc(r.Options, func(o string) bool { return utf8.RuneCountInString(o) > 40 }) {
		return Row{Do: text, Title: title(text)}
	}
	r.Do = strings.TrimSpace(do)
	r.Title = title(r.Do)
	if r.Title == "" {
		r.Title = title(text)
	}
	return r
}

// title is the first clause of s, at most 72 runes: it ends at the first ". ", "; ", "? " or " — " outside brackets,
// or at ": " after at least 16 runes, so a label ("What: …", "4th: …") is not the whole title. A leading "What:" goes.
func title(s string) string {
	t := strings.TrimSpace(s)
	if l, rest, ok := strings.Cut(t, ": "); ok && strings.EqualFold(l, "what") {
		t = rest
	}
	depth := 0
	for i, r := range t {
		switch r {
		case '(', '[':
			depth++
			continue
		case ')', ']':
			depth = max(0, depth-1)
			continue
		}
		if depth > 0 {
			continue
		}
		rest := t[i:]
		if strings.HasPrefix(rest, ". ") || strings.HasPrefix(rest, "; ") || strings.HasPrefix(rest, " — ") ||
			strings.HasPrefix(rest, ": ") && utf8.RuneCountInString(t[:i]) >= 16 {
			t = t[:i]
			break
		}
		if strings.HasPrefix(rest, "? ") {
			t = t[:i+1]
			break
		}
	}
	t = strings.TrimRight(strings.TrimSpace(t), ".;:")
	if r := []rune(t); len(r) > 72 {
		t = strings.TrimSpace(string(r[:71])) + "…"
	}
	return t
}

// from is who asks: the project of a lead ("upload-central:lead" -> upload-central), Crius for Crius's own lines.
func from(sender string) string {
	p, _, _ := strings.Cut(sender, ":")
	if p == "crius" {
		return "Crius"
	}
	return p
}

func key(log, line string) string {
	h := sha256.Sum256([]byte(log + "\n" + line))
	return hex.EncodeToString(h[:12])
}

func load(path string) (*Queue, error) {
	q := &Queue{}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return q, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for n := 1; s.Scan(); n++ {
		if len(bytes.TrimSpace(s.Bytes())) == 0 {
			continue
		}
		var r Row
		if err := json.Unmarshal(s.Bytes(), &r); err != nil {
			return nil, fmt.Errorf("%s line %d: %w", path, n, err)
		}
		q.Rows = append(q.Rows, r)
	}
	return q, s.Err()
}

func save(home string, q *Queue) error {
	rows, _, _ := paths(home)
	var b bytes.Buffer
	for _, r := range q.Rows {
		line, err := json.Marshal(r)
		if err != nil {
			return err
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return replace(rows, b.Bytes())
}

func writeJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return replace(path, append(b, '\n'))
}

// replace writes path whole through a temp file and a rename, so a reader never sees half a file.
func replace(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".")
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
	}
	return werr
}

// locked runs fn holding the queue's OS lock; the kernel drops it if the process dies.
func locked(home string, fn func() error) error {
	_, _, lock := paths(home)
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("locking %s: %w", lock, err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}
