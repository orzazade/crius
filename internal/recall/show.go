package recall

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/store"
)

type Field struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Shown is one thing in full (design §4 `show`): its fields, then related lines (children, citing lines, a chain).
type Shown struct {
	Status  string   `json:"status"`
	Reasons []string `json:"reasons,omitempty"`
	Kind    string   `json:"kind"`
	ID      string   `json:"id"`
	Found   bool     `json:"found"`
	Fields  []Field  `json:"fields,omitempty"`
	Lines   []string `json:"lines,omitempty"`
	Body    string   `json:"body,omitempty"` // a project's STATE.md
}

func (s *Shown) add(name, value string) {
	if value != "" {
		s.Fields = append(s.Fields, Field{name, value})
	}
}

var projectRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// querier is a *sql.DB or a *sql.Conn.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// supersededBy maps each decision id to the later decision of its project that supersedes it. As in
// store.ActiveIDs, a supersede of the same or a later number is ignored.
func supersededBy(ctx context.Context, q querier) (map[string]string, error) {
	rows, err := q.QueryContext(ctx, "select id, supersedes from decision where coalesce(supersedes,'') <> ''")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	by := map[string]string{}
	for rows.Next() {
		var id, sup string
		if err := rows.Scan(&id, &sup); err != nil {
			return nil, err
		}
		if p, n := splitD(id); p != "" {
			// Two decisions superseding one: the first (lowest number) is its successor, whatever the row order.
			if sp, sn := splitD(sup); sp == p && sn < n {
				if _, on := splitD(by[sup]); by[sup] == "" || n < on {
					by[sup] = id
				}
			}
		}
	}
	return by, rows.Err()
}

// Show gives one item, decision, session, commit, event or project. Not found is a complete "no match" that says
// where it looked; an id of no known shape is an ErrQuery.
func Show(db *sql.DB, home, id string, history bool, now time.Time) (*Shown, error) {
	s := &Shown{ID: id}
	switch {
	case itemRe.MatchString(id):
		s.Kind = "item"
	case decisionRe.MatchString(id):
		s.Kind = "decision"
	case eventRe.MatchString(id):
		s.Kind = "event"
	case shaRe.MatchString(id) || sessionRe.MatchString(id):
		s.Kind = "commit or session"
	case projectRe.MatchString(id):
		s.Kind = "project"
	default:
		return nil, fmt.Errorf("%w: %q is not an item (<project>#<n>), decision (<project>-D<n>), session or commit "+
			"prefix, event:<n> or project", ErrQuery, id)
	}
	if history && s.Kind != "decision" && s.Kind != "item" {
		return nil, fmt.Errorf("%w: --history covers decisions (their supersede chain) and items (acceptance, defects,"+
			" revokes, fixes, corrections)", ErrQuery)
	}
	var err error
	if s.Status, s.Reasons, err = Freshness(db, now); err != nil {
		return nil, err
	}
	ctx := context.Background()
	switch s.Kind {
	case "item":
		err = showItem(ctx, db, s, history)
	case "decision":
		err = showDecision(ctx, db, s, history)
	case "event":
		err = showEvent(ctx, db, s)
	case "commit or session":
		err = showCommitOrSession(ctx, db, s)
	case "project":
		err = showProject(db, home, s)
	}
	return s, err
}

// showItem reads the item as corrected (item_v). With history, the lines after its children are the chain in time
// order: its opening, then every event naming it or a fix item opened for it.
func showItem(ctx context.Context, db *sql.DB, s *Shown, history bool) error {
	var project, kind, title, criteria, opened, state, parent sql.NullString
	var taint int
	err := db.QueryRowContext(ctx, "select project, kind, title, criteria, opened, state, parent, taint from item_v where id = ?",
		s.ID).Scan(&project, &kind, &title, &criteria, &opened, &state, &parent, &taint)
	if err == sql.ErrNoRows {
		s.Kind = "items"
		return nil
	}
	if err != nil {
		return err
	}
	s.Found = true
	s.add("Project", project.String)
	s.add("Title", title.String)
	s.add("Kind", kind.String)
	s.add("State", state.String)
	s.add("Opened", opened.String)
	s.add("Parent", parent.String)
	var crit []string
	if err := json.Unmarshal([]byte(criteria.String), &crit); err != nil {
		return fmt.Errorf("item %s has bad criteria %q: %w", s.ID, criteria.String, err)
	}
	for i, c := range crit {
		s.add("Criteria", fmt.Sprintf("%d. %s", i+1, c))
	}
	if taint != 0 {
		s.add("Taint", "tainted")
	}
	rows, err := db.QueryContext(ctx, "select id, state, title from item_v where parent = ? order by n", s.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, cstate, ctitle string
		if err := rows.Scan(&cid, &cstate, &ctitle); err != nil {
			return err
		}
		s.Lines = append(s.Lines, "child "+cid+" "+cstate+" "+ctitle)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if history {
		s.Lines = append(s.Lines, strings.Replace(opened.String, "T", " ", 1)[:min(16, len(opened.String))]+
			" opened "+s.ID)
		ids := []string{s.ID}
		for i := 0; i < len(ids); i++ {
			fixes, err := db.QueryContext(ctx, "select fix_item from acceptance where item = ? and fix_item is not null"+
				" order by id", ids[i])
			if err != nil {
				return err
			}
			for fixes.Next() {
				var f string
				if err := fixes.Scan(&f); err != nil {
					fixes.Close()
					return err
				}
				if !slices.Contains(ids, f) {
					ids = append(ids, f)
				}
			}
			fixes.Close()
			if err := fixes.Err(); err != nil {
				return err
			}
		}
		return citing(ctx, db, s, ids...)
	}
	if err := acceptances(ctx, db, s); err != nil {
		return err
	}
	return citing(ctx, db, s, s.ID)
}

// acceptances adds one line per acceptance of the item, oldest first, with the check for each criterion.
func acceptances(ctx context.Context, db *sql.DB, s *Shown) error {
	rows, err := db.QueryContext(ctx, "select ts, revision, checks, review_ref, accepted_by, state, coalesce(revoked_ts,"+
		" ''), coalesce(revoked_by, ''), coalesce(defect, ''), coalesce(fix_item, '') from acceptance where item = ? order by id",
		s.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var ts, rev, checks, review, by, state, revokedTS, revokedBy, defect, fix string
		if err := rows.Scan(&ts, &rev, &checks, &review, &by, &state, &revokedTS, &revokedBy, &defect, &fix); err != nil {
			return err
		}
		var cs []store.AcceptCheck
		if err := json.Unmarshal([]byte(checks), &cs); err != nil {
			return fmt.Errorf("acceptance of %s has bad checks %q: %w", s.ID, checks, err)
		}
		var parts []string
		for _, c := range cs {
			ref := c.Ref
			if !strings.HasPrefix(ref, "event:") {
				ref = ref[:min(len(ref), 12)] // a full sha
			}
			parts = append(parts, fmt.Sprintf("%d=%s", c.Criterion, ref))
		}
		line := fmt.Sprintf("%s %s on %s by %s · checks %s · review %s", ts, state, rev[:min(len(rev), 12)], by,
			strings.Join(parts, " "), review)
		if state == "revoked" {
			line += fmt.Sprintf(" · revoked %s by %s (%s), fix %s", revokedTS, revokedBy, defect, fix)
		}
		s.Lines = append(s.Lines, line)
	}
	return rows.Err()
}

// citing adds the events whose refs name one of the ids exactly, oldest first.
func citing(ctx context.Context, db *sql.DB, s *Shown, ids ...string) error {
	var where []string
	var args []any
	for _, id := range ids {
		where, args = append(where, "instr(refs, ?) > 0"), append(args, `"`+id+`"`)
	}
	rows, err := db.QueryContext(ctx, "select id, ts, kind, coalesce(project,''), text, refs, taint from event"+
		" where "+strings.Join(where, " or ")+" order by "+norm+", id", args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var h Hit
		var n, taint int
		var text, refs sql.NullString
		if err := rows.Scan(&n, &h.TS, &h.Kind, &h.Project, &text, &refs, &taint); err != nil {
			return err
		}
		h.ID, h.Text, h.Taint = fmt.Sprint("event:", n), text.String, taint != 0
		s.Lines = append(s.Lines, h.Line())
	}
	return rows.Err()
}

func showDecision(ctx context.Context, db *sql.DB, s *Shown, history bool) error {
	by, err := supersededBy(ctx, db)
	if err != nil {
		return err
	}
	d, ok, err := decisionRow(ctx, db, s.ID)
	if err != nil || !ok {
		s.Kind = "decisions"
		return err
	}
	s.Found = true
	s.add("Project", d.project)
	s.add("Date", d.ts)
	s.add("Decision", d.title)
	full, err := decisionEntry(d)
	if err != nil {
		s.Status, s.Reasons = "partial", append(s.Reasons, err.Error())
	} else {
		s.add("Why", full.Why)
		s.add("Decided by", full.DecidedBy)
		s.add("Supersedes", full.Supersedes)
		s.add("Reason", full.Reason)
		s.add("Overrides", full.Overrides)
		s.add("Refs", full.Refs)
	}
	if d.taint {
		s.add("Taint", "tainted")
	}
	s.add("File", fmt.Sprintf("%s:%d", d.file, d.line))
	if newer := by[s.ID]; newer != "" {
		n, _, err := decisionRow(ctx, db, newer)
		if err != nil {
			return err
		}
		s.add("Status", fmt.Sprintf("SUPERSEDED by %s (%s): %s", newer, n.ts, n.title))
	} else {
		s.add("Status", "active")
	}
	if !history {
		return nil
	}
	// The chain: back through Supersedes to the first, then forward through what supersedes each.
	first := s.ID
	for seen := map[string]bool{first: true}; ; {
		r, ok, err := decisionRow(ctx, db, first)
		if err != nil {
			return err
		}
		if !ok || r.supersedes == "" || by[r.supersedes] != first || seen[r.supersedes] {
			break
		}
		first = r.supersedes
		seen[first] = true
	}
	for id, seen := first, map[string]bool{}; id != "" && !seen[id]; id = by[id] {
		seen[id] = true
		r, ok, err := decisionRow(ctx, db, id)
		if err != nil {
			return err
		}
		if ok {
			s.Lines = append(s.Lines, fmt.Sprintf("%s %s %s · decided by %s", r.id, r.ts, r.title, r.decidedBy))
		}
	}
	return nil
}

type decRow struct {
	id, project, ts, title, decidedBy, supersedes, file string
	line                                                int
	taint                                               bool
}

func decisionRow(ctx context.Context, db *sql.DB, id string) (decRow, bool, error) {
	r := decRow{id: id}
	var project, ts, title, by, sup, file sql.NullString
	var line sql.NullInt64
	var taint int
	err := db.QueryRowContext(ctx, "select project, ts, title, decided_by, supersedes, file, line, taint from decision"+
		" where id = ?", id).Scan(&project, &ts, &title, &by, &sup, &file, &line, &taint)
	if err == sql.ErrNoRows {
		return r, false, nil
	}
	r.project, r.ts, r.title, r.decidedBy, r.supersedes = project.String, ts.String, title.String, by.String, sup.String
	r.file, r.line, r.taint = file.String, int(line.Int64), taint != 0
	return r, err == nil, err
}

// decisionEntry reads the full entry (why, reason, refs) from its DECISIONS.md; the store keeps only the title.
func decisionEntry(d decRow) (store.Decision, error) {
	b, err := os.ReadFile(d.file)
	if err != nil {
		return store.Decision{}, fmt.Errorf("cannot read %s for the full entry: %v", d.file, err)
	}
	entries, _ := store.ParseDecisions(string(b))
	for _, e := range entries {
		if e.ID == d.id {
			return e, nil
		}
	}
	return store.Decision{}, fmt.Errorf("%s has no entry %s any more", d.file, d.id)
}

func showEvent(ctx context.Context, db *sql.DB, s *Shown) error {
	var ts, project, kind, actor, session, item, refs, text, key sql.NullString
	var taint, verified int
	err := db.QueryRowContext(ctx, "select ts, project, kind, actor, session, item, refs, text, taint, verified, source_key"+
		" from event where id = ?", strings.TrimPrefix(s.ID, "event:")).Scan(&ts, &project, &kind, &actor, &session, &item,
		&refs, &text, &taint, &verified, &key)
	if err == sql.ErrNoRows {
		s.Kind = "events"
		return nil
	}
	if err != nil {
		return err
	}
	s.Found = true
	for _, f := range []Field{{"Time", ts.String}, {"Project", project.String}, {"Kind", kind.String},
		{"Actor", actor.String}, {"Session", session.String}, {"Item", item.String}, {"Refs", refs.String},
		{"Text", text.String}, {"Source", key.String}} {
		s.add(f.Name, f.Value)
	}
	if taint != 0 {
		s.add("Taint", "tainted")
	}
	if verified != 0 {
		s.add("Verified", "yes")
	}
	return nil
}

// showCommitOrSession resolves a hex prefix; more than one match is an ErrQuery listing them.
func showCommitOrSession(ctx context.Context, db *sql.DB, s *Shown) error {
	var ids []string
	for _, q := range []string{"select 'commit '||sha from git_commit where sha like ? || '%'",
		"select 'session '||id from session where id like ? || '%'"} {
		rows, err := db.QueryContext(ctx, q, s.ID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	switch {
	case len(ids) == 0:
		s.Kind = "commits, sessions"
		return nil
	case len(ids) > 1:
		if len(ids) > 5 {
			ids = append(ids[:5], fmt.Sprintf("... %d more", len(ids)-5))
		}
		return fmt.Errorf("%w: %s matches %s; give more of the id", ErrQuery, s.ID, strings.Join(ids, ", "))
	}
	kind, full, _ := strings.Cut(ids[0], " ")
	s.Kind, s.ID, s.Found = kind, full, true
	if kind == "commit" {
		var repo, project, ts, session, files, reverts, ci sql.NullString
		var isRevert int
		if err := db.QueryRowContext(ctx, "select repo, project, ts, session, files, is_revert, reverts_sha, ci from git_commit"+
			" where sha = ?", full).Scan(&repo, &project, &ts, &session, &files, &isRevert, &reverts, &ci); err != nil {
			return err
		}
		for _, f := range []Field{{"Repo", repo.String}, {"Project", project.String}, {"Time", ts.String},
			{"Session", session.String}, {"Files", files.String}, {"Reverts", reverts.String}, {"CI", ci.String}} {
			s.add(f.Name, f.Value)
		}
		return nil
	}
	var v [17]sql.NullString
	if err := db.QueryRowContext(ctx, "select project, persona, account, role, item, attrib_source, title, model_main,"+
		" started, ended, end_reason, peak_ctx, compactions, human_prompts, interrupts, parent_id, cwd from session where id = ?",
		full).Scan(&v[0], &v[1], &v[2], &v[3], &v[4], &v[5], &v[6], &v[7], &v[8], &v[9], &v[10], &v[11], &v[12], &v[13],
		&v[14], &v[15], &v[16]); err != nil {
		return err
	}
	for i, name := range []string{"Project", "Persona", "Account", "Role", "Item", "Attributed by", "Title", "Model",
		"Started", "Ended", "End", "Peak context", "Compactions", "Human prompts", "Interrupts", "Parent", "Cwd"} {
		s.add(name, v[i].String)
	}
	var n int
	var cost sql.NullFloat64
	if err := db.QueryRowContext(ctx, "select count(*), sum(cost_list_usd) from request where session = ?", full).Scan(&n,
		&cost); err != nil {
		return err
	}
	s.add("Requests", fmt.Sprintf("%d · $%.2f list", n, cost.Float64))
	return nil
}

// showProject prints the project's STATE.md (design §4); until the digest step builds it, that is unavailable. A
// name with no row in the store is no project: a true "no".
func showProject(db *sql.DB, home string, s *Shown) error {
	path := filepath.Join(home, "state", s.ID, "STATE.md")
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		known, err := knownProject(db, s.ID)
		if err != nil {
			return err
		}
		if !known {
			return nil
		}
		s.Status, s.Reasons = "unavailable", []string{path + " is not built yet (the digest step builds it)"}
		return nil
	}
	if err != nil {
		return err
	}
	s.Found, s.Body = true, string(b)
	return nil
}

// Text is the answer for people: a status line, the fields, the related lines, or the STATE.md body.
func (s *Shown) Text() string {
	var b strings.Builder
	b.WriteString(s.Status)
	if len(s.Reasons) > 0 {
		b.WriteString(" (" + strings.Join(s.Reasons, "; ") + ")")
	}
	if !s.Found {
		if s.Status == "unavailable" {
			fmt.Fprintf(&b, " · show %s\n", s.ID)
		} else {
			fmt.Fprintf(&b, " · no match in %s (id %s)\n", s.Kind, s.ID)
		}
		return b.String()
	}
	fmt.Fprintf(&b, " · %s %s\n", s.Kind, s.ID)
	for _, f := range s.Fields {
		b.WriteString(f.Name + ": " + strings.Join(strings.Fields(f.Value), " ") + "\n")
	}
	for _, l := range s.Lines {
		b.WriteString("  " + l + "\n")
	}
	b.WriteString(s.Body)
	return b.String()
}

// knownProject says whether any row of the store names the project.
func knownProject(db *sql.DB, p string) (bool, error) {
	var known bool
	err := db.QueryRow("select exists (select 1 from session where project = ?1) or exists (select 1 from event where"+
		" project = ?1) or exists (select 1 from item where project = ?1) or exists (select 1 from decision where"+
		" project = ?1) or exists (select 1 from git_commit where project = ?1) or exists (select 1 from artifact where"+
		" project = ?1)", p).Scan(&known)
	return known, err
}
