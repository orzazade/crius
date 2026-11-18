//go:build darwin

package main

import (
	"time"

	"golang.org/x/sys/unix"
)

// wakeTime is the Mac's last wake from sleep (sysctl kern.waketime).
func wakeTime() (time.Time, error) {
	tv, err := unix.SysctlTimeval("kern.waketime")
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(tv.Unix()), nil
}
