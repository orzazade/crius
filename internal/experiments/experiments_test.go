package experiments

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/store"
	"github.com/orzazade/crius/internal/testutil"
)

func at(s string) time.Time {
	v, err := time.ParseInLocation("2006-01-02 15:04", s, time.Local)
	if err != nil {
		panic(err)
	}
	return v
}

var seq int

// bus stores a bus line as the ingest does: the sender's project and role, the type lower-case, a bus: source key.
func bus(t *testing.T, db *sql.DB, ts, sender, kind, text string) {
	t.Helper()
	seq++
	p, role, _ := strings.Cut(sender, ":")
	if _, err := db.Exec("insert into event (ts, project, kind, actor, text, source_key) values (?,?,?,?,?,?)",
		at(ts).UTC().Format("2006-01-02T15:04:05Z"), p, kind, role, text, fmt.Sprint("bus:f:", seq)); err != nil {
		t.Fatal(err)
	}
}

func call(t *testing.T, db *sql.DB, session, ts string, ctx int, cost float64) {
	t.Helper()
	seq++
	if _, err := db.Exec("insert into request (request_id, session, ts_first, tok_in, tok_cache_read, cost_list_usd) values (?,?,?,?,?,?)",
		fmt.Sprint("r", seq), session, at(ts).UTC().Format("2006-01-02T15:04:05.000Z"), 1000, ctx-1000, cost); err != nil {
		t.Fatal(err)
	}
}

// Brief fixes-1025 item 7 (owner): a daily scorecard of experiments E1-E8 (docs/LEARNINGS.md) from the record and the
// bus. A metric with no data source says so on its row; none is left blank.
func TestTheScorecardCountsADayFromTheRecordAndTheBus(t *testing.T) {
	_, home := testutil.Temp(t)
	os.MkdirAll(filepath.Join(home, "state"), 0o700)
	db, err := store.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	transcript := filepath.Join(home, "lead.jsonl")
	if _, err := db.Exec("insert into session (id, project, role, transcript_path) values ('ql', 'quotes', 'lead', ?), "+
		"('qa', 'quotes', 'qa1', ''), ('c', 'crius', 'lead', '')", transcript); err != nil {
		t.Fatal(err)
	}
	// Three wakes: a re-arm only, a real edit, a text-only answer; then the owner. The day before is not counted.
	wake := func(ts string) string {
		return fmt.Sprintf(`{"type":"user","timestamp":%q,"origin":{"kind":"task-notification"},"message":{"content":"<task-notification>x"}}`,
			at(ts).UTC().Format(time.RFC3339)) + "\n"
	}
	tool := func(name, input string) string {
		return `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"` + name + `","input":` + input + `}]}}` + "\n"
	}
	testutil.Write(t, transcript, wake("2026-10-23 23:00")+tool("Edit", "{}")+
		wake("2026-10-24 10:00")+tool("Bash", `{"command":"tail -F ~/.crius/bus/quotes.log | awk 'x'","run_in_background":true}`)+
		`{"type":"user","message":{"content":[{"type":"tool_result","content":"ok"}]}}`+"\n"+tool("Monitor", "{}")+
		wake("2026-10-24 10:10")+tool("Edit", "{}")+
		wake("2026-10-24 10:20")+`{"type":"assistant","message":{"content":[{"type":"text","text":"nothing for me"}]}}`+"\n"+
		`{"type":"user","timestamp":"2026-10-24T07:00:00Z","origin":{"kind":"human"},"message":{"content":"Crius: go"}}`+"\n"+
		tool("Edit", "{}"))
	call(t, db, "ql", "2026-10-24 09:00", 200000, 10)
	call(t, db, "ql", "2026-10-24 23:59", 200000, 10)
	call(t, db, "qa", "2026-10-24 10:00", 40000, 1)
	call(t, db, "qa", "2026-10-24 10:05", 60000, 1)
	call(t, db, "ql", "2026-10-25 00:00", 300000, 100) // the next day
	call(t, db, "ql", "2026-10-23 23:59", 300000, 100) // the day before
	call(t, db, "c", "2026-10-24 12:00", 300000, 5)    // another project
	bus(t, db, "2026-10-24 08:00", "quotes:lead", "landed", "train-112 d9df6e10 on main")
	bus(t, db, "2026-10-24 16:00", "quotes:lead", "landed", "train-113 47f81cfa on main")
	bus(t, db, "2026-10-23 23:50", "quotes:p2", "ready", "READY G-1 p2/g1 abcd1234 (replaces 99aa88bb)")
	bus(t, db, "2026-10-24 00:10", "quotes:qa1", "pass", "QA1 PASS G-1 abcd1234 on base 0ddba11a: npm 1300/0")
	bus(t, db, "2026-10-24 11:00", "quotes:p5", "ready", "READY G-2 beef5678")
	// A repost (10-25 08:41 "repost via ready-post guard") does not restart the clock; a sha is cited at 8 or 9 hex.
	bus(t, db, "2026-10-24 11:30", "quotes:p5", "ready", "READY G-2 beef56789 (repost via guard)")
	bus(t, db, "2026-10-24 11:40", "quotes:qa2", "pass", "QA2 PASS G-2 beef567 on main 0abc1234 (no: 7 hex); beef5678a")
	bus(t, db, "2026-10-24 12:00", "quotes:qa1", "fail", "FAIL G-3 cafe0001: tab order")
	bus(t, db, "2026-10-24 15:00", "quotes:qa3", "fail", "FAIL batch: G-1 abcd12345 breaks the tab bar") // after QA1's PASS
	bus(t, db, "2026-10-24 15:10", "quotes:qa3", "fail", "FAIL G-9 f00dcafe on base 0ddba11a")           // the base, no item
	bus(t, db, "2026-10-24 15:20", "quotes:qa2", "fail", "FAIL G-8 f00dbeef on base 0ddba11a")
	bus(t, db, "2026-10-24 13:20", "quotes:lead", "info", "Lead 13:30 queue: 7 open")      // a typed time 10 min ahead
	bus(t, db, "2026-10-24 13:20", "quotes:lead", "info", "Lead to P5 13:10 asked for it") // a citation
	bus(t, db, "2026-10-24 14:00", "crius:lead", "need-owner", "Summary B: which option?")
	// The record holds bus lines with no actor (seen 10-25 in record.db).
	for _, kind := range []string{"pass", "info"} {
		if _, err := db.Exec("insert into event (ts, project, kind, text, source_key) values ('2026-10-24T08:00:00Z', 'quotes', ?, 'x', ?)",
			kind, "bus:null:"+kind); err != nil {
			t.Fatal(err)
		}
	}
	testutil.Write(t, filepath.Join(home, "logs", "load.log"), "2026-10-23T23:55:00+04:00 30.0 30.0 30.0\n"+
		"2026-10-24T09:00:00+04:00 6.0 5.0 4.0\n2026-10-24T12:00:00+04:00 10.0 9.0 8.0\n2026-10-24T15:00:00+04:00 14.0 13.0 12.0\n")
	testutil.Write(t, filepath.Join(home, "state", "digest", "2026-10-24-0900.md"), "x\n- quotes:lead 40% · answered on the bus, "+
		"left out of Needs the owner: (1) by crius:lead ANSWER 2026-10-24 00:02, (3) by crius:lead CORRECTED 2026-10-23 18:30\n")
	testutil.Write(t, filepath.Join(home, "logs", "bus-post-refused.log"),
		"2026-10-24T17:43:00+04:00 rc=6 crius:lead ASK crius.log — bus-post: refused: typed time 17:50 at the start is 7 min off\n"+
			"2026-10-24T18:00:00+04:00 rc=3 quotes:p5 INFO quotes.log — bus-post: refused: deadbee0 is not a commit\n"+
			"2026-10-24T19:00:00+04:00 rc=6 quotes:p2 READY quotes.log — bus-post: refused: typed time 19:05 at the start\n"+
			"2026-10-25T07:00:00+04:00 rc=6 quotes:p2 READY quotes.log — bus-post: refused: typed time 07:09 at the start\n")

	d := Deps{Home: home,
		Worktrees: func() (int, float64, error) { return 29, 32.4, nil },
		Leftovers: func() (int, time.Time, error) { return 436, at("2026-10-24 18:15"), nil }}
	rows, err := Build(db, d, at("2026-10-24 00:00"))
	if err != nil {
		t.Fatal(err)
	}
	md := Markdown(at("2026-10-24 00:00"), rows)
	for _, want := range []string{
		"| E1 |", "$11.00 per LANDED (2 LANDED)", "$22.00 a day", "load 10.0 average, 14.0 top (3 samples)",
		"| E2 |", "READY -> PASS median 30 min (2 pairs)",
		"| E3 |", "50K context per QA1 call (2 calls); 2 QA1 PASS/FAIL", "1 FAIL after a QA1 PASS",
		"| E4 |", "50% of calls over 150K (2 of 4); 125K average context",
		"| E5 |", "2 of 3 wakes with no action (lead 2)",
		"| E6 |", "typed time: 3 (2 refused by bus-post, 1 on the bus); other guard refusals: 1",
		"| E7 |", "1 NEED-OWNER", "2 answered questions kept out of the owner list",
		"| E8 |", "29 worktrees, 32 GB now", "436 leftover test quotes on 7090 (rig count 2026-10-24 18:15)",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("lacks %q:\n%s", want, md)
		}
	}
	// The two with no bus line yet say which line is missing (crius:lead 18:38: "say which line").
	for _, want := range []string{"owner bug -> on 7090: no source (needs a line", "gate minutes: no source (needs"} {
		if !strings.Contains(md, want) {
			t.Errorf("lacks %q:\n%s", want, md)
		}
	}
	if err := Write(home, at("2026-10-24 00:00"), md); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(home, "state", "experiments", "2026-10-24.md")); err != nil || string(b) != md {
		t.Fatal(err, string(b))
	}
}

// A day with no calls and no lines is counted as nothing, never as a failure or a division by zero.
func TestAnEmptyDayShowsNoneNotNaN(t *testing.T) {
	_, home := testutil.Temp(t)
	os.MkdirAll(filepath.Join(home, "state"), 0o700)
	db, err := store.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := Deps{Home: home, Worktrees: func() (int, float64, error) { return 0, 0, nil },
		Leftovers: func() (int, time.Time, error) { return 0, time.Time{}, nil }}
	rows, err := Build(db, d, at("2026-10-24 00:00"))
	if err != nil {
		t.Fatal(err)
	}
	md := Markdown(at("2026-10-24 00:00"), rows)
	if strings.Contains(md, "NaN") || strings.Contains(md, "Inf") || !strings.Contains(md, "$0.00 a day") ||
		!strings.Contains(md, "no load samples") || !strings.Contains(md, "no rig count") {
		t.Fatal(md)
	}
}

// Failed to look is not found nothing: a worktree list or rig count that fails is an error, not a zero.
func TestASourceThatFailsIsAnError(t *testing.T) {
	_, home := testutil.Temp(t)
	os.MkdirAll(filepath.Join(home, "state"), 0o700)
	db, err := store.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	d := Deps{Home: home, Worktrees: func() (int, float64, error) { return 0, 0, fmt.Errorf("git: no repo") },
		Leftovers: func() (int, time.Time, error) { return 0, time.Time{}, nil }}
	if _, err := Build(db, d, at("2026-10-24 00:00")); err == nil || !strings.Contains(err.Error(), "git: no repo") {
		t.Fatal(err)
	}
}
