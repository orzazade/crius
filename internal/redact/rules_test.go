package redact

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

// The embedded JSON is the vendored TOML converted (bin/refresh-gitleaks-rules): same sha, a full rule set.
func TestGitleaksRulesAreTheVendoredFile(t *testing.T) {
	var h struct {
		TOMLSHA256 string `json:"toml_sha256"`
		Rules      []json.RawMessage
	}
	if err := json.Unmarshal(gitleaksJSON, &h); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("rules/gitleaks.toml")
	if err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256(b); hex.EncodeToString(sum[:]) != h.TOMLSHA256 {
		t.Fatalf("rules/gitleaks.toml is not the file gitleaks.json was made from (%s)", h.TOMLSHA256)
	}
	if len(rules) != len(h.Rules) || len(rules) < 200 {
		t.Fatalf("%d rules loaded of %d", len(rules), len(h.Rules))
	}
}

// A rule with an entropy floor keeps a low-entropy value, and only the secret group goes, as in gitleaks.
func TestGitleaksRulesKeepWhatTheyDoNotReport(t *testing.T) {
	for in, want := range map[string]string{"cache_key = aaaaaaaaaaaaaaaa": "cache_key = aaaaaaaaaaaaaaaa",
		"auth_code = 9fK2xQ7pLm4Zr8Tb": "auth_code = [redacted]"} {
		if out, _ := Redact(in); out != want {
			t.Errorf("%q -> %q, want %q", in, out, want)
		}
	}
}
