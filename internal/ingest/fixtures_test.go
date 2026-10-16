package ingest

// Synthetic transcript lines and a test home. No real transcript text, ever.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius"
	"github.com/orzazade/crius/internal/store"
	"github.com/orzazade/crius/internal/testutil"
)

const secretPrompt = "SYNTHETIC-PROMPT-TEXT-MUST-NOT-BE-STORED"

type A struct {
	Req, TS, Model, UUID string
	Out, In, CR, CW      int
	Tools                [][2]string // id, name
	Sidechain            bool
}

func assistant(a A) map[string]any {
	if a.Out == 0 {
		a.Out = 10
	}
	if a.In == 0 {
		a.In = 5
	}
	if a.Model == "" {
		a.Model = "claude-opus-5-5"
	}
	if a.UUID == "" {
		a.UUID = fmt.Sprintf("a-%s-%s-%d", a.Req, a.TS, a.Out)
	}
	content := []any{map[string]any{"type": "text", "text": "synthetic answer"}}
	for _, t := range a.Tools {
		content = append(content, map[string]any{"type": "tool_use", "id": t[0], "name": t[1], "input": map[string]any{}})
	}
	return map[string]any{"type": "assistant", "uuid": a.UUID, "timestamp": a.TS, "isSidechain": a.Sidechain,
		"requestId": a.Req, "cwd": "/nowhere",
		"message": map[string]any{"id": "msg-" + a.Req, "model": a.Model, "role": "assistant", "content": content,
			"usage": map[string]any{"input_tokens": a.In, "output_tokens": a.Out, "cache_read_input_tokens": a.CR,
				"cache_creation_input_tokens": a.CW}}}
}

func prompt(uuid, ts, text, kind, cwd string) map[string]any {
	if text == "" {
		text = secretPrompt
	}
	if kind == "" {
		kind = "human"
	}
	if cwd == "" {
		cwd = "/nowhere"
	}
	return map[string]any{"type": "user", "uuid": uuid, "timestamp": ts, "isSidechain": false, "cwd": cwd,
		"origin": map[string]any{"kind": kind}, "message": map[string]any{"role": "user", "content": text}}
}

func toolResult(uuid, ts, tid, text string, isError bool) map[string]any {
	return map[string]any{"type": "user", "uuid": uuid, "timestamp": ts, "isSidechain": false,
		"message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result",
			"tool_use_id": tid, "content": text, "is_error": isError}}}}
}

func compaction(uuid, ts string) map[string]any {
	return map[string]any{"type": "system", "subtype": "compact_boundary", "uuid": uuid, "timestamp": ts,
		"compactMetadata": map[string]any{"trigger": "auto", "preTokens": 300000}}
}

func lines(recs ...map[string]any) string {
	var b strings.Builder
	for _, r := range recs {
		j, _ := json.Marshal(r)
		b.Write(j)
		b.WriteByte('\n')
	}
	return b.String()
}

func appendFile(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

type cfg struct {
	repo, artifactDir, teamLogs, cwd, extra string
}

func writeConfig(t *testing.T, home, personal, work string, c cfg) {
	if c.cwd == "" {
		c.cwd = "/work/demo"
	}
	list := func(s string) string {
		if s == "" {
			return "[]"
		}
		var q []string
		for _, p := range strings.Split(s, ",") {
			q = append(q, fmt.Sprintf("%q", p))
		}
		return "[" + strings.Join(q, ", ") + "]"
	}
	testutil.Write(t, filepath.Join(home, "projects.toml"), fmt.Sprintf(`[transcripts]
personal = %q
work = %q

[projects.demo]
persona = "personal"
accounts = ["max"]
cwd = [%q]
repos = %s
team_logs = %s
artifact_dirs = %s
%s`, personal, work, c.cwd, list(c.repo), list(c.teamLogs), list(c.artifactDir), c.extra))
}

type ic struct {
	t                   *testing.T
	root, home          string
	personal, work, sid string
	tx                  string
}

func newIC(t *testing.T, c cfg) *ic {
	root, home := testutil.Temp(t)
	if err := store.InitLayout(home, crius.ProjectsExample); err != nil {
		t.Fatal(err)
	}
	x := &ic{t: t, root: root, home: home, personal: filepath.Join(root, "personal"), work: filepath.Join(root, "work"),
		sid: "11111111-2222-3333-4444-555555555555"}
	os.MkdirAll(x.personal, 0o755)
	os.MkdirAll(x.work, 0o755)
	x.tx = filepath.Join(x.personal, "-work-demo", x.sid+".jsonl")
	writeConfig(t, home, x.personal, x.work, c)
	return x
}

func (x *ic) run() *Stats {
	x.t.Helper()
	st, err := Run(x.home)
	if err != nil {
		x.t.Fatal(err)
	}
	return st
}

// q runs a query and returns each row as strings (NULL is "<nil>").
func (x *ic) q(query string, args ...any) [][]string {
	x.t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(x.home, "state", "record.db"))
	if err != nil {
		x.t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(query, args...)
	if err != nil {
		x.t.Fatal(err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out [][]string
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			x.t.Fatal(err)
		}
		row := make([]string, len(cols))
		for i, v := range vals {
			switch v := v.(type) {
			case nil:
				row[i] = "<nil>"
			case []byte:
				row[i] = string(v)
			default:
				row[i] = fmt.Sprint(v)
			}
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		x.t.Fatal(err)
	}
	return out
}

func (x *ic) one(query string, args ...any) string {
	x.t.Helper()
	r := x.q(query, args...)
	if len(r) != 1 || len(r[0]) != 1 {
		x.t.Fatalf("%s: %v", query, r)
	}
	return r[0][0]
}

var durable = []string{"request", "session", "assignment", "event", "decision", "git_commit", "evidence", "artifact", "item"}

func (x *ic) counts() map[string]string {
	m := map[string]string{}
	for _, tb := range durable {
		m[tb] = x.one(`select count(*) from "` + tb + `"`)
	}
	return m
}

func age(t *testing.T, path string, ago time.Duration, now time.Time) {
	t.Helper()
	ts := now.Add(-ago)
	if err := os.Chtimes(path, ts, ts); err != nil {
		t.Fatal(err)
	}
}
