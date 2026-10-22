package ingest

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
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
	p, err := QuotaProjects(x.home)
	if err != nil {
		x.t.Fatal(err)
	}
	rep, err := quota.Build(db, p, qNow)
	if err != nil {
		x.t.Fatal(err)
	}
	return rep
}

// Day 3 of the epoch (started 2.5 days ago) at 62%: one push (design §9), and a second run adds none. 62% by day 3
// also runs out before the reset, so that one push is the runout push, carrying the pace facts too.
func TestQuotaAtSixtyPercentOnDayThreePushesOnce(t *testing.T) {
	x := quotaCase(t)
	reset := qNow.Add(4*24*time.Hour + 12*time.Hour)
	x.session("personal", x.sid, "/work/demo", "", qNow.Add(-20*time.Minute))
	x.spool(sampleAt(x.sid, qNow.Add(-2*time.Hour), 55, reset), sampleAt(x.sid, qNow.Add(-10*time.Minute), 62, reset))
	x.run()
	x.run()
	push := grepAlerts(x.alerts(), "— quota ")
	if len(push) != 1 || !strings.Contains(push[0], "7d 62% day 3") || !strings.Contains(push[0], "top burner demo 100%, slow it") ||
		!strings.Contains(push[0], "only Appxite work (axi) may continue") || !strings.Contains(push[0], "Enterprise this week") {
		t.Fatalf("want one whole push naming the top burner and the Appxite rule, got %q", x.alerts())
	}
	eq(t, x.one("select count(*) from quota"), "2")
	eq(t, x.q("select rl from account_seen where session=?", x.sid), [][]string{{"1"}, {"1"}})
}

// 59% on day 3, and 62% on day 4: no pace push (design §8: >= 60% by day 3). Both run out before the reset; that is
// the runout push, tested on its own.
func TestQuotaBelowThePaceRuleDoesNotPush(t *testing.T) {
	for _, c := range []struct {
		pct   float64
		reset time.Duration
	}{{59, 4*24*time.Hour + 12*time.Hour}, {62, 3*24*time.Hour + 12*time.Hour}} {
		x := quotaCase(t)
		x.session("personal", x.sid, "/work/demo", "", qNow.Add(-20*time.Minute))
		x.spool(sampleAt(x.sid, qNow.Add(-10*time.Minute), c.pct, qNow.Add(c.reset)))
		x.run()
		if got := grepAlerts(x.alerts(), "quota pace high"); len(got) != 0 {
			t.Fatalf("%v%%: want no pace push, got %q", c.pct, got)
		}
		// the runout push can stand for a pace push; the pace rule itself must not have held
		eq(t, x.one("select count(*) from alert where key like 'quota:pace:%'"), "0")
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
	if len(stale) != 2 || !strings.Contains(stale[0], "personal: no statusline line yet") {
		t.Fatalf("want one stale alert per gap (none, then the 50-min-old sample), got %q", x.alerts())
	}
	if rep := x.report(); rep.Status != "partial" || len(rep.Stale) == 0 {
		t.Fatalf("status %s stale %v, want partial and stale", rep.Status, rep.Stale)
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

// Codex round 2 P1: the latest statusline line says where the session bills now, and any enterprise line (or an
// enterprise tag) puts it under the guard, even when Max was seen before.
func TestQuotaLatestObservationBillsAndAnyEnterpriseIsGuarded(t *testing.T) {
	x := quotaCase(t)
	byTag := "88888888-2222-3333-4444-555555555555"
	x.session("work", x.sid, "/work/demo", "", qNow.Add(-20*time.Minute))
	x.session("work", byTag, "/work/demo", "[crius account=enterprise project=demo role=lead]", qNow.Add(-20*time.Minute))
	x.spool(sampleAt(x.sid, qNow.Add(-30*time.Minute), 20, qNow.Add(4*24*time.Hour)),
		quota.Line{TS: iso(qNow.Add(-19 * time.Minute)), Kind: "session", Session: x.sid, Persona: "work"},
		sampleAt(byTag, qNow.Add(-19*time.Minute), 20, qNow.Add(4*24*time.Hour)))
	x.run()
	s1 := strings.Join(grepAlerts(x.alerts(), "S1 personal work on the enterprise account"), "\n")
	if !strings.Contains(s1, "session 11111111, project demo (account from statusline)") ||
		!strings.Contains(s1, "session 88888888, project demo (account from tag)") {
		t.Fatalf("want S1 for both, got %q", x.alerts())
	}
	if rep := x.report(); rep.EntWeekUSD == 0 {
		t.Fatalf("the session now bills enterprise: %+v", rep)
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

// Codex P2: a sample with no seven-day share measured nothing: not 0%, not a cover for requests, not a sign of the
// account. Ingest counts it as a bad line.
func TestQuotaSampleWithoutShareIsNotZeroPercent(t *testing.T) {
	x := quotaCase(t)
	x.session("personal", x.sid, "/work/demo", "", qNow.Add(-20*time.Minute))
	l := sampleAt(x.sid, qNow.Add(-10*time.Minute), 0, qNow.Add(4*24*time.Hour))
	l.SevenPct = nil
	x.spool(l)
	if st := x.run(); st.BadLines != 1 || st.Status != "partial" {
		t.Fatalf("the line must be counted as bad, got %+v", st)
	}
	if rep := x.report(); rep.Status != "partial" || len(rep.Stale) == 0 || !rep.LastSample.IsZero() || rep.UnknownUSD == 0 {
		t.Fatalf("status %s last %v unknown %v", rep.Status, rep.LastSample, rep.UnknownUSD)
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

// Codex round 2 P2: an idle session's heartbeat from the previous epoch is not a sample of the current one.
func TestQuotaOldEpochHeartbeatIsNotACurrentSample(t *testing.T) {
	x := quotaCase(t)
	other := "99999999-2222-3333-4444-555555555555"
	x.session("personal", x.sid, "/work/demo", "", qNow.Add(-10*time.Minute))
	x.spool(sampleAt(x.sid, qNow.Add(-2*time.Hour), 20, qNow.Add(4*24*time.Hour)),
		sampleAt(other, qNow.Add(-5*time.Minute), 90, qNow.Add(-3*24*time.Hour)))
	x.run()
	if rep := x.report(); rep.Status != "partial" || len(rep.Stale) == 0 || rep.Uncovered == 0 || rep.SevenPct != 20 {
		t.Fatalf("status %s uncovered %v 7d %v", rep.Status, rep.Uncovered, rep.SevenPct)
	}
}

// Codex round 2 P2: enterprise "today" counts from local midnight even when the Max epoch started later.
func TestQuotaEnterpriseTodayIsNotCutByTheMaxEpoch(t *testing.T) {
	x := quotaCase(t)
	ent := "aaaaaaaa-2222-3333-4444-555555555555"
	morning := time.Date(qNow.Year(), qNow.Month(), qNow.Day(), 0, 30, 0, 0, time.Local)
	reset := morning.Add(30*time.Minute + 7*24*time.Hour) // the Max epoch began at 01:00 local
	x.session("work", ent, "/work/axi", "", morning)
	x.spool(quota.Line{TS: iso(morning.Add(time.Minute)), Kind: "session", Session: ent, Persona: "work"},
		sampleAt(x.sid, qNow.Add(-time.Minute), 1, reset))
	x.run()
	if rep := x.report(); rep.EntDay == 0 || rep.EntWeekUSD != 0 {
		t.Fatalf("today %v week %v", rep.EntDay, rep.EntWeekUSD)
	}
}

// Codex round 2 P2: once the work statusline samples, an untagged work session it never saw counts as open.
func TestQuotaUnknownSessionOfASampledPersonaCanBeStale(t *testing.T) {
	x := quotaCase(t)
	seenBefore := "bbbbbbbb-2222-3333-4444-555555555555"
	x.session("work", x.sid, "/work/demo", "", qNow.Add(-10*time.Minute))
	l := sampleAt(seenBefore, qNow.Add(-3*time.Hour), 20, qNow.Add(4*24*time.Hour))
	l.Persona = "work"
	x.spool(l)
	x.run()
	if rep := x.report(); rep.Status != "partial" || len(rep.Stale) == 0 {
		t.Fatalf("status %s stale %v", rep.Status, rep.Stale)
	}
}

// Codex round 2 P1 claim: a pace push longer than a bus line blocks the alert queue. queueAlert cuts every alert
// to the bus limit, so a long one is posted and the next one still goes out.
func TestQuotaLongPaceAlertIsPostedAndDoesNotBlockTheQueue(t *testing.T) {
	long := strings.Repeat("x", 60)
	extra := axiProject
	for _, p := range []string{"a" + long, "b" + long, "c" + long} {
		extra += "\n[projects." + p + "]\npersona = \"personal\"\naccounts = [\"max\"]\ncwd = [\"/work/" + p +
			"\"]\nrepos = []\nteam_logs = []\nartifact_dirs = []\n"
	}
	clock = func() time.Time { return qNow }
	t.Cleanup(func() { clock = time.Now })
	x := newIC(t, cfg{extra: extra})
	reset := qNow.Add(4*24*time.Hour + 12*time.Hour)
	for i, p := range []string{"a" + long, "b" + long, "c" + long} {
		sid := strings.Repeat(string(rune('c'+i)), 8) + "-2222-3333-4444-555555555555"
		x.session("personal", sid, "/work/"+p, "", qNow.Add(-20*time.Minute))
		x.spool(sampleAt(sid, qNow.Add(-10*time.Minute), 62, reset))
	}
	x.run()
	if len(grepAlerts(x.alerts(), "quota runs out")) != 1 { // 62% on day 3 also runs out: one push carries both
		t.Fatalf("pace alert not posted: %q", x.alerts())
	}
	x.session("work", x.sid, "/work/demo", "[crius account=enterprise project=demo role=lead]", qNow.Add(-5*time.Minute))
	x.run()
	if len(grepAlerts(x.alerts(), "S1 personal work")) != 1 {
		t.Fatalf("the next alert must still go out: %q", x.alerts())
	}
}

// Codex round 3 P1: enterprise use is judged against the project active when it happened. A session billed to
// enterprise on axi that switches to demo (still enterprise) is S1 for demo; one that did axi on enterprise and then
// demo on Max is not S1.
func TestQuotaEnterpriseStretchIsCheckedAgainstTheProjectsActiveInIt(t *testing.T) {
	x := quotaCase(t)
	bad, good := "c1c1c1c1-2222-3333-4444-555555555555", "c2c2c2c2-2222-3333-4444-555555555555"
	for _, sid := range []string{bad, good} {
		recs := []map[string]any{
			prompt("p1-"+sid, iso(qNow.Add(-60*time.Minute)), "[crius account=enterprise project=axi role=lead]\n", "", "/work/axi"),
			assistant(A{Req: sid + "-1", TS: iso(qNow.Add(-59 * time.Minute))}),
			prompt("p2-"+sid, iso(qNow.Add(-30*time.Minute)), "[crius account=max project=demo role=lead]\n", "", "/work/axi"),
			assistant(A{Req: sid + "-2", TS: iso(qNow.Add(-29 * time.Minute))})}
		testutil.Write(t, filepath.Join(x.work, "-p", sid+".jsonl"), lines(recs...))
	}
	x.spool(quota.Line{TS: iso(qNow.Add(-58 * time.Minute)), Kind: "session", Session: bad, Persona: "work"},
		quota.Line{TS: iso(qNow.Add(-58 * time.Minute)), Kind: "session", Session: good, Persona: "work"},
		sampleAt(good, qNow.Add(-28*time.Minute), 20, qNow.Add(4*24*time.Hour)))
	x.run()
	s1 := grepAlerts(x.alerts(), "S1 personal work")
	if len(s1) != 1 || !strings.Contains(s1[0], "session c1c1c1c1, project demo (account from statusline)") {
		t.Fatalf("want S1 only for the session that stayed on enterprise into demo, got %q", x.alerts())
	}
	if len(grepAlerts(x.alerts(), "account mismatch in session c1c1c1c1")) != 1 {
		t.Fatalf("tag max with an enterprise statusline is a mismatch: %q", x.alerts())
	}
	rep := x.report()
	ent := map[string]float64{}
	for _, sh := range rep.EntWeek {
		ent[sh.Project] = sh.USD
	}
	if ent["axi"] == 0 || ent["demo"] == 0 || rep.MaxUSD == 0 {
		t.Fatalf("enterprise by project %v, max $ %v", ent, rep.MaxUSD)
	}
}

// Codex round 3 P2: an unpriced enterprise request today, before the Max epoch began, is still said missing.
func TestQuotaUnpricedEnterpriseTodayBeforeTheEpochIsSaid(t *testing.T) {
	x := quotaCase(t)
	ent := "aaaaaaaa-2222-3333-4444-555555555555"
	morning := time.Date(qNow.Year(), qNow.Month(), qNow.Day(), 0, 30, 0, 0, time.Local)
	x.session("work", ent, "/work/axi", "", morning)
	x.spool(quota.Line{TS: iso(morning.Add(time.Minute)), Kind: "session", Session: ent, Persona: "work"},
		sampleAt(x.sid, qNow.Add(-time.Minute), 1, morning.Add(30*time.Minute+7*24*time.Hour)))
	x.run()
	db, err := store.Open(x.home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("update request set cost_list_usd=null"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if rep := x.report(); rep.Unpriced != 1 || rep.Status != "partial" {
		t.Fatalf("unpriced %d status %s", rep.Unpriced, rep.Status)
	}
}

// Codex round 4 P2: requests before a session's first statusline line take that line's account, so the guard checks
// the projects they ran under too.
func TestQuotaEnterpriseBeforeTheFirstLineIsGuarded(t *testing.T) {
	x := quotaCase(t)
	sid := "c3c3c3c3-2222-3333-4444-555555555555"
	recs := []map[string]any{
		prompt("p1", iso(qNow.Add(-60*time.Minute)), "fix the demo\n", "", "/work/demo"),
		assistant(A{Req: sid + "-1", TS: iso(qNow.Add(-59 * time.Minute))}),
		prompt("p2", iso(qNow.Add(-50*time.Minute)), "[crius account=enterprise project=axi role=lead]\n", "", "/work/demo"),
		assistant(A{Req: sid + "-2", TS: iso(qNow.Add(-49 * time.Minute))})}
	testutil.Write(t, filepath.Join(x.work, "-p", sid+".jsonl"), lines(recs...))
	x.spool(quota.Line{TS: iso(qNow.Add(-45 * time.Minute)), Kind: "session", Session: sid, Persona: "work"})
	x.run()
	if s1 := grepAlerts(x.alerts(), "S1 personal work"); len(s1) != 1 ||
		!strings.Contains(s1[0], "session c3c3c3c3, project demo (account from statusline)") {
		t.Fatalf("want S1 for demo, got %q", x.alerts())
	}
}

// Codex round 4 P2: a spool time with a zone offset is ordered by the instant, not by its text.
func TestQuotaSpoolTimesWithOffsetsAreOrderedByInstant(t *testing.T) {
	x := quotaCase(t)
	x.session("work", x.sid, "/work/demo", "", qNow.Add(-20*time.Minute))
	ent := quota.Line{TS: qNow.Add(-60 * time.Minute).In(time.FixedZone("", 4*3600)).Format(time.RFC3339),
		Kind: "session", Session: x.sid, Persona: "work"}
	x.spool(ent, sampleAt(x.sid, qNow.Add(-30*time.Minute), 20, qNow.Add(4*24*time.Hour)))
	x.run()
	if rep := x.report(); rep.EntWeekUSD != 0 || rep.MaxUSD == 0 {
		t.Fatalf("the 20-min-old request bills Max: enterprise %v max %v", rep.EntWeekUSD, rep.MaxUSD)
	}
}

// Codex round 5 P2: a session whose requests have no time does not stop the quota checks.
func TestQuotaRequestsWithoutTimeDoNotStopTheChecks(t *testing.T) {
	x := quotaCase(t)
	x.session("work", x.sid, "/work/demo", "", qNow.Add(-10*time.Minute))
	x.run()
	db, err := store.Open(x.home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("update request set ts_first=null, ts_last=null"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	x.report()
}

// Crius 18:48 P1: spend whose account is unknown may be enterprise $, so the report is partial and the enterprise
// line says its totals are a lower bound, not "$0.00 (none)".
func TestQuotaUnknownAccountSpendIsNotZeroEnterprise(t *testing.T) {
	x := quotaCase(t)
	unknown := "dddddddd-2222-3333-4444-555555555555"
	x.session("personal", x.sid, "/work/demo", "", qNow.Add(-10*time.Minute))
	x.session("work", unknown, "/work/demo", "", qNow.Add(-2*time.Hour)) // closed: not stale, only unknown
	x.spool(sampleAt(x.sid, qNow.Add(-5*time.Minute), 20, qNow.Add(4*24*time.Hour)))
	x.run()
	rep := x.report()
	if rep.UnknownUSD == 0 || rep.Status != "partial" || len(rep.Reasons) != 1 ||
		!strings.Contains(rep.Reasons[0], "account unknown") {
		t.Fatalf("unknown %v status %s reasons %q", rep.UnknownUSD, rep.Status, rep.Reasons)
	}
	txt := rep.Text()
	if !strings.Contains(txt, "Enterprise (Appxite API billing, list price): unknown "+fmt.Sprintf("$%.2f", rep.UnknownUSD)) ||
		!strings.Contains(txt, "guard: not measured for 1 session") || strings.Contains(txt, "violations: none") ||
		strings.Contains(txt, "$0.00 (none)") {
		t.Fatalf("unknown spend must read as unknown, not $0 or none:\n%s", txt)
	}
}

// Crius 19:04: a persona with open sessions that has never written a statusline line is stale, even while the
// other persona samples.
func TestQuotaPersonaThatNeverSampledIsStale(t *testing.T) {
	x := quotaCase(t)
	w := "eeeeeeee-2222-3333-4444-555555555555"
	x.session("personal", x.sid, "/work/demo", "", qNow.Add(-10*time.Minute))
	x.session("work", w, "/work/demo", "", qNow.Add(-10*time.Minute))
	x.spool(sampleAt(x.sid, qNow.Add(-5*time.Minute), 20, qNow.Add(4*24*time.Hour)))
	x.run()
	if rep := x.report(); rep.Status != "partial" || len(rep.Stale) != 1 || !strings.HasPrefix(rep.Stale[0], "work") {
		t.Fatalf("status %s stale %v", rep.Status, rep.Stale)
	}
	if a := grepAlerts(x.alerts(), "quota/max STALE"); len(a) != 1 || !strings.Contains(a[0], "work: no statusline line yet") {
		t.Fatalf("%q", x.alerts())
	}
}

// §8: the pace push also fires when this epoch burns faster per day than the last 7 days did on average. It needs 7
// days of samples, starts on day 2, and is the one pace push of the epoch.
func TestQuotaPaceFasterThanTheSevenDayAverage(t *testing.T) {
	day := 24 * time.Hour
	reset := qNow.Add(5 * day) // day 3, 2 days passed
	for _, c := range []struct {
		name          string
		before, prevE float64 // the previous epoch's share 8 days ago and at its end
		history       bool
		want          int
	}{
		{"faster than average", 5, 19, true, 1},  // avg (14+20)/7 = 4.9%/day, now 10%/day
		{"slower than average", 10, 90, true, 0}, // avg (80+20)/7 = 14.3%/day
		{"under 7 days of samples", 5, 19, false, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := quotaCase(t)
			x.session("personal", x.sid, "/work/demo", "", qNow.Add(-10*time.Minute))
			var ls []quota.Line
			if c.history {
				ls = append(ls, sampleAt(x.sid, qNow.Add(-8*day), c.before, qNow.Add(-2*day)))
			}
			ls = append(ls, sampleAt(x.sid, qNow.Add(-2*day-time.Hour), c.prevE, qNow.Add(-2*day)),
				sampleAt(x.sid, qNow.Add(-5*time.Minute), 20, reset))
			x.spool(ls...)
			x.run()
			x.run()
			if got := grepAlerts(x.alerts(), "quota pace high"); len(got) != c.want {
				t.Fatalf("want %d pace pushes, got %q", c.want, x.alerts())
			} else if c.want == 1 && !strings.Contains(got[0], "faster than the 7-day average 4.9%/day") {
				t.Fatalf("%q", got[0])
			}
		})
	}
}

// §8: when the share will pass 100% before resets_at at this pace, one push (from day 2) says only Appxite work may
// continue on the enterprise account.
func TestQuotaRunsOutBeforeTheResetPushesOnce(t *testing.T) {
	day := 24 * time.Hour
	for _, c := range []struct {
		name   string
		passed time.Duration
		pct    float64
		want   int
	}{
		{"day 3 at 40%", 2 * day, 40, 1}, // runs out on day 6
		{"day 3 at 25%", 2 * day, 25, 0}, // runs out after the reset
		{"day 1 at 20%", 12 * time.Hour, 20, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			x := quotaCase(t)
			x.session("personal", x.sid, "/work/demo", "", qNow.Add(-10*time.Minute))
			x.spool(sampleAt(x.sid, qNow.Add(-5*time.Minute), c.pct, qNow.Add(7*day-c.passed)))
			x.run()
			x.run()
			got := grepAlerts(x.alerts(), "quota runs out")
			if len(got) != c.want || c.want == 1 && (!strings.Contains(got[0], "only Appxite work (axi) may continue") ||
				!strings.Contains(got[0], "Enterprise this week")) {
				t.Fatalf("want %d, got %q", c.want, x.alerts())
			}
		})
	}
}

// With some spend known to be enterprise and some unknown, the known enterprise totals print as a floor.
func TestQuotaKnownEnterpriseBesideUnknownIsAFloor(t *testing.T) {
	x := quotaCase(t)
	ent, unknown := "ffffffff-2222-3333-4444-555555555555", "dddddddd-2222-3333-4444-555555555555"
	x.session("work", ent, "/work/axi", "[crius account=enterprise project=axi role=lead]", qNow.Add(-2*time.Hour))
	x.session("work", unknown, "/work/demo", "", qNow.Add(-2*time.Hour))
	x.spool(sampleAt(x.sid, qNow.Add(-5*time.Minute), 20, qNow.Add(4*24*time.Hour)))
	x.run()
	if txt := x.report().Text(); !strings.Contains(txt, "(account not measured) · at least week $0.02 (axi $0.02)") {
		t.Fatalf("%s", txt)
	}
}

// Codex round 6: every push says unknown-account spend as unknown, not as "Enterprise this week $0.00 (none)".
func TestQuotaPushesSayUnknownSpend(t *testing.T) {
	x := quotaCase(t)
	unknown := "dddddddd-2222-3333-4444-555555555555"
	x.session("personal", x.sid, "/work/demo", "", qNow.Add(-10*time.Minute))
	x.session("work", unknown, "/work/demo", "", qNow.Add(-2*time.Hour))
	x.spool(sampleAt(x.sid, qNow.Add(-5*time.Minute), 40, qNow.Add(5*24*time.Hour)))
	x.run()
	push := grepAlerts(x.alerts(), "quota runs out")
	if len(push) != 1 || strings.Contains(push[0], "$0.00 (none)") || !strings.Contains(push[0], "unknown $") {
		t.Fatalf("%q", x.alerts())
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

// Codex round 10: a guard alert names the project clipped, so its instruction is not cut off the bus line.
func TestQuotaGuardAlertFitsABusLine(t *testing.T) {
	x := quotaCase(t)
	long := strings.Repeat("p", 200)
	x.session("work", x.sid, "/work/demo", "[crius account=enterprise project="+long+" role=lead]", qNow.Add(-20*time.Minute))
	x.run()
	s1 := grepAlerts(x.alerts(), "S1 personal work on the enterprise account")
	if len(s1) != 1 || !strings.HasSuffix(s1[0], "Stop it or move it to the Max account.") {
		t.Fatalf("%q", x.alerts())
	}
}

// Crius 22:12, end to end (sample -> ingest -> report): an enterprise-only work persona with one session that
// rendered every 5 min for 3 h after its one response, and a headless session (no statusline) with a request 20 min
// ago, which puts work among the personas that need a line, is not stale.
func TestQuotaEnterprisePersonaIsNotStale(t *testing.T) {
	x := quotaCase(t)
	if err := os.MkdirAll(filepath.Join(x.home, "spool"), 0o700); err != nil {
		t.Fatal(err)
	}
	e, u := "eeeeeeee-1111-2222-3333-444444444444", "dddddddd-1111-2222-3333-444444444444"
	start := qNow.Add(-3 * time.Hour)
	x.session("work", e, "/work/axi", "", start)
	x.session("work", u, "/work/axi", "", qNow.Add(-20*time.Minute))
	render := func(usd float64, at time.Time) {
		in := fmt.Sprintf(`{"session_id":%q,"cost":{"total_cost_usd":%v,"total_api_duration_ms":%v}}`, e, usd, usd*1000)
		if _, err := quota.Sample(x.home, "work", []byte(in), at); err != nil {
			t.Fatal(err)
		}
	}
	render(0, start.Add(-2*time.Minute))
	render(1, start.Add(time.Minute))
	for at := start.Add(5 * time.Minute); !at.After(qNow); at = at.Add(5 * time.Minute) {
		render(1, at)
	}
	x.run()
	if rep := x.report(); len(rep.Stale) != 0 {
		t.Fatalf("work stale: %v", rep.Stale)
	}
}

// Review of 47842ed (codex P1): a heartbeat repeats a session's last values, so it measures nothing new. A 95%
// measured 2 h ago with heartbeats since is a stale measurement (unknown), not a fresh one (room). A Max sample
// without a 5h window does not say 5h is 0%.
func TestQuotaRoomCountsMeasurementsNotHeartbeats(t *testing.T) {
	for _, c := range []struct {
		name, limits string
		since        time.Duration
	}{
		{"heartbeats", `"five_hour":{"used_percentage":40,"resets_at":%d},"seven_day":{"used_percentage":95,"resets_at":%d}`, 2 * time.Hour},
		{"no 5h", `"seven_day":{"used_percentage":40,"resets_at":%[2]d}`, 10 * time.Minute},
	} {
		x := quotaCase(t)
		if err := os.MkdirAll(filepath.Join(x.home, "spool"), 0o700); err != nil {
			t.Fatal(err)
		}
		m := "mmmmmmmm-1111-2222-3333-444444444444"
		x.session("personal", m, "/p/x", "", qNow.Add(-c.since))
		limits := fmt.Sprintf(c.limits, qNow.Add(time.Hour).Unix(), qNow.Add(72*time.Hour).Unix())
		in := []byte(fmt.Sprintf(`{"session_id":%q,"cost":{"total_cost_usd":1,"total_api_duration_ms":900},"rate_limits":{%s}}`, m, limits))
		for at := qNow.Add(-c.since); !at.After(qNow.Add(-time.Minute)); at = at.Add(5 * time.Minute) {
			if _, err := quota.Sample(x.home, "personal", in, at); err != nil {
				t.Fatal(err)
			}
		}
		x.run()
		if v, reason := quota.Room(x.report()); v == "room" {
			t.Errorf("%s: %s %s", c.name, v, reason)
		}
	}
}

// Review of 27cdaa1 (codex P1): a share's age is the age of the measurement that shows it. A session first seen 5 min
// ago carrying an older 80% does not refresh the 95% measured 2 h ago; a 7d-only sample 5 min ago does not refresh a
// 5h 95% measured 2 h ago.
func TestQuotaRoomAgesEachShareByItsOwnMeasurement(t *testing.T) {
	week, five := qNow.Add(72*time.Hour).Unix(), qNow.Add(time.Hour).Unix()
	for _, c := range []struct{ name, old, recent string }{
		{"older reading", fmt.Sprintf(`"five_hour":{"used_percentage":10,"resets_at":%d},"seven_day":{"used_percentage":95,"resets_at":%d}`, five, week),
			fmt.Sprintf(`"five_hour":{"used_percentage":10,"resets_at":%d},"seven_day":{"used_percentage":80,"resets_at":%d}`, five, week)},
		{"older 5h reading", fmt.Sprintf(`"five_hour":{"used_percentage":95,"resets_at":%d},"seven_day":{"used_percentage":40,"resets_at":%d}`, five, week),
			fmt.Sprintf(`"five_hour":{"used_percentage":50,"resets_at":%d},"seven_day":{"used_percentage":40,"resets_at":%d}`, five, week)},
		{"5h borrowed", fmt.Sprintf(`"five_hour":{"used_percentage":95,"resets_at":%d},"seven_day":{"used_percentage":40,"resets_at":%d}`, five, week),
			fmt.Sprintf(`"seven_day":{"used_percentage":40,"resets_at":%d}`, week)},
	} {
		x := quotaCase(t)
		if err := os.MkdirAll(filepath.Join(x.home, "spool"), 0o700); err != nil {
			t.Fatal(err)
		}
		a, b := "aaaaaaaa-1111-2222-3333-444444444444", "bbbbbbbb-1111-2222-3333-444444444444"
		x.session("personal", a, "/p/x", "", qNow.Add(-2*time.Hour))
		x.session("personal", b, "/p/x", "", qNow.Add(-5*time.Minute))
		for _, r := range []struct {
			sid, limits string
			at          time.Time
		}{{a, c.old, qNow.Add(-2 * time.Hour)}, {b, c.recent, qNow.Add(-5 * time.Minute)}} {
			in := fmt.Sprintf(`{"session_id":%q,"cost":{"total_cost_usd":1,"total_api_duration_ms":900},"rate_limits":{%s}}`, r.sid, r.limits)
			if _, err := quota.Sample(x.home, "personal", []byte(in), r.at); err != nil {
				t.Fatal(err)
			}
		}
		x.run()
		if v, reason := quota.Room(x.report()); v == "room" {
			t.Errorf("%s: %s %s", c.name, v, reason)
		}
	}
}

// Crius 23:11: only a line that came with a response measures (a line from before the field counts as it did). A
// session first seen 5 min ago with the same 95% does not make a 95% from 2 h ago fresh; a heartbeat written on a
// response does.
func TestQuotaRoomCountsResponsesOnly(t *testing.T) {
	limits := fmt.Sprintf(`{"five_hour":{"used_percentage":10,"resets_at":%d},"seven_day":{"used_percentage":95,"resets_at":%d}}`,
		qNow.Add(time.Hour).Unix(), qNow.Add(72*time.Hour).Unix())
	a, b := "aaaaaaaa-1111-2222-3333-444444444444", "bbbbbbbb-1111-2222-3333-444444444444"
	for _, c := range []struct {
		name    string
		renders []struct {
			sid string
			usd float64
			at  time.Duration
		}
		want string
	}{
		{"first seen", []struct {
			sid string
			usd float64
			at  time.Duration
		}{{a, 0, -121 * time.Minute}, {a, 1, -120 * time.Minute}, {b, 1, -5 * time.Minute}}, "unknown"},
		{"heartbeat on a response", []struct {
			sid string
			usd float64
			at  time.Duration
		}{{a, 0, -56 * time.Minute}, {a, 1, -55 * time.Minute}, {a, 1, -47 * time.Minute}, {a, 1, -39 * time.Minute},
			{a, 1, -31 * time.Minute}, {a, 2, -24 * time.Minute}}, "room"},
		// review of d25f4f2: no render between, so the response may be 50 min old
		{"response after a render gap", []struct {
			sid string
			usd float64
			at  time.Duration
		}{{a, 0, -56 * time.Minute}, {a, 1, -55 * time.Minute}, {a, 2, -5 * time.Minute}}, "unknown"},
	} {
		x := quotaCase(t)
		if err := os.MkdirAll(filepath.Join(x.home, "spool"), 0o700); err != nil {
			t.Fatal(err)
		}
		x.session("personal", a, "/p/x", "", qNow.Add(-2*time.Hour))
		x.session("personal", b, "/p/x", "", qNow.Add(-5*time.Minute))
		for _, r := range c.renders {
			in := fmt.Sprintf(`{"session_id":%q,"cost":{"total_cost_usd":%v,"total_api_duration_ms":%v}`, r.sid, r.usd, r.usd*1000)
			if r.usd > 0 {
				in += `,"rate_limits":` + limits
			}
			if _, err := quota.Sample(x.home, "personal", []byte(in+"}"), qNow.Add(r.at)); err != nil {
				t.Fatal(err)
			}
		}
		x.run()
		if v, reason := quota.Room(x.report()); v != c.want {
			t.Errorf("%s: %s %s", c.name, v, reason)
		}
	}

	// A line from before the mark counts as it did: a session line is a measurement, a heartbeat is not.
	x := quotaCase(t)
	x.session("personal", a, "/p/x", "", qNow.Add(-2*time.Hour))
	seven, five, week, fr := 95.0, 10.0, qNow.Add(72*time.Hour).Unix(), qNow.Add(time.Hour).Unix()
	old := func(kind string, at time.Duration) quota.Line {
		return quota.Line{TS: iso(qNow.Add(at)), Kind: kind, Session: a, Persona: "personal", RL: true, SevenPct: &seven,
			SevenReset: &week, FivePct: &five, FiveReset: &fr}
	}
	x.spool(old("session", -2*time.Hour), old("heartbeat", -5*time.Minute))
	x.run()
	if v, reason := quota.Room(x.report()); v != "unknown" || !strings.Contains(reason, "120 min") && !strings.Contains(reason, "2 h") {
		t.Errorf("old lines: %s %s", v, reason)
	}
	x.spool(old("change", -3*time.Minute))
	x.run()
	if v, reason := quota.Room(x.report()); v != "room" {
		t.Errorf("old change line: %s %s", v, reason)
	}
}

// org appends the transcript line that names the session's organization (Claude Code's credential_org attachment).
func (x *ic) org(persona, sid, id string, at time.Time) {
	x.t.Helper()
	root := x.personal
	if persona == "work" {
		root = x.work
	}
	appendFile(x.t, filepath.Join(root, "-p", sid+".jsonl"), lines(map[string]any{"type": "attachment", "uuid": "o-" + sid,
		"timestamp": iso(at), "attachment": map[string]any{"type": "credential_org", "organizationUuid": id}}))
}

// Crius 10-17 07:17 (P6 a): a session the statusline never saw takes its account from the organization its
// transcript names, learned from the sessions the statusline did see; one that names none stays unknown.
func TestQuotaAccountFromTheTranscriptsOrganization(t *testing.T) {
	x := quotaCase(t)
	ent, onMax, onEnt, none := "e0000000-2222-3333-4444-555555555555", "a1000000-2222-3333-4444-555555555555",
		"b1000000-2222-3333-4444-555555555555", "c1000000-2222-3333-4444-555555555555"
	x.session("personal", x.sid, "/work/demo", "", qNow.Add(-10*time.Minute))
	x.session("work", ent, "/work/axi", "", qNow.Add(-20*time.Minute))
	closed := qNow.Add(-2 * time.Hour) // not open: only the account is in question, not a stale sampler
	x.session("work", onMax, "/work/demo", "", closed)
	x.session("work", onEnt, "/work/axi", "", closed)
	x.session("work", none, "/work/demo", "", closed)
	x.org("personal", x.sid, "org-max", qNow.Add(-11*time.Minute))
	x.org("work", ent, "org-ent", qNow.Add(-21*time.Minute))
	x.org("work", onMax, "org-max", closed.Add(-time.Minute))
	x.org("work", onEnt, "org-ent", closed.Add(-time.Minute))
	x.spool(sampleAt(x.sid, qNow.Add(-5*time.Minute), 20, qNow.Add(4*24*time.Hour)),
		quota.Line{TS: iso(qNow.Add(-19 * time.Minute)), Kind: "session", Session: ent, Persona: "work"})
	x.run()
	rep := x.report()
	one := rep.UnknownUSD
	if rep.UnknownSessions != 1 || one == 0 || math.Abs(rep.MaxUSD-2*one) > 1e-9 || math.Abs(rep.EntWeekUSD-2*one) > 1e-9 {
		t.Fatalf("unknown %d sessions $%v, max $%v, enterprise $%v; want 1 unknown, 2 each", rep.UnknownSessions, one,
			rep.MaxUSD, rep.EntWeekUSD)
	}
}

// An organization the statusline saw on both accounts tells nothing: its sessions stay unknown, and the report
// says why.
func TestQuotaOrganizationOnBothAccountsIsLoud(t *testing.T) {
	x := quotaCase(t)
	ent, other := "e0000000-2222-3333-4444-555555555555", "a1000000-2222-3333-4444-555555555555"
	x.session("personal", x.sid, "/work/demo", "", qNow.Add(-10*time.Minute))
	x.session("work", ent, "/work/axi", "", qNow.Add(-20*time.Minute))
	x.session("work", other, "/work/demo", "", qNow.Add(-2*time.Hour))
	for _, s := range []string{x.sid, ent, other} {
		x.org("work", s, "org-both", qNow.Add(-3*time.Hour))
	}
	x.spool(sampleAt(x.sid, qNow.Add(-5*time.Minute), 20, qNow.Add(4*24*time.Hour)),
		quota.Line{TS: iso(qNow.Add(-19 * time.Minute)), Kind: "session", Session: ent, Persona: "work"})
	x.run()
	rep := x.report()
	if rep.UnknownSessions != 1 || !strings.Contains(strings.Join(rep.Reasons, "\n"), "organization org-both seen on both accounts") {
		t.Fatalf("unknown %d, reasons %q", rep.UnknownSessions, rep.Reasons)
	}
}

// Live store 10-17: a login switch puts the statusline's first Max line 14 s before the transcript's new organization
// line. A line that close to a switch is not paired with either organization, so the switch is not a conflict.
func TestQuotaOrganizationSwitchIsNotAConflict(t *testing.T) {
	x := quotaCase(t)
	switched, other := "e0000000-2222-3333-4444-555555555555", "b1000000-2222-3333-4444-555555555555"
	x.session("work", switched, "/work/axi", "", qNow.Add(-60*time.Minute), qNow.Add(-10*time.Minute))
	x.session("work", other, "/work/axi", "", qNow.Add(-3*time.Hour))
	x.org("work", switched, "org-ent", qNow.Add(-61*time.Minute))
	x.org("work", other, "org-ent", qNow.Add(-3*time.Hour).Add(-time.Minute))
	x.spool(quota.Line{TS: iso(qNow.Add(-59 * time.Minute)), Kind: "session", Session: switched, Persona: "work"},
		sampleAt(switched, qNow.Add(-30*time.Minute), 20, qNow.Add(4*24*time.Hour)),
		sampleAt(switched, qNow.Add(-5*time.Minute), 20, qNow.Add(4*24*time.Hour)))
	appendFile(t, filepath.Join(x.work, "-p", switched+".jsonl"), lines(map[string]any{"type": "attachment",
		"uuid": "o2", "timestamp": iso(qNow.Add(-30*time.Minute + 14*time.Second)),
		"attachment": map[string]any{"type": "credential_org", "organizationUuid": "org-max"}}))
	x.run()
	rep := x.report()
	if rep.UnknownSessions != 0 || len(grepAlerts(rep.Reasons, "both accounts")) != 0 {
		t.Fatalf("unknown %d, reasons %q", rep.UnknownSessions, rep.Reasons)
	}
}

// Review of 8c8d844 P1: a session known to be on the enterprise account only from its organization is guarded like
// one the statusline saw; before, it was "not measured", and the organization must not turn that into "none".
func TestQuotaEnterpriseByOrganizationIsGuarded(t *testing.T) {
	x := quotaCase(t)
	ent, demo := "e0000000-2222-3333-4444-555555555555", "d1000000-2222-3333-4444-555555555555"
	x.session("work", ent, "/work/axi", "", qNow.Add(-20*time.Minute))
	x.session("work", demo, "/work/demo", "", qNow.Add(-2*time.Hour))
	x.org("work", ent, "org-ent", qNow.Add(-21*time.Minute))
	x.org("work", demo, "org-ent", qNow.Add(-2*time.Hour).Add(-time.Minute))
	x.spool(quota.Line{TS: iso(qNow.Add(-19 * time.Minute)), Kind: "session", Session: ent, Persona: "work"})
	x.run()
	rep := x.report()
	if len(rep.Violations) != 1 || rep.Violations[0].Session != demo || rep.Violations[0].Kind != "s1" ||
		rep.Violations[0].Source != "organization" {
		t.Fatalf("violations %+v", rep.Violations)
	}
}

// Review of 8c8d844 P2: a resumed session can name its organization days after its first requests (the line is
// new in Claude Code); requests more than Grace before the first organization line do not take it.
func TestQuotaRequestsLongBeforeTheFirstOrganizationLineStayUnknown(t *testing.T) {
	x := quotaCase(t)
	ent, resumed := "e0000000-2222-3333-4444-555555555555", "b1000000-2222-3333-4444-555555555555"
	x.session("work", ent, "/work/axi", "", qNow.Add(-20*time.Minute))
	x.session("work", resumed, "/work/axi", "", qNow.Add(-5*time.Hour), qNow.Add(-2*time.Hour))
	x.org("work", ent, "org-ent", qNow.Add(-21*time.Minute))
	x.org("work", resumed, "org-ent", qNow.Add(-2*time.Hour).Add(-time.Minute))
	x.spool(quota.Line{TS: iso(qNow.Add(-19 * time.Minute)), Kind: "session", Session: ent, Persona: "work"})
	x.run()
	rep := x.report()
	if rep.UnknownSessions != 1 || rep.UnknownUSD == 0 || math.Abs(rep.EntWeekUSD-2*rep.UnknownUSD) > 1e-9 {
		t.Fatalf("unknown %d $%v, enterprise $%v; want the request before the line unknown", rep.UnknownSessions,
			rep.UnknownUSD, rep.EntWeekUSD)
	}
}

// Re-review of 4d1f810 P2: the organization is measured, a launch tag is only said, so a max tag does not move a
// session logged into the enterprise organization off it: billing and the guard agree.
func TestQuotaMaxTagDoesNotHideAnEnterpriseOrganization(t *testing.T) {
	x := quotaCase(t)
	ent, tagged := "e0000000-2222-3333-4444-555555555555", "d1000000-2222-3333-4444-555555555555"
	x.session("work", ent, "/work/axi", "", qNow.Add(-20*time.Minute))
	x.session("work", tagged, "/work/axi", "[crius account=max project=demo role=lead]", qNow.Add(-2*time.Hour))
	x.org("work", ent, "org-ent", qNow.Add(-21*time.Minute))
	x.org("work", tagged, "org-ent", qNow.Add(-2*time.Hour).Add(-time.Minute))
	x.spool(quota.Line{TS: iso(qNow.Add(-19 * time.Minute)), Kind: "session", Session: ent, Persona: "work"})
	x.run()
	rep := x.report()
	if rep.MaxUSD != 0 || rep.EntWeekUSD == 0 || len(rep.Violations) != 1 || rep.Violations[0].Session != tagged ||
		rep.Violations[0].Source != "organization" {
		t.Fatalf("max $%v, enterprise $%v, violations %+v", rep.MaxUSD, rep.EntWeekUSD, rep.Violations)
	}
}

// Crius 10-17 07:41: a guard alert for a session that has ended does not say "stop it": it says when it ended.
func TestQuotaGuardAlertForAnEndedSessionSaysItEnded(t *testing.T) {
	x := quotaCase(t)
	live, ended := "33333333-2222-3333-4444-555555555555", "44444444-2222-3333-4444-555555555555"
	tag := "[crius account=enterprise project=demo role=lead]"
	x.session("work", live, "/work/demo", tag, qNow.Add(-6*time.Hour), qNow.Add(-10*time.Minute)) // open: its last request counts
	x.session("work", ended, "/work/demo", tag, qNow.Add(-5*time.Hour))
	x.run()
	l, e := grepAlerts(x.alerts(), "session 33333333"), grepAlerts(x.alerts(), "session 44444444")
	if len(l) != 1 || !strings.Contains(l[0], "Stop it") || len(e) != 1 || strings.Contains(e[0], "Stop it") ||
		!strings.Contains(e[0], "ended "+qNow.Add(-5*time.Hour).Local().Format("Mon 01-02 15:04")) {
		t.Fatalf("live %q\nended %q", l, e)
	}
}

// Light review of 58d168a: a session known only from the statusline (no transcript read) ends by its newest line.
func TestQuotaEndedSessionWithoutTranscriptSaysItEnded(t *testing.T) {
	x := quotaCase(t)
	at := qNow.Add(-5 * time.Hour)
	x.spool(quota.Line{TS: iso(at), Kind: "session", Session: "66666666-x", Persona: "work"})
	x.run()
	if len(grepAlerts(x.alerts(), "enterprise session 66666666 (ended "+at.Local().Format("Mon 01-02 15:04")+")")) != 1 {
		t.Fatalf("got %q", x.alerts())
	}
}

const guardedProject = `
[projects.quotes]
persona = "work"
accounts = ["max"]
cwd = ["/work/quotes"]
pause_at = 70
`

func (x *ic) bus(project string) []string {
	x.t.Helper()
	b, err := os.ReadFile(filepath.Join(x.home, "bus", project+".log"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		x.t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// Crius 10-22 20:38 (owner: Quotes pauses at 70% of the weekly quota): a project with pause_at gets one PAUSE on its
// own bus when the Max 7d share reaches it, and Crius one NEED, which wakes it (an ALERT does not). Once per window.
func TestQuotaGuardPausesAProjectAtItsShareAndWakesCrius(t *testing.T) {
	clock = func() time.Time { return qNow }
	t.Cleanup(func() { clock = time.Now })
	x := newIC(t, cfg{extra: axiProject + guardedProject})
	reset := qNow.Add(4 * 24 * time.Hour)
	x.session("personal", x.sid, "/work/demo", "", qNow.Add(-20*time.Minute))
	x.spool(sampleAt(x.sid, qNow.Add(-30*time.Minute), 69, reset))
	x.run()
	if p := grepAlerts(x.bus("quotes"), "PAUSE"); len(p) != 0 {
		t.Fatalf("69%% is under 70%%: %q", p)
	}
	x.spool(sampleAt(x.sid, qNow.Add(-10*time.Minute), 71, reset))
	x.run()
	x.run()
	p := grepAlerts(x.bus("quotes"), " PAUSE crius:ingest -> quotes:all — ")
	if len(p) != 1 || !strings.Contains(p[0], "71%") || !strings.Contains(p[0], "70%") {
		t.Fatalf("want one PAUSE to quotes:all naming 71%% and 70%%, got %q", x.bus("quotes"))
	}
	need := grepAlerts(x.alerts(), " NEED crius:ingest -> crius:lead — ")
	if len(need) != 1 || !strings.Contains(need[0], "quotes") || !strings.Contains(need[0], "71%") {
		t.Fatalf("want one NEED to crius:lead naming quotes and 71%%, got %q", x.alerts())
	}
	if x.bus("axi") != nil {
		t.Fatalf("axi has no pause_at: %q", x.bus("axi"))
	}
	// The next weekly window pauses again.
	x.spool(sampleAt(x.sid, qNow.Add(-5*time.Minute), 72, reset.Add(7*24*time.Hour)))
	x.run()
	if p := grepAlerts(x.bus("quotes"), " PAUSE "); len(p) != 2 {
		t.Fatalf("want a second PAUSE in the next window, got %q", x.bus("quotes"))
	}
}

// The guard cannot tell a project under its share from a sampler that stopped: a stale sample while sessions are
// open is a NEED that wakes Crius when some project has pause_at (the stopgap exits 1), once per gap.
func TestQuotaGuardThatCannotSeeTheShareWakesCrius(t *testing.T) {
	clock = func() time.Time { return qNow }
	t.Cleanup(func() { clock = time.Now })
	x := newIC(t, cfg{extra: axiProject + guardedProject})
	x.session("personal", x.sid, "/work/demo", "", qNow.Add(-10*time.Minute))
	x.spool(sampleAt(x.sid, qNow.Add(-50*time.Minute), 20, qNow.Add(4*24*time.Hour)))
	x.run()
	x.run()
	need := grepAlerts(x.alerts(), " NEED crius:ingest -> crius:lead — ")
	if len(need) != 1 || !strings.Contains(need[0], "quotes") || !strings.Contains(need[0], "blind") {
		t.Fatalf("want one NEED saying the quotes guard is blind, got %q", x.alerts())
	}
	// Without pause_at the stale sample stays an ALERT only (TestQuotaOpenSessionsWithNoRecentSampleAreStale).
	y := quotaCase(t)
	y.session("personal", y.sid, "/work/demo", "", qNow.Add(-10*time.Minute))
	y.spool(sampleAt(y.sid, qNow.Add(-50*time.Minute), 20, qNow.Add(4*24*time.Hour)))
	y.run()
	if n := grepAlerts(y.alerts(), " NEED "); len(n) != 0 {
		t.Fatalf("no guarded project, no NEED: %q", n)
	}
}

func TestQuotaGuardShareMustBeAPercent(t *testing.T) {
	for _, v := range []string{"0", "101", "-5", `"70%"`} {
		home := t.TempDir()
		testutil.Write(t, filepath.Join(home, "projects.toml"),
			"[projects.quotes]\naccounts = [\"max\"]\ncwd = [\"/q\"]\npause_at = "+v+"\n")
		if _, err := loadConfig(home); err == nil || !strings.Contains(err.Error(), "pause_at") {
			t.Errorf("pause_at = %s: err %v, want one naming pause_at", v, err)
		}
	}
	home := t.TempDir()
	testutil.Write(t, filepath.Join(home, "projects.toml"),
		"[projects.quotes]\naccounts = [\"max\"]\ncwd = [\"/q\"]\npause_at = 70\n")
	if c, err := loadConfig(home); err != nil || c.projects[0].pauseAt != 70 {
		t.Fatalf("pause_at = 70: %v", err)
	}
}

// Samples of one weekly window carry resets seconds apart (quota.avgPerDay): a later sample's reset is the same window,
// so the guard and the used-up push say it once (review round 13).
func TestQuotaKeysOneWindowWhateverItsResetSecond(t *testing.T) {
	clock = func() time.Time { return qNow }
	t.Cleanup(func() { clock = time.Now })
	x := newIC(t, cfg{extra: axiProject + guardedProject})
	reset := qNow.Add(4 * 24 * time.Hour)
	x.session("personal", x.sid, "/work/demo", "", qNow.Add(-20*time.Minute))
	x.spool(sampleAt(x.sid, qNow.Add(-15*time.Minute), 71, reset))
	x.run()
	x.spool(sampleAt(x.sid, qNow.Add(-10*time.Minute), 100, reset.Add(3*time.Second)))
	x.run()
	x.spool(sampleAt(x.sid, qNow.Add(-5*time.Minute), 100, reset.Add(5*time.Second)))
	x.run()
	if p := grepAlerts(x.bus("quotes"), " PAUSE "); len(p) != 1 {
		t.Errorf("want one PAUSE in one window, got %q", x.bus("quotes"))
	}
	if n := grepAlerts(x.alerts(), " NEED crius:ingest -> crius:lead — quota guard:"); len(n) != 1 {
		t.Errorf("want one NEED in one window, got %q", x.alerts())
	}
	if u := grepAlerts(x.alerts(), "quota used up"); len(u) != 1 {
		t.Errorf("want one used-up push in one window, got %q", x.alerts())
	}
}

// A bus that cannot be written holds back its own lines and the lines that say they were posted, not the other buses:
// Crius still gets its used-up push while quotes.log is broken, and the PAUSE and its NEED follow once it is fixed
// (review round 13).
func TestABrokenBusHoldsBackOnlyItsOwnLines(t *testing.T) {
	clock = func() time.Time { return qNow }
	t.Cleanup(func() { clock = time.Now })
	x := newIC(t, cfg{extra: axiProject + guardedProject})
	broken := filepath.Join(x.home, "bus", "quotes.log")
	if err := os.MkdirAll(broken, 0o700); err != nil {
		t.Fatal(err)
	}
	x.session("personal", x.sid, "/work/demo", "", qNow.Add(-20*time.Minute))
	x.spool(sampleAt(x.sid, qNow.Add(-10*time.Minute), 100, qNow.Add(4*24*time.Hour)))
	x.run()
	if u := grepAlerts(x.alerts(), "quota used up"); len(u) != 1 {
		t.Fatalf("a broken quotes.log held back Crius's used-up push: %q", x.alerts())
	}
	if n := grepAlerts(x.alerts(), "PAUSE posted"); len(n) != 0 {
		t.Fatalf("the NEED says the PAUSE was posted while quotes.log is broken: %q", n)
	}
	if err := os.Remove(broken); err != nil {
		t.Fatal(err)
	}
	x.run()
	if p, n := grepAlerts(x.bus("quotes"), " PAUSE "), grepAlerts(x.alerts(), "PAUSE posted"); len(p) != 1 || len(n) != 1 {
		t.Fatalf("after the fix want one PAUSE and one NEED, got %q and %q", p, n)
	}
}
