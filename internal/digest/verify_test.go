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

// verified stores a VERIFIED line as the ingest does: the sender's project and role, the recipient in refs "to".
func verified(t *testing.T, db *sql.DB, ts time.Time, sender, to, text string) int64 {
	t.Helper()
	id := line(t, db, ts, sender, "verified", text)
	if _, err := db.Exec("update event set refs = ? where id = ?", `{"to":["`+to+`"]}`, id); err != nil {
		t.Fatal(err)
	}
	return id
}

// RULES 3.4 (Crius 10-17 02:57): a done milestone is verified only by a VERIFIED line to its lead from Crius
// (crius:lead) or the lead's own QA (qa, qa-*), "<ref> · <evidence>": ref is a sha (7+ hex with a letter, any case,
// prefix either way), one of the milestone's refs, or its text (any case). A trusted VERIFIED line to the lead that
// matches no done milestone is counted, so a check that does not land is seen.
func TestVerifiedNeedsAVerifiedLineToTheLead(t *testing.T) {
	db := open(t)
	at := time.Date(2026, 10, 17, 2, 57, 0, 0, time.Local)
	sha := verified(t, db, at, "crius:lead", "crius:build", "B3A87CB · step 4: 31/34")
	tick := verified(t, db, at, "crius:lead", "crius:build", "f-126 · ticks seen")
	byText := verified(t, db, at, "crius:qa-api", "crius:build", "Step 5 digest · rc 0")
	verified(t, db, at, "crius:lead", "webapi:build", "c0ffee1 · to another lead")
	verified(t, db, at, "axi:qa", "crius:build", "d00d123 · QA of another project")
	verified(t, db, at, "crius:build", "crius:build", "e1e1e1e · the builder itself")
	verified(t, db, at, "crius:qanda", "crius:build", "f2f2f2f · not a QA role")
	line(t, db, at, "crius:lead", "info", "a1a1a1a checked PASS") // prose no longer counts
	info := line(t, db, at, "crius:lead", "info", "a1a1a1a · an info line to the lead")
	if _, err := db.Exec(`update event set refs = '{"to":["crius:build"]}' where id = ?`, info); err != nil {
		t.Fatal(err)
	}
	verified(t, db, at.Add(time.Hour), "crius:lead", "crius:build", "b3a87cb · checked again: the first check stays")
	verified(t, db, at, "crius:lead", "crius:build", "9999999 · digits only: a run id, not a sha")
	verified(t, db, at, "crius:lead", "crius:build", "zzz · matches nothing")
	verified(t, db, at, "crius:lead", "crius:build", "c5c5c5c · a milestone not done")
	g := &Goal{Milestones: []Milestone{
		{State: Done, Text: "step 4", Refs: "b3a87cbf12, bus 2026-10-17T02:27"},
		{State: Done, Text: "quotes ticks", Refs: "F-126, ccc9039"},
		{State: Done, Text: "step 5 digest", Refs: "run 7"},
		{State: Done, Text: "others", Refs: "c0ffee1, d00d123, e1e1e1e, f2f2f2f, a1a1a1a"},
		{State: Done, Text: "run", Refs: "99999990"},
		{State: Doing, Text: "in progress", Refs: "c5c5c5c"},
	}}
	got, unmatched, err := Verified(db, "crius:build", g)
	if err != nil {
		t.Fatal(err)
	}
	if want := []int64{sha, tick, byText, 0, 0, 0}; !reflect.DeepEqual(got, want) || unmatched != 3 {
		t.Fatalf("got %v, %d unmatched; want %v, 3", got, unmatched, want)
	}
}
