// Package testutil holds helpers shared by the tests: temp homes and throwaway git repos.
package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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

// ProcStart is pid's start time as Claude Code writes it in its status file (procStart: UTC, as ctime prints it), read
// from ps.
func ProcStart(t *testing.T, pid int) string {
	t.Helper()
	cmd := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid))
	cmd.Env = append(os.Environ(), "TZ=UTC")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

// Seats are config folders under root, as accounts.toml in crius (a Crius home) lists them: WorkA (row max2) and WorkB
// (row max3) are two Max accounts of one persona, their projects/ links to one work transcript folder; Personal (row
// max1) is a Max account of another persona with its own projects/; Ent (an enterprise row) links to the work folder.
type Seats struct{ WorkA, WorkB, Personal, Ent string }

func MaxSeats(t *testing.T, root, crius string) Seats {
	t.Helper()
	s := Seats{filepath.Join(root, ".claude-a"), filepath.Join(root, ".claude-b"), filepath.Join(root, ".claude-p"),
		filepath.Join(root, ".claude-e")}
	work := filepath.Join(root, ".work", "projects")
	for _, d := range []string{work, filepath.Join(s.Personal, "projects"), s.WorkA, s.WorkB, s.Ent} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []string{s.WorkA, s.WorkB, s.Ent} {
		if err := os.Symlink(work, filepath.Join(d, "projects")); err != nil {
			t.Fatal(err)
		}
	}
	row := func(name, plan, dir string) string {
		return "[[account]]\nname = \"" + name + "\"\nplan = \"" + plan + "\"\ndirs = [\"" + dir + "\"]\n"
	}
	Write(t, filepath.Join(crius, "accounts.toml"), row("max1", "max", s.Personal)+row("max2", "max", s.WorkA)+
		row("max3", "max", s.WorkB)+row("ent", "enterprise", s.Ent))
	return s
}
