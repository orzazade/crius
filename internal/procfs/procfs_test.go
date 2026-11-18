package procfs

import (
	"testing"
	"time"
)

func TestBtimeReadsTheBootLine(t *testing.T) {
	stat := "cpu  10 0 20 300 0 0 0 0 0 0\nintr 1 2 3\nctxt 456\nbtime 1763520000\nprocesses 789\n"
	got, err := btime([]byte(stat))
	if err != nil || !got.Equal(time.Unix(1763520000, 0)) {
		t.Fatalf("btime: %v, %v", got, err)
	}
}

// A /proc/stat that does not read is an error, never a zero boot time that every later time would pass.
func TestBtimeRefusesAFileWithoutABootTime(t *testing.T) {
	for _, stat := range []string{"", "cpu 1 2 3\n", "btime\n", "btime x\n", "btime 0\n", "btime -5\n", "btime 1 2\n"} {
		if got, err := btime([]byte(stat)); err == nil {
			t.Errorf("%q: %v, no error", stat, got)
		}
	}
}
