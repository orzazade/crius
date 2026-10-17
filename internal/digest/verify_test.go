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
// (crius:lead) or the lead's own QA (qa, qa-*), "<ref> · <evidence>" for one milestone: ref is a sha (7+ hex with a
// letter, any case, prefix either way), one of the milestone's refs, or its text (both exact). A trusted VERIFIED
// line to the lead that counts for no milestone (no match, two matches, no evidence) is counted as not counted, so a
// check that does not land is seen (review of 636d4da).
func TestVerifiedNeedsAVerifiedLineToTheLead(t *testing.T) {
	db := open(t)
	at := time.Date(2026, 10, 17, 2, 57, 0, 0, time.Local)
	sha := verified(t, db, at, "crius:lead", "crius:build", "B3A87CB · step 4: 31/34")
	tick := verified(t, db, at, "crius:lead", "crius:build", "F-126 · ticks seen")
	byText := verified(t, db, at, "crius:qa-api", "crius:build", "Sources flow · mail · rc 0") // text with a " · "
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
	// Not counted: a run id, no match, a milestone not done, other case, no evidence, two milestones.
	verified(t, db, at, "crius:lead", "crius:build", "9999999 · digits only: a run id, not a sha")
	verified(t, db, at, "crius:lead", "crius:build", "zzz · matches nothing")
	verified(t, db, at, "crius:lead", "crius:build", "c5c5c5c · a milestone not done")
	verified(t, db, at, "crius:lead", "crius:build", "f-126 · the ref in another case")
	verified(t, db, at, "crius:lead", "crius:build", "sources flow · mail · the text in another case")
	verified(t, db, at, "crius:lead", "crius:build", "ccc9039")
	verified(t, db, at, "crius:lead", "crius:build", "ccc9039 · ")
	verified(t, db, at, "crius:lead", "crius:build", "step 4 · ")
	verified(t, db, at, "crius:lead", "crius:build", "77aa77a · two milestones carry it")
	g := &Goal{Milestones: []Milestone{
		{State: Done, Text: "step 4", Refs: "b3a87cbf12, bus 2026-10-17T02:27"},
		{State: Done, Text: "quotes ticks", Refs: "F-126, ccc9039"},
		{State: Done, Text: "Sources flow · mail", Refs: "run 7"},
		{State: Done, Text: "others", Refs: "c0ffee1, d00d123, e1e1e1e, f2f2f2f, a1a1a1a"},
		{State: Done, Text: "run", Refs: "99999990"},
		{State: Doing, Text: "in progress", Refs: "c5c5c5c"},
		{State: Done, Text: "api", Refs: "77aa77a"},
		{State: Done, Text: "ui", Refs: "77aa77a"},
	}}
	got, _, notCounted, err := Verified(db, "crius:build", g)
	if err != nil {
		t.Fatal(err)
	}
	if want := []int64{sha, tick, byText, 0, 0, 0, 0, 0}; !reflect.DeepEqual(got, want) || notCounted != 9 {
		t.Fatalf("got %v, %d not counted; want %v, 9", got, notCounted, want)
	}
}

// Crius 10-17 04:23: a VERIFIED ref matches any sha (prefix either way) or id token on the milestone line, in its
// text or its refs, not only a whole ref: quotes' refs read "UI c84d78c (F-118, on rig data, RB-89)". An id has a
// letter and a digit, in the same case; a plain word or number is no id.
func TestVerifiedMatchesAnyIdOrShaOnTheMilestoneLine(t *testing.T) {
	db := open(t)
	at := time.Date(2026, 10, 17, 4, 23, 0, 0, time.Local)
	inRefs := verified(t, db, at, "crius:lead", "quotes:lead", "F-118 · on rig data")
	inText := verified(t, db, at, "crius:lead", "quotes:lead", "F-97 · markup edit")
	sha := verified(t, db, at, "crius:lead", "quotes:lead", "F651D23A · prefix of a sha in a ref")
	textSha := verified(t, db, at, "crius:lead", "quotes:lead", "9a8b7c6 · a sha in the text")
	verified(t, db, at, "crius:lead", "quotes:lead", "rig · a word, no id")
	verified(t, db, at, "crius:lead", "quotes:lead", "2 · a number, no id")
	verified(t, db, at, "crius:lead", "quotes:lead", "rb-89 · an id in another case")
	g := &Goal{Milestones: []Milestone{
		{State: Done, Text: "Pricing", Refs: "UI c84d78c (F-118, on rig data, RB-89)"},
		{State: Done, Text: "B3 rest: distributor markup edit (F-97)", Refs: "waits 2 days"},
		{State: Done, Text: "promotions", Refs: "ccc9039 + f651d23 (F-110)"},
		{State: Done, Text: "merged as 9a8b7c6", Refs: "run 7"},
	}}
	got, _, notCounted, err := Verified(db, "quotes:lead", g)
	if err != nil {
		t.Fatal(err)
	}
	if want := []int64{inRefs, inText, sha, textSha}; !reflect.DeepEqual(got, want) || notCounted != 3 {
		t.Fatalf("got %v, %d not counted; want %v, 3", got, notCounted, want)
	}
}

// Review of 2edcda5: an exact match (the milestone's text, one of its refs, a sha of its refs) wins over an id or sha
// that another milestone only mentions, so a line that counted before still counts. Loose matches count only when no
// milestone matches exactly; two loose hits are still not counted.
func TestVerifiedPrefersAnExactMatch(t *testing.T) {
	db := open(t)
	at := time.Date(2026, 10, 17, 4, 50, 0, 0, time.Local)
	exact := verified(t, db, at, "crius:lead", "quotes:lead", "F-118 · ok")
	sha := verified(t, db, at, "crius:lead", "quotes:lead", "c84d78c · the sha in Pricing's refs")
	verified(t, db, at, "crius:lead", "quotes:lead", "M1 · two milestones mention it")
	prefix := verified(t, db, at, "crius:lead", "quotes:lead", "D00D1E5 · a prefix of UI's sha, which Docs only mentions")
	g := &Goal{Milestones: []Milestone{
		{State: Done, Text: "Pricing", Refs: "F-118, c84d78c"},
		{State: Done, Text: "Promotions on F-118 data, rebased onto c84d78c, M1", Refs: "ccc9039"},
		{State: Done, Text: "M1 lines", Refs: "run 7"},
		{State: Done, Text: "UI", Refs: "UI d00d1e5f (F-200)"},
		{State: Done, Text: "Docs, after d00d1e5", Refs: "run 8"},
	}}
	got, _, notCounted, err := Verified(db, "quotes:lead", g)
	if err != nil {
		t.Fatal(err)
	}
	if want := []int64{exact, 0, 0, prefix, 0}; !reflect.DeepEqual(got, want) || notCounted != 1 || sha == 0 {
		t.Fatalf("got %v, %d not counted; want %v, 1", got, notCounted, want)
	}
}

// Review of 178deae: the strongest match is looked for over all milestones, done or not; a line counts only when that
// match points at one done milestone. A check of a milestone not yet done is not credited to a done one that only
// mentions its ref. In rows mode an F-id line counts for its row and still for the milestone it matches.
func TestVerifiedLooksAtEveryMilestoneAndRowsKeepMilestones(t *testing.T) {
	db := open(t)
	at := time.Date(2026, 10, 17, 5, 0, 0, 0, time.Local)
	verified(t, db, at, "crius:lead", "quotes:lead", "RB-90 · deploy checked")
	tick := verified(t, db, at, "crius:lead", "quotes:lead", "F-126 · ticks seen")
	verified(t, db, at, "crius:lead", "quotes:lead", "F-127 · a row only")
	g := &Goal{DoneHow: "M1 rows: 33 of 130 ticked", Milestones: []Milestone{
		{State: Done, Text: "M1 build, unblocks RB-90", Refs: "RB-89"},
		{State: Todo, Text: "M2 deploy", Refs: "RB-90"},
		{State: Done, Text: "quotes ticks", Refs: "F-126"},
	}}
	got, rows, notCounted, err := Verified(db, "quotes:lead", g)
	if err != nil {
		t.Fatal(err)
	}
	if want := []int64{0, 0, tick}; !reflect.DeepEqual(got, want) || rows != 3 || notCounted != 0 {
		t.Fatalf("got %v, %d rows, %d not counted; want %v, 3, 0", got, rows, notCounted, want)
	}
}
