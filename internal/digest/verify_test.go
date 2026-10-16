package digest

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/store"
	"github.com/orzazade/crius/internal/testutil"
)

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

var seq int

// line stores a bus line as the ingest does: the sender's project and role, the type in lower case.
func line(t *testing.T, db *sql.DB, ts time.Time, sender, kind, text string) int64 {
	t.Helper()
	seq++
	p, role, _ := strings.Cut(sender, ":")
	r, err := db.Exec("insert into event (ts, project, kind, actor, text, source_key) values (?,?,?,?,?,?)",
		ts.UTC().Format("2006-01-02T15:04:05Z"), p, kind, role, text, fmt.Sprint("k", seq))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := r.LastInsertId()
	return id
}

// Step 5 P3 (Crius 02:29): a done milestone counts only when a check on the bus names one of its refs. A check is an
// info, done or decision line from Crius (crius:lead) or from the lead's own project's QA (qa, qa-*) that says
// "checked PASS" (no "?" after it) and has no FAIL. A sha ref (7+ hex with a letter, sha= allowed, any case) matches a
// whole word of the check as a prefix either way; "bus <local YYYY-MM-DDTHH:MM>" matches the check at that minute
// when it names the lead or is the lead's own QA (review of 909b68e).
func TestVerifiedNeedsACheckerPassNamingARef(t *testing.T) {
	db := open(t)
	at := time.Date(2026, 10, 17, 2, 27, 0, 0, time.Local)
	crius := line(t, db, at, "crius:lead", "info", "b3a87cb checked PASS (31/34). Merge + push.")
	qa := line(t, db, at.Add(time.Hour), "axi:qa-api", "done", "item=axi#7 sha=9c644de0 checked PASS on env004")
	named := line(t, db, at.Add(2*time.Hour), "crius:lead", "info", "crius:build step 5 P4 checked PASS on the live digest")
	line(t, db, at, "crius:build", "done", "5f74e59 checked PASS, mutants red") // the builder's own claim
	line(t, db, at, "crius:lead", "info", "1eb7a34 not PASS yet")               // no "checked PASS"
	line(t, db, at, "crius:lead", "info", "FAIL 2a22222; checked PASS 3b33333") // a FAIL on the line
	line(t, db, at, "crius:lead", "blocked", "4d44444 checked PASS")            // a blocked line is no check
	line(t, db, at, "crius:lead", "info", "5e55555 checked PASS?")              // a question
	line(t, db, at, "crius:lead", "info", "checked PASS run 1823456 sha=fab5555555c")
	line(t, db, at.Add(3*time.Hour), "zzz:qanda", "info", "checked PASS 6f66666") // not a QA role
	line(t, db, at.Add(4*time.Hour), "axi:qa", "info", "7a77777 checked PASS")    // QA of another team
	line(t, db, at, "axi:qanda", "info", "8b88888 checked PASS")                  // not a QA role, in the lead's project
	line(t, db, at, "crius:lead", "info", "86a776a checked PASSWORD reset")       // PASS only as part of a word
	line(t, db, at, "crius:lead", "info", "abcdef1 checked PASS")
	mixed := line(t, db, at, "crius:lead", "info", "9d9d9d9 re-checked PASS")
	g := &Goal{Milestones: []Milestone{
		{State: Done, Text: "step 4", Refs: "b3a87cbf12, PR 7"},                    // longer sha in the block
		{State: Done, Text: "session gaps", Refs: "5f74e59"},                       // only claimed
		{State: Done, Text: "not yet", Refs: "1eb7a34, 2a22222, 3b33333, 4d44444"}, // no, FAIL, FAIL, blocked
		{State: Done, Text: "question", Refs: "5e55555"},
		{State: Done, Text: "run id", Refs: "18234567890, b5555555"},          // digits only; inside a longer word
		{State: Done, Text: "other teams", Refs: "6f66666, 7a77777, 9c644de"}, // not QA; another team's QA
		{State: Done, Text: "word", Refs: "86a776a"},
		{State: Done, Text: "forms", Refs: "sha=9D9D9D9 PR-3"}, // sha=, upper case, a space list
		{State: Done, Text: "check named me", Refs: "bus 2026-10-17T04:27"},
		{State: Done, Text: "check named no one", Refs: "bus 2026-10-17T02:27"}, // Crius's line names no lead
		{State: Done, Text: "prose", Refs: "checked PASS"},                      // words, not a sha
		{State: Doing, Text: "in progress", Refs: "b3a87cb"},                    // only done milestones count
	}}
	got, err := Verified(db, "crius:build", g)
	if err != nil {
		t.Fatal(err)
	}
	if want := []int64{crius, 0, 0, 0, 0, 0, 0, mixed, named, 0, 0, 0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	// The lead's own QA verifies by sha and by bus minute.
	g = &Goal{Milestones: []Milestone{{State: Done, Text: "a", Refs: "9c644de"}, {State: Done, Text: "b", Refs: "bus 2026-10-17T03:27"},
		{State: Done, Text: "c", Refs: "8b88888"}}}
	if got, err = Verified(db, "axi:leader", g); err != nil || !reflect.DeepEqual(got, []int64{qa, qa, 0}) {
		t.Fatal(got, err)
	}
}

// Review of a4908e3: a check about webapi:lead does not name api:lead; "PASS ?", "not yet", a lower-case "failed"
// are no pass; a word of the check made only of digits is a run id, not a sha.
func TestVerifiedReviewOfA4908e3(t *testing.T) {
	db := open(t)
	at := time.Date(2026, 10, 17, 5, 10, 0, 0, time.Local)
	line(t, db, at, "crius:lead", "info", "webapi:lead deploy checked PASS")
	line(t, db, at.Add(time.Minute), "crius:lead", "info", "lead-fe checked PASS for api:lead-fe")
	named := line(t, db, at.Add(2*time.Minute), "crius:lead", "info", "checked PASS (api:lead, step 2)")
	line(t, db, at, "crius:lead", "info", "aaa1111 checked PASS ? not sure")
	line(t, db, at, "crius:lead", "info", "bbb2222 not yet checked PASS")
	line(t, db, at, "crius:lead", "info", "ccc3333 checked PASS but e2e failed")
	line(t, db, at, "crius:lead", "info", "checked PASS run 1234567")
	g := &Goal{Milestones: []Milestone{
		{State: Done, Text: "a", Refs: "bus 2026-10-17T05:10"},
		{State: Done, Text: "b", Refs: "bus 2026-10-17T05:11"},
		{State: Done, Text: "c", Refs: "bus 2026-10-17T05:12"},
		{State: Done, Text: "d", Refs: "aaa1111, bbb2222, ccc3333"},
		{State: Done, Text: "e", Refs: "1234567abc"},
	}}
	got, err := Verified(db, "api:lead", g)
	if err != nil {
		t.Fatal(err)
	}
	if want := []int64{0, 0, named, 0, 0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}
