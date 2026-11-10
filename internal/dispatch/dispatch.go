// Package dispatch is the dispatcher's one run: it reads a project's board and bus, starts a fresh session for each
// ready criterion up to a cap, and moves rows on what their sessions and PRs do. No model runs in it.
//
// One run: read the PR states (no lock held); then, under the board's lock, apply them, apply the bus lines after the
// cursor, look at the building sessions, claim the rows to start (each moved to building with its session before any
// tab opens), and write the board and the cursor; then, with no lock held, open the tabs and post. A spawn that fails
// or half-works has claimed its row already, so no later run opens a second tab for it: the dead-session rule gives it
// one restart and then blocks it. A run cut off before its spawns leaves claimed rows whose sessions read dead, which
// the same rule handles.
//
// <home>/state/<project>/DISPATCH.toml:
//
//	cap = 3                      # rows building at once (rows in pr or merged keep their sessions and are not counted)
//	prefix = "pilot-"            # a row's role is prefix + its id in lower case (AC12 -> pilot-ac12)
//	prompt = "Your criterion: {id}. Read ~/.crius/state/<project>/PILOT-BRIEF.md first and follow it."
package dispatch

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/orzazade/crius/internal/board"
)

// Deps is what one run needs. Spawn starts a session for a role with a first prompt, Close marks a role's session
// closed on purpose (crius-spawn -close; it does not end the Claude), Alive says whether a role's session runs, PRState
// gives a PR's status ("active", "completed", "abandoned"), Post writes a bus line from crius:dispatch on the project's
// bus.
type Deps struct {
	Home, Project string
	Now           time.Time
	Spawn         func(role, prompt string) error
	Close         func(role string) error
	Alive         func(role string) (bool, error)
	PRState       func(pr int) (string, error)
	Post          func(typ, to, text string) error
}

type config struct {
	Cap    int    `toml:"cap"`
	Prefix string `toml:"prefix"`
	Prompt string `toml:"prompt"`
}

const (
	lead      = "crius:lead"
	self      = "crius:dispatch"
	restarted = " Your last session ended: read your log and go on."
	// grace is how long a building session may read dead before it is started again: crius-spawn -move ends a
	// session and resumes it on another account in that time.
	grace = 2 * time.Minute
)

var (
	lineRe = regexp.MustCompile(`^\S+ ([A-Z][A-Z-]*) (\S+) (?:->|→) (.*?) — (.*)$`)
	prRe   = regexp.MustCompile(`(?i)\bPR #?(\d+)\b`)
	partRe = regexp.MustCompile(`\bPARTIAL\b`) // capitals only: "partial refunds" is product text
)

type post struct{ typ, to, text string }

type spawn struct{ id, role, prompt string }

// Run is one dispatcher run.
func Run(d Deps) error {
	dir := filepath.Join(d.Home, "state", d.Project)
	var cfg config
	if _, err := toml.DecodeFile(filepath.Join(dir, "DISPATCH.toml"), &cfg); err != nil {
		return fmt.Errorf("reading the dispatch config: %w", err)
	}
	if cfg.Cap < 1 || cfg.Prefix == "" || !strings.Contains(cfg.Prompt, "{id}") {
		return fmt.Errorf("DISPATCH.toml needs cap >= 1, a prefix and a prompt with {id}")
	}
	boardPath := filepath.Join(dir, "BOARD.toml")
	session := func(id string) string { return d.Project + ":" + role(cfg, id) }
	answerers := map[string]bool{lead: true, d.Project + ":lead": true}

	// 0. the PR states, read with no lock held (az may take a minute); applied below only to a row still on that PR
	b0, err := board.Load(boardPath)
	if err != nil {
		return err
	}
	var errs []error
	prs := map[int]string{}
	for _, r := range b0.Rows {
		if r.State != board.PR {
			continue
		}
		st, err := d.PRState(r.PR)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s PR %d: %w", r.ID, r.PR, err))
			continue
		}
		prs[r.PR] = st
	}

	var posts []post
	var spawns []spawn
	var closes []string // sessions to close, after the board is saved
	upto := int64(-1)
	err = board.Update(boardPath, d.Now, func(b *board.Board) error {
		lines, n, err := newLines(filepath.Join(d.Home, "bus", d.Project+".log"), filepath.Join(dir, "dispatch.cursor"))
		if err != nil {
			return err
		}
		upto = n
		bySession := map[string]string{}
		for _, r := range b.Rows {
			bySession[session(r.ID)] = r.ID
		}
		state := func(id string) board.Row {
			for _, r := range b.Rows {
				if r.ID == id {
					return r
				}
			}
			return board.Row{}
		}
		move := func(id, to, why string, set func(*board.Row)) bool {
			if err := b.Move(id, to, self, why, d.Now, set); err != nil {
				errs = append(errs, err)
				return false
			}
			return true
		}
		closeRole := func(r board.Row) {
			if r.Session == "" {
				return // proven by its check alone: no session was started
			}
			closes = append(closes, r.ID)
		}
		// 1. the PRs (before the bus, so a VERIFIED in the minute of its merge finds the row merged)
		for _, r := range b.Rows {
			st, ok := prs[r.PR]
			if r.State != board.PR || !ok {
				continue
			}
			switch st {
			case "completed":
				move(r.ID, board.Merged, fmt.Sprintf("PR %d merged", r.PR), nil)
			case "abandoned":
				pr := r.PR
				if move(r.ID, board.Building, fmt.Sprintf("PR %d abandoned", pr), func(r *board.Row) { r.PR = 0 }) {
					posts = append(posts, post{"INFO", session(r.ID), fmt.Sprintf("%s: PR %d was abandoned; the row is back to building", r.ID, pr)})
				}
			}
		}
		// 2. what the sessions, Crius and the project's lead said
		for _, l := range lines {
			m := lineRe.FindStringSubmatch(l)
			if m == nil {
				continue
			}
			typ, sender, to, text := m[1], m[2], recipients(m[3]), m[4]
			if id, ok := bySession[sender]; ok {
				r := state(id)
				working := r.State == board.Building || r.State == board.Blocked && r.From == board.Building
				switch {
				case (typ == "NEED" || typ == "ASK" || typ == "BLOCKED" || strings.HasPrefix(typ, "NEED-")) &&
					(r.State == board.Building || r.State == board.PR):
					move(id, board.Blocked, typ+" from its session", func(r *board.Row) { r.Question = text })
				case typ == "DONE" && r.State == board.Building && partRe.MatchString(text):
					move(id, board.Blocked, "its session ended partial", func(r *board.Row) { r.Question = text })
				case typ == "DONE" && working && len(prNumbers(text)) > 1:
					ns := prNumbers(text)
					posts = append(posts, post{"NEED", lead, fmt.Sprintf("%s: its DONE names PR %s; set the right one with crius-board %s set %s pr --pr <n>", id, strings.Join(ns, " and PR "), d.Project, id)})
				case typ == "DONE" && working && prRe.MatchString(text):
					if r.State == board.Blocked && !move(id, board.Building, "its session went on and opened its PR", nil) {
						continue
					}
					n, _ := strconv.Atoi(prRe.FindStringSubmatch(text)[1])
					move(id, board.PR, "its session opened the PR", func(r *board.Row) { r.PR = n })
				}
				continue
			}
			if sender == self || len(to) == 0 {
				continue
			}
			// an answer counts only when it is sent straight to the session (not a cc) by Crius or the project's lead
			if id, ok := bySession[to[0]]; ok && answerers[sender] && (typ == "DECISION" || typ == "ANSWER" || typ == "ORDER") {
				if r := state(id); r.State == board.Blocked {
					// the answer is new: a fresh restart, and no grace (a dead session is started again in step 3 of this run)
					long := d.Now.Add(-grace)
					move(id, r.From, sender+" answered", func(r *board.Row) {
						r.Restarts, r.Dead = 0, nil
						if r.From == board.Building {
							r.Dead = &long
						}
					})
				}
			}
			if typ != "VERIFIED" || sender != lead {
				continue
			}
			for _, s := range to {
				id, ok := bySession[s]
				if !ok || !regexp.MustCompile(`\b`+regexp.QuoteMeta(id)+`\b`).MatchString(text) {
					continue // a VERIFIED proves the row it names
				}
				r := state(id)
				switch r.State {
				case board.PR: // Crius saw it live on the merged code before az said merged
					if move(id, board.Merged, "Crius verified it on the merged code", nil) && move(id, board.Proven, "Crius verified it live", nil) {
						closeRole(r)
					}
				case board.Building, board.Ready, board.Merged:
					if move(id, board.Proven, "Crius verified it live", nil) {
						closeRole(r)
					}
				}
			}
		}
		// 3. sessions that ended with no result: after the grace, started again once, then the row waits on Crius
		for _, r := range b.Rows {
			if r.State != board.Building || r.Session == "" {
				continue
			}
			alive, err := d.Alive(role(cfg, r.ID))
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: is %s alive: %w", r.ID, r.Session, err))
				continue
			}
			note := func(why string, set func(*board.Row)) {
				if err := b.Note(r.ID, self, why, d.Now, set); err != nil {
					errs = append(errs, err)
				}
			}
			switch {
			case alive && r.Dead != nil:
				note("its session runs again", func(r *board.Row) { r.Dead = nil })
			case alive:
			case r.Dead == nil:
				now := d.Now
				note("its session reads ended; checked again in 2 min", func(r *board.Row) { r.Dead = &now })
			case d.Now.Sub(*r.Dead) < grace:
			case r.Restarts == 0: // counts tries, not successes: a restart that fails is not tried again
				note("its session ended; started again", func(r *board.Row) { r.Restarts++; r.Dead = nil })
				spawns = append(spawns, spawn{r.ID, role(cfg, r.ID), prompt(cfg, r.ID) + restarted})
			default:
				move(r.ID, board.Blocked, "its session ended twice", func(r *board.Row) { r.Question = "its session ended twice with no result" })
				posts = append(posts, post{"NEED", lead, fmt.Sprintf("%s: %s ended twice with no result; the row is blocked", r.ID, r.Session)})
			}
		}
		// 4. claim ready rows free of others' files, up to the cap; a role that runs already is taken over, not spawned
		building := 0
		for _, r := range b.Rows {
			if r.State == board.Building || r.State == board.Blocked && r.From == board.Building {
				building++
			}
		}
		for building < cfg.Cap {
			r, ok := b.Next()
			if !ok {
				break
			}
			s := session(r.ID)
			alive, err := d.Alive(role(cfg, r.ID))
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: is %s alive: %w", r.ID, s, err))
				break
			}
			why := "claimed for " + s
			if alive {
				why = s + " runs already; taken over"
			}
			if !move(r.ID, board.Building, why, func(r *board.Row) { r.Session, r.Dead = s, nil }) {
				break
			}
			if !alive {
				spawns = append(spawns, spawn{r.ID, role(cfg, r.ID), prompt(cfg, r.ID)})
			}
			building++
		}
		return nil
	})
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	// the cursor moves only once the board is saved: a run cut off before this reads its lines again, which moves
	// nothing twice (each rule needs the state the line moves the row from)
	if err := writeCursor(filepath.Join(dir, "dispatch.cursor"), upto); err != nil {
		errs = append(errs, err)
	}
	for _, id := range closes {
		if err := d.Close(role(cfg, id)); err != nil {
			errs = append(errs, fmt.Errorf("closing %s: %w", session(id), err))
		}
	}
	// 5. open the tabs, with no lock held
	var started []string
	for _, s := range spawns {
		if err := d.Spawn(s.role, s.prompt); err != nil {
			errs = append(errs, fmt.Errorf("%s: starting %s: %w", s.id, session(s.id), err))
			continue
		}
		started = append(started, session(s.id)+" on "+s.id)
	}
	if len(started) > 0 {
		posts = append(posts, post{"INFO", lead, fmt.Sprintf("started %s (cap %d)", strings.Join(started, ", "), cfg.Cap)})
	}
	for _, p := range posts {
		if err := d.Post(p.typ, p.to, p.text); err != nil {
			errs = append(errs, fmt.Errorf("posting %q: %w", p.text, err))
		}
	}
	return errors.Join(errs...)
}

func role(cfg config, id string) string { return cfg.Prefix + strings.ToLower(id) }

func prompt(cfg config, id string) string { return strings.ReplaceAll(cfg.Prompt, "{id}", id) }

// recipients are the bus names after the arrow, the direct one first, cc ones after.
func recipients(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '(' || r == ')' }) {
		if f != "cc" && f != "cc:" {
			out = append(out, strings.TrimPrefix(f, "cc:"))
		}
	}
	return out
}

// newLines gives the whole lines of a log after the cursor's byte offset, and the offset after the last of them. With
// no cursor (the first run on a project) it starts at the log's end: lines from before the board was seeded never
// move its rows. A cursor that does not read is an error. A log shorter than the cursor (rotated) is read from its
// start.
func newLines(path, cursorPath string) ([]string, int64, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	from := st.Size()
	if b, err := os.ReadFile(cursorPath); err == nil {
		if from, err = strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err != nil || from < 0 {
			return nil, 0, fmt.Errorf("%s does not read as a byte offset (%q): fix or remove it (removed = start at the bus end)", cursorPath, b)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, 0, err
	}
	if st.Size() < from {
		from = 0
	}
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return nil, 0, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, 0, err
	}
	end := strings.LastIndexByte(string(data), '\n')
	if end < 0 {
		return nil, from, nil
	}
	return strings.Split(string(data[:end]), "\n"), from + int64(end) + 1, nil
}

// writeCursor writes the cursor whole (a new file renamed over the old).
func writeCursor(path string, upto int64) error {
	tmp := path + ".new"
	if err := os.WriteFile(tmp, []byte(strconv.FormatInt(upto, 10)+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// prNumbers are the distinct PR numbers a line names, in order.
func prNumbers(text string) []string {
	var out []string
	for _, m := range prRe.FindAllStringSubmatch(text, -1) {
		if n := strings.TrimLeft(m[1], "0"); !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}
