// Package digest builds the twice-daily goal report (step 5): per lead the GOAL block of its status file
// (RULES 3.1), checked against the bus, plus what needs the owner. It runs with no model.
package digest

import (
	"errors"
	"fmt"
	"os"
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
	countRe     = regexp.MustCompile(`(\d+) of (\d+)`)
	clauseRe    = regexp.MustCompile(`[.;,()]|\s[-–—]\s|\band\b`)
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

// Rows gives the rows the Done line claims when it counts rows: the text has one "N of M" and it shares a clause with
// the word rows ("M1 rows ticked with evidence: 33 of 130"). Anything less clear (a second count, the word in another
// clause, a thousands mark) is not read, and the report says so: a guess once took a milestone count for the claim.
func (g *Goal) Rows() (int, bool) {
	n, counts := -1, 0
	for _, c := range clauseRe.Split(g.DoneHow, -1) {
		for _, m := range countRe.FindAllStringSubmatchIndex(c, -1) {
			counts++
			spaced := m[0] >= 2 && c[m[0]-1] == ' ' && isDigit(c[m[0]-2]) || m[1]+1 < len(c) && c[m[1]] == ' ' && isDigit(c[m[1]+1])
			if rowsWordRe.MatchString(c) && !spaced {
				n, _ = strconv.Atoi(c[m[2]:m[3]])
			}
		}
	}
	if counts != 1 || n < 0 {
		return 0, false
	}
	return n, true
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

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
