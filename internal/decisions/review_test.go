package decisions

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/store"
	"github.com/orzazade/crius/internal/testutil"
)

var now = time.Date(2026, 10, 30, 18, 0, 0, 0, time.Local)

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
	return db
}

func utc(local string) string {
	t, err := time.ParseInLocation("2006-01-02T15:04", local, time.Local)
	if err != nil {
		panic(err)
	}
	return t.UTC().Format(time.RFC3339)
}

func exec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

// Owner 10-30: each decision is logged "for later how I decide and how to reduce my involvement". The review shows
// how many questions came, how long they waited, who asks most, and the questions answered the same way again and
// again: those could be a standing rule Crius asks the owner for once.
func TestReviewShowsWaitsAskersAndRepeats(t *testing.T) {
	db := open(t)
	exec(t, db, "insert into coverage (source, resource, offset, last_ok) values ('owner-queue', '/q', 1, '2026-10-30T17:00:00Z')")
	add := func(id, asked, by, title, answer, at string) {
		state := "open"
		if answer != "" {
			state = "answered"
		}
		exec(t, db, "insert into owner_decision (id, asked, asked_by, title, state, answer, answered_at) values (?,?,?,?,?,?,?)",
			id, utc(asked), by, title, state, nullable(answer), nullable(at))
	}
	add("q1", "2026-10-20T10:00", "cortex", "Approve the merge of F-1", "yes", utc("2026-10-20T10:10")) // before the week
	add("q2", "2026-10-27T10:00", "cortex", "Approve the merge of F-12", "Yes", utc("2026-10-27T10:10"))
	add("q3", "2026-10-28T10:00", "cortex", "Approve the merge of F-13?", "ok, go", utc("2026-10-28T10:30"))
	add("q4", "2026-10-29T10:00", "cortex", "approve merge F-14", "yes", utc("2026-10-29T12:00"))
	add("q5", "2026-10-29T11:00", "falcon", "Restart Chrome", "done", utc("2026-10-29T17:00"))
	add("q6", "2026-10-30T09:00", "Crius", "Pick a name for the queue", "", "")
	exec(t, db, "insert into event (ts, project, kind, actor, text, source_key) values (?, 'crius', 'owner', 'lead', 'owner: no Telegram', 'k1'),"+
		" (?, 'crius', 'owner', 'lead', 'old', 'k2')", utc("2026-10-29T15:00"), utc("2026-10-01T15:00"))

	got, err := Review(db, now.AddDate(0, 0, -7), now)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# How the owner decided, 2026-10-23 18:00 to 2026-10-30 18:00\n",
		"Asked 5 · answered 4 · still open 1 · median wait 1h15m · longest 6h00m: Restart Chrome (falcon)\n",
		"Asked by: cortex 3, Crius 1, falcon 1\n",
		"Decided outside the queue (OWNER lines on the bus): 1\n",
		"  cortex · approve merge: yes 3 of 3 (Approve the merge of F-12; Approve the merge of F-13?; approve merge F-14)\n",
		"  10-29 11:00 falcon · Restart Chrome → done (6h00m)\n",
		"Still open\n  10-30 09:00 Crius · Pick a name for the queue (9h00m so far)\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "F-1)") || strings.Contains(got, "falcon · restart") {
		t.Errorf("a row before the week, or a one-off, is listed as a repeat:\n%s", got)
	}
}

// A store that never read the owner queue says so: no rows there is a failed look, not "no questions".
func TestReviewOfAQueueNeverReadSaysSo(t *testing.T) {
	got, err := Review(open(t), now.AddDate(0, 0, -7), now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "NOT READ: the store never read the owner queue") {
		t.Fatal(got)
	}
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
