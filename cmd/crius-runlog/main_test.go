package main

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/runlog"
)

func runlogCmd(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errs bytes.Buffer
	rc := run(args, &out, &errs)
	return rc, out.String(), errs.String()
}

// A hand check's log comes out in the form bus-post takes: its own reader (runlog.Proven) gives the sha back, and the
// first lines are exactly sha, pass, cmd.
func TestALogItWritesIsTakenByBusPostsReader(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CRIUS_HOME", home)
	rc, out, errs := runlogCmd(t, "crius-verify-axi-8448-ca96b1d6", "ca96b1d6", "7348",
		"Crius check of tip tree = QA land tree 8367756a", "gate 7348/0 is ms-flow's run, not re-run by Crius")
	if rc != 0 {
		t.Fatalf("rc %d: %s", rc, errs)
	}
	path := filepath.Join(home, "runs", "crius-verify-axi-8448-ca96b1d6.md")
	if !strings.Contains(out, path) {
		t.Fatalf("stdout does not name %s: %q", path, out)
	}
	sha, err := runlog.Proven(path, path)
	if err != nil || sha != "ca96b1d6" {
		t.Fatalf("bus-post's reader: sha %q, err %v", sha, err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "sha: ca96b1d6\npass: 7348\ncmd: Crius check of tip tree = QA land tree 8367756a\n" +
		"gate 7348/0 is ms-flow's run, not re-run by Crius\n"
	if string(b) != want {
		t.Fatalf("log is\n%s\nwant\n%s", b, want)
	}
}

// The header forms refused on 11-19 (words after the pass count) and other bad values are refused with a message that
// says what to type, and no file is written.
func TestBadHeaderValuesAreRefusedAndNothingIsWritten(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CRIUS_HOME", home)
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"words after pass", []string{"a", "ca96b1d6", "7348 of 7348 green", "cmd"}, `pass "7348 of 7348 green" is not a bare whole number`},
		{"pass with fails", []string{"a", "ca96b1d6", "7397/0", "cmd"}, `pass "7397/0" is not a bare whole number`},
		{"negative pass", []string{"a", "ca96b1d6", "-1", "cmd"}, `pass "-1" is not a bare whole number`},
		{"empty pass", []string{"a", "ca96b1d6", "", "cmd"}, `pass "" is not a bare whole number`},
		{"spaced pass", []string{"a", "ca96b1d6", " 7", "cmd"}, `pass " 7" is not a bare whole number`},
		{"zero pass", []string{"a", "ca96b1d6", "0", "cmd"}, "pass 0 proves nothing"},
		{"upper-case sha", []string{"a", "CA96B1D6", "1", "cmd"}, `sha "CA96B1D6" is not 7 to 40 lower-case hex`},
		{"short sha", []string{"a", "ca96b1", "1", "cmd"}, `sha "ca96b1" is not 7 to 40 lower-case hex`},
		{"long sha", []string{"a", strings.Repeat("a", 41), "1", "cmd"}, "is not 7 to 40 lower-case hex"},
		{"not hex", []string{"a", "row-F118", "1", "cmd"}, `sha "row-F118" is not 7 to 40`},
		{"name with a folder", []string{"../x", "ca96b1d6", "1", "cmd"}, `name "../x" is not a file name`},
		{"empty cmd", []string{"a", "ca96b1d6", "1", " \n "}, "cmd is empty"},
		{"note that reads as pass", []string{"a", "ca96b1d6", "1", "cmd", "pass: 99"}, `note 1 "pass: 99" reads as a header line`},
		{"note that reads as sha", []string{"a", "ca96b1d6", "1", "cmd", "sha: abcdef0"}, `note 1 "sha: abcdef0" reads as a header line`},
		{"sha with a line break", []string{"a", "ca96b1d6\n", "1", "cmd"}, `sha "ca96b1d6\n" is not 7 to 40 lower-case hex`},
		{"cmd that breaks the header", []string{"a", "ca96b1d6", "1", "cmd\npass: 99"}, ""},
		{"too few", []string{"a", "ca96b1d6", "1"}, "usage: crius-runlog"},
	} {
		rc, out, errs := runlogCmd(t, c.args...)
		if c.want == "" { // a line break in cmd is folded into one line; the pass stays the one typed
			if rc != 0 {
				t.Errorf("%s: rc %d: %s", c.name, rc, errs)
				continue
			}
			p := strings.Fields(out)[1]
			if _, pass, err := runlog.Read(p); err != nil || pass != 1 {
				t.Errorf("%s: pass %d, err %v", c.name, pass, err)
			}
			os.Remove(p)
			continue
		}
		if rc != 2 || !strings.Contains(errs, c.want) {
			t.Errorf("%s: want rc 2 and %q, got rc %d: %s", c.name, c.want, rc, errs)
		}
		if out != "" {
			t.Errorf("%s: stdout %q, want nothing", c.name, out)
		}
	}
	left, _ := os.ReadDir(filepath.Join(home, "runs"))
	for _, e := range left {
		t.Errorf("left in runs: %s", e.Name())
	}
}

// A run log is never replaced: a second write under the same name is refused and the first log stays as it was.
func TestARunLogIsNeverOverwritten(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CRIUS_HOME", home)
	if rc, _, errs := runlogCmd(t, "check.md", "abc1234", "3", "first"); rc != 0 {
		t.Fatalf("rc %d: %s", rc, errs)
	}
	path := filepath.Join(home, "runs", "check.md")
	before, _ := os.ReadFile(path)
	rc, out, errs := runlogCmd(t, "check", "def5678", "9", "second")
	if rc != 2 || !strings.Contains(errs, "is there already") || out != "" {
		t.Fatalf("want refused, got rc %d out %q: %s", rc, out, errs)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) || !strings.HasPrefix(string(after), "sha: abc1234\npass: 3\n") {
		t.Fatalf("log changed:\n%s", after)
	}
	left, _ := os.ReadDir(filepath.Join(home, "runs"))
	if len(left) != 1 {
		t.Fatalf("runs holds %d files, want 1 (no temp file left)", len(left))
	}
}

// The run form runs the check: exit 0 writes a log bus-post's reader takes and prints its path; a failed command
// writes a `fail:` log that Proven refuses, and exits 1; a bad form writes nothing and exits 2.
func TestRunFormRunsTheCheckAndWritesItsLog(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CRIUS_HOME", home)
	rc, out, errs := runlogCmd(t, "run", "cortex", "1e4d6df", "1e4d6df", "--", "sh", "-c", "echo 1e4d6df")
	if rc != 0 {
		t.Fatalf("rc %d: %s", rc, errs)
	}
	if !strings.Contains(errs, "1e4d6df") {
		t.Fatalf("the check's output is not echoed: %q", errs)
	}
	m, _ := filepath.Glob(filepath.Join(home, "runs", "cortex-1e4d6df-*.md"))
	if len(m) != 1 || !strings.Contains(out, m[0]) {
		t.Fatalf("logs %v, stdout %q", m, out)
	}
	if sha, err := runlog.Proven(m[0], m[0]); err != nil || sha != "1e4d6df" {
		t.Fatalf("bus-post's reader: sha %q, err %v", sha, err)
	}
	b, _ := os.ReadFile(m[0])
	if !strings.HasPrefix(string(b), "sha: 1e4d6df\npass: 1\ncmd: sh -c echo 1e4d6df\nexit: 0\n") || !strings.Contains(string(b), "\n| 1e4d6df\n") {
		t.Fatalf("log is\n%s", b)
	}

	rc, _, errs = runlogCmd(t, "run", "cortex", "71eb7a0", "71eb7a0", "--", "sh", "-c", "echo 'pass: 4'; exit 2")
	if rc != 1 || !strings.Contains(errs, "the check failed (exit 2)") {
		t.Fatalf("failed check: rc %d: %s", rc, errs)
	}
	m, _ = filepath.Glob(filepath.Join(home, "runs", "cortex-71eb7a0-*.md"))
	if len(m) != 1 {
		t.Fatalf("logs %v", m)
	}
	if _, err := runlog.Proven(m[0], m[0]); err == nil || !strings.Contains(err.Error(), "header has no pass count of 1 or more") {
		t.Fatalf("a failed check's log is not refused: %v", err)
	}

	// a check killed by a signal fails; a second check of one ref in the same second keeps its own log
	if rc, _, errs := runlogCmd(t, "run", "cortex", "71eb7a0", "71eb7a0", "--", "sh", "-c", "kill -9 $$"); rc != 1 || !strings.Contains(errs, "exit -1") {
		t.Fatalf("killed check: rc %d: %s", rc, errs)
	}
	for i := 0; i < 2; i++ {
		if rc, _, errs := runlogCmd(t, "run", "cortex", "twice", "1e4d6df", "--", "true"); rc != 0 {
			t.Fatalf("check %d of one ref: rc %d: %s", i+1, rc, errs)
		}
	}
	if m, _ := filepath.Glob(filepath.Join(home, "runs", "cortex-twice-*.md")); len(m) != 2 {
		t.Fatalf("two checks of one ref left %v", m)
	}

	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"run", "cortex", "x", "1e4d6df", "sh", "-c", "true"}, "usage: crius-runlog run"},
		{[]string{"run", "cortex", "../x", "1e4d6df", "--", "true"}, `ref "../x" is not part of a file name`},
		{[]string{"run", "cortex", "x", "NOTHEX", "--", "true"}, `sha "NOTHEX" is not 7 to 40`},
		{[]string{"run", "cortex", "x", "1e4d6df", "--", "/no/such/check-cmd"}, "the check did not start"},
	} {
		before, _ := filepath.Glob(filepath.Join(home, "runs", "*.md"))
		if rc, _, errs := runlogCmd(t, c.args...); rc != 2 || !strings.Contains(errs, c.want) {
			t.Fatalf("%v: rc %d, want %q: %s", c.args, rc, c.want, errs)
		}
		if after, _ := filepath.Glob(filepath.Join(home, "runs", "*.md")); len(after) != len(before) {
			t.Fatalf("%v wrote a log", c.args)
		}
	}
}

// Review of 8f9968b: the run form had no time limit, so a hung check blocked forever. With -limit it kills the
// check's whole process group (a child it left running too) when the limit runs out, writes a `fail: 1` log that
// says so, and exits 1.
func TestRunFormKillsAHungCheckAtItsLimit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CRIUS_HOME", home)
	pidFile := filepath.Join(home, "child.pid")
	rc, _, errs := runlogCmd(t, "run", "-limit", "1s", "cortex", "hung", "1e4d6df", "--",
		"sh", "-c", "sleep 999 & echo $! > "+pidFile+"; wait")
	if rc != 1 || !strings.Contains(errs, "timed out after 1s") {
		t.Fatalf("hung check: rc %d: %s", rc, errs)
	}
	m, _ := filepath.Glob(filepath.Join(home, "runs", "cortex-hung-*.md"))
	if len(m) != 1 {
		t.Fatalf("logs %v", m)
	}
	b, _ := os.ReadFile(m[0])
	if !strings.HasPrefix(string(b), "sha: 1e4d6df\nfail: 1\n") || !strings.Contains(string(b), "\nlimit: timed out after 1s;") {
		t.Fatalf("log is\n%s", b)
	}
	if _, err := runlog.Proven(m[0], m[0]); err == nil {
		t.Fatal("a timed-out check's log reads as passing")
	}
	pb, _ := os.ReadFile(pidFile)
	pid, err := strconv.Atoi(strings.TrimSpace(string(pb)))
	if err != nil {
		t.Fatalf("child pid %q: %v", pb, err)
	}
	gone := false
	for i := 0; i < 50 && !gone; i++ { // a killed child may take a moment to be reaped; this waits, it asserts no time
		gone = syscall.Kill(pid, 0) == syscall.ESRCH
		if !gone {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if !gone {
		syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("the check's child %d still runs after the limit", pid)
	}

	for _, bad := range []string{"0s", "-1s", "soon"} {
		if rc, _, errs := runlogCmd(t, "run", "-limit", bad, "cortex", "x", "1e4d6df", "--", "true"); rc != 2 {
			t.Fatalf("-limit %s: rc %d: %s", bad, rc, errs)
		}
	}
}

// Review of 8f9968b: the run form kept all of a check's output in memory. It keeps the head and the tail and says how
// many bytes it cut between them.
func TestRunFormKeepsTheHeadAndTailOfAHugeOutput(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CRIUS_HOME", home)
	rc, _, errs := runlogCmd(t, "run", "cortex", "big", "1e4d6df", "--",
		"sh", "-c", "echo FIRST; yes 0123456789 | head -c 4999995; echo LAST")
	if rc != 0 {
		t.Fatalf("rc %d: %.300s", rc, errs)
	}
	m, _ := filepath.Glob(filepath.Join(home, "runs", "cortex-big-*.md"))
	if len(m) != 1 {
		t.Fatalf("logs %v", m)
	}
	b, _ := os.ReadFile(m[0])
	cut := regexp.MustCompile(`\n… (\d+) bytes cut …\n`).FindStringSubmatch(string(b))
	if cut == nil || !strings.Contains(string(b), "\n| FIRST\n") || !strings.HasSuffix(string(b), "\n| LAST\n") {
		t.Fatalf("log of %d bytes has no bytes-cut note, or lost the head or tail:\n%.500s", len(b), b)
	}
	// The output was 6 + 4999995 + 5 bytes; what the log does not show is what it cut.
	n, _ := strconv.Atoi(cut[1])
	shown := 0
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "| ") {
			shown += len(l) - 2 + 1
		}
	}
	if n+shown != 5000006 {
		t.Fatalf("cut %d + shown %d bytes is not the 5000006 the check wrote", n, shown)
	}
}
