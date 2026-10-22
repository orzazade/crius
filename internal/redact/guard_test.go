package redact

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The repo is public and GitHub refuses a push that holds a token (Crius 00:49: two fixtures in leaks.jsonl). No
// tracked file may hold a raw match of a gitleaks token rule: a fixture writes its token with JSON escapes
// (sk_live_...), which the test decodes. Not checked: the generic rule (it matches go.sum hashes and every
// password = x fixture, and push protection looks for token formats), and the vendored rule files, which are
// upstream's as pinned.
// Untracked files count too: a gate run on files copied in before the commit passed while 86f33e3 held a token
// (Crius 10-22 17:2x).
func TestRepoHoldsNoRawToken(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("git", "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard").Output()
	if err != nil {
		t.Fatalf("git ls-files in %s: %v", root, err)
	}
	files := strings.Split(strings.TrimRight(string(out), "\x00"), "\x00")
	if len(files) < 50 {
		t.Fatalf("only %d tracked files under %s", len(files), root)
	}
	for _, hit := range rawTokens(t, root, files) {
		t.Error(hit)
	}
}

// The guard reads a file whole: a key over several lines is found (light review of 43fc99a).
func TestRawTokensFindsAKeyOverSeveralLines(t *testing.T) {
	dir := t.TempDir()
	key := "-----BEGIN " + "RSA PRIVATE KEY-----\n" + strings.Repeat(strings.Repeat("MIIEpAIBAAKCAQEA", 4)+"\n", 3) +
		"-----END " + "RSA PRIVATE KEY-----\n"
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("setup:\n"+key), 0o600); err != nil {
		t.Fatal(err)
	}
	if hits := rawTokens(t, dir, []string{"notes.md"}); len(hits) != 1 {
		t.Fatalf("got %q", hits)
	}
}

// rawTokens names each file (relative to root) holding a raw match of a gitleaks rule but the generic one.
func rawTokens(t *testing.T, root string, files []string) []string {
	t.Helper()
	var tokenRules []rule
	for _, r := range rules {
		if r.id != "generic-api-key" {
			tokenRules = append(tokenRules, r)
		}
	}
	if len(tokenRules) != len(rules)-1 {
		t.Fatalf("generic-api-key not among %d rules", len(rules))
	}
	var hits []string
	for _, f := range files {
		if strings.HasPrefix(f, "internal/redact/rules/") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, f))
		if err != nil {
			t.Fatal(err)
		}
		if _, n := redactRules(string(b), tokenRules); n > 0 {
			hits = append(hits, f+": a gitleaks token rule matches")
		}
	}
	return hits
}
