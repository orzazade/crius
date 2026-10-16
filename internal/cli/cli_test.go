package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
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
	if log := testutil.Read(t, filepath.Join(home, "bus", "demo.log")); !strings.Contains(log, "[redacted]") {
		t.Fatal(log)
	}
}
