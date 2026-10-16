package redact

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The repo is public and GitHub refuses a push that holds a token (Crius 00:49: two fixtures in leaks.jsonl). No file
// may hold a raw match of a gitleaks token rule: a fixture writes its token with JSON escapes (sk_live_...),
// which the test decodes. Not checked: the generic rule (it matches go.sum hashes and every password = x fixture, and
// push protection looks for token formats), and the vendored rule files, which are upstream's as pinned.
func TestRepoHoldsNoRawToken(t *testing.T) {
	var tokenRules []rule
	for _, r := range rules {
		if r.id != "generic-api-key" {
			tokenRules = append(tokenRules, r)
		}
	}
	if len(tokenRules) != len(rules)-1 {
		t.Fatalf("generic-api-key not among %d rules", len(rules))
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repo root %s: %v", root, err)
	}
	files := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || path == filepath.Join(root, "internal", "redact", "rules") {
				return filepath.SkipDir
			}
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.IndexByte(b, 0) >= 0 {
			return nil // binary
		}
		files++
		for i, l := range strings.Split(string(b), "\n") {
			if _, n := redactRules(l, tokenRules); n > 0 {
				rel, _ := filepath.Rel(root, path)
				t.Errorf("%s:%d holds a token a gitleaks rule matches", rel, i+1)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files < 50 {
		t.Fatalf("only %d files read under %s", files, root)
	}
}
