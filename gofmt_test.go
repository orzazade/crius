package crius

import (
	"bytes"
	"go/format"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The repo has no CI, so go test is the gate: an unformatted file fails it here, not in a later reviewer's diff.
func TestGofmt(t *testing.T) {
	n := 0
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != "." && (strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		got, err := format.Source(src)
		if err != nil {
			return err
		}
		if !bytes.Equal(got, src) {
			t.Errorf("%s is not gofmt-formatted: run gofmt -w %s", path, path)
		}
		n++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n < 20 { // a walk that finds nothing must not pass
		t.Fatalf("checked only %d .go files", n)
	}
}
