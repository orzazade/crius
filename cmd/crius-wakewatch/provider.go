package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/playbook"
	"github.com/orzazade/crius/internal/store"
	"github.com/orzazade/crius/internal/tabsend"
	"github.com/orzazade/crius/internal/wakewatch"
	"github.com/orzazade/crius/internal/watchdog"
)

// providerCall bounds one sqlite3, t3, ssh or crius-tsend call of the provider-error sweep.
const providerCall = 30 * time.Second

// t3State is T3 Code's own store. `t3 thread show` prints messages only, not a run's error item, so the sweep reads the
// store, read-only.
func t3State() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".t3", "userdata", "statev2.sqlite")
}

// failedQuery gives each live thread whose latest run failed or was interrupted, with that run's newest error item.
const failedQuery = `SELECT t.thread_id AS id, t.title AS title, coalesce(p.title, '') AS project, r.run_id AS run,
 r.status AS status, coalesce(r.completed_at, '') AS ended,
 coalesce((SELECT i.payload_json FROM orchestration_v2_projection_turn_items i WHERE i.thread_id = r.thread_id
   AND i.run_id = r.run_id AND i.type = 'error' ORDER BY i.ordinal DESC LIMIT 1), '') AS error
FROM orchestration_v2_projection_threads t
JOIN orchestration_v2_projection_runs r ON r.thread_id = t.thread_id
 AND r.ordinal = (SELECT max(ordinal) FROM orchestration_v2_projection_runs WHERE thread_id = t.thread_id)
LEFT JOIN projection_projects p ON p.project_id = t.project_id
WHERE t.deleted_at IS NULL AND t.archived_at IS NULL AND p.deleted_at IS NULL AND p.archived_at IS NULL
 AND r.status IN ('failed', 'interrupted')`

// failedThreads reads T3 Code's store with sqlite3 (mode=ro: the T3 server keeps it open in WAL mode).
func failedThreads() ([]wakewatch.FailedThread, error) {
	db := t3State()
	if _, err := os.Stat(db); err != nil {
		return nil, err
	}
	out, err := run(nil, "/usr/bin/sqlite3", "-json", "-cmd", ".timeout 5000", "file:"+db+"?mode=ro", failedQuery)
	if err != nil {
		return nil, fmt.Errorf("sqlite3 %s: %v", db, err)
	}
	return parseFailedThreads(out)
}

// parseFailedThreads reads `sqlite3 -json` rows of failedQuery; no output is no rows. A row it cannot read is named in
// the error and the others are still given.
func parseFailedThreads(b []byte) ([]wakewatch.FailedThread, error) {
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, nil
	}
	var rows []struct{ ID, Title, Project, Run, Status, Ended, Error string }
	if err := json.Unmarshal(b, &rows); err != nil {
		return nil, fmt.Errorf("T3 failed threads: %v", err)
	}
	var out []wakewatch.FailedThread
	var errs []error
	for _, r := range rows {
		th := wakewatch.FailedThread{ID: r.ID, Title: r.Title, Project: r.Project, Run: r.Run, Status: r.Status}
		if r.Ended != "" {
			t, err := time.Parse(time.RFC3339Nano, r.Ended)
			if err != nil {
				errs = append(errs, fmt.Errorf("T3 thread %s: run end %q: %v", r.ID, r.Ended, err))
				continue
			}
			th.Ended = t
		}
		if r.Error != "" {
			var e struct {
				Failure struct{ Class, Message string }
			}
			if err := json.Unmarshal([]byte(r.Error), &e); err != nil {
				errs = append(errs, fmt.Errorf("T3 thread %s: its error item: %v", r.ID, err))
				continue
			}
			th.Class, th.Message = e.Failure.Class, e.Failure.Message
		}
		out = append(out, th)
	}
	return out, errors.Join(errs...)
}

// sendThread sends a thread one message with `t3 thread send` (a turn of its own when it is idle), the text on stdin.
func sendThread(id, text string) error {
	_, err := run(strings.NewReader(text), t3Bin(), "thread", "send", id, "-")
	return err
}

func t3Bin() string {
	if p := filepath.Join(store.Home(), "bin", "t3"); fileThere(p) {
		return p
	}
	return "t3"
}

// tabTarget: tmux session:window names only, as crius-tsend takes them, so a line in the tab list is never shell code.
var tabTarget = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_@+-]*:[A-Za-z0-9_][A-Za-z0-9_@+-]*$`)

// captureTab reads a remote tab's screen over the box's ssh (the playbook's hosts.json), dim text taken out.
func captureTab(t wakewatch.RemoteTab) (string, error) {
	if !tabTarget.MatchString(t.Target) {
		return "", fmt.Errorf("%q is not a tmux session:window", t.Target)
	}
	path := filepath.Join(store.Home(), "state", "playbook", "hosts.json")
	hosts, err := playbook.LoadHosts(path)
	if err != nil {
		return "", err
	}
	for _, h := range hosts {
		if h.Name == t.Box && len(h.SSH) > 0 {
			args := append(append([]string{}, h.SSH[1:]...), h.Target, "tmux capture-pane -p -e -t '"+t.Target+"'")
			out, err := run(nil, h.SSH[0], args...)
			if err != nil {
				return "", err
			}
			return tabsend.Visible(string(out)), nil
		}
	}
	return "", fmt.Errorf("no box %q in %s", t.Box, path)
}

// sendTab types the text into a remote tab with crius-tsend, which proves it was sent (exit 0 SENT).
func sendTab(t wakewatch.RemoteTab, text string) error {
	_, err := run(nil, filepath.Join(store.Home(), "bin", "crius-tsend"), t.Box, t.Target, text)
	return err
}

// run runs one command with providerCall as its limit and gives its stdout; a failure names its stderr's first line.
func run(stdin io.Reader, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), providerCall)
	defer cancel()
	var out, errOut bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, &out, &errOut
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			err = fmt.Errorf("no answer in %s", providerCall)
		}
		said, _, _ := strings.Cut(strings.TrimSpace(errOut.String()+"\n"+out.String()), "\n")
		return nil, fmt.Errorf("%s: %v: %s", filepath.Base(name), err, said)
	}
	return out.Bytes(), nil
}

// providerDeps is the live sweep: T3 Code's store, the tabs in wakewatch.ProviderTabsFile, Crius's bus and tab.
func providerDeps(now time.Time) wakewatch.ProviderDeps {
	home := store.Home()
	return wakewatch.ProviderDeps{Home: home, Now: now, Threads: failedThreads,
		Tabs: func() ([]wakewatch.RemoteTab, error) { return wakewatch.ReadProviderTabs(home) }, Capture: captureTab,
		SendThread: sendThread, SendTab: sendTab, SendCrius: send,
		Post: func(typ, text string) error {
			return watchdog.PostOn(home, "crius", typ, "crius:watchdog", "crius", now, text)
		}}
}

// fixtureDeps reads a folder of recorded data instead: threads.json (sqlite3 -json rows of failedQuery),
// provider-tabs.txt, one <box>-<session>-<window>.txt screen per tab (`tmux capture-pane -p -e`), and now.txt, the
// sweep's clock for the recording (RFC 3339; the real clock without it). It sends nothing.
func fixtureDeps(dir string, now time.Time) (wakewatch.ProviderDeps, error) {
	if b, err := os.ReadFile(filepath.Join(dir, "now.txt")); err == nil {
		if now, err = time.Parse(time.RFC3339, strings.TrimSpace(string(b))); err != nil {
			return wakewatch.ProviderDeps{}, fmt.Errorf("now.txt: %v", err)
		}
	}
	home, err := os.MkdirTemp("", "provider-demo-")
	if err != nil {
		return wakewatch.ProviderDeps{}, err
	}
	b, err := os.ReadFile(filepath.Join(dir, "provider-tabs.txt"))
	if err == nil {
		err = os.WriteFile(wakewatch.ProviderTabsFile(home), b, 0o600)
	}
	if err != nil {
		return wakewatch.ProviderDeps{}, err
	}
	none := func(...any) error { return errors.New("a fixture run sends nothing") }
	return wakewatch.ProviderDeps{Home: home, Now: now, Dry: true,
		Threads: func() ([]wakewatch.FailedThread, error) {
			b, err := os.ReadFile(filepath.Join(dir, "threads.json"))
			if err != nil {
				return nil, err
			}
			return parseFailedThreads(b)
		},
		Tabs: func() ([]wakewatch.RemoteTab, error) { return wakewatch.ReadProviderTabs(home) },
		Capture: func(t wakewatch.RemoteTab) (string, error) {
			b, err := os.ReadFile(filepath.Join(dir, t.Box+"-"+strings.ReplaceAll(t.Target, ":", "-")+".txt"))
			return tabsend.Visible(string(b)), err
		},
		SendThread: func(id, text string) error { return none() }, SendTab: func(wakewatch.RemoteTab, string) error { return none() },
		SendCrius: func(string) error { return none() }, Post: func(string, string) error { return none() },
	}, nil
}

// providerCmd is `crius-wakewatch provider-errors [-fixtures DIR]`: one dry sweep of the live sessions (or of recorded
// data), and what it saw and would do. It sends, posts and saves nothing: only the 5-min run restarts, so two runs never
// race on the state. Exit 1 when it could not look at something.
func providerCmd(args []string, w io.Writer) int {
	dir := ""
	switch {
	case len(args) == 0:
	case len(args) == 2 && args[0] == "-fixtures":
		dir = args[1]
	default:
		fmt.Fprintln(w, "usage: crius-wakewatch provider-errors [-fixtures DIR]")
		return 2
	}
	now := time.Now()
	d := providerDeps(now)
	if dir != "" {
		var err error
		if d, err = fixtureDeps(dir, now); err != nil {
			fmt.Fprintln(w, "crius-wakewatch provider-errors:", err)
			return 1
		}
		defer os.RemoveAll(d.Home)
	}
	d.Dry = true
	if dir == "" {
		if last, err := wakewatch.ProviderLastRun(d.Home); err == nil && !last.IsZero() {
			fmt.Fprintf(w, "last sweep by the 5-min watchdog run: %s\n", last.Local().Format("2006-01-02 15:04:05"))
		}
	}
	rep, err := wakewatch.ProviderErrors(d)
	now = d.Now
	fmt.Fprintf(w, "provider-error sweep %s: %d failed or interrupted T3 thread(s) read, %d remote tab(s) read, %d stopped by a provider error\n",
		now.Local().Format("2006-01-02 15:04:05"), rep.Threads, rep.Tabs, rep.Stopped)
	for _, a := range rep.Actions {
		fmt.Fprintln(w, "  "+a)
	}
	if err != nil {
		fmt.Fprintln(w, "crius-wakewatch provider-errors:", err)
		return 1
	}
	return 0
}
