package procfs

import (
	"bytes"
	"fmt"
	"strconv"
)

// StartTicks reads a process's start time from a /proc/<pid>/stat line: field 22, in clock ticks since boot. The
// process name (field 2) sits in parentheses and may itself hold spaces and ")", so the fields are counted from the
// last ")".
func StartTicks(stat []byte) (uint64, error) {
	i := bytes.LastIndexByte(stat, ')')
	if i < 0 {
		return 0, fmt.Errorf("stat line %q: no \")\" after the process name", stat)
	}
	f := bytes.Fields(stat[i+1:]) // f[0] is field 3, the state
	if len(f) < 20 {
		return 0, fmt.Errorf("stat line %q: %d fields after the name, want at least 20", stat, len(f))
	}
	return strconv.ParseUint(string(f[19]), 10, 64)
}
