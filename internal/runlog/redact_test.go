package runlog

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/redact"
)

// Redaction that fails writes nothing and says why; before a check runs it is not run at all.
func TestFailedRedactionWritesNothing(t *testing.T) {
	ran := filepath.Join(t.TempDir(), "ran")
	check := func(dir string) error {
		_, _, _, err := Check(dir, "p", "r", "1e4d6df", []string{"sh", "-c", "echo out; : > " + ran}, time.Minute, io.Discard)
		return err
	}
	hand := func(dir string) error {
		_, _, err := Write(dir, "n", "1e4d6df", "1", "c", []string{"note"})
		return err
	}
	oldCommand, oldScript, oldLog, oldStream := redactCommand, redactScript, redactLog, newStream
	restore := func() { redactCommand, redactScript, redactLog, newStream = oldCommand, oldScript, oldLog, oldStream }
	defer restore()
	for _, c := range []struct {
		name string
		do   func(dir string) error
		set  func()
		runs bool
	}{
		{"command", check, func() { redactCommand = func([]string) string { panic("boom") } }, false},
		{"file name", check, func() { redactLog = func(string) string { panic("boom") } }, false},
		{"output", check, func() {
			newStream = func(w io.Writer) *redact.Stream {
				s := redact.NewStream(w)
				s.Line = func(string) string { panic("boom") }
				return s
			}
		}, true},
		{"hand cmd", hand, func() { redactScript = func(string) string { panic("boom") } }, false},
		{"hand note", hand, func() {
			redactLog = func(s string) string {
				if s == "note" {
					panic("boom")
				}
				return s
			}
		}, false},
	} {
		restore()
		c.set()
		os.Remove(ran)
		dir := t.TempDir()
		if err := c.do(dir); err == nil || !strings.Contains(err.Error(), "redaction failed") || !strings.Contains(err.Error(), "boom") {
			t.Errorf("%s: err %v", c.name, err)
		}
		if m, _ := os.ReadDir(dir); len(m) != 0 {
			t.Errorf("%s: wrote %v", c.name, m)
		}
		if _, err := os.Stat(ran); (err == nil) != c.runs {
			t.Errorf("%s: the check ran: %v", c.name, err == nil)
		}
	}
}
