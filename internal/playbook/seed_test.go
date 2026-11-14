package playbook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A lesson file gives one candidate per paragraph, with its heading; tables and
// short lines are left out, and a second read gives nothing new.
func TestSeedSplitsLessonFileByParagraph(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "lessons.md")
	doc := "# Lessons\n\n## Tests\n\nRun the API tests in 3 parts: the whole suite took 26 min against a 20 min cap.\n\n| a | b |\n|---|---|\n\nshort\n\n## Bus\n\nA bus line does not wake a T3 thread; send the text to the thread as well.\n"
	if err := os.WriteFile(p, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	cur, _ := loadCursor(dir)
	cs, err := readDoc(p, cur, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 || !strings.HasPrefix(cs[0].Text, "Tests\n") || !strings.HasPrefix(cs[1].Text, "Bus\n") || cs[0].Team != "lessons" {
		t.Fatalf("candidates = %+v, want the two lesson paragraphs with their headings", cs)
	}
	if again, _ := readDoc(p, cur, time.Now()); len(again) != 0 {
		t.Fatalf("second read gave %d candidates, want 0", len(again))
	}
}
