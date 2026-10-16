// Package testutil holds helpers shared by the tests: temp homes and throwaway git repos.
package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// RepoRoot is this checkout: the nearest folder above the test's working folder that holds go.mod.
func RepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test folder")
		}
		dir = parent
	}
}

// Temp gives a fresh temp root (symlinks resolved, no git tree above it) and points CRIUS_HOME at root/crius-home.
func Temp(t *testing.T) (root, home string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home = filepath.Join(root, "crius-home")
	t.Setenv("CRIUS_HOME", home)
	return root, home
}

// Git runs git in dir with the user's global and system config switched off.
func Git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func GitRepo(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	Git(t, path, "init", "-q", "-b", "main")
	Git(t, path, "config", "user.email", "t@example.invalid")
	Git(t, path, "config", "user.name", "t")
	return path
}

func CommitAll(t *testing.T, path string) {
	t.Helper()
	Git(t, path, "add", "-A")
	Git(t, path, "commit", "-q", "-m", "c")
}

func Write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func Read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
