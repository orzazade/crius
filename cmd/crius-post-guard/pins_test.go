package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

var (
	testPinSet  map[string][]string
	testPinFile string
)

// pinBody adds body's SHA-256 under name to this test's pin file, made on first use.
func pinBody(t *testing.T, name, body string) {
	t.Helper()
	if testPinSet == nil {
		testPinSet = map[string][]string{}
		testPinFile = filepath.Join(t.TempDir(), "post-guard-pins.json")
		old := pinsPath
		pinsPath = func() string { return testPinFile }
		t.Cleanup(func() { pinsPath, testPinSet = old, nil })
	}
	h := sha256.Sum256([]byte(body))
	testPinSet[name] = append(testPinSet[name], hex.EncodeToString(h[:]))
	b, err := json.Marshal(testPinSet)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(testPinFile, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// shippedCopies are the files testdata/pins.json pins: the copies kept beside the guard.
var shippedCopies = map[string]string{
	"ado-suite.ts":      "testdata/ado-suite/ado-suite.ts",
	"empty-bunfig.toml": "testdata/ado-suite/empty-bunfig.toml",
	"pr-build.sh":       "testdata/pr-build/pr-build.sh",
	"pass-post.sh":      "testdata/pass-post/pass-post.sh",
	"verdict-check.ts":  "testdata/pass-post/verdict-check.ts",
	"merge.sh":          "testdata/team/merge.sh",
	"merge-ok.sh":       "testdata/team/merge-ok.sh",
	"review-gate.sh":    "testdata/team/review-gate.sh",
	"click-check.sh":    "testdata/ops/click-check.sh",
	"ops-pins.sh":       "testdata/ops/ops-pins.sh",
}

// testdata/pins.json, the pin file to install with this build, pins exactly the copies in testdata: a change to a copy
// fails here until its pin moves with it.
func TestPinFileIsTheShippedCopies(t *testing.T) {
	b, err := os.ReadFile("testdata/pins.json")
	if err != nil {
		t.Fatal(err)
	}
	var pins map[string][]string
	if err := json.Unmarshal(b, &pins); err != nil {
		t.Fatal(err)
	}
	if len(pins) != len(shippedCopies) {
		t.Errorf("pins.json names %d files, testdata holds %d", len(pins), len(shippedCopies))
	}
	for name, p := range shippedCopies {
		body, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256(body)
		if got := hex.EncodeToString(h[:]); len(pins[name]) != 1 || pins[name][0] != got {
			t.Errorf("%s: pins.json has %v, %s is %s", name, pins[name], p, got)
		}
	}
}
