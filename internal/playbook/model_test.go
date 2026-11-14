package playbook

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Seen 11-14: a sonnet reply with a remark after the array failed the whole run.
func TestDecodeArrayIgnoresTextAroundIt(t *testing.T) {
	for _, s := range []string{
		"[{\"id\":\"a\",\"keep\":true}]\nWait, one more note [x].",
		"```json\n[{\"id\":\"a\",\"keep\":true}]\n```",
		"Here: [{\"id\":\"a\",\"keep\":true}]",
	} {
		var vs []Verdict
		if err := decodeArray(s, &vs); err != nil || len(vs) != 1 || vs[0].ID != "a" {
			t.Errorf("decodeArray(%q) = %+v, %v", s, vs, err)
		}
	}
	var vs []Verdict
	if decodeArray("no json here", &vs) == nil {
		t.Error("a reply with no array decoded")
	}
}

func fakeClaude(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// A call on an API key is killed at its init line, not after it ran.
func TestAPIKeyCallIsKilledAtInit(t *testing.T) {
	bin := fakeClaude(t, `echo '{"type":"system","subtype":"init","apiKeySource":"ANTHROPIC_API_KEY"}'; sleep 30; echo '{"type":"result","result":"[]"}'`)
	start := time.Now()
	_, err := ClaudeCLI{Bin: bin}.run(context.Background(), "haiku", "p", nil)
	if !errors.Is(err, ErrAPIKey) || time.Since(start) > 10*time.Second {
		t.Fatalf("err = %v after %s, want ErrAPIKey at once", err, time.Since(start))
	}
}

// The child never gets the work persona's config folder.
func TestChildEnvHasNoConfigDir(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/Users/x/.claude-work")
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	bin := fakeClaude(t, `echo '{"type":"system","subtype":"init","apiKeySource":"none"}'; printf '{"type":"result","result":"[\\"%s|%s\\"]"}\n' "$CLAUDE_CONFIG_DIR" "$ANTHROPIC_API_KEY"`)
	out, err := ClaudeCLI{Bin: bin}.run(context.Background(), "haiku", "p", nil)
	if err != nil || out != `["|"]` {
		t.Fatalf("child saw %q (err %v), want both variables empty", out, err)
	}
}
