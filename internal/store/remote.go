package store

import (
	"errors"
	"fmt"
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

// OpenBus opens a bus log for appending (O_APPEND is added to flag). It refuses a log that is only a copy of a bus on
// another machine, naming the wrapper its marker gives, and writes nothing: the line would reach no one. It does not
// route the line there: the wrappers run bus-post over ssh, one slow call per line from jobs that run every few minutes
// and must not hang on a machine that is down, and the marker names the wrapper in words for a person, not as a command.
// A log with no marker opens as before. Its users: AppendLine, watchdog.PostOn, bus-post and wakewatch's clicks.
// busappend_test.go checks that every Go function that names O_APPEND is this one or a listed log that is no bus.
// Not seen by it: a flag built in a package-level var, a read-then-os.WriteFile rewrite, a shell writer (">>").
// crius-dispatch's Post goes through PostOn and returns the refusal as its error, with no fallback to Crius's bus.
func OpenBus(path string, flag int, perm os.FileMode) (*os.File, error) {
	if err := notACopy(path, "posted"); err != nil {
		return nil, err
	}
	return os.OpenFile(path, flag|os.O_APPEND, perm)
}

// ErrRemoteCopy is what a refusal of a copy of a bus on another machine is (errors.Is); it is also a Refused. The error's
// text names the log and the wrapper the marker gives. A caller with another place to say the line (Crius's own bus)
// says it there.
var ErrRemoteCopy = errors.New("the log is a copy of a bus on another machine")

type remoteCopyError struct{ r *Refused }

func (e *remoteCopyError) Error() string   { return e.r.msg }
func (e *remoteCopyError) Unwrap() []error { return []error{ErrRemoteCopy, e.r} }

// notACopy refuses a log that is a copy of a bus on another machine; done says what did not happen.
func notACopy(log, done string) error {
	use, ok, err := RemoteCopy(log)
	if err != nil {
		return fmt.Errorf("could not read the remote-copy marker of %s: %w", log, err)
	}
	if ok {
		return &remoteCopyError{&Refused{fmt.Sprintf("%s is a copy of a bus on another machine, nothing %s: %s", log, done,
			use)}}
	}
	return nil
}
