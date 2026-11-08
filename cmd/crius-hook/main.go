// Command crius-hook is a Claude Code hook for SessionStart, UserPromptSubmit, PreToolUse, PostToolUse,
// PostToolUseFailure, PermissionRequest, PermissionDenied, Notification, Stop, StopFailure and SessionEnd. It keeps one state
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
	"github.com/orzazade/crius/internal/quota"
	"github.com/orzazade/crius/internal/sessionstate"
	"github.com/orzazade/crius/internal/store"
)

func main() {
	at := time.Now() // the event's time, before any slow read: one drain applies its queued events in this order
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
	if err := record(in, at); err != nil {
		fail(err) // each error names its event: this process may apply others' events too
	}
	// Ended sessions' files would stay forever: each start prunes them, after its own lock has gone. Prune holds a
	// session's lock without applying its queue, and a hook killed while it held one leaves its queue: each start then
	// applies every session's queue it can lock.
	if in.Event == "SessionStart" {
		dir := filepath.Join(store.Home(), "sessions")
		if err := sessionstate.Prune(dir, time.Now()); err != nil {
			fail(fmt.Errorf("prune: %w", err))
		}
		ids, err := sessionstate.QueuedSessions(dir)
		if err != nil {
			err = fmt.Errorf("queued sessions: %w", err)
		}
		for _, id := range ids {
			err = errors.Join(err, drain(store.Home(), dir, id, at.Add(drainFor)))
		}
		if err != nil {
			fail(err)
		}
	}
}

// record queues one hook event and applies the session's queue (drain). What only this process can see (its config
// folder, the session's live status file, its cmux env) goes into the event. A missing projects.toml or Claude Code
// status file loses the project, role or name, not the state: it is logged and the state is written.
func record(in sessionstate.Input, at time.Time) error {
	home := store.Home()
	dir := filepath.Join(home, "sessions")
	ev := sessionstate.Event{In: in, Time: at, Workspace: os.Getenv("CMUX_WORKSPACE_ID"), Surface: os.Getenv("CMUX_SURFACE_ID")}
	cfg, err := claudeConfig()
	if err == nil {
		ev.PID, ev.Name, err = sessionstate.Process(cfg, in.SessionID)
	}
	if err != nil {
		fail(err)
	}
	ev.Config = cfg
	if err := sessionstate.Queue(dir, ev); err != nil {
		return fmt.Errorf("%s %s: queue: %w, event lost", in.Event, in.SessionID, err)
	}
	return drain(home, dir, in.SessionID, at.Add(drainFor))
}

// drainFor is how long after its start a hook applies queued events: well inside Claude Code's 5 s hook timeout, as
// the holder may be a PreToolUse hook that holds up the tool (review of the queue P2-1).
const drainFor = 3 * time.Second

// drain applies the session's queued events while it can take the session's lock at once. A hook that finds the lock
// held leaves them to the holder, and every holder looks at the queue again after it lets go: the last hook to let go
// sees every event queued before it did, so none is lost and none waits on the lock (crius:lead 11-08). Events left at
// the deadline wait for the session's next hook and are logged; one applied a minute late is logged too.
func drain(home, dir, id string, deadline time.Time) error {
	var errs []error
	for {
		unlock, err := sessionstate.Lock(dir, id, 0)
		if errors.Is(err, sessionstate.ErrLocked) {
			return errors.Join(errs...)
		} else if err != nil {
			return errors.Join(append(errs, fmt.Errorf("%s: %w, its queued events wait for its next hook", id, err))...)
		}
		paths, err := sessionstate.Queued(dir, id)
		if err != nil {
			unlock()
			return errors.Join(append(errs, fmt.Errorf("%s: %w, its queued events wait for its next hook", id, err))...)
		}
		for i, p := range paths {
			if time.Now().After(deadline) {
				unlock()
				return errors.Join(append(errs, fmt.Errorf("%s: %d queued events left for its next hook at the %s limit", id, len(paths)-i, drainFor))...)
			}
			if err := applyQueued(home, dir, id, p); err != nil {
				errs = append(errs, err)
			}
			// applied or not, it goes: kept, it would be applied again; gone already, Prune took it
			if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
				unlock()
				return errors.Join(append(errs, fmt.Errorf("%w, its queue waits for its next hook", err))...)
			}
		}
		unlock()
		if left, err := sessionstate.Queued(dir, id); err != nil || len(left) == 0 {
			return errors.Join(append(errs, err)...)
		}
	}
}

// applyQueued applies one queued file. A panic is that event's error, so one bad event cannot stop its session's queue.
func applyQueued(home, dir, id, path string) (err error) {
	name := filepath.Base(path)
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%s %s: panic: %v", name, id, r)
		}
	}()
	ev, err := sessionstate.ReadQueued(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil // Prune took it after 30 days and named it
	} else if err != nil {
		return fmt.Errorf("%s: %w", id, err)
	}
	name = ev.In.Event
	if late := time.Since(ev.Time); late > time.Minute {
		fail(fmt.Errorf("%s %s applied %s late", name, id, late.Round(time.Second)))
	}
	if err := apply(home, dir, ev); err != nil {
		return fmt.Errorf("%s %s: %w", name, id, err)
	}
	return nil
}

// apply writes one queued event into the session's file, and its role's file when it names the role's session. The
// caller holds the session's lock.
func apply(home, dir string, ev sessionstate.Event) error {
	in := ev.In
	prev, err := sessionstate.Read(dir, in.SessionID)
	if errors.Is(err, sessionstate.ErrDamaged) {
		fail(fmt.Errorf("replacing it: %w", err))
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	at := ev.Time // its own time, so one applied late is not news; never before the last, so time does not go back
	if at.Before(prev.Time) {
		at = prev.Time
	}
	s := sessionstate.Apply(prev, in, at)
	if in.Event == "UserPromptSubmit" {
		s.PromptSHA = ev.PromptSHA // the queue keeps only the prompt's hash
	}
	if ev.PID != 0 { // a live session resumed without -n has no name now
		s.Name = ev.Name
	}
	s.Config = ev.Config
	if p, r, err := ingest.Filing(home, s.Cwd, s.Name); err != nil {
		configFailed(home, dir, err)
	} else {
		s.Project, s.Role = p, r
	}
	// the session's own env on every event: one resumed outside cmux no longer names its old surface
	s.Workspace, s.Surface = ev.Workspace, ev.Surface
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
	// A restart of the role's own session, or a new id in its tab other than a fresh start (/clear, whose first event
	// may come before its start), keeps the folder its start filed (Crius 10-29 00:28: cmux restored sessions with no
	// CLAUDE_CONFIG_DIR, on another account); wakewatch names one that runs elsewhere.
	s.Filed = s.Config
	if replace {
		cur, err := sessionstate.ReadRole(roles, s.Project, s.Role)
		sameTab := s.Surface != "" && cur.Surface == s.Surface && !(in.Event == "SessionStart" && s.Detail == "startup")
		if err == nil && (cur.SessionID == s.SessionID || sameTab) {
			s.Filed = cur.FiledConfig()
			// A move to another Max account of the same persona is filed (crius:lead 11-07 19:58), so -resume
			// resumes it where it now runs.
			if s.Filed != s.Config {
				as, err := quota.LoadAccounts(home)
				if err != nil {
					fail(fmt.Errorf("role %s.%s keeps its filed folder %s, as accounts.toml does not read: %w", s.Project, s.Role, s.Filed, err))
				} else if as.SameSeat(s.Filed, s.Config) {
					s.Filed = s.Config
				}
			}
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
