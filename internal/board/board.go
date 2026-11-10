// Package board is a project's work board for the dispatcher: one row per acceptance criterion, with its own id,
// text, the files it may touch, its state and who works on it. It lives in <home>/state/<project>/BOARD.toml beside
// the lead's STATUS.md; every change is one line in BOARD.events.log (the pilot's hours per criterion come from it).
package board

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
)

// idRe is what a row id may be: it becomes part of a session role (launch takes [a-z0-9][a-z0-9-]*).
var idRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*$`)

// The states of a row.
const (
	Open         = "open"          // known, no live check yet
	CheckWritten = "check-written" // its live check is written (by another session than the builder)
	Ready        = "ready"         // may be picked
	Building     = "building"      // a session works on it
	PR           = "pr"            // its PR is open
	Merged       = "merged"        // its PR is merged; the check has not passed on the merged code yet
	Proven       = "proven"        // its live check passed where it is meant to work
	Blocked      = "blocked"       // waits on a question; goes back to the state it came from
)

// moves is what each state may go to. An AC that is already built is proven by its live check alone (ready or
// building -> proven); merged or proven sent back to building is a reopen; a PR sent back by review or CI is not.
var moves = map[string][]string{
	Open:         {CheckWritten, Blocked},
	CheckWritten: {Ready, Blocked},
	Ready:        {Building, Proven, Blocked},
	Building:     {PR, Proven, Blocked},
	PR:           {Merged, Building, Blocked},
	Merged:       {Proven, Building, Blocked},
	Proven:       {Building},
}

// Row is one criterion.
type Row struct {
	ID       string     `toml:"id"`
	Text     string     `toml:"text"`
	Story    string     `toml:"story,omitempty"`
	Mockup   string     `toml:"mockup,omitempty"`
	Files    []string   `toml:"files,omitempty"` // globs it may touch; none means it may touch anything
	State    string     `toml:"state"`
	Check    string     `toml:"check,omitempty"`    // the live check, which the builder may not edit
	Session  string     `toml:"session,omitempty"`  // bus name of the session on it
	PR       int        `toml:"pr,omitempty"`       // its PR
	Question string     `toml:"question,omitempty"` // blocked: what it waits on
	From     string     `toml:"from,omitempty"`     // blocked: the state it came from
	Reopens  int        `toml:"reopens,omitempty"`
	Restarts int        `toml:"restarts,omitempty"` // times its session was started again after it ended with no result
	Dead     *time.Time `toml:"dead,omitempty"`     // building: when its session first read ended (the dispatcher waits a grace before a restart)
	Opened   time.Time  `toml:"opened"`
	Updated  time.Time  `toml:"updated"`
}

// Board is every row of one project, in the order they were added.
type Board struct {
	Rows   []Row `toml:"row"`
	events []string
}

// EventsPath is the event log beside a board.
func EventsPath(path string) string {
	return strings.TrimSuffix(path, filepath.Ext(path)) + ".events.log"
}

// Load reads a board; a board not written yet is empty. One that cannot be read, is empty, or has keys the board does
// not know is an error: never an empty board, or rows short of a field, that the next write would save over it.
func Load(path string) (Board, error) {
	b, _, err := load(path)
	return b, err
}

func load(path string) (Board, []byte, error) {
	var b Board
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return b, nil, nil
	}
	if err != nil {
		return b, nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return Board{}, nil, fmt.Errorf("reading %s: the file is empty", path)
	}
	md, err := toml.Decode(string(data), &b)
	if err != nil {
		return Board{}, nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if u := md.Undecoded(); len(u) > 0 {
		return Board{}, nil, fmt.Errorf("reading %s: keys the board does not know: %v", path, u)
	}
	return b, data, nil
}

// Update changes a board under its OS lock: read, fn, write whole (a new file synced, then renamed over the old), then
// the event lines. An error from fn writes nothing, and so does a change that leaves the board as it was.
func Update(path string, now time.Time, fn func(*Board) error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("locking %s: %w", path, err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	b, old, err := load(path)
	if err != nil {
		return err
	}
	if err := fn(&b); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(b); err != nil {
		return err
	}
	if bytes.Equal(buf.Bytes(), old) && len(b.events) == 0 {
		return nil
	}
	tmp := path + ".new"
	if err := writeSynced(tmp, buf.Bytes()); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if len(b.events) == 0 {
		return nil
	}
	f, err := os.OpenFile(EventsPath(path), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("the board changed, but its event log cannot be opened: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(strings.Join(b.events, "\n") + "\n"); err != nil {
		return fmt.Errorf("the board changed, but its event log was not written: %w", err)
	}
	return nil
}

// Add puts a new row on the board in state open.
func (b *Board) Add(r Row, now time.Time) error {
	if !idRe.MatchString(r.ID) {
		return fmt.Errorf("a row id is letters, digits and dashes, starting with a letter or digit (it becomes a session role), got %q", r.ID)
	}
	if strings.TrimSpace(r.Text) == "" {
		return fmt.Errorf("%s: no text", r.ID)
	}
	if i := slices.IndexFunc(b.Rows, func(o Row) bool { return strings.EqualFold(o.ID, r.ID) }); i >= 0 {
		return fmt.Errorf("%s is on the board already (as %s; ids differ by more than case)", r.ID, b.Rows[i].ID)
	}
	r.State, r.Opened, r.Updated = Open, now, now
	b.Rows = append(b.Rows, r)
	b.event(now, r.ID, "-", Open, "add", "")
	return nil
}

// Move takes row id to state to, after set (when not nil) has filled what the move needs (a session, a PR, the
// question of a block). A move the table does not allow is refused and changes nothing.
func (b *Board) Move(id, to, by, why string, now time.Time, set func(*Row)) error {
	if _, ok := moves[to]; !ok && to != Blocked {
		return fmt.Errorf("no state %q (states: %s)", to, strings.Join(states(), ", "))
	}
	i := b.find(id)
	if i < 0 {
		return fmt.Errorf("no row %s", id)
	}
	r := b.Rows[i]
	from := r.State
	allowed := moves[from]
	if from == Blocked {
		allowed = []string{r.From}
	}
	if !slices.Contains(allowed, to) {
		return fmt.Errorf("%s: %s -> %s is not allowed (from %s: %s)", id, from, to, from, strings.Join(allowed, ", "))
	}
	if to == Building {
		for _, o := range b.Rows {
			if o.ID != id && holds(o) && Overlap(r.Files, o.Files) {
				return fmt.Errorf("%s: its files overlap %s (%s); it waits until that is done", id, o.ID, o.State)
			}
		}
	}
	if set != nil {
		set(&r)
	}
	old := b.Rows[i]
	r.ID, r.Opened, r.Reopens, r.From = old.ID, old.Opened, old.Reopens, old.From // Move owns these
	if to != Blocked {
		r.Question = "" // a question lives only on a blocked row
	}
	switch {
	case to == Blocked:
		if strings.TrimSpace(r.Question) == "" {
			return fmt.Errorf("%s: blocked needs the question it waits on", id)
		}
		r.From = from
	case from == Blocked:
		r.From = ""
	case to == Building && (from == Merged || from == Proven):
		r.Reopens++
		r.PR = 0 // the next pr move names the new PR
		r.Restarts, r.Dead = 0, nil
	}
	if to == PR && r.PR <= 0 {
		return fmt.Errorf("%s: pr needs its PR number", id)
	}
	r.State, r.Updated = to, now
	b.Rows[i] = r
	b.event(now, id, from, to, by, why)
	return nil
}

// Note changes a row's fields with no change of state (a restart), as one event line.
func (b *Board) Note(id, by, why string, now time.Time, set func(*Row)) error {
	i := b.find(id)
	if i < 0 {
		return fmt.Errorf("no row %s", id)
	}
	r := b.Rows[i]
	set(&r)
	r.ID, r.State, r.From, r.Updated = b.Rows[i].ID, b.Rows[i].State, b.Rows[i].From, now
	b.Rows[i] = r
	b.event(now, id, r.State, r.State, by, why)
	return nil
}

// Next is the oldest ready row whose files overlap no row being built, in a PR, or blocked from one of those (its
// session still holds the files); false when none is ready and free.
func (b *Board) Next() (Row, bool) {
	var held [][]string
	for _, r := range b.Rows {
		if holds(r) {
			held = append(held, r.Files)
		}
	}
	var best *Row
	for i := range b.Rows {
		r := &b.Rows[i]
		if r.State != Ready || best != nil && !r.Opened.Before(best.Opened) {
			continue
		}
		if !slices.ContainsFunc(held, func(f []string) bool { return Overlap(r.Files, f) }) {
			best = r
		}
	}
	if best == nil {
		return Row{}, false
	}
	return *best, true
}

// holds says whether a row's session holds its files: building, in a PR, or blocked from one of those.
func holds(r Row) bool {
	return r.State == Building || r.State == PR || r.State == Blocked && (r.From == Building || r.From == PR)
}

// Overlap says whether two sets of file globs may name one file. It errs toward yes. Paths are compared cleaned
// (no ./ or / in front, no / at the end) and without case (the Mac's disk ignores case). A path with no wildcard is
// that file or everything under it (a folder); one with a wildcard (* ? [ {) is compared by its literal part before
// the wildcard: two overlap when either starts the other. A set that names no files may touch anything.
func Overlap(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return true
	}
	for _, x := range a {
		for _, y := range b {
			px, wx := literal(x)
			py, wy := literal(y)
			if !wx && !wy {
				if px == py || strings.HasPrefix(py, px+"/") || strings.HasPrefix(px, py+"/") || px == "" || py == "" {
					return true
				}
			} else if strings.HasPrefix(py, px) || strings.HasPrefix(px, py) {
				return true
			}
		}
	}
	return false
}

// literal is a cleaned glob's part before its first wildcard, and whether it has one.
func literal(glob string) (string, bool) {
	g := strings.ToLower(strings.TrimSpace(glob))
	wild := strings.IndexAny(g, "*?[{")
	if wild < 0 {
		g = strings.Trim(path.Clean("/"+g), "/")
		return g, false
	}
	// clean only the part before the wildcard; keep a / that ends it (src/a/** is under src/a, not src/ab)
	head, slash := g[:wild], strings.HasSuffix(g[:wild], "/")
	head = strings.Trim(path.Clean("/"+head), "/")
	if slash && head != "" {
		head += "/"
	}
	return head, true
}

func (b *Board) find(id string) int {
	return slices.IndexFunc(b.Rows, func(r Row) bool { return r.ID == id })
}

// writeSynced writes a file and syncs it to disk before it is renamed over the board (a power cut never leaves an
// empty board in its place).
func writeSynced(name string, data []byte) error {
	f, err := os.OpenFile(name, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func (b *Board) event(now time.Time, id, from, to, by, why string) {
	by = strings.Join(strings.Fields(by), "_") // one word, so a line cannot be split or forged
	line := fmt.Sprintf("%s %s %s -> %s by %s", now.Format(time.RFC3339), id, from, to, by)
	if why = strings.Join(strings.Fields(why), " "); why != "" {
		line += ": " + why
	}
	b.events = append(b.events, line)
}

func states() []string {
	return []string{Open, CheckWritten, Ready, Building, PR, Merged, Proven, Blocked}
}
