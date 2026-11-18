package reconcile

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/orzazade/crius/internal/testutil"
)

// A TSV board: columns found by name in any order; shas come from the sha column (the row's own), PR numbers from
// any cell, but only in the PR forms ("PR 12", "PR#12", "!12"): "#104" is not a PR.
func TestParseTSV(t *testing.T) {
	board := "flow\trow\towner\tstate\tqa\tsha\tnote\n" +
		"f1\tR1\tb1\treview\tpass\tabc1234\tlanded as PR 101\n" +
		"f1\tR2\tb2\tmerged\tfail\tabc1234 9f8e7d6c\tsee !102 and PR#103, attempt #104 failed\n" +
		"\n" +
		"f2\tR3\tb1\tproven\t\t\tnone; baseline deadbee0 is not a row sha\n"
	rows, err := ParseTSV(strings.NewReader(board))
	if err != nil {
		t.Fatal(err)
	}
	want := []Row{
		{ID: "R1", State: "review", Line: 2, SHAs: []string{"abc1234"}, PRs: []string{"101"}},
		{ID: "R2", State: "merged", Line: 3, SHAs: []string{"abc1234", "9f8e7d6c"}, PRs: []string{"102", "103"}},
		{ID: "R3", State: "proven", Line: 5},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows:\n got %+v\nwant %+v", rows, want)
	}
}

// A TSV without a row or state column cannot be judged: that is an error, not an empty board.
func TestParseTSVNeedsRowAndState(t *testing.T) {
	_, err := ParseTSV(strings.NewReader("name\tstatus_x\nR1\tdone\n"))
	if err == nil || !strings.Contains(err.Error(), "no row column") {
		t.Fatalf("want a 'no row column' error, got %v", err)
	}
}

// A TSV saved by a spreadsheet may start with a byte-order mark; the first column name is still "row".
func TestParseTSVByteOrderMark(t *testing.T) {
	rows, err := ParseTSV(strings.NewReader(string(rune(0xFEFF)) + "row\tstate\nR1\tdone\n"))
	if err != nil || len(rows) != 1 || rows[0].ID != "R1" {
		t.Fatalf("rows %+v err %v, want one row R1", rows, err)
	}
}

// A markdown board: bullet rows "- <mark> <id> text" (also "**id**", dotted ids, task lists) under one section, and
// table rows whose state is the header's state column. Shas come from free text (Cited); in "A -> B" only B is the
// row's own. A line in the section that starts with a mark or a pipe but gives no row is listed as not read; a "#"
// inside a code fence is no heading.
func TestParseMarkdown(t *testing.T) {
	board := "# Board\n" + //1
		"## OTHER\n" + //2
		"- ✅ Z1 not in the section\n" + //3
		"## READINESS — counted\n" + //4
		"Group one\n" + //5
		"- ✅ X1 first thing (abc1234, gate fp-0123456789abcdef)\n" + //6
		"- ⬜ X2b second thing\n" + //7
		"  - FOLLOW-UP X2b-1 plain bullet, no mark\n" + //8
		"- ✖ DROPPED by the owner: X3\n" + //9
		"- 🔶 X4: part landed (TRAIN 3, 9f8e7d6c) PR 55, attempt #3\n" + //10
		"- ✅ **B1** bold id\n" + //11
		"- [ ] T1 task list row\n" + //12
		"- [x] T2 task list done\n" + //13
		"- ✅ C1.M2 dotted id\n" + //14
		"- ⬜ X6/X7 two ids\n" + //15
		"- ✅ X8 LANDED 1111aaa -> 2222bbb (old 1111aaa)\n" + //16
		"```\n" + //17
		"# not a heading\n" + //18
		"- ✅ F1 inside a fence\n" + //19
		"```\n" + //20
		"| id | state | qa | sha |\n" + //21
		"|----|-------|----|-----|\n" + //22
		"| X5 | review | ✅ | abc1234 |\n" + //23
		"| T5 | done | — | |\n" + //24
		"| T6 | — | ✅ | |\n" + //25
		"| note | — | | |\n" + //26
		"## NEXT\n" + //27
		"- ⬜ Z2 after the section\n" //28
	rows, notRead, err := ParseMarkdown(strings.NewReader(board), "READINESS")
	if err != nil {
		t.Fatal(err)
	}
	want := []Row{
		{ID: "X1", State: "✅", Line: 6, SHAs: []string{"abc1234"}, Cited: true},
		{ID: "X2b", State: "⬜", Line: 7, Cited: true},
		{ID: "X4", State: "🔶", Line: 10, SHAs: []string{"9f8e7d6c"}, PRs: []string{"55"}, Cited: true},
		{ID: "B1", State: "✅", Line: 11, Cited: true},
		{ID: "T1", State: "[ ]", Line: 12, Cited: true},
		{ID: "T2", State: "[x]", Line: 13, Cited: true},
		{ID: "C1.M2", State: "✅", Line: 14, Cited: true},
		{ID: "X8", State: "✅", Line: 16, SHAs: []string{"2222bbb"}, Arrow: []string{"2222bbb"}, Cited: true},
		{ID: "X5", State: "review", Line: 23, SHAs: []string{"abc1234"}, Cited: true},
		{ID: "T5", State: "done", Line: 24, Cited: true},
		{ID: "T6", State: "", Line: 25, Cited: true},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows:\n got %+v\nwant %+v", rows, want)
	}
	var lines []int
	for _, n := range notRead {
		lines = append(lines, n.Line)
	}
	if !reflect.DeepEqual(lines, []int{9, 15, 26}) {
		t.Fatalf("not read lines %v (%+v), want [9 15 26]", lines, notRead)
	}
	if _, _, err := ParseMarkdown(strings.NewReader(board), "NO SUCH"); err == nil || !strings.Contains(err.Error(), "no section") {
		t.Fatalf("a missing section must fail, got %v", err)
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit makes an empty commit at a fixed date, so its sha is the same on every run (two runs in one second used
// to share shas, and an 8-digit sha with no letter is no sha by design); the sha it returns must look like one.
func commit(t *testing.T, dir, date, msg string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "commit", "-q", "--allow-empty", "-m", msg)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
	sha := git(t, dir, "rev-parse", "--short=8", "HEAD")
	if !isSHA(sha) {
		t.Fatalf("commit %q got sha %s, which a board would not read as a sha: change the date", msg, sha)
	}
	return sha
}

// demo is a synthetic repo: branch integ holds a plain commit, an Azure-style merge, a GitHub merge, a squash, a
// commit tagged with row X9 and subjects that only look like row ids; branch side holds a commit that never landed.
type demo struct{ dir, landed, unlanded, tagged, z3 string }

const date1, date2 = "2026-01-02T03:04:05Z", "2026-01-03T03:04:05Z"

func demoRepo(t *testing.T) demo { return demoRepoAt(t, date1) }

func demoRepoAt(t *testing.T, date string) demo {
	t.Helper()
	root, _ := testutil.Temp(t)
	dir := testutil.GitRepo(t, filepath.Join(root, "repo"))
	commit(t, dir, date, "init")
	git(t, dir, "checkout", "-q", "-b", "side")
	unlanded := commit(t, dir, date, "feat: never merged")
	git(t, dir, "checkout", "-q", "main")
	git(t, dir, "checkout", "-q", "-b", "integ")
	d := demo{dir: dir, unlanded: unlanded}
	d.landed = commit(t, dir, date, "feat: the thing")
	commit(t, dir, date, "Merged PR 101: add y")
	commit(t, dir, date, "Merge pull request #202 from someone/branch")
	commit(t, dir, date, "add z (#303)")
	commit(t, dir, date, "fix(X9): z")
	commit(t, dir, date, "review P3-6 follow-up")
	d.z3 = commit(t, dir, date, "fix(web): Z3: the header")
	d.tagged = commit(t, dir, date, "docs: tagged")
	git(t, dir, "tag", "a1b2c3d") // a tag whose name looks like a short sha: no row may resolve it as one
	return d
}

var cfg = Config{
	Done:    []string{"merged", "done", "proven", "landed", "✅", "[x]"},
	Partial: []string{"🔶"},
	Skip:    []string{"✖"},
	Open:    []string{"⬜", "open", "review", "[ ]"},
	Depth:   100,
}

func byID(r Report) map[string]Finding {
	m := map[string]Finding{}
	for _, f := range r.Findings {
		m[f.Row.ID] = f
	}
	return m
}

func kinds(r Report) map[string]string {
	k := map[string]string{}
	for id, f := range byID(r) {
		k[id] = f.Kind
	}
	return k
}

func TestCheck(t *testing.T) {
	d := demoRepo(t)
	rows := []Row{
		{ID: "R1", State: "review", SHAs: []string{d.landed}},   // not done, own sha landed -> stale
		{ID: "R2", State: "Review", PRs: []string{"101"}},       // not done, merge subject names its PR -> stale
		{ID: "R2g", State: "review", PRs: []string{"202"}},      // GitHub merge subject -> stale
		{ID: "R2s", State: "review", PRs: []string{"303"}},      // GitHub squash subject -> stale
		{ID: "X9", State: "⬜"},                                  // row id as a commit scope -> stale
		{ID: "P3", State: "⬜"},                                  // "P3-6" is another word, not P3 -> clean
		{ID: "R3", State: "merged", SHAs: []string{d.landed}},   // done and landed -> clean
		{ID: "R4", State: "MERGED", SHAs: []string{d.unlanded}}, // done, sha not on the branch -> unlanded
		{ID: "R5", State: "done", SHAs: []string{"deadbee1"}},   // done, sha unknown -> unlanded
		{ID: "R6", State: "🔶", SHAs: []string{d.landed}},        // partial with landed work -> noted, not judged
		{ID: "R7", State: "✅"},                                  // done, nothing to check -> unchecked
		{ID: "R8", State: "open", SHAs: []string{d.unlanded}},   // open, not landed -> clean
		{ID: "R10", State: "review", PRs: []string{"999"}},      // open, PR nowhere -> clean
		{ID: "R11", State: "✖", SHAs: []string{d.landed}},       // dropped -> skipped
		{ID: "R12", State: "weird", SHAs: []string{d.landed}},   // a state in no list -> unknown, not judged
		{ID: "R13", State: "done", SHAs: []string{"a1b2c3d"}},   // a tag name is no sha -> unlanded
	}
	rep, err := Check(rows, []Repo{{Path: d.dir, Branch: "integ"}}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := byID(rep)
	wantKinds := map[string]string{"R1": "stale", "R2": "stale", "R2g": "stale", "R2s": "stale", "X9": "stale",
		"R4": "unlanded", "R5": "unlanded", "R13": "unlanded"}
	if k := kinds(rep); !reflect.DeepEqual(k, wantKinds) {
		t.Fatalf("findings:\n got %v\nwant %v", k, wantKinds)
	}
	wantEv := map[string]string{"R1": "sha", "R2": "merge-subject", "R2g": "merge-subject", "R2s": "merge-subject", "X9": "row-id-subject"}
	for id, kind := range wantEv {
		ev := got[id].Evidence
		if len(ev) == 0 || ev[0].Kind != kind || ev[0].Commit == "" {
			t.Errorf("%s: evidence %+v, want kind %s with a commit", id, ev, kind)
		}
	}
	for id, want := range map[string]string{"R5": "not found", "R13": "not found", "R4": "not on integ"} {
		if m := strings.Join(got[id].Misses, ";"); !strings.Contains(m, want) {
			t.Errorf("%s misses %q, want %q", id, m, want)
		}
	}
	if !reflect.DeepEqual(rep.PartialLanded, []string{"R6"}) || !reflect.DeepEqual(rep.Unchecked, []string{"R7"}) ||
		!reflect.DeepEqual(rep.Unknown, []string{"R12 (weird)"}) {
		t.Errorf("partial %v unchecked %v unknown %v, want [R6] [R7] [R12 (weird)]", rep.PartialLanded, rep.Unchecked, rep.Unknown)
	}
	if rep.Rows != 16 || rep.Skipped != 1 || rep.Done != 5 || rep.Partial != 1 || rep.Open != 8 {
		t.Errorf("counts rows %d done %d open %d partial %d skipped %d", rep.Rows, rep.Done, rep.Open, rep.Partial, rep.Skipped)
	}
	if len(rep.Scans) != 1 || rep.Scans[0].Commits != 9 {
		t.Errorf("scans %+v, want one with 9 commits", rep.Scans)
	}
}

// A subject names a row only as a commit scope ("fix(R1): …", "feat(R4, R5): …") or as "ID:" at the start of the
// subject after an optional type prefix ("fix(web): R2: …"), case-sensitive, ids not split at dots; a Revert
// subject never names a row. Prose mentions ("P1 null check", "bump to v2", "unblocks X9") do not count, and a
// subject alone does not check a done row.
func TestSubjectNamesRowOnlyAsATag(t *testing.T) {
	root, _ := testutil.Temp(t)
	dir := testutil.GitRepo(t, filepath.Join(root, "repo"))
	for _, s := range []string{"init", "fix(review): P1 null check in parser", "feat(C1.M2.T): task done",
		`Revert "fix(Q7): thing"`, "chore: bump to v2 and s3 upload", "docs: plan for X8, unblocks X9",
		"fix(R1): a", "fix(web): R2: b", "R3: c", "feat(R4, R5): d", "test(r6): lowercase scope", "fix(R7): done row"} {
		commit(t, dir, date1, s)
	}
	var rows []Row
	for _, id := range []string{"P1", "C1", "Q7", "V2", "S3", "X9", "R1", "R2", "R3", "R4", "R5", "R6"} {
		rows = append(rows, Row{ID: id, State: "⬜"})
	}
	rows = append(rows, Row{ID: "R7", State: "✅"})
	rep, err := Check(rows, []Repo{{Path: dir, Branch: "main"}}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"R1": "stale", "R2": "stale", "R3": "stale", "R4": "stale", "R5": "stale"}
	if k := kinds(rep); !reflect.DeepEqual(k, want) {
		t.Fatalf("findings:\n got %v\nwant %v", k, want)
	}
	if !reflect.DeepEqual(rep.Unchecked, []string{"R7"}) {
		t.Fatalf("unchecked %v: a done row with only a subject naming it is not checked", rep.Unchecked)
	}
}

// Shas cited in markdown text may be baselines ("today header.tsx:66 (web <sha>)"): for an open row such a sha
// proves landing only when it is the right side of "A -> B" or its commit subject names the row.
func TestCitedShaOnOpenRow(t *testing.T) {
	d := demoRepo(t)
	board := "## ROWS\n" +
		"- ⬜ Z1 header: today header.tsx:66 (web " + d.landed + ") shows the sign-in link\n" +
		"- ⬜ Z2 LANDED integration " + d.landed + " -> " + d.tagged + "\n" +
		"- ⬜ Z3 built, see " + d.z3 + "\n"
	rows, _, err := ParseMarkdown(strings.NewReader(board), "ROWS")
	if err != nil {
		t.Fatal(err)
	}
	rep, err := Check(rows, []Repo{{Path: d.dir, Branch: "integ"}}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if k := kinds(rep); !reflect.DeepEqual(k, map[string]string{"Z2": "stale", "Z3": "stale"}) {
		t.Fatalf("findings %v, want Z2 and Z3 stale, Z1 clean\nrows %+v", k, rows)
	}
}

// A done row's shas: in a sha column (TSV) every one is the row's own, so any missing one makes it unlanded. In
// markdown text shas are cited and rows cite history (a commit from before a rebase, a red test on a rig branch), so
// the row is unlanded when the right of an "A -> B" is missing or when none of its cited shas is on the branch; the
// left of "A -> B" (the old tip) is never its own.
func TestDoneRowShas(t *testing.T) {
	d := demoRepo(t)
	board := "## ROWS\n" +
		"- ✅ Z5 LANDED " + d.landed + " -> 5eadbee1\n" + // the landing sha is missing
		"- ✅ Z6 LANDED " + d.landed + " (red was " + d.unlanded + " before the rebase)\n" + // history sha cited
		"- ✅ Z7 LANDED " + d.unlanded + " -> " + d.landed + "\n" + // old tip missing, landing sha on the branch
		"- ✅ Z8 LANDED " + d.unlanded + "\n" // its only sha is not on the branch
	rows, _, err := ParseMarkdown(strings.NewReader(board), "ROWS")
	if err != nil {
		t.Fatal(err)
	}
	rows = append(rows, Row{ID: "T9", State: "merged", SHAs: []string{d.landed, d.unlanded}}) // sha column: both own
	rep, err := Check(rows, []Repo{{Path: d.dir, Branch: "integ"}}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"Z5": "unlanded", "Z8": "unlanded", "T9": "unlanded"}
	if k := kinds(rep); !reflect.DeepEqual(k, want) {
		t.Fatalf("findings %v, want %v", k, want)
	}
}

// With several repos a PR number merged in more than one is ambiguous: no proof either way, and said so.
func TestPRInTwoReposIsAmbiguous(t *testing.T) {
	a, b := demoRepo(t), demoRepoAt(t, date2)
	rows := []Row{{ID: "R1", State: "review", PRs: []string{"101"}}, {ID: "R2", State: "merged", PRs: []string{"101"}}}
	rep, err := Check(rows, []Repo{{Path: a.dir, Branch: "integ"}, {Path: b.dir, Branch: "integ"}}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if k := kinds(rep); !reflect.DeepEqual(k, map[string]string{"R2": "unlanded"}) {
		t.Fatalf("findings %v, want only R2 unlanded", k)
	}
	if m := strings.Join(byID(rep)["R2"].Misses, ";"); !strings.Contains(m, "ambiguous") {
		t.Errorf("R2 misses %q, want 'ambiguous'", m)
	}
	if n := strings.Join(rep.Notes, ";"); !strings.Contains(n, "R1") || !strings.Contains(n, "ambiguous") {
		t.Errorf("notes %q, want R1's PR named ambiguous", n)
	}
}

// A sha in a row resolves only as an object id: no match is "not found", two commits are "ambiguous".
func TestPickCommit(t *testing.T) {
	for _, c := range []struct {
		in        []string
		full, why string
	}{{nil, "", "not found"}, {[]string{"aa"}, "aa", ""}, {[]string{"aa", "ab"}, "", "ambiguous"}} {
		full, why := pickCommit(c.in)
		if full != c.full || why != c.why {
			t.Errorf("pickCommit(%v) = %q %q, want %q %q", c.in, full, why, c.full, c.why)
		}
	}
}

// A sha landed in the second repo is landed: a row may name a web sha and a backend sha.
func TestCheckSeveralRepos(t *testing.T) {
	a, b := demoRepo(t), demoRepoAt(t, date2)
	rows := []Row{{ID: "R1", State: "merged", SHAs: []string{b.landed}}}
	rep, err := Check(rows, []Repo{{Path: a.dir, Branch: "integ"}, {Path: b.dir, Branch: "integ"}}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Findings) != 0 || rep.Done != 1 || len(rep.Scans) != 2 {
		t.Fatalf("a sha landed in the second repo is landed: findings %+v done %d scans %d", rep.Findings, rep.Done, len(rep.Scans))
	}
}

// Failing to look is loud: a missing branch, a folder that is no repo, a board with no rows.
func TestCheckFailsLoud(t *testing.T) {
	d := demoRepo(t)
	rows := []Row{{ID: "R1", State: "merged"}}
	if _, err := Check(rows, []Repo{{Path: d.dir, Branch: "nope"}}, cfg); err == nil || !strings.Contains(err.Error(), `branch "nope"`) {
		t.Errorf("missing branch: want an error naming it, got %v", err)
	}
	if _, err := Check(rows, []Repo{{Path: t.TempDir(), Branch: "integ"}}, cfg); err == nil {
		t.Errorf("a folder that is no git repo must fail")
	}
	if _, err := Check(nil, []Repo{{Path: d.dir, Branch: "integ"}}, cfg); err == nil || !strings.Contains(err.Error(), "0 rows") {
		t.Errorf("0 rows: want a '0 rows' error, got %v", err)
	}
}
