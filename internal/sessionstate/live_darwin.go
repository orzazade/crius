//go:build darwin

package sessionstate

import (
	"errors"
	"fmt"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Live reports whether pid is still the process that wrote a Claude Code status file. Claude writes its own start time
// in the file (procStart: UTC, as ctime prints it); a pid the system gave to a later process started at another time.
func Live(pid int, procStart string) (bool, error) {
	// pid 0 would test our own process group.
	if pid <= 0 || errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		return false, nil
	}
	want, err := time.Parse(time.ANSIC, procStart)
	if err != nil {
		return false, fmt.Errorf("pid %d: procStart %q: %w", pid, procStart, err)
	}
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) { // it ended while we looked
			return false, nil
		}
		return false, fmt.Errorf("pid %d: %w", pid, err)
	}
	return time.Unix(k.Proc.P_starttime.Sec, 0).Equal(want), nil
}
