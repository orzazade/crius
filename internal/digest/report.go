package digest

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/ingest"
	"github.com/orzazade/crius/internal/quota"
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

// Input is what a report is built from besides the store.
type Input struct {
	Leads  []ingest.Lead
	Prev   Snapshot      // the last report's numbers
	Since  time.Time     // the last report's time; zero: none
	Quota  *quota.Report // built for now
	Health *recall.Health
}

// Build makes the goal report (RULES 3.4, Crius 10-17 02:29): what needs the owner on top, the first marked; then per
// lead the claimed % against the verified milestones, the change since the last report, the goal and the next
// milestone. A lead with no GOAL block is LOST, a block outside the grammar UNREADABLE, a file that cannot be read an
// ERROR, which also makes the report partial; each is its own line, and keeps its last numbers for the next report.
// Then what the store saw since the last report, quota pace and health (design §8), each list at most 3 items. Over
// 60 lines, the goal and next lines go first, from the last lead up; the rest stays.
func Build(db *sql.DB, in Input, now time.Time) (*Report, error) {
	leads, prev := in.Leads, in.Prev
	status, reasons, err := recall.Freshness(db, now)
	if err != nil {
		return nil, err
	}
	if in.Quota.Status != "complete" || in.Health.Status != "complete" {
		status = "partial"
	}
	r := &Report{Snap: Snapshot{}}
	stamp := "sources read:"
	for i, src := range in.Health.Sources {
		if i > 0 {
			stamp += " ·"
		}
		if src.Newest == "" {
			stamp += " " + src.Name + " never"
			continue
		}
		t, err := time.Parse(time.RFC3339, src.Newest)
		if err != nil {
			return nil, fmt.Errorf("source %s: newest %q: %w", src.Name, src.Newest, err)
		}
		stamp += " " + src.Name + " " + t.Local().Format("01-02 15:04")
	}
	if len(in.Health.Sources) == 0 {
		stamp += " none"
	}
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
		checked, notCounted, err := Verified(db, l.Name, g)
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
		lost := ""
		if notCounted > 0 {
			lost = fmt.Sprintf(" · VERIFIED lines not counted: %d", notCounted)
		}
		entries = append(entries, entry{
			line: fmt.Sprintf("%s: %d%% claimed · verified %d of %d done (%d milestones) · %s%s", l.Name, g.Done, verified, done,
				len(g.Milestones), change(prev, l.Name, snap), lost),
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
	head = append(head, stamp)
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
	tail, err := storeSections(db, in, now)
	if err != nil {
		return nil, err
	}
	// Lines without details, then add details from the first lead down while they fit.
	fixed := len(head) + len(body) + len(entries) + len(tail)
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
	r.Text = strings.Join(append(append(head, body...), tail...), "\n") + "\n"
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

// maxItems bounds each list of the store sections.
const maxItems = 3

// storeSections lists what the store saw since the last report (the last 24 h when there is none): checks that
// passed (VERIFIED lines from Crius or a QA role, with whom they are for), blocked lines, decisions and open waits;
// then quota pace and health.
func storeSections(db *sql.DB, in Input, now time.Time) ([]string, error) {
	since, title := in.Since, "## Since the last report ("+in.Since.Format("2006-01-02 15:04")+")"
	if since.IsZero() {
		since, title = now.Add(-24*time.Hour), "## Since the last report (none; the last 24 h)"
	}
	out := []string{"", title}
	list := func(name string, items []string) {
		if len(items) == 0 {
			out = append(out, name+": none")
			return
		}
		out = append(out, fmt.Sprintf("%s: %d", name, len(items)))
		for i, it := range items {
			if i == maxItems {
				out = append(out, fmt.Sprintf("  +%d more", len(items)-maxItems))
				break
			}
			out = append(out, "  "+clip(it, 110))
		}
	}
	events := func(cond string) ([]string, error) {
		rows, err := db.Query("select ts, project, actor, coalesce(refs, ''), text from event where ts >= ? and ("+cond+
			") order by ts, id",
			since.UTC().Format("2006-01-02T15:04:05Z"))
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var items []string
		for rows.Next() {
			var ts, project, actor, refs, text string
			if err := rows.Scan(&ts, &project, &actor, &refs, &text); err != nil {
				return nil, err
			}
			to := ""
			if refs != "" {
				var r map[string][]string
				if err := json.Unmarshal([]byte(refs), &r); err != nil {
					return nil, fmt.Errorf("event at %s: refs: %w", ts, err)
				}
				if len(r["to"]) > 0 && r["to"][0] != "crius" {
					to = " -> " + r["to"][0]
				}
			}
			t, err := time.Parse(time.RFC3339, ts)
			if err != nil {
				return nil, fmt.Errorf("event at %q: %w", ts, err)
			}
			items = append(items, fmt.Sprintf("%s:%s%s %s %s", project, actor, to, t.Local().Format("15:04"), text))
		}
		return items, rows.Err()
	}
	for _, l := range []struct{ name, cond string }{
		{"Verified by Crius or QA", "kind = 'verified' and ((project = 'crius' and actor = 'lead') or actor = 'qa' or" +
			" actor like 'qa-%')"},
		{"Blocked", "kind = 'blocked'"},
		{"Decisions", "kind = 'decision'"},
	} {
		items, err := events(l.cond)
		if err != nil {
			return nil, err
		}
		list(l.name, items)
	}
	w, err := recall.Find(db, recall.Query{Waiting: true, Now: now}, nil)
	if err != nil {
		return nil, err
	}
	var waits []string
	for _, h := range w.Hits {
		t, err := time.Parse(time.RFC3339, h.TS)
		if err != nil {
			return nil, fmt.Errorf("wait %s at %q: %w", h.ID, h.TS, err)
		}
		waits = append(waits, t.Local().Format("15:04")+" "+h.Text) // the text names the session; the project is "waits"
	}
	list("Open waits (last 24 h)", waits)

	q := in.Quota
	out = append(out, "", "## Quota and health")
	switch ro := q.Runout(); {
	case q.Reset.IsZero():
		out = append(out, "Quota: no sample this epoch")
	case q.SevenPct >= 100:
		out = append(out, fmt.Sprintf("Quota: 7d used up (%.0f%%) · resets %s", q.SevenPct, q.Reset.Local().Format("Mon 01-02 15:04")))
	default:
		pace := fmt.Sprintf("Quota: 7d %.0f%% on day %d of 7, %.1fx the time passed · ", q.SevenPct, q.Day, q.SevenPct/100/q.Passed)
		if ro.Before(q.Reset) {
			out = append(out, pace+"runs out ~"+ro.Local().Format("Mon 01-02 15:04")+" at this pace")
		} else {
			out = append(out, pace+"lasts to the reset "+q.Reset.Local().Format("Mon 01-02 15:04"))
		}
	}
	if q.Status != "complete" {
		more := ""
		if n := len(q.Reasons) - 1; n > 0 {
			more = fmt.Sprintf(" (+%d more: crius-rec quota)", n)
		}
		out = append(out, "Quota partial: "+q.Reasons[0]+more)
	}
	h := in.Health
	if h.Status == "complete" {
		return append(out, "Health: complete"), nil
	}
	out = append(out, fmt.Sprintf("Health: %s, %d problems", h.Status, len(h.Problems)))
	for i, p := range h.Problems {
		if i == maxItems {
			out = append(out, fmt.Sprintf("  +%d more: crius-rec health", len(h.Problems)-maxItems))
			break
		}
		out = append(out, "  "+clip(p, 110))
	}
	return out, nil
}

// clip cuts s to n runes, marking the cut.
func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
