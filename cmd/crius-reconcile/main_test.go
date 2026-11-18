package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/orzazade/crius/internal/testutil"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	// a fixed date keeps the shas the same on every run, so none comes out as a word a board would not read as a sha
	const date = "2026-01-02T03:04:05Z"
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// setup makes a repo whose branch integ holds a commit and an Azure-style merge of PR 101, and a side commit that
// never landed.
func setup(t *testing.T) (root, repo, landed, unlanded string) {
	t.Helper()
	root, _ = testutil.Temp(t)
	repo = testutil.GitRepo(t, filepath.Join(root, "repo"))
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	git(t, repo, "checkout", "-q", "-b", "side")
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "feat: never merged")
	unlanded = git(t, repo, "rev-parse", "--short=8", "HEAD")
	git(t, repo, "checkout", "-q", "-b", "integ", "main")
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "feat: the thing")
	landed = git(t, repo, "rev-parse", "--short=8", "HEAD")
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "Merged PR 101: add y")
	for _, s := range []string{landed, unlanded} {
		if !shaLike.MatchString(s) || !strings.ContainsAny(s, "abcdef") || !strings.ContainsAny(s, "0123456789") {
			t.Fatalf("sha %s would not be read as a sha by a board: change the date", s)
		}
	}
	return root, repo, landed, unlanded
}

var shaLike = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errw bytes.Buffer
	code := run(args, &out, &errw)
	return code, out.String(), errw.String()
}

func TestStaleAndUnlandedRowsExit4(t *testing.T) {
	root, repo, landed, unlanded := setup(t)
	board := filepath.Join(root, "board.tsv")
	testutil.Write(t, board, "row\tstate\tsha\tpr\tnote\n"+
		"R1\treview\t"+landed+"\t\t\n"+ // landed but not done
		"R2\treview\t\t101\t\n"+ // its PR merged but not done
		"R3\tmerged\t"+landed+"\t\tafter PR 101\n"+ // fine: a PR in the notes is no claim
		"R4\tmerged\t"+unlanded+"\t\t\n") // says merged, not on the branch
	code, out, errw := runCLI(t, "--board", board, "--format", "tsv", "--git", repo+":integ")
	if code != 4 {
		t.Fatalf("exit %d, want 4\nout:\n%s\nerr:\n%s", code, out, errw)
	}
	for _, want := range []string{"STALE R1", "sha " + landed, "STALE R2", "Merged PR 101", "UNLANDED R4", "not on integ",
		"4 rows", "2 stale", "1 unlanded"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "R3 ") {
		t.Errorf("R3 is clean and must not be listed:\n%s", out)
	}
}

func TestCleanBoardExit0(t *testing.T) {
	root, repo, landed, _ := setup(t)
	board := filepath.Join(root, "board.md")
	testutil.Write(t, board, "## ROWS\n- ✅ X1 done ("+landed+")\n- ⬜ X2 not started\n")
	code, out, errw := runCLI(t, "--board", board, "--format", "md", "--section", "ROWS", "--git", repo+":integ")
	if code != 0 {
		t.Fatalf("exit %d, want 0\nout:\n%s\nerr:\n%s", code, out, errw)
	}
	if !strings.Contains(out, "2 rows") || !strings.Contains(out, "0 stale") {
		t.Errorf("summary missing:\n%s", out)
	}
}

// A board that parses to no rows is a failed look, never a clean board.
func TestZeroRowsExit1(t *testing.T) {
	root, repo, _, _ := setup(t)
	board := filepath.Join(root, "board.tsv")
	testutil.Write(t, board, "row\tstate\tsha\n")
	code, out, errw := runCLI(t, "--board", board, "--format", "tsv", "--git", repo+":integ")
	if code != 1 || !strings.Contains(errw, "0 rows") {
		t.Fatalf("exit %d err %q, want 1 and '0 rows'\nout:\n%s", code, errw, out)
	}
}

func TestMissingBranchExit1(t *testing.T) {
	root, repo, landed, _ := setup(t)
	board := filepath.Join(root, "board.tsv")
	testutil.Write(t, board, "row\tstate\tsha\nR1\tmerged\t"+landed+"\n")
	code, _, errw := runCLI(t, "--board", board, "--format", "tsv", "--git", repo+":nope")
	if code != 1 || !strings.Contains(errw, `branch "nope"`) {
		t.Fatalf("exit %d err %q, want 1 naming the branch", code, errw)
	}
	code, _, errw = runCLI(t, "--board", filepath.Join(root, "missing.tsv"), "--format", "tsv", "--git", repo+":integ")
	if code != 1 || !strings.Contains(errw, "missing.tsv") {
		t.Fatalf("unreadable board: exit %d err %q, want 1 naming the file", code, errw)
	}
}

func TestBadArgsExit2(t *testing.T) {
	for _, args := range [][]string{
		{"--board", "b.tsv", "--format", "tsv"},                      // no --git
		{"--board", "b.tsv", "--format", "csv", "--git", "/x:integ"}, // unknown format
		{"--format", "tsv", "--git", "/x:integ"},                     // no --board
		{"--board", "b.tsv", "--format", "tsv", "--git", "/x"},       // no branch
	} {
		if code, _, _ := runCLI(t, args...); code != 2 {
			t.Errorf("%q: exit %d, want 2", args, code)
		}
	}
}

// A git error while reading (here a commit object missing from the history: git log exits 128) is a failed look.
func TestGitErrorExit1(t *testing.T) {
	root, repo, landed, _ := setup(t)
	full := git(t, repo, "rev-parse", landed)
	if err := os.Remove(filepath.Join(repo, ".git", "objects", full[:2], full[2:])); err != nil {
		t.Fatal(err)
	}
	board := filepath.Join(root, "board.tsv")
	testutil.Write(t, board, "row\tstate\tsha\nR1\tmerged\t"+landed+"\n")
	code, out, errw := runCLI(t, "--board", board, "--format", "tsv", "--git", repo+":integ")
	if code != 1 || !strings.Contains(errw, "FAILED TO LOOK") || !strings.Contains(errw, "exit 128") {
		t.Fatalf("exit %d err %q, want 1 naming git's exit 128\nout:\n%s", code, errw, out)
	}
}

// Lines the parser could not read and rows in a state no list names are shown and counted, never silently dropped;
// they are not mismatches, so the exit stays 0.
func TestNotReadAndUnknownStateShown(t *testing.T) {
	root, repo, landed, _ := setup(t)
	board := filepath.Join(root, "board.md")
	testutil.Write(t, board, "## ROWS\n- ✅ X1 done ("+landed+")\n- ⬜ X6/X7 two ids\n| X2 | weird |\n")
	code, out, errw := runCLI(t, "--board", board, "--format", "md", "--section", "ROWS", "--git", repo+":integ")
	if code != 0 {
		t.Fatalf("exit %d, want 0\nout:\n%s\nerr:\n%s", code, out, errw)
	}
	for _, want := range []string{"NOT-READ line 3: - ⬜ X6/X7 two ids", "1 not read", "unknown state: X2 (weird)", "1 unknown"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// A PR merged in two repos leaves a done row ambiguous: its own kind, never worded as "not on the branch".
func TestAmbiguousDoneRow(t *testing.T) {
	root, repo, _, _ := setup(t)
	other := testutil.GitRepo(t, filepath.Join(root, "other"))
	git(t, other, "commit", "-q", "--allow-empty", "-m", "Merged PR 101: elsewhere")
	board := filepath.Join(root, "board.md")
	testutil.Write(t, board, "## ROWS\n- ✅ X9 merged as PR 101\n")
	code, out, errw := runCLI(t, "--board", board, "--format", "md", "--section", "ROWS", "--git", repo+":integ", "--git", other+":main")
	if code != 4 || !strings.Contains(out, "AMBIGUOUS X9") || strings.Contains(out, "not on the branch") || !strings.Contains(out, "1 ambiguous") {
		t.Fatalf("exit %d, want 4 with AMBIGUOUS X9 and no 'not on the branch'\nout:\n%s\nerr:\n%s", code, out, errw)
	}
}

// A board where every row is in a state no list names judged nothing: exit 1, not a clean 0.
func TestNothingJudgedExit1(t *testing.T) {
	root, repo, _, _ := setup(t)
	board := filepath.Join(root, "board.md")
	testutil.Write(t, board, "## ROWS\n- ✔ R1 x\n- ✔ R2 y\n")
	code, out, errw := runCLI(t, "--board", board, "--format", "md", "--section", "ROWS", "--git", repo+":integ")
	if code != 1 || !strings.Contains(errw, "no row was judged") {
		t.Fatalf("exit %d err %q, want 1 and 'no row was judged'\nout:\n%s", code, errw, out)
	}
}
