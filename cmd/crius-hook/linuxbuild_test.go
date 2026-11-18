package main

import (
	"os"
	"os/exec"
	"testing"

	"github.com/orzazade/crius/internal/boxtools"
)

// The tools in ~/.crius/bin on the Linux team machines (the Legion and the ThinkPad) must build for Linux straight
// from the repo. Until 11-18 two Mac-only calls (sysctl kern.proc.pid, Stat_t.Birthtimespec) broke that build, so
// those machines ran binaries from a patched scratch copy and missed repo fixes. The list is boxtools.Tools, the
// one crius-install-box copies.
func TestLinuxToolsBuildFromTheRepo(t *testing.T) {
	args := []string{"build", "-buildvcs=false"}
	for _, name := range boxtools.Tools {
		args = append(args, "./cmd/"+name)
	}
	cmd := exec.Command("go", args...)
	cmd.Dir = "../.."
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOFLAGS=", "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("linux/amd64 build: %v\n%s", err, out)
	}
}
