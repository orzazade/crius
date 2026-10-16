package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
