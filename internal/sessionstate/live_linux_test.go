//go:build linux

package sessionstate

import (
	"os"
	"strconv"
	"testing"

	"github.com/orzazade/crius/internal/procfs"
)

// Live on Linux matches the ticks Claude writes (field 22 of /proc/<pid>/stat) and refuses any other start time, so a
// pid handed to a later process does not count as the session's.
func TestLiveMatchesOwnStartTicks(t *testing.T) {
	b, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		t.Fatal(err)
	}
	ticks, err := procfs.StartTicks(b)
	if err != nil {
		t.Fatal(err)
	}
	pid := os.Getpid()
	if live, err := Live(pid, strconv.FormatUint(ticks, 10)); err != nil || !live {
		t.Fatalf("Live(self, %d) = %v, %v; want true", ticks, live, err)
	}
	if live, err := Live(pid, strconv.FormatUint(ticks+1, 10)); err != nil || live {
		t.Fatalf("Live(self, %d) = %v, %v; want false (another start time)", ticks+1, live, err)
	}
	if _, err := Live(pid, "Tue Nov 18 10:00:00 2026"); err == nil {
		t.Fatal("Live(self, ctime string) = nil error; want the procStart error")
	}
}
