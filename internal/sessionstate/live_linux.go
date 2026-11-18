//go:build linux

package sessionstate

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"syscall"

	"github.com/orzazade/crius/internal/procfs"
)

// Live reports whether pid is still the process that wrote a Claude Code status file. On Linux Claude writes its own
// start time in the file as procStart: field 22 of /proc/<pid>/stat, clock ticks since boot, as a decimal string
// (seen on the Legion and the ThinkPad, Claude Code 2.1.325-326). Comparing the raw ticks is exact; a pid the system
// gave to a later process has other ticks.
func Live(pid int, procStart string) (bool, error) {
	// pid 0 would test our own process group.
	if pid <= 0 || errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		return false, nil
	}
	want, err := strconv.ParseUint(procStart, 10, 64)
	if err != nil {
		return false, fmt.Errorf("pid %d: procStart %q: %w", pid, procStart, err)
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) { // it ended while we looked
			return false, nil
		}
		return false, fmt.Errorf("pid %d: %w", pid, err)
	}
	got, err := procfs.StartTicks(b)
	if err != nil {
		return false, fmt.Errorf("pid %d: %w", pid, err)
	}
	return got == want, nil
}
