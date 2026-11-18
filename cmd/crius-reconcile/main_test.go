package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orzazade/crius/internal/testutil"
)

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
	return root, repo, landed, unlanded
}

func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errw bytes.Buffer
	code := run(args, &out, &errw)
	return code, out.String(), errw.String()
}

func TestStaleAndUnlandedRowsExit4(t *testing.T) {
	root, repo, landed, unlanded := setup(t)
	board := filepath.Join(root, "board.tsv")
	testutil.Write(t, board, "row\tstate\tsha\tnote\n"+
		"R1\treview\t"+landed+"\t\n"+ // landed but not done
		"R2\treview\t\tPR 101\n"+ // its PR merged but not done
		"R3\tmerged\t"+landed+"\t\n"+ // fine
		"R4\tmerged\t"+unlanded+"\t\n") // says merged, not on the branch
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
