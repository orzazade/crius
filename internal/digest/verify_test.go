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
	if _, err := db.Exec("update event set refs = ? where id = ?", `{"to":["`+strings.ReplaceAll(to, ", ", `","`)+`"]}`, id); err != nil {
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
	// A milestone not marked done is done by its VERIFIED line (crius:lead 10-25 21:22; it was not counted before).
	notDone := verified(t, db, at, "crius:lead", "crius:build", "c5c5c5c · a milestone not done")
	// Not counted: a run id, no match, other case, no evidence, two milestones.
	verified(t, db, at, "crius:lead", "crius:build", "9999999 · digits only: a run id, not a sha")
	verified(t, db, at, "crius:lead", "crius:build", "zzz · matches nothing")
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
	if want := []int64{sha, tick, byText, 0, 0, notDone, 0, 0}; !reflect.DeepEqual(got, want) || notCounted != 8 {
		t.Fatalf("got %v, %d not counted; want %v, 8", got, notCounted, want)
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
// match points at one milestone. A check of a milestone not yet done is not credited to a done one that only
// mentions its ref; it makes that milestone done (crius:lead 10-25 21:22). In rows mode a "row <id>" line counts for its row and still for the milestone it matches.
func TestVerifiedLooksAtEveryMilestoneAndRowsKeepMilestones(t *testing.T) {
	db := open(t)
	at := time.Date(2026, 10, 17, 5, 0, 0, 0, time.Local)
	deploy := verified(t, db, at, "crius:lead", "quotes:lead", "RB-90 · deploy checked") // M2 not marked done: done by it
	tick := verified(t, db, at, "crius:lead", "quotes:lead", "row F-126 · ticks seen")
	verified(t, db, at, "crius:lead", "quotes:lead", "row F-127 · a row only")
	g := &Goal{DoneHow: "rows: 33 of 130 in M1-BUILD.md", Milestones: []Milestone{
		{State: Done, Text: "M1 build, unblocks RB-90", Refs: "RB-89"},
		{State: Todo, Text: "M2 deploy", Refs: "RB-90"},
		{State: Done, Text: "quotes ticks", Refs: "F-126"},
	}}
	got, rows, notCounted, err := Verified(db, "quotes:lead", g)
	if err != nil {
		t.Fatal(err)
	}
	if want := []int64{0, deploy, tick}; !reflect.DeepEqual(got, want) || rows != 2 || notCounted != 0 {
		t.Fatalf("got %v, %d rows, %d not counted; want %v, 2, 0", got, rows, notCounted, want)
	}
}

// Review of 26e2143: a bug found after acceptance withdraws it (RULES 2.3) and the lead un-ticks the milestone; the
// old VERIFIED line then no longer makes it done. A later FAIL, CORRECTION, CORRECTED or REOPEN bus line from the
// project or Crius, or a revoke, that names an id or sha of the milestone's refs in any case withdraws it; a VERIFIED
// after that counts again. Review of 1f2e54d: a role tag or the milestone's text names nothing, a store field
// correction withdraws nothing. Review of 19d3040: an id or sha in the milestone's text names it too, a team log line
// of the project withdraws as a bus line does, and a base or next sha is another commit. Review of 24b07b1: any other
// sha of the line names, Crius's team log names its own items, a lower-case word (slate-400) is no id, and a store
// revoke is not read. Review of 48708f9: a base on a branch (base main 441055ff) is another commit too; review of 3c7fc4f: only the sha
// of the base or next is, not a later sha of the line or one after a clause break; review of b288e8f: base/main@sha, base-sha, next.sha and
// base next/main@sha are the base too, firebase sha is not; review of b5023f4: main-sha, origin/main.sha and
// 20261029@sha are too, while a sha past three words of the clause is not; review of 02e9a5a: base X next Y skips both. A row line never closes an open milestone, also in a lead that counts milestones.
func TestAWithdrawnAcceptanceDoesNotCloseAnOpenMilestone(t *testing.T) {
	db := open(t)
	at := time.Date(2026, 10, 25, 21, 0, 0, 0, time.Local)
	keyed := func(key, sender, kind, text string) {
		id := line(t, db, at, sender, kind, text)
		if _, err := db.Exec("update event set source_key = ? where id = ?", fmt.Sprint(key, id), id); err != nil {
			t.Fatal(err)
		}
	}
	bus := func(sender, kind, text string) { keyed("bus:f:", sender, kind, text) }
	verified(t, db, at, "crius:lead", "quotes:lead", "RB-91 · checked")
	bus("quotes:qa1", "fail", "qa1 fail rb-91 breaks again on 7090")
	bus("quotes:qa1", "fail", "QA1 FAIL c7c7c7c before it was checked")
	early := verified(t, db, at, "crius:lead", "quotes:lead", "c7c7c7c · checked after the fail")
	verified(t, db, at, "crius:lead", "quotes:lead", "RB-93 · checked")
	bus("quotes:qa1", "fail", "FAIL RB-93")
	again := verified(t, db, at, "crius:lead", "quotes:lead", "RB-93 · checked again on the fix")
	verified(t, db, at, "crius:lead", "quotes:lead", "RB-97 · checked")
	bus("crius:lead", "corrected", "my VERIFIED of RB-97 was wrong: the build was stale")
	verified(t, db, at, "crius:lead", "quotes:lead", "RB-98 · checked")
	bus("quotes:lead", "reopen", "REOPEN RB-98: the fix broke the list")
	verified(t, db, at, "crius:lead", "quotes:lead", "RB-99 · checked")
	bus("quotes:lead", "correction", "my DONE of rb-99 named the wrong build")
	revoked := verified(t, db, at, "crius:lead", "quotes:lead", "F-50 · checked")
	revoke := line(t, db, at, "quotes:lead", "revoked", "revoked F-50 on 1a2b3c4 (D-9, by qa1); opened fix F-51: breaks RB-94")
	if _, err := db.Exec("update event set item = 'F-50' where id = ?", revoke); err != nil {
		t.Fatal(err)
	}
	verified(t, db, at, "crius:lead", "quotes:lead", "F-60 · checked")
	bus("quotes:qa1", "fail", "qa1 fail f-60 again")
	verified(t, db, at, "crius:lead", "quotes:lead", "RB-70 · checked")
	keyed("team:f:", "quotes:lead", "corrected", "CORRECTED my RB-70 check was on a stale build")
	verified(t, db, at, "crius:lead", "quotes:lead", "7f76684 · checked")
	bus("quotes:qa1", "fail", "FAIL 7f76684 on the rig")
	base := verified(t, db, at, "crius:lead", "quotes:lead", "441055ff · checked")
	next := verified(t, db, at, "crius:lead", "quotes:lead", "52a52a5 · checked")
	bus("quotes:qa1", "fail", "QA1 FAIL F22 2e90ad27 base main 441055ff next 52a52a5")
	verified(t, db, at, "crius:lead", "quotes:lead", "2a6560a4 · checked")
	verified(t, db, at, "crius:lead", "quotes:lead", "c3c3c3c3 · checked")
	verified(t, db, at, "crius:lead", "quotes:lead", "d4d4d4d4 · checked")
	verified(t, db, at, "crius:lead", "quotes:lead", "e5e5e5e5 · checked")
	onMain := verified(t, db, at, "crius:lead", "quotes:lead", "9a9a9a9a · checked")
	verified(t, db, at, "crius:lead", "quotes:lead", "f6f6f6f6 · checked")
	bus("quotes:qa1", "fail", "QA1 FAIL F23 e1e1e1e1 base 9a9a9a9a; c3c3c3c3 also fails")
	bus("quotes:qa1", "fail", "QA1 retest on next build; d4d4d4d4 still fails")
	bus("quotes:qa1", "fail", "FAIL F24 on base 9a9a9a9a e5e5e5e5 (head)")
	bus("quotes:qa1", "fail", "FAIL F25 on base: origin/main@9a9a9a9a")
	bus("quotes:qa1", "fail", "QA1 FAIL aec8b704 base/main@9a9a9a9a")
	bus("quotes:qa1", "fail", "QA1 FAIL aec8b704 base next/main@9a9a9a9a")
	bus("quotes:qa1", "fail", "QA1 FAIL aec8b704 base-9a9a9a9a, next.9a9a9a9a")
	bus("quotes:qa1", "fail", "QA1 FAIL login on firebase f6f6f6f6")
	bus("quotes:qa1", "fail", "QA1 FAIL aec8b704 base main-9a9a9a9a; base origin/main.9a9a9a9a; base 20261029@9a9a9a9a")
	verified(t, db, at, "crius:lead", "quotes:lead", "b7b7b7b7 · checked")
	bus("quotes:qa1", "fail", "QA1 FAIL base image is stale so b7b7b7b7 fails")
	bus("quotes:qa1", "fail", "FAIL QA1 bug 1 (6b03f155 + d64c0078) on 2a6560a4")
	slate := verified(t, db, at, "crius:lead", "quotes:lead", "F-61 · checked")
	bus("quotes:qa1", "fail", "FAIL F-88: text slate-400 too light")
	other := verified(t, db, at, "crius:lead", "quotes:lead", "RB-94 · checked")
	bus("axi:qa", "fail", "FAIL RB-94 of another project")
	keyed("team:f:", "crius:lead", "corrected", "CORRECTED my RB-94 row count") // its own team log
	revoke = line(t, db, at, "quotes:lead", "revoked", "revoked F-53 on 9f9f9f9 (D-10, by qa1); opened fix F-54: as RB-94")
	if _, err := db.Exec("update event set item = 'F-53' where id = ?", revoke); err != nil {
		t.Fatal(err)
	}
	bus("quotes:qa1", "fail", "FAIL RB-940 is another id; QA1 P2 tags name nothing")
	line(t, db, at, "quotes:lead", "correction", "correction C-1: F-52 title RB-94 old -> new (by lead)")
	done := verified(t, db, at, "crius:lead", "quotes:lead", "RB-95 · checked")
	bus("quotes:qa1", "fail", "FAIL RB-95")
	verified(t, db, at, "crius:lead", "quotes:lead", "row F-12 · a row line")
	g := &Goal{Milestones: []Milestone{
		{State: Doing, Text: "withdrawn", Refs: "RB-91"},
		{State: Todo, Text: "failed before", Refs: "c7c7c7c"},
		{State: Doing, Text: "checked again", Refs: "RB-93"},
		{State: Doing, Text: "Crius corrected", Refs: "RB-97"},
		{State: Doing, Text: "reopened", Refs: "RB-98"},
		{State: Doing, Text: "corrected on the bus", Refs: "RB-99"},
		{State: Doing, Text: "revoked", Refs: "F-50"},
		{State: Doing, Text: "QA1 signs off after P2", Refs: "RB-94, by QA1 after P2"},
		{State: Done, Text: "marked done", Refs: "RB-95"},
		{State: Todo, Text: "a row", Refs: "row F-12"},
		{State: Doing, Text: "F-60 login fix", Refs: ""},
		{State: Doing, Text: "a team log check", Refs: "RB-70"},
		{State: Doing, Text: "a dirty build", Refs: "7f76684-dirty"},
		{State: Doing, Text: "the base of the next", Refs: "441055ff"},
		{State: Doing, Text: "the next one", Refs: "52a52a5"},
		{State: Doing, Text: "the head under test", Refs: "2a6560a4"},
		{State: Doing, Text: "F-61 cards use slate-400", Refs: ""},
		{State: Doing, Text: "fails after a base", Refs: "c3c3c3c3"},
		{State: Doing, Text: "fails after rebased on next", Refs: "d4d4d4d4"},
		{State: Doing, Text: "fails on a base", Refs: "e5e5e5e5"},
		{State: Doing, Text: "the base on origin/main", Refs: "9a9a9a9a"},
		{State: Doing, Text: "fails on firebase", Refs: "f6f6f6f6"},
		{State: Doing, Text: "fails three words after base", Refs: "b7b7b7b7"},
	}}
	got, _, notCounted, err := Verified(db, "quotes:lead", g)
	if err != nil {
		t.Fatal(err)
	}
	if want := []int64{0, early, again, 0, 0, 0, revoked, other, done, 0, 0, 0, 0, base, next, 0, slate, 0, 0, 0, onMain, 0, 0}; !reflect.DeepEqual(got, want) || notCounted != 1 {
		t.Fatalf("got %v, %d not counted; want %v, 1", got, notCounted, want)
	}
}
