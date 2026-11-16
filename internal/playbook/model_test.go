package playbook

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
	_, err := ClaudeCLI{Bin: bin, ConfigDir: t.TempDir()}.run(context.Background(), "haiku", "p", nil, nil)
	if !errors.Is(err, ErrAPIKey) || time.Since(start) > 10*time.Second {
		t.Fatalf("err = %v after %s, want ErrAPIKey at once", err, time.Since(start))
	}
}

// Seen 11-16: the child always ran on ~/.claude (max1) because CLAUDE_CONFIG_DIR
// was stripped, so max1's weekly limit parked every batch. The child now runs in
// the picked account's folder, never the caller's, and gets no API credentials.
func TestChildRunsInThePickedAccountFolder(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/Users/x/.claude-work")
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "1")
	bin := fakeClaude(t, `echo '{"type":"system","subtype":"init","apiKeySource":"none"}'; printf '{"type":"result","result":"[\\"%s|%s|%s\\"]"}\n' "$CLAUDE_CONFIG_DIR" "$ANTHROPIC_API_KEY" "$CLAUDE_CODE_USE_BEDROCK"`)
	out, err := ClaudeCLI{Bin: bin, ConfigDir: "/Users/x/.claude-max2"}.run(context.Background(), "haiku", "p", nil, nil)
	if err != nil || out != `["/Users/x/.claude-max2||"]` {
		t.Fatalf("child saw %q (err %v), want the picked folder and no API variables", out, err)
	}
}

// A ClaudeCLI with no picked folder never starts claude (it would fall back to
// ~/.claude), and the run stops instead of parking every batch.
func TestNoPickedFolderStopsTheRun(t *testing.T) {
	ran := filepath.Join(t.TempDir(), "ran")
	bin := fakeClaude(t, `touch `+ran+`; echo '{"type":"system","subtype":"init","apiKeySource":"none"}'; echo '{"type":"result","result":"[]"}'`)
	_, err := ClaudeCLI{Bin: bin}.run(context.Background(), "haiku", "p", nil, nil)
	if _, serr := os.Stat(ran); !errors.Is(err, ErrNoAccount) || serr == nil || !stop(context.Background(), err) {
		t.Fatalf("err = %v, claude started: %v; want ErrNoAccount, no start, and a stop", err, serr == nil)
	}
}

// Seen 11-14 (first real run): 6 of 57 sonnet replies were broken JSON, and
// others named a candidate of another batch or left kind and id empty. Every
// call now passes a schema that names this batch's ids, the topics and kinds.
func TestCallsPassASchemaWithTheBatchIDs(t *testing.T) {
	args := filepath.Join(t.TempDir(), "args")
	bin := fakeClaude(t, `printf '%s\n' "$@" > `+args+`
echo '{"type":"system","subtype":"init","apiKeySource":"none"}'
echo '{"type":"result","result":"{\"items\":[]}"}'`)
	c := ClaudeCLI{Bin: bin, ConfigDir: t.TempDir()}
	cs := []Candidate{{ID: "bus:a.log:12", Text: "x"}, {ID: "tx:u1", Text: "y"}}
	check := func(step string) map[string]any {
		data, _ := os.ReadFile(args)
		lines := strings.Split(string(data), "\n")
		for i, l := range lines {
			if l == "--json-schema" && i+1 < len(lines) {
				var s map[string]any
				if err := json.Unmarshal([]byte(lines[i+1]), &s); err != nil {
					t.Fatalf("%s: schema is not JSON: %v", step, err)
				}
				return s
			}
		}
		t.Fatalf("%s: no --json-schema in the call: %q", step, data)
		return nil
	}
	if _, err := c.Pick(context.Background(), cs); err != nil {
		t.Fatal(err)
	}
	s, _ := json.Marshal(check("pick"))
	for _, want := range []string{`"bus:a.log:12"`, `"tx:u1"`, `"review-and-qa"`} {
		if !strings.Contains(string(s), want) {
			t.Errorf("pick schema has no %s: %s", want, s)
		}
	}
	if _, err := c.Write(context.Background(), cs, nil); err != nil {
		t.Fatal(err)
	}
	s, _ = json.Marshal(check("write"))
	for _, want := range []string{`"bus:a.log:12"`, `"tx:u1"`, `"owner-preference"`, `"minLength":3`} {
		if !strings.Contains(string(s), want) {
			t.Errorf("write schema has no %s: %s", want, s)
		}
	}
}
