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

func TestDeepCitesMessagesOfOneProjectOnly(t *testing.T) {
	db := open(t)
	key := "ghp_" + strings.Repeat("F", 36)
	path := transcript(t, db, "falcon", "s1",
		msg("user", "u1", "2026-10-15T10:00:00.120Z", `"G says MAIL STOPPED on 09-21; token `+key+`"`),
		msg("assistant", "a1", "2026-10-15T10:01:00Z", `[{"type":"text","text":"mail stopped: the poll returns 201 but stores nothing"}]`),
		msg("user", "u2", "2026-10-15T10:02:00Z", `"only the cwd says it"`),
		msg("system", "x9", "2026-10-15T10:03:00Z", `"mail stopped (a hook notice, not a message)"`),
		`{"type":"summary","summary":"mail stopped"}`)
	transcript(t, db, "trading", "s2", msg("user", "x1", "2026-10-15T10:00:00Z", `"mail stopped too"`))
	r := deep(t, db, Query{Text: "mail stopped", Project: "falcon"})
	if r.Status != "complete" || len(r.Hits) != 2 {
		t.Fatalf("%s %+v", r.Status, r.Hits)
	}
	h := r.Hits[1]
	if h.ID != "u1" || h.Path != path || h.TS != "2026-10-15T10:00:00.120Z" || !strings.Contains(h.Text, "09-21") ||
		strings.Contains(h.Text, "FFFF") || !strings.Contains(h.Text, "[redacted]") {
		t.Fatalf("%+v", h)
	}
	if r.Hits[0].ID != "a1" || !strings.Contains(r.Text(), "transcripts of project falcon") {
		t.Fatal(r.Text())
	}
}

// Bounded: the newest 50 files, at most 10 citations, and a time limit; each cut makes the answer partial.
func TestDeepIsBounded(t *testing.T) {
	db := open(t)
	for i := 0; i < 55; i++ {
		p := transcript(t, db, "falcon", fmt.Sprint("s", i), msg("user", fmt.Sprint("u", i), "2026-10-15T10:00:00Z", `"mail stopped"`))
		mt := now.Add(-time.Duration(i) * time.Minute)
		os.Chtimes(p, mt, mt)
	}
	r := deep(t, db, Query{Text: "mail stopped", Project: "falcon"})
	reasons := strings.Join(r.Reasons, "; ")
	if r.Status != "partial" || len(r.Hits) != 10 || !r.More || !strings.Contains(reasons, "newest 50 of 55 files") ||
		r.Hits[0].ID != "u0" {
		t.Fatal(r.Status, len(r.Hits), r.More, reasons)
	}
	defer func(d time.Duration) { deepTimeout = d }(deepTimeout)
	deepTimeout = 0
	if r = deep(t, db, Query{Text: "mail stopped", Project: "falcon"}); !strings.Contains(strings.Join(r.Reasons, "; "), "time limit") {
		t.Fatal(r.Reasons)
	}
}

// A question about more than 30 days ago is expired, not "no match", and shows the durable rows left.
func TestDeepPastRetentionIsExpiredWithTheDurableRows(t *testing.T) {
	db := open(t)
	event(t, db, "2026-09-05T10:00:00Z", "falcon", "info", "mail stopped 09-21", "")
	r := deep(t, db, Query{Text: "mail stopped", Project: "falcon", Since: now.AddDate(0, 0, -45)})
	if r.Status != "expired" || len(r.Hits) != 1 || r.Hits[0].Match != "durable" || !strings.Contains(r.Text(), "older than 30 days") {
		t.Fatal(r.Text())
	}
}

func TestDeepNeedsAProjectAndWords(t *testing.T) {
	db := open(t)
	for _, q := range []Query{{Text: "mail"}, {Project: "falcon"}, {Text: "mail", Project: "falcon", Waiting: true}} {
		q.Now = now
		if _, err := Deep(db, q, store.Types); !errors.Is(err, ErrQuery) {
			t.Fatal(q, err)
		}
	}
}
