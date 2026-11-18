//go:build linux

package main

import (
	"time"

	"github.com/orzazade/crius/internal/procfs"
)

// wakeTime on Linux is the boot time (/proc/stat btime): Linux keeps no last-wake time, and the boot is never after
// it. So the check counts from an earlier time than on the Mac: after a suspend it may alert before the ingest catches
// up, never later than it should.
func wakeTime() (time.Time, error) {
	return procfs.BootTime()
}
