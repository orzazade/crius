package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Live run 10-22: cmux prints a deprecation notice on stderr, and crius-spawn parsed it with the reply, so it failed
// after making the workspace. A notice must not reach the parse; on failure it must show.
func TestCmuxStderrStaysOutOfTheReplyUnlessItFails(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\necho \"cmux: 'new-workspace' is now an alias\" >&2\n[ \"$1\" = bad ] && { echo 'Error: no such thing' >&2; exit 1; }\necho 'OK workspace:9'\n"
	if err := os.WriteFile(filepath.Join(dir, "cmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if out, err := runCmux("new-workspace"); err != nil || out != "OK workspace:9\n" {
		t.Fatalf("%q %v", out, err)
	}
	if out, err := runCmux("bad"); err == nil || !strings.Contains(out, "Error: no such thing") {
		t.Fatalf("%q %v", out, err)
	}
}
