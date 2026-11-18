//go:build linux

package main

import (
	"errors"
	"fmt"
	"syscall"
	"time"

	"github.com/orzazade/crius/internal/procfs"
)

// crius-wakewatch runs on the Mac (launchd, cmux); the Linux team machines only build and test it. So the boot time
// is real on Linux, and the two process reads it has no use for there say they are not supported, never "nothing
// found": a check that cannot read a process must not pass.

// process gives the start time of pid, or false when no process has it. On Linux only a gone pid has an answer.
func process(pid int) (time.Time, bool, error) {
	if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		return time.Time{}, false, nil
	}
	return time.Time{}, false, fmt.Errorf("pid %d: reading a process's start time is not supported on Linux", pid)
}

// bootTime is when the machine last booted (/proc/stat btime).
func bootTime() (time.Time, error) {
	return procfs.BootTime()
}

// ended says the process is gone. On Linux a running, exiting or zombie process reads as not ended, so claudes lists
// it with procArgs's error.
func ended(pid int) bool {
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

// procArgs reads the arguments and the environment a process was started with: not supported on Linux.
func procArgs(pid int) ([]string, map[string]string, error) {
	return nil, nil, fmt.Errorf("pid %d: reading a process's arguments and environment is not supported on Linux", pid)
}
