package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The words after the role: --from-ready once, --dry-run at most once, any order, nothing else (so "--dry-run" alone is
// never taken as a brief to send).
func TestFromReadyArgs(t *testing.T) {
	for args, want := range map[string][2]bool{
		"--from-ready":                         {false, true},
		"--from-ready --dry-run":               {true, true},
		"--dry-run --from-ready":               {true, true},
		"--dry-run":                            {false, false},
		"--from-ready --from-ready":            {false, false},
		"--from-ready --dry-run --dry-run":     {false, false},
		"--from-ready extra":                   {false, false},
		"--from-ready --dry-run Read BRIEF.md": {false, false},
	} {
		dry, ok := fromReadyArgs(strings.Fields(args))
		if dry != want[0] || ok != want[1] {
			t.Errorf("%q: dry %v ok %v, want %v %v", args, dry, ok, want[0], want[1])
		}
	}
}

// On this machine the save keeps the old text as prev, writes the new text with the template's mode kept (a read-only
// template stays read-only), and refuses, changing nothing, when prev already exists.
func TestMacSaveKeepsPrevAndMode(t *testing.T) {
	dir := t.TempDir()
	path, prev := filepath.Join(dir, "RESTART-qa.md"), filepath.Join(dir, "RESTART-qa.md.prev-2305")
	if err := os.WriteFile(path, []byte("old"), 0o464); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o464); err != nil {
		t.Fatal(err)
	}
	if err := macSave(path, prev, []byte("new")); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]string{path: "new", prev: "old"} {
		b, err := os.ReadFile(p)
		if err != nil || string(b) != want {
			t.Errorf("%s holds %q (%v), want %q", p, b, err, want)
		}
		if st, err := os.Stat(p); err != nil || st.Mode().Perm() != 0o464 {
			t.Errorf("%s mode %v (%v), want -r--rw-r--", p, st.Mode().Perm(), err)
		}
	}
	err := macSave(path, prev, []byte("newer"))
	if err == nil || !strings.Contains(err.Error(), "could not keep the old copy") {
		t.Errorf("a second save into an existing prev: %v", err)
	}
	if b, _ := os.ReadFile(path); string(b) != "new" {
		t.Errorf("the refused save changed the template to %q", b)
	}
	if left, _ := filepath.Glob(filepath.Join(dir, "*.new-*")); len(left) != 0 {
		t.Errorf("left %q", left)
	}
}

// On a box the paths go quoted, a leading ~/ outside the quotes so the box expands it, and prev is checked first.
func TestBoxSaveCmdQuotesThePaths(t *testing.T) {
	got := boxSaveCmd("~/b/RESTART-qa.md", "~/b/RESTART-qa.md.prev-2305")
	if !strings.HasPrefix(got, "set -e; if [ -e ~/'b/RESTART-qa.md.prev-2305' ]; then") {
		t.Errorf("got %q", got)
	}
	for _, h := range []string{"cp -p -- ~/'b/RESTART-qa.md' ~/'b/RESTART-qa.md.prev-2305'", "cat > ~/'b/RESTART-qa.md.new'",
		"chmod --reference ~/'b/RESTART-qa.md' -- ~/'b/RESTART-qa.md.new'", "mv -f -- ~/'b/RESTART-qa.md.new' ~/'b/RESTART-qa.md'"} {
		if !strings.Contains(got, h) {
			t.Errorf("got %q, lacks %q", got, h)
		}
	}
	if q := shQuote("/abs/RESTART-qa.md"); q != "'/abs/RESTART-qa.md'" {
		t.Errorf("absolute path quoted as %s", q)
	}
}
