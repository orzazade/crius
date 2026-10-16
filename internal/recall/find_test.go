package recall

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/store"
	"github.com/orzazade/crius/internal/testutil"
)

var now = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

// open gives a store with one complete ingest run that finished a minute before now.
func open(t *testing.T) *sql.DB {
	t.Helper()
	_, home := testutil.Temp(t)
	if err := os.MkdirAll(filepath.Join(home, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	exec(t, db, "insert into source_run (started, finished, status, stats) values (?,?,?,?)",
		"2026-10-16T11:58:00Z", "2026-10-16T11:59:00Z", "complete", "{}")
	return db
}

func exec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatal(q, err)
	}
}

var seq int

func event(t *testing.T, db *sql.DB, ts, project, kind, text string, refs string) int64 {
	t.Helper()
	seq++
	r, err := db.Exec("insert into event (ts, project, kind, actor, text, refs, source_key) values (?,?,?,?,?,?,?)",
		ts, project, kind, "leader", text, refs, fmt.Sprint("k", seq))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := r.LastInsertId()
	return id
}

func find(t *testing.T, db *sql.DB, q Query) *Result {
	t.Helper()
	if q.Now.IsZero() {
		q.Now = now
	}
	r, err := Find(db, q, store.Types)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestFindRanksAnExactIDFirstThenText(t *testing.T) {
	db := open(t)
	exec(t, db, `insert into item (id, project, n, kind, title, criteria, opened) values ('demo#3','demo',3,'code','mail poll fix','["CI"]','2026-10-15T09:00:00Z')`)
	event(t, db, "2026-10-15T10:00:00Z", "demo", "done", "item=demo#3 mail poll fixed", `{"item":["demo#3"]}`)
	event(t, db, "2026-10-15T11:00:00Z", "demo", "info", "nothing to see", "")
	r := find(t, db, Query{Text: "demo#3"})
	if len(r.Hits) != 2 || r.Hits[0].ID != "demo#3" || r.Hits[0].Match != "exact" || r.Hits[1].Kind != "done" {
		t.Fatalf("%+v", r.Hits)
	}
	if r.Status != "complete" {
		t.Fatal(r.Status, r.Reasons)
	}
}

// The FTS5 search stems words, so "stop" finds "STOPPED", and the best match comes first.
func TestFindTextSearchStemsAndRanks(t *testing.T) {
	db := open(t)
	event(t, db, "2026-10-15T10:00:00Z", "falcon", "info", "MAIL STOPPED 09-21: newest stored mail record 09-21", "")
	event(t, db, "2026-10-15T11:00:00Z", "falcon", "info", "meetings fixed, mail later", "")
	event(t, db, "2026-10-15T12:00:00Z", "falcon", "info", "unrelated deploy", "")
	r := find(t, db, Query{Text: "when did mail stop?"})
	if len(r.Hits) != 2 || !strings.Contains(r.Hits[0].Text, "09-21") || r.Hits[0].Match != "text" {
		t.Fatalf("%+v", r.Hits)
	}
}

func TestATrueNoNamesItsScope(t *testing.T) {
	db := open(t)
	event(t, db, "2026-10-15T10:00:00Z", "demo", "info", "mail stopped", "")
	r := find(t, db, Query{Text: "zebra", Project: "demo", Since: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)})
	out := r.Text()
	if r.Status != "complete" || len(r.Hits) != 0 || !strings.Contains(out, "no match in events, decisions, items, artifacts, commits") ||
		!strings.Contains(out, "project demo") || !strings.Contains(out, "since 2026-10-01") {
		t.Fatal(out)
	}
}

func TestStaleFailedOrMissingIngestIsPartial(t *testing.T) {
	db := open(t)
	r := find(t, db, Query{Text: "x", Now: now.Add(2 * time.Hour)})
	if r.Status != "partial" || !strings.Contains(strings.Join(r.Reasons, ";"), "ingest") {
		t.Fatal(r.Status, r.Reasons)
	}
	exec(t, db, "insert into source_run (started, finished, status, stats) values (?,?,?,?)",
		"2026-10-16T11:59:30Z", "2026-10-16T11:59:40Z", "partial", `{"Warnings":["git: repo x missing"]}`)
	if r = find(t, db, Query{Text: "x"}); r.Status != "partial" || !strings.Contains(strings.Join(r.Reasons, ";"), "repo x missing") {
		t.Fatal(r.Status, r.Reasons)
	}
	exec(t, db, "delete from source_run")
	if r = find(t, db, Query{Text: "x"}); r.Status != "partial" {
		t.Fatal(r.Status, r.Reasons)
	}
}

func decision(t *testing.T, db *sql.DB, id, title, supersedes string) {
	t.Helper()
	exec(t, db, "insert into decision (id, project, ts, title, decided_by, supersedes) values (?,?,?,?,?,?)",
		id, "demo", "2026-10-15", title, "owner", supersedes)
}

func TestKindDecisionMarksSupersededAndActiveDropsThem(t *testing.T) {
	db := open(t)
	decision(t, db, "demo-D1", "poll every hour", "")
	decision(t, db, "demo-D2", "poll every 10 min", "demo-D1")
	r := find(t, db, Query{Kinds: []string{"decision"}})
	if len(r.Hits) != 2 {
		t.Fatalf("%+v", r.Hits)
	}
	for _, h := range r.Hits {
		if (h.ID == "demo-D1") != (h.SupersededBy == "demo-D2") {
			t.Fatalf("%+v", h)
		}
	}
	if !strings.Contains(r.Text(), "superseded by demo-D2") {
		t.Fatal(r.Text())
	}
	r = find(t, db, Query{Kinds: []string{"decision"}, Active: true})
	if len(r.Hits) != 1 || r.Hits[0].ID != "demo-D2" {
		t.Fatalf("%+v", r.Hits)
	}
}

// A kind no source has would give a silent "no match"; it is refused and the known kinds are named.
func TestAnUnknownKindIsRefused(t *testing.T) {
	db := open(t)
	if _, err := Find(db, Query{Kinds: []string{"decisoin"}, Now: now}, store.Types); err == nil || !strings.Contains(err.Error(), "decision") {
		t.Fatal(err)
	}
	if _, err := Find(db, Query{Active: true, Now: now}, store.Types); err == nil {
		t.Fatal("--active without --kind decision")
	}
}

// A wait is open until its session makes another API call a minute or more after it.
func TestWaitingListsOnlyOpenWaits(t *testing.T) {
	db := open(t)
	event(t, db, "2026-10-16T11:00:00Z", "waits", "wait", "wait idle_prompt \"a\" sid=aaaa1111 at=15:00:10 — waiting", "")
	event(t, db, "2026-10-16T11:10:00Z", "waits", "wait", "wait idle_prompt \"b\" sid=bbbb2222 at=15:10:10 — waiting", "")
	event(t, db, "2026-10-16T11:20:00Z", "waits", "wait", "wait screen_sweep surface=s1 — proceed?", "")
	exec(t, db, "insert into request (request_id, session, ts_first) values ('r1','aaaa1111-0000-0000-0000-000000000000','2026-10-16T11:05:00Z')")
	exec(t, db, "insert into request (request_id, session, ts_first) values ('r2','bbbb2222-0000-0000-0000-000000000000','2026-10-16T11:10:30Z')")
	r := find(t, db, Query{Waiting: true})
	var got []string
	for _, h := range r.Hits {
		got = append(got, h.WaitState+" "+h.Text[:20])
	}
	if strings.Join(got, "|") != `unknown wait screen_sweep su|open wait idle_prompt "b"` {
		t.Fatal(got)
	}
}

// --since and --project filter; with no query and no --kind, only rows with text are listed (no prompt flood).
func TestFiltersAndTimeline(t *testing.T) {
	db := open(t)
	event(t, db, "2026-10-10T10:00:00Z", "demo", "info", "old line", "")
	event(t, db, "2026-10-15T10:00:00Z", "demo", "done", "new line", "")
	event(t, db, "2026-10-15T11:00:00Z", "other", "done", "other line", "")
	event(t, db, "2026-10-15T12:00:00Z", "demo", "prompt", "", "")
	decision(t, db, "demo-D1", "dated by day only", "")
	r := find(t, db, Query{Project: "demo", Since: time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)})
	var got []string
	for _, h := range r.Hits {
		got = append(got, h.Text)
	}
	if strings.Join(got, "|") != "new line|dated by day only" {
		t.Fatal(got)
	}
	if r = find(t, db, Query{Kinds: []string{"prompt"}}); len(r.Hits) != 1 {
		t.Fatalf("%+v", r.Hits)
	}
}

func TestACommitShaAndASessionPrefixAreExactHits(t *testing.T) {
	db := open(t)
	exec(t, db, "insert into git_commit (sha, repo, project, ts, files) values ('cf62176aaaa','/r/falcon','falcon','2026-10-15T22:40:00Z','[\"a\",\"b\"]')")
	exec(t, db, "insert into session (id, project, title, started) values ('7dce2bdf-7b14-464f-a18a-c263010a0a8c','crius','build','2026-10-16T08:00:00Z')")
	if r := find(t, db, Query{Text: "cf62176"}); len(r.Hits) != 1 || r.Hits[0].Kind != "commit" {
		t.Fatalf("%+v", r.Hits)
	}
	if r := find(t, db, Query{Text: "7dce2bdf"}); len(r.Hits) != 1 || r.Hits[0].Kind != "session" {
		t.Fatalf("%+v", r.Hits)
	}
}

// Review 0178403: a cited id matches whole (demo#1 is not demo#11); a full sha finds events citing its short form;
// an id with no row is a true "no", not a word search; sessions are a kind.
func TestFindMatchesIDsWhole(t *testing.T) {
	db := open(t)
	event(t, db, "2026-10-16T10:00:00Z", "demo", "info", "item=demo#11 shipped", `{"item":["demo#11"]}`)
	event(t, db, "2026-10-16T10:01:00Z", "demo", "info", "per demo-D12 we poll", `{"decision":["demo-D12"]}`)
	event(t, db, "2026-10-16T10:02:00Z", "demo", "commit_made", "committed", `{"sha":["8e66e952"]}`)
	for _, id := range []string{"demo#1", "demo-D1", "demo#99"} {
		if r := find(t, db, Query{Text: id}); len(r.Hits) != 0 || r.Status != "complete" {
			t.Fatalf("%s: %+v", id, r.Hits)
		}
	}
	if r := find(t, db, Query{Text: "8e66e952" + strings.Repeat("0", 32)}); len(r.Hits) != 1 || r.Hits[0].Text != "committed" {
		t.Fatalf("%+v", r.Hits)
	}
	if r := find(t, db, Query{Text: "8e66e9"}); len(r.Hits) != 0 {
		t.Fatalf("a 6-char prefix is not a sha: %+v", r.Hits)
	}
	// Sessions are listed, or found by id prefix; words do not search them, so a word query on them is refused.
	exec(t, db, "insert into session (id, project, started) values ('7dce2bdf-0000', 'demo', '2026-10-16T11:00:00.000Z')")
	if r := find(t, db, Query{Kinds: []string{"session"}, Since: now.Add(-2 * time.Hour)}); len(r.Hits) != 1 ||
		r.Hits[0].Match != "list" {
		t.Fatalf("%+v", r.Hits)
	}
	// Review 4836329: an id lookup is no word search, alone or among other kinds.
	for _, kinds := range [][]string{{"session"}, {"session", "commit"}} {
		if r := find(t, db, Query{Text: "(7dce2bdf)", Kinds: kinds}); len(r.Hits) != 1 || r.Status != "complete" {
			t.Fatalf("%v: %s %v %+v", kinds, r.Status, r.Reasons, r.Hits)
		}
	}
	if _, err := Find(db, Query{Text: "mail", Kinds: []string{"session"}, Now: now}, store.Types); !errors.Is(err, ErrQuery) {
		t.Fatal(err)
	}
}

// Review 5d5fee4: the session list says when it is cut; words with sessions among other kinds are not refused.
func TestFindSessionListSaysMore(t *testing.T) {
	db := open(t)
	for i := 0; i <= maxHits; i++ {
		exec(t, db, "insert into session (id, project, started) values (?, 'demo', '2026-10-16T11:00:00.000Z')", fmt.Sprint("s", i))
	}
	if r := find(t, db, Query{Kinds: []string{"session"}}); len(r.Hits) != maxHits || !r.More {
		t.Fatal(len(r.Hits), r.More)
	}
	// Review 831be40: sessions asked for next to another kind are not searched by words, and the answer says so.
	r, err := Find(db, Query{Text: "deploy", Kinds: []string{"session", "decision"}, Now: now}, store.Types)
	if err != nil || r.Status != "partial" || !strings.Contains(strings.Join(r.Reasons, "; "), "sessions are not searched by words") {
		t.Fatal(r, err)
	}
}

// Crius 01:02: commit subjects are searched, but shown as their own block of at most 3 after the other hits, so
// commits never push an answer down; asked for by --kind commit, they are the hits.
func TestFindShowsCommitsAsTheirOwnBlock(t *testing.T) {
	db := open(t)
	event(t, db, "2026-10-16T10:00:00Z", "trading", "info", "broker clock moved to New York time", "")
	for i := 0; i < 6; i++ {
		exec(t, db, "insert into git_commit (sha, repo, project, ts, files, subject) values (?, '/r/synapse', 'trading', ?,"+
			" '[]', 'fix(clock): broker clock follows DST')", fmt.Sprintf("%07dabc", i), fmt.Sprintf("2026-10-16T1%d:00:00Z", i))
	}
	r := find(t, db, Query{Text: "synapse broker clock"})
	if len(r.Hits) != 1 || r.Hits[0].Kind == "commit" || len(r.Commits) != 3 ||
		!strings.Contains(r.Commits[0].Text, "synapse · fix(clock): broker clock follows DST") {
		t.Fatalf("hits %+v commits %+v", r.Hits, r.Commits)
	}
	if txt := r.Text(); !strings.Contains(txt, "1 hits") || !strings.Contains(txt, "commits:\n") {
		t.Fatal(txt)
	}
	if r = find(t, db, Query{Text: "broker clock", Kinds: []string{"commit"}}); len(r.Hits) != 6 || len(r.Commits) != 0 {
		t.Fatalf("hits %d commits %d", len(r.Hits), len(r.Commits))
	}
	if r = find(t, db, Query{Text: "dst"}); len(r.Hits) != 0 || len(r.Commits) != 3 || strings.Contains(r.Text(), "no match") {
		t.Fatalf("%+v %q", r, r.Text())
	}
}

// A full list of other hits does not leave the commit block empty, and commits do not mark the answer as cut.
func TestFindCommitBlockSurvivesAFullList(t *testing.T) {
	db := open(t)
	for i := 0; i < maxHits+1; i++ {
		event(t, db, "2026-10-16T10:00:00Z", "trading", "info", "broker note", "")
	}
	exec(t, db, "insert into git_commit (sha, repo, project, ts, files, subject) values ('abcdef1', '/r/synapse', 'trading',"+
		" '2026-10-16T10:00:00Z', '[]', 'broker fix')")
	if r := find(t, db, Query{Text: "broker"}); len(r.Hits) != maxHits || !r.More || len(r.Commits) != 1 {
		t.Fatalf("hits %d more %v commits %d", len(r.Hits), r.More, len(r.Commits))
	}
	if r := find(t, db, Query{Text: "fix"}); r.More || len(r.Commits) != 1 {
		t.Fatalf("more %v commits %d", r.More, len(r.Commits))
	}
}

// More matching commits than one page: none of them reaches the hits, and they do not mark the hits as cut.
func TestFindManyCommitsStayInTheirBlock(t *testing.T) {
	db := open(t)
	event(t, db, "2026-10-16T10:00:00Z", "trading", "info", "deploy went out", "")
	for i := 0; i < maxHits+3; i++ {
		exec(t, db, "insert into git_commit (sha, repo, project, ts, files, subject) values (?, '/r/synapse', 'trading',"+
			" '2026-10-16T10:00:00Z', '[]', 'deploy script')", fmt.Sprintf("%07dabc", i))
	}
	r := find(t, db, Query{Text: "deploy"})
	if len(r.Hits) != 1 || r.Hits[0].Kind == "commit" || r.More || len(r.Commits) != maxCommits {
		t.Fatalf("hits %+v more %v commits %d", r.Hits, r.More, len(r.Commits))
	}
}
