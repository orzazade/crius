package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Crius 11-01 22:35: an ask on the quotes lead's ctx_execute code (22:29) was lost: the tab was cleared and only its last
// line was seen. Each ask is logged with its reason and the first 200 characters of what was asked on; a pass logs nothing.
func TestEachAskIsLoggedWithItsReasonAndText(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CRIUS_HOME", home)
	if err := os.Mkdir(filepath.Join(home, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	long := "az boards work-item update --id 1 --discussion hi\n" + strings.Repeat("x", 300)
	for _, in := range []string{
		`{"tool_name":"Bash","cwd":"/tmp","tool_input":{"command":"ls -la"}}`,
		`{"tool_name":"mcp__plugin_context-mode_context-mode__ctx_execute","cwd":"/tmp","tool_input":{"language":"shell","code":` + strconvQuote(long) + `}}`,
	} {
		var out bytes.Buffer
		if err := run(strings.NewReader(in), &out); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(filepath.Join(home, "state", "post-guard-asks.log"))
	if err != nil {
		t.Fatal("no ask log:", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("%d lines, want 1 (the pass logs nothing): %q", len(lines), b)
	}
	l := lines[0]
	for _, want := range []string{"__ctx_execute", "/tmp", "(az boards work-item update --discussion)", "az boards work-item update --id 1 --discussion hi⏎xxx"} {
		if !strings.Contains(l, want) {
			t.Errorf("log line lacks %q: %q", want, l)
		}
	}
	if strings.Contains(l, strings.Repeat("x", 160)) {
		t.Errorf("log line holds more than 200 characters of the text: %q", l)
	}
	if fi, err := os.Stat(filepath.Join(home, "state", "post-guard-asks.log")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("log mode %v, want 0600 (%v)", fi.Mode().Perm(), err)
	}
}

func strconvQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s) + `"`
}
