// Package sessionstate is the state file crius-hook keeps for each Claude Code session, one JSON file per session id
// in ~/.crius/sessions/. Hooks write it; wakewatch and promptwatch read it before they read a screen (OpenRig lesson
// A and G, docs/notes/OPENRIG-LESSONS.md).
package sessionstate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// The states a session can be in.
const (
	Running = "running" // a prompt was submitted or a tool started
	Waiting = "waiting" // a dialog waits on a person: a permission prompt or a question
	Idle    = "idle"    // the turn ended, or the session started and waits for a prompt
	Ended   = "ended"   // the session ended (exit, /clear, logout)
)

// Input is the part of a Claude Code hook's stdin that matters here.
type Input struct {
	SessionID        string `json:"session_id"`
	Event            string `json:"hook_event_name"`
	Cwd              string `json:"cwd"`
	NotificationType string `json:"notification_type"`
	Source           string `json:"source"` // SessionStart: startup, resume, clear, compact
	Reason           string `json:"reason"` // SessionEnd
	ToolName         string `json:"tool_name"`
	Prompt           string `json:"prompt"`
	AgentID          string `json:"agent_id"` // set when a subagent fired the event
	IsInterrupt      bool   `json:"is_interrupt"`
}

// State is one session's file.
type State struct {
	SessionID string    `json:"session_id"`
	State     string    `json:"state"`
	Event     string    `json:"event"`            // the last hook event
	Detail    string    `json:"detail,omitempty"` // its notification type, start source, end reason or tool
	Time      time.Time `json:"time"`
	Cwd       string    `json:"cwd"`
	Name      string    `json:"name,omitempty"` // the session's -n name, as Claude Code's own session file has it
	Project   string    `json:"project,omitempty"`
	Role      string    `json:"role,omitempty"`
	Workspace string    `json:"workspace,omitempty"` // cmux workspace and surface the session runs in
	Surface   string    `json:"surface,omitempty"`
	Config    string    `json:"config,omitempty"` // the Claude Code config folder it runs under, where its status file is
	// PromptSHA is the first 16 hex digits of the sha256 of the last submitted prompt, trimmed: proof that a message
	// a watcher typed was submitted as itself, not mixed with a draft. The prompt itself is never stored.
	PromptSHA  string    `json:"prompt_sha,omitempty"`
	PromptTime time.Time `json:"prompt_time,omitzero"`
	// Waits names who has a dialog open: "main" or a subagent id, sorted. Subagents share the session id, so one
	// subagent's tool must not hide another's dialog.
	Waits []string `json:"waits,omitempty"`
}

// Apply gives the state after one hook event. The fields the hook fills from elsewhere (name, project, role, cmux
// workspace and surface) are kept from prev; the caller sets them.
//
// A dialog is open from PermissionRequest until the same agent's next tool event: its tool starts, ends or fails, or the
// auto-mode classifier denies it (an approved tool shows waiting until it ends: approving fires no hook). A dialog
// notification names no agent (Claude Code 2.1.308 sends no agent_id with it): it opens a dialog only when none is
// open, as "notify", which any agent's next tool event closes. A prompt, the end of the turn, an idle prompt, a new
// start and the end of the session close them all; so does Esc while a main-agent tool runs (is_interrupt, no Stop).
// Not seen by any hook: a person's "No" or Esc at a dialog, and Ctrl-C with no tool running, leave the last state until
// the next prompt or the idle prompt.
func Apply(prev State, in Input, now time.Time) State {
	s := prev
	s.SessionID, s.Event, s.Time, s.Detail = in.SessionID, in.Event, now, ""
	if in.Cwd != "" {
		s.Cwd = in.Cwd
	}
	who := in.AgentID
	if who == "" {
		who = "main"
	}
	switch in.Event {
	case "SessionStart":
		s.Detail = in.Source
		if in.Source != "compact" { // auto-compact fires in the middle of a turn: the session is still working
			s.State, s.Waits = Idle, nil
		}
	case "UserPromptSubmit":
		s.State, s.Waits = Running, nil // the box takes a prompt only when no dialog holds it
		s.PromptSHA, s.PromptTime = PromptSHA(in.Prompt), now
	case "PreToolUse", "PostToolUse", "PostToolUseFailure", "PermissionDenied":
		s.Waits = without(s.Waits, who, notify)
		s.State, s.Detail = busy(s.Waits), in.ToolName
		if in.IsInterrupt && in.AgentID == "" {
			s.State, s.Waits = Idle, nil
		}
	case "PermissionRequest": // the dialog shows; the permission_prompt notification comes seconds later
		s.Waits = with(s.Waits, who)
		s.State, s.Detail = Waiting, in.ToolName
	case "Notification":
		s.Detail = in.NotificationType
		switch in.NotificationType {
		case "permission_prompt", "elicitation_dialog", "elicitation_url_dialog", "worker_permission_prompt":
			if len(s.Waits) == 0 {
				s.Waits = []string{notify}
			}
			s.State = Waiting
		case "idle_prompt":
			s.State, s.Waits = Idle, nil
		}
	case "Stop":
		s.State, s.Waits = Idle, nil
	case "SessionEnd":
		s.State, s.Detail, s.Waits = Ended, in.Reason, nil
	}
	return s
}

// notify stands for a dialog only a notification told of: it names no agent.
const notify = "notify"

// busy is the state of a session that works: waiting while any agent has a dialog open, else running.
func busy(waits []string) string {
	if len(waits) > 0 {
		return Waiting
	}
	return Running
}

func with(waits []string, who string) []string {
	if slices.Contains(waits, who) {
		return waits
	}
	w := append(slices.Clone(waits), who)
	slices.Sort(w)
	return w
}

func without(waits []string, who ...string) []string {
	w := slices.DeleteFunc(slices.Clone(waits), func(x string) bool { return slices.Contains(who, x) })
	if len(w) == 0 {
		return nil
	}
	return w
}

// PromptSHA is how State.PromptSHA is computed from a prompt.
func PromptSHA(prompt string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(prompt)))
	return hex.EncodeToString(sum[:8])
}

// Write saves s as <dir>/<session id>.json in one step (a reader never sees half a file), creating dir.
func Write(dir string, s State) error {
	p, err := Path(dir, s.SessionID)
	if err != nil {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		os.Remove(f.Name())
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return err
	}
	if err := os.Rename(f.Name(), p); err != nil {
		os.Remove(f.Name())
		return err
	}
	return nil
}

// ErrDamaged is a session file that is there but cannot be read as a state.
var ErrDamaged = errors.New("damaged session file")

// Read gives the state saved for one session id.
func Read(dir, id string) (State, error) {
	p, err := Path(dir, id)
	if err != nil {
		return State{}, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return State{}, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return State{}, fmt.Errorf("%s: %w: %w", p, ErrDamaged, err)
	}
	return s, nil
}

// idRe is what Claude Code's session ids look like (a UUID) and a little more; anything that could name another
// folder is refused.
var idRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// Path is the file of one session id in dir; an id that could name another folder is an error.
func Path(dir, id string) (string, error) {
	if !idRe.MatchString(id) {
		return "", fmt.Errorf("session id %q is not a plain name", id)
	}
	return filepath.Join(dir, id+".json"), nil
}

// List gives the state of every session in dir, sorted by session id. A damaged file is named in the error (it wraps
// ErrDamaged) and the other states are still given. A missing dir is fs.ErrNotExist: no hook has written here.
func List(dir string) ([]State, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []State
	var errs []error
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || strings.HasPrefix(id, ".") || e.IsDir() {
			continue
		}
		s, err := Read(dir, id)
		if errors.Is(err, os.ErrNotExist) {
			continue // the session ended and its file went while we looked
		} else if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, s)
	}
	return out, errors.Join(errs...)
}

// RolePath is the file in dir that names a role's current session: <project>.<role>.json. A project or role that could
// name another folder is an error.
func RolePath(dir, project, role string) (string, error) {
	if !roleRe.MatchString(project) || !roleRe.MatchString(role) {
		return "", fmt.Errorf("project %q, role %q: want lowercase letters, digits and dashes", project, role)
	}
	return filepath.Join(dir, project+"."+role+".json"), nil
}

var roleRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// WriteRole makes s its role's current session (s.Project, s.Role). With replace false it writes only when the role
// has no file yet, in one step: two sessions racing for an empty role leave one whole file.
func WriteRole(dir string, s State, replace bool) error {
	p, err := RolePath(dir, s.Project, s.Role)
	if err != nil {
		return err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if replace {
		return os.Rename(f.Name(), p)
	}
	if err := os.Link(f.Name(), p); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	return nil
}

// ReadRole gives the state crius-hook kept for a role's current session.
func ReadRole(dir, project, role string) (State, error) {
	p, err := RolePath(dir, project, role)
	if err != nil {
		return State{}, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return State{}, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return State{}, fmt.Errorf("%s: %w: %w", p, ErrDamaged, err)
	}
	return s, nil
}

// Process finds the live process that has session id open, from Claude Code's own status files,
// <config>/sessions/<pid>.json: its pid and -n name, or pid 0 when no live process's file names the session (a crashed
// one leaves its file behind, and after /clear the same process names the new id).
func Process(config, id string) (pid int, name string, err error) {
	if config == "" {
		return 0, "", fmt.Errorf("session %s: no Claude config folder to look in", id)
	}
	files, err := filepath.Glob(filepath.Join(config, "sessions", "*.json"))
	if err != nil {
		return 0, "", err
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
			ProcStart string `json:"procStart"`
		}
		if json.Unmarshal(b, &st) != nil || st.SessionID != id {
			continue
		}
		live, err := Live(st.PID, st.ProcStart)
		if err != nil {
			return 0, "", fmt.Errorf("%s: %w", f, err)
		}
		if live {
			return st.PID, st.Name, nil
		}
	}
	return 0, "", nil
}

// Live reports whether pid is still the process that wrote a Claude Code status file. Claude writes its own start time
// in the file (procStart: UTC, as ctime prints it); a pid the system gave to a later process started at another time.
func Live(pid int, procStart string) (bool, error) {
	// pid 0 would test our own process group.
	if pid <= 0 || errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		return false, nil
	}
	want, err := time.Parse(time.ANSIC, procStart)
	if err != nil {
		return false, fmt.Errorf("pid %d: procStart %q: %w", pid, procStart, err)
	}
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) { // it ended while we looked
			return false, nil
		}
		return false, fmt.Errorf("pid %d: %w", pid, err)
	}
	return time.Unix(k.Proc.P_starttime.Sec, 0).Equal(want), nil
}
