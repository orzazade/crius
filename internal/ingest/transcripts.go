package ingest

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/orzazade/crius/internal/store"
)

// knownTypes are every top-level line type seen in real transcripts on 2026-10-16 (Claude Code 2.x). Others are
// counted and alerted.
var knownTypes = map[string]bool{}

func init() {
	for _, t := range strings.Fields(`assistant user system attachment atis-latch last-prompt mode permission-mode
		queue-operation custom-title agent-name api-request-blob api-request agent-setting ai-title bridge-session
		file-history-snapshot file-history-delta cost-state api-request-shape artifact-autoreact-ledger
		artifact-comment-monitor summary fork-context-ref frame-link started result launched pr-link dev-mods`) {
		knownTypes[t] = true
	}
}

var (
	tagRe       = regexp.MustCompile(`(?m)^\[crius((?: [a-z]+=[^\s\]]+)+)\][ \t]*$`)
	commitOutRe = regexp.MustCompile(`(?m)^\[([^\]\s]+)(?: \(root-commit\))? ([0-9a-f]{7,40})\] `)
	hookErrRe   = regexp.MustCompile(`(?i)hook|blocked|denied`)
	tagFields   = map[string]*regexp.Regexp{
		"account": regexp.MustCompile(`^(max|enterprise)$`), "project": store.Slug, "role": store.Slug,
		"parent": regexp.MustCompile(`^[0-9a-f-]{8,36}$`), "item": regexp.MustCompile(`^[a-z0-9][a-z0-9-]*#\d+$`),
	}
)

// parseTag returns the last valid provenance tag line in a prompt, or nil. Only allowlisted fields pass.
func parseTag(text string) map[string]string {
	var found map[string]string
	for _, m := range tagRe.FindAllStringSubmatch(text, -1) {
		fields, ok := map[string]string{}, true
		for _, kv := range strings.Fields(m[1]) {
			k, v, _ := strings.Cut(kv, "=")
			re := tagFields[k]
			if re == nil || !re.MatchString(v) {
				ok = false
				break
			}
			fields[k] = v
		}
		if ok && fields["account"] != "" && fields["project"] != "" && fields["role"] != "" {
			found = fields
		}
	}
	return found
}

type txLine struct {
	Type             string          `json:"type"`
	Subtype          string          `json:"subtype"`
	Timestamp        string          `json:"timestamp"`
	UUID             string          `json:"uuid"`
	RequestID        string          `json:"requestId"`
	IsSidechain      bool            `json:"isSidechain"`
	IsMeta           bool            `json:"isMeta"`
	IsCompactSummary bool            `json:"isCompactSummary"`
	CWD              string          `json:"cwd"`
	CustomTitle      string          `json:"customTitle"`
	Origin           json.RawMessage `json:"origin"`
	TurnOrigin       json.RawMessage `json:"turnOrigin"`
	Message          json.RawMessage `json:"message"`
}

type txMessage struct {
	ID    string `json:"id"`
	Model string `json:"model"`
	Usage *struct {
		In            int64 `json:"input_tokens"`
		Out           int64 `json:"output_tokens"`
		CacheRead     int64 `json:"cache_read_input_tokens"`
		CacheWrite    int64 `json:"cache_creation_input_tokens"`
		CacheCreation struct {
			H1 int64 `json:"ephemeral_1h_input_tokens"`
			M5 int64 `json:"ephemeral_5m_input_tokens"`
		} `json:"cache_creation"`
	} `json:"usage"`
	Content json.RawMessage `json:"content"`
}

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
}

type reqAgg struct {
	first, last, model, uuid    string
	in, out, cr, cw, cw1h, cw5m int64
}

func isObject(raw json.RawMessage) bool {
	s := strings.TrimLeft(string(raw), " \t\r\n")
	return strings.HasPrefix(s, "{")
}

func (r *runner) ingestTranscripts() {
	for _, rt := range r.cfg.roots {
		// WalkDir does not follow a root that is a symlink, so resolve it first.
		real, err := filepath.EvalSymlinks(rt.path)
		if err != nil {
			r.warn(true, "cannot read %s: %v", rt.path, err)
			continue
		}
		rt.path = real
		err = filepath.WalkDir(rt.path, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				r.warn(true, "cannot read %s: %v", path, err)
				if d != nil && d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				if skipTranscriptDir(d.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(path, ".jsonl") {
				if err := r.transcriptFile(rt, path); err != nil {
					r.warn(true, "cannot ingest %s: %v", path, err)
				}
			}
			return nil
		})
		if err != nil {
			r.warn(true, "cannot walk %s: %v", rt.path, err)
		}
	}
	if err := r.refile(); err != nil {
		r.warn(true, "re-filing sessions by folder or title: %v", err)
	}
}

// refile re-derives the filing of sessions filed by their folder or title when projects.toml or the title rules now
// file them differently. rollUp runs only for a file with new lines, so without this a finished session keeps a stale
// project or role.
func (r *runner) refile() error {
	// A registered session's events from before its first tag are filed by its folder too.
	// The role of a registered session is its tag's: '' leaves it out of the comparison.
	rows, err := r.db.Query("select id, coalesce(cwd,''), coalesce(title,''), coalesce(project,''), role from session" +
		" where attrib_source in ('cwd','title','unknown') union select distinct s.id, coalesce(s.cwd,''), coalesce(s.title,'')," +
		" coalesce(e.project,''), '' from (select session, min(from_ts) f from assignment group by session) a join session s" +
		" on s.id = a.session join event e on e.session = a.session and e.ts < a.f")
	if err != nil {
		return err
	}
	var stale []string
	for rows.Next() {
		var id, cwd, title, project, role string
		if err := rows.Scan(&id, &cwd, &title, &project, &role); err != nil {
			rows.Close()
			return err
		}
		filed, titleRole, _ := r.cfg.filing(cwd, title)
		if (filed != project || role != "" && titleRole != role) && !slices.Contains(stale, id) {
			stale = append(stale, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(stale) == 0 {
		return err
	}
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, sid := range stale {
		if err := r.rollUp(tx, sid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *runner) transcriptFile(rt root, path string) error {
	rel, err := filepath.Rel(rt.path, path)
	if err != nil {
		return err
	}
	parts := strings.Split(rel, string(filepath.Separator))
	sub := false
	for _, p := range parts {
		sub = sub || p == "subagents"
	}
	sid := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	qsrc := "main"
	if sub {
		if len(parts) < 2 {
			return fmt.Errorf("subagent file outside a session folder")
		}
		sid = parts[1]
		qsrc = "subagent:unknown"
		var meta struct {
			AgentType string `json:"agentType"`
		}
		if b, err := os.ReadFile(strings.TrimSuffix(path, ".jsonl") + ".meta.json"); err == nil &&
			json.Unmarshal(b, &meta) == nil && meta.AgentType != "" {
			qsrc = "subagent:" + truncate(r.text(meta.AgentType), 40)
		}
	}
	return r.inTx(func(tx *sql.Tx) error {
		c, err := getCursor(tx, "transcript", path)
		if err != nil {
			return err
		}
		reqs := map[string]*reqAgg{}
		var order []string
		var first, last, cwd, title string
		unknown := map[string]int{}
		bad := 0
		next, n, err := scanLines(path, c, func(pos int64, raw []byte) error {
			var o txLine
			if err := json.Unmarshal(raw, &o); err != nil {
				bad++
				return nil
			}
			if !knownTypes[o.Type] {
				unknown[truncate(o.Type, 40)]++
				return nil
			}
			ts := o.Timestamp
			if !sub {
				if o.Type == "custom-title" && o.CustomTitle != "" {
					title = r.text(o.CustomTitle)
				}
				if ts != "" && (o.Type == "user" || o.Type == "assistant" || o.Type == "system") {
					if first == "" || ts < first {
						first = ts
					}
					if ts > last {
						last = ts
					}
					if cwd == "" {
						cwd = r.text(o.CWD)
					}
				}
			}
			var m txMessage
			if isObject(o.Message) {
				if err := json.Unmarshal(o.Message, &m); err != nil {
					bad++
					return nil
				}
			}
			switch {
			case o.Type == "assistant":
				req := o.RequestID
				if req == "" {
					req = m.ID
				}
				if m.Usage != nil && req != "" && m.Model == "<synthetic>" {
					r.st.Synthetic++
				} else if m.Usage != nil && req != "" && (sub || !o.IsSidechain) {
					a := reqs[req]
					if a == nil {
						a = &reqAgg{first: ts, last: ts, uuid: o.UUID}
						reqs[req] = a
						order = append(order, req)
					}
					if a.model == "" {
						a.model = r.text(m.Model)
					}
					if ts != "" {
						if a.first == "" || ts < a.first {
							a.first = ts
						}
						if ts > a.last {
							a.last = ts
						}
					}
					u := m.Usage
					a.in, a.out, a.cr, a.cw = max(a.in, u.In), max(a.out, u.Out), max(a.cr, u.CacheRead), max(a.cw, u.CacheWrite)
					a.cw1h, a.cw5m = max(a.cw1h, u.CacheCreation.H1), max(a.cw5m, u.CacheCreation.M5)
				}
				var blocks []block
				if json.Unmarshal(m.Content, &blocks) == nil {
					for _, b := range blocks {
						if b.Type == "tool_use" && b.Name == "AskUserQuestion" {
							if err := r.event(tx, event{ts: ts, kind: "ask_user", key: "tx:ask:" + b.ID, session: sid}); err != nil {
								return err
							}
						}
					}
				}
			case o.Type == "user":
				return r.userLine(tx, o, m, sid, sub)
			case o.Type == "system" && o.Subtype == "compact_boundary" && !sub:
				return r.event(tx, event{ts: ts, kind: "compaction", key: "tx:" + o.UUID, session: sid})
			}
			return nil
		})
		if err != nil {
			return err
		}
		if next == c {
			return nil
		}
		for _, req := range order {
			if err := upsertRequest(tx, req, sid, qsrc, path, reqs[req]); err != nil {
				return err
			}
		}
		if !sub && (first != "" || title != "") {
			if _, err := tx.Exec("insert into session (id, persona, started, ended, cwd, title, transcript_path)"+
				" values (?,?,?,?,?,?,?) on conflict(id) do update set"+
				" started=coalesce(min(session.started, excluded.started), session.started, excluded.started),"+
				" ended=coalesce(max(session.ended, excluded.ended), session.ended, excluded.ended),"+
				" cwd=coalesce(session.cwd, excluded.cwd), title=coalesce(excluded.title, session.title)",
				sid, rt.persona, nullable(first), nullable(last), nullable(cwd), nullable(title), path); err != nil {
				return err
			}
		}
		if err := r.rollUp(tx, sid); err != nil {
			return err
		}
		for t, k := range unknown {
			r.st.UnknownTypes[t] += k
			if err := r.queueAlert(tx, "unknown-type:"+t, fmt.Sprintf("unknown transcript line type '%s' (%d lines in %s);"+
				" Claude Code format may have changed; rows from these lines are missing", t, k, filepath.Base(path))); err != nil {
				return err
			}
		}
		if bad > 0 {
			r.st.BadLines += bad
			if err := r.queueAlert(tx, "bad-json:"+path, fmt.Sprintf("%d transcript lines in %s are not valid JSON;"+
				" they were skipped", bad, filepath.Base(path))); err != nil {
				return err
			}
		}
		r.touched[sid] = true
		return saveCursor(tx, "transcript", path, next, n)
	})
}

func (r *runner) userLine(tx *sql.Tx, o txLine, m txMessage, sid string, sub bool) error {
	ts := o.Timestamp
	var blocks []block
	isList := json.Unmarshal(m.Content, &blocks) == nil
	hasResult := false
	for _, b := range blocks {
		hasResult = hasResult || b.Type == "tool_result"
	}
	if isList && hasResult {
		for _, b := range blocks {
			if b.Type != "tool_result" {
				continue
			}
			var body string
			if json.Unmarshal(b.Content, &body) != nil {
				body = string(b.Content)
			}
			if b.IsError && hookErrRe.MatchString(head(body, 600)) {
				if err := r.event(tx, event{ts: ts, kind: "hook_error", key: "tx:hook:" + b.ToolUseID, session: sid}); err != nil {
					return err
				}
			}
			for _, cm := range commitOutRe.FindAllStringSubmatch(head(body, 2000), -1) {
				if err := r.event(tx, event{ts: ts, kind: "commit_made", key: "tx:commit:" + o.UUID + ":" + cm[2],
					session: sid, refs: map[string][]string{"sha": {cm[2]}, "branch": {truncate(cm[1], 100)}}}); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if sub || o.IsSidechain || o.IsMeta || o.IsCompactSummary {
		return nil
	}
	var text string
	if json.Unmarshal(m.Content, &text) != nil {
		var parts []string
		for _, b := range blocks {
			if b.Type == "text" {
				parts = append(parts, b.Text)
			}
		}
		if len(parts) == 0 {
			return nil
		}
		text = strings.Join(parts, "\n")
	}
	if strings.HasPrefix(text, "[Request interrupted") {
		return r.event(tx, event{ts: ts, kind: "interrupt", key: "tx:" + o.UUID, session: sid})
	}
	var origin struct {
		Kind string `json:"kind"`
	}
	actor := ""
	if json.Unmarshal(o.Origin, &origin) == nil {
		actor = origin.Kind
	}
	if actor == "" {
		var turn string
		if json.Unmarshal(o.TurnOrigin, &turn) == nil {
			actor = turn
		}
	}
	if actor == "" {
		actor = "unknown"
	}
	if err := r.event(tx, event{ts: ts, kind: "prompt", key: "tx:" + o.UUID, session: sid, actor: truncate(actor, 30)}); err != nil {
		return err
	}
	if tag := parseTag(text); tag != nil && ts != "" {
		for k, v := range tag {
			tag[k] = r.text(v) // the checks allow slugs, and a slug can look like a key
		}
		_, err := tx.Exec("insert or ignore into assignment (session, item, role, account, project, parent, from_ts,"+
			" source) values (?,?,?,?,?,?,?,'launch')", sid, nullable(tag["item"]), tag["role"], tag["account"],
			tag["project"], nullable(tag["parent"]), ts)
		return err
	}
	return nil
}

func upsertRequest(tx *sql.Tx, req, sid, qsrc, path string, a *reqAgg) error {
	if _, err := tx.Exec("insert into request (request_id, session, ts_first, ts_last, model, query_source, tok_in,"+
		" tok_out, tok_cache_read, tok_cache_write, tok_cw_1h, tok_cw_5m, transcript_path, line_uuid)"+
		" values (?,?,?,?,?,?,?,?,?,?,?,?,?,?) on conflict(request_id) do update set"+
		" ts_first=coalesce(min(request.ts_first, excluded.ts_first), request.ts_first, excluded.ts_first),"+
		" ts_last=coalesce(max(request.ts_last, excluded.ts_last), request.ts_last, excluded.ts_last),"+
		" tok_in=max(tok_in, excluded.tok_in), tok_out=max(tok_out, excluded.tok_out),"+
		" tok_cache_read=max(tok_cache_read, excluded.tok_cache_read),"+
		" tok_cache_write=max(tok_cache_write, excluded.tok_cache_write),"+
		" tok_cw_1h=max(tok_cw_1h, excluded.tok_cw_1h), tok_cw_5m=max(tok_cw_5m, excluded.tok_cw_5m),"+
		" model=coalesce(nullif(request.model, ''), excluded.model)",
		req, sid, nullable(a.first), nullable(a.last), a.model, qsrc, a.in, a.out, a.cr, a.cw, a.cw1h, a.cw5m, path,
		nullable(a.uuid)); err != nil {
		return err
	}
	var model string
	var in, out, cr, cw, c1, c5 int64
	if err := tx.QueryRow("select coalesce(model, ''), tok_in, tok_out, tok_cache_read, tok_cache_write, tok_cw_1h, tok_cw_5m"+
		" from request where request_id=?", req).Scan(&model, &in, &out, &cr, &cw, &c1, &c5); err != nil {
		return err
	}
	var cost any
	if usd, ok := ListCost(model, in, cr, cw, c1, c5, out); ok {
		cost = usd
	}
	_, err := tx.Exec("update request set cost_list_usd=? where request_id=?", cost, req)
	return err
}

// rollUp recomputes one session's assignments, attribution, counters and event projects.
func (r *runner) rollUp(tx *sql.Tx, sid string) error {
	rows, err := tx.Query("select rowid, from_ts from assignment where session=? order by from_ts", sid)
	if err != nil {
		return err
	}
	type asg struct {
		id   int64
		from string
	}
	var as []asg
	for rows.Next() {
		var a asg
		if err := rows.Scan(&a.id, &a.from); err != nil {
			rows.Close()
			return err
		}
		as = append(as, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for i, a := range as {
		source, to := "switch", any(nil)
		if i == 0 {
			source = "launch"
		}
		if i+1 < len(as) {
			to = as[i+1].from
		}
		if _, err := tx.Exec("update assignment set source=?, to_ts=? where rowid=?", source, to, a.id); err != nil {
			return err
		}
	}
	var cwd, title sql.NullString
	switch err := tx.QueryRow("select cwd, title from session where id=?", sid).Scan(&cwd, &title); err {
	case nil:
	case sql.ErrNoRows:
		return nil
	default:
		return err
	}
	filed, titleRole, byTitle := r.cfg.filing(cwd.String, title.String)
	var attrib, account, project, role string
	var parent, item sql.NullString
	err = tx.QueryRow("select account, project, role, parent, item from assignment where session=?"+
		" order by from_ts desc limit 1", sid).Scan(&account, &project, &role, &parent, &item)
	switch {
	case err == nil:
		attrib = "registered"
	case err == sql.ErrNoRows:
		attrib, account, project, role = "unknown", "unknown", filed, titleRole
		switch {
		case byTitle:
			attrib = "title"
		case filed != "":
			attrib = "cwd"
		}
	default:
		return err
	}
	counts := map[string]int{}
	crows, err := tx.Query("select kind || ':' || coalesce(actor, ''), count(*) from event where session=? group by 1", sid)
	if err != nil {
		return err
	}
	for crows.Next() {
		var k string
		var n int
		if err := crows.Scan(&k, &n); err != nil {
			crows.Close()
			return err
		}
		counts[k] = n
	}
	crows.Close()
	if err := crows.Err(); err != nil {
		return err
	}
	var modelMain sql.NullString
	var peak sql.NullInt64
	if err := tx.QueryRow("select (select model from request where session=?1 and query_source='main' group by model"+
		" order by count(*) desc limit 1), (select max(tok_in + tok_cache_read + tok_cache_write) from request"+
		" where session=?1 and query_source='main')", sid).Scan(&modelMain, &peak); err != nil {
		return err
	}
	if _, err := tx.Exec("update session set attrib_source=?, account=?, project=?, role=?, parent_id=?, item=?,"+
		" human_prompts=?, peer_prompts=?, interrupts=?, compactions=?, hook_errors=?, model_main=?, peak_ctx=?"+
		" where id=?", attrib, account, nullable(project), role, parent, item, counts["prompt:human"],
		counts["prompt:peer"], counts["interrupt:"], counts["compaction:"], counts["hook_error:"], modelMain, peak,
		sid); err != nil {
		return err
	}
	// Each event keeps the project of the assignment that was active at its time; before the first tag, the
	// project the session is filed by (its title's or its folder's).
	_, err = tx.Exec("update event set project=coalesce((select a.project from assignment a where a.session=event.session"+
		" and a.from_ts <= event.ts order by a.from_ts desc limit 1), ?) where session=?", nullable(filed), sid)
	return err
}

// measuredAccount sets each session's account from the newest statusline line seen for it, when there is one: rate
// limits mean Max, a response without them enterprise. What the statusline saw wins over a tag (the tag's account
// stays on the assignment, where the guard compares the two). It runs after rollUp, which resets the account.
func measuredAccount(tx *sql.Tx) error {
	_, err := tx.Exec("update session set account=(select case rl when 1 then 'max' else 'enterprise' end from" +
		" account_seen where session=session.id order by ts desc, rowid desc limit 1) where exists (select 1 from" +
		" account_seen where session=session.id)")
	return err
}

func head(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
