package ingest

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/orzazade/crius/internal/store"
	"github.com/orzazade/crius/internal/testutil"
)

func eq(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("\ngot  %v\nwant %v", got, want)
	}
}

func TestRequestSpanningTwoRunsGivesOneRowWithMaxUsage(t *testing.T) {
	x := newIC(t, cfg{})
	testutil.Write(t, x.tx, lines(assistant(A{Req: "req_1", TS: "2026-10-16T09:00:00Z", Out: 10, CR: 100})))
	x.run()
	appendFile(t, x.tx, lines(assistant(A{Req: "req_1", TS: "2026-10-16T09:00:05Z", Out: 50, CR: 40})))
	x.run()
	rows := x.q("select request_id, session, tok_out, tok_cache_read, ts_first, ts_last from request")
	eq(t, rows, [][]string{{"req_1", x.sid, "50", "100", "2026-10-16T09:00:00Z", "2026-10-16T09:00:05Z"}})
	if c, _ := strconv.ParseFloat(x.one("select cost_list_usd from request"), 64); c <= 0 {
		t.Fatal("no cost")
	}
}

func TestPartialLastLineWaitsForItsNewline(t *testing.T) {
	x := newIC(t, cfg{})
	full := lines(assistant(A{Req: "req_1", TS: "2026-10-16T09:00:00Z"}))
	part := lines(assistant(A{Req: "req_2", TS: "2026-10-16T09:01:00Z"}))
	testutil.Write(t, x.tx, full+part[:40])
	x.run()
	eq(t, x.one("select count(*) from request"), "1")
	appendFile(t, x.tx, part[40:])
	x.run()
	eq(t, x.q("select request_id from request order by 1"), [][]string{{"req_1"}, {"req_2"}})
}

func TestTruncationRescansAndKeepsRows(t *testing.T) {
	x := newIC(t, cfg{})
	a := lines(assistant(A{Req: "req_1", TS: "2026-10-16T09:00:00Z"}))
	b := lines(assistant(A{Req: "req_2", TS: "2026-10-16T09:01:00Z"}))
	testutil.Write(t, x.tx, a+b)
	x.run()
	testutil.Write(t, x.tx, a)
	x.run()
	appendFile(t, x.tx, lines(assistant(A{Req: "req_3", TS: "2026-10-16T09:02:00Z"})))
	x.run()
	eq(t, x.one("select count(*) from request"), "3")
}

func TestReplacementRescansWithoutDuplicates(t *testing.T) {
	x := newIC(t, cfg{})
	a := lines(assistant(A{Req: "req_1", TS: "2026-10-16T09:00:00Z"}), assistant(A{Req: "req_2", TS: "2026-10-16T09:01:00Z"}))
	testutil.Write(t, x.tx, a)
	x.run()
	tmp := x.tx + ".new"
	testutil.Write(t, tmp, a+lines(assistant(A{Req: "req_3", TS: "2026-10-16T09:02:00Z"})))
	if err := os.Rename(tmp, x.tx); err != nil {
		t.Fatal(err)
	}
	x.run()
	eq(t, x.one("select count(*) from request"), "3")
}

func TestLateArrivalStillUpserts(t *testing.T) {
	x := newIC(t, cfg{})
	testutil.Write(t, x.tx, lines(assistant(A{Req: "req_1", TS: "2026-10-16T09:10:00Z", Out: 10})))
	x.run()
	appendFile(t, x.tx, lines(assistant(A{Req: "req_1", TS: "2026-10-16T09:05:00Z", Out: 70}),
		assistant(A{Req: "req_0", TS: "2026-10-15T23:00:00Z"})))
	x.run()
	eq(t, x.q("select ts_first, tok_out from request where request_id='req_1'"), [][]string{{"2026-10-16T09:05:00Z", "70"}})
	eq(t, x.one("select count(*) from request where request_id='req_0'"), "1")
}

func TestSecondRunAddsZeroRows(t *testing.T) {
	x := newIC(t, cfg{})
	testutil.Write(t, x.tx, lines(
		prompt("u1", "2026-10-16T09:00:00Z", "[crius account=max project=demo role=qa item=demo#4]\nx", "", ""),
		assistant(A{Req: "req_1", TS: "2026-10-16T09:00:01Z", Tools: [][2]string{{"t1", "Bash"}, {"t2", "AskUserQuestion"}}}),
		toolResult("u2", "2026-10-16T09:00:02Z", "t1", "[main abc1234] synthetic commit", false),
		compaction("c1", "2026-10-16T09:00:03Z")))
	x.run()
	first := x.counts()
	x.run()
	eq(t, x.counts(), first)
	if first["request"] == "0" || first["event"] == "0" {
		t.Fatal(first)
	}
}

func TestSessionAttributionFromTagThenCwdThenUnknown(t *testing.T) {
	x := newIC(t, cfg{})
	tag := "[crius account=max project=demo role=qa parent=aaaaaaaa-bbbb item=demo#4]"
	testutil.Write(t, x.tx, lines(prompt("u1", "2026-10-16T09:00:00Z", tag+"\nsynthetic", "", "/elsewhere")))
	s2 := "22222222-2222-3333-4444-555555555555"
	testutil.Write(t, filepath.Join(x.personal, "-work-demo", s2+".jsonl"), lines(prompt("u2", "2026-10-16T09:00:00Z", "", "", "/work/demo/sub")))
	s3 := "33333333-2222-3333-4444-555555555555"
	testutil.Write(t, filepath.Join(x.work, "-x", s3+".jsonl"), lines(prompt("u3", "2026-10-16T09:00:00Z", "", "", "/other")))
	x.run()
	eq(t, x.q("select id, account, project, role, item, parent_id, attrib_source, persona from session order by id"), [][]string{
		{x.sid, "max", "demo", "qa", "demo#4", "aaaaaaaa-bbbb", "registered", "personal"},
		{s2, "unknown", "demo", "unknown", "<nil>", "<nil>", "cwd", "personal"},
		{s3, "unknown", "<nil>", "unknown", "<nil>", "<nil>", "unknown", "work"}})
	eq(t, x.q("select session, item, role, source from assignment"), [][]string{{x.sid, "demo#4", "qa", "launch"}})
}

func TestABadTagIsNotTrusted(t *testing.T) {
	x := newIC(t, cfg{})
	testutil.Write(t, x.tx, lines(prompt("u1", "2026-10-16T09:00:00Z", "[crius account=max project=demo role=qa evil=1]", "", "/other")))
	x.run()
	eq(t, x.q("select project, attrib_source from session"), [][]string{{"<nil>", "unknown"}})
}

func TestANewTagAtTaskSwitchOpensANewAssignment(t *testing.T) {
	x := newIC(t, cfg{})
	testutil.Write(t, x.tx, lines(
		prompt("u1", "2026-10-16T09:00:00Z", "[crius account=max project=demo role=qa item=demo#4]", "", ""),
		prompt("u2", "2026-10-16T10:00:00Z", "[crius account=max project=demo role=qa item=demo#5]", "", "")))
	x.run()
	eq(t, x.q("select item, source, from_ts, to_ts from assignment order by from_ts"), [][]string{
		{"demo#4", "launch", "2026-10-16T09:00:00Z", "2026-10-16T10:00:00Z"},
		{"demo#5", "switch", "2026-10-16T10:00:00Z", "<nil>"}})
	eq(t, x.q("select item from session"), [][]string{{"demo#5"}})
}

// Codex P2 on the Python version: events took the session's latest project.
func TestEventsKeepTheProjectOfTheirAssignmentAtThatTime(t *testing.T) {
	x := newIC(t, cfg{extra: "\n[projects.zz]\ncwd = []\n"})
	testutil.Write(t, x.tx, lines(
		prompt("u1", "2026-10-16T09:00:00Z", "[crius account=max project=demo role=qa]", "", ""),
		prompt("u2", "2026-10-16T09:30:00Z", "", "", ""),
		prompt("u3", "2026-10-16T10:00:00Z", "[crius account=max project=zz role=qa]", "", ""),
		prompt("u4", "2026-10-16T10:30:00Z", "", "", "")))
	x.run()
	eq(t, x.q("select source_key, project from event order by ts"), [][]string{
		{"tx:u1", "demo"}, {"tx:u2", "demo"}, {"tx:u3", "zz"}, {"tx:u4", "zz"}})
	eq(t, x.one("select project from session"), "zz")
}

func TestSessionCountersAndNoPromptTextStored(t *testing.T) {
	x := newIC(t, cfg{})
	testutil.Write(t, x.tx, lines(
		prompt("u1", "2026-10-16T09:00:00Z", "", "", ""), prompt("u2", "2026-10-16T09:01:00Z", "", "", ""),
		prompt("u3", "2026-10-16T09:02:00Z", "", "peer", ""),
		prompt("u4", "2026-10-16T09:03:00Z", "[Request interrupted by user]", "", ""),
		assistant(A{Req: "req_1", TS: "2026-10-16T09:04:00Z", In: 1000, CR: 5000, CW: 200, Tools: [][2]string{{"t1", "Bash"}}}),
		toolResult("u5", "2026-10-16T09:05:00Z", "t1", "PreToolUse hook blocked: "+secretPrompt, true),
		compaction("c1", "2026-10-16T09:06:00Z")))
	x.run()
	eq(t, x.q("select human_prompts, peer_prompts, interrupts, compactions, hook_errors, peak_ctx, model_main, started, ended from session"),
		[][]string{{"2", "1", "1", "1", "1", "6200", "claude-opus-5-5", "2026-10-16T09:00:00Z", "2026-10-16T09:06:00Z"}})
	for _, f := range []string{"record.db", "record.db-wal"} {
		if b, err := os.ReadFile(filepath.Join(x.home, "state", f)); err == nil && strings.Contains(string(b), secretPrompt) {
			t.Fatalf("prompt text stored in %s", f)
		}
	}
}

// Codex P3 on the Python version: a title-only append moved the cursor without saving the title.
func TestATitleOnlyAppendIsSaved(t *testing.T) {
	x := newIC(t, cfg{})
	testutil.Write(t, x.tx, lines(prompt("u1", "2026-10-16T09:00:00Z", "", "", "")))
	x.run()
	appendFile(t, x.tx, lines(map[string]any{"type": "custom-title", "customTitle": "synthetic title", "sessionId": x.sid}))
	x.run()
	eq(t, x.one("select title from session"), "synthetic title")
}

func TestSubagentRequestsBelongToTheParentSession(t *testing.T) {
	x := newIC(t, cfg{})
	sub := filepath.Join(x.personal, "-work-demo", x.sid, "subagents")
	testutil.Write(t, filepath.Join(sub, "agent-abc.jsonl"), lines(assistant(A{Req: "req_s", TS: "2026-10-16T09:00:00Z", Sidechain: true})))
	testutil.Write(t, filepath.Join(sub, "agent-abc.meta.json"), `{"agentType": "Explore"}`)
	x.run()
	eq(t, x.q("select session, query_source from request"), [][]string{{x.sid, "subagent:Explore"}})
}

func TestPrivateTmpFoldersAreSkipped(t *testing.T) {
	x := newIC(t, cfg{})
	testutil.Write(t, filepath.Join(x.personal, "-private-tmp-x", "s.jsonl"), lines(assistant(A{Req: "req_t", TS: "2026-10-16T09:00:00Z"})))
	x.run()
	eq(t, x.one("select count(*) from request"), "0")
}

// Codex P2 on the Python version: os.walk hid unreadable folders.
func TestAnUnreadableTranscriptFolderMakesTheRunPartial(t *testing.T) {
	x := newIC(t, cfg{})
	testutil.Write(t, x.tx, lines(assistant(A{Req: "req_1", TS: "2026-10-16T09:00:00Z"})))
	dir := filepath.Dir(x.tx)
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o755)
	st := x.run()
	if st.Status != "partial" || !strings.Contains(strings.Join(st.Warnings, "\n"), "-work-demo") {
		t.Fatalf("%+v", st)
	}
}

func TestSyntheticMessagesAreNotRequests(t *testing.T) {
	x := newIC(t, cfg{})
	testutil.Write(t, x.tx, lines(assistant(A{Req: "req_x", TS: "2026-10-16T09:00:00Z", Model: "<synthetic>"}),
		assistant(A{Req: "req_1", TS: "2026-10-16T09:00:01Z"})))
	st := x.run()
	eq(t, x.q("select request_id from request"), [][]string{{"req_1"}})
	eq(t, st.Synthetic, 1)
	eq(t, len(st.Warnings), 0)
}

func TestMetadataLineTypesSeenOnThisMacAreKnown(t *testing.T) {
	x := newIC(t, cfg{})
	var recs []map[string]any
	for _, typ := range []string{"fork-context-ref", "frame-link", "started", "result", "launched", "pr-link", "dev-mods"} {
		recs = append(recs, map[string]any{"type": typ})
	}
	testutil.Write(t, x.tx, lines(recs...))
	eq(t, len(x.run().UnknownTypes), 0)
}

func TestUnknownShapesAreCountedAndAlertedOncePerType(t *testing.T) {
	x := newIC(t, cfg{})
	testutil.Write(t, x.tx, lines(map[string]any{"type": "brand-new-thing", "uuid": "z"})+"{not json\n")
	st := x.run()
	eq(t, st.UnknownTypes, map[string]int{"brand-new-thing": 1})
	eq(t, st.BadLines, 1)
	appendFile(t, x.tx, lines(map[string]any{"type": "brand-new-thing", "uuid": "z2"})+"{not json either\n")
	x.run()
	alerts := strings.Split(strings.TrimSpace(testutil.Read(t, filepath.Join(x.home, "bus", "crius.log"))), "\n")
	n, bad := 0, 0
	for _, a := range alerts {
		if !strings.Contains(a, " ALERT crius:ingest -> crius — ") {
			t.Fatal(a)
		}
		if strings.Contains(a, "brand-new-thing") {
			n++
		}
		if strings.Contains(a, "not valid JSON") {
			bad++
		}
	}
	eq(t, [2]int{n, bad}, [2]int{1, 1})
}

// Codex P2 on the Python version: alerts were decided after the cursor commit, so a failure in between lost them.
func TestAnAlertThatCouldNotBePostedIsPostedOnTheNextRun(t *testing.T) {
	x := newIC(t, cfg{})
	testutil.Write(t, x.tx, lines(map[string]any{"type": "brand-new-thing"}))
	bus := filepath.Join(x.home, "bus")
	if err := os.Chmod(bus, 0o500); err != nil {
		t.Fatal(err)
	}
	st := x.run()
	os.Chmod(bus, 0o700)
	eq(t, st.Status, "partial")
	x.run()
	if !strings.Contains(testutil.Read(t, filepath.Join(bus, "crius.log")), "brand-new-thing") {
		t.Fatal("alert lost")
	}
}

// Review of 87b7286: an alert row longer than a bus line (queued by an older build) is cut when posted, so it never
// blocks the queue.
func TestALongStoredAlertIsCutWhenPosted(t *testing.T) {
	x := newIC(t, cfg{})
	x.run()
	db, err := sql.Open("sqlite", filepath.Join(x.home, "state", "record.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("insert into alert (key, ts, text) values ('old', '2026-10-17T03:00:00Z', ?)", strings.Repeat("y", 400))
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	testutil.Write(t, x.tx, lines(map[string]any{"type": "brand-new-thing"}))
	x.run()
	log := testutil.Read(t, filepath.Join(x.home, "bus", "crius.log"))
	if !strings.Contains(log, strings.Repeat("y", 300)+"\n") || strings.Contains(log, strings.Repeat("y", 301)) ||
		!strings.Contains(log, "brand-new-thing") {
		t.Fatal(log)
	}
}

// Codex P2 on the Go watchdog, same class in the ingest: a root that is a symlink read 0 files and said complete.
func TestASymlinkedTranscriptRootIsRead(t *testing.T) {
	x := newIC(t, cfg{})
	link := filepath.Join(x.root, "link")
	if err := os.Symlink(x.personal, link); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, x.home, link, x.work, cfg{})
	testutil.Write(t, x.tx, lines(assistant(A{Req: "req_1", TS: "2026-10-16T09:00:00Z"})))
	x.run()
	eq(t, x.one("select count(*) from request"), "1")
}

// Codex P2 on the Go version: when a request's first record had no model, the model (and so the cost) stayed empty.
func TestAModelArrivingInALaterRecordIsKept(t *testing.T) {
	x := newIC(t, cfg{})
	noModel := func(req, ts string) map[string]any {
		a := assistant(A{Req: req, TS: ts})
		a["message"].(map[string]any)["model"] = ""
		return a
	}
	testutil.Write(t, x.tx, lines(noModel("req_a", "2026-10-16T09:00:00Z"), assistant(A{Req: "req_a", TS: "2026-10-16T09:00:01Z"}),
		noModel("req_b", "2026-10-16T09:00:02Z")))
	x.run()
	appendFile(t, x.tx, lines(assistant(A{Req: "req_b", TS: "2026-10-16T09:00:03Z"})))
	x.run()
	eq(t, x.q("select request_id, model, cost_list_usd is not null from request order by 1"),
		[][]string{{"req_a", "claude-opus-5-5", "1"}, {"req_b", "claude-opus-5-5", "1"}})
}

// A row priced before the price table changed kept its old cost, so the store mixed two price tables silently.
func TestAPriceTableChangeRepricesStoredRowsOnce(t *testing.T) {
	x := newIC(t, cfg{})
	testutil.Write(t, x.tx, lines(assistant(A{Req: "req_1", TS: "2026-10-16T09:00:00Z", Out: 10, CR: 100})))
	x.run()
	want := x.one("select cost_list_usd from request")
	db, err := store.Open(x.home)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("update request set cost_list_usd=999; update coverage set sha='old' where source='prices'"); err != nil {
		t.Fatal(err)
	}
	x.run()
	if got := x.one("select cost_list_usd from request"); got != want {
		t.Fatalf("not repriced: %s, want %s", got, want)
	}
	if _, err := db.Exec("update request set cost_list_usd=999"); err != nil {
		t.Fatal(err)
	}
	x.run() // same table: no full reprice on every run
	if got := x.one("select cost_list_usd from request"); got != "999" && got != "999.0" {
		t.Fatalf("repriced again: %s", got)
	}
}

// Codex P2 on the Go version: one request with a NULL model made the reprice fail, and so every run.
func TestARequestWithANullModelIsLeftUnpricedNotFatal(t *testing.T) {
	x := newIC(t, cfg{})
	db, err := store.Open(x.home)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("insert into request (request_id, session, model) values ('r0', 's0', null)"); err != nil {
		t.Fatal(err)
	}
	x.run()
	if got := x.one("select cost_list_usd is null from request where request_id='r0'"); got != "1" {
		t.Fatal(got)
	}
}

// projects.toml is the source of truth: a session filed by its folder is re-filed when the config changes, even
// with no new transcript lines (a finished session would otherwise keep its old filing forever).
func TestAConfigChangeRefilesSessionsFiledByFolder(t *testing.T) {
	x := newIC(t, cfg{})
	testutil.Write(t, x.tx, lines(prompt("u1", "2026-10-16T09:00:00Z", "hello", "", "/other/wt")))
	x.run()
	eq(t, x.q("select coalesce(project,'-'), attrib_source from session"), [][]string{{"-", "unknown"}})
	writeConfig(t, x.home, x.personal, x.work, cfg{cwd: "/other"})
	x.run()
	eq(t, x.q("select project, attrib_source from session"), [][]string{{"demo", "cwd"}})
	eq(t, x.q("select distinct project from event"), [][]string{{"demo"}})
}

// Review 831be40: a registered session's events from before its first tag follow the folder; a config change
// re-files them too.
func TestAConfigChangeRefilesPreTagEvents(t *testing.T) {
	x := newIC(t, cfg{})
	testutil.Write(t, x.tx, lines(prompt("u1", "2026-10-16T09:00:00Z", "hello", "", "/other/wt"),
		prompt("u2", "2026-10-16T09:05:00Z", "[crius account=max project=demo role=qa]", "", "/other/wt")))
	x.run()
	writeConfig(t, x.home, x.personal, x.work, cfg{cwd: "/other"})
	x.run()
	eq(t, x.q("select coalesce(project,'-') from event order by ts"), [][]string{{"demo"}, {"demo"}})
}
