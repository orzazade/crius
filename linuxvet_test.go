package crius

import (
	"os"
	"os/exec"
	"testing"
)

// The Linux team machines run go test ./... and build the tools straight from the repo. go vet with GOOS=linux
// type-checks every package and its tests for Linux, so a Mac-only call (sysctl, Chflags) without a darwin build tag
// fails here. Until 11-19 three such calls (in crius-watchdog, crius-wakewatch and an outbox test) broke that.
func TestEveryPackageVetsForLinux(t *testing.T) {
	if testing.Short() {
		t.Skip("vets the whole repo for Linux; skipped with -short")
	}
	cmd := exec.Command("go", "vet", "./...")
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOFLAGS=", "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("GOOS=linux go vet ./...: %v\n%s", err, out)
	}
}
