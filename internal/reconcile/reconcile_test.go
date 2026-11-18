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

// A TSV board: columns found by name in any order; shas come from the sha column, PR numbers from any cell.
func TestParseTSV(t *testing.T) {
	board := "flow\trow\towner\tstate\tqa\tsha\tnote\n" +
		"f1\tR1\tb1\treview\tpass\tabc1234\tlanded as PR 101\n" +
		"f1\tR2\tb2\tmerged\tfail\tabc1234 9f8e7d6c\tsee !102 and PR#103, #104\n" +
		"\n" +
		"f2\tR3\tb1\tproven\t\t\tnone; baseline deadbee0 is not a row sha\n"
	rows, err := ParseTSV(strings.NewReader(board))
	if err != nil {
		t.Fatal(err)
	}
	want := []Row{
		{ID: "R1", State: "review", Line: 2, SHAs: []string{"abc1234"}, PRs: []string{"101"}},
		{ID: "R2", State: "merged", Line: 3, SHAs: []string{"abc1234", "9f8e7d6c"}, PRs: []string{"102", "103", "104"}},
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

// A markdown board: bullet rows "- <mark> <id> text" under one section, table rows "| id | state | … |". A gate
// fingerprint (fp-<hex>) is not a sha; a bullet whose first word after the mark has no digit is not a row.
func TestParseMarkdown(t *testing.T) {
	board := "# Board\n" +
		"## OTHER\n" +
		"- ✅ Z1 not in the section\n" +
		"## READINESS — counted\n" +
		"Group one\n" +
		"- ✅ X1 first thing (abc1234, gate fp-0123456789abcdef)\n" +
		"- ⬜ X2b second thing\n" +
		"  - FOLLOW-UP X2b-1 not a row\n" +
		"- ✖ DROPPED by the owner: X3\n" +
		"- 🔶 X4: part landed (TRAIN 3, 9f8e7d6c) PR 55\n" +
		"| row | state | sha |\n" +
		"|-----|-------|-----|\n" +
		"| X5 | review | abc1234 |\n" +
		"## NEXT\n" +
		"- ⬜ Z2 after the section\n"
	rows, err := ParseMarkdown(strings.NewReader(board), "READINESS")
	if err != nil {
		t.Fatal(err)
	}
	want := []Row{
		{ID: "X1", State: "✅", Line: 6, SHAs: []string{"abc1234"}},
		{ID: "X2b", State: "⬜", Line: 7},
		{ID: "X4", State: "🔶", Line: 10, SHAs: []string{"9f8e7d6c"}, PRs: []string{"55"}},
		{ID: "X5", State: "review", Line: 13, SHAs: []string{"abc1234"}},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows:\n got %+v\nwant %+v", rows, want)
	}
	if _, err := ParseMarkdown(strings.NewReader(board), "NO SUCH"); err == nil || !strings.Contains(err.Error(), "no section") {
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

func commit(t *testing.T, dir, msg string) string {
	t.Helper()
	git(t, dir, "commit", "-q", "--allow-empty", "-m", msg)
	return git(t, dir, "rev-parse", "--short=8", "HEAD")
}

// Demo is a synthetic repo: branch integ holds a plain commit, an Azure-style merge, a GitHub merge, a squash and a
// commit naming row X9; branch side holds a commit that never landed.
type demo struct{ dir, landed, unlanded string }

func demoRepo(t *testing.T) demo {
	t.Helper()
	root, _ := testutil.Temp(t)
	dir := testutil.GitRepo(t, filepath.Join(root, "repo"))
	commit(t, dir, "init")
	git(t, dir, "checkout", "-q", "-b", "side")
	unlanded := commit(t, dir, "feat: never merged")
	git(t, dir, "checkout", "-q", "main")
	git(t, dir, "checkout", "-q", "-b", "integ")
	d := demo{dir: dir, unlanded: unlanded}
	d.landed = commit(t, dir, "feat: the thing")
	commit(t, dir, "Merged PR 101: add y")
	commit(t, dir, "Merge pull request #202 from someone/branch")
	commit(t, dir, "add z (#303)")
	commit(t, dir, "fix(X9): z")
	commit(t, dir, "review P3-6 follow-up")
	return d
}

var cfg = Config{Done: []string{"merged", "done", "proven", "landed", "✅"}, Partial: []string{"🔶"}, Skip: []string{"✖"}, Depth: 100}

func byID(r Report) map[string]Finding {
	m := map[string]Finding{}
	for _, f := range r.Findings {
		m[f.Row.ID] = f
	}
	return m
}

func TestCheck(t *testing.T) {
	d := demoRepo(t)
	rows := []Row{
		{ID: "R1", State: "review", SHAs: []string{d.landed}},   // not done, sha landed -> stale
		{ID: "R2", State: "Review", PRs: []string{"101"}},       // not done, merge subject names its PR -> stale
		{ID: "R2g", State: "review", PRs: []string{"202"}},      // GitHub merge subject -> stale
		{ID: "R2s", State: "review", PRs: []string{"303"}},      // GitHub squash subject -> stale
		{ID: "X9", State: "⬜"},                                  // row id in a subject -> stale
		{ID: "P3", State: "⬜"},                                  // "P3-6" is another word, not P3 -> clean
		{ID: "R3", State: "merged", SHAs: []string{d.landed}},   // done and landed -> clean
		{ID: "R4", State: "MERGED", SHAs: []string{d.unlanded}}, // done, sha not on the branch -> unlanded
		{ID: "R5", State: "done", SHAs: []string{"deadbee1"}},   // done, sha unknown -> unlanded
		{ID: "R6", State: "🔶", SHAs: []string{d.landed}},        // partial with landed work -> noted, not judged
		{ID: "R7", State: "✅"},                                  // done, nothing to check -> unchecked
		{ID: "R8", State: "open", SHAs: []string{d.unlanded}},   // open, not landed -> clean
		{ID: "R10", State: "review", PRs: []string{"999"}},      // open, PR nowhere -> clean
		{ID: "R11", State: "✖", SHAs: []string{d.landed}},       // dropped -> skipped
	}
	rep, err := Check(rows, []Repo{{Path: d.dir, Branch: "integ"}}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := byID(rep)
	wantKinds := map[string]string{"R1": "stale", "R2": "stale", "R2g": "stale", "R2s": "stale", "X9": "stale",
		"R4": "unlanded", "R5": "unlanded"}
	kinds := map[string]string{}
	for id, f := range got {
		kinds[id] = f.Kind
	}
	if !reflect.DeepEqual(kinds, wantKinds) {
		t.Fatalf("findings:\n got %v\nwant %v", kinds, wantKinds)
	}
	wantEv := map[string]string{"R1": "sha", "R2": "merge-subject", "R2g": "merge-subject", "R2s": "merge-subject", "X9": "row-id-subject"}
	for id, kind := range wantEv {
		ev := got[id].Evidence
		if len(ev) == 0 || ev[0].Kind != kind || ev[0].Commit == "" {
			t.Errorf("%s: evidence %+v, want kind %s with a commit", id, ev, kind)
		}
	}
	if !strings.Contains(strings.Join(got["R5"].Misses, ";"), "not found") {
		t.Errorf("R5 misses %v, want the unknown sha named", got["R5"].Misses)
	}
	if !strings.Contains(strings.Join(got["R4"].Misses, ";"), "not on integ") {
		t.Errorf("R4 misses %v, want 'not on integ'", got["R4"].Misses)
	}
	if !reflect.DeepEqual(rep.PartialLanded, []string{"R6"}) || !reflect.DeepEqual(rep.Unchecked, []string{"R7"}) {
		t.Errorf("partial %v unchecked %v, want [R6] [R7]", rep.PartialLanded, rep.Unchecked)
	}
	if rep.Rows != 14 || rep.Skipped != 1 || rep.Done != 4 || rep.Partial != 1 || rep.Open != 8 {
		t.Errorf("counts rows %d done %d open %d partial %d skipped %d", rep.Rows, rep.Done, rep.Open, rep.Partial, rep.Skipped)
	}
	if len(rep.Scans) != 1 || rep.Scans[0].Commits != 7 {
		t.Errorf("scans %+v, want one with 7 commits", rep.Scans)
	}
}

// A sha on the branch of any of the repos counts: a row may name a web sha and a backend sha.
func TestCheckSeveralRepos(t *testing.T) {
	a, b := demoRepo(t), demoRepo(t)
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

// A TSV saved by a spreadsheet may start with a byte-order mark; the first column name is still "row".
func TestParseTSVByteOrderMark(t *testing.T) {
	rows, err := ParseTSV(strings.NewReader("\ufeffrow\tstate\nR1\tdone\n"))
	if err != nil || len(rows) != 1 || rows[0].ID != "R1" {
		t.Fatalf("rows %+v err %v, want one row R1", rows, err)
	}
}
