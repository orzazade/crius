package ingest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/quota"
	"github.com/orzazade/crius/internal/sessionstate"
	"github.com/orzazade/crius/internal/testutil"
)

// T3 Code threads run no statusline, so crius-statusline writes no quota sample for them (11-21: max2 read "room" for
// 6 h on an old sample while T3 threads spent it). T3 logs each rate_limit_event Claude Code sends; ingest reads them.
// The fixtures copy the shape of T3's provider log lines only: every id and value is made up.

// t3Logs is the provider log folder of this test's T3 home (testutil.Temp points T3CODE_HOME at a temp folder).
func t3Logs(t *testing.T) string {
	t.Helper()
	h := os.Getenv("T3CODE_HOME")
	if real, _ := os.UserHomeDir(); h == "" || samePathT(h, filepath.Join(real, ".t3")) {
		t.Fatalf("T3CODE_HOME %q is not a test folder: the test would read or write the real T3 logs", h)
	}
	return filepath.Join(h, "userdata", "logs", "provider")
}

func samePathT(a, b string) bool {
	ra, _ := filepath.EvalSymlinks(a)
	rb, _ := filepath.EvalSymlinks(b)
	return filepath.Clean(a) == filepath.Clean(b) || ra != "" && ra == rb
}

// t3Event is one provider log line as T3 writes it for a Claude Code rate_limit_event of a session run by instance inst.
func t3Event(at time.Time, inst, sid, uuid string, util float64, reset time.Time) string {
	return fmt.Sprintf(`[%s] NTIVE: {"provider":"claudeAgent","protocol":"claude-agent-sdk.query","kind":"protocol",`+
		`"providerSessionId":"provider-session:provider-instance:%s:thread:mcp%%3Afake:%s","event":{"direction":"incoming",`+
		`"stage":"decoded","payload":{"type":"rate_limit_event",`+
		`"rate_limit_info":{"status":"allowed","resetsAt":%d,"rateLimitType":"seven_day","overageStatus":"rejected",`+
		`"overageDisabledReason":"fake","isUsingOverage":false,"unifiedWindows":{"seven_day":{"utilization":%v,"resetsAt":%d,`+
		`"status":"allowed"}}},"uuid":"%s","session_id":"%s"}}}`+"\n",
		at.UTC().Format("2006-01-02T15:04:05.000Z"), inst, sid, reset.Unix(), util, reset.Unix(), uuid, sid)
}

// hookRecord writes the crius-hook record of a session: the config folder its process runs in and its T3 thread.
func (x *ic) hookRecord(sid, config, thread string) {
	x.t.Helper()
	b, err := json.Marshal(sessionstate.State{SessionID: sid, State: "working", Event: "Stop", Time: qNow.Add(-time.Minute),
		Config: config, Thread: thread})
	if err != nil {
		x.t.Fatal(err)
	}
	testutil.Write(x.t, filepath.Join(x.home, "sessions", sid+".json"), string(b))
}

// t3Settings writes T3's settings.json with Claude provider instances: id -> config folder.
func t3Settings(t *testing.T, insts map[string]string) {
	t.Helper()
	all := map[string]any{"i-codex": map[string]any{"driver": "codex"}}
	for id, home := range insts {
		all[id] = map[string]any{"driver": "claudeAgent", "config": map[string]string{"homePath": home}}
	}
	b, err := json.Marshal(map[string]any{"providerInstances": all})
	if err != nil {
		t.Fatal(err)
	}
	testutil.Write(t, filepath.Join(filepath.Dir(filepath.Dir(t3Logs(t))), "settings.json"), string(b))
}

func sid(n int) string { return fmt.Sprintf("aaaaaaaa-0000-4000-8000-%012d", n) }

// t3Case: accounts max1, max2, an enterprise row and max3 (its folder signed into no login); max2's newest statusline
// sample 6 h old at 12%; T3 logs (one rotated) with samples of sessions on max1 and max2, and one sample of each kind no
// record places. accounts is the accounts.toml text.
func t3Case(t *testing.T) (x *ic, l1, l2 string, reset time.Time, accounts string) {
	x = quotaCase(t)
	dir := func(n string) string { return filepath.Join(x.root, "cfg-"+n) }
	d1, d2, other, ent, nologin := dir("max1"), dir("max2"), dir("other"), dir("ent"), dir("nologin")
	l1, l2 = x.signIn(d1, "u-fake-1"), x.signIn(d2, "u-fake-2")
	x.signIn(other, "u-fake-3")
	x.signIn(ent, "u-fake-4")
	testutil.Write(t, filepath.Join(nologin, "settings.json"), "{}")
	row := func(name, plan, d string) string {
		return "[[account]]\nname = \"" + name + "\"\nplan = \"" + plan + "\"\ndirs = [\"" + d + "\"]\n"
	}
	accounts = row("max1", "max", d1) + row("max2", "max", d2) + row("ent", "enterprise", ent) + row("max3", "max", nologin)
	testutil.Write(t, filepath.Join(x.home, "accounts.toml"), accounts)
	t3Settings(t, map[string]string{"i-max1": d1, "i-max2": d2, "i-other": other, "i-ent": ent, "i-nologin": nologin})
	reset = qNow.Add(3 * 24 * time.Hour)
	old := sampleAt("statusline-s", qNow.Add(-6*time.Hour), 12, reset)
	old.Login, old.Dir = l2, d2
	x.spool(old)
	x.hookRecord(sid(1), d1, "thread:fake-1")
	x.hookRecord(sid(2), d2, "thread:fake-2")
	// sid(3): no record
	x.hookRecord(sid(4), other, "thread:fake-4")
	x.hookRecord(sid(5), d2, "") // its newest record is outside T3: it moved
	x.hookRecord(sid(6), d2, "thread:fake-6")
	x.hookRecord(sid(7), ent, "thread:fake-7")
	x.hookRecord(sid(8), nologin, "thread:fake-8")
	testutil.Write(t, filepath.Join(x.home, "sessions", sid(9)+".json"), "{not json")
	x.hookRecord(sid(10), d1, "thread:fake-10")
	logs := t3Logs(t)
	testutil.Write(t, filepath.Join(logs, "events.thread-fake.log.1"),
		t3Event(qNow.Add(-40*time.Minute), "i-max1", sid(1), "e-0001", 0.4, reset)+
			t3Event(qNow.Add(-30*time.Minute), "i-max2", sid(2), "e-0002", 0.14, reset)+
			strings.Replace(t3Event(qNow.Add(-35*time.Minute), "i-max1", sid(1), "", 0.4, reset), `"uuid":""`, `"uuid":null`, 1)+
			`[2026-10-16T11:00:00.000Z] NTIVE: {"provider":"claudeAgent","event":{"payload":{"type":"assistant",`+
			`"text":"rate_limit_event is only a word here"}}}`+"\n")
	five := `[2026-10-16T11:56:00.000Z] NTIVE: {"providerSessionId":"provider-session:provider-instance:i-max2:thread:x",` +
		`"event":{"payload":{"type":"rate_limit_event","rate_limit_info":{"rateLimitType":"five_hour","unifiedWindows":` +
		`{"five_hour":{"utilization":0.5,"resetsAt":1}}},"uuid":"e-0011","session_id":"` + sid(2) + `"}}}` + "\n"
	at := qNow.Add(-4 * time.Minute)
	testutil.Write(t, filepath.Join(logs, "events.thread-fake.log"),
		t3Event(qNow.Add(-5*time.Minute), "i-max2", sid(2), "e-0003", 0.15, reset)+five+
			t3Event(at, "i-max2", sid(3), "e-0004", 0.9, reset)+
			t3Event(at, "i-other", sid(4), "e-0005", 0.9, reset)+
			t3Event(at, "i-max2", sid(5), "e-0006", 0.9, reset)+
			t3Event(at, "i-max1", sid(6), "e-0007", 0.9, reset)+ // the record says max2, the line's instance max1
			t3Event(at, "i-ent", sid(7), "e-0008", 0.9, reset)+
			t3Event(at, "i-nologin", sid(8), "e-0009", 0.9, reset)+
			t3Event(at, "i-max2", sid(9), "e-0010", 0.9, reset)+
			t3Event(at, "i-gone", sid(10), "e-0012", 0.9, reset)+
			strings.Replace(t3Event(at, "x", sid(1), "e-0013", 0.9, reset), "provider-session:provider-instance:x:", "other:", 1))
	return x, l1, l2, reset, accounts
}

func measured(rep *quota.Report, login string) string {
	for _, a := range rep.Accounts {
		if a.Login == login {
			return fmt.Sprintf("%s %.0f%% at %s", a.Name, a.SevenMeasuredPct, iso(a.SevenMeasuredAt))
		}
	}
	return "no samples"
}

// t3Said is the warnings about T3 samples.
func t3Said(st *Stats) []string { return grepAlerts(st.Warnings, "T3 quota") }

const t3Skips = "map[T3 instance and crius-hook record on two accounts:1 T3 provider instance not in T3's settings:1" +
	" folder not in accounts.toml:1 folder of a row that is not Max:1 folder signed into no login:1" +
	" line names no T3 provider instance:1 newest crius-hook record is outside T3:1 no crius-hook record:1" +
	" not a seven-day event:1 unreadable crius-hook record:1]"

// Red first: before the reader, max2's share is the 6 h old 12% and max1 has no sample at all.
func TestT3RateLimitEventsAreSamplesOfTheHookFoldersLogin(t *testing.T) {
	x, l1, l2, _, _ := t3Case(t)
	st := x.run()
	rep := x.report()
	if got, want := measured(rep, l2), "max2 15% at "+iso(qNow.Add(-5*time.Minute)); got != want {
		t.Errorf("max2 share: got %q, want %q (the T3 log's newest line)", got, want)
	}
	if got, want := measured(rep, l1), "max1 40% at "+iso(qNow.Add(-35*time.Minute)); got != want {
		t.Errorf("max1 share: got %q, want %q (from the rotated log)", got, want)
	}
	// The guard reads it: max2's verdict comes from the 5 min old T3 sample, not the 6 h old statusline one.
	if v, why := quota.Room(rep.Account(l2)); v != "room" || !strings.Contains(why, "7d 15%") || !strings.Contains(why, "sample 5 min old") {
		t.Errorf("max2 room: %s, %s; want room from the 5 min old 15%% T3 sample", v, why)
	}
	// Only samples both records place are filed (the one with no uuid too); the others are counted by reason.
	eq(t, x.q("select session, login, seven_day_pct from quota where sample_kind = 't3' order by ts"), [][]string{
		{sid(1), l1, "40"}, {sid(1), l1, "40"}, {sid(2), l2, "14"}, {sid(2), l2, "15"}})
	eq(t, x.q("select count(*) from quota q join quota_response r using (source_key) where q.sample_kind = 't3'"+
		" and r.responded = 1"), [][]string{{"4"}})
	if st.T3Samples != 4 {
		t.Errorf("T3Samples = %d, want 4", st.T3Samples)
	}
	eq(t, fmt.Sprint(st.T3Skipped), t3Skips)
	if said := t3Said(st); len(said) != 2 || !strings.Contains(said[1], "4 samples filed, 10 skipped") ||
		!strings.Contains(said[0], sid(9)) {
		t.Errorf("want the unreadable record named and one warning naming the 10 skipped T3 samples, got %q", said)
	}
	if st.BadLines != 0 { // (the run is partial: the limit check says the damaged record too)
		t.Errorf("skipped samples are counted, not bad lines: bad %d, %q", st.BadLines, st.Warnings)
	}
}

// A second run reads nothing twice, and a rotation (each file renamed one up, a new .log) reads only the new file: the
// old lines are neither filed nor counted as skipped again.
func TestT3QuotaRereadAndRotationAddNoDuplicates(t *testing.T) {
	x, _, l2, reset, _ := t3Case(t)
	if st := x.run(); st.T3Samples != 4 {
		t.Fatalf("first run: %d samples, want 4", st.T3Samples)
	}
	if st := x.run(); st.T3Samples != 0 || len(st.T3Skipped) != 0 {
		t.Errorf("second run: %d samples, skipped %v; want none", st.T3Samples, st.T3Skipped)
	}
	logs := t3Logs(t)
	for _, mv := range [][2]string{{".log.1", ".log.2"}, {".log", ".log.1"}} {
		if err := os.Rename(filepath.Join(logs, "events.thread-fake"+mv[0]), filepath.Join(logs, "events.thread-fake"+mv[1])); err != nil {
			t.Fatal(err)
		}
	}
	testutil.Write(t, filepath.Join(logs, "events.thread-fake.log"), t3Event(qNow.Add(-time.Minute), "i-max2", sid(2), "e-0020", 0.16, reset))
	st := x.run()
	if st.T3Samples != 1 || len(st.T3Skipped) != 0 {
		t.Errorf("after rotation: %d new samples, skipped %v; want 1 and none", st.T3Samples, st.T3Skipped)
	}
	eq(t, x.q("select count(*), max(seven_day_pct) from quota where sample_kind = 't3' and login = ?", l2), [][]string{{"3", "16"}})
	eq(t, x.q("select count(*) from quota where sample_kind = 't3'"), [][]string{{"5"}})
	if got, want := measured(x.report(), l2), "max2 16% at "+iso(qNow.Add(-time.Minute)); got != want {
		t.Errorf("max2 share: got %q, want %q", got, want)
	}
}

// A rate_limit_event without the seven-day share and no other window named is a changed log shape: a bad line, said,
// and the run partial; never 0%.
func TestT3RateLimitEventWithoutTheSevenDayShareIsBad(t *testing.T) {
	x, _, _, _, _ := t3Case(t)
	appendFile(t, filepath.Join(t3Logs(t), "events.thread-fake.log"), `[2026-10-16T11:59:00.000Z] NTIVE: {"event":{"payload":`+
		`{"type":"rate_limit_event","rate_limit_info":{"unifiedWindows":{"seven_day":{"used":0.5}}},"uuid":"e-1",`+
		`"session_id":"`+sid(2)+`"}}}`+"\n")
	st := x.run()
	eq(t, x.q("select count(*) from quota where sample_kind = 't3'"), [][]string{{"4"}})
	if st.BadLines != 1 || len(grepAlerts(st.Warnings, "bad T3 rate_limit_event")) != 1 || st.Status != "partial" {
		t.Errorf("want one bad line said and a partial run, got bad=%d status=%s warnings %q", st.BadLines, st.Status, st.Warnings)
	}
}

// With no account listed no sample can be placed: the run says so and is partial, and the lines wait for the file
// (review P2: a missing accounts.toml skipped every sample for good and the run read complete).
func TestT3QuotaWaitsForTheAccountsFile(t *testing.T) {
	x, _, _, _, accounts := t3Case(t)
	if err := os.Remove(filepath.Join(x.home, "accounts.toml")); err != nil {
		t.Fatal(err)
	}
	st := x.run()
	if st.T3Samples != 0 || st.Status != "partial" || len(grepAlerts(st.Warnings, "lists no account")) != 1 {
		t.Errorf("no accounts.toml: %d samples, status %s, warnings %q; want 0, partial, said", st.T3Samples, st.Status, st.Warnings)
	}
	testutil.Write(t, filepath.Join(x.home, "accounts.toml"), accounts)
	if st := x.run(); st.T3Samples != 4 {
		t.Errorf("after accounts.toml is back: %d samples, want 4", st.T3Samples)
	}
}

// T3 installed but its log folder gone (moved by a T3 update) is said, never read as "no T3 here".
func TestT3QuotaLogFolderMissingIsLoud(t *testing.T) {
	x, _, _, _, _ := t3Case(t)
	if st := x.run(); st.T3Samples == 0 {
		t.Fatal("the first run read no T3 sample")
	}
	if err := os.RemoveAll(t3Logs(t)); err != nil {
		t.Fatal(err)
	}
	st := x.run()
	if len(t3Said(st)) != 1 || st.Status != "partial" {
		t.Errorf("want the missing log folder said and a partial run, got status %s, %q", st.Status, st.Warnings)
	}
	// No T3 home at all: nothing to say.
	if err := os.RemoveAll(os.Getenv("T3CODE_HOME")); err != nil {
		t.Fatal(err)
	}
	if st := x.run(); len(t3Said(st)) != 0 {
		t.Errorf("no T3 home: want no T3 warning, got %q", t3Said(st))
	}
}

// Landing review 11-22: a T3 sample was no account_seen row, so a role that runs on T3 alone still read "last statusline
// line 15:25" in Stale and raised the blind guard NEED, and no role of it was placed on its login.
func TestAT3SampleIsItsSessionsAccountLineToo(t *testing.T) {
	x, _, l2, _, _ := t3Case(t)
	x.session("personal", sid(2), "/work/demo", "", qNow.Add(-6*time.Minute))
	x.session("personal", "statusline-s", "/work/demo", "", qNow.Add(-6*time.Hour))
	x.run()
	if rep := x.report(); len(rep.Stale) != 0 {
		t.Errorf("Stale = %q, want none: max2's newest T3 sample is 5 min old", rep.Stale)
	}
	eq(t, x.q("select login, rl, count(*) from account_seen where session = ?", sid(2)), [][]string{{l2, "1", "2"}})
	eq(t, x.q("select account from session where id = ?", sid(2)), [][]string{{"max"}})
}

// Landing review 11-22: headless sessions (an org line, no statusline line, no hook record) marked every idle login
// "in use since, unmeasured", so an idle account with an older sample was never room. Their organization names the login.
func TestAHeadlessSessionSpendsOnItsOrganizationsLoginOnly(t *testing.T) {
	x, l1, l2, reset, _ := t3Case(t)
	headless, seen := "e0000000-2222-3333-4444-555555555555", "a1000000-2222-3333-4444-555555555555"
	old := qNow.Add(-3 * time.Hour)
	x.session("personal", seen, "/work/demo", "", old)
	x.org("personal", seen, "org-1", old.Add(-time.Minute))
	for _, at := range []time.Time{old.Add(-2 * time.Minute), old} { // l1 before and after the org line
		l := sampleAt(seen, at, 30, reset)
		l.Login = l1
		x.spool(l)
	}
	x.session("personal", headless, "/work/demo", "", qNow.Add(-time.Minute))
	x.org("personal", headless, "org-1", qNow.Add(-2*time.Minute))
	// l2's newest sample is 3 h old and nothing ran on it since: idle, so room. l1's is spent past.
	logs := t3Logs(t)
	if err := os.RemoveAll(logs); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(logs, 0o755); err != nil {
		t.Fatal(err)
	}
	idle := sampleAt("statusline-2", old, 20, reset)
	idle.Login = l2
	x.spool(idle)
	x.run()
	rep := x.report()
	if v, why := quota.Room(rep.Account(l2)); v != "room" {
		t.Errorf("idle max2: %s, %s; want room: the headless request is org-1's, which is max1's", v, why)
	}
	if v, why := quota.Room(rep.Account(l1)); v != "unknown" || !strings.Contains(why, "in use since") {
		t.Errorf("max1: %s, %s; want unknown, in use since: the headless request spent it", v, why)
	}
}

// An organization seen between lines of two logins names neither: its headless request may be on any.
func TestAnOrganizationOfTwoLoginsPlacesNothing(t *testing.T) {
	x, l1, l2, reset, _ := t3Case(t)
	if err := os.RemoveAll(t3Logs(t)); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(t3Logs(t), 0o755); err != nil {
		t.Fatal(err)
	}
	old := qNow.Add(-3 * time.Hour)
	for i, login := range []string{l1, l2} {
		id := fmt.Sprintf("a%d000000-2222-3333-4444-555555555555", i+1)
		x.session("personal", id, "/work/demo", "", old)
		x.org("personal", id, "org-1", old.Add(-time.Minute))
		for _, at := range []time.Time{old.Add(-2 * time.Minute), old} {
			l := sampleAt(id, at, 30, reset)
			l.Login = login
			x.spool(l)
		}
	}
	headless := "e0000000-2222-3333-4444-555555555555"
	x.session("personal", headless, "/work/demo", "", qNow.Add(-time.Minute))
	x.org("personal", headless, "org-1", qNow.Add(-2*time.Minute))
	x.run()
	for _, l := range []string{l1, l2} {
		if v, why := quota.Room(x.report().Account(l)); v != "unknown" {
			t.Errorf("%s: %s, %s; want unknown", l, v, why)
		}
	}
}

// Landing review 11-22: a T3 home with no provider logs yet (installed, no thread ever run) made every run partial, so
// no heartbeat was written and the watchdog alarmed for ever. It is said, and the run is whole. Logs that were read once
// and are gone are still a failed look.
func TestT3QuotaLogsNeverWrittenAreSaidButNoFailedLook(t *testing.T) {
	x := quotaCase(t)
	if err := os.MkdirAll(os.Getenv("T3CODE_HOME"), 0o755); err != nil {
		t.Fatal(err)
	}
	st := x.run()
	if len(t3Said(st)) != 1 || st.Status != "complete" {
		t.Errorf("want the missing log folder said on a complete run, got status %s, %q", st.Status, st.Warnings)
	}
}
