//go:build darwin

package outbox

import (
	"syscall"
	"testing"
)

// immutable sets UF_IMMUTABLE on p until the test ends, so p cannot be renamed.
func immutable(t *testing.T, p string) {
	t.Helper()
	if err := syscall.Chflags(p, 0x2); err != nil { // UF_IMMUTABLE
		t.Fatal(err)
	}
	t.Cleanup(func() { syscall.Chflags(p, 0) })
}
