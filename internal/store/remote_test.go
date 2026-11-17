package store

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/orzazade/crius/internal/testutil"
)

// The marker's first line is shown as one line: control characters in it become spaces; an empty one says so.
func TestRemoteCopyShowsTheMarkerAsOneLine(t *testing.T) {
	_, home := testutil.Temp(t)
	log := filepath.Join(home, "x.log")
	if _, ok, err := RemoteCopy(log); ok || err != nil {
		t.Fatalf("no marker: want not remote, got %v %v", ok, err)
	}
	testutil.Write(t, log+".remote", "use a\rb\x1b[2Jc\r\nsecond\n")
	if use, ok, err := RemoteCopy(log); !ok || err != nil || use != "use a b [2Jc" {
		t.Errorf("got %q %v %v", use, ok, err)
	}
	testutil.Write(t, log+".remote", "\n")
	if use, ok, _ := RemoteCopy(log); !ok || !strings.Contains(use, "names no wrapper") {
		t.Errorf("empty marker: got %q %v", use, ok)
	}
}
