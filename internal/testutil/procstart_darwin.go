//go:build darwin

package testutil

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

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

// OtherProcStart is a procStart in the Mac format that no live process has: a pid reused since its file was written.
const OtherProcStart = "Thu Jan  1 00:00:00 2026"
