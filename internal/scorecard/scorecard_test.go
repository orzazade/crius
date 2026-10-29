package scorecard

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

	"github.com/orzazade/crius"
	"github.com/orzazade/crius/internal/quota"
	"github.com/orzazade/crius/internal/store"
	"github.com/orzazade/crius/internal/testutil"
)

const rev = "abc1234def5678abc1234def5678abc1234def56"

// The owner's zone: week ends at 00:00 UTC are 02:00 local, on the day the file is named after.
func TestMain(m *testing.M) {
	time.Local = time.FixedZone("CEST", 2*3600)
	os.Exit(m.Run())
}

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// fixture: one Max sample in the epoch ending 09-17 00:00 UTC (week A = 09-10..09-17, week B the next 7 days).
// Week A accepts demo#1 (code), demo#2 (research) and demo#3 (a child task of demo#1, not an outcome); week B
// accepts demo#4 (code).
func fixture(t *testing.T) (db *sql.DB, home string) {
	t.Helper()
	_, home = testutil.Temp(t)
	if err := store.InitLayout(home, crius.ProjectsExample); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec("insert into quota (source_key, ts, account, epoch, seven_day_pct, sample_kind) values" +
		" ('q1', '2026-09-15T10:00:00Z', 'max', 1789603200, 71, 'session')"); err != nil {
		t.Fatal(err)
	}
	for _, it := range []struct{ title, kind, parent, opened, accepted string }{
		{"price fix", "code", "", "2026-09-11T08:00:00Z", "2026-09-12T08:00:00Z"},
		{"market scan", "research", "", "2026-09-11T08:00:00Z", "2026-09-13T08:00:00Z"},
		{"child task", "code", "demo#1", "2026-09-11T09:00:00Z", "2026-09-12T07:00:00Z"},
		{"mail fix", "code", "", "2026-09-17T08:00:00Z", "2026-09-18T08:00:00Z"},
	} {
		id, err := store.RecordItem(db, "demo", it.title, it.kind, []string{"CI pass"}, it.parent, false)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("update item set opened = ?, state = 'accepted' where id = ?", it.opened, id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("insert into acceptance (item, revision, checks, review_ref, accepted_by, ts)"+
			" values (?, ?, '[]', 'event:1', 'crius', ?)", id, rev, it.accepted); err != nil {
			t.Fatal(err)
		}
	}
	return db, home
}

func weekOf(t *testing.T, db *sql.DB, ts string) Week {
	t.Helper()
	w, err := WeekOf(db, at(ts))
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func build(t *testing.T, db *sql.DB, week, now string) *Card {
	t.Helper()
	c, err := Build(db, weekOf(t, db, week), demoProjects, at(now))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func revoke(t *testing.T, db *sql.DB, item string) {
	t.Helper()
	out, err := store.RecordDefect(db, store.DefectInput{Item: item, Title: "sends twice", FoundBy: "owner",
		Severity: "S2", Class: "missing test"})
	if err != nil {
		t.Fatal(err)
	}
	d := strings.Fields(out)[1]
	if _, err := store.RecordRevoke(db, store.RevokeInput{Item: item, Defect: d, Reason: "sends twice", By: "crius",
		Criteria: []string{"red test first"}}); err != nil {
		t.Fatal(err)
	}
}

func TestWeeksStepSevenDaysFromSampledEpochs(t *testing.T) {
	_, home := testutil.Temp(t)
	if err := store.InitLayout(home, crius.ProjectsExample); err != nil {
		t.Fatal(err)
	}
	empty, err := store.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer empty.Close()
	if _, err := WeekOf(empty, at("2026-09-12T00:00:00Z")); !errors.Is(err, ErrNoWeeks) {
		t.Fatalf("no samples: want ErrNoWeeks, got %v", err)
	}
	db, _ := fixture(t)
	// a second epoch 22 days on: the gap is cut into 7-day weeks back from it, the one left over is short
	if _, err := db.Exec("insert into quota (source_key, ts, account, epoch, seven_day_pct, sample_kind) values" +
		" ('q2', '2026-10-05T10:00:00Z', 'max', 1791504000, 20, 'session')"); err != nil {
		t.Fatal(err)
	}
	for ts, want := range map[string][2]string{
		"2026-09-16T23:59:59Z": {"2026-09-10T00:00:00Z", "2026-09-17T00:00:00Z"},
		"2026-09-10T00:00:00Z": {"2026-09-10T00:00:00Z", "2026-09-17T00:00:00Z"},
		"2026-09-02T00:00:00Z": {"2026-08-27T00:00:00Z", "2026-09-03T00:00:00Z"},
		"2026-09-17T00:00:00Z": {"2026-09-17T00:00:00Z", "2026-09-18T00:00:00Z"},
		"2026-09-20T00:00:00Z": {"2026-09-18T00:00:00Z", "2026-09-25T00:00:00Z"},
		"2026-10-09T00:00:00Z": {"2026-10-09T00:00:00Z", "2026-10-16T00:00:00Z"},
	} {
		w := weekOf(t, db, ts)
		if !w.Start.Equal(at(want[0])) || !w.End.Equal(at(want[1])) {
			t.Errorf("%s: week %s..%s, want %s..%s", ts, w.Start.UTC(), w.End.UTC(), want[0], want[1])
		}
	}
}

func TestOutcomesCountTopLevelItemsOfTheWeek(t *testing.T) {
	db, _ := fixture(t)
	a := build(t, db, "2026-09-12T00:00:00Z", "2026-09-20T00:00:00Z")
	if n := a.Numbers; n.Code != 1 || n.Research != 1 || n.Revoked != 0 || n.MaxPct != 71 {
		t.Fatalf("week A: %+v\n%s", n, a.Text)
	}
	for _, want := range []string{"code 1 · research 1 · falcon 0 · other 0 (provisional", "71% observed",
		"status complete", "def " + Def} {
		if !strings.Contains(a.Text, want) {
			t.Errorf("week A text lacks %q:\n%s", want, a.Text)
		}
	}
	if strings.Count(a.Text, "def v") != strings.Count(a.Text, "def "+Def) {
		t.Errorf("the text names another definition than %s:\n%s", Def, a.Text)
	}
	b := build(t, db, "2026-09-18T00:00:00Z", "2026-09-20T00:00:00Z")
	if n := b.Numbers; n.Code != 1 || n.Research != 0 || n.MaxPct != -1 {
		t.Fatalf("week B: %+v", n)
	}
	if !strings.Contains(b.Text, "status partial") || !strings.Contains(b.Text, "(running") {
		t.Errorf("week B is running with no sample:\n%s", b.Text)
	}
	if m := build(t, db, "2026-09-12T00:00:00Z", "2026-10-02T00:00:00Z"); !strings.Contains(m.Text, "(matured") {
		t.Errorf("14 days after its end week A is matured:\n%s", m.Text)
	}
}

// §9 step 6: a revoke keeps the acceptance row and lowers the original week, not the week it was found in.
func TestRevokeLowersTheWeekItWasAcceptedIn(t *testing.T) {
	db, _ := fixture(t)
	revoke(t, db, "demo#1")
	a := build(t, db, "2026-09-12T00:00:00Z", "2026-10-20T00:00:00Z")
	if n := a.Numbers; n.Code != 0 || n.Research != 1 || n.Revoked != 1 || n.Defects != 1 || n.Escapes != 1 {
		t.Fatalf("week A after the revoke: %+v\n%s", n, a.Text)
	}
	if !strings.Contains(a.Text, "demo#1") || !strings.Contains(a.Text, "D-1") {
		t.Errorf("week A names the revoked outcome and its defect:\n%s", a.Text)
	}
	now := weekOf(t, db, time.Now().UTC().Format(time.RFC3339)) // the week the revoke was found in
	if c, err := Build(db, now, demoProjects, time.Now()); err != nil || c.Numbers.Revoked != 0 || c.Numbers.Defects != 0 {
		t.Fatalf("the week found: %+v %v", c, err)
	}
}

// §9 step 6: a correction changes the view, not the row.
func TestCorrectionChangesTheScorecard(t *testing.T) {
	db, _ := fixture(t)
	for _, c := range []store.CorrectInput{
		{Target: "demo#2", Field: "kind", Value: "code", Reason: "it shipped code", By: "crius"},
		{Target: "demo#4", Field: "parent", Value: "demo#1", Reason: "part of the price fix", By: "crius"},
	} {
		if _, err := store.RecordCorrect(db, c); err != nil {
			t.Fatal(err)
		}
	}
	if a := build(t, db, "2026-09-12T00:00:00Z", "2026-09-20T00:00:00Z"); a.Numbers.Code != 2 || a.Numbers.Research != 0 {
		t.Fatalf("week A after the kind correction: %+v", a.Numbers)
	}
	if b := build(t, db, "2026-09-18T00:00:00Z", "2026-09-20T00:00:00Z"); b.Numbers.Code != 0 {
		t.Fatalf("week B: demo#4 is a child task now: %+v", b.Numbers)
	}
	var kind string
	if err := db.QueryRow("select kind from item where id = 'demo#2'").Scan(&kind); err != nil || kind != "research" {
		t.Fatalf("the item row changed: %q %v", kind, err)
	}
}

// §7b: re-writing a changed week appends `revised <date>: …` and keeps the old value; an unchanged week is left alone.
// The revised line names the store events since, not a bus line of the same kind.
func TestRewriteAppendsARevisedLine(t *testing.T) {
	db, home := fixture(t)
	dir := filepath.Join(home, "state", "scorecards")
	if _, _, err := Write(db, dir, build(t, db, "2026-09-18T00:00:00Z", "2026-09-20T00:00:00Z"), at("2026-09-20T00:00:00Z")); !errors.Is(err, ErrRunning) {
		t.Fatalf("a running week is not written: %v", err)
	}
	first := build(t, db, "2026-09-12T00:00:00Z", "2026-09-18T06:00:00Z")
	path, what, err := Write(db, dir, first, at("2026-09-18T06:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "2026-09-17.md" || what != "wrote" {
		t.Fatalf("path %s", path)
	}
	orig := testutil.Read(t, path)
	if _, _, err := Write(db, dir, build(t, db, "2026-09-12T00:00:00Z", "2026-09-19T00:00:00Z"), at("2026-09-19T00:00:00Z")); err != nil {
		t.Fatal(err)
	}
	if got := testutil.Read(t, path); got != orig {
		t.Fatalf("an unchanged week was rewritten:\n%s", got)
	}
	revoke(t, db, "demo#1")
	// A bus CORRECTION line is a correction event too, with no refs (live 10-29: "a correction event has bad refs").
	if _, err := db.Exec("insert into event (ts, project, kind, actor, text, source_key) values" +
		" (\x272026-09-19T12:00:00Z\x27, \x27demo\x27, \x27correction\x27, \x27lead\x27, \x27CORRECTION my demo#1 line\x27, \x27bus:x:1:y\x27)"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Write(db, dir, build(t, db, "2026-09-12T00:00:00Z", "2026-09-20T00:00:00Z"), at("2026-09-20T00:00:00Z")); err != nil {
		t.Fatal(err)
	}
	got := testutil.Read(t, path)
	if !strings.HasPrefix(got, orig) {
		t.Fatalf("the old values are gone:\n%s", got)
	}
	added := strings.TrimPrefix(got, orig)
	for _, want := range []string{"revised 2026-09-20: code 1 -> 0", "revoked 0 -> 1", "demo#1 revoked", "D-1"} {
		if !strings.Contains(added, want) {
			t.Errorf("revised line lacks %q: %q", want, added)
		}
	}
	if strings.Count(added, "\n") != 1 {
		t.Errorf("one revised line, got %q", added)
	}
	if _, _, err := Write(db, dir, build(t, db, "2026-09-12T00:00:00Z", "2026-09-21T00:00:00Z"), at("2026-09-21T00:00:00Z")); err != nil {
		t.Fatal(err)
	}
	if again := testutil.Read(t, path); again != got {
		t.Fatalf("a second write of the same numbers added a line:\n%s", again)
	}
	if _, err := os.Stat(strings.TrimSuffix(path, ".md") + ".json"); err != nil {
		t.Fatal("no numbers file next to the scorecard:", err)
	}
}

// Crius 10-29 02:00: the weekly run re-writes the weeks still maturing, and two written under def v2 failed it after
// the code moved to v3 ("written with def v2, not v3: not compared"). A week written under another definition is
// counted again under this one: one line says so with what changed, and later writes compare under the new one.
func TestANewDefinitionCountsAnOldWeekAgain(t *testing.T) {
	db, home := fixture(t)
	dir := filepath.Join(home, "state", "scorecards")
	path, _, err := Write(db, dir, build(t, db, "2026-09-12T00:00:00Z", "2026-09-18T06:00:00Z"), at("2026-09-18T06:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	orig := testutil.Read(t, path)
	nums := strings.TrimSuffix(path, ".md") + ".json"
	var old written
	if err := json.Unmarshal([]byte(testutil.Read(t, nums)), &old); err != nil {
		t.Fatal(err)
	}
	old.Def, old.Numbers.Code = "v2", 5 // the old definition counted the week otherwise
	b, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	testutil.Write(t, nums, string(b))
	for i, want := range []string{"redefined", "unchanged"} {
		_, what, err := Write(db, dir, build(t, db, "2026-09-12T00:00:00Z", "2026-09-19T00:00:00Z"), at("2026-09-19T00:00:00Z"))
		if err != nil || what != want {
			t.Fatalf("write %d: %q %v, want %s", i+1, what, err, want)
		}
	}
	added := strings.TrimPrefix(testutil.Read(t, path), orig)
	if want := "redefined 2026-09-19: def v2 -> " + Def + ", counted again: code 5 -> 1, Max $0 (new in v3), enterprise $0 (new in v3)\n"; added != want {
		t.Errorf("added %q, want %q", added, want)
	}
	if err := json.Unmarshal([]byte(testutil.Read(t, nums)), &old); err != nil || old.Def != Def || old.Numbers.Code != 1 {
		t.Errorf("numbers file %+v %v: want def %s and code 1", old, err, Def)
	}
	// Review: an older build never counts a week written under a newer definition down.
	setDef := func(def string) {
		var w written
		if err := json.Unmarshal([]byte(testutil.Read(t, nums)), &w); err != nil {
			t.Fatal(err)
		}
		w.Def = def
		b, err := json.Marshal(w)
		if err != nil {
			t.Fatal(err)
		}
		testutil.Write(t, nums, string(b))
	}
	before := testutil.Read(t, path)
	for _, def := range []string{"v99", "v3.1", "V4", "v03", ""} {
		setDef(def)
		if _, _, err := Write(db, dir, build(t, db, "2026-09-12T00:00:00Z", "2026-09-20T00:00:00Z"), at("2026-09-20T00:00:00Z")); err == nil ||
			!strings.Contains(err.Error(), fmt.Sprintf("written with def %q, newer than this build's %s or unknown to it: not revised", def, Def)) ||
			testutil.Read(t, path) != before {
			t.Errorf("def %s: %v", def, err)
		}
	}
	// Review: a record event since the last write that changed numbers is named on the redefined line too.
	setDef("v2")
	revoke(t, db, "demo#1")
	if _, what, err := Write(db, dir, build(t, db, "2026-09-12T00:00:00Z", "2026-09-20T00:00:00Z"), at("2026-09-20T00:00:00Z")); err != nil || what != "redefined" {
		t.Fatalf("%q %v", what, err)
	}
	if added := strings.TrimPrefix(testutil.Read(t, path), before); !strings.HasPrefix(added, "redefined 2026-09-20: def v2 -> "+Def+
		", counted again: code 1 -> 0, revoked 0 -> 1") || !strings.HasSuffix(added, " (record events since: D-1 on demo#1; demo#1 revoked D-1)\n") {
		t.Errorf("added %q", added)
	}
}

// Review of 61fdb75 (P2): a defect filed before the acceptance is no escape of the accepted outcome.
func TestDefectBeforeAcceptanceIsNotCounted(t *testing.T) {
	db, _ := fixture(t)
	if _, err := store.RecordDefect(db, store.DefectInput{Item: "demo#2", Title: "typo", FoundBy: "owner",
		Severity: "S3", Class: "bad data"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("update defect set found_ts = '2026-09-12T00:00:00Z'"); err != nil {
		t.Fatal(err)
	}
	if n := build(t, db, "2026-09-12T00:00:00Z", "2026-09-20T00:00:00Z").Numbers; n.Defects != 0 || n.Escapes != 0 {
		t.Fatalf("a defect found before demo#2 was accepted: %+v", n)
	}
}

// Review of 61fdb75 (P2, P3): --week names the UTC day a week ends, also when it ends at 00:00, in any zone.
func TestWeekEndingFindsAWeekEndingAtMidnight(t *testing.T) {
	db, _ := fixture(t)
	for _, zone := range []*time.Location{time.UTC, time.FixedZone("x", -5*3600), time.FixedZone("y", 4*3600)} {
		time.Local = zone
		w, err := WeekEnding(db, "2026-09-17")
		if err != nil || !w.End.Equal(at("2026-09-17T00:00:00Z")) {
			t.Errorf("%s: %+v %v", zone, w, err)
		}
		if _, err := WeekEnding(db, "2026-09-16"); err == nil {
			t.Errorf("%s: no week ends on 09-16", zone)
		}
		if Name(w) != "2026-09-17" {
			t.Errorf("%s: name %s", zone, Name(w))
		}
	}
	time.Local = time.FixedZone("CEST", 2*3600)
}

// Review of 61fdb75 (P3): resets read within an hour of the one before are one reset, and its samples one week's.
func TestEpochChainIsOneWeekEnd(t *testing.T) {
	db, _ := fixture(t)
	if _, err := db.Exec("insert into quota (source_key, ts, account, epoch, seven_day_pct, sample_kind) values" +
		" ('q3', '2026-09-16T10:00:00Z', 'max', 1789606200, 80, 'session')," +
		" ('q4', '2026-09-16T11:00:00Z', 'max', 1789607200, 75, 'session')"); err != nil {
		t.Fatal(err)
	}
	if w := weekOf(t, db, "2026-09-17T00:30:00Z"); !w.Start.Equal(at("2026-09-17T00:00:00Z")) ||
		!w.End.Equal(at("2026-09-24T00:00:00Z")) {
		t.Fatalf("week after the chain: %s..%s", w.Start.UTC(), w.End.UTC())
	}
	if n := build(t, db, "2026-09-12T00:00:00Z", "2026-09-20T00:00:00Z").Numbers; n.MaxPct != 80 {
		t.Fatalf("week A takes the chain's samples: %+v", n)
	}
	if n := build(t, db, "2026-09-18T00:00:00Z", "2026-09-20T00:00:00Z").Numbers; n.MaxPct != -1 {
		t.Fatalf("week B has none of them: %+v", n)
	}
}

// Review of 61fdb75 (P3): an event recorded after a build is named by the next revision, whatever the clocks say.
func TestRevisedLineNamesEventsAfterTheBuild(t *testing.T) {
	db, home := fixture(t)
	dir := filepath.Join(home, "state", "scorecards")
	late := "2027-01-01T00:00:00Z" // a write stamped after the events below
	c := build(t, db, "2026-09-12T00:00:00Z", late)
	revoke(t, db, "demo#1")
	if _, err := store.RecordCorrect(db, store.CorrectInput{Target: "demo#2", Field: "kind", Value: "code",
		Reason: "it shipped code", By: "crius"}); err != nil {
		t.Fatal(err)
	}
	path, _, err := Write(db, dir, c, at(late))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Write(db, dir, build(t, db, "2026-09-12T00:00:00Z", late), at(late)); err != nil {
		t.Fatal(err)
	}
	got := testutil.Read(t, path)
	for _, want := range []string{"demo#1 revoked D-1", "D-1 on demo#1", "C-1 on demo#2"} {
		if !strings.Contains(got, want) {
			t.Errorf("revised line lacks %q:\n%s", want, got)
		}
	}
}

// accepted opens an accepted item under parent, accepted at ts.
func accepted(t *testing.T, db *sql.DB, parent, ts string) string {
	t.Helper()
	id, err := store.RecordItem(db, "demo", "sub "+ts, "code", []string{"CI pass"}, parent, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("update item set opened = ?, state = 'accepted' where id = ?", ts, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("insert into acceptance (item, revision, checks, review_ref, accepted_by, ts)"+
		" values (?, ?, '[]', 'event:1', 'crius', ?)", id, rev, ts); err != nil {
		t.Fatal(err)
	}
	return id
}

// Owner 10-19 (P3c): a defect or revoke on an accepted child task, at any depth, counts against its outcome's week
// (the week the outcome was accepted), not the child's own week and not the week it was found in.
func TestChildDefectsCountAgainstTheOutcomeWeek(t *testing.T) {
	db, _ := fixture(t)
	revoke(t, db, "demo#3") // child of demo#1, week A
	grand := accepted(t, db, "demo#3", "2026-09-12T07:30:00Z")
	if _, err := store.RecordDefect(db, store.DefectInput{Item: grand, Title: "slow", FoundBy: "us", Severity: "S3",
		Class: "silent failure"}); err != nil {
		t.Fatal(err)
	}
	early := accepted(t, db, "demo#4", "2026-09-12T08:00:00Z") // accepted in week A, its outcome in week B
	revoke(t, db, early)
	a := build(t, db, "2026-09-12T00:00:00Z", "2026-10-20T00:00:00Z")
	if n := a.Numbers; n.Code != 1 || n.Revoked != 0 || n.ChildRevoked != 1 || n.Defects != 2 || n.Escapes != 1 {
		t.Fatalf("week A: %+v\n%s", n, a.Text)
	}
	for _, want := range []string{"child tasks 1: demo#3 on abc1234def56 (D-1", "found by us 1, owner 1"} {
		if !strings.Contains(a.Text, want) {
			t.Errorf("week A text lacks %q:\n%s", want, a.Text)
		}
	}
	b := build(t, db, "2026-09-18T00:00:00Z", "2026-10-20T00:00:00Z")
	if n := b.Numbers; n.Code != 1 || n.ChildRevoked != 1 || n.Defects != 1 || n.Escapes != 1 {
		t.Fatalf("week B: %+v\n%s", n, b.Text)
	}
	now := weekOf(t, db, time.Now().UTC().Format(time.RFC3339))
	if c, err := Build(db, now, demoProjects, time.Now()); err != nil || c.Numbers.ChildRevoked != 0 || c.Numbers.Defects != 0 {
		t.Fatalf("the week found: %+v %v", c, err)
	}
}

// A child revoke after the week was written appends a revised line that names it.
func TestChildRevokeRevisesTheOutcomeWeek(t *testing.T) {
	db, home := fixture(t)
	dir := filepath.Join(home, "state", "scorecards")
	path, _, err := Write(db, dir, build(t, db, "2026-09-12T00:00:00Z", "2026-09-18T06:00:00Z"), at("2026-09-18T06:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	orig := testutil.Read(t, path)
	revoke(t, db, "demo#3")
	if _, _, err := Write(db, dir, build(t, db, "2026-09-12T00:00:00Z", "2026-09-20T00:00:00Z"), at("2026-09-20T00:00:00Z")); err != nil {
		t.Fatal(err)
	}
	added := strings.TrimPrefix(testutil.Read(t, path), orig)
	for _, want := range []string{"defects 0 -> 1", "escapes 0 -> 1", "child revoked 0 -> 1", "demo#3 revoked D-1"} {
		if !strings.Contains(added, want) {
			t.Errorf("revised line lacks %q: %q", want, added)
		}
	}
	if !strings.Contains(orig, "def "+Def) {
		t.Errorf("the card does not name def %s: %s", Def, orig)
	}
}

// Review of P3c (P2): an item accepted twice before a defect counts that defect once, on an outcome and on a child.
func TestDefectCountsOnceOverTwoAcceptances(t *testing.T) {
	db, _ := fixture(t)
	for _, id := range []string{"demo#1", "demo#3"} {
		if _, err := db.Exec("insert into acceptance (item, revision, checks, review_ref, accepted_by, ts)"+
			" values (?, ?, '[]', 'event:1', 'crius', '2026-09-12T09:00:00Z')", id, rev); err != nil {
			t.Fatal(err)
		}
		if _, err := store.RecordDefect(db, store.DefectInput{Item: id, Title: "x", FoundBy: "owner", Severity: "S3",
			Class: "bad data"}); err != nil {
			t.Fatal(err)
		}
	}
	if n := build(t, db, "2026-09-12T00:00:00Z", "2026-10-20T00:00:00Z").Numbers; n.Defects != 2 || n.Escapes != 2 {
		t.Fatalf("two defects, each on an item accepted twice: %+v", n)
	}
}

// Review of P3c (P2): child tasks are not SQL parameters, so a tree past SQLite's 32766 variables still builds.
func TestManyChildTasksBuild(t *testing.T) {
	db, _ := fixture(t)
	if _, err := db.Exec("with recursive k(n) as (select 1000 union all select n + 1 from k where n < 34000)" +
		" insert into item (id, project, n, parent, kind, title, criteria, opened, state)" +
		" select 'demo#' || n, 'demo', n, 'demo#1', 'code', 't', '[]', '2026-09-11T09:00:00Z', 'open' from k"); err != nil {
		t.Fatal(err)
	}
	if c, err := Build(db, weekOf(t, db, "2026-09-12T00:00:00Z"), demoProjects, at("2026-10-20T00:00:00Z")); err != nil || len(c.Children) != 33002 {
		t.Fatalf("children %v, err %v", c != nil && len(c.Children) > 0, err)
	}
}

// demoProjects: quotes and axi may run on the enterprise account; no folder names a project.
var demoProjects = quota.Projects{EnterpriseOK: map[string]bool{"quotes": true, "axi": true},
	ForCwd: func(string, *store.Worktree) string { return "" }}

// P3b (design §7, M2e and M2b): the week's list-price $ from the store. Max $ with the part no sample covers, enterprise
// $ by project with its violations, and each project's share of the Max $; requests of the next week do not count.
func TestCardShowsTheWeeksMoneyByAccountAndProject(t *testing.T) {
	db, _ := fixture(t)
	for _, s := range []struct{ id, account, project string }{
		{"s1", "max", "quotes"}, {"s2", "max", "crius"}, {"s3", "enterprise", "quotes"}, {"s4", "enterprise", "axi"},
		{"s5", "enterprise", "crius"},
	} {
		if _, err := db.Exec("insert into assignment (session, account, project, from_ts, source) values"+
			" (?, ?, ?, '2026-09-10T01:00:00Z', 'tag')", s.id, s.account, s.project); err != nil {
			t.Fatal(err)
		}
	}
	for i, r := range []struct {
		session, ts string
		usd         float64
	}{
		{"s1", "2026-09-15T10:10:00Z", 30}, // within 45 min of the 10:00 sample
		{"s1", "2026-09-12T10:00:00Z", 10}, // no sample near it
		{"s2", "2026-09-15T09:50:00Z", 10},
		{"s3", "2026-09-14T10:00:00Z", 260},
		{"s4", "2026-09-13T10:00:00Z", 152},
		{"s5", "2026-09-13T11:00:00Z", 5}, // crius may not run on enterprise
		{"s6", "2026-09-14T11:00:00Z", 7}, // no tag, no statusline: account unknown
		{"s1", "2026-09-18T10:00:00Z", 1000},
	} {
		if _, err := db.Exec("insert into request (request_id, session, ts_first, ts_last, cost_list_usd) values (?, ?, ?, ?, ?)",
			fmt.Sprint("r", i), r.session, r.ts, r.ts, r.usd); err != nil {
			t.Fatal(err)
		}
	}
	c := build(t, db, "2026-09-12T00:00:00Z", "2026-10-20T00:00:00Z")
	for _, want := range []string{
		"Quota      Max (both personas): 71% observed (1 samples), list $50, uncovered 20% · enterprise: $417 (quotes 260," +
			" axi 152, crius 5), 1 violation · account unknown: $7 (not measured)\n",
		"Shares     Max list-price: quotes 80% · crius 20%\n",
	} {
		if !strings.Contains(c.Text, want) {
			t.Errorf("card lacks %q:\n%s", want, c.Text)
		}
	}
	if n := c.Numbers; n.MaxUSD != 50 || n.EntUSD != 417 {
		t.Errorf("numbers %+v", n)
	}
	later := c.Numbers
	later.MaxUSD, later.EntUSD = 60, 400
	if d := changes(c.Numbers, later, 3); d != "Max $50 -> $60, enterprise $417 -> $400" {
		t.Errorf("changes: %q", d)
	}
	// Review of 033573f: a v2 week has no $ (0 in its numbers file); counted again, its $ is new, not "$0 -> $60".
	if d := changes(Numbers{MaxPct: c.Numbers.MaxPct}, later, 2); !strings.HasSuffix(d, "Max $60 (new in v3), enterprise $400 (new in v3)") {
		t.Errorf("changes from v2: %q", d)
	}
	later.ChildRevoked = 2 // counted since v2
	if d := changes(Numbers{MaxPct: c.Numbers.MaxPct}, later, 1); !strings.Contains(d, "child revoked 2 (new in v2)") {
		t.Errorf("changes from v1: %q", d)
	}
}

func TestSmallAmountsDoNotReadAsNothing(t *testing.T) {
	sp := &quota.Spend{MaxUSD: 100.2, Shares: []quota.Share{{Project: "a", USD: 99.9}, {Project: "b", USD: 0.3}},
		EntUSD: 0.2, Ent: []quota.Share{{Project: "falcon", USD: 0.2}}, UnknownUSD: 0.3}
	if got, want := spendText(sp), ", list $100, uncovered 0% · enterprise: $<1 (falcon <1), 0 violations"+
		" · account unknown: $<1 (not measured)"; got != want {
		t.Errorf("spend:\n got %q\nwant %q", got, want)
	}
	if got, want := sharesText(sp), "Shares     Max list-price: a 100% · b <1%"; got != want {
		t.Errorf("shares:\n got %q\nwant %q", got, want)
	}
}

// A session's violation is counted in the week of its request, not in every week the session has a request in
// (review of P3b): s7 ran crius on enterprise the week before and is on Max in this one.
func TestViolationCountsInTheWeekOfItsRequest(t *testing.T) {
	db, _ := fixture(t)
	for _, a := range []struct{ account, from, to string }{
		{"enterprise", "2026-09-05T00:00:00Z", "2026-09-11T00:00:00Z"}, {"max", "2026-09-11T00:00:00Z", ""},
	} {
		if _, err := db.Exec("insert into assignment (session, account, project, from_ts, to_ts, source) values"+
			" ('s7', ?, 'crius', ?, nullif(?, ''), 'tag')", a.account, a.from, a.to); err != nil {
			t.Fatal(err)
		}
	}
	for i, ts := range []string{"2026-09-08T10:00:00Z", "2026-09-13T10:00:00Z"} {
		if _, err := db.Exec("insert into request (request_id, session, ts_first, ts_last, cost_list_usd) values"+
			" (?, 's7', ?, ?, 2)", fmt.Sprint("r", i), ts, ts); err != nil {
			t.Fatal(err)
		}
	}
	if c := build(t, db, "2026-09-12T00:00:00Z", "2026-10-20T00:00:00Z"); !strings.Contains(c.Text, "enterprise: $0, 0 violations") {
		t.Errorf("this week has no enterprise request:\n%s", c.Text)
	}
	if c := build(t, db, "2026-09-08T00:00:00Z", "2026-10-20T00:00:00Z"); !strings.Contains(c.Text, "enterprise: $2 (crius 2), 1 violation") {
		t.Errorf("the week of the enterprise request lacks its violation:\n%s", c.Text)
	}
}

// The card counts what guard() counts for the week's requests (review of P3b): an unpriced request still breaks the
// rule, and an enterprise tag on a project not allowed there is a violation whatever the statusline measured.
func TestViolationsMatchTheGuardsRules(t *testing.T) {
	for _, c := range []struct {
		name, seen, cost, want string
	}{
		{"unpriced", "", "", "enterprise: $0, 1 violation · 1 requests unpriced"},
		{"tag under a Max statusline", "2026-09-13T10:00:00Z", "3", "enterprise: $0, 2 violations"}, // mismatch and s1
	} {
		t.Run(c.name, func(t *testing.T) {
			db, _ := fixture(t)
			if _, err := db.Exec("insert into assignment (session, account, project, from_ts, source) values" +
				" ('s8', 'enterprise', 'crius', '2026-09-10T01:00:00Z', 'tag')"); err != nil {
				t.Fatal(err)
			}
			if c.seen != "" {
				if _, err := db.Exec("insert into account_seen (source_key, session, ts, rl) values ('a1', 's8', ?, 1)",
					c.seen); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Exec("insert into request (request_id, session, ts_first, ts_last, cost_list_usd) values"+
				" ('r1', 's8', '2026-09-13T10:00:00Z', '2026-09-13T10:00:00Z', nullif(?, ''))", c.cost); err != nil {
				t.Fatal(err)
			}
			if card := build(t, db, "2026-09-12T00:00:00Z", "2026-10-20T00:00:00Z"); !strings.Contains(card.Text, c.want) {
				t.Errorf("card lacks %q:\n%s", c.want, card.Text)
			}
		})
	}
}

// The compared numbers come from the store alone: a projects.toml edit (here crius allowed on enterprise) must not
// revise a written week (review of P3b).
func TestProjectsEditDoesNotReviseAWrittenWeek(t *testing.T) {
	db, home := fixture(t)
	if _, err := db.Exec("insert into assignment (session, account, project, from_ts, source) values" +
		" ('s5', 'enterprise', 'crius', '2026-09-10T01:00:00Z', 'tag')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("insert into request (request_id, session, ts_first, ts_last, cost_list_usd) values" +
		" ('r1', 's5', '2026-09-13T11:00:00Z', '2026-09-13T11:00:00Z', 5)"); err != nil {
		t.Fatal(err)
	}
	dir, now := filepath.Join(home, "state", "scorecards"), at("2026-09-19T00:00:00Z")
	w := weekOf(t, db, "2026-09-12T00:00:00Z")
	if _, _, err := Write(db, dir, build(t, db, "2026-09-12T00:00:00Z", "2026-09-19T00:00:00Z"), now); err != nil {
		t.Fatal(err)
	}
	ok := quota.Projects{EnterpriseOK: map[string]bool{"quotes": true, "axi": true, "crius": true}, ForCwd: demoProjects.ForCwd}
	c, err := Build(db, w, ok, now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.Text, "(crius 5), 0 violations") { // the edit took effect
		t.Errorf("crius allowed, still a violation:\n%s", c.Text)
	}
	if _, what, err := Write(db, dir, c, now); err != nil || what != "unchanged" {
		t.Errorf("after the edit: %s, %v", what, err)
	}
}

func TestWholeRoundsHalfUp(t *testing.T) {
	for v, want := range map[float64]string{0.5: "1", 2.5: "3", 1232.5: "1233", 0.49: "<1", 0: "0"} {
		if got := whole(v); got != want {
			t.Errorf("whole(%v) = %q, want %q", v, got, want)
		}
	}
}
