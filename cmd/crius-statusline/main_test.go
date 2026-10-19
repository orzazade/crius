package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

// Outside a repository: nothing; a repository before its first commit keeps its branch (review of the work line, 3);
// a detached HEAD says so.
func TestBranch(t *testing.T) {
	none := t.TempDir()
	if got := branch(none); got != "" {
		t.Errorf("outside a repository: %q", got)
	}
	unborn := t.TempDir()
	git(t, unborn, "init", "-q", "-b", "trunk")
	if got := branch(unborn); got != "trunk" {
		t.Errorf("unborn branch: %q", got)
	}
	det := t.TempDir()
	git(t, det, "init", "-q", "-b", "trunk")
	git(t, det, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "x")
	git(t, det, "checkout", "-q", "--detach")
	if got := branch(det); got != "HEAD" {
		t.Errorf("detached: %q", got)
	}
}

// A git that leaves a child holding its output open must not hold the statusline past the deadline (review 1).
func TestBranchSlowGitIsBounded(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\n/bin/sleep 5 &\n/bin/sleep 5\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t0 := time.Now()
	got := branch(t.TempDir())
	if d := time.Since(t0); d > 2*time.Second || got != "git?" {
		t.Errorf("slow git: %q after %v", got, d)
	}
}
