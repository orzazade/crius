package playbook

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeSession(t *testing.T, path string, texts []string) {
	t.Helper()
	var b strings.Builder
	for i, s := range texts {
		fmt.Fprintf(&b, `{"type":"assistant","uuid":"%s-%d","sessionId":"s","cwd":"/Users/x/Projects/scifi/crius","entrypoint":"cli","timestamp":"2026-11-20T1%d:00:00Z","message":{"role":"assistant","content":[{"type":"text","text":%q}]}}`+"\n",
			filepath.Base(path), i, i%10, s)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A long session with many replies that match keeps only the strongest few of a day.
func TestTranscriptCapPerSessionDay(t *testing.T) {
	dir := t.TempDir()
	var texts []string
	for i := 0; i < 9; i++ {
		texts = append(texts, fmt.Sprintf("The build is slower because the cache is cold: %d s to %d s.", 100+i, 50+i))
	}
	texts = append(texts, "Root cause: the cache key had the date in it, so every run missed. 702 s to 198 s.")
	writeSession(t, filepath.Join(dir, "p", "long.jsonl"), texts)
	cur, _ := loadCursor(filepath.Join(dir, "state"))
	cs, st, err := Gather(Config{TxRoots: []string{filepath.Join(dir, "p")}, Since: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)}, cur)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != perSessionDay || st.TxCapped != 10-perSessionDay {
		t.Fatalf("got %d candidates, %d capped; want %d, %d", len(cs), st.TxCapped, perSessionDay, 10-perSessionDay)
	}
	strong := false
	for _, c := range cs {
		strong = strong || strings.Contains(c.Text, "Root cause")
	}
	if !strong {
		t.Fatal("the strong finding was capped out")
	}
	// One number with a cause word is a status line, not a finding.
	if score("Tests are slower because of the rig: 3 of 9 rows done in 40 s.") != 0 {
		t.Fatal("one measurement scored as a finding")
	}
}
