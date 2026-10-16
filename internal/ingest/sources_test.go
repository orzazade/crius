package ingest

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/store"
	"github.com/orzazade/crius/internal/testutil"
)

func gitOut(t *testing.T, repo string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func gitCase(t *testing.T) (*ic, string) {
	root := t.TempDir()
	repo := testutil.GitRepo(t, filepath.Join(root, "repo"))
	return newIC(t, cfg{repo: repo}), repo
}

func TestCommitsRevertsAndSessionLink(t *testing.T) {
	x, repo := gitCase(t)
	testutil.Write(t, filepath.Join(repo, "a.txt"), "1\n")
	testutil.CommitAll(t, repo)
	sha1 := gitOut(t, repo, "rev-parse", "HEAD")
	testutil.Git(t, repo, "revert", "--no-edit", "HEAD")
	sha2 := gitOut(t, repo, "rev-parse", "HEAD")
	testutil.Write(t, x.tx, lines(assistant(A{Req: "req_1", TS: "2026-10-16T09:00:00Z", Tools: [][2]string{{"t1", "Bash"}}}),
		toolResult("u1", "2026-10-16T09:00:01Z", "t1", "[main "+sha1[:7]+"] first\n", false)))
	x.run()
	eq(t, x.q("select project, is_revert, reverts_sha, session, files from git_commit where sha=?", sha1),
		[][]string{{"demo", "0", "<nil>", x.sid, `["a.txt"]`}})
	eq(t, x.q("select is_revert, reverts_sha from git_commit where sha=?", sha2), [][]string{{"1", sha1}})
	eq(t, x.one("select count(*) from evidence where kind='commit'"), "2")
	before := x.counts()
	x.run()
	eq(t, x.counts(), before)
	testutil.Write(t, filepath.Join(repo, "b.txt"), "2\n")
	testutil.CommitAll(t, repo)
	x.run()
	eq(t, x.one("select count(*) from git_commit"), "3")
}

// Codex P1 on the Python version: the branch was resolved after `git log`, so a commit landing in between was
// taken as the cursor without its row and skipped forever.
func TestACommitLandingDuringTheRunIsNotSkipped(t *testing.T) {
	x, repo := gitCase(t)
	testutil.Write(t, filepath.Join(repo, "a.txt"), "1\n")
	testutil.CommitAll(t, repo)
	afterHeadHook = func() {
		testutil.Write(t, filepath.Join(repo, "b.txt"), "2\n")
		testutil.CommitAll(t, repo)
		afterHeadHook = nil
	}
	defer func() { afterHeadHook = nil }()
	x.run()
	x.run()
	eq(t, x.one("select count(*) from git_commit"), "2")
}

func TestAMissingRepoMakesTheRunPartial(t *testing.T) {
	x := newIC(t, cfg{})
	writeConfig(t, x.home, x.personal, x.work, cfg{repo: filepath.Join(x.root, "gone")})
	st := x.run()
	if st.Status != "partial" || !strings.Contains(strings.Join(st.Warnings, "\n"), "gone") {
		t.Fatalf("%+v", st)
	}
}

func busCase(t *testing.T) (*ic, string, string) {
	root := t.TempDir()
	arts := filepath.Join(root, "docs")
	os.MkdirAll(arts, 0o755)
	teamLog := filepath.Join(root, "BUILD-LOG.md")
	return newIC(t, cfg{artifactDir: arts, teamLogs: teamLog}), arts, teamLog
}

// RULES 3.4: a VERIFIED line is a bus type and keeps its recipient (refs "to"), so the digest can tell which lead a
// check is for; other lines do not (review of 636d4da). A slug recipient is stored as written: redaction would turn
// token-service:lead into [redacted]; any other recipient is redacted like all text.
func TestBusLinesKeepTheirRecipient(t *testing.T) {
	x, _, teamLog := busCase(t)
	testutil.Write(t, filepath.Join(x.home, "bus", "demo.log"),
		"2026-10-17T02:57 VERIFIED crius:lead -> token-service:lead — b3a87cb · step 4: 31/34\n"+
			"2026-10-17T02:58 DONE demo:leader -> crius — done\n"+
			"2026-10-17T03:00 VERIFIED crius:lead -> sk-ant-api03-"+strings.Repeat("A", 40)+" — F-1 · x\n")
	testutil.Write(t, teamLog, "2026-10-17T02:59 VERIFIED crius:lead -> demo:qa-ui — F-126 · ticks seen\n")
	x.run()
	if rows := x.q("select refs from event where kind = 'done'"); len(rows) != 1 || rows[0][0] != "<nil>" {
		t.Fatal("a line other than VERIFIED keeps its recipient:", rows)
	}
	rows := x.q("select kind, refs from event where kind = 'verified' order by ts")
	if len(rows) != 3 {
		t.Fatal(rows)
	}
	for i, want := range []string{"token-service:lead", "demo:qa-ui", "[redacted]"} {
		var refs map[string][]string
		if err := json.Unmarshal([]byte(rows[i][1]), &refs); err != nil || len(refs["to"]) != 1 || refs["to"][0] != want {
			t.Errorf("%v: %v", rows[i], err)
		}
	}
	eq(t, rows[0][0], "verified")
}

// Crius 10-17 03:03: bus stamps are local time, UTC ends in Z. A dated line more than an hour behind the newest stamp
// before it in its file (a writer stamping UTC without Z), or more than 10 minutes ahead of the clock, is loud: one
// alert per file, writer, day and kind (ahead, behind). The line keeps the stamp it was written with; a line flagged ahead does not move
// the newest stamp, so the good lines after it are not blamed.
func TestBusClockMistakesAreLoud(t *testing.T) {
	x, _, _ := busCase(t)
	now := time.Date(2026, 10, 17, 3, 0, 0, 0, time.Local)
	clock = func() time.Time { return now }
	t.Cleanup(func() { clock = time.Now })
	testutil.Write(t, filepath.Join(x.home, "bus", "falcon.log"),
		"2026-10-17T02:19 INFO crius:lead -> falcon:lead — go\n"+
			"2026-10-16T22:23 DONE falcon:lead -> crius:lead — UTC without Z\n"+
			"2026-10-16T22:30 DONE falcon:lead -> crius:lead — again: the same alert\n"+
			"2026-10-15T23:00 DONE falcon:lead -> crius:lead — another day: another alert\n"+
			"2026-10-17T01:20 INFO crius:lead -> falcon:lead — 59 min behind: no alert\n"+
			"2026-10-17T01:10 INFO axi:lead -> crius — 69 min behind 02:19, the newest; 01:20 does not move it\n"+
			"2026-10-17T03:15 INFO quotes:lead -> crius — 15 min ahead\n"+
			"2026-10-17T03:09 INFO crius:lead -> falcon:lead — 9 min ahead: fine\n"+
			"2026-10-17T02:08 INFO crius:lead -> falcon:lead — 61 min behind 03:09\n")
	x.run()
	rows := x.q("select text from alert where key like 'bus-clock:%' order by text")
	want := []string{"axi:lead .* 1h9m behind", "crius:lead .* 1h1m behind",
		"falcon:lead line 2026-10-15T23:00 .* 27h19m behind",
		"falcon:lead line 2026-10-16T22:23 .* 3h56m behind the newest stamp before it \\(2026-10-17T02:19\\)",
		"quotes:lead line 2026-10-17T03:15 .* 15m ahead"}
	if len(rows) != len(want) {
		t.Fatal(rows)
	}
	for i, w := range want {
		if !regexp.MustCompile(w).MatchString(rows[i][0]) {
			t.Errorf("alert %d: %q does not match %q", i, rows[i][0], w)
		}
	}
	eq(t, x.one("select ts from event where text = 'UTC without Z'"),
		time.Date(2026, 10, 16, 22, 23, 0, 0, time.Local).UTC().Format("2006-01-02T15:04:05Z"))
}

// Review of 944e44a: an alert key holds no writer name (a role can be a token); a slug writer stays readable in the
// alert (the redactor takes token-service:lead for a key); a re-read with a later clock does not let a line flagged
// ahead move the newest stamp and blame good lines; a Z line in a DST fall-back hour keeps its instant.
func TestBusClockReviewOf944e44a(t *testing.T) {
	x, _, _ := busCase(t)
	now := time.Date(2026, 10, 17, 3, 0, 0, 0, time.Local)
	clock = func() time.Time { return now }
	t.Cleanup(func() { clock = time.Now })
	token := "ghp_" + strings.Repeat("A", 36)
	path := filepath.Join(x.home, "bus", "demo.log")
	text := "2026-10-17T07:00 INFO bad:lead -> crius — bad clock\n" +
		"2026-10-17T03:00 INFO good:lead -> crius — correct clock\n" +
		"2026-10-17T01:30 INFO bad:lead -> crius — and behind: a second kind, a second alert\n" +
		"2026-10-17T03:15 INFO demo:" + token + " -> crius — a token for a role\n" +
		"2026-10-17T03:20 INFO token-service:lead -> crius — x\n"
	testutil.Write(t, path, text)
	testutil.Write(t, filepath.Join(x.home, "bus", token+".log"), "2026-10-17T03:30 INFO demo:lead -> crius — a token for a file name\n")
	x.run()
	eq(t, x.one("select count(*) from alert where key like 'bus-clock:%'"), "5")
	eq(t, x.one("select count(*) from alert where key like '%ghp_%' or text like '%ghp_%'"), "0")
	eq(t, x.one("select count(*) from alert where text like 'bus clock: token-service:lead line 2026-10-17T03:20 %'"), "1")
	now = now.Add(5 * time.Hour)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	testutil.Write(t, path, text) // a new file: read again from the start
	x.run()
	eq(t, x.one("select count(*) from alert where text like '%good:lead%'"), "0")

	local := time.Local
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	time.Local = ny
	t.Cleanup(func() { time.Local = local })
	now = time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	testutil.Write(t, filepath.Join(x.home, "bus", "dst.log"), "2026-11-01T06:30Z INFO demo:lead -> crius — second 01:30\n")
	x.run()
	eq(t, x.one("select ts from event where text = 'second 01:30'"), "2026-11-01T06:30:00Z")
}

func TestBusLinesDatedUndatedMalformedAndRedacted(t *testing.T) {
	x, _, _ := busCase(t)
	token := "ghp_" + strings.Repeat("A", 36)
	testutil.Write(t, filepath.Join(x.home, "bus", "demo.log"),
		"2026-10-16T09:20 DONE demo:leader -> crius — item=demo#4 sha=abc1234 done\n"+
			"09:30 BLOCKED demo:leader -> crius — waiting on CI\n"+
			"this is not a bus line\n"+
			"2026-10-16T09:40 NEED-OWNER demo:leader -> crius — token "+token+" leaked path="+token+"\n")
	x.run()
	rows := x.q("select ts, kind, actor, project, text, refs from event where source_key like 'bus:%' order by id")
	var kinds []string
	for _, r := range rows {
		kinds = append(kinds, r[1])
	}
	eq(t, kinds, []string{"done", "blocked", "malformed", "need-owner"})
	var refs map[string][]string
	if err := json.Unmarshal([]byte(rows[0][5]), &refs); err != nil || refs["item"][0] != "demo#4" {
		t.Fatal(rows[0][5], err)
	}
	if !strings.HasPrefix(rows[1][0], "2026-10-16T") {
		t.Fatal(rows[1][0])
	}
	if strings.Contains(rows[3][4], token) || !strings.Contains(rows[3][4], "[redacted]") {
		t.Fatal(rows[3][4])
	}
	// Codex P1 on the Python version: refs were stored without redaction.
	if strings.Contains(rows[3][5], token) {
		t.Fatal("secret kept in refs:", rows[3][5])
	}
	eq(t, rows[2][4], "<nil>")
	before := x.counts()
	x.run()
	eq(t, x.counts(), before)
}

// Crius 16:04 NEED: lines stored as malformed under an older type list get their real type once the list has
// them, once, and nothing is added twice.
func TestANewBusTypeUpgradesItsMalformedLinesOnce(t *testing.T) {
	x, _, _ := busCase(t)
	delete(store.Types, "WAIT")
	t.Cleanup(func() { store.Types["WAIT"] = true })
	testutil.Write(t, filepath.Join(x.home, "bus", "waits.log"),
		"2026-10-16T09:20 DONE demo:leader -> crius — done\n"+
			"2026-10-16T09:30 WAIT demo:leader -> crius — waiting on the owner\n"+
			"this is not a bus line\n")
	x.run()
	kinds := "select kind from event where source_key like 'bus:%' order by id"
	eq(t, x.q(kinds), [][]string{{"done"}, {"malformed"}, {"malformed"}})
	store.Types["WAIT"] = true
	before := x.counts()
	x.run()
	eq(t, x.q(kinds), [][]string{{"done"}, {"wait"}, {"malformed"}})
	eq(t, x.one("select text from event where kind='wait'"), "waiting on the owner")
	eq(t, x.counts(), before)
	x.run()
	eq(t, x.counts(), before)
	// Codex P2: the re-read counted every line of the file a second time.
	eq(t, x.one("select rows from coverage where source='bus'"), "3")
}

// Codex P2: a line that now parses moves the date of the undated lines after it; their stored ts must follow.
// Codex P2: also when the file was replaced in between; the dated first line anchors the dates, not the birth.
func TestANewBusTypeRedatesTheUndatedLinesAfterIt(t *testing.T) {
	text := "2026-10-15T20:00 DONE demo:leader -> crius — one\n" +
		"03:00 WAIT demo:leader -> crius — two\n" +
		"21:00 DONE demo:leader -> crius — three\n"
	for _, replace := range []bool{false, true} {
		x, _, _ := busCase(t)
		delete(store.Types, "WAIT")
		t.Cleanup(func() { store.Types["WAIT"] = true })
		path := filepath.Join(x.home, "bus", "waits.log")
		testutil.Write(t, path, text)
		x.run()
		if replace {
			testutil.Write(t, path+".new", text)
			if err := os.Rename(path+".new", path); err != nil {
				t.Fatal(err)
			}
		}
		store.Types["WAIT"] = true
		x.run()
		want, _ := time.ParseInLocation("2006-01-02T15:04", "2026-10-16T21:00", time.Local)
		eq(t, x.one("select ts from event where text='three'"), want.UTC().Format("2006-01-02T15:04:05Z"))
	}
}

// Codex P2 on the fix above: a replaced file with the same lines (new inode, later birth) keeps the stored dates.
func TestAReplacedBusFileKeepsItsStoredDates(t *testing.T) {
	for _, newTypes := range []bool{false, true} {
		x, _, _ := busCase(t)
		if newTypes {
			delete(store.Types, "WAIT")
			t.Cleanup(func() { store.Types["WAIT"] = true })
		}
		path := filepath.Join(x.home, "bus", "demo.log")
		testutil.Write(t, path, "09:30 DONE demo:leader -> crius — one\n09:40 DONE demo:leader -> crius — two\n")
		old := time.Now().AddDate(0, 0, -2)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
		x.run()
		ts := x.q("select ts from event where text in ('one', 'two') order by id")
		testutil.Write(t, path+".new", "09:30 DONE demo:leader -> crius — one\n09:40 DONE demo:leader -> crius — two\n")
		if err := os.Rename(path+".new", path); err != nil {
			t.Fatal(err)
		}
		store.Types["WAIT"] = true
		x.run()
		eq(t, x.q("select ts from event where text in ('one', 'two') order by id"), ts)
		// Opus P1 on 277f507: a later re-read (a newer parser) must not date the lines from the new birth either.
		db, err := store.Open(x.home)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("update coverage set sha='older-parser' where source='bus'"); err != nil {
			t.Fatal(err)
		}
		db.Close()
		x.run()
		eq(t, x.q("select ts from event where text in ('one', 'two') order by id"), ts)
	}
}

// Opus P3 on 29fdcb6: a late line is not an anchor. Its stored date is the day before the line it follows, so the
// lines after it would move a day back.
func TestALateLineIsNotAnAnchor(t *testing.T) {
	x, _, _ := busCase(t)
	path := filepath.Join(x.home, "bus", "demo.log")
	birth := time.Now().AddDate(0, 0, -3)
	write := func(first string) {
		testutil.Write(t, path+".new", "00:01 DONE demo:leader -> crius — "+first+"\n"+
			"23:58 DONE demo:leader -> crius — b\n00:03 DONE demo:leader -> crius — c\n")
		if err := os.Rename(path+".new", path); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, birth, birth); err != nil {
			t.Fatal(err)
		}
	}
	write("a")
	x.run()
	c := x.one("select ts from event where text='c'")
	write("A") // the first line changed: b is the first stored line
	x.run()
	eq(t, x.one("select ts from event where text='c'"), c)
}

// Opus P3 on 277f507: a dated line keeps its stored ts on a re-read; it was read in the zone of its day, and a run
// in another zone must not shift it.
func TestARereadKeepsADatedLine(t *testing.T) {
	x, _, _ := busCase(t)
	testutil.Write(t, filepath.Join(x.home, "bus", "demo.log"), "2026-10-16T09:30 DONE demo:leader -> crius — one\n")
	x.run()
	db, err := store.Open(x.home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("update event set ts='2026-10-16T01:30:00Z'; update coverage set sha='older-parser'"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	x.run()
	eq(t, x.one("select ts from event"), "2026-10-16T01:30:00Z")
}

// Crius 01:09: a line stored with a date an older parser got wrong is re-dated when a changed parser reads the file
// again (bus and team logs alike), without a manual delete.
func TestAChangedParserRedatesStoredLines(t *testing.T) {
	for _, source := range []string{"bus", "team"} {
		x, _, teamLog := busCase(t)
		path := teamLog
		if source == "bus" {
			path = filepath.Join(x.home, "bus", "demo.log")
		}
		testutil.Write(t, path, "17:53 DONE demo:leader -> crius — one\n17:52 DONE demo:leader -> crius — two\n"+
			"18:00 DONE demo:leader -> crius — three\n")
		x.run()
		good := x.q("select ts from event where text in ('two', 'three') order by id")
		db, err := store.Open(x.home)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("update event set ts='2000-01-01T00:00:00Z' where text in ('two', 'three');"+
			" update coverage set sha='older-parser' where source=?", source); err != nil {
			t.Fatal(err)
		}
		db.Close()
		before := x.counts()
		x.run()
		eq(t, x.q("select ts from event where text in ('two', 'three') order by id"), good)
		eq(t, x.counts(), before)
		// The cursor keeps the parser it read with, so the next run does not read the whole file again.
		want := datingVersion
		if source == "bus" {
			want += typesSum()
		}
		eq(t, x.one("select sha from coverage where source=?", source), want)
	}
}

// A file's first undated line is on its birth date at any hour, also late in the evening.
func TestAnEveningFirstLineIsOnTheBirthDate(t *testing.T) {
	x, _, teamLog := busCase(t)
	testutil.Write(t, teamLog, "23:55 NEED api -> leader — schema?\n")
	x.run()
	b, err := born(teamLog)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := time.ParseInLocation("2006-01-02T15:04", b.Format("2006-01-02")+"T23:55", time.Local)
	eq(t, x.one("select ts from event where source_key like 'team:%'"), want.UTC().Format("2006-01-02T15:04:05Z"))
}

func TestTeamLogLinesUseTheLenientGrammar(t *testing.T) {
	x, _, teamLog := busCase(t)
	testutil.Write(t, teamLog, "# BUILD-LOG\n09:20 NEED api -> leader — schema?\n")
	x.run()
	eq(t, x.q("select kind, actor, project from event where source_key like 'team:%'"), [][]string{{"need", "api", "demo"}})
}

// Codex P2 on the Python version: keys used the file's base name, so two BUILD-LOG.md files collided.
func TestTwoTeamLogsWithTheSameNameDoNotCollide(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "a", "BUILD-LOG.md"), filepath.Join(root, "b", "BUILD-LOG.md")
	same := "2026-10-16T09:20 DONE api -> leader — shipped\n"
	testutil.Write(t, a, same)
	testutil.Write(t, b, same)
	x := newIC(t, cfg{teamLogs: a + "," + b})
	x.run()
	eq(t, x.one("select count(*) from event where source_key like 'team:%'"), "2")
}

func TestDecisionsAndArtifacts(t *testing.T) {
	x, arts, _ := busCase(t)
	testutil.Write(t, filepath.Join(x.home, "state", "demo", "DECISIONS.md"),
		"## demo-D1 · 2026-10-16\nDecision: a\nWhy: b\nDecided by: crius\nRefs: - · taint: no\n\n"+
			"## demo-D2 · 2026-10-16\nDecision: c\nWhy: d\nDecided by: owner\nSupersedes: demo-D1\nReason: r\nRefs: - · taint: yes\n")
	testutil.Write(t, filepath.Join(arts, "plan.md"), "# Synthetic plan\n\n## Part one\ntext\n## Part two\n")
	x.run()
	eq(t, x.q("select id, project, title, decided_by, supersedes, taint from decision order by id"), [][]string{
		{"demo-D1", "demo", "a", "crius", "<nil>", "0"}, {"demo-D2", "demo", "c", "owner", "demo-D1", "1"}})
	eq(t, x.q("select title, headings, project from artifact"), [][]string{{"Synthetic plan", "Part one | Part two", "demo"}})
	testutil.Write(t, filepath.Join(arts, "plan.md"), "# Renamed plan\n")
	x.run()
	eq(t, x.q("select title from artifact"), [][]string{{"Renamed plan"}})
}

// Codex P1 on the Go version: the sender role and a Supersedes value were stored without redaction.
func TestRolesAndSupersedesAreRedacted(t *testing.T) {
	x, _, _ := busCase(t)
	token := "ghp_" + strings.Repeat("B", 36)
	testutil.Write(t, filepath.Join(x.home, "bus", "demo.log"), "2026-10-16T09:20 DONE demo:"+token+" -> crius — done\n")
	testutil.Write(t, filepath.Join(x.home, "state", "demo", "DECISIONS.md"),
		"## demo-D1 · 2026-10-16\nDecision: a\nWhy: b\nDecided by: crius\nSupersedes: "+token+"\nReason: r\nRefs: - · taint: no\n")
	x.run()
	for _, v := range []string{x.one("select actor from event where source_key like 'bus:%'"), x.one("select supersedes from decision")} {
		if strings.Contains(v, token) {
			t.Fatal("secret stored:", v)
		}
	}
}

// Codex P2 on the Go version: a short sha that matches commits in two repos linked one session to both.
func TestAnAmbiguousShortShaLinksNoCommit(t *testing.T) {
	x := newIC(t, cfg{})
	x.run()
	db, err := store.Open(x.home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("insert into git_commit (sha, repo) values ('abc1234" + strings.Repeat("0", 33) + "', 'r1')," +
		" ('abc1234" + strings.Repeat("1", 33) + "', 'r2'), ('def5678" + strings.Repeat("0", 33) + "', 'r1');" +
		" insert into event (ts, kind, session, refs, source_key) values" +
		" ('2026-10-16T09:00:00Z', 'commit_made', 's1', '{\"sha\":[\"abc1234\"]}', 'k1')," +
		" ('2026-10-16T09:00:00Z', 'commit_made', 's2', '{\"sha\":[\"def5678\"]}', 'k2')"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	x.run()
	eq(t, x.q("select repo, session from git_commit order by sha"), [][]string{{"r1", "<nil>"}, {"r2", "<nil>"}, {"r1", "s2"}})
}

// Codex P2 on the Go version: a symlinked artifact kept the link's stamp, so edits to its target were missed.
func TestASymlinkedArtifactFollowsItsTarget(t *testing.T) {
	x, arts, _ := busCase(t)
	target := filepath.Join(x.root, "report.md")
	testutil.Write(t, target, "# First\n")
	if err := os.Symlink(target, filepath.Join(arts, "latest.md")); err != nil {
		t.Fatal(err)
	}
	x.run()
	testutil.Write(t, target, "# Second title\n")
	x.run()
	eq(t, x.q("select title from artifact"), [][]string{{"Second title"}})
}

// Codex P2 on the Go version: file names with tabs or non-ASCII letters were stored in git's quoted form.
func TestCommitFileNamesAreStoredAsIs(t *testing.T) {
	x, repo := gitCase(t)
	testutil.Write(t, filepath.Join(repo, "a\tb.txt"), "1\n")
	testutil.Write(t, filepath.Join(repo, "ü.txt"), "1\n")
	testutil.CommitAll(t, repo)
	x.run()
	eq(t, x.one("select files from git_commit"), `["a\tb.txt","ü.txt"]`)
}

// Same class as the symlinked transcript root: an artifact folder that is a symlink was never walked.
func TestASymlinkedArtifactFolderIsWalked(t *testing.T) {
	root := t.TempDir()
	arts := filepath.Join(root, "docs")
	testutil.Write(t, filepath.Join(arts, "plan.md"), "# Linked plan\n")
	link := filepath.Join(root, "link")
	if err := os.Symlink(arts, link); err != nil {
		t.Fatal(err)
	}
	x := newIC(t, cfg{artifactDir: link})
	x.run()
	eq(t, x.q("select title from artifact"), [][]string{{"Linked plan"}})
}

// Codex P1 twice on the Go version (bus role and Supersedes, then tag values, cwd, model, agent type): fields were
// redacted one insert at a time, so each new field could miss it. This plants a secret in every text field the
// ingest reads and scans every column of every table, so a field added later is covered too.
func TestAPlantedSecretReachesNoColumn(t *testing.T) {
	root := t.TempDir()
	repo := testutil.GitRepo(t, filepath.Join(root, "repo"))
	arts := filepath.Join(root, "docs")
	teamLog := filepath.Join(root, "BUILD-LOG.md")
	x := newIC(t, cfg{repo: repo, artifactDir: arts, teamLogs: teamLog})
	slugTok := "sk-" + strings.Repeat("q", 30) // passes the tag's slug checks
	tok := "ghp_" + strings.Repeat("Z", 36)
	tag := "[crius account=max project=demo role=" + slugTok + " item=" + slugTok + "#1]"
	a := assistant(A{Req: "req_1", TS: "2026-10-16T09:00:01Z", Model: "claude-" + tok})
	testutil.Write(t, x.tx, lines(prompt("u1", "2026-10-16T09:00:00Z", tag, "", "/work/demo/"+tok), a,
		map[string]any{"type": "custom-title", "customTitle": "t " + tok}))
	sub := filepath.Join(x.personal, "-work-demo", x.sid, "subagents")
	testutil.Write(t, filepath.Join(sub, "agent-1.jsonl"), lines(assistant(A{Req: "req_s", TS: "2026-10-16T09:00:02Z", Sidechain: true})))
	testutil.Write(t, filepath.Join(sub, "agent-1.meta.json"), `{"agentType": "`+tok+`"}`)
	testutil.Write(t, filepath.Join(x.home, "bus", "demo.log"), "2026-10-16T09:20 DONE demo:"+tok+" -> crius — "+tok+" path="+tok+"\n")
	testutil.Write(t, teamLog, "09:20 NEED "+tok+" -> leader — "+tok+"\n")
	testutil.Write(t, filepath.Join(x.home, "state", "demo", "DECISIONS.md"), "## demo-D1 · 2026-10-16\nDecision: "+tok+
		"\nWhy: "+tok+"\nDecided by: "+tok+"\nSupersedes: "+tok+"\nReason: "+tok+"\nRefs: "+tok+" · taint: no\n")
	testutil.Write(t, filepath.Join(arts, "plan.md"), "# "+tok+"\n## "+tok+"\n")
	testutil.Write(t, filepath.Join(repo, "f.txt"), "1\n")
	testutil.Git(t, repo, "add", "-A")
	testutil.Git(t, repo, "commit", "-q", "-m", tok, "-m", tok)
	x.run()
	for _, tb := range x.q("select name from sqlite_master where type='table'") {
		for _, row := range x.q(`select * from "` + tb[0] + `"`) {
			for _, v := range row {
				if strings.Contains(v, strings.Repeat("Z", 20)) || strings.Contains(v, strings.Repeat("q", 20)) {
					t.Errorf("%s: secret stored: %s", tb[0], v)
				}
			}
		}
	}
}

// Commit subjects are stored for find; commits read before that get theirs on the next run, once: a commit no
// longer on the branch gets ” so the repo is not read whole again on every run.
func TestCommitSubjectsAndBackfill(t *testing.T) {
	x, repo := gitCase(t)
	testutil.Write(t, filepath.Join(repo, "a.txt"), "1\n")
	testutil.Git(t, repo, "add", "-A")
	testutil.Git(t, repo, "commit", "-q", "-m", "fix(clock): broker time follows DST")
	sha := gitOut(t, repo, "rev-parse", "HEAD")
	x.run()
	eq(t, x.one("select subject from git_commit where sha=?", sha), "fix(clock): broker time follows DST")
	// Review of 376393d (P3): a redacted subject counts once.
	testutil.Write(t, filepath.Join(repo, "b.txt"), "1\n")
	testutil.Git(t, repo, "add", "-A")
	testutil.Git(t, repo, "commit", "-q", "-m", "set password=hunter22x for the test")
	eq(t, x.run().Redactions, 1)
	db, err := store.Open(x.home)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("update git_commit set subject=null; insert into git_commit (sha, repo, project) values ('gone', ?, 'demo')", repo); err != nil {
		t.Fatal(err)
	}
	db.Close()
	x.run()
	eq(t, x.q("select sha, subject from git_commit where sha in (?, 'gone') order by sha='gone'", sha), [][]string{{sha, "fix(clock): broker time follows DST"}, {"gone", ""}})
}
