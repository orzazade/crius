package redact

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// Step 4b (Crius 23:40): every leak case of the six review rounds and every known gap, from testdata/leaks.jsonl.
// None may print its secret, alone or inside a JSON string.
func TestLeakFixtures(t *testing.T) {
	f, err := os.Open("testdata/leaks.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	n := 0
	for sc := bufio.NewScanner(f); sc.Scan(); n++ {
		var c struct{ Src, In, Secret string }
		if err := json.Unmarshal(sc.Bytes(), &c); err != nil || c.In == "" || c.Secret == "" {
			t.Fatalf("line %d: %v %q", n+1, err, sc.Text())
		}
		if out, _ := Redact(c.In); strings.Contains(out, c.Secret) {
			t.Errorf("%s: Redact(%q) = %q", c.Src, c.In, out)
		}
		if b, err := JSON(map[string]any{"text": c.In}); err != nil || strings.Contains(string(b), c.Secret) {
			t.Errorf("%s: JSON(%q) = %s %v", c.Src, c.In, b, err)
		}
	}
	if n < 70 {
		t.Fatalf("only %d fixtures", n)
	}
}
