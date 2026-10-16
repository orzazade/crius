package recall

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/redact"
	"github.com/orzazade/crius/internal/store"
	"github.com/orzazade/crius/internal/testutil"
)

// transcript writes one session's transcript for a project and registers it as the ingest would.
func transcript(t *testing.T, db *sql.DB, project, sid string, lines ...string) string {
	t.Helper()
	path := filepath.Join(os.Getenv("CRIUS_HOME"), "tx", "-"+project, sid+".jsonl")
	testutil.Write(t, path, strings.Join(lines, "\n")+"\n")
	exec(t, db, "insert into session (id, project, transcript_path) values (?,?,?)", sid, project, path)
	return path
}

func msg(typ, uuid, ts, content string) string {
	return fmt.Sprintf(`{"type":%q,"uuid":%q,"timestamp":%q,"cwd":"/x/mail-stopped","message":{"role":%q,"content":%s}}`,
		typ, uuid, ts, typ, content)
}

func deep(t *testing.T, db *sql.DB, q Query) *Result {
	t.Helper()
	q.Now = now
	r, err := Deep(db, q, store.Types)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// Crius 20:57: every project is searched (filing errors must not hide answers); between equally tight matches the
// asked project comes first.
func TestDeepCitesAllProjectsTheAskedOneFirst(t *testing.T) {
	db := open(t)
	key := "ghp_" + strings.Repeat("F", 36)
	path := transcript(t, db, "falcon", "s1",
		msg("user", "u1", "2026-10-15T10:00:00.120Z", `"G says MAIL STOPPED on 09-21; token `+key+`"`),
		msg("assistant", "a1", "2026-10-15T10:01:00Z", `[{"type":"text","text":"mail stopped: the poll returns 201 but stores nothing"}]`),
		msg("user", "u2", "2026-10-15T10:02:00Z", `"only the cwd says it"`),
		msg("system", "x9", "2026-10-15T10:03:00Z", `"mail stopped (a hook notice, not a message)"`),
		`{"type":"summary","summary":"mail stopped"}`)
	transcript(t, db, "trading", "s2", msg("user", "x1", "2026-10-15T11:00:00Z", `"mail stopped too"`))
	transcript(t, db, "", "s3", msg("user", "n1", "2026-10-15T12:00:00Z", `"unfiled: mail stopped"`))
	exec(t, db, "update session set project = null where id = 's3'")
	r := deep(t, db, Query{Text: "mail stopped", Project: "falcon"})
	if r.Status != "complete" || len(r.Hits) != 4 || r.Hits[2].ID != "x1" && r.Hits[2].ID != "n1" {
		t.Fatalf("%s %+v", r.Status, r.Hits)
	}
	h := r.Hits[1]
	if h.ID != "u1" || h.Path != path || h.TS != "2026-10-15T10:00:00.120Z" || !strings.Contains(h.Text, "09-21") ||
		strings.Contains(h.Text, "FFFF") || !strings.Contains(h.Text, "[redacted]") {
		t.Fatalf("%+v", h)
	}
	if r.Hits[0].ID != "a1" || !strings.Contains(r.Text(), "transcripts of all projects, falcon first") {
		t.Fatal(r.Text())
	}
	if r = deep(t, db, Query{Text: "mail stopped", Project: "trading"}); r.Hits[0].ID != "x1" || len(r.Hits) != 4 {
		t.Fatalf("%+v", r.Hits)
	}
}

// Crius 02:11 (10-17): closeness ranks first, the asked project only breaks ties. A project-first rank let every loose
// match in a project that talks a lot push a tight match elsewhere out of the top 5 as the store grew.
func TestDeepRanksATightMatchElsewhereAboveALooseOneInTheAskedProject(t *testing.T) {
	db := open(t)
	transcript(t, db, "falcon", "s1", msg("user", "loose", "2026-10-15T12:00:00Z", `"33 open PRs, and the mail queue was lost"`))
	transcript(t, db, "crius", "s2", msg("user", "tight", "2026-10-15T10:00:00Z", `"was 422 on poll-pr, 33 PRs lost"`))
	r := deep(t, db, Query{Text: "33 PRs lost", Project: "falcon"})
	if len(r.Hits) != 2 || r.Hits[0].ID != "tight" {
		t.Fatalf("%+v", r.Hits)
	}
}

// Within a project, the message holding the words closest together ranks first, then the newest.
func TestDeepRanksTheTightestMatchFirst(t *testing.T) {
	db := open(t)
	transcript(t, db, "falcon", "s1",
		msg("user", "far", "2026-10-15T12:00:00Z", `"mail went out, and much later the poll stopped"`),
		msg("user", "near", "2026-10-15T10:00:00Z", `"so mail stopped on 09-21"`))
	if r := deep(t, db, Query{Text: "mail stopped"}); len(r.Hits) != 2 || r.Hits[0].ID != "near" {
		t.Fatalf("%+v", r.Hits)
	}
	// The snippet shows the tightest stretch, wherever it is, with a rune index (Ж is 2 bytes, 1 rune).
	long := "mail " + strings.Repeat("Ж", 300) + " stopped " + strings.Repeat("x", 300) + " mail stopped on 09-21"
	transcript(t, db, "falcon", "s2", msg("user", "long", "2026-10-15T09:00:00Z", fmt.Sprintf("%q", long)))
	r := deep(t, db, Query{Text: "mail stopped"})
	if h := r.Hits[1]; h.ID != "long" || !strings.Contains(h.Text, "mail stopped on 09-21") {
		t.Fatalf("%+v", h)
	}
}

// Bounded: at most 10 citations (more is said), and a time limit that makes the answer partial.
func TestDeepIsBounded(t *testing.T) {
	db := open(t)
	for i := 0; i < 55; i++ {
		p := transcript(t, db, "falcon", fmt.Sprint("s", i), msg("user", fmt.Sprint("u", i), "2026-10-15T10:00:00Z", `"mail stopped"`))
		mt := now.Add(-time.Duration(i) * time.Minute)
		os.Chtimes(p, mt, mt)
	}
	r := deep(t, db, Query{Text: "mail stopped", Project: "falcon"})
	reasons := strings.Join(r.Reasons, "; ")
	if r.Status != "complete" || len(r.Hits) != 10 || !r.More || r.Hits[0].ID != "u0" {
		t.Fatal(r.Status, len(r.Hits), r.More, reasons)
	}
	defer func(d time.Duration) { deepTimeout = d }(deepTimeout)
	deepTimeout = 0
	if r = deep(t, db, Query{Text: "mail stopped", Project: "falcon"}); r.Status != "partial" ||
		!strings.Contains(strings.Join(r.Reasons, "; "), "time limit, 55 of 55 files not read") {
		t.Fatal(r.Reasons)
	}
}

// A question about more than 30 days ago is expired, not "no match", and shows the durable rows left.
func TestDeepPastRetentionIsExpiredWithTheDurableRows(t *testing.T) {
	db := open(t)
	event(t, db, "2026-09-05T10:00:00Z", "falcon", "info", "mail stopped 09-21", "")
	// Review of 376393d (P3): the durable rows include the commit block.
	exec(t, db, "insert into git_commit (sha, repo, project, ts, files, subject) values ('abcdef1', '/r/falcon', 'falcon',"+
		" '2026-09-05T11:00:00Z', '[]', 'mail stopped: poll fix')")
	r := deep(t, db, Query{Text: "mail stopped", Project: "falcon", Since: now.AddDate(0, 0, -45)})
	if r.Status != "expired" || len(r.Hits) != 1 || r.Hits[0].Match != "durable" || !strings.Contains(r.Text(), "older than 30 days") ||
		len(r.Commits) != 1 || r.Commits[0].Match != "durable" {
		t.Fatal(r.Text())
	}
}

func TestDeepNeedsWords(t *testing.T) {
	db := open(t)
	for _, q := range []Query{{Project: "falcon"}, {Text: "mail", Project: "falcon", Waiting: true}} {
		q.Now = now
		if _, err := Deep(db, q, store.Types); !errors.Is(err, ErrQuery) {
			t.Fatal(q, err)
		}
	}
}

// The asking session holds the question itself: it ranks after every other session, but is still searched.
func TestDeepRanksTheAskingSessionLast(t *testing.T) {
	db := open(t)
	transcript(t, db, "crius", "self", msg("user", "q", "2026-10-15T12:00:00Z", `"which commit armed the k3s hooks?"`))
	transcript(t, db, "staffy", "old", msg("assistant", "a", "2026-10-14T10:00:00Z", `"k3s now runs the gating hooks (cf62176)"`))
	r := deep(t, db, Query{Text: "k3s hooks", Project: "crius", Self: "self"})
	if len(r.Hits) != 2 || r.Hits[0].ID != "a" {
		t.Fatalf("%+v", r.Hits)
	}
}

// Review 5d5fee4: the message is redacted before the snippet is cut, so a secret cut in half cannot leak its tail.
func TestDeepRedactsBeforeCutting(t *testing.T) {
	db := open(t)
	key := "sk-ant-api03-" + strings.Repeat("Q", 100)
	transcript(t, db, "falcon", "s1", msg("user", "u1", "2026-10-15T10:00:00Z", `"`+key+` mail stopped"`))
	if r := deep(t, db, Query{Text: "mail stopped"}); len(r.Hits) != 1 || strings.Contains(r.Hits[0].Text, "QQQQ") {
		t.Fatalf("%+v", r.Hits)
	}
}

// Review 5d5fee4: workflow agents write deeper (subagents/workflows/wf_*/agent-*.jsonl); they are searched too.
func TestDeepReadsNestedSubagentFiles(t *testing.T) {
	db := open(t)
	p := transcript(t, db, "falcon", "s1", msg("user", "u1", "2026-10-15T10:00:00Z", `"hello"`))
	testutil.Write(t, filepath.Join(strings.TrimSuffix(p, ".jsonl"), "subagents", "workflows", "wf_1", "agent-1.jsonl"),
		msg("assistant", "a1", "2026-10-15T10:01:00Z", `"mail stopped"`)+"\n")
	if r := deep(t, db, Query{Text: "mail stopped"}); len(r.Hits) != 1 || r.Hits[0].ID != "a1" {
		t.Fatalf("%+v", r.Hits)
	}
}

// Review 5d5fee4: failing to look is not "no": a transcript of a session active in the window that is gone makes the
// answer partial; a project the store does not know is refused.
func TestDeepSaysWhatItCouldNotRead(t *testing.T) {
	db := open(t)
	p := transcript(t, db, "falcon", "s1", msg("user", "u1", "2026-10-15T10:00:00Z", `"hello"`))
	exec(t, db, "update session set ended = '2026-10-15T10:00:00Z'")
	os.Remove(p)
	r := deep(t, db, Query{Text: "mail stopped"})
	if r.Status != "partial" || !strings.Contains(strings.Join(r.Reasons, "; "), "1 transcript of a session active in the window is missing") {
		t.Fatal(r.Status, r.Reasons)
	}
	if _, err := Deep(db, Query{Text: "mail", Project: "falcn", Now: now}, store.Types); !errors.Is(err, ErrQuery) {
		t.Fatal(err)
	}
}

// Review 5d5fee4: the durable rows' "more" survives an expired answer.
func TestDeepExpiredKeepsDurableMore(t *testing.T) {
	db := open(t)
	for i := 0; i <= maxHits; i++ {
		event(t, db, "2026-09-05T10:00:00Z", "falcon", "info", "mail stopped", "")
	}
	if r := deep(t, db, Query{Text: "mail stopped", Since: now.AddDate(0, 0, -45)}); r.Status != "expired" || !r.More {
		t.Fatal(r.Status, r.More)
	}
}

// Review 831be40: tool input is decoded before redaction; raw JSON keeps \" and the key=value pattern missed it.
func TestDeepRedactsToolInput(t *testing.T) {
	db := open(t)
	transcript(t, db, "falcon", "s1", msg("assistant", "a1", "2026-10-15T10:00:00Z",
		`[{"type":"tool_use","input":{"command":"export PASSWORD=\"hunter2xyz\" && echo mail stopped"}}]`))
	if r := deep(t, db, Query{Text: "mail stopped"}); len(r.Hits) != 1 || strings.Contains(r.Hits[0].Text, "hunter2") {
		t.Fatalf("%+v", r.Hits)
	}
}

// Review 831be40: only the best citations are kept while reading; more is still said.
func TestDeepKeepsOnlyTheBest(t *testing.T) {
	db := open(t)
	var lines []string
	for i := 0; i < 200; i++ {
		lines = append(lines, msg("user", fmt.Sprint("u", i), fmt.Sprintf("2026-10-15T10:%02d:%02dZ", i/60, i%60),
			fmt.Sprintf("%q", "mail"+strings.Repeat(" x", i)+" stopped")))
	}
	transcript(t, db, "falcon", "s1", lines...)
	var c []cand
	keep(&c, cand{span: 5}, 3)
	keep(&c, cand{span: 9}, 3)
	keep(&c, cand{span: 1}, 3)
	keep(&c, cand{span: 7}, 3)
	keep(&c, cand{span: 3}, 3)
	if len(c) != 3 || c[0].span != 1 || c[2].span != 5 {
		t.Fatalf("%+v", c)
	}
	r := deep(t, db, Query{Text: "mail stopped"})
	if len(r.Hits) != 10 || !r.More || r.Hits[0].ID != "u0" || r.Hits[9].ID != "u9" {
		t.Fatalf("%d %v %+v", len(r.Hits), r.More, r.Hits[0])
	}
}

// Review 831be40: the span ends at the furthest word end in the window, counted in letters (runes).
func TestTightestOverlapAndRunes(t *testing.T) {
	terms := func(ws ...string) (b [][]byte) {
		for _, w := range ws {
			b = append(b, []byte(w))
		}
		return b
	}
	for _, ws := range [][]string{{"mailbox", "mail"}, {"mail", "mailbox"}} {
		if at, span := tightest([]rune("a mailbox"), terms(ws...)); at != 2 || span != 7 {
			t.Fatal(ws, at, span)
		}
	}
	if _, span := tightest([]rune("почта остановилась"), terms("почта", "остановилась")); span != 18 {
		t.Fatal(span)
	}
}

// Review 831be40: an unreadable folder or a file gone before it is read makes the answer partial, not an error or
// a silent skip. An expired window stays expired (exit 5) and keeps the cut's reason.
func TestDeepSaysWhatItSkipped(t *testing.T) {
	db := open(t)
	p := transcript(t, db, "falcon", "s1", msg("user", "u1", "2026-10-15T10:00:00Z", `"mail stopped"`))
	sub := filepath.Join(strings.TrimSuffix(p, ".jsonl"), "subagents")
	testutil.Write(t, filepath.Join(sub, "a.jsonl"), "{}\n")
	os.Chmod(sub, 0)
	defer os.Chmod(sub, 0o755)
	r := deep(t, db, Query{Text: "mail stopped"})
	if r.Status != "partial" || len(r.Hits) != 1 || !strings.Contains(strings.Join(r.Reasons, "; "), "cannot read") {
		t.Fatal(r.Status, r.Reasons)
	}
	var c []cand
	_, gone, err := deepFile(deepSrc{path: p + ".gone"}, [][]byte{[]byte("x")}, now, time.Time{}, time.Now().Add(time.Minute), &c, new(int))
	if err != nil || !gone {
		t.Fatal(gone, err)
	}
	defer func(d time.Duration) { deepTimeout = d }(deepTimeout)
	deepTimeout = 0
	r = deep(t, db, Query{Text: "mail stopped", Since: now.AddDate(0, 0, -45)})
	if r.Status != "expired" || !strings.Contains(strings.Join(r.Reasons, "; "), "time limit") {
		t.Fatal(r.Status, r.Reasons)
	}
}

// Review 4836329: decoded tool input keeps its keys and its numbers: the value of a secret-named key is redacted
// whatever its shape, and keys and numbers can still be searched.
func TestDeepToolInputKeepsKeysAndNumbers(t *testing.T) {
	db := open(t)
	transcript(t, db, "falcon", "s1",
		msg("assistant", "a1", "2026-10-15T10:00:00Z", `[{"type":"tool_use","input":{"user":"bob","password":"two words hunter2xyz",`+
			`"env":{"DB_PASSWORD":"pgsecret9"},"note":"mail stopped"}}]`),
		msg("assistant", "a2", "2026-10-15T10:01:00Z", `[{"type":"tool_use","input":{"issue_number":7479,"draft":false,"action":"merge"}}]`))
	r := deep(t, db, Query{Text: "mail stopped"})
	if len(r.Hits) != 1 || strings.Contains(r.Hits[0].Text, "hunter2") || strings.Contains(r.Hits[0].Text, "words") ||
		strings.Contains(r.Hits[0].Text, "pgsecret9") || !strings.Contains(r.Hits[0].Text, "bob") {
		t.Fatalf("%+v", r.Hits)
	}
	for _, q := range []string{"7479 merge", "issue_number merge", "draft false"} {
		if r := deep(t, db, Query{Text: q}); len(r.Hits) != 1 || r.Hits[0].ID != "a2" {
			t.Fatalf("%s: %+v", q, r.Hits)
		}
	}
}

// Review 9b807e4: a JSON body inside a command keeps its escaped quotes after one decode; a secret held as a
// name/value pair (env lists) has its key in another field.
func TestDeepToolInputNestedSecrets(t *testing.T) {
	db := open(t)
	transcript(t, db, "falcon", "s1",
		msg("assistant", "a1", "2026-10-15T10:00:00Z", `[{"type":"tool_use","input":{"command":"curl -d \"{\\\"password\\\":\\\"hunter2\\\"}\" mail stopped"}}]`),
		msg("assistant", "a2", "2026-10-15T10:01:00Z", `[{"type":"tool_use","input":{"env":[{"name":"DB_PASSWORD","value":"hunter3"}],"note":"mail stopped"}}]`))
	r := deep(t, db, Query{Text: "mail stopped"})
	if len(r.Hits) != 2 {
		t.Fatalf("%+v", r.Hits)
	}
	for _, h := range r.Hits {
		if strings.Contains(h.Text, "hunter") {
			t.Error(h.Text)
		}
	}
}

// Review 33c07fd: name/value pairs in any case or naming (AWS, CloudFormation), and a flag with its value in an
// argument list.
func TestDeepToolInputPairsAndArgs(t *testing.T) {
	for _, in := range []string{`{"Name":"DB_PASSWORD","Value":"hunter2"}`, `{"ParameterKey":"DbPassword","ParameterValue":"hunter2"}`,
		`{"key":"API_KEY","val":"hunter2"}`, `{"args":["--password","hunter2"]}`} {
		if out, _ := redact.Redact(strings.Join(toolText(json.RawMessage(in)), " ")); strings.Contains(out, "hunter2") {
			t.Errorf("%s -> %s", in, out)
		}
	}
}
