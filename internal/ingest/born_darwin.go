//go:build darwin

package ingest

import (
	"os"
	"syscall"
	"time"
)

// born is the file's birth time (macOS), so the first undated line of a rescanned file gets a real date.
func born(path string) (time.Time, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}, err
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Birthtimespec.Sec > 0 {
		return time.Unix(st.Birthtimespec.Sec, st.Birthtimespec.Nsec), nil
	}
	return fi.ModTime(), nil
}
