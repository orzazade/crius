package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
