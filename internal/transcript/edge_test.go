package transcript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func userLine(uuid, ts string, content any) string {
	c, _ := json.Marshal(content)
	return `{"type":"user","uuid":"` + uuid + `","sessionId":"s","cwd":"/p","entrypoint":"cli","timestamp":"` + ts + `","origin":{"kind":"human"},"message":{"role":"user","content":` + string(c) + `}}`
}

func parseLines(t *testing.T, lines ...string) *Session {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := ParseFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// Owner text that starts like a relay but is the owner's own words must stay.
func TestOwnerTextThatLooksLikeRelayIsKept(t *testing.T) {
	kept := []string{
		"DECISION: use postgres for the store",
		"DONE with that, now ship",
		"NEED this by friday",
		"INFO page needs a logo",
		"Lead the way on the spout work",
		"Crius, stop the deploy",
		"Owner field must be required",
		"owner should be a dropdown",
		"From 9:00, run it hourly",
		"<div> is off center on the card",
		"Read the last BUILD-LOG lines and tell me what failed",
		"Leaders board needs paging",
		"STOP the deploy now",
		"You are right, do it",
	}
	var lines []string
	for i, k := range kept {
		lines = append(lines, userLine(string(rune('a'+i)), "2026-11-01T09:00:00Z", k))
	}
	s := parseLines(t, lines...)
	if len(s.Turns) != len(kept) {
		for _, in := range s.Injected {
			t.Errorf("dropped as injected: %q", in.Text)
		}
		t.Fatalf("kept %d of %d", len(s.Turns), len(kept))
	}
}

func TestImageMessageKeepsOwnerWords(t *testing.T) {
	arr := []map[string]any{
		{"type": "text", "text": "make the header like this"},
		{"type": "image", "source": map[string]string{"type": "base64", "data": "AAAA"}},
	}
	s := parseLines(t,
		userLine("img1", "2026-11-01T09:00:00Z", arr),
		userLine("img2", "2026-11-01T09:01:00Z", "[Image #1] use this colour for the button"),
		userLine("img3", "2026-11-01T09:02:00Z", "[Image: original 1206x2622, displayed at 920x2000.]"),
	)
	if len(s.Turns) != 2 || s.Turns[0].Text != "make the header like this" || s.Turns[1].Text != "use this colour for the button" {
		t.Errorf("turns = %+v", s.Turns)
	}
}

func TestBadTimestampIsCounted(t *testing.T) {
	s := parseLines(t, userLine("t1", "not a time", "go with plan b"), userLine("t2", "", "and ship it"))
	if s.BadTimes != 2 || len(s.Turns) != 0 {
		t.Errorf("bad times = %d, turns = %d; want 2, 0", s.BadTimes, len(s.Turns))
	}
}

func TestHugeLineAndHalfWrittenLastLine(t *testing.T) {
	big := userLine("big", "2026-11-01T09:00:00Z", "go "+strings.Repeat("x", 3<<20))
	half := `{"type":"user","uuid":"half","message":{"content":"never ` // still being written
	s := parseLines(t, big, userLine("ok", "2026-11-01T09:01:00Z", "ship it"), half)
	if len(s.Turns) != 2 {
		t.Errorf("turns = %d, want 2", len(s.Turns))
	}
	if s.BadLines != 1 {
		t.Errorf("bad lines = %d, want 1 (the half line)", s.BadLines)
	}
}

func TestWalkSymlinkedRoot(t *testing.T) {
	real := t.TempDir()
	os.MkdirAll(filepath.Join(real, "p"), 0o755)
	os.WriteFile(filepath.Join(real, "p", "s.jsonl"), []byte("{}\n"), 0o644)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	files, err := Walk([]string{link})
	if err != nil || len(files) != 1 {
		t.Errorf("files = %v, err = %v; want 1 file", files, err)
	}
}

func TestWalkUnreadableFolderIsReportedRestStillWalked(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"a", "b", "c"} {
		os.MkdirAll(filepath.Join(root, d), 0o755)
		os.WriteFile(filepath.Join(root, d, "s.jsonl"), []byte("{}\n"), 0o644)
	}
	if err := os.Chmod(filepath.Join(root, "b"), 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(filepath.Join(root, "b"), 0o755)
	files, err := Walk([]string{root})
	if err == nil {
		t.Errorf("unreadable folder gave no error")
	}
	if len(files) != 2 {
		t.Errorf("files = %v, want the 2 readable ones", files)
	}
}

func TestWalkNothingIsAnError(t *testing.T) {
	if _, err := Walk(nil); err == nil {
		t.Errorf("no roots gave no error")
	}
	if _, err := Walk([]string{t.TempDir()}); err == nil || !strings.Contains(err.Error(), "no transcripts") {
		t.Errorf("empty root error = %v", err)
	}
}

func TestPrevAssistantResetAtCompactAndSkipsAPIErrors(t *testing.T) {
	s := parseLines(t,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-11-01T09:00:00Z","message":{"content":[{"type":"text","text":"Ship it now?"}]}}`,
		`{"type":"user","uuid":"c1","timestamp":"2026-11-01T09:01:00Z","isCompactSummary":true,"message":{"content":"This session is being continued from a previous conversation"}}`,
		`{"type":"assistant","uuid":"a2","timestamp":"2026-11-01T09:02:00Z","isApiErrorMessage":true,"message":{"content":[{"type":"text","text":"API Error: overloaded"}]}}`,
		userLine("u1", "2026-11-01T09:03:00Z", "yes"),
	)
	if len(s.Turns) != 1 || s.Turns[0].PrevAssistant != "" {
		t.Errorf("turns = %+v", s.Turns)
	}
}

func TestTurnWithoutIDIsABadLine(t *testing.T) {
	s := parseLines(t, userLine("", "2026-11-01T09:00:00Z", "paint the kettle blue"), userLine("t2", "2026-11-01T09:01:00Z", "and the lid too"))
	if s.BadLines != 1 || len(s.Turns) != 1 {
		t.Errorf("bad lines = %d, turns = %d; want 1, 1", s.BadLines, len(s.Turns))
	}
}
