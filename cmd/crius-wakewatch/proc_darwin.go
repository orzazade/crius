//go:build darwin

package main

import (
	"errors"
	"fmt"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// process gives the start time of pid, or false when no process has it.
func process(pid int) (time.Time, bool, error) {
	if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		return time.Time{}, false, nil
	}
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) { // it ended while we looked
			return time.Time{}, false, nil
		}
		return time.Time{}, false, fmt.Errorf("pid %d: %w", pid, err)
	}
	return time.Unix(k.Proc.P_starttime.Sec, int64(k.Proc.P_starttime.Usec)*1000), true, nil
}

// bootTime is when the machine last booted (sysctl kern.boottime).
func bootTime() (time.Time, error) {
	tv, err := unix.SysctlTimeval("kern.boottime")
	if err != nil {
		return time.Time{}, fmt.Errorf("sysctl kern.boottime: %w", err)
	}
	if tv.Sec <= 0 {
		return time.Time{}, fmt.Errorf("sysctl kern.boottime: %d", tv.Sec)
	}
	return time.Unix(tv.Sec, int64(tv.Usec)*1000), nil
}

// ended says the process is gone, exiting (P_WEXIT) or a zombie (SZOMB): its arguments go before its pid does.
func ended(pid int) bool {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	return errors.Is(unix.Kill(pid, 0), unix.ESRCH) ||
		err == nil && (k.Proc.P_pid != int32(pid) || k.Proc.P_stat == szomb || k.Proc.P_flag&pWexit != 0)
}

const (
	szomb  = 5      // p_stat of a zombie (sys/proc.h)
	pWexit = 0x2000 // p_flag P_WEXIT: working on exiting (sys/proc.h)
)

// procArgs reads the arguments and the environment a process was started with.
func procArgs(pid int) ([]string, map[string]string, error) {
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil, nil, fmt.Errorf("sysctl kern.procargs2: %v", err)
	}
	return parseProcArgs(b)
}
