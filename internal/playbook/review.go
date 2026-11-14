package playbook

import (
	"bufio"
	"errors"
	"io"
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

// LoadShown reads dir/shown.log; no file is no lines. A line that cannot be
// read (a torn append) is skipped, so one bad line never stops the nightly run.
// Bad lines are counted only from byte from on (where the last run stopped), so
// each one is reported on one night, not every night after. end is the byte
// after the last whole line; a last line with no newline yet may still be being
// written, so it is neither read nor counted.
func LoadShown(dir string, from int64) (out []Shown, bad int, end int64, err error) {
	f, err := os.Open(filepath.Join(dir, "shown.log"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0, 0, nil
	}
	if err != nil {
		return nil, 0, 0, err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	for {
		line, rerr := r.ReadString('\n')
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				return out, bad, end, nil
			}
			return out, bad, end, rerr
		}
		at := end
		end += int64(len(line))
		p := strings.Split(strings.TrimSuffix(line, "\n"), "\t")
		t, terr := time.Parse(time.RFC3339, p[0])
		if len(p) < 3 || p[2] == "" || terr != nil {
			if at >= from {
				bad++
			}
			continue
		}
		out = append(out, Shown{Time: t, Session: p[1], ID: p[2]})
	}
}

// Use is what the record says about one entry.
type Use struct {
	ID       string
	Shown    int      // session starts that showed it
	Followed int      // sightings after it was shown where the advice was used
	Teams    []string // teams that followed it
	Repeats  int      // the problem happened again after it was shown
}

// Review is one run's verdicts.
type Review struct {
	Uses       []Use
	Promote    []string // followed by 2+ teams with no repeat: make it a rule or a check
	NeedsCheck []string // hit again after it was shown: a note is not enough
	Archived   []string // not shown and not added to in 30 days of the book's own time
}

// sessionOf is the session id of a transcript evidence ref ("<dir>/<id>.jsonl#<uuid>").
func sessionOf(ref string) string {
	path, _, ok := strings.Cut(ref, "#")
	if !ok || !strings.HasSuffix(path, ".jsonl") {
		return ""
	}
	return strings.TrimSuffix(filepath.Base(path), ".jsonl")
}

func dayOf(t time.Time) string { return t.Local().Format("2006-01-02") }

// ReviewBook counts use per entry and archives the idle ones in b.
//   - followed: a sighting dated on or after the entry was first shown, that the
//     writing model marked "applied", or that came from a session shown the entry
//     (on or after that showing) and was not a "hit";
//   - repeat: a sighting marked "hit" dated after the entry was first shown;
//   - idle: the book has not added it or to it, and no session start showed it,
//     in 30 days. The book's dates count, not the record's, so a seed or a
//     backfill of old findings is not archived on arrival.
func ReviewBook(b *Book, shown []Shown, now time.Time) Review {
	first := map[string]string{}                // entry -> first day shown
	last := map[string]string{}                 // entry -> last day shown
	inSession := map[string]map[string]string{} // entry -> session -> first day shown there
	count := map[string]int{}
	for _, s := range shown {
		d := dayOf(s.Time)
		count[s.ID]++
		if f, ok := first[s.ID]; !ok || d < f {
			first[s.ID] = d
		}
		if d > last[s.ID] {
			last[s.ID] = d
		}
		if s.Session == "" {
			continue // a showing with no session (show -cwd) ties no sighting to it
		}
		if inSession[s.ID] == nil {
			inSession[s.ID] = map[string]string{}
		}
		if f, ok := inSession[s.ID][s.Session]; !ok || d < f {
			inSession[s.ID][s.Session] = d
		}
	}
	var r Review
	cut := dayOf(now.AddDate(0, 0, -30))
	for i := range b.Entries {
		e := &b.Entries[i]
		if e.Archived {
			continue
		}
		u := Use{ID: e.ID, Shown: count[e.ID]}
		teams := map[string]bool{}
		active := e.Added
		if active == "" {
			active = e.Created // entries from before the book kept its own dates
		}
		f, wasShown := first[e.ID]
		for _, ev := range e.Evidence {
			added := ev.Added
			if added == "" {
				added = ev.Date
			}
			if added > active {
				active = added
			}
			if !wasShown || ev.Date < f {
				continue
			}
			sd, fromShown := inSession[e.ID][sessionOf(ev.Ref)]
			switch {
			case ev.Outcome == "hit":
				if ev.Date > f {
					u.Repeats++
				}
			case ev.Outcome == "applied", fromShown && ev.Date >= sd:
				u.Followed++
				teams[ev.Team] = true
			}
		}
		if last[e.ID] > active {
			active = last[e.ID]
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
		if active < cut {
			e.Archived = true
			r.Archived = append(r.Archived, e.ID)
		}
	}
	return r
}
