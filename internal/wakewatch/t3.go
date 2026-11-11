package wakewatch

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// CriusThreadFile is the file, under the Crius home, that holds the id of the T3 Code thread Crius runs in (one line,
// e.g. mcp:5bba4b79-…). While it exists Crius has no cmux tab: outbox sends its messages with `t3 thread send`, and the
// checks of a cmux tab titled crius are left out.
func CriusThreadFile(home string) string { return filepath.Join(home, "state", "crius", "t3-thread") }

// CriusThread gives the id in CriusThreadFile; "" with no error when there is no such file. A file that cannot be read,
// or holds no single id (empty, two words, two lines, or a word starting with "-" that t3 would read as a flag), is an
// error: the file says Crius has moved, so its messages must not go to a cmux tab.
func CriusThread(home string) (string, error) {
	path := CriusThreadFile(home)
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	f := strings.Fields(string(b))
	if len(f) != 1 || strings.HasPrefix(f[0], "-") {
		return "", fmt.Errorf("%s holds %q, not one T3 Code thread id", path, strings.TrimSpace(string(b)))
	}
	return f[0], nil
}
