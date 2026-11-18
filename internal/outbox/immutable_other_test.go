//go:build !darwin

package outbox

import "testing"

// immutable skips the test off the Mac: Linux's immutable flag (chattr +i) needs root, and the tests run as a user.
func immutable(t *testing.T, p string) {
	t.Skip("needs a file that cannot be renamed: macOS chflags UF_IMMUTABLE; Linux chattr +i needs root")
}
