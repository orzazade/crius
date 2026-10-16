package digest

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/ingest"
	"github.com/orzazade/crius/internal/recall"
)

// maxLines bounds the report (Crius 10-17: ≤ 60 lines, plain short sentences).
const maxLines = 60

// LeadSnap is what the next report compares against: the claimed % and the verified milestones.
type LeadSnap struct {
	Done     int `json:"done"`
	Verified int `json:"verified"`
}

// Snapshot holds each readable lead's numbers at one report.
type Snapshot map[string]LeadSnap

// Report is the text of one goal report and the numbers the next one compares against.
type Report struct {
	Text   string
	Status string // the store's freshness: complete or partial
	Snap   Snapshot
}

// Build makes the goal report (RULES 3.4, Crius 10-17 02:29): what needs the owner on top, the first marked; then per
// lead the claimed % against the verified milestones, the change since the last report, the goal and the next
// milestone. A lead with no GOAL block is LOST, a block outside the grammar UNREADABLE, a file that cannot be read an
// ERROR, which also makes the report partial; each is its own line, and keeps its last numbers for the next report.
// Over 60 lines, the goal and next lines go first, from the last lead up; lead lines and
// owner needs always stay.
func Build(db *sql.DB, leads []ingest.Lead, prev Snapshot, now time.Time) (*Report, error) {
	status, reasons, err := recall.Freshness(db, now)
	if err != nil {
		return nil, err
	}
	r := &Report{Snap: Snapshot{}}
	var needs []string
	type entry struct{ line, goal, next string }
	var entries []entry
	cannotRead := 0
	for _, l := range leads {
		g, err := ReadGoal(l.StatusFile)
		var u *Unreadable
		if err != nil {
			if p, ok := prev[l.Name]; ok {
				r.Snap[l.Name] = p // compared with when the lead is readable again
			}
		}
		switch {
		case errors.Is(err, ErrLost):
			entries = append(entries, entry{line: fmt.Sprintf("%s: LOST: %s has no GOAL block", l.Name, l.StatusFile)})
			continue
		case errors.As(err, &u):
			entries = append(entries, entry{line: fmt.Sprintf("%s: UNREADABLE: %v", l.Name, err)})
			continue
		case err != nil:
			entries = append(entries, entry{line: fmt.Sprintf("%s: ERROR: %v", l.Name, err)})
			cannotRead++
			continue
		}
		checked, err := Verified(db, l.Name, g)
		if err != nil {
			return nil, err
		}
		done, verified := 0, 0
		for i, m := range g.Milestones {
			if m.State == Done {
				done++
				if checked[i] != 0 {
					verified++
				}
			}
		}
		snap := LeadSnap{Done: g.Done, Verified: verified}
		r.Snap[l.Name] = snap
		entries = append(entries, entry{
			line: fmt.Sprintf("%s: %d%% claimed · verified %d of %d done (%d milestones) · %s", l.Name, g.Done, verified, done,
				len(g.Milestones), change(prev, l.Name, snap)),
			goal: "  Goal: " + g.Goal,
			next: "  Next: " + g.Next,
		})
		if n := strings.Trim(strings.TrimSpace(g.NeedsOwner), `".`); !strings.EqualFold(n, "nothing") {
			needs = append(needs, fmt.Sprintf("%s: %s", l.Name, g.NeedsOwner))
		}
	}
	if cannotRead > 0 {
		status = "partial"
		reasons = append(reasons, fmt.Sprintf("%d lead status file cannot be read", cannotRead))
	}
	r.Status = status
	head := []string{fmt.Sprintf("# Goal report · %s · status %s", now.Format("2006-01-02 15:04"), status)}
	for _, why := range reasons {
		head = append(head, "! "+why)
	}
	body := []string{"", "## Needs the owner"}
	if len(needs) == 0 {
		body = append(body, "Nothing needs the owner.")
	}
	for i, n := range needs {
		mark := "  "
		if i == 0 {
			mark = "▶ "
		}
		body = append(body, fmt.Sprintf("%s%d. %s", mark, i+1, n))
	}
	body = append(body, "", "## Leads")
	if len(leads) == 0 {
		body = append(body, `No leads in projects.toml (leads = { role = "file" }).`)
	}
	// Lines without details, then add details from the first lead down while they fit.
	fixed := len(head) + len(body) + len(entries)
	details := 0
	for _, e := range entries {
		if e.goal != "" {
			details += 2
		}
	}
	room, cut := maxLines-fixed, false
	if details > room {
		room, cut = room-1, true
	}
	short := 0
	for _, e := range entries {
		body = append(body, e.line)
		switch {
		case e.goal == "":
		case room >= 2:
			body, room = append(body, e.goal, e.next), room-2
		default:
			short++
		}
	}
	if cut {
		body = append(body, fmt.Sprintf("(goal and next lines of %d leads cut for length; see their status files)", short))
	}
	r.Text = strings.Join(append(head, body...), "\n") + "\n"
	return r, nil
}

// change says how a lead's claimed % and verified milestones moved since the last report.
func change(prev Snapshot, lead string, now LeadSnap) string {
	p, ok := prev[lead]
	switch {
	case !ok:
		return "first report"
	case p == now:
		return "no change since the last report"
	}
	var parts []string
	if d := now.Done - p.Done; d != 0 {
		parts = append(parts, fmt.Sprintf("%+d", d))
	}
	if d := now.Verified - p.Verified; d != 0 {
		parts = append(parts, fmt.Sprintf("verified %+d", d))
	}
	return strings.Join(parts, ", ") + " since the last report"
}
