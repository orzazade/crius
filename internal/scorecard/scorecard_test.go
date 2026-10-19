package scorecard

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius"
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
	c, err := Build(db, weekOf(t, db, week), at(now))
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
		"status complete", "def v1"} {
		if !strings.Contains(a.Text, want) {
			t.Errorf("week A text lacks %q:\n%s", want, a.Text)
		}
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
	if c, err := Build(db, now, time.Now()); err != nil || c.Numbers.Revoked != 0 || c.Numbers.Defects != 0 {
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
