package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orzazade/crius"
	"github.com/orzazade/crius/internal/testutil"
)

func initHome(t *testing.T, home string) {
	t.Helper()
	if err := InitLayout(home, crius.ProjectsExample); err != nil {
		t.Fatal(err)
	}
}

func anyContains(probs []string, s string) bool {
	for _, p := range probs {
		if strings.Contains(p, s) {
			return true
		}
	}
	return false
}

func TestInitCreatesPrivateLayout(t *testing.T) {
	_, home := testutil.Temp(t)
	initHome(t, home)
	for _, sub := range []string{"bus", "state", "spool", "backup", "logs"} {
		if fi, err := os.Stat(filepath.Join(home, sub)); err != nil || !fi.IsDir() {
			t.Error(sub)
		}
	}
	if fi, _ := os.Stat(home); fi.Mode().Perm() != 0o700 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
	if fi, err := os.Stat(filepath.Join(home, "projects.toml")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("projects.toml: %v", err)
	}
}

func TestInitKeepsExistingProjectsFile(t *testing.T) {
	_, home := testutil.Temp(t)
	testutil.Write(t, filepath.Join(home, "projects.toml"), "# mine\n")
	initHome(t, home)
	if got := testutil.Read(t, filepath.Join(home, "projects.toml")); got != "# mine\n" {
		t.Fatal(got)
	}
}

func TestHomeFollowsEnv(t *testing.T) {
	_, home := testutil.Temp(t)
	if Home() != home {
		t.Fatal(Home())
	}
}

// leakSetup: a clean checkout with one tracked file, and a fresh private home outside every git tree.
func leakSetup(t *testing.T) (root, home, repo string) {
	root, home = testutil.Temp(t)
	repo = testutil.GitRepo(t, filepath.Join(root, "checkout"))
	testutil.Write(t, filepath.Join(repo, "README.md"), "code only\n")
	testutil.CommitAll(t, repo)
	initHome(t, home)
	return root, home, repo
}

func ignoreAllRepo(t *testing.T, root string) string {
	outer := testutil.GitRepo(t, filepath.Join(root, "outer"))
	testutil.Write(t, filepath.Join(outer, ".gitignore"), "*\n")
	testutil.Git(t, outer, "add", "-f", ".gitignore")
	testutil.Git(t, outer, "commit", "-q", "-m", "ignore all")
	return outer
}

func privateDecision(t *testing.T, home string) {
	testutil.Write(t, filepath.Join(home, "state", "demo", "DECISIONS.md"), "## demo-D3 · 2026-10-16\nDecision: x\n")
}

func TestLeakCleanSetupPasses(t *testing.T) {
	_, home, repo := leakSetup(t)
	if p := LeakProblems(home, repo); len(p) != 0 {
		t.Fatal(p)
	}
}

func TestLeakTrackedDBFails(t *testing.T) {
	_, home, repo := leakSetup(t)
	testutil.Write(t, filepath.Join(repo, "state", "record.db"), "x")
	testutil.CommitAll(t, repo)
	if p := LeakProblems(home, repo); !anyContains(p, "record.db") {
		t.Fatal(p)
	}
}

func TestLeakTrackedBusLogFails(t *testing.T) {
	_, home, repo := leakSetup(t)
	testutil.Write(t, filepath.Join(repo, "bus", "demo.log"), "x\n")
	testutil.CommitAll(t, repo)
	if len(LeakProblems(home, repo)) == 0 {
		t.Fatal("passed")
	}
}

func TestLeakHomeInsideWorkTreeFails(t *testing.T) {
	root, _, repo := leakSetup(t)
	outer := testutil.GitRepo(t, filepath.Join(root, "outer"))
	home := filepath.Join(outer, ".crius")
	initHome(t, home)
	if p := LeakProblems(home, repo); !anyContains(p, "work tree") {
		t.Fatal(p)
	}
}

func TestLeakHomeInsideWorkTreeThatIgnoresItPasses(t *testing.T) {
	root, _, repo := leakSetup(t)
	home := filepath.Join(ignoreAllRepo(t, root), ".crius")
	initHome(t, home)
	if p := LeakProblems(home, repo); len(p) != 0 {
		t.Fatal(p)
	}
}

func TestLeakTrackedFileUnderIgnoredHomeFails(t *testing.T) {
	root, _, repo := leakSetup(t)
	outer := testutil.GitRepo(t, filepath.Join(root, "outer"))
	testutil.Write(t, filepath.Join(outer, ".gitignore"), "*\n")
	home := filepath.Join(outer, ".crius")
	initHome(t, home)
	testutil.Git(t, outer, "add", "-f", ".gitignore", ".crius/projects.toml")
	testutil.Git(t, outer, "commit", "-q", "-m", "oops")
	if len(LeakProblems(home, repo)) == 0 {
		t.Fatal("passed")
	}
}

func TestLeakPrivateDecisionIDInTrackedFileFails(t *testing.T) {
	_, home, repo := leakSetup(t)
	privateDecision(t, home)
	testutil.Write(t, filepath.Join(repo, "NOTES.md"), "see demo-D3 for why\n")
	testutil.CommitAll(t, repo)
	if p := LeakProblems(home, repo); !anyContains(p, "demo-D3") {
		t.Fatal(p)
	}
}

func TestLeakLongerIDIsNotAMatch(t *testing.T) {
	_, home, repo := leakSetup(t)
	privateDecision(t, home)
	testutil.Write(t, filepath.Join(repo, "NOTES.md"), "see demo-D31\n")
	testutil.CommitAll(t, repo)
	if p := LeakProblems(home, repo); len(p) != 0 {
		t.Fatal(p)
	}
}

func TestLeakStagedPrivateIDFailsEvenIfWorktreeIsClean(t *testing.T) {
	_, home, repo := leakSetup(t)
	privateDecision(t, home)
	testutil.Write(t, filepath.Join(repo, "README.md"), "see demo-D3\n")
	testutil.Git(t, repo, "add", "README.md")
	testutil.Write(t, filepath.Join(repo, "README.md"), "code only\n")
	if p := LeakProblems(home, repo); !anyContains(p, "demo-D3") {
		t.Fatal(p)
	}
}

func TestLeakSymlinkedLayoutFolderFails(t *testing.T) {
	root, home, repo := leakSetup(t)
	if err := os.Remove(filepath.Join(home, "bus")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "elsewhere"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "elsewhere"), filepath.Join(home, "bus")); err != nil {
		t.Fatal(err)
	}
	if p := LeakProblems(home, repo); !anyContains(p, "symlink") {
		t.Fatal(p)
	}
}

func TestLeakUnusualAndNestedPrivateFileNamesFail(t *testing.T) {
	_, home, repo := leakSetup(t)
	names := []string{"café.db", "nested/bus/demo.log", "state/.gitkeep"}
	for _, n := range names {
		testutil.Write(t, filepath.Join(repo, n), "x\n")
	}
	testutil.CommitAll(t, repo)
	p := LeakProblems(home, repo)
	for _, n := range names {
		if !anyContains(p, n) {
			t.Errorf("%s not flagged: %v", n, p)
		}
	}
}

func TestLeakBrokenIndexOfTheEnclosingTreeIsAProblemNotAPass(t *testing.T) {
	root, _, repo := leakSetup(t)
	outer := ignoreAllRepo(t, root)
	testutil.Write(t, filepath.Join(outer, ".git", "index"), "garbage")
	home := filepath.Join(outer, ".crius")
	initHome(t, home)
	if len(LeakProblems(home, repo)) == 0 {
		t.Fatal("passed")
	}
}

func TestLeakThisCheckoutPassesWithAFreshHome(t *testing.T) {
	_, home := testutil.Temp(t)
	initHome(t, home)
	if p := LeakProblems(home, testutil.RepoRoot(t)); len(p) != 0 {
		t.Fatal(p)
	}
}

func TestLeakUnreadableProjectFolderIsAProblemNotAPass(t *testing.T) {
	_, home, repo := leakSetup(t)
	privateDecision(t, home)
	testutil.Write(t, filepath.Join(repo, "NOTES.md"), "see demo-D3\n")
	testutil.CommitAll(t, repo)
	dir := filepath.Join(home, "state", "demo")
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)
	if len(LeakProblems(home, repo)) == 0 {
		t.Fatal("passed although the decisions could not be read")
	}
}

func TestLeakHomeWithGlobCharactersStillFindsDecisionIDs(t *testing.T) {
	root, _, repo := leakSetup(t)
	home := filepath.Join(root, "priv[1]*")
	initHome(t, home)
	privateDecision(t, home)
	testutil.Write(t, filepath.Join(repo, "NOTES.md"), "see demo-D3\n")
	testutil.CommitAll(t, repo)
	if p := LeakProblems(home, repo); !anyContains(p, "demo-D3") {
		t.Fatal(p)
	}
}

func TestInitRefusesAFileWhereAFolderShouldBe(t *testing.T) {
	_, home := testutil.Temp(t)
	testutil.Write(t, filepath.Join(home, "state"), "not a folder")
	if err := InitLayout(home, crius.ProjectsExample); err == nil {
		t.Fatal("init accepted a file as the state folder")
	}
}
