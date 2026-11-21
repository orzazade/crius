package tabsend

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// LockFile is the lock every sender on a machine holds while it types into a pane (crius-tsend, bus-post): two sends at
// once could press Enter on each other's text.
func LockFile(home string) string { return filepath.Join(home, "state", "tsend.lock") }

// Lock waits for and holds the file lock at path; the func it gives lets it go.
func Lock(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return func() { f.Close() }, nil
}
