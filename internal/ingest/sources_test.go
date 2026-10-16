package ingest

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

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
