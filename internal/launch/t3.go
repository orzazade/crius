package launch

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/sessionstate"
	"github.com/orzazade/crius/internal/wakewatch"
)

// T3 is what crius-spawn needs to run a role as a T3 Code thread instead of a cmux tab (crius-spawn -backend t3).
type T3 struct {
	// CLI runs the t3 command with stdin and gives its stdout; on failure an error that holds what it said.
	CLI func(stdin string, args ...string) (string, error)
	// Instances gives T3's Claude provider instances: id -> the Claude config folder each runs in (T3Instances).
	Instances func() (map[string]string, error)
	// Sessions gives crius-hook's session files: the one that names a thread proves which Claude session runs it.
	Sessions func() ([]sessionstate.State, error)
	Roles    string        // the roles folder ($CRIUS_HOME/roles)
	Wait     time.Duration // between two reads
	Tries    int           // reads before a step gives up
}

// T3Started is a role started as a thread: the thread, the Claude session crius-hook filed for it, the Max account
// and T3 provider instance it runs on, and the provenance tag line sent before the first prompt.
type T3Started struct{ Thread, Session, Account, Instance, Tag string }

// t3Thread is the part of `t3 thread show --json` read here.
type t3Thread struct {
	Thread *struct {
		Status   string `json:"status"`
		Instance string `json:"providerInstanceId"`
		Runs     int    `json:"runCount"`
		Pending  int    `json:"pendingRequestCount"`
		Archived bool   `json:"archived"`
	} `json:"thread"`
	Messages []struct {
		Role string `json:"role"`
	} `json:"messages"`
}

// RunT3 starts a role as a T3 Code thread, as Run does in a tab: on the Max account Pick gives (owner 10-28 and 11-19
// rules, as for a tab), on the T3 provider instance that runs in that account's config folder, in the project's launch
// folder, titled "<project> · <role>" (T3 gives Claude the title as its session name, so crius-hook files the role),
// with the role's model, and the provenance tag line alone on the line before the first prompt. It proves the thread
// started work (a run, or a reply already) on that instance and that crius-hook filed its Claude session in that
// account's folder, then makes sure the role record names that session and thread, filed in that folder.
func RunT3(s Spawn, d Deps, t T3) (T3Started, error) {
	if err := s.check(); err != nil {
		return T3Started{}, err
	}
	cwd, _, account, acct, dir, err := s.place(d)
	if err != nil {
		return T3Started{}, err
	}
	if acct == "" { // no aitch route in T3: the instance is the account
		return T3Started{}, fmt.Errorf("no signed-in Max folder for %s on account %s: a T3 thread needs one", s.Project, account)
	}
	inst, err := instanceFor(t, acct, dir)
	if err != nil {
		return T3Started{}, err
	}
	name, model, _ := command(s.Project, s.Role, acct, dir)
	tag := s.tag(account)
	out, err := t.CLI(tag+"\n"+s.Prompt, "thread", "start", "--json", "--title", name, "--provider", inst, "--model", model, cwd, "-")
	if err != nil {
		return T3Started{}, fmt.Errorf("t3 thread start: %v", err)
	}
	var started struct {
		Thread string `json:"threadId"`
	}
	if err := decode("t3 thread start", out, &started); err != nil {
		return T3Started{}, err
	}
	if started.Thread == "" || strings.HasPrefix(started.Thread, "-") {
		return T3Started{}, fmt.Errorf("t3 thread start printed no threadId: %.120q", out)
	}
	id := started.Thread
	// Work started: a run open, or a reply already (a short prompt can finish before the first read).
	var last string
	err = poll(t, func() (bool, error) {
		th, err := showT3(t, id)
		if err != nil {
			return false, err
		}
		if th.Thread.Instance != inst {
			return false, fmt.Errorf("thread %s runs on provider instance %s, not %s", id, th.Thread.Instance, inst)
		}
		last = th.Thread.Status
		return th.Thread.Status == "running" || th.replied(), nil
	})
	if err == errTries {
		err = fmt.Errorf("thread %s started no work: status %s, no reply after %d reads", id, last, t.Tries)
	}
	if err != nil {
		return T3Started{}, err
	}
	var sess sessionstate.State
	err = poll(t, func() (bool, error) {
		ss, err := t.Sessions()
		if err != nil {
			return false, err
		}
		for _, x := range ss {
			if x.Thread == id && (sess.SessionID == "" || x.Time.After(sess.Time)) {
				sess = x
			}
		}
		return sess.SessionID != "", nil
	})
	if err == errTries {
		err = fmt.Errorf("crius-hook filed no session for thread %s after %d reads: its role record cannot be written, so nothing watches it", id, t.Tries)
	}
	if err != nil {
		return T3Started{}, fmt.Errorf("%s (thread %s, running): %w", name, id, err)
	}
	if realPath(sess.Config) != realPath(dir) {
		return T3Started{}, fmt.Errorf("thread %s: its Claude session %s runs in %s, not %s (account %s)", id, sess.SessionID, sess.Config, dir, acct)
	}
	r, err := sessionstate.ReadRole(t.Roles, s.Project, s.Role)
	if err != nil || r.SessionID != sess.SessionID || r.Thread != id {
		// crius-hook files it too when the session's name is the title; this covers a hook that has not yet
		sess.Project, sess.Role, sess.Thread, sess.Filed = s.Project, s.Role, id, dir
		if err := sessionstate.WriteRole(t.Roles, sess, true); err != nil {
			return T3Started{}, fmt.Errorf("thread %s: role record: %w", id, err)
		}
	}
	return T3Started{Thread: id, Session: sess.SessionID, Account: acct, Instance: inst, Tag: tag}, nil
}

// ResumeT3 brings back a role that runs as a T3 Code thread (crius-spawn -resume): it sends ResumedPrompt into the
// thread as its next turn and proves a new run started. A thread that still runs a turn, waits on an approval or a
// question, or is archived is left alone.
func ResumeT3(thread string, t T3) error {
	th, err := showT3(t, thread)
	if err != nil {
		return err
	}
	switch {
	case th.Thread.Archived:
		return fmt.Errorf("thread %s is archived in T3: nothing resumed", thread)
	case th.Thread.Status == "running":
		return fmt.Errorf("thread %s still runs a turn: nothing resumed", thread)
	case th.Thread.Pending > 0:
		return fmt.Errorf("thread %s waits on %d open request(s), an approval or a question: answer it in T3; nothing resumed", thread, th.Thread.Pending)
	}
	before := th.Thread.Runs
	out, err := t.CLI(ResumedPrompt, "thread", "send", "--json", thread, "-")
	if err != nil {
		return fmt.Errorf("t3 thread send: %v", err)
	}
	var sent map[string]any
	if err := decode("t3 thread send", out, &sent); err != nil {
		return err
	}
	err = poll(t, func() (bool, error) {
		th, err := showT3(t, thread)
		if err != nil {
			return false, err
		}
		return th.Thread.Runs > before || th.Thread.Status == "running", nil
	})
	if err == errTries {
		return fmt.Errorf("sent the resume prompt, but no new run in thread %s after %d reads (look at the thread before sending again)", thread, t.Tries)
	}
	return err
}

// T3Instances reads T3 Code's settings.json (path) and gives its Claude provider instances: id -> the config folder
// each runs in, ~ expanded.
func T3Instances(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var v struct {
		Instances map[string]struct {
			Driver string `json:"driver"`
			Config struct {
				Home string `json:"homePath"`
			} `json:"config"`
		} `json:"providerInstances"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	out := map[string]string{}
	for id, in := range v.Instances {
		if in.Driver != "claudeAgent" || in.Config.Home == "" {
			continue
		}
		home := in.Config.Home
		if h, err := os.UserHomeDir(); err == nil && (home == "~" || strings.HasPrefix(home, "~/")) {
			home = filepath.Join(h, home[1:])
		}
		out[id] = home
	}
	return out, nil
}

// instanceFor is the T3 provider instance that runs in dir, the config folder of Max account acct.
func instanceFor(t T3, acct, dir string) (string, error) {
	all, err := t.Instances()
	if err != nil {
		return "", fmt.Errorf("T3 provider instances: %w", err)
	}
	var ids []string
	for id, home := range all {
		if realPath(home) == realPath(dir) {
			ids = append(ids, id)
		}
	}
	if ids == nil {
		return "", fmt.Errorf("no T3 provider instance runs in %s (account %s): add one in T3 Code's settings", dir, acct)
	}
	slices.Sort(ids)
	return ids[0], nil
}

// showT3 reads a thread with `t3 thread show --json`.
func showT3(t T3, id string) (t3Thread, error) {
	var th t3Thread
	out, err := t.CLI("", "thread", "show", "--json", "--messages", "5", id)
	if err != nil {
		return th, fmt.Errorf("t3 thread show %s: %v", id, err)
	}
	if err := decode("t3 thread show", out, &th); err != nil {
		return th, err
	}
	if th.Thread == nil {
		return th, fmt.Errorf("t3 thread show %s printed no thread: %.120q", id, out)
	}
	return th, nil
}

// replied says the thread's latest messages hold a reply.
func (th t3Thread) replied() bool {
	for _, m := range th.Messages {
		if m.Role == "assistant" {
			return true
		}
	}
	return false
}

// decode reads the JSON a t3 command printed into v. An older t3 printed a log line before it, so the JSON starts at
// the first line that opens with { ; output with no such line, or that does not parse, is an error.
func decode(what, out string, v any) error {
	s := out
	for !strings.HasPrefix(strings.TrimSpace(s), "{") {
		_, rest, ok := strings.Cut(s, "\n")
		if !ok {
			return fmt.Errorf("%s printed no JSON: %.120q", what, out)
		}
		s = rest
	}
	d := json.NewDecoder(bytes.NewReader([]byte(s)))
	if err := d.Decode(v); err != nil {
		return fmt.Errorf("%s printed no JSON (%v): %.120q", what, err, out)
	}
	return nil
}

var errTries = errors.New("gave up")

// poll calls step until it says done, at most t.Tries times with t.Wait between; errTries when it never did.
func poll(t T3, step func() (bool, error)) error {
	for i := 0; i < t.Tries; i++ {
		if i > 0 {
			time.Sleep(t.Wait)
		}
		done, err := step()
		if err != nil {
			return err
		}
		if done {
			return nil
		}
	}
	return errTries
}

// realPath is p with its links resolved, so ~/.claude-personal and ~/.claude are one folder; p cleaned when it cannot
// be resolved.
func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// RoleThread is the T3 Code thread of the role's newest session, "" when that session runs in none (a tab): the one
// wakewatch watches (wakewatch.NewestThread), as a role whose sessions moved to a new thread may still name the old one
// in its file r (11-17: cortex:lead).
func RoleThread(home, project, role string, r sessionstate.State) (string, error) {
	states, err := sessionstate.List(filepath.Join(home, "sessions"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, sessionstate.ErrDamaged) {
		return "", fmt.Errorf("crius's session files: %w", err)
	}
	return wakewatch.NewestThread(home, project, role, r, states), nil
}

// ErrT3Move says why a role in a T3 thread is not moved (owner 10-30: a move keeps the same session, with its full
// context). T3 changes a thread's account by changing its provider instance (its t3_thread_configure tool; the t3 CLI
// has no command for it), and that starts a new Claude session that gets only a summary of the thread: seen 11-18,
// AXI api-split went from session 0f892d3e on max3 to 6c5ae86a on max2, which opens with "Context handoff
// (full_thread_summary)".
var ErrT3Move = errors.New("T3 cannot move a thread to another account as the same session: a change of its provider " +
	"instance starts a new Claude session that gets only a summary of the thread; nothing moved")
