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
	// GOAL rows their lead stopped asking are no owner decision: not asked, not answered, no repeat (review of the queue gap).
	for _, id := range []string{"q7", "q8", "q9"} {
		exec(t, db, "insert into owner_decision (id, asked, asked_by, title, state, answer, answered_at, answered_by) values"+
			" (?,?,?,?,?,?,?,?)", id, utc("2026-10-30T10:00"), "falcon", "Q1 CI as a source", "withdrawn",
			"the GOAL block no longer asks this", utc("2026-10-30T11:00"), "falcon:lead")
	}
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

// Review of ec1ff36: a row with no date (an undated bus line) fell out of every review, a row answered with no
// answer stamp was shown as still open, and an OWNER line that relays a queue answer was counted as decided outside it.
func TestReviewShowsUndatedRowsAndTrustsTheState(t *testing.T) {
	db := open(t)
	exec(t, db, "insert into coverage (source, resource, offset, last_ok) values ('owner-queue', '/q', 1, '2026-10-30T17:00:00Z')")
	exec(t, db, "insert into owner_decision (id, asked, asked_by, title, state, answer, answered_at) values"+
		" ('q1', ?, 'cortex', 'Approve F-12', 'answered', 'yes', null),"+
		" ('q2', null, 'falcon', 'Restart Chrome', 'open', null, null),"+
		" ('q3', null, 'axi', 'Pick a seller', 'answered', 'b', null)", utc("2026-10-29T10:00"))
	exec(t, db, "insert into event (ts, project, kind, actor, text, source_key) values (?, 'crius', 'owner', 'lead', '[queue q1] yes', 'k1')",
		utc("2026-10-29T10:05"))
	got, err := Review(db, now.AddDate(0, 0, -7), now)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Asked 1 · answered 1 · still open 0\n",
		"Decided outside the queue (OWNER lines on the bus): 0\n",
		"  10-29 10:00 cortex · Approve F-12 → yes (no answer time)\n",
		"Undated (the bus line had no date; not in the counts above): 2\n  falcon · Restart Chrome (open)\n  axi · Pick a seller → b\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "Still open") {
		t.Errorf("an answered row is shown as open:\n%s", got)
	}
}

// Review of ec1ff36: a row asked before the week and answered in it was in no review, so the long waits fell out.
func TestReviewShowsAWaitThatCrossedTheWeeklyRun(t *testing.T) {
	db := open(t)
	exec(t, db, "insert into coverage (source, resource, offset, last_ok) values ('owner-queue', '/q', 1, '2026-10-30T17:00:00Z')")
	exec(t, db, "insert into owner_decision (id, asked, asked_by, title, state, answer, answered_at) values"+
		" ('q1', ?, 'cortex', 'Approve F-12', 'answered', 'yes', ?)", utc("2026-10-23T12:00"), utc("2026-10-24T10:00"))
	got, err := Review(db, now.AddDate(0, 0, -7), now)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Asked 0 · answered 1 (1 asked before the week) · still open 0 · median wait 22h00m · longest 22h00m: Approve F-12 (cortex)\n",
		"  10-23 12:00 cortex · Approve F-12 → yes (22h00m)\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
}
