// Package digest builds the twice-daily goal report (step 5): per lead the GOAL block of its status file
// (RULES 3.1), checked against the bus, plus what needs the owner. It runs with no model.
package digest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// State is a milestone's mark: [x] done, [~] in progress, [ ] not started.
type State byte

const (
	Todo  State = ' '
	Doing State = '~'
	Done  State = 'x'
)

// Milestone is one "- [x] <text> · <refs>" line.
type Milestone struct {
	State      State
	Text, Refs string
}

// Goal is a lead's GOAL block (RULES 3.1).
type Goal struct {
	Updated    time.Time
	Goal       string
	Milestones []Milestone
	Done       int
	DoneHow    string
	Next       string
	NeedsOwner string
}

// ErrLost is a status file with no GOAL block: the lead is lost (RULES 3.3).
var ErrLost = errors.New("no GOAL block (lead LOST)")

// Unreadable is a GOAL block outside the grammar: the line and why, never a guess at what was meant.
type Unreadable struct {
	Line int
	Why  string
}

func (u *Unreadable) Error() string {
	return fmt.Sprintf("GOAL block unreadable: line %d: %s", u.Line, u.Why)
}

var (
	headingRe   = regexp.MustCompile(`^## GOAL \((\d{4}-\d{2}-\d{2} \d{2}:\d{2})\)$`)
	goalHeadRe  = regexp.MustCompile(`^\s*#+\s*(GOAL\b|(?i:goal)\s*\()`) // a try at the heading; headingRe checks it
	fenceRe     = regexp.MustCompile("^(`{3,}|~{3,})")
	milestoneRe = regexp.MustCompile(`^- \[([x~ ])\] (.+)$`)
	doneRe      = regexp.MustCompile(`^Done: (\d{1,3})% \((.+)\)$`)
	rowsWordRe  = regexp.MustCompile(`(?i)\brows\b`)
	countRe     = regexp.MustCompile(`\b\d+ of \d+\b`)
	// The rows claim of RULES 3.1: "rows: X of Y", then the end, " in <row file>" and/or "; <command>".
	rowsClaimRe = regexp.MustCompile(`(?i)^rows: (0|[1-9]\d*) of [1-9]\d*(?: in ([^;]+))?(?:;.*)?$`)
	// rowLineRe: a row line of a row file, a checklist item that starts its line ("- [x] F-110", "1. [ ] F-111") or a
	// table row whose first cell is the id ("| O1 |"), with its id.
	rowLineRe = regexp.MustCompile(`^(?:(?:[-*+]|\d+[.)]) \[[ xX~-]\] \**([0-9A-Za-z]+(?:-[0-9A-Za-z]+)*)|` +
		`\| *\**([0-9A-Za-z]+(?:-[0-9A-Za-z]+)*)\** *\|)`)
)

// ReadGoal reads the GOAL block of a status file. A file that cannot be read is an error of its own: it is never
// reported as LOST.
func ReadGoal(path string) (*Goal, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	g, err := ParseGoal(string(b))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return g, nil
}

// Rows gives the rows the Done line claims when it counts rows. The claim is read only in the RULES 3.1 form, as the
// one "N of M" of the text; anything else is not read, and the report says so. Five review rounds found prose that a
// looser reading took a milestone count from.
func (g *Goal) Rows() (int, bool) {
	m := rowsClaimRe.FindStringSubmatch(g.DoneHow)
	if m == nil || len(countRe.FindAllString(g.DoneHow, -1)) != 1 {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil { // too large for an int: not the claim
		return 0, false
	}
	return n, true
}

// RowIDs reads the row ids of the row file the rows claim names, in upper case (Crius 10-17 05:13: the row file is
// the one source of row ids). A row is a checklist item that starts its line or a table row whose first cell is the
// id; headings, indented items, prose and code fences give none (review of 23ad139). The path is absolute, ~/, or
// relative to the folder of the status file; only a regular file of at most 10 MB is read. No row file named, a file
// that cannot be read and a file with no row ids are errors: rows are then not counted, never counted as none.
func (g *Goal) RowIDs(statusFile string) (map[string]bool, error) {
	m := rowsClaimRe.FindStringSubmatch(g.DoneHow)
	if m == nil || strings.TrimSpace(m[2]) == "" {
		return nil, errors.New(`the Done line names no row file: "rows: X of Y in <row file>"`)
	}
	p := strings.TrimSpace(m[2])
	switch {
	case strings.HasPrefix(p, "~/"):
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		p = filepath.Join(home, p[2:])
	case !filepath.IsAbs(p):
		p = filepath.Join(filepath.Dir(statusFile), p)
	}
	fi, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > 10<<20 {
		return nil, fmt.Errorf("row file %s is not a regular file of at most 10 MB", p)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	ids, fenced := map[string]bool{}, false
	for _, line := range strings.Split(strings.TrimPrefix(string(b), "\ufeff"), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fenced = !fenced
		}
		if m := rowLineRe.FindStringSubmatch(line); m != nil && !fenced {
			if id := m[1] + m[2]; isID(id) {
				ids[strings.ToUpper(id)] = true
			}
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("row file %s has no row ids", p)
	}
	return ids, nil
}

// ParseGoal reads the first GOAL block of a status file outside code fences (``` or ~~~, closed by the same mark at
// least as long), in the exact form of RULES 3.1. A heading that is almost right (indented, one #, "GOAL" with no
// date, "Goal (" in another case) is an unreadable block, not LOST; a prose heading like "## Goal" is not a try. Blank lines in the
// block are skipped; the block ends at its "Needs owner:" line.
func ParseGoal(text string) (*Goal, error) {
	lines := strings.Split(strings.TrimPrefix(text, "\ufeff"), "\n")
	at, fence := -1, "" // fence: the open code fence's marker
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if f := fenceRe.FindString(t); f != "" {
			switch {
			case fence == "":
				fence = f
			case f[0] == fence[0] && len(f) >= len(fence) && t == f:
				fence = ""
			}
			continue
		}
		if fence == "" && goalHeadRe.MatchString(l) {
			at = i
			break
		}
	}
	if at < 0 {
		return nil, ErrLost
	}
	bad := func(i int, why string) error { return &Unreadable{i + 1, why} }
	m := headingRe.FindStringSubmatch(strings.TrimRight(lines[at], " \t\r"))
	if m == nil {
		return nil, bad(at, "heading needs (YYYY-MM-DD HH:MM)")
	}
	g := &Goal{}
	var err error
	if g.Updated, err = time.ParseInLocation("2006-01-02 15:04", m[1], time.Local); err != nil {
		return nil, bad(at, "heading needs (YYYY-MM-DD HH:MM)")
	}
	// next returns the index and text of the next non-blank line (len(lines) at the end).
	next := func() (int, string) {
		for at++; at < len(lines); at++ {
			if l := strings.TrimRight(lines[at], " \t\r"); l != "" {
				return at, l
			}
		}
		return at, ""
	}
	field := func(name string) (string, error) {
		i, l := next()
		v, ok := strings.CutPrefix(l, name+": ")
		if !ok {
			return "", bad(i, "want "+name+":")
		}
		return v, nil
	}
	if g.Goal, err = field("Goal"); err != nil {
		return nil, err
	}
	i, l := next()
	for {
		ms := milestoneRe.FindStringSubmatch(l)
		if ms == nil {
			break
		}
		text, refs := ms[2], ""
		if k := strings.LastIndex(text, " · "); k >= 0 {
			text, refs = text[:k], strings.TrimSpace(text[k+len(" · "):])
		}
		if State(ms[1][0]) == Done && refs == "" {
			return nil, bad(i, "a done milestone needs refs after ·")
		}
		g.Milestones = append(g.Milestones, Milestone{State(ms[1][0]), text, refs})
		i, l = next()
	}
	switch {
	case strings.HasPrefix(l, "- ["):
		return nil, bad(i, "not in the grammar")
	case len(g.Milestones) == 0:
		return nil, bad(i, "want at least one milestone")
	case !strings.HasPrefix(l, "Done:"):
		return nil, bad(i, "not in the grammar")
	}
	d := doneRe.FindStringSubmatch(l)
	n := -1
	if d != nil {
		n, _ = strconv.Atoi(d[1])
	}
	if n < 0 || n > 100 {
		return nil, bad(i, "want Done: N% (how it was counted)")
	}
	g.Done, g.DoneHow = n, d[2]
	if g.Next, err = field("Next"); err != nil {
		return nil, err
	}
	if g.NeedsOwner, err = field("Needs owner"); err != nil {
		return nil, err
	}
	return g, nil
}
