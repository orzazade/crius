package digest

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/ingest"
	"github.com/orzazade/crius/internal/quota"
	"github.com/orzazade/crius/internal/recall"
	"github.com/orzazade/crius/internal/redact"
)

// maxLines bounds the report (Crius 10-17: ≤ 60 lines, plain short sentences).
const maxLines = 60

// LeadSnap is what the next report compares against: the claimed % and the verified milestones.
type LeadSnap struct {
	Done     int  `json:"done"`
	Verified int  `json:"verified"`
	Rows     bool `json:"rows,omitempty"` // Verified counts rows (Goal.Rows), not milestones
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
		g, err := readGoal(l)
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
		checked, rows, notCounted, err := Verified(db, l.Name, g)
		if err != nil {
			return nil, err
		}
		done, verified := 0, 0
		var behind []string // rows done by a VERIFIED line but not marked done in the status file
		for i, m := range g.Milestones {
			if m.State == Done || checked[i] != 0 {
				done++
			}
			if checked[i] != 0 {
				verified++
			}
			if m.State != Done && checked[i] != 0 {
				var ts string
				if err := db.QueryRow("select ts from event where id = ?", checked[i]).Scan(&ts); err != nil {
					return nil, err
				}
				at, err := time.Parse(time.RFC3339, ts)
				if err != nil {
					return nil, fmt.Errorf("event %d ts %q: %w", checked[i], ts, err)
				}
				behind = append(behind, fmt.Sprintf("%s (VERIFIED %s)", m.Text, at.In(now.Location()).Format("2006-01-02 15:04")))
			}
		}
		counted := fmt.Sprintf("verified %d of %d done (%d milestones)", verified, done, len(g.Milestones))
		claimed, countsRows := g.Rows()
		if countsRows {
			counted = fmt.Sprintf("verified rows %d of claimed %d, milestones %d of %d", rows, claimed, verified, done)
			if extra := rows - claimed; extra > 0 { // a withdrawn acceptance (RULES 2.3, Crius 10-17 04:41)
				counted += fmt.Sprintf(" · MISMATCH: %d verified %s more than claimed (a verified row was un-ticked or the"+
					" claim shrank)", extra, map[bool]string{true: "row", false: "rows"}[extra == 1])
			}
			verified = rows
		}
		if !countsRows && rowsWordRe.MatchString(g.DoneHow) {
			counted += ` · the Done line names rows but its claim was not read: write "rows: X of Y in <row file>; <command>" (RULES 3.1) and no other "N of M"`
		}
		snap := LeadSnap{Done: g.Done, Verified: verified, Rows: countsRows}
		r.Snap[l.Name] = snap
		lost := ""
		if len(behind) > 0 {
			lost = " · done by VERIFIED, still [~] or [ ] in the status file: " + strings.Join(behind, "; ")
		}
		if notCounted > 0 {
			lost += fmt.Sprintf(" · VERIFIED lines not counted: %d", notCounted)
		}
		stale, err := staleness(db, l.Name, g)
		if err != nil {
			return nil, err
		}
		var open []string
		var answered string
		if n := strings.Trim(strings.TrimSpace(g.NeedsOwner), `".`); !strings.EqualFold(n, "nothing") {
			if open, answered, err = ownerAnswers(db, g); err != nil {
				return nil, err
			}
		}
		entries = append(entries, entry{
			line: fmt.Sprintf("%s: %s%d%% claimed · %s · %s%s%s", l.Name, stale, g.Done, counted, change(prev, l.Name, snap),
				lost, answered),
			goal: "  Goal: " + g.Goal,
			next: "  Next: " + g.Next,
		})
		if len(open) > 0 {
			needs = append(needs, fmt.Sprintf("%s: %s", l.Name, strings.Join(open, "; ")))
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
	details := 0
	for _, e := range entries {
		if e.goal != "" {
			details += 2
		}
	}
	// The store lists give up items until the lines without details fit (with the cut note when details are cut);
	// then details are added from the first lead down while they fit.
	var tail []string
	var fixed int
	for items := maxItems; ; items-- {
		var err error
		if tail, err = storeSections(db, in, now, items); err != nil {
			return nil, err
		}
		fixed = len(head) + len(body) + len(entries) + len(tail)
		need := fixed
		if details > maxLines-fixed {
			need++
		}
		if need <= maxLines || items == 0 {
			break
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

// readGoal reads a lead's GOAL block from its status file, or from the git object when the lead names one (its
// committed copy, not a checkout's). An object git cannot read is an error, never LOST.
func readGoal(l ingest.Lead) (*Goal, error) {
	if l.Repo == "" {
		return ReadGoal(l.StatusFile)
	}
	var out, errb bytes.Buffer
	// --end-of-options: an object from projects.toml that starts with '-' is never read as an option.
	cmd := exec.Command("git", "-C", l.Repo, "cat-file", "blob", "--end-of-options", l.StatusFile)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		why, _, _ := strings.Cut(strings.TrimSpace(errb.String()), "\n") // one report line
		if len(why) > 200 {
			why = why[:200] + "…"
		}
		return nil, fmt.Errorf("%s %s: git cat-file: %v: %s", l.Repo, l.StatusFile, err, why)
	}
	g, err := ParseGoal(out.String())
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", l.Repo, l.StatusFile, err)
	}
	return g, nil
}

// staleness says a lead's GOAL block is stale when the lead posted a DONE or CLEAR-READY on the Crius bus after its
// stamp (Crius GO 10-19 18:25): its status file was not written with the milestone. INFO, NEED and team log lines
// never count. A stamp in a repeated hour (DST fall-back) is taken at its later reading. "" when it is not stale.
func staleness(db *sql.DB, lead string, g *Goal) (string, error) {
	project, role, _ := strings.Cut(lead, ":")
	latest := laterReading(g.Updated)
	var ts, kind string
	err := db.QueryRow("select ts, kind from event where project = ? and actor = ? and kind in ('done', 'clear-ready')"+
		" and source_key like 'bus:%' and ts > ? order by ts desc, id desc limit 1",
		project, role, latest.UTC().Format("2006-01-02T15:04:05Z")).Scan(&ts, &kind)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	at, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return "", fmt.Errorf("event %s %s: ts %q: %w", lead, kind, ts, err)
	}
	return fmt.Sprintf("STALE: GOAL (%s) is older than its %s %s on the bus · ", g.Updated.Format("2006-01-02 15:04"),
		strings.ToUpper(kind), at.Local().Format("2006-01-02 15:04")), nil
}

// laterReading gives the later reading of a GOAL stamp in a repeated hour (DST fall-back), else the stamp.
func laterReading(stamp time.Time) time.Time {
	latest, wall := stamp, stamp.Format("2006-01-02 15:04")
	for d := 15 * time.Minute; d <= 2*time.Hour; d += 15 * time.Minute {
		if t := stamp.Add(d); t.Format("2006-01-02 15:04") == wall {
			latest = t
		}
	}
	return latest
}

var (
	// A "Needs owner" field of numbered items: "(1) …; (2) …". Only a number at the start or after ';' starts an item:
	// "option (1) keep ours or (2) copy" is one question with choices (review of 0853631).
	itemSepRe  = regexp.MustCompile(`(?:^|[;:])\s*(\(\d+\)\s)`)
	itemMarkRe = regexp.MustCompile(`^\((\d+)\)\s`)
	// What names an item on the bus: a sha, an id (F-118, G-SUB-503), a quoted phrase ('Vendor cost').
	needShaRe   = regexp.MustCompile(`\b[0-9a-f]{7,40}\b`)
	needIDRe    = regexp.MustCompile(`\b[A-Z][A-Z0-9]*(?:-[A-Z0-9]+)*-[0-9]+\b`)
	needQuoteRe = regexp.MustCompile(`(?:^|[^\w])(?:'([^']{3,60})'|"([^"]{3,60})")`) // a pair of one mark: "owner's choice"
	ownerWordRe = regexp.MustCompile(`(?i)\bowner\b`)
)

// ownerAnswers splits a GOAL's "Needs owner" into its numbered items (one item when it has no numbers) and leaves out
// each item a crius:lead ANSWER, ORDER or CORRECTED on the bus after the stamp answers: one that speaks for the owner
// and names the item by a sha, id or quoted phrase of it (10-25 09:00: the digest re-sent 4 settled Quotes questions).
// It gives the open items and, when some were answered, a note for the lead's line saying which line answered which.
func ownerAnswers(db *sql.DB, g *Goal) (open []string, note string, err error) {
	text := strings.TrimSpace(g.NeedsOwner)
	var items []string
	if marks := itemSepRe.FindAllStringSubmatchIndex(text, -1); len(marks) >= 2 {
		// A label before the list ("Questions:") is no question (review of 0d5f348).
		if pre := strings.TrimSpace(text[:marks[0][0]]); pre != "" && text[marks[0][0]] != ':' {
			items = append(items, strings.TrimRight(pre, "; "))
		}
		for i, m := range marks {
			end := len(text)
			if i+1 < len(marks) {
				end = marks[i+1][0]
			}
			items = append(items, strings.TrimRight(strings.TrimSpace(text[m[2]:end]), ";"))
		}
	} else {
		items = []string{text}
	}
	rows, err := db.Query("select ts, kind, text from event where project = 'crius' and actor = 'lead' and kind in"+
		" ('answer', 'order', 'corrected') and source_key like 'bus:%' and ts > ? order by ts, id",
		laterReading(g.Updated).UTC().Format("2006-01-02T15:04:05Z"))
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	by := make([]string, len(items))
	for rows.Next() {
		var ts, kind string
		var line sql.NullString
		if err := rows.Scan(&ts, &kind, &line); err != nil {
			return nil, "", err
		}
		if !ownerWordRe.MatchString(line.String) {
			continue
		}
		for i, item := range items {
			// A whole token: G-SUB-503 is not in "G-SUB-5030" (review of 0853631); a sha also as the start of a longer one
			// (review of 0d5f348).
			if by[i] != "" || !slices.ContainsFunc(itemRefs(item), func(r string) bool {
				tail := `(?:[^\w-]|$)`
				if shaRe.MatchString(r) {
					tail = `[0-9a-fA-F]*` + tail
				}
				return regexp.MustCompile(`(?i)(?:^|[^\w-])` + regexp.QuoteMeta(r) + tail).MatchString(line.String)
			}) {
				continue
			}
			at, err := time.Parse(time.RFC3339, ts)
			if err != nil {
				return nil, "", fmt.Errorf("event crius:lead %s: ts %q: %w", kind, ts, err)
			}
			by[i] = fmt.Sprintf("by crius:lead %s %s", strings.ToUpper(kind), at.Local().Format("2006-01-02 15:04"))
		}
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	var done []string
	for i, item := range items {
		if by[i] == "" {
			open = append(open, item)
			continue
		}
		label := "the question"
		if m := itemMarkRe.FindStringSubmatch(item); m != nil && strings.HasPrefix(item, m[0]) {
			label = "(" + m[1] + ")"
		}
		done = append(done, label+" "+by[i])
	}
	if len(done) > 0 {
		note = " · answered on the bus, left out of Needs the owner: " + strings.Join(done, ", ")
	}
	return open, note, nil
}

// itemRefs gives the shas (with a letter), ids and quoted phrases of an owner question.
func itemRefs(item string) []string {
	var refs []string
	for _, s := range needShaRe.FindAllString(item, -1) {
		if strings.ContainsAny(s, "abcdef") {
			refs = append(refs, s)
		}
	}
	refs = append(refs, needIDRe.FindAllString(item, -1)...)
	for _, m := range needQuoteRe.FindAllStringSubmatch(item, -1) {
		refs = append(refs, m[1]+m[2])
	}
	return refs
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
	if p.Rows != now.Rows { // the counts are of different things
		parts = append(parts, map[bool]string{true: "verified now counts rows", false: "verified now counts milestones"}[now.Rows])
	} else if d := now.Verified - p.Verified; d != 0 {
		parts = append(parts, fmt.Sprintf("verified %+d", d))
	}
	return strings.Join(parts, ", ") + " since the last report"
}

// maxItems bounds each list of the store sections.
const maxItems = 3

// storeSections lists what the store saw since the last report (the last 24 h when there is none), up to maxItems
// per list: checks that passed (VERIFIED lines from Crius or a QA role, with whom they are for), blocked lines,
// decisions (with those saved without a DECISION bus line) and open waits; then quota pace and health.
func storeSections(db *sql.DB, in Input, now time.Time, maxItems int) ([]string, error) {
	since, title := in.Since, "## Since the last report ("+in.Since.Format("2006-01-02 15:04")+")"
	if since.IsZero() {
		since, title = now.Add(-24*time.Hour), "## Since the last report (none; the last 24 h)"
	}
	out := []string{"", title}
	list := func(name string, items []string, capped bool) {
		if len(items) == 0 {
			out = append(out, name+": none")
			return
		}
		n := fmt.Sprint(len(items))
		if capped {
			n += "+"
		}
		out = append(out, name+": "+n)
		if maxItems == 0 { // no room left: the count line alone
			return
		}
		for i, it := range items {
			if i == maxItems {
				out = append(out, fmt.Sprintf("  +%d more", len(items)-maxItems))
				break
			}
			out = append(out, "  "+clip(it, 110))
		}
	}
	events := func(cond string) ([]string, error) {
		// An OWNER line (the owner's own words relayed by Crius) says so: it lists with the decisions.
		rows, err := db.Query("select ts, project, actor, coalesce(refs, ''), case kind when 'owner' then 'OWNER ' || text"+
			" else text end from event where ts >= ? and ("+cond+
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
				if len(r["to"]) > 0 { // a VERIFIED line; one to "crius" names no lead and shows it
					to = " -> " + strings.Join(r["to"], ", ")
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
		{"Decisions", "kind in ('decision', 'owner')"},
	} {
		items, err := events(l.cond)
		if err != nil {
			return nil, err
		}
		if l.name == "Decisions" {
			saved, err := offBus(db, since)
			if err != nil {
				return nil, err
			}
			items = append(items, saved...)
		}
		list(l.name, items, false)
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
	list("Open waits (last 24 h)", waits, w.More)
	bad, err := malformed(db, since)
	if err != nil {
		return nil, err
	}
	bad = " · lines not read (malformed): " + bad // on the health line: no line of the 60 for it

	q := in.Quota
	out = append(out, "", "## Quota and health")
	switch ro := q.Runout(); {
	case q.Reset.IsZero():
		out = append(out, "Quota: no sample this epoch")
	case q.SevenPct >= 100:
		out = append(out, fmt.Sprintf("Quota: 7d used up (%.0f%%) · resets %s", q.SevenPct, q.Reset.Local().Format("Mon 01-02 15:04")))
	default:
		pace := fmt.Sprintf("Quota: 7d %.0f%% on day %d of 7, %.1fx the time passed · ", q.SevenPct, q.Day, q.SevenPct/100/q.Passed)
		if !ro.IsZero() && ro.Before(q.Reset) { // no use so far: no runout
			out = append(out, pace+"runs out ~"+ro.Local().Format("Mon 01-02 15:04")+" at this pace")
		} else {
			out = append(out, pace+"lasts to the reset "+q.Reset.Local().Format("Mon 01-02 15:04"))
		}
	}
	if !q.Reset.IsZero() {
		if !q.FiveReset.IsZero() {
			out[len(out)-1] += fmt.Sprintf(" · 5h %.0f%% (resets %s)", q.FivePct, q.FiveReset.Local().Format("15:04"))
		}
		if len(q.Shares) > 0 {
			out[len(out)-1] += " · top " + q.Burners(1)
		}
	}
	if len(q.Accounts) > 0 {
		out = append(out, "Quota by account: "+q.AccountsText())
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
		return append(out, "Health: complete"+bad), nil
	}
	out = append(out, fmt.Sprintf("Health: %s, %d problems", h.Status, len(h.Problems))+bad)
	for i, p := range h.Problems {
		if i == maxItems {
			out = append(out, fmt.Sprintf("  +%d more: crius-rec health", len(h.Problems)-maxItems))
			break
		}
		out = append(out, "  "+clip(p, 110))
	}
	return out, nil
}

// offBus lists the decisions saved on or after since's day that no DECISION bus line cites (record decision saved
// the entry and its bus line failed): the decision table holds a day, no time.
func offBus(db *sql.DB, since time.Time) ([]string, error) {
	rows, err := db.Query("select coalesce(project, ''), ts, id, coalesce(title, '') from decision d where ts >= ? and"+
		" not exists (select 1 from event e where e.kind = 'decision' and instr(e.refs, '\"' || d.id || '\"') > 0)"+
		" order by ts, id", since.Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var project, ts, id, title string
		if err := rows.Scan(&project, &ts, &id, &title); err != nil {
			return nil, err
		}
		out = append(out, fmt.Sprintf("%s %s %s (no DECISION bus line) %s", project, ts, id, title))
	}
	return out, rows.Err()
}

// clip redacts s, then cuts it to n runes, marking the cut: a cut secret no longer looks like one to the redactor.
func clip(s string, n int) string {
	s, _ = redact.Redact(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// malformed counts the log lines ingest read since since and could not parse, per project: "none" or
// "3 (axi 2, quotes 1)".
func malformed(db *sql.DB, since time.Time) (string, error) {
	rows, err := db.Query("select project, count(*) from event where kind = 'malformed' and ts >= ? group by project"+
		" order by count(*) desc, project", since.UTC().Format("2006-01-02T15:04:05Z"))
	if err != nil {
		return "", err
	}
	defer rows.Close()
	total := 0
	var per []string
	for rows.Next() {
		var project string
		var n int
		if err := rows.Scan(&project, &n); err != nil {
			return "", err
		}
		total += n
		per = append(per, fmt.Sprintf("%s %d", project, n))
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if total == 0 {
		return "none", nil
	}
	return fmt.Sprintf("%d (%s)", total, strings.Join(per, ", ")), nil
}
