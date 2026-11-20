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
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
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

// run dispatches each project with a DISPATCH.toml; one project's error does not stop the others. Each project's error
// is said on Crius's bus as one ALERT naming the log; the same error on the next runs is not said again until resay
// has passed (a fix that did not hold must not go quiet: 11-17 to 11-21, 2760 failures after one ALERT). A clean run,
// or a new error, ends the quiet.
func run(home string, now time.Time, mk func(home, project string, now time.Time) dispatch.Deps) error {
	cfgs, err := filepath.Glob(filepath.Join(home, "state", "*", "DISPATCH.toml"))
	if err != nil {
		return err
	}
	var errs []error
	for _, c := range cfgs {
		project := filepath.Base(filepath.Dir(c))
		err := boardGone(filepath.Dir(c), now)
		if err == nil {
			err = dispatch.Run(mk(home, project, now))
		}
		if err == nil {
			if rerr := os.Remove(markPath(home, project)); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
				err = fmt.Errorf("could not clear the alert mark, so its next failure would not be said: %w", rerr)
			}
		}
		if err == nil {
			continue
		}
		err = fmt.Errorf("%s: %w", project, err)
		if aerr := alert(home, project, now, err); aerr != nil {
			err = fmt.Errorf("%w; %v", err, aerr)
		}
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// resay is how long one project's same error stays quiet before it is said again.
const resay = 6 * time.Hour

// markName is the file in <home>/state/<project>/ that keeps one project's last said error. It names the host: a
// project's state folder may be a copy of another machine's (legion-pull mirrors the Legion's state/quotes/), and that
// machine's mark must not stand in for this one's.
var markName = func() string {
	h, _ := os.Hostname()
	if h = strings.Split(h, ".")[0]; h == "" {
		h = "host"
	}
	return "dispatch-alert-" + h + ".json"
}()

// writeFile is os.WriteFile; a test makes it fail.
var writeFile = os.WriteFile

func markPath(home, project string) string { return filepath.Join(home, "state", project, markName) }

type mark struct {
	Key   string    `json:"key"`   // the error with its volatile numbers taken out
	Since time.Time `json:"since"` // its first run
	Said  time.Time `json:"said"`  // when it was last said
}

// alert says err on Crius's bus unless the project's mark holds the same error said less than resay ago. The mark is
// saved before the ALERT is posted; a mark that cannot be saved is said in the ALERT (which then repeats every run)
// and returned; an ALERT that cannot be posted takes its new mark back, so the next run tries again.
func alert(home, project string, now time.Time, err error) error {
	path := markPath(home, project)
	key := alertKey(err)
	var old mark
	prev, rerr := os.ReadFile(path)
	if rerr == nil && json.Unmarshal(prev, &old) == nil && old.Key == key && now.Sub(old.Said) < resay {
		return nil
	}
	m := mark{Key: key, Since: now, Said: now}
	// the log first: the bus cuts a line at 300 characters
	logp := "(log " + filepath.Join(home, "logs", "dispatch.log") + "): "
	text := "dispatcher failed " + logp + err.Error()
	if rerr == nil && old.Key == key {
		m.Since = old.Since
		text = "dispatcher still failing since " + old.Since.Format("2006-01-02T15:04") + " " + logp + err.Error()
	}
	next, _ := json.Marshal(m)
	werr := writeFile(path, next, 0o644)
	if werr != nil {
		text = "could not save the alert mark, so this repeats every run (why: in the log); " + text // short: 300 characters
		werr = fmt.Errorf("the alert mark was not saved: %w", werr)
	}
	if perr := watchdog.PostOn(home, "crius", "ALERT", "crius:dispatch", "crius:lead", now, text); perr != nil {
		var berr error
		if werr == nil { // take the new mark back: this ALERT was not said, and the next run must try again
			if rerr == nil {
				berr = writeFile(path, prev, 0o644)
			} else {
				berr = os.Remove(path)
			}
			if berr != nil {
				berr = fmt.Errorf("the old alert mark was not put back, so the next runs may be quiet: %w", berr)
			}
		}
		return errors.Join(werr, fmt.Errorf("and the ALERT was not posted: %v", perr), berr)
	}
	return werr
}

// boardGone is the error for a project whose board was taken away while its DISPATCH.toml stayed: no BOARD.toml, and
// a BOARD.toml.lock this user may not write (quotes 11-21: its board moved to the Legion, and legion-pull leaves the
// copied lock read-only). A project with no rows yet has a writable lock and no board: it is not gone.
func boardGone(dir string, now time.Time) error {
	if _, err := os.Lstat(filepath.Join(dir, "BOARD.toml")); !errors.Is(err, os.ErrNotExist) {
		return nil
	}
	lock := filepath.Join(dir, "BOARD.toml.lock")
	if _, err := os.Lstat(lock); err != nil || syscall.Access(lock, 2) == nil { // 2: W_OK
		return nil
	}
	return fmt.Errorf("this project's board is gone (no BOARD.toml, only a read-only BOARD.toml.lock); switch dispatch off: mv DISPATCH.toml DISPATCH.toml.off-%s", now.Format("0102"))
}

// volatile are the numbers that change from run to run with no new cause: a tab's surface, workspace or pid, and
// durations, and the date in boardGone's advice (.off-MMDD); row ids and roles stay, so the same error on another row is
// said.
var volatile = regexp.MustCompile(`\b(surface|workspace|window|pane|pid)[: ]\d+|\b\d+(\.\d+)?(ms|s|m|h|%)\b|\.off-\d{4}\b`)

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
		Target: func(pr int) (string, error) {
			out, err := tool(time.Minute, "az", "repos", "pr", "show", "--id", strconv.Itoa(pr), "--query", "targetRefName", "-o", "tsv")
			if t := strings.TrimSpace(string(out)); err != nil || !strings.HasPrefix(t, "refs/heads/") {
				return "", fmt.Errorf("az repos pr show %d: no target branch in %q: %v", pr, t, err)
			}
			return strings.TrimSpace(string(out)), nil
		},
		Merge: func(command string) (string, error) {
			// launchd stops the job with SIGTERM (a reinstall): the merge command, in its own group, stops with it
			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
			defer stop()
			return runMerge(ctx, command, 20*time.Minute)
		},
		StartLand: startLand,
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

// runMerge runs the project's merge command under bash with a time limit and gives its output, stdout and stderr
// together; a non-zero exit is an error.
func runMerge(parent context.Context, command string, limit time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(parent, limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/bash", "-c", command)
	// its own process group, killed whole at the limit: an az call it started could still complete the PR (review P3-1)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	switch {
	case parent.Err() != nil:
		return string(out), fmt.Errorf("merge command: its dispatcher was stopped, and it with all it started: %w", dispatch.ErrOutcomeUnknown)
	case ctx.Err() != nil:
		return string(out), fmt.Errorf("merge command: no answer in %v; it and all it started were stopped: %w", limit, dispatch.ErrOutcomeUnknown)
	}
	return string(out), err
}

// startLand starts the land command under bash in a session of its own (it outlives this dispatcher run; launchd ends
// only this job's group), its output to log and its exit code into rc when it ends. The lock is taken here, before the
// start, and handed to bash, so it is held from before startLand returns until bash ends, after it writes rc: a dispatcher
// that finds the lock free reads the run as ended. (Under lockf the lock was taken only some time after the start, so a
// look in that gap read a fresh run as ended with no rc, and the look itself could make lockf give up: flaky
// TestStartLandRunsDetachedUnderItsLock, 11-20.) A lock already held is an error, and no command runs.
func startLand(command, lock, log, rc string) error {
	l, err := os.OpenFile(lock, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer l.Close() // bash keeps its own copy, and with it the lock
	if err := syscall.Flock(int(l.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); errors.Is(err, syscall.EWOULDBLOCK) {
		return fmt.Errorf("the land lock %s is held: another landing run is going", lock)
	} else if err != nil {
		return fmt.Errorf("taking the land lock: %w", err)
	}
	out, err := os.OpenFile(log, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	// the lock is fd 3 in bash, closed for the command (what it leaves running does not hold it); a subshell: an exit in
	// it still writes rc
	cmd := exec.Command("/bin/bash", "-c", "(\n"+command+"\n) 3>&-\necho $? > "+q(rc))
	cmd.Stdout, cmd.Stderr = out, out
	cmd.ExtraFiles = []*os.File{l}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
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
