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
	// Ended sessions' files would stay forever: each start prunes them, after its own lock has gone.
	if in.Event == "SessionStart" {
		if err := sessionstate.Prune(filepath.Join(store.Home(), "sessions"), time.Now()); err != nil {
			fail(fmt.Errorf("prune: %w", err))
		}
	}
}

// record applies one hook event to the session's file. A missing projects.toml or Claude Code status file loses
// the project, role or name, not the state: it is logged and the state is written. A damaged file is logged and
// replaced. Events of one session run as parallel processes, so the read, change and write hold the session's lock.
func record(in sessionstate.Input) error {
	home := store.Home()
	dir := filepath.Join(home, "sessions")
	pid, name := 0, ""
	cfg, err := claudeConfig()
	if err == nil {
		pid, name, err = sessionstate.Process(cfg, in.SessionID)
	}
	if err != nil {
		fail(err)
	}
	// A lock held for a second skips the event rather than wait until Claude Code's hook timeout.
	unlock, err := sessionstate.Lock(dir, in.SessionID, time.Second)
	if err != nil {
		return fmt.Errorf("%w, event skipped", err)
	}
	defer unlock()
	prev, err := sessionstate.Read(dir, in.SessionID)
	if errors.Is(err, sessionstate.ErrDamaged) {
		fail(fmt.Errorf("replacing it: %w", err))
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	s := sessionstate.Apply(prev, in, time.Now())
	if pid != 0 { // a live session resumed without -n has no name now
		s.Name = name
	}
	s.Config = cfg
	if p, r, err := ingest.Filing(home, s.Cwd, s.Name); err != nil {
		configFailed(home, dir, err)
	} else {
		s.Project, s.Role = p, r
	}
	// the session's own env on every event: one resumed outside cmux no longer names its old surface
	s.Workspace, s.Surface = os.Getenv("CMUX_WORKSPACE_ID"), os.Getenv("CMUX_SURFACE_ID")
	if err := sessionstate.Write(dir, s); err != nil {
		return err
	}
	// The role's current session, for crius-spawn -resume: a start makes this one current; any other event names it
	// only when the role has none, as a session started before the hook went live has no start to go by, or when the
	// role's session no longer runs.
	if s.Project == "" || s.Role == "unknown" {
		return nil
	}
	roles := filepath.Join(home, "roles")
	replace := in.Event == "SessionStart"
	if !replace {
		if replace, err = takesRole(roles, s); err != nil {
			return err
		}
	}
	return sessionstate.WriteRole(roles, s, replace)
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

// claudeConfig is the Claude Code config folder this session runs under, where its own status file is.
func claudeConfig() (string, error) {
	if cfg := os.Getenv("CLAUDE_CONFIG_DIR"); cfg != "" {
		return cfg, nil
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, ".claude"), nil
}

// takesRole tells whether s, not at its start, becomes its role's current session: when the role's session is another
// one that no longer runs (it crashed, or /clear gave its process a new id before this one's start was filed). A role
// with no file yet is taken by WriteRole without replacing.
func takesRole(roles string, s sessionstate.State) (bool, error) {
	cur, err := sessionstate.ReadRole(roles, s.Project, s.Role)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case errors.Is(err, sessionstate.ErrDamaged):
		fail(fmt.Errorf("replacing it: %w", err))
		return true, nil
	case err != nil:
		return false, err
	case cur.SessionID == s.SessionID:
		return false, nil // kept as its start filed it: a later event's folder may be one a tool cd'd into
	}
	pid, _, err := sessionstate.Process(cur.Config, cur.SessionID)
	return pid == 0 && err == nil, err
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
