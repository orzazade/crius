// Package watchdog alerts when the ingest heartbeat is stale while transcripts keep growing (design §5).
// It runs as its own launchd job and imports nothing from the ingest or the store on purpose: a bug there must
// not silence the check on it.
//
// Stale = older than maxAge, counted from the later of the last stamp and the last wake from sleep, so a Mac
// that just woke does not alert before the ingest catches up. One ALERT line per stale episode goes to
// ~/.crius/bus/crius.log; Crius reads it and decides what to do.
package watchdog

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// The ingest's defaults when projects.toml has no [transcripts] table.
var defaultRoots = map[string]string{"personal": "~/.claude-personal/projects", "work": "~/.claude-work/projects"}

func expand(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[1:])
		}
	}
	return p
}

// roots are the transcript folders the ingest reads, from projects.toml.
func roots(home string) ([]string, error) {
	var raw struct{ Transcripts map[string]string }
	path := filepath.Join(home, "projects.toml")
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		return nil, err
	}
	m := raw.Transcripts
	if len(m) == 0 {
		m = defaultRoots
	}
	var all, existing []string
	for _, p := range m {
		all = append(all, expand(p))
	}
	sort.Strings(all)
	for _, p := range all {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			existing = append(existing, p)
		}
	}
	if len(existing) == 0 {
		return nil, fmt.Errorf("no transcript root exists in %v", all)
	}
	return existing, nil
}

var errFound = errors.New("found")

// grownSince reports whether any transcript under the roots changed after t.
func grownSince(dirs []string, t time.Time) (bool, error) {
	for _, d := range dirs {
		err := filepath.WalkDir(d, func(path string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
				return nil
			}
			fi, err := e.Info()
			if errors.Is(err, fs.ErrNotExist) {
				return nil // removed while we walked
			}
			if err != nil {
				return err
			}
			if fi.ModTime().After(t) {
				return errFound
			}
			return nil
		})
		if errors.Is(err, errFound) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
	}
	return false, nil
}

// Check posts an ALERT when the heartbeat is older than maxAge (counted from the later of the stamp and wake)
// while transcripts grew after it. It returns an error when it cannot tell.
func Check(home string, now, wake time.Time, maxAge time.Duration) error {
	bus := filepath.Join(home, "bus")
	if fi, err := os.Stat(bus); err != nil || !fi.IsDir() {
		return fmt.Errorf("%s is missing", bus)
	}
	dirs, err := roots(home)
	if err != nil {
		return err
	}
	var stamp time.Time
	key, since := "missing", now.Add(-maxAge)
	switch fi, err := os.Stat(filepath.Join(home, "heartbeat")); {
	case err == nil:
		stamp = fi.ModTime()
		key, since = strconv.FormatInt(stamp.UnixNano(), 10), stamp
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	ref := stamp
	if wake.After(ref) {
		ref = wake
	}
	if now.Sub(ref) <= maxAge {
		return nil
	}
	grown, err := grownSince(dirs, since)
	if err != nil || !grown {
		return err
	}
	seen := filepath.Join(home, "watchdog.alerted")
	if b, err := os.ReadFile(seen); err == nil && strings.TrimSpace(string(b)) == key {
		return nil
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	what := "no ingest heartbeat"
	if !stamp.IsZero() {
		what = fmt.Sprintf("ingest heartbeat %d min old", int(now.Sub(stamp).Minutes()))
	}
	if err := Post(home, now, what+" while transcripts grow; see ~/.crius/logs/ingest.log"); err != nil {
		return err
	}
	return os.WriteFile(seen, []byte(key+"\n"), 0o600)
}

// Post appends one ALERT line from the watchdog to Crius's bus.
func Post(home string, now time.Time, text string) error {
	text = strings.Join(strings.Fields(text), " ")
	if r := []rune(text); len(r) > 300 {
		text = string(r[:300])
	}
	f, err := os.OpenFile(filepath.Join(home, "bus", "crius.log"), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(f, "%s ALERT crius:watchdog -> crius — %s\n", now.Format("2006-01-02T15:04"), text); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
