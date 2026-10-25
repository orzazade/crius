package experiments

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orzazade/crius/internal/testutil"
)

// E8 (crius:lead 18:38): the rig's newest quote count says how many test quotes on 7090 are run leftovers.
func TestLeftoversAreReadFromTheNewestRigCount(t *testing.T) {
	_, dir := testutil.Temp(t)
	count := func(stamp, n7090 string) {
		testutil.Write(t, filepath.Join(dir, "quote-count-"+stamp, "run.log"), "== 7090: 499 quote files (25 marked deleted)\n"+
			"  broken: 104 []\n  run leftovers not named, not seed: "+n7090+"\n== 7111: 8 quote files (0 marked deleted)\n"+
			"  run leftovers not named, not seed: 0\n")
	}
	if _, _, err := Leftovers(dir)(); err == nil {
		t.Fatal("a missing rig folder counted as no count")
	}
	os.MkdirAll(dir, 0o700)
	if n, at, err := Leftovers(dir)(); err != nil || n != 0 || !at.IsZero() {
		t.Fatal("no count yet:", n, at, err)
	}
	count("20261025T0052", "414")
	count("20261025T1815", "436")
	testutil.Write(t, filepath.Join(dir, "quote-count-notes.txt"), "x")
	n, at, err := Leftovers(dir)()
	if err != nil || n != 436 || at.Format("2006-01-02 15:04") != "2026-10-25 18:15" {
		t.Fatal(n, at, err)
	}
	// A count whose log no longer has the line is a format change: loud, not zero.
	testutil.Write(t, filepath.Join(dir, "quote-count-20261026T0900", "run.log"), "== 7090: 12 quote files\n")
	if _, _, err := Leftovers(dir)(); err == nil || !strings.Contains(err.Error(), "run leftovers") {
		t.Fatal(err)
	}
}

// E8: the worktrees besides the main checkout, and their size.
func TestWorktreesAreCountedBesideTheMainCheckout(t *testing.T) {
	_, dir := testutil.Temp(t)
	repo := filepath.Join(dir, "repo")
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatal(err, string(out))
		}
	}
	os.MkdirAll(repo, 0o700)
	git("init", "-q")
	git("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "x")
	git("worktree", "add", "-q", filepath.Join(dir, "w1"))
	git("worktree", "add", "-q", filepath.Join(dir, "w2"))
	testutil.Write(t, filepath.Join(dir, "w1", "big"), strings.Repeat("x", 1<<20))
	n, gb, err := Worktrees(repo)()
	if err != nil || n != 2 || gb < 0.0009 || gb > 0.01 {
		t.Fatal(n, gb, err)
	}
	if _, _, err := Worktrees(filepath.Join(dir, "nope"))(); err == nil {
		t.Fatal("a missing repo counted as no worktrees")
	}
}
