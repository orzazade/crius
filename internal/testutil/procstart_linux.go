//go:build linux

package testutil

import (
	"fmt"
	"os"
	"strconv"
	"testing"

	"github.com/orzazade/crius/internal/procfs"
)

// ProcStart is pid's start time as Claude Code on Linux writes it in its status file (procStart): field 22 of
// /proc/<pid>/stat, clock ticks since boot, as a decimal string.
func ProcStart(t *testing.T, pid int) string {
	t.Helper()
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		t.Fatal(err)
	}
	ticks, err := procfs.StartTicks(b)
	if err != nil {
		t.Fatal(err)
	}
	return strconv.FormatUint(ticks, 10)
}

// OtherProcStart is a procStart in the Linux format that no test process has (a process started one tick after boot):
// a pid reused since its file was written.
const OtherProcStart = "1"
