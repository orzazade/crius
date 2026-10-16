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
	eq(t, x.q("select rl from account_seen where session=?", x.sid), [][]string{{"1"}, {"1"}})
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

// Codex round 2 P2: an idle session's heartbeat from the previous epoch is not a sample of the current one.
func TestQuotaOldEpochHeartbeatIsNotACurrentSample(t *testing.T) {
	x := quotaCase(t)
	other := "99999999-2222-3333-4444-555555555555"
	x.session("personal", x.sid, "/work/demo", "", qNow.Add(-10*time.Minute))
	x.spool(sampleAt(x.sid, qNow.Add(-2*time.Hour), 20, qNow.Add(4*24*time.Hour)),
		sampleAt(other, qNow.Add(-5*time.Minute), 90, qNow.Add(-3*24*time.Hour)))
	x.run()
	if rep := x.report(); rep.Status != "stale" || rep.Uncovered == 0 || rep.SevenPct != 20 {
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
	if rep := x.report(); rep.Status != "stale" {
		t.Fatalf("status %s", rep.Status)
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
	if len(grepAlerts(x.alerts(), "quota pace high")) != 1 {
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
