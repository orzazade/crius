package playbook

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Shown is one line of shown.log: an entry shown at a session start.
type Shown struct {
	Time    time.Time
	Session string
	ID      string
}

// LoadShown reads dir/shown.log; no file is no lines. A bad line is an error,
// so a broken log is never read as "never shown".
func LoadShown(dir string) ([]Shown, error) {
	f, err := os.Open(filepath.Join(dir, "shown.log"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Shown
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		n++
		p := strings.Split(sc.Text(), "\t")
		if len(p) < 3 {
			return nil, fmt.Errorf("shown.log line %d: want time, session, id", n)
		}
		t, err := time.Parse(time.RFC3339, p[0])
		if err != nil {
			return nil, fmt.Errorf("shown.log line %d: %w", n, err)
		}
		out = append(out, Shown{Time: t, Session: p[1], ID: p[2]})
	}
	return out, sc.Err()
}

// Use is what the record says about one entry.
type Use struct {
	ID       string
	Shown    int      // session starts that showed it
	Followed int      // evidence from a session that was shown the entry: it was applied
	Teams    []string // teams that followed it
	Repeats  int      // the trap was hit again after the entry was first shown
}

// Review is one run's verdicts.
type Review struct {
	Uses       []Use
	Promote    []string // followed by 2+ teams with no repeat: make it a rule or a check
	NeedsCheck []string // hit again after it was shown: a note is not enough
	Archived   []string // not shown and no new evidence in 30 days
}

// sessionOf is the session id of a transcript evidence ref ("<dir>/<id>.jsonl#<uuid>").
func sessionOf(ref string) string {
	path, _, ok := strings.Cut(ref, "#")
	if !ok || !strings.HasSuffix(path, ".jsonl") {
		return ""
	}
	return strings.TrimSuffix(filepath.Base(path), ".jsonl")
}

// ReviewBook counts use per entry and archives the idle ones in b.
func ReviewBook(b *Book, shown []Shown, now time.Time) Review {
	type seen struct {
		first    time.Time
		count    int
		sessions map[string]bool
	}
	by := map[string]*seen{}
	for _, s := range shown {
		x := by[s.ID]
		if x == nil {
			x = &seen{first: s.Time, sessions: map[string]bool{}}
			by[s.ID] = x
		}
		x.count++
		x.sessions[s.Session] = true
		if s.Time.Before(x.first) {
			x.first = s.Time
		}
	}
	var r Review
	cut := now.AddDate(0, 0, -30)
	for i := range b.Entries {
		e := &b.Entries[i]
		if e.Archived {
			continue
		}
		u := Use{ID: e.ID}
		x := by[e.ID]
		teams := map[string]bool{}
		last := e.Created
		for _, ev := range e.Evidence {
			if ev.Date > last {
				last = ev.Date
			}
			if x == nil {
				continue
			}
			if s := sessionOf(ev.Ref); s != "" && x.sessions[s] {
				u.Followed++
				teams[ev.Team] = true
				continue
			}
			if e.Kind != "tactic" && ev.Date > x.first.Local().Format("2006-01-02") {
				u.Repeats++ // the trap was met again, by a session the entry did not reach or did not help
			}
		}
		if x != nil {
			u.Shown = x.count
			if d := x.first.Local().Format("2006-01-02"); d > last {
				last = d
			}
			for _, s := range shown {
				if s.ID == e.ID && s.Time.Local().Format("2006-01-02") > last {
					last = s.Time.Local().Format("2006-01-02")
				}
			}
		}
		for t := range teams {
			u.Teams = append(u.Teams, t)
		}
		sort.Strings(u.Teams)
		r.Uses = append(r.Uses, u)
		switch {
		case u.Repeats > 0:
			r.NeedsCheck = append(r.NeedsCheck, e.ID)
		case len(u.Teams) >= 2:
			r.Promote = append(r.Promote, e.ID)
		}
		if last < cut.Format("2006-01-02") {
			e.Archived = true
			r.Archived = append(r.Archived, e.ID)
		}
	}
	return r
}
