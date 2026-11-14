package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseOwnerTurns(t *testing.T) {
	s, err := ParseFile("testdata/basic.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "s-basic" {
		t.Errorf("session id = %q", s.ID)
	}
	if s.Cwd != "/home/dev/Projects/kettle" || s.Project != "kettle" {
		t.Errorf("cwd/project = %q/%q", s.Cwd, s.Project)
	}
	if s.Headless {
		t.Errorf("cli session marked headless")
	}
	if len(s.Injected) != 8 { // the 9th tool line is a harness tag, dropped before
		t.Errorf("injected = %d, want 8", len(s.Injected))
	}
	if s.BadLines != 1 {
		t.Errorf("bad lines = %d, want 1", s.BadLines)
	}
	want := []struct {
		uuid, text string
		queue      bool
	}{
		{"u1", "Add a boil timer to the kettle app", false},
		{"u2", "beep, and from now on always log boil events", false},
		{"u-q", "go with the blue lid", true},
		{"u-old", "never ship without the lid sensor", false},
		{"u-last", "ok ship it", false},
		{"u-caps", "NEVER push to main without review", false},
	}
	if len(s.Turns) != len(want) {
		for _, tr := range s.Turns {
			t.Logf("got turn %s %q", tr.UUID, tr.Text)
		}
		t.Fatalf("owner turns = %d, want %d", len(s.Turns), len(want))
	}
	for i, w := range want {
		g := s.Turns[i]
		if g.UUID != w.uuid || g.Text != w.text || g.Queue != w.queue {
			t.Errorf("turn %d = {%s %q queue=%v}, want {%s %q queue=%v}", i, g.UUID, g.Text, g.Queue, w.uuid, w.text, w.queue)
		}
	}
	if s.Turns[2].QueueID != "q7" {
		t.Errorf("queue id = %q", s.Turns[2].QueueID)
	}
	// u2 answers the assistant's question.
	if got := s.Turns[1].PrevAssistant; got != "Should the timer beep when the water boils, or stay silent?" {
		t.Errorf("prev assistant = %q", got)
	}
	if s.Turns[0].PrevAssistant != "" {
		t.Errorf("first turn has prev assistant %q", s.Turns[0].PrevAssistant)
	}
	wantTime := time.Date(2026, 11, 1, 9, 1, 0, 0, time.UTC)
	if !s.Turns[0].Time.Equal(wantTime) {
		t.Errorf("time = %v", s.Turns[0].Time)
	}
	// Assistant actions: the Edit and the Bash commit.
	if len(s.Actions) != 2 {
		t.Fatalf("actions = %d, want 2: %+v", len(s.Actions), s.Actions)
	}
	if s.Actions[0].Text != "b" {
		t.Errorf("edit text = %q, want the new_string", s.Actions[0].Text)
	}
	if s.Actions[0].Tool != "Edit" || s.Actions[0].Path != "/home/dev/Projects/kettle/STATUS.md" {
		t.Errorf("action 0 = %+v", s.Actions[0])
	}
	if s.Actions[1].Tool != "Bash" || s.Actions[1].Command != "git commit -m 'timer'" {
		t.Errorf("action 1 = %+v", s.Actions[1])
	}
}

func TestHeadlessSession(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "h.jsonl")
	line := `{"type":"user","uuid":"h1","sessionId":"s-h","cwd":"/x/y","entrypoint":"sdk-cli","timestamp":"2026-11-01T09:00:00Z","message":{"role":"user","content":"Review the diff"}}` + "\n"
	if err := os.WriteFile(p, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := ParseFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Headless {
		t.Errorf("sdk-cli session not marked headless")
	}
}

func TestWalkSkipsSubagents(t *testing.T) {
	root := t.TempDir()
	mk := func(rel string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("proj-a/s1.jsonl")
	mk("proj-a/s1/subagents/agent-1.jsonl")
	mk("proj-b/s2.jsonl")
	mk("proj-b/notes.txt")
	files, err := Walk([]string{root, filepath.Join(root, "does-not-exist")})
	if err == nil {
		t.Errorf("missing root gave no error")
	}
	if len(files) != 2 {
		t.Errorf("files = %v, want 2", files)
	}
}

func TestRelayTurnsAreNotOwner(t *testing.T) {
	s, err := ParseFile("testdata/relay.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tr := range s.Turns {
		got = append(got, tr.UUID)
	}
	want := []string{"o1", "o2", "o3"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("owner turns = %v, want %v", got, want)
	}
	if len(s.Injected) != 9 {
		t.Errorf("injected = %d, want 9", len(s.Injected))
	}
}
