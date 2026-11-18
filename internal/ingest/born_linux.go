//go:build linux

package ingest

import (
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// born is the file's birth time, so the first undated line of a rescanned file gets a real date. Linux keeps it only
// where the file system records one and statx reports it (STATX_BTIME in the returned mask: ext4, btrfs, xfs v5 on
// kernel 4.11+). Elsewhere it falls back to the modification time, as the macOS code does when there is no birth time.
func born(path string) (time.Time, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}, err
	}
	var st unix.Statx_t
	if unix.Statx(unix.AT_FDCWD, path, 0, unix.STATX_BTIME, &st) == nil && st.Mask&unix.STATX_BTIME != 0 && st.Btime.Sec > 0 {
		return time.Unix(st.Btime.Sec, int64(st.Btime.Nsec)), nil
	}
	return fi.ModTime(), nil
}
