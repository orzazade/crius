// Package recall answers from the record store (design §4): find, show and health. Every answer carries a status:
// complete, partial (names the stale or failed sources), unavailable or expired; a true "no" names its scope.
package recall

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// maxAge is how old the newest complete ingest run may be: two missed 10-minute runs, as in the watchdog.
const maxAge = 20 * time.Minute

// maxHits bounds one answer; the text output is capped further by the CLI.
const maxHits = 500

const sources = "events, decisions, items, artifacts, commits, sessions"

type Query struct {
	Text, Project   string
	Since           time.Time // zero: all time (--waiting: the last 24 h)
	Until           time.Time // zero: up to now; the eval asks the store as it was when a question was written
	Kinds           []string
	Active, Waiting bool
	Now             time.Time
	Self            string // the asking session (CLAUDE_CODE_SESSION_ID): --deep ranks it last
}

type Hit struct {
	Kind         string `json:"kind"`
	ID           string `json:"id"`
	Project      string `json:"project,omitempty"`
	TS           string `json:"ts,omitempty"`
	Text         string `json:"text,omitempty"`
	Match        string `json:"match"` // exact, text, list, deep (a transcript citation) or durable (left after expiry)
	Taint        bool   `json:"taint,omitempty"`
	SupersededBy string `json:"superseded_by,omitempty"`
	WaitState    string `json:"wait_state,omitempty"` // open, or unknown when the wait names no session
	Path         string `json:"path,omitempty"`       // the transcript of a deep citation
}

type Result struct {
	Status  string   `json:"status"`
	Reasons []string `json:"reasons,omitempty"`
	Query   string   `json:"query,omitempty"`
	Scope   string   `json:"scope"`
	More    bool     `json:"more,omitempty"` // more than maxHits matched
	Hits    []Hit    `json:"hits"`
}

// Freshness is the store's status: complete when the newest ingest run is complete and at most maxAge old.
func Freshness(db *sql.DB, now time.Time) (string, []string, error) {
	var finished, status, stats sql.NullString
	err := db.QueryRow("select finished, status, stats from source_run order by id desc limit 1").Scan(&finished, &status, &stats)
	if err == sql.ErrNoRows {
		return "partial", []string{"no ingest run recorded"}, nil
	}
	if err != nil {
		return "", nil, err
	}
	if status.String != "complete" {
		why := fmt.Sprintf("last ingest run (%s) is %s", finished.String, status.String)
		var st struct{ Warnings []string }
		if json.Unmarshal([]byte(stats.String), &st) == nil && len(st.Warnings) > 0 {
			why += ": " + st.Warnings[0]
		}
		return "partial", []string{why}, nil
	}
	t, err := time.Parse(time.RFC3339, finished.String)
	if err != nil {
		return "partial", []string{fmt.Sprintf("last ingest run has a bad finish time %q", finished.String)}, nil
	}
	if age := now.Sub(t); age > maxAge {
		return "partial", []string{fmt.Sprintf("last ingest run finished %s ago (it runs every 10 min)", age.Round(time.Minute))}, nil
	}
	return "complete", nil, nil
}

var (
	itemRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*#\d+$`)
	decisionRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*-D\d+$`)
	shaRe      = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	sessionRe  = regexp.MustCompile(`^[0-9a-f]{8}(-[0-9a-f]{1,12}){0,4}$`)
	eventRe    = regexp.MustCompile(`^event:\d+$`)
	wordRe     = regexp.MustCompile(`[\p{L}\p{N}]+`)
	sidRe      = regexp.MustCompile(`\bsid=([0-9a-f]{8})\b`)
)

// docs is one row per searchable thing. Only text is indexed; listed rows show in a list with no --kind (rows with
// text, not the prompt/compaction/commit flood). Built per query in a temp table, so it can never be stale.
const docs = `
create virtual table temp.doc using fts5(kind unindexed, id unindexed, project unindexed, ts unindexed,
  taint unindexed, sup unindexed, refs unindexed, listed unindexed, disp unindexed, text, tokenize='porter unicode61');
insert into temp.doc select kind, 'event:'||id, project, ts, taint, '', coalesce(refs,''),
  coalesce(text,'') <> '', '', coalesce(text,'') from event;
insert into temp.doc select 'decision', id, project, ts, taint, coalesce(supersedes,''), '', 1, '', coalesce(title,'')
  from decision;
insert into temp.doc select 'item', id, project, opened, taint, '', '', 1, '', title||' · '||criteria from item;
insert into temp.doc select 'artifact', path, project, mtime, taint, '', '', 1, '',
  coalesce(title,'')||' · '||coalesce(headings,'') from artifact;
insert into temp.doc select 'commit', sha, project, ts, 0, '', '', 0,
  replace(repo, rtrim(repo, replace(repo, '/', '')), '')||' · '||coalesce(json_array_length(files),0)||' files', ''
  from git_commit;
`

// norm makes a day-only ts comparable with full ones.
const norm = `(case when length(ts)=10 then ts||'T00:00:00Z' else ts end)`

func stamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// Kinds are the kinds find accepts: every event kind in the store, the bus types, and the other sources.
func Kinds(db *sql.DB, busTypes map[string]bool) ([]string, error) {
	known := []string{"decision", "item", "artifact", "commit", "session"}
	for t := range busTypes {
		known = append(known, strings.ToLower(t))
	}
	rows, err := db.Query("select distinct kind from event")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		known = append(known, k)
	}
	slices.Sort(known)
	return slices.Compact(known), rows.Err()
}

// ErrQuery wraps a bad query: an unknown kind, or a flag that needs or excludes another.
var ErrQuery = errors.New("bad query")

// Find answers one query. A bad query is an ErrQuery naming the fix.
// idTrim is the punctuation around an id in a question ("(7dce2bdf)", "cf62176?").
const idTrim = ".,;:?!()[]\"'"

func Find(db *sql.DB, q Query, busTypes map[string]bool) (*Result, error) {
	known, err := Kinds(db, busTypes)
	if err != nil {
		return nil, err
	}
	for _, k := range q.Kinds {
		if !slices.Contains(known, k) {
			return nil, fmt.Errorf("%w: unknown kind %q; known kinds: %s", ErrQuery, k, strings.Join(known, ", "))
		}
	}
	if q.Active && !slices.Contains(q.Kinds, "decision") {
		return nil, fmt.Errorf("%w: --active works with --kind decision", ErrQuery)
	}
	if q.Waiting && (q.Project != "" || len(q.Kinds) > 0 || q.Text != "") {
		return nil, fmt.Errorf("%w: --waiting lists open waits across projects; it takes only --since", ErrQuery)
	}
	res := &Result{Query: q.Text, Hits: []Hit{}}
	if res.Status, res.Reasons, err = Freshness(db, q.Now); err != nil {
		return nil, err
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "drop table if exists temp.doc;"+docs); err != nil {
		return nil, fmt.Errorf("building the search index: %w", err)
	}
	defer conn.ExecContext(ctx, "drop table if exists temp.doc")

	if q.Waiting {
		return res, waits(ctx, conn, q, res)
	}
	res.Scope = scope(sources, q)

	// The filters every part of the answer shares.
	where, args := []string{"1"}, []any{}
	if q.Project != "" {
		where, args = append(where, "project = ?"), append(args, q.Project)
	}
	if !q.Since.IsZero() {
		where, args = append(where, norm+" >= ?"), append(args, stamp(q.Since))
	}
	if !q.Until.IsZero() {
		where, args = append(where, norm+" <= ?"), append(args, stamp(q.Until))
	}
	if len(q.Kinds) > 0 {
		where = append(where, "kind in ("+strings.TrimSuffix(strings.Repeat("?,", len(q.Kinds)), ",")+")")
		for _, k := range q.Kinds {
			args = append(args, k)
		}
	}
	filter := strings.Join(where, " and ")
	seen := map[string]bool{}
	add := func(match, cond, order string, extra ...any) error {
		rows, err := conn.QueryContext(ctx, "select kind, id, coalesce(project,''), coalesce(ts,''), taint, sup, disp, text"+
			" from temp.doc where "+filter+" and ("+cond+") order by "+order, append(slices.Clone(args), extra...)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var h Hit
			var taint int
			var sup, disp string
			if err := rows.Scan(&h.Kind, &h.ID, &h.Project, &h.TS, &taint, &sup, &disp, &h.Text); err != nil {
				return err
			}
			if seen[h.ID] {
				continue
			}
			if len(res.Hits) == maxHits {
				res.More = true
				break
			}
			seen[h.ID], h.Match, h.Taint = true, match, taint != 0
			if disp != "" {
				h.Text = disp
			}
			res.Hits = append(res.Hits, h)
		}
		return rows.Err()
	}

	// Sessions are not in the word index (titles are not records): a word query on them alone could only say "no".
	// The words as the id lookup below reads them, punctuation trimmed.
	var toks []string
	for _, tok := range strings.Fields(q.Text) {
		toks = append(toks, strings.Trim(tok, idTrim))
	}
	if slices.Equal(q.Kinds, []string{"session"}) && wordRe.MatchString(q.Text) && !slices.ContainsFunc(toks, sessionRe.MatchString) {
		return nil, fmt.Errorf("%w: sessions are found by id prefix or listed with no query; words do not search them", ErrQuery)
	}
	if slices.Contains(q.Kinds, "session") && slices.ContainsFunc(toks, func(w string) bool {
		return wordRe.MatchString(w) && !sessionRe.MatchString(w)
	}) {
		res.Status = "partial"
		res.Reasons = append(res.Reasons, "sessions are not searched by words (give an id prefix, or list them with no query)")
	}
	// 1. Exact ids, and the events that cite them: an item or decision id whole, a sha by prefix either way (refs
	// hold short shas). An item, decision or event id is not searched as words: no row for it is a true "no".
	words := q.Text
	for _, tok := range toks {
		cite := `id <> '' and instr(refs, '"' || ? || '"') > 0`
		citeSha := `id <> '' and case when json_valid(refs) then exists (select 1 from json_tree(refs) j where` +
			` j.type = 'text' and length(j.atom) >= 7 and (j.atom like ? || '%' or ? like j.atom || '%')) end`
		var err error
		if itemRe.MatchString(tok) || decisionRe.MatchString(tok) || eventRe.MatchString(tok) {
			words = strings.Replace(words, tok, " ", 1)
		}
		switch {
		case itemRe.MatchString(tok):
			err = add("exact", "kind = 'item' and id = ?", "id", tok)
		case decisionRe.MatchString(tok):
			err = add("exact", "id = ? and id not like 'event:%'", "id", tok)
		case eventRe.MatchString(tok):
			err = add("exact", "id = ?", "id", tok)
		}
		if err == nil && shaRe.MatchString(tok) {
			err = add("exact", "kind = 'commit' and id like ? || '%'", norm+" desc", tok)
		}
		if err == nil && sessionRe.MatchString(tok) {
			err = session(ctx, conn, q, tok, "exact", res, seen)
		}
		if err == nil && (itemRe.MatchString(tok) || decisionRe.MatchString(tok)) {
			err = add("exact", cite, norm+" desc", tok)
		}
		if err == nil && shaRe.MatchString(tok) {
			err = add("exact", citeSha, norm+" desc", tok, tok)
		}
		if err != nil {
			return nil, err
		}
	}
	// 2. Full-text, best match first; 3. with no words, a list, newest first.
	var terms []string
	for _, w := range wordRe.FindAllString(strings.ToLower(words), -1) {
		terms = append(terms, `"`+w+`"`)
	}
	if len(terms) > 0 {
		err = add("text", "doc match ?", "rank, "+norm+" desc", strings.Join(terms, " OR "))
	} else if q.Text == "" {
		cond := "listed"
		if len(q.Kinds) > 0 {
			cond = "1"
		}
		err = add("list", cond, norm+" desc, id desc")
		if err == nil && slices.Contains(q.Kinds, "session") {
			err = session(ctx, conn, q, "", "list", res, seen)
		}
	}
	if err != nil {
		return nil, err
	}
	return res, supersede(ctx, conn, q, res)
}

// session adds the sessions whose id starts with tok (they are not in the text index: titles are not records).
func session(ctx context.Context, conn *sql.Conn, q Query, tok, match string, res *Result, seen map[string]bool) error {
	cond, args := "id like ? || '%'", []any{tok}
	if q.Project != "" {
		cond, args = cond+" and project = ?", append(args, q.Project)
	}
	if !q.Since.IsZero() {
		cond, args = cond+" and started >= ?", append(args, stamp(q.Since))
	}
	if !q.Until.IsZero() {
		cond, args = cond+" and started <= ?", append(args, stamp(q.Until))
	}
	if len(q.Kinds) > 0 && !slices.Contains(q.Kinds, "session") {
		return nil
	}
	rows, err := conn.QueryContext(ctx, "select id, coalesce(project,''), coalesce(started,''), coalesce(title,''), taint"+
		" from session where "+cond+" order by started desc", args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		h := Hit{Kind: "session", Match: match}
		var taint int
		if err := rows.Scan(&h.ID, &h.Project, &h.TS, &h.Text, &taint); err != nil {
			return err
		}
		if seen[h.ID] {
			continue
		}
		if len(res.Hits) == maxHits {
			res.More = true
			break
		}
		seen[h.ID], h.Taint = true, taint != 0
		res.Hits = append(res.Hits, h)
	}
	return rows.Err()
}

// supersede marks each decision a later decision of its project supersedes; --active drops those.
func supersede(ctx context.Context, conn *sql.Conn, q Query, res *Result) error {
	by, err := supersededBy(ctx, conn)
	if err != nil {
		return err
	}
	kept := res.Hits[:0]
	for _, h := range res.Hits {
		if h.Kind == "decision" && !strings.HasPrefix(h.ID, "event:") {
			h.SupersededBy = by[h.ID]
		}
		if !(q.Active && h.SupersededBy != "") {
			kept = append(kept, h)
		}
	}
	res.Hits = kept
	return nil
}

func splitD(id string) (string, int) {
	i := strings.LastIndex(id, "-D")
	if i <= 0 {
		return "", 0
	}
	n, err := strconv.Atoi(id[i+2:])
	if err != nil {
		return "", 0
	}
	return id[:i], n
}

// waits lists WAIT lines still open: a wait with sid=<8 hex> is closed once that session makes an API call a minute
// or more after it; a wait that names no session is listed as unknown.
func waits(ctx context.Context, conn *sql.Conn, q Query, res *Result) error {
	since := q.Since
	if since.IsZero() {
		since = q.Now.Add(-24 * time.Hour)
	}
	res.Scope = scope("waits", Query{Since: since})
	rows, err := conn.QueryContext(ctx, "select id, coalesce(project,''), ts, text, taint from temp.doc where kind = 'wait'"+
		" and "+norm+" >= ? order by "+norm+" desc, id desc", stamp(since))
	if err != nil {
		return err
	}
	var all []Hit
	for rows.Next() {
		h := Hit{Kind: "wait", Match: "list"}
		var taint int
		if err := rows.Scan(&h.ID, &h.Project, &h.TS, &h.Text, &taint); err != nil {
			rows.Close()
			return err
		}
		h.Taint = taint != 0
		all = append(all, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, h := range all {
		m := sidRe.FindStringSubmatch(h.Text)
		if m == nil {
			h.WaitState = "unknown"
		} else {
			t, err := time.Parse(time.RFC3339, h.TS)
			if err != nil {
				return fmt.Errorf("wait %s has a bad time %q", h.ID, h.TS)
			}
			var one int
			err = conn.QueryRowContext(ctx, "select 1 from request where session like ? || '%' and ts_first >= ? limit 1",
				m[1], stamp(t.Add(time.Minute))).Scan(&one)
			if err == nil {
				continue
			}
			if err != sql.ErrNoRows {
				return err
			}
			h.WaitState = "open"
		}
		if len(res.Hits) == maxHits {
			res.More = true
			break
		}
		res.Hits = append(res.Hits, h)
	}
	return nil
}

func scope(what string, q Query) string {
	project, since := "all", "all time"
	if q.Project != "" {
		project = q.Project
	}
	if !q.Since.IsZero() {
		since = q.Since.UTC().Format("2006-01-02 15:04") + " UTC"
	}
	s := fmt.Sprintf("%s (project %s, since %s", what, project, since)
	if len(q.Kinds) > 0 {
		s += ", kind " + strings.Join(q.Kinds, "|")
	}
	return s + ")"
}

// Text is the answer for people: a status line, then one line per hit. The CLI caps it at 20 lines.
func (r *Result) Text() string {
	var b strings.Builder
	b.WriteString(r.Status)
	if len(r.Reasons) > 0 {
		b.WriteString(" (" + strings.Join(r.Reasons, "; ") + ")")
	}
	if r.Query != "" {
		fmt.Fprintf(&b, " · find %q", r.Query)
	}
	if len(r.Hits) == 0 {
		b.WriteString(" · no match in " + r.Scope + "\n")
		return b.String()
	}
	n := strconv.Itoa(len(r.Hits))
	if r.More {
		n += "+"
	}
	fmt.Fprintf(&b, " · %s hits in %s\n", n, r.Scope)
	for _, h := range r.Hits {
		b.WriteString(h.Line() + "\n")
	}
	return b.String()
}

// Line is one hit on one line.
func (h Hit) Line() string {
	ts := strings.Replace(h.TS, "T", " ", 1)
	if len(ts) > 16 {
		ts = ts[:16]
	}
	s := strings.TrimSpace(ts + " " + h.Kind + " " + h.ID)
	if h.Project != "" {
		s += " [" + h.Project + "]"
	}
	if h.WaitState != "" {
		s += " " + h.WaitState
	}
	if t := strings.Join(strings.Fields(h.Text), " "); t != "" {
		s += " — " + t
	}
	if h.SupersededBy != "" {
		s += " · superseded by " + h.SupersededBy
	}
	if h.Taint {
		s += " · tainted"
	}
	if h.Path != "" {
		s += " · " + h.Path
	}
	return s
}
