package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/store"
	"github.com/orzazade/crius/internal/testutil"
)

func run(args ...string) (int, string) {
	var out, errb bytes.Buffer
	rc := Run(args, &out, &errb)
	return rc, out.String() + errb.String()
}

func TestInitRecordAndLeakTest(t *testing.T) {
	_, home := testutil.Temp(t)
	repo := testutil.RepoRoot(t)
	t.Setenv("CRIUS_REPO", repo)
	// Built at runtime: the leak test (rightly) fails if a tracked file holds a literal id from the store.
	slug := "zz" + "cli"
	if rc, out := run("init"); rc != 0 {
		t.Fatal(rc, out)
	}
	rc, out := run("record", "item", "--project", slug, "--title", "t", "--kind", "code",
		"--criteria", "CI pass", "--criteria", "codex review")
	if rc != 0 || strings.TrimSpace(out) != slug+"#1" {
		t.Fatal(rc, out)
	}
	rc, out = run("record", "decision", "--project", slug, "--title", "t", "--why", "w", "--by", "crius")
	if rc != 0 || strings.TrimSpace(out) != slug+"-D1" {
		t.Fatal(rc, out)
	}
	if rc, out = run("record", "bus", "--project", slug, "--role", "leader", "--type", "DONE",
		"--text", "item="+slug+"#1 sha=abc1234 done"); rc != 0 {
		t.Fatal(rc, out)
	}
	log := testutil.Read(t, filepath.Join(home, "bus", slug+".log"))
	if n := strings.Count(log, "\n"); n != 2 {
		t.Fatal(log)
	}
	if rc, out = run("leak-test"); rc != 0 || strings.TrimSpace(out) != "complete" {
		t.Fatal(rc, out)
	}
}

func TestBadBusLineExitsNonzero(t *testing.T) {
	testutil.Temp(t)
	run("init")
	if rc, _ := run("record", "bus", "--project", "demo", "--role", "leader", "--type", "WAT", "--text", "x"); rc != 2 {
		t.Fatal(rc)
	}
}

func TestLeakTestReportsProblemsWithExit4(t *testing.T) {
	root, _ := testutil.Temp(t)
	repo := testutil.GitRepo(t, filepath.Join(root, "checkout"))
	testutil.Write(t, filepath.Join(repo, "record.db"), "x")
	testutil.CommitAll(t, repo)
	run("init")
	if rc, out := run("leak-test", "--repo", repo); rc != 4 || !strings.HasPrefix(out, "unavailable") {
		t.Fatal(rc, out)
	}
}

func TestRecordWithoutHomeIsUnavailable(t *testing.T) {
	testutil.Temp(t)
	if rc, _ := run("record", "bus", "--project", "demo", "--role", "leader", "--type", "DONE", "--text", "x"); rc != 4 {
		t.Fatal(rc)
	}
}

func TestLeakTestWithoutRepoIsAnError(t *testing.T) {
	testutil.Temp(t)
	t.Setenv("CRIUS_REPO", "")
	run("init")
	if rc, out := run("leak-test"); rc != 2 || !strings.Contains(out, "--repo") {
		t.Fatal(rc, out)
	}
}

func TestInitRejectsArgumentsAndWritesNothing(t *testing.T) {
	_, home := testutil.Temp(t)
	for _, arg := range []string{"--bogus", "-h", "extra"} {
		if rc, _ := run("init", arg); rc != 2 {
			t.Errorf("init %s: rc %d", arg, rc)
		}
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatal("init wrote the home despite bad arguments")
	}
}

func TestItemWithoutCriteriaIsRefusedBeforeTouchingTheStore(t *testing.T) {
	_, home := testutil.Temp(t)
	run("init")
	rc, out := run("record", "item", "--project", "demo", "--title", "t", "--kind", "code")
	if rc != 2 || !strings.Contains(out, "--criteria") {
		t.Fatal(rc, out)
	}
	for _, f := range []string{"record.db", "record.lock"} {
		if _, err := os.Stat(filepath.Join(home, "state", f)); !os.IsNotExist(err) {
			t.Errorf("%s was created", f)
		}
	}
}

func TestEmptyBusTextIsAccepted(t *testing.T) {
	testutil.Temp(t)
	run("init")
	if rc, out := run("record", "bus", "--project", "demo", "--role", "leader", "--type", "DONE", "--text", ""); rc != 0 {
		t.Fatal(rc, out)
	}
}

// With no sample the quota answer says so and exits partial, never a quiet 0%.
func TestQuotaWithNoSampleSaysSoAndExitsPartial(t *testing.T) {
	testutil.Temp(t)
	run("init")
	rc, out := run("quota")
	if rc != exitPartial || !strings.HasPrefix(out, "partial\n  no quota sample yet\n") ||
		!strings.Contains(out, "Enterprise (Appxite API billing") {
		t.Fatal(rc, out)
	}
}

// §4: --json for scripts, with the same status.
func TestQuotaJSON(t *testing.T) {
	testutil.Temp(t)
	run("init")
	rc, out := run("quota", "--json")
	var r struct {
		Status  string
		Reasons []string
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil || rc != exitPartial || r.Status != "partial" ||
		len(r.Reasons) == 0 {
		t.Fatal(rc, err, out)
	}
}

// §4: text output is capped at 20 lines and says what it left out.
func TestCapLines(t *testing.T) {
	var long []string
	for i := 0; i < 25; i++ {
		long = append(long, fmt.Sprint("line ", i))
	}
	got := strings.Split(strings.TrimSuffix(capLines(strings.Join(long, "\n")+"\n", 20), "\n"), "\n")
	if len(got) != 20 || got[18] != "line 18" || got[19] != "... 6 more lines (use --json)" {
		t.Fatalf("%q", got)
	}
	if short := "a\nb\n"; capLines(short, 20) != short {
		t.Fatal("short output changed")
	}
}

// Codex P2: an error can quote a config value; the quota command redacts it like every other sink.
func TestQuotaErrorOutputIsRedacted(t *testing.T) {
	_, home := testutil.Temp(t)
	run("init")
	key := "sk-ant-api03-" + strings.Repeat("A", 40)
	toml := "[projects.demo]\npersona = \"personal\"\naccounts = [\"" + key + "\"]\ncwd = []\nrepos = []\nteam_logs = []\nartifact_dirs = []\n"
	if err := os.WriteFile(filepath.Join(home, "projects.toml"), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	rc, out := run("quota")
	if rc != exitError || strings.Contains(out, key) || !strings.Contains(out, "unknown account") {
		t.Fatal(rc, out)
	}
}

// Crius 22:36 and aitch's contract: --room prints one JSON line on stdout on every exit (0 room, 3 full, 4 unknown),
// and says an unknown answer on stderr too.
func TestQuotaRoomUnknownIsLoud(t *testing.T) {
	_, home := testutil.Temp(t)
	var out, errb bytes.Buffer
	if rc := Run([]string{"quota", "--room"}, &out, &errb); rc != exitUnavailable || !strings.Contains(errb.String(), "missing") {
		t.Fatal(rc, out.String(), errb.String())
	}
	var r struct{ Verdict, Reason string }
	if err := json.Unmarshal(out.Bytes(), &r); err != nil || r.Verdict != "unknown" || !strings.Contains(r.Reason, "missing") ||
		strings.Count(out.String(), "\n") != 1 {
		t.Fatal(err, out.String())
	}
	run("init")
	out.Reset()
	errb.Reset()
	if rc := Run([]string{"quota", "--room"}, &out, &errb); rc != exitUnavailable || !strings.Contains(out.String(), `"verdict":"unknown"`) ||
		!strings.Contains(out.String(), "no quota sample") || !strings.Contains(errb.String(), "no quota sample") {
		t.Fatal(rc, out.String(), errb.String())
	}
	_ = home
}

// Step 4 P1: a secret given to record never reaches the bus, DECISIONS.md, the store, the output or the error text.
func TestRecordRedactsEverySink(t *testing.T) {
	_, home := testutil.Temp(t)
	run("init")
	key := "sk-ant-api03-" + strings.Repeat("B", 40)
	cases := [][]string{
		{"record", "bus", "--project", "demo", "--role", "leader", "--type", "INFO", "--text", "token " + key},
		{"record", "decision", "--project", "demo", "--title", "t " + key, "--why", "w " + key, "--by", "crius " + key,
			"--refs", key},
		{"record", "item", "--project", "demo", "--title", "t " + key, "--kind", "code", "--criteria", "c " + key},
		{"record", "bus", "--project", "demo", "--role", "leader", "--type", key, "--text", "x"},
		{"record", "item", "--project", "demo", "--title", "t", "--kind", key, "--criteria", "c"},
		{"record", "decision", "--project", "demo", "--title", "t", "--why", "w", "--by", "crius", "--supersedes", key,
			"--reason", "r"},
		{"record", "bus", "--project", "demo", "--role", "leader", "--type", "INFO", "--text", key, "--" + key},
	}
	for _, c := range cases {
		if rc, out := run(c...); strings.Contains(out, key) {
			t.Errorf("%v: exit %d printed the secret: %s", c[1:], rc, out)
		}
	}
	filepath.WalkDir(home, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.Contains(testutil.Read(t, p), key) {
			t.Errorf("%s holds the secret", p)
		}
		return nil
	})
	// Refused, not rewritten (review of P1): prose that only looks like a secret must not change unseen.
	rc, out := run("record", "item", "--project", "demo", "--title", "t", "--kind", "code", "--criteria",
		"password: reset emails a one-time link")
	if rc != exitUsage || !strings.Contains(out, "--criteria looks like a secret") {
		t.Fatal(rc, out)
	}
	if _, err := os.Stat(filepath.Join(home, "bus", "demo.log")); !os.IsNotExist(err) {
		t.Fatal("a refused record wrote a bus line", err)
	}
}

// findStore is an initialised home with an ingest run that just finished, and one event per text.
func findStore(t *testing.T, texts ...string) string {
	t.Helper()
	_, home := testutil.Temp(t)
	if rc, out := run("init"); rc != 0 {
		t.Fatal(rc, out)
	}
	db, err := store.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ts := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec("insert into source_run (started, finished, status) values (?,?,'complete')", ts, ts); err != nil {
		t.Fatal(err)
	}
	for i, text := range texts {
		if _, err := db.Exec("insert into event (ts, project, kind, text, source_key) values (?,?,?,?,?)",
			fmt.Sprintf("2026-10-15T10:%02d:00Z", i), "demo", "info", text, fmt.Sprint("k", i)); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

// Step 4 P1: a secret in a stored line (an old row, or one a pattern missed then) never reaches find's output;
// JSON escapes quotes, so --json is redacted value by value.
func TestFindNeverPrintsAStoredSecret(t *testing.T) {
	findStore(t, "token ghp_"+strings.Repeat("E", 36), `password: "hunter22" was rotated`)
	for _, args := range [][]string{{"find", "token", "rotated"}, {"find", "token", "rotated", "--json"}} {
		rc, out := run(args...)
		if rc != 0 || strings.Contains(out, "EEEE") || strings.Contains(out, "hunter22") || !strings.Contains(out, "[redacted]") {
			t.Fatal(args, rc, out)
		}
	}
}

func TestFindCapsTextAt20LinesAndJSONKeepsAll(t *testing.T) {
	var texts []string
	for i := 0; i < 30; i++ {
		texts = append(texts, fmt.Sprint("mail line ", i))
	}
	findStore(t, texts...)
	rc, out := run("find", "mail")
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if rc != 0 || len(lines) != 20 || !strings.Contains(lines[19], "12 more lines (use --json)") {
		t.Fatal(rc, len(lines), out)
	}
	rc, out = run("find", "mail", "--json")
	var res struct{ Hits []any }
	if err := json.Unmarshal([]byte(out), &res); rc != 0 || err != nil || len(res.Hits) != 30 {
		t.Fatal(rc, err, len(res.Hits))
	}
}

// Review of 376393d (P2): the commit block comes last, so the 20-line cap cut it off on most word queries. It keeps
// its lines inside the cap; the hits give up the room.
func TestFindKeepsTheCommitBlockInsideTheCap(t *testing.T) {
	var texts []string
	for i := 0; i < 30; i++ {
		texts = append(texts, fmt.Sprint("mail line ", i))
	}
	home := findStore(t, texts...)
	db, err := store.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("insert into git_commit (sha, repo, project, ts, files, subject) values ('abcdef1', '/r/falcon'," +
		" 'demo', '2026-10-15T10:00:00Z', '[]', 'mail poll fix')"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	rc, out := run("find", "mail")
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if rc != 0 || len(lines) != 20 || lines[18] != "commits:" || !strings.Contains(lines[19], "falcon · mail poll fix") ||
		!strings.Contains(lines[17], "14 more lines (use --json)") {
		t.Fatal(rc, len(lines), out)
	}
}

func TestFindExitCodesAndFlagsAfterTheQuery(t *testing.T) {
	testutil.Temp(t)
	if rc, out := run("find", "x"); rc != exitUnavailable || !strings.Contains(out, "unavailable") {
		t.Fatal(rc, out)
	}
	home := findStore(t, "mail stopped")
	if rc, out := run("find", "mail", "--project", "demo", "--kind", "info"); rc != 0 || !strings.Contains(out, "mail stopped") {
		t.Fatal(rc, out)
	}
	if rc, out := run("find", "mail", "--kind", "nope"); rc != exitUsage || !strings.Contains(out, "unknown kind") {
		t.Fatal(rc, out)
	}
	if rc, out := run("find", "x", "--since", "yesterday"); rc != exitUsage {
		t.Fatal(rc, out)
	}
	db, _ := store.Open(home)
	db.Exec("update source_run set finished = '2026-01-01T00:00:00Z'")
	db.Close()
	if rc, out := run("find", "mail"); rc != exitPartial || !strings.HasPrefix(out, "partial (last ingest run finished") {
		t.Fatal(rc, out)
	}
}

func TestShowExitCodes(t *testing.T) {
	testutil.Temp(t)
	if rc, out := run("show", "demo#1"); rc != exitUnavailable {
		t.Fatal(rc, out)
	}
	findStore(t)
	if rc, out := run("record", "item", "--project", "demo", "--title", "mail fix", "--kind", "code", "--criteria", "CI"); rc != 0 {
		t.Fatal(rc, out)
	}
	if rc, out := run("show", "demo#1", "--json"); rc != 0 || !strings.Contains(out, `"value": "mail fix"`) {
		t.Fatal(rc, out)
	}
	if rc, out := run("show", "demo#2"); rc != 0 || !strings.Contains(out, "no match in items (id demo#2)") {
		t.Fatal(rc, out)
	}
	if rc, out := run("show", "demo"); rc != exitUnavailable || !strings.Contains(out, "STATE.md") {
		t.Fatal(rc, out)
	}
	if rc, out := run("show", "NotAnId"); rc != exitUsage {
		t.Fatal(rc, out)
	}
}

func TestHealthExitCodes(t *testing.T) {
	testutil.Temp(t)
	if rc, out := run("health"); rc != exitUnavailable {
		t.Fatal(rc, out)
	}
	home := findStore(t)
	tx := filepath.Join(home, "tx")
	os.MkdirAll(tx, 0o700)
	testutil.Write(t, filepath.Join(home, "projects.toml"), "[transcripts]\npersonal = \""+tx+"\"\n")
	// No heartbeat, no backup: partial, each problem named.
	rc, out := run("health")
	if rc != exitPartial || !strings.Contains(out, "PROBLEM no heartbeat") || !strings.Contains(out, "PROBLEM no backup") {
		t.Fatal(rc, out)
	}
	testutil.Write(t, filepath.Join(home, "heartbeat"), time.Now().UTC().Format(time.RFC3339)+"\n")
	testutil.Write(t, filepath.Join(home, "backup", "record-2026-10-16.db.gz"), "x")
	if rc, out = run("health", "--json"); rc != 0 || !strings.Contains(out, `"status": "complete"`) {
		t.Fatal(rc, out)
	}
}

// Step 4 P5: --deep searches one project's transcripts; past 30 days it is expired (exit 5), not "no match".
func TestFindDeepExitCodes(t *testing.T) {
	findStore(t, "x")
	for _, args := range [][]string{{"--deep", "--project", "demo"}, {"mail", "--deep", "--project", "dmeo"}} {
		if rc, out := run(append([]string{"find"}, args...)...); rc != exitUsage {
			t.Fatal(args, rc, out)
		}
	}
	if rc, out := run("find", "mail", "--deep", "--project", "demo"); rc != 0 || !strings.Contains(out, "transcripts of all projects, demo breaks ties") {
		t.Fatal(rc, out)
	}
	if rc, out := run("find", "mail", "--deep", "--project", "demo", "--since", "45d"); rc != exitExpired || !strings.Contains(out, "older than 30 days") {
		t.Fatal(rc, out)
	}
}

// Review 0178403: --json is redacted per value once; a raw pass over the encoded text must not eat a closing quote.
func TestFindJSONStaysValid(t *testing.T) {
	findStore(t, "owner asked for the new password:")
	rc, out := run("find", "owner", "--json")
	if rc != 0 || !json.Valid([]byte(out)) {
		t.Fatal(rc, out)
	}
}

// Review 626e532: a word that is no known project is a true "no", not "unavailable".
func TestShowUnknownProjectIsNoMatch(t *testing.T) {
	findStore(t, "x")
	if rc, out := run("show", "nosuchproj"); rc != 0 || !strings.Contains(out, "no match") {
		t.Fatal(rc, out)
	}
	if rc, out := run("show", "demo"); rc != exitUnavailable {
		t.Fatal(rc, out)
	}
}

// Step 5 P4: digest prints the goal report; --write saves it (redacted, like every sink) with the numbers the next
// report compares against.
func TestDigestWritesARedactedReportAndComparesWithTheLast(t *testing.T) {
	home := findStore(t)
	key := "ghp_" + strings.Repeat("F", 36)
	status := filepath.Join(home, "lead.md")
	testutil.Write(t, status, "## GOAL (2026-10-17 08:30)\nGoal: ship\n- [ ] one\nDone: 10% (guess)\nNext: one\nNeeds owner: rotate "+key+"\n")
	testutil.Write(t, filepath.Join(home, "projects.toml"), "[projects.demo]\ncwd = [\"/w/demo\"]\nleads = { lead = \""+status+"\" }\n")
	dir := filepath.Join(home, "state", "digest")
	rc, out := run("digest")
	if rc != 0 || !strings.Contains(out, "▶ 1. demo:lead: rotate [redacted]") || strings.Contains(out, "FFFF") ||
		!strings.Contains(out, "demo:lead: 10% claimed · verified 0 of 0 done (1 milestones) · first report") {
		t.Fatal(rc, out)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("digest without --write saved something", err)
	}
	if rc, out = run("digest", "--write"); rc != 0 {
		t.Fatal(rc, out)
	}
	mds, _ := filepath.Glob(filepath.Join(dir, "*.md"))
	jsons, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(mds) != 1 || len(jsons) != 1 {
		t.Fatal(mds, jsons)
	}
	b, _ := os.ReadFile(mds[0])
	if string(b) != out || strings.Contains(string(b), "FFFF") {
		t.Fatalf("saved:\n%s\nprinted:\n%s", b, out)
	}
	// The newest saved numbers are the ones compared with, not an older report's.
	testutil.Write(t, filepath.Join(dir, "2026-01-01-0900.json"), `{"demo:lead":{"done":99,"verified":0}}`)
	if rc, out = run("digest"); rc != 0 || !strings.Contains(out, "· no change since the last report") {
		t.Fatal(rc, out)
	}
	// A store that is not fresh says so and exits partial, like find.
	db, err := store.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("insert into source_run (started, finished, status) values ('2026-10-17T08:00:00Z','2026-10-17T08:01:00Z','partial')"); err != nil {
		t.Fatal(err)
	}
	if rc, out = run("digest"); rc != exitPartial || !strings.Contains(out, "· status partial") {
		t.Fatal(rc, out)
	}
	if rc, out = run("digest", "extra"); rc != exitUsage {
		t.Fatal(rc, out)
	}
}
