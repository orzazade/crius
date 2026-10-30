package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 31, 4, 30, 0, 0, time.Local)

// clone makes an empty copy folder last changed age ago.
func clone(t *testing.T, root, name string, age time.Duration) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, name), 0o700); err != nil {
		t.Fatal(err)
	}
	at := now.Add(-age)
	if err := os.Chtimes(filepath.Join(root, name), at, at); err != nil {
		t.Fatal(err)
	}
}

func noChrome() ([]byte, error) { return []byte("Sat Oct 31 00:22:16 2026     /bin/zsh\n"), nil }

// A clean run says what it removed and what the disk got back; a cleanup that fails, or cannot tell which copies a
// running Chrome uses, removes nothing it was not sure of, says so in its log and on Crius's bus, and exits 1.
func TestARunSaysWhatItFreedAndAFailureAlertsCrius(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, "bus"), 0o700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(home, "X")
	for i, age := range []time.Duration{5, 4, 3, 2} {
		clone(t, root, "code_sign_clone."+string(rune('a'+i)), age*24*time.Hour)
	}
	var out bytes.Buffer
	if rc := run(home, root, now, func() ([]byte, error) { return nil, errors.New("exit status 1") }, &out); rc != 1 ||
		!strings.Contains(out.String(), "removed 0, kept 0") || !strings.Contains(out.String(), "crius-tidy: ps: exit status 1") {
		t.Fatalf("rc %d: %s", rc, out.String())
	}
	if _, err := os.Stat(filepath.Join(root, "code_sign_clone.a")); err != nil {
		t.Fatal("removed with no ps:", err)
	}
	out.Reset()
	if rc := run(home, root, now, noChrome, &out); rc != 0 || !strings.Contains(out.String(), "removed 1, kept 3, they held 0.0 GB, disk free ") {
		t.Fatalf("rc %d: %s", rc, out.String())
	}
	if err := os.Chmod(root, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(root, 0o700) })
	out.Reset()
	if rc := run(home, root, now, noChrome, &out); rc != 1 || !strings.Contains(out.String(), "crius-tidy: open "+root) {
		t.Fatalf("rc %d: %s", rc, out.String())
	}
	bus, err := os.ReadFile(filepath.Join(home, "bus", "crius.log"))
	if err != nil || strings.Count(string(bus), "ALERT crius:watchdog -> crius — crius-tidy: the Chrome clone cleanup failed: ") != 2 ||
		!strings.Contains(string(bus), "failed: open "+root) {
		t.Fatalf("bus %q %v", bus, err)
	}
}
