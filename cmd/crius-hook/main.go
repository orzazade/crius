// Command crius-hook is a Claude Code hook for SessionStart, UserPromptSubmit, PreToolUse, PostToolUse,
// PostToolUseFailure, PermissionRequest, PermissionDenied, Notification, Stop and SessionEnd. It keeps one state
// file per session in ~/.crius/sessions/ (package sessionstate), so wakewatch and promptwatch can read what a
// session is doing instead of guessing from its screen (docs/notes/OPENRIG-LESSONS.md, row A).
//
// It must never get in the session's way: it prints nothing on stdout (Claude Code would put it in the model's
// context), and it exits 0 on its own errors (exit 2 would block the prompt or the tool). Its errors go to
// ~/.crius/logs/crius-hook.log, which wakewatch reads, or to stderr when that log cannot be written.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/orzazade/crius/internal/ingest"
	"github.com/orzazade/crius/internal/sessionstate"
	"github.com/orzazade/crius/internal/store"
)

func main() {
	defer func() {
		if r := recover(); r != nil {
			fail(fmt.Errorf("panic: %v", r))
		}
		os.Exit(0)
	}()
	var in sessionstate.Input
	if err := json.NewDecoder(os.Stdin).Decode(&in); err != nil {
		fail(fmt.Errorf("unreadable hook input: %w", err))
		return
	}
	if strings.HasPrefix(in.SessionID, "served:") {
		return // Claude Code serving a remote caller's call: not a session of ours
	}
	if err := record(in); err != nil {
		fail(fmt.Errorf("%s %s: %w", in.Event, in.SessionID, err))
	}
}

// record applies one hook event to the session's file. A missing projects.toml or Claude Code status file loses
// the project, role or name, not the state: it is logged and the state is written. A damaged file is logged and
// replaced. Events of one session run as parallel processes, so the read, change and write hold the session's lock.
func record(in sessionstate.Input) error {
	home := store.Home()
	dir := filepath.Join(home, "sessions")
	name, found, err := claudeName(in.SessionID)
	if err != nil {
		fail(err)
	}
	unlock, err := lock(dir, in.SessionID)
	if err != nil {
		return err
	}
	defer unlock()
	prev, err := sessionstate.Read(dir, in.SessionID)
	if errors.Is(err, sessionstate.ErrDamaged) {
		fail(fmt.Errorf("replacing it: %w", err))
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	s := sessionstate.Apply(prev, in, time.Now())
	if found { // a live session resumed without -n has no name now
		s.Name = name
	}
	if p, r, err := ingest.Filing(home, s.Cwd, s.Name); err != nil {
		configFailed(home, dir, err)
	} else {
		s.Project, s.Role = p, r
	}
	// the session's own env on every event: one resumed outside cmux no longer names its old surface
	s.Workspace, s.Surface = os.Getenv("CMUX_WORKSPACE_ID"), os.Getenv("CMUX_SURFACE_ID")
	return sessionstate.Write(dir, s)
}

// lock holds the session's lock file, <id>.json.lock, until unlock is called; the kernel lets go if the hook is killed.
// A lock still held after a second is an error: the event is skipped, not waited on until Claude Code's hook timeout.
func lock(dir, id string) (unlock func(), err error) {
	p, err := sessionstate.Path(dir, id)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for end := time.Now().Add(time.Second); ; time.Sleep(20 * time.Millisecond) {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { f.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			f.Close()
			return nil, fmt.Errorf("lock %s: %w", p+".lock", err)
		}
		if time.Now().After(end) {
			f.Close()
			return nil, fmt.Errorf("%s locked for 1s, event skipped", p+".lock")
		}
	}
}

// configFailed logs a projects.toml error once until the file or the error changes: a bad file would otherwise log on
// every event of every session. The last one logged is kept in <sessions>/.config-error.
func configFailed(home, dir string, err error) {
	mtime := "missing"
	if fi, serr := os.Stat(filepath.Join(home, "projects.toml")); serr == nil {
		mtime = fi.ModTime().String()
	}
	mark, seen := filepath.Join(dir, ".config-error"), mtime+" "+err.Error()
	if b, rerr := os.ReadFile(mark); rerr == nil && string(b) == seen {
		return
	}
	fail(err)
	if os.MkdirAll(dir, 0o700) == nil {
		if werr := os.WriteFile(mark, []byte(seen), 0o600); werr != nil {
			fail(werr)
		}
	}
}

// claudeName is the session's -n name from Claude Code's own status file, <config>/sessions/<pid>.json, found by
// session id. found is false when no live process's file names this session (a crashed one leaves its file behind).
func claudeName(id string) (name string, found bool, err error) {
	cfg := os.Getenv("CLAUDE_CONFIG_DIR")
	if cfg == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return "", false, err
		}
		cfg = filepath.Join(h, ".claude")
	}
	files, err := filepath.Glob(filepath.Join(cfg, "sessions", "*.json"))
	if err != nil {
		return "", false, err
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue // the session ended and its file went while we looked
		}
		var st struct {
			PID       int    `json:"pid"`
			SessionID string `json:"sessionId"`
			Name      string `json:"name"`
		}
		if json.Unmarshal(b, &st) == nil && st.SessionID == id && st.PID > 0 &&
			!errors.Is(syscall.Kill(st.PID, 0), syscall.ESRCH) {
			return st.Name, true, nil
		}
	}
	return "", false, nil
}

// fail logs err to ~/.crius/logs/crius-hook.log, or to stderr when the log cannot be written.
func fail(err error) {
	line := time.Now().Format(time.RFC3339) + " crius-hook: " + err.Error() + "\n"
	if werr := appendLog(line); werr != nil {
		fmt.Fprint(os.Stderr, line)
		fmt.Fprintln(os.Stderr, "crius-hook: log not written:", werr)
	}
}

func appendLog(line string) (err error) {
	defer func() {
		if r := recover(); r != nil { // store.Home panics with no HOME
			err = fmt.Errorf("%v", r)
		}
	}()
	dir := filepath.Join(store.Home(), "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "crius-hook.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = io.WriteString(f, line)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
