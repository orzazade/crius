// Package procfs reads the Linux /proc files the Linux builds of the Crius tools need. It has no build tag so its
// tests run on every machine; only the _linux.go files call it.
package procfs

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"time"
)

// BootTime is when the machine booted: the btime line of /proc/stat.
func BootTime() (time.Time, error) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}, err
	}
	return btime(b)
}

// btime reads the "btime <seconds since the epoch>" line of a /proc/stat file. A file without one, or with a value that
// is not a positive number, is an error, never the zero time.
func btime(stat []byte) (time.Time, error) {
	for _, line := range bytes.Split(stat, []byte("\n")) {
		f := bytes.Fields(line)
		if len(f) != 2 || string(f[0]) != "btime" {
			continue
		}
		sec, err := strconv.ParseInt(string(f[1]), 10, 64)
		if err != nil || sec <= 0 {
			return time.Time{}, fmt.Errorf("/proc/stat btime %q: not a boot time", f[1])
		}
		return time.Unix(sec, 0), nil
	}
	return time.Time{}, fmt.Errorf("/proc/stat: no btime line")
}
