package store

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"unicode"
)

// RemoteCopy (used by bus-post) gives the first line of <log>.remote, the marker of a log that is only a copy of a live bus on another
// machine (staffy.log is pulled from the ThinkPad, quotes.log from the Legion), and true. A line written to the copy
// reaches no one and the next pull overwrites it: 11-17/18 Crius posted 6 times on the Mac's staffy.log, bus-post said
// exit 0, and a decision sat unseen for 2.5 h. The marker's first line names the wrapper that posts on the live bus; it
// comes back as one line (control characters are spaces). A marker it cannot read is an error: failed to look is not
// found nothing.
func RemoteCopy(log string) (string, bool, error) {
	b, err := os.ReadFile(log + ".remote")
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	first, _, _ := strings.Cut(string(b), "\n")
	first = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, first))
	if first == "" {
		first = "the marker names no wrapper; post through the machine that holds the live log"
	}
	return first, true, nil
}
