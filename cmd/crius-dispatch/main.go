// Command crius-dispatch runs the dispatcher (package dispatch) once for every project with a
// $CRIUS_HOME/state/<project>/DISPATCH.toml; launchd runs it every minute. No model runs in it: it starts sessions with
// crius-spawn, ends them with crius-spawn -close, reads PR states with az and builds with ci-proof (both read-only) and
// posts on the project's bus.
// A project with no DISPATCH.toml is left alone; remove the file to stop dispatching there.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/dispatch"
	"github.com/orzazade/crius/internal/sessionstate"
	"github.com/orzazade/crius/internal/store"
	"github.com/orzazade/crius/internal/wakewatch"
	"github.com/orzazade/crius/internal/watchdog"
)

func main() {
	home := store.Home()
	if err := run(home, time.Now(), deps); err != nil {
		fmt.Fprintln(os.Stderr, time.Now().Format(time.RFC3339), "crius-dispatch:", err)
		os.Exit(1)
	}
}

// run dispatches each project with a DISPATCH.toml; one project's error does not stop the others, and each is said
// once on Crius's bus as an ALERT (a dispatcher that fails quietly leaves rows waiting with no one told).
func run(home string, now time.Time, mk func(home, project string, now time.Time) dispatch.Deps) error {
	cfgs, err := filepath.Glob(filepath.Join(home, "state", "*", "DISPATCH.toml"))
	if err != nil {
		return err
	}
	var errs []error
	for _, c := range cfgs {
		project := filepath.Base(filepath.Dir(c))
		if err := dispatch.Run(mk(home, project, now)); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", project, err))
		}
	}
	err = errors.Join(errs...)
	seen := filepath.Join(home, "state", "dispatch.error")
	if err == nil {
		os.Remove(seen) // a literal path: the next failure is said again
		return nil
	}
	// the same error with only its volatile numbers changed (a new tab's surface, a duration) is not said again
	key := alertKey(err)
	if last, _ := os.ReadFile(seen); string(last) != key {
		if perr := watchdog.PostOn(home, "crius", "ALERT", "crius:dispatch", "crius:lead", now, "dispatcher failed: "+err.Error()); perr != nil {
			return fmt.Errorf("%w; and the ALERT was not posted: %v", err, perr)
		}
		os.WriteFile(seen, []byte(key), 0o644)
	}
	return err
}

// volatile are the numbers that change from run to run with no new cause: a tab's surface, workspace or pid, and
// durations; row ids and roles stay, so the same error on another row is said.
var volatile = regexp.MustCompile(`\b(surface|workspace|window|pane|pid)[: ]\d+|\b\d+(\.\d+)?(ms|s|m|h|%)\b`)

// alertKey is an error with its volatile numbers taken out.
func alertKey(err error) string { return volatile.ReplaceAllString(err.Error(), "#") }

const toolWait = 3 * time.Minute // crius-spawn waits for the tab, the trust dialog and the first prompt

func deps(home, project string, now time.Time) dispatch.Deps {
	bin := filepath.Join(home, "bin")
	return dispatch.Deps{Home: home, Project: project, Now: now,
		Spawn: func(role, prompt string) error {
			_, err := tool(toolWait, filepath.Join(bin, "crius-spawn"), "-account", "max", project, role, prompt)
			return err
		},
		Close: func(role string) error {
			_, err := tool(time.Minute, filepath.Join(bin, "crius-spawn"), "-close", project, role)
			return err
		},
		Alive: func(role string) (bool, error) { return alive(home, project, role) },
		PRState: func(pr int) (string, error) {
			out, err := tool(time.Minute, "az", "repos", "pr", "show", "--id", strconv.Itoa(pr), "-o", "json")
			if err != nil {
				return "", err
			}
			var p struct {
				Status string `json:"status"`
			}
			if err := json.Unmarshal(out, &p); err != nil || p.Status == "" {
				return "", fmt.Errorf("az repos pr show %d: no status in its answer", pr)
			}
			return p.Status, nil
		},
		Proof: func(pr, pipeline int) (string, error) { return ciProof(filepath.Join(bin, "ci-proof"), pr, pipeline) },
		Post: func(typ, to, text string) error {
			return watchdog.PostOn(home, project, typ, "crius:dispatch", to, time.Now(), text)
		},
	}
}

// ciProof runs ci-proof for one PR, with -pipeline when pipeline > 0. Its exit 1 (FAIL, STALE, NONE) is an answer; exit
// 2, or any other failure, is an error that carries its LOUD line.
func ciProof(bin string, pr, pipeline int) (string, error) {
	args := []string{strconv.Itoa(pr)}
	if pipeline > 0 {
		args = append([]string{"-pipeline", strconv.Itoa(pipeline)}, args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	line := strings.TrimSpace(string(out))
	var ee *exec.ExitError
	switch {
	case ctx.Err() != nil:
		return "", fmt.Errorf("ci-proof %d: no answer in 3m", pr)
	case line != "" && (err == nil || errors.As(err, &ee) && ee.ExitCode() == 1):
		return line, nil
	default:
		return "", fmt.Errorf("ci-proof %d: %v: %s", pr, err, line)
	}
}

// alive says whether the role's current session (crius-hook's role file) runs in a Claude now, not closed on purpose.
func alive(home, project, role string) (bool, error) {
	dir := filepath.Join(home, "roles")
	r, err := sessionstate.ReadRole(dir, project, role)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if closed, err := sessionstate.ClosedRole(dir, project, role); err != nil {
		return false, err
	} else if closed == r.SessionID {
		return false, nil
	}
	user, err := os.UserHomeDir()
	if err != nil {
		return false, err
	}
	ss, err := wakewatch.LiveSessions(user)
	if err != nil {
		return false, err
	}
	for _, s := range ss {
		if s.ID == r.SessionID {
			return true, nil
		}
	}
	return false, nil
}

// tool runs a command with a time limit; an error carries the end of its output.
func tool(limit time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("%s: no answer in %v", filepath.Base(name), limit)
	}
	if err != nil {
		msg := ""
		if ee, ok := err.(*exec.ExitError); ok {
			msg = strings.TrimSpace(string(ee.Stderr))
			if i := strings.LastIndexByte(msg, '\n'); i >= 0 {
				msg = msg[i+1:]
			}
		}
		return nil, fmt.Errorf("%s %s: %v: %s", filepath.Base(name), strings.Join(args[:min(2, len(args))], " "), err, msg)
	}
	return out, nil
}
