package procfs

import (
	"strings"
	"testing"
)

// A real /proc/<pid>/stat line from the Legion (a Claude Code process, 11-18), with the name swapped for one that
// holds spaces and parentheses, as a renamed thread may: its words must not shift the count to field 22.
const statLine = "1946300 (we (ird) na) me) S 2181754 1946300 2181754 34816 1946300 4194304 32006192 848663727 3580 164480 606330 36594 8607172 655251 20 0 25 0 43034361 5884973056 139627 18446744073709551615 34281728 152782944 140725306424624 0 0 0 0 4096 2072145151 0 0 0 17 7 0 0 0 0 0 152844064 367894528 746541056 140725306430205 140725306430375 140725306430375 140725306433477 0\n"

func TestStartTicksReadsField22PastANameWithSpacesAndParens(t *testing.T) {
	got, err := StartTicks([]byte(statLine))
	if err != nil || got != 43034361 {
		t.Fatalf("StartTicks = %d, %v; want 43034361 (field 22, the procStart Claude wrote for this pid)", got, err)
	}
	got, err = StartTicks([]byte(strings.Replace(statLine, "(we (ird) na) me)", "(claude)", 1)))
	if err != nil || got != 43034361 {
		t.Fatalf("plain name: StartTicks = %d, %v; want 43034361", got, err)
	}
}

func TestStartTicksRefusesABrokenLine(t *testing.T) {
	for name, line := range map[string]string{
		"no name":   "1946300 claude S 1 2 3",
		"too short": "1946300 (claude) S 2181754 1946300",
		"not ticks": strings.Replace(statLine, " 43034361 ", " 4303436x ", 1),
	} {
		if got, err := StartTicks([]byte(line)); err == nil {
			t.Errorf("%s: StartTicks = %d, nil; want an error", name, got)
		}
	}
}
