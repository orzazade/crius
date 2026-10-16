package ingest

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/quota"
	"github.com/orzazade/crius/internal/store"
	"github.com/orzazade/crius/internal/testutil"
)

// Step 3 tests (design §9): pace push, used-up push, a gap is uncovered not 0, a scifi session on enterprise is S1.

var qNow = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

const axiProject = `
[projects.axi]
persona = "work"
accounts = ["max", "enterprise"]
cwd = ["/work/axi"]
repos = []
team_logs = []
artifact_dirs = []
`

func quotaCase(t *testing.T) *ic {
	clock = func() time.Time { return qNow }
	t.Cleanup(func() { clock = time.Now })
	return newIC(t, cfg{extra: axiProject})
}

func iso(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// session writes a transcript: one prompt in cwd, then one request per time given.
func (x *ic) session(persona, sid, cwd, tag string, at ...time.Time) {
	x.t.Helper()
	root := x.personal
	if persona == "work" {
		root = x.work
	}
	text := ""
	if tag != "" {
		text = tag + "\n"
	}
	recs := []map[string]any{prompt("p-"+sid, iso(at[0].Add(-time.Minute)), text, "", cwd)}
	for i, t := range at {
		recs = append(recs, assistant(A{Req: sid + "-r" + string(rune('a'+i)), TS: iso(t), In: 1000, Out: 1000}))
	}
	testutil.Write(x.t, filepath.Join(root, "-p", sid+".jsonl"), lines(recs...))
}

// spool appends statusline samples as crius-statusline writes them.
func (x *ic) spool(ls ...quota.Line) {
	x.t.Helper()
	var b strings.Builder
	for _, l := range ls {
		j, err := json.Marshal(l)
		if err != nil {
			x.t.Fatal(err)
		}
		b.Write(append(j, '\n'))
	}
	appendFile(x.t, filepath.Join(x.home, "spool", "quota.jsonl"), b.String())
}

func f(v float64) *float64 { return &v }
func i64(v int64) *int64   { return &v }

func sampleAt(sid string, at time.Time, seven float64, reset time.Time) quota.Line {
	return quota.Line{TS: iso(at), Kind: "change", Session: sid, Persona: "personal", RL: true, FivePct: f(10),
		FiveReset: i64(at.Add(3 * time.Hour).Unix()), SevenPct: f(seven), SevenReset: i64(reset.Unix())}
}

func (x *ic) alerts() []string {
	x.t.Helper()
	b, err := os.ReadFile(filepath.Join(x.home, "bus", "crius.log"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		x.t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func grepAlerts(as []string, s string) []string {
	var out []string
	for _, a := range as {
		if strings.Contains(a, s) {
			out = append(out, a)
		}
	}
	return out
}

func (x *ic) report() *quota.Report {
	x.t.Helper()
	db, err := store.Open(x.home)
	if err != nil {
		x.t.Fatal(err)
	}
	defer db.Close()
	rep, err := quota.Build(db, map[string]bool{"axi": true}, qNow)
	if err != nil {
		x.t.Fatal(err)
	}
	return rep
}

// Day 3 of the epoch (started 2.5 days ago) at 62%: one pace push, and a second run adds none.
func TestQuotaAtSixtyPercentOnDayThreePushesOnce(t *testing.T) {
	x := quotaCase(t)
	reset := qNow.Add(4*24*time.Hour + 12*time.Hour)
	x.session("personal", x.sid, "/work/demo", "", qNow.Add(-20*time.Minute))
	x.spool(sampleAt(x.sid, qNow.Add(-2*time.Hour), 55, reset), sampleAt(x.sid, qNow.Add(-10*time.Minute), 62, reset))
	x.run()
	x.run()
	pace := grepAlerts(x.alerts(), "quota pace high")
	if len(pace) != 1 || !strings.Contains(pace[0], "7d 62% on day 3 of 7") || !strings.Contains(pace[0], "demo") {
		t.Fatalf("want one pace push naming the top burner, got %q", x.alerts())
	}
	eq(t, x.one("select count(*) from quota"), "2")
	eq(t, x.one("select rl from account_seen where session=?", x.sid), "1")
}

// 59% on day 3, and 62% on day 4: no pace push (design §8: >= 60% by day 3).
func TestQuotaBelowThePaceRuleDoesNotPush(t *testing.T) {
	for _, c := range []struct {
		pct   float64
		reset time.Duration
	}{{59, 4*24*time.Hour + 12*time.Hour}, {62, 3*24*time.Hour + 12*time.Hour}} {
		x := quotaCase(t)
		x.session("personal", x.sid, "/work/demo", "", qNow.Add(-20*time.Minute))
		x.spool(sampleAt(x.sid, qNow.Add(-10*time.Minute), c.pct, qNow.Add(c.reset)))
		x.run()
		if got := grepAlerts(x.alerts(), "quota "); len(got) != 0 {
			t.Fatalf("%v%%: want no quota push, got %q", c.pct, got)
		}
	}
}

// At 100% the push names Appxite-only work and the enterprise account, with enterprise $ so far.
func TestQuotaUsedUpNamesAppxiteOnlyAndTheEnterpriseAccount(t *testing.T) {
	x := quotaCase(t)
	reset := qNow.Add(24 * time.Hour)
	x.session("personal", x.sid, "/work/demo", "", qNow.Add(-20*time.Minute))
	ent := "22222222-2222-3333-4444-555555555555"
	x.session("work", ent, "/work/axi", "", qNow.Add(-30*time.Minute))
	x.spool(sampleAt(x.sid, qNow.Add(-10*time.Minute), 100, reset),
		quota.Line{TS: iso(qNow.Add(-29 * time.Minute)), Kind: "session", Session: ent, Persona: "work"})
	x.run()
	x.run()
	used := grepAlerts(x.alerts(), "quota used up")
	if len(used) != 1 || !strings.Contains(used[0], "Only Appxite work (axi) may continue, on the Appxite enterprise account") ||
		!strings.Contains(used[0], "Enterprise this week $") || strings.Contains(used[0], "Enterprise this week $0.00 ") {
		t.Fatalf("want one used-up push naming Appxite-only work and enterprise $, got %q", x.alerts())
	}
	if got := grepAlerts(x.alerts(), "quota pace"); len(got) != 0 {
		t.Fatalf("used up is one push, not two: %q", got)
	}
	if got := grepAlerts(x.alerts(), "enterprise account: session"); len(got) != 0 {
		t.Fatalf("axi may use the enterprise account: %q", got)
	}
}

// Samples two hours apart: the request between them is uncovered usage, not 0 and not silently covered.
func TestQuotaTwoHourGapIsUncoveredNotZero(t *testing.T) {
	x := quotaCase(t)
	reset := qNow.Add(4 * 24 * time.Hour)
	t0 := qNow.Add(-3 * time.Hour)
	x.session("personal", x.sid, "/work/demo", "", t0, t0.Add(time.Hour), t0.Add(2*time.Hour))
	x.spool(sampleAt(x.sid, t0, 20, reset), sampleAt(x.sid, t0.Add(2*time.Hour), 24, reset))
	x.run()
	rep := x.report()
	var gap float64
	if err := sqlOne(x, "select cost_list_usd from request where ts_first=?", iso(t0.Add(time.Hour)), &gap); err != nil {
		t.Fatal(err)
	}
	if gap == 0 || rep.Uncovered != gap || rep.MaxUSD <= gap {
		t.Fatalf("uncovered %v, want the gap request's %v of %v", rep.Uncovered, gap, rep.MaxUSD)
	}
	if !strings.Contains(rep.Text(), "uncovered $") || strings.Contains(rep.Text(), "uncovered $0.00 ") {
		t.Fatalf("the report must say the uncovered usage:\n%s", rep.Text())
	}
}

// Open sessions with no sample in the last 45 minutes: the quota is stale, and the ingest alerts once.
func TestQuotaOpenSessionsWithNoRecentSampleAreStale(t *testing.T) {
	x := quotaCase(t)
	x.session("personal", x.sid, "/work/demo", "", qNow.Add(-10*time.Minute))
	x.run() // no spool at all: the sampler is not installed or broken
	x.spool(sampleAt(x.sid, qNow.Add(-50*time.Minute), 20, qNow.Add(4*24*time.Hour)))
	x.run()
	x.run()
	stale := grepAlerts(x.alerts(), "quota/max STALE")
	if len(stale) != 2 || !strings.Contains(stale[0], "newest quota sample is none") {
		t.Fatalf("want one stale alert per gap (none, then the 50-min-old sample), got %q", x.alerts())
	}
	if rep := x.report(); rep.Status != "stale" {
		t.Fatalf("status %s, want stale", rep.Status)
	}
}

// A scifi project on the enterprise account is S1, whether the launch tag or the statusline says so.
func TestQuotaScifiSessionOnTheEnterpriseAccountIsS1(t *testing.T) {
	x := quotaCase(t)
	byStatusline := "33333333-2222-3333-4444-555555555555"
	byTag := "44444444-2222-3333-4444-555555555555"
	axi := "55555555-2222-3333-4444-555555555555"
	x.session("work", byStatusline, "/work/demo", "", qNow.Add(-20*time.Minute))
	x.session("work", byTag, "/work/demo", "[crius account=enterprise project=demo role=lead]", qNow.Add(-20*time.Minute))
	x.session("work", axi, "/work/axi", "", qNow.Add(-20*time.Minute))
	x.spool(quota.Line{TS: iso(qNow.Add(-19 * time.Minute)), Kind: "session", Session: byStatusline, Persona: "work"},
		quota.Line{TS: iso(qNow.Add(-19 * time.Minute)), Kind: "session", Session: axi, Persona: "work"})
	x.run()
	x.run()
	s1 := grepAlerts(x.alerts(), "S1 personal work on the enterprise account")
	if len(s1) != 2 || !strings.Contains(strings.Join(s1, "\n"), "session 33333333, project demo (account from statusline)") ||
		!strings.Contains(strings.Join(s1, "\n"), "session 44444444, project demo (account from tag)") {
		t.Fatalf("want two S1 alerts (statusline, tag), got %q", x.alerts())
	}
	if got := grepAlerts(x.alerts(), "55555555"); len(got) != 0 {
		t.Fatalf("axi may use the enterprise account: %q", got)
	}
}

// A session that showed rate_limits once is on Max, whether a render without them came before or after.
func TestQuotaRateLimitsSeenOnceMeanMax(t *testing.T) {
	x := quotaCase(t)
	x.session("work", x.sid, "/work/demo", "", qNow.Add(-20*time.Minute))
	x.spool(quota.Line{TS: iso(qNow.Add(-19 * time.Minute)), Kind: "session", Session: x.sid, Persona: "work"},
		sampleAt(x.sid, qNow.Add(-18*time.Minute), 20, qNow.Add(4*24*time.Hour)))
	x.run()
	x.spool(quota.Line{TS: iso(qNow.Add(-17 * time.Minute)), Kind: "session", Session: x.sid, Persona: "work"})
	x.run()
	if got := grepAlerts(x.alerts(), "enterprise account"); len(got) != 0 {
		t.Fatalf("want no S1, got %q", got)
	}
}

// The newest sample's epoch already ended: no day or share from it, no push, and the report says so.
func TestQuotaPastEpochIsNotTheCurrentOne(t *testing.T) {
	x := quotaCase(t)
	x.session("personal", x.sid, "/work/demo", "", qNow.Add(-20*time.Minute))
	x.spool(sampleAt(x.sid, qNow.Add(-10*time.Minute), 100, qNow.Add(-time.Hour)))
	x.run()
	if got := grepAlerts(x.alerts(), "quota "); len(got) != 0 {
		t.Fatalf("a past epoch must not push: %q", got)
	}
	rep := x.report()
	if rep.Day != 0 || rep.SevenPct != 0 || rep.Status != "partial" || !strings.Contains(rep.Text(), "epoch ended") || rep.MaxUSD == 0 {
		t.Fatalf("day %d pct %v max %v\n%s", rep.Day, rep.SevenPct, rep.MaxUSD, rep.Text())
	}
}

// Codex P1: a launch tag that says max does not hide what the statusline saw. A scifi session billed to the
// enterprise account is S1 even when its tag claims Max, and its $ counts as enterprise.
func TestQuotaMaxTagDoesNotHideEnterpriseBilling(t *testing.T) {
	x := quotaCase(t)
	x.session("work", x.sid, "/work/demo", "[crius account=max project=demo role=lead]", qNow.Add(-20*time.Minute))
	x.spool(quota.Line{TS: iso(qNow.Add(-19 * time.Minute)), Kind: "session", Session: x.sid, Persona: "work"})
	x.run()
	if len(grepAlerts(x.alerts(), "S1 personal work on the enterprise account: session 11111111")) != 1 ||
		len(grepAlerts(x.alerts(), "account mismatch in session 11111111")) != 1 {
		t.Fatalf("want S1 and mismatch, got %q", x.alerts())
	}
	if rep := x.report(); rep.EntWeekUSD == 0 || rep.MaxUSD != 0 {
		t.Fatalf("enterprise $ %v, max $ %v", rep.EntWeekUSD, rep.MaxUSD)
	}
}

// Codex P2: an enterprise session the statusline saw but no transcript gave is still checked.
func TestQuotaEnterpriseSessionWithoutTranscriptIsChecked(t *testing.T) {
	x := quotaCase(t)
	x.spool(quota.Line{TS: iso(qNow.Add(-19 * time.Minute)), Kind: "session", Session: "66666666-x", Persona: "work"})
	x.run()
	if len(grepAlerts(x.alerts(), "enterprise session 66666666 has no known project")) != 1 {
		t.Fatalf("got %q", x.alerts())
	}
}

// Codex P2 x2: an idle session repeating the previous epoch (older reset, its own 5h window) does not roll the
// report back, and the 5h share is the highest for the current window, not the newest line's.
func TestQuotaIdleSessionRepeatingOldValuesDoesNotRollBack(t *testing.T) {
	x := quotaCase(t)
	other := "77777777-2222-3333-4444-555555555555"
	x.session("personal", x.sid, "/work/demo", "", qNow.Add(-20*time.Minute))
	cur := sampleAt(x.sid, qNow.Add(-10*time.Minute), 30, qNow.Add(5*24*time.Hour))
	cur.FivePct = f(80)
	old := sampleAt(other, qNow.Add(-5*time.Minute), 95, qNow.Add(-2*24*time.Hour))
	old.Kind, old.FivePct, old.FiveReset = "heartbeat", f(20), cur.FiveReset
	x.spool(cur, old)
	x.run()
	rep := x.report()
	if !rep.Ended.IsZero() || rep.SevenPct != 30 || rep.FivePct != 80 || rep.Day != 3 {
		t.Fatalf("ended %v 7d %v 5h %v day %d", rep.Ended, rep.SevenPct, rep.FivePct, rep.Day)
	}
}

// Codex P2: a sample with no seven-day share measured nothing: not 0%, not a cover for requests.
func TestQuotaSampleWithoutShareIsNotZeroPercent(t *testing.T) {
	x := quotaCase(t)
	x.session("personal", x.sid, "/work/demo", "", qNow.Add(-20*time.Minute))
	l := sampleAt(x.sid, qNow.Add(-10*time.Minute), 0, qNow.Add(4*24*time.Hour))
	l.SevenPct = nil
	x.spool(l)
	x.run()
	if rep := x.report(); rep.Status != "stale" || !rep.LastSample.IsZero() || rep.Uncovered == 0 {
		t.Fatalf("status %s last %v uncovered %v", rep.Status, rep.LastSample, rep.Uncovered)
	}
}

// Codex P2: a request with no list price is missing $, said in the report, never $0 in a complete answer.
func TestQuotaUnpricedRequestsMakeThePartial(t *testing.T) {
	x := quotaCase(t)
	x.session("personal", x.sid, "/work/demo", "", qNow.Add(-20*time.Minute))
	x.spool(sampleAt(x.sid, qNow.Add(-10*time.Minute), 30, qNow.Add(4*24*time.Hour)))
	x.run()
	db, err := store.Open(x.home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("update request set cost_list_usd=null"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if rep := x.report(); rep.Status != "partial" || rep.Unpriced != 1 || !strings.Contains(rep.Text(), "unpriced: 1") {
		t.Fatalf("status %s unpriced %d\n%s", rep.Status, rep.Unpriced, rep.Text())
	}
}

func TestQuotaBadSpoolLineIsCountedNotSkippedQuietly(t *testing.T) {
	x := quotaCase(t)
	appendFile(t, filepath.Join(x.home, "spool", "quota.jsonl"), "{not json\n")
	st := x.run()
	if st.BadLines != 1 || !strings.Contains(strings.Join(st.Warnings, "\n"), "bad quota line") {
		t.Fatalf("%+v", st)
	}
}

func sqlOne(x *ic, q string, arg any, dst any) error {
	db, err := sql.Open("sqlite", filepath.Join(x.home, "state", "record.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	return db.QueryRow(q, arg).Scan(dst)
}
