// Package watchdog alerts when the ingest heartbeat is stale while transcripts keep growing (design §5).
// It runs as its own launchd job and imports nothing from the ingest or the store on purpose: a bug there must
// not silence the check on it. It imports only internal/redact, which uses only the standard library.
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

	"github.com/orzazade/crius/internal/redact"
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
		// A missing root is skipped (the ingest reports it); one that cannot be read is an error. Symlinks are
		// resolved, because WalkDir does not follow a root that is a link.
		real, err := filepath.EvalSymlinks(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		fi, err := os.Stat(real)
		if err != nil {
			return nil, err
		}
		if !fi.IsDir() {
			return nil, fmt.Errorf("transcript root %s is not a folder", p)
		}
		existing = append(existing, real)
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
	// With no heartbeat at all, any transcript counts: the ingest has never finished a run.
	var stamp, since time.Time
	key := "missing"
	switch fi, err := os.Stat(filepath.Join(home, "heartbeat")); {
	case err == nil:
		stamp = fi.ModTime()
		key, since = strconv.FormatInt(stamp.UnixNano(), 10), stamp
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	if stamp.After(now.Add(time.Minute)) || wake.After(now.Add(time.Minute)) {
		return fmt.Errorf("the heartbeat or the wake time is in the future; did the clock move back?")
	}
	seen := filepath.Join(home, "watchdog.alerted")
	if !stamp.IsZero() && now.Sub(stamp) <= maxAge {
		// A fresh stamp ends the episode, so a later outage alerts again.
		if err := os.Remove(seen); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
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

// Post appends one ALERT line from the watchdog to Crius's bus, redacted (an error can quote a config value).
func Post(home string, now time.Time, text string) error {
	text, _ = redact.Redact(text)
	text = strings.Join(strings.Fields(text), " ")
	if r := []rune(text); len(r) > 300 {
		text = string(r[:300])
	}
	path := filepath.Join(home, "bus", "crius.log")
	// A bus that ends mid-line (a writer died) gets a newline first, so the alert is a line of its own.
	lead := ""
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 && b[len(b)-1] != '\n' {
		lead = "\n"
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(f, "%s%s ALERT crius:watchdog -> crius — %s\n", lead, now.Format("2006-01-02T15:04"), text); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
