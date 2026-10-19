package drivers

import (
	"database/sql"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius"
	"github.com/orzazade/crius/internal/store"
	"github.com/orzazade/crius/internal/testutil"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// fixture: window Fri 2026-10-09 .. Mon 10-12 06:00 UTC. Personal p1 has 3 main calls in it (Fri 100K $1, Sat 300K
// $3, Mon 50K $0.5), one before it, one at its end and a subagent call; work w1 has one main call (Fri 400K $2).
// Compactions: p1 Fri and Sat, w1 Mon 07:00 (after the window).
func fixture(t *testing.T) *sql.DB {
	t.Helper()
	_, home := testutil.Temp(t)
	if err := store.InitLayout(home, crius.ProjectsExample); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, q := range []string{
		"insert into session (id, persona) values ('p1', 'personal'), ('w1', 'work')",
		"insert into request (request_id, session, ts_first, query_source, tok_in, tok_cache_read, tok_cache_write, cost_list_usd) values" +
			" ('r1', 'p1', '2026-10-09T10:00:00.000Z', 'main', 1000, 90000, 9000, 1)," +
			" ('r2', 'p1', '2026-10-10T10:00:00Z', 'main', 0, 290000, 10000, 3)," +
			" ('r3', 'p1', '2026-10-12T05:59:59.999Z', 'main', 0, 50000, 0, 0.5)," +
			" ('r4', 'p1', '2026-10-08T23:59:59.999Z', 'main', 0, 1, 0, 100)," +
			" ('r5', 'p1', '2026-10-12T06:00:00.000Z', 'main', 0, 1, 0, 100)," +
			" ('r6', 'p1', '2026-10-09T11:00:00Z', 'subagent:Explore', 0, 1, 0, 100)," +
			" ('r7', 'w1', '2026-10-09T12:00:00Z', 'main', 0, 400000, 0, 2)",
		"insert into event (ts, kind, session, source_key) values ('2026-10-09T10:30:00Z', 'compaction', 'p1', 'c1')," +
			" ('2026-10-10T10:30:00Z', 'compaction', 'p1', 'c2'), ('2026-10-12T07:00:00Z', 'compaction', 'w1', 'c3')",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestBuild(t *testing.T) {
	db := fixture(t)
	rows, err := Build(db, at("2026-10-09T00:00:00Z"), at("2026-10-12T06:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Row{
		{Persona: "personal", Days: "all", MainCalls: 3, MainUSD: 4.5, USDPerMainCall: 1.5, Compactions: 2,
			CompactionsPer100: 200.0 / 3, ShareAbove267K: 3 / 4.5, MeanCtx: 150000},
		{Persona: "personal", Days: "weekdays", MainCalls: 2, MainUSD: 1.5, USDPerMainCall: 0.75, Compactions: 1,
			CompactionsPer100: 50, ShareAbove267K: 0, MeanCtx: 75000},
		{Persona: "work", Days: "all", MainCalls: 1, MainUSD: 2, USDPerMainCall: 2, Compactions: 0,
			CompactionsPer100: 0, ShareAbove267K: 1, MeanCtx: 400000},
		{Persona: "work", Days: "weekdays", MainCalls: 1, MainUSD: 2, USDPerMainCall: 2, Compactions: 0,
			CompactionsPer100: 0, ShareAbove267K: 1, MeanCtx: 400000},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows %+v, want %+v", rows, want)
	}
	for i := range want {
		if lines, ok := Compare([]Row{want[i]}, []Row{rows[i]}, 1e-9); !ok {
			t.Errorf("row %d: %v", i, lines)
		}
	}
}

// A main call whose session has no persona is counted under "unknown", never dropped.
func TestBuildUnknownPersona(t *testing.T) {
	db := fixture(t)
	if _, err := db.Exec("insert into request (request_id, session, ts_first, query_source, cost_list_usd) values" +
		" ('r8', 'gone', '2026-10-09T13:00:00Z', 'main', 1)"); err != nil {
		t.Fatal(err)
	}
	rows, err := Build(db, at("2026-10-09T00:00:00Z"), at("2026-10-12T06:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 6 || rows[2].Persona != "unknown" || rows[2].MainCalls != 1 {
		t.Fatalf("rows %+v", rows)
	}
}

// A main call with no list price makes the build fail: $ per call would silently drop it.
func TestBuildUnpriced(t *testing.T) {
	db := fixture(t)
	if _, err := db.Exec("update request set cost_list_usd = null where request_id = 'r2'"); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(db, at("2026-10-09T00:00:00Z"), at("2026-10-12T06:00:00Z")); err == nil ||
		!strings.Contains(err.Error(), "1 main call") {
		t.Fatalf("err %v", err)
	}
}

func TestCompare(t *testing.T) {
	ref := []Row{
		{Persona: "personal", Days: "weekdays", MainCalls: 23856, MainUSD: 1819.82, USDPerMainCall: 0.0763,
			Compactions: 68, CompactionsPer100: 0.285, ShareAbove267K: 0.4163, MeanCtx: 217698},
		{Persona: "work", Days: "all", MainCalls: 3536, MainUSD: 336.55, USDPerMainCall: 0.0952,
			Compactions: 7, CompactionsPer100: 0.198, ShareAbove267K: 0.7214, MeanCtx: 360573},
	}
	cp := func() []Row { return append([]Row(nil), ref...) }
	if lines, ok := Compare(ref, cp(), 0.02); !ok {
		t.Fatalf("same rows failed: %v", lines)
	}
	got := cp()
	got[0].USDPerMainCall *= 1.03
	lines, ok := Compare(ref, got, 0.02)
	if ok || !strings.Contains(strings.Join(lines, "\n"), "personal weekdays usd_per_main_call") {
		t.Fatalf("3%% move passed: %v", lines)
	}
	got = cp()
	got[1].MainUSD *= 0.985
	if lines, ok := Compare(ref, got, 0.02); !ok {
		t.Fatalf("1.5%% move failed: %v", lines)
	}
	if lines, ok := Compare(ref, got[:1], 0.02); ok || !strings.Contains(strings.Join(lines, "\n"), "work all: missing") {
		t.Fatalf("missing row passed: %v", lines)
	}
	got = cp()
	got[1].Compactions = 6
	if _, ok := Compare(ref, got, 0.02); ok {
		t.Fatal("7 -> 6 compactions passed")
	}
}

// Stored stamps are compared as times, not text: one without milliseconds or with an offset lands where its time
// says (weekday by its UTC date), and one that is missing or unreadable fails the build.
func TestBuildStampForms(t *testing.T) {
	db := fixture(t)
	if _, err := db.Exec("insert into request (request_id, session, ts_first, query_source, cost_list_usd) values" +
		" ('r8', 'w1', '2026-10-12T05:59:59Z', 'main', 1), ('r9', 'w1', '2026-10-12T00:30:00+04:00', 'main', 1)"); err != nil {
		t.Fatal(err)
	}
	rows, err := Build(db, at("2026-10-09T00:00:00Z"), at("2026-10-12T05:59:59.500Z"))
	if err != nil {
		t.Fatal(err)
	}
	if w := rows[2]; w.Persona != "work" || w.MainCalls != 3 || rows[3].MainCalls != 2 {
		t.Fatalf("rows %+v", rows)
	}
	for _, bad := range []string{"null", "'yesterday'"} {
		if _, err := db.Exec("update request set ts_first = " + bad + " where request_id = 'r9'"); err != nil {
			t.Fatal(err)
		}
		if _, err := Build(db, at("2026-10-09T00:00:00Z"), at("2026-10-12T06:00:00Z")); err == nil ||
			!strings.Contains(err.Error(), "1 main call") {
			t.Fatalf("%s: err %v", bad, err)
		}
	}
}

// A row only in the store fails the gate (a whole persona would otherwise go unchecked), and so does a NaN.
func TestCompareExtraAndNaN(t *testing.T) {
	ref := []Row{{Persona: "personal", Days: "all", MainCalls: 1, MainUSD: 1}}
	if lines, ok := Compare(ref, append(ref, Row{Persona: "work", Days: "all"}), 0.02); ok {
		t.Fatalf("extra row passed: %v", lines)
	}
	got := []Row{ref[0]}
	got[0].MainUSD = math.NaN()
	if lines, ok := Compare(ref, got, 0.02); ok {
		t.Fatalf("NaN passed: %v", lines)
	}
}
