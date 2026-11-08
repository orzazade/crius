package sessionstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Event is one hook event as the process that got it saw it. Each hook process queues its event before it tries the
// session's lock, and the process that holds the lock applies every queued event of the session, oldest first: a hook
// that finds the lock held leaves its event to the holder rather than wait or drop it (crius:lead 11-08: under load 7
// of 20 parallel events were dropped after a 1 s wait for the lock). The prompt is kept only as its hash.
type Event struct {
	In        Input     `json:"in"`
	PromptSHA string    `json:"prompt_sha,omitempty"`
	Time      time.Time `json:"time"`
	Config    string    `json:"config,omitempty"`
	PID       int       `json:"pid,omitempty"` // the session's live process, 0 when its status file was not found
	Name      string    `json:"name,omitempty"`
	Workspace string    `json:"workspace,omitempty"`
	Surface   string    `json:"surface,omitempty"`
}

// queueDir holds the queued events of every session, <id>.<unix nanoseconds>-<pid>.json. List and Prune's session
// scan pass it by: its name has no .json suffix.
func queueDir(dir string) string { return filepath.Join(dir, ".queue") }

// Queue saves ev for whoever holds its session's lock. It is written whole (a temp file renamed into place), so a
// holder never reads half of one.
func Queue(dir string, ev Event) error {
	if _, err := Path(dir, ev.In.SessionID); err != nil {
		return err
	}
	if ev.In.Event == "UserPromptSubmit" {
		ev.PromptSHA = PromptSHA(ev.In.Prompt)
	}
	ev.In.Prompt = ""
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	q := queueDir(dir)
	if err := os.MkdirAll(q, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(q, ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(f.Name())
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return err
	}
	name := fmt.Sprintf("%s.%020d-%d.json", ev.In.SessionID, ev.Time.UnixNano(), os.Getpid())
	if err := os.Rename(f.Name(), filepath.Join(q, name)); err != nil {
		os.Remove(f.Name())
		return err
	}
	return nil
}

// Queued gives the paths of a session's queued events, oldest first. No queue folder is none queued.
func Queued(dir, id string) ([]string, error) {
	if _, err := Path(dir, id); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(queueDir(dir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), id+".") && strings.HasSuffix(e.Name(), ".json") { // ids hold no "."
			out = append(out, filepath.Join(queueDir(dir), e.Name()))
		}
	}
	slices.Sort(out) // the time is zero-padded: the names sort in time order
	return out, nil
}

// QueuedSessions gives the ids of the sessions with queued events, sorted.
func QueuedSessions(dir string) ([]string, error) {
	entries, err := os.ReadDir(queueDir(dir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		id, _, ok := strings.Cut(e.Name(), ".")
		if ok && strings.HasSuffix(e.Name(), ".json") && idRe.MatchString(id) && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out, nil
}

// ReadQueued reads one queued event.
func ReadQueued(path string) (Event, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Event{}, err
	}
	var ev Event
	if err := json.Unmarshal(b, &ev); err != nil {
		return Event{}, fmt.Errorf("%s: %w", path, err)
	}
	return ev, nil
}

// pruneQueue removes queued events and temp files last changed longer than KeepAny ago: a session that never fired
// again after its hook was killed between queueing and applying. Each event it drops unapplied is named in the error.
func pruneQueue(dir string, now time.Time) error {
	entries, err := os.ReadDir(queueDir(dir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		fi, err := e.Info()
		if err == nil && now.Sub(fi.ModTime()) > KeepAny {
			if err = os.Remove(filepath.Join(queueDir(dir), e.Name())); err == nil && !strings.HasPrefix(e.Name(), ".tmp-") {
				err = fmt.Errorf("dropped queued event %s, never applied", e.Name())
			}
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
