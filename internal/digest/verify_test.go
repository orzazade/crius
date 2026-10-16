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

// Step 5 P3 (Crius 02:29): a done milestone counts only when a Crius or QA check on the bus says PASS and names one
// of its refs: a sha (7 or more hex, matched on the first 7) or "bus <local YYYY-MM-DDTHH:MM>", the check line itself.
func TestVerifiedNeedsACheckerPassNamingARef(t *testing.T) {
	db := open(t)
	at := time.Date(2026, 10, 17, 2, 27, 0, 0, time.Local)
	crius := line(t, db, at, "crius:lead", "info", "b3a87cb checked PASS (31/34). Merge + push.")
	qa := line(t, db, at.Add(time.Hour), "axi:qa-api", "done", "item=axi#7 sha=9c644de0 PASS on env004")
	line(t, db, at, "crius:build", "done", "5f74e59 suite PASS, mutants red")       // the builder's own claim
	line(t, db, at, "crius:lead", "info", "1eb7a34 checked: FAIL on the leak case") // a check that failed
	line(t, db, at, "crius:lead", "info", "86a776a PASSWORD field reset")           // PASS only as part of a word
	g := &Goal{Milestones: []Milestone{
		{State: Done, Text: "step 4", Refs: "b3a87cbf12, PR 7"},               // longer sha in the block than on the bus
		{State: Done, Text: "route", Refs: "9c644de"},                         // a QA check of another project's team
		{State: Done, Text: "session gaps", Refs: "5f74e59"},                  // only claimed
		{State: Done, Text: "redaction", Refs: "1eb7a34, 86a776a"},            // failed check, a word that is not PASS
		{State: Done, Text: "closed", Refs: "bus 2026-10-17T02:27"},           // points at the check line
		{State: Done, Text: "other", Refs: "run 24628, bus 2026-10-17T03:27"}, // the QA check at that minute
		{State: Done, Text: "prose", Refs: "checked PASS"},                    // words, not a sha, even when a check holds them
		{State: Doing, Text: "in progress", Refs: "b3a87cb"},                  // only done milestones are checked
	}}
	got, err := Verified(db, g)
	if err != nil {
		t.Fatal(err)
	}
	if want := []int64{crius, qa, 0, 0, crius, qa, 0, 0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}
