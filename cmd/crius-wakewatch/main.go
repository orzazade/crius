// Command crius-wakewatch runs one missed-wake check (internal/wakewatch) and exits 1 when it could not finish or not
// reach Crius's tab. launchd starts it every 5 min.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/orzazade/crius/internal/store"
	"github.com/orzazade/crius/internal/wakewatch"
)

var (
	swapRe = regexp.MustCompile(`used = ([\d.]+)([MG])`)
	cmux   = runCmux
)

// runCmux gives cmux's stdout, or on failure its stdout and stderr: a warning on stderr must not reach a JSON parse.
func runCmux(args ...string) ([]byte, error) {
	var out, msg bytes.Buffer
	cmd := exec.Command("cmux", args...)
	cmd.Stdout, cmd.Stderr = &out, &msg
	if err := cmd.Run(); err != nil {
		return append(out.Bytes(), msg.Bytes()...), err
	}
	return out.Bytes(), nil
}

func main() {
	os.Setenv("PATH", toolPath())
	err := wakewatch.Run(wakewatch.Deps{Home: store.Home(), Now: time.Now(), Screens: screens, Send: send,
		Resources: resources, Tools: tools, Sessions: func() ([]wakewatch.Session, error) {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, err
			}
			return sessions(home)
		}})
	if err != nil {
		fmt.Fprintln(os.Stderr, time.Now().Format(time.RFC3339), "crius-wakewatch:", err)
		os.Exit(1)
	}
}

// terminal is one terminal tab in cmux.
type terminal struct{ Workspace, Surface, Title, TTY string }

// terminals lists every terminal tab from cmux's JSON tree. The text tree prints titles as they are, so a title can
// look like another tab's row.
func terminals() ([]terminal, error) {
	b, err := cmux("--json", "tree", "--all")
	if err != nil {
		return nil, fmt.Errorf("cmux tree: %v: %s", err, strings.TrimSpace(string(b)))
	}
	var tree struct {
		Windows []struct {
			Workspaces []struct {
				Ref   string
				Panes []struct {
					Surfaces []struct{ Ref, Title, TTY, Type string }
				}
			}
		}
	}
	if err := json.Unmarshal(b, &tree); err != nil {
		return nil, fmt.Errorf("cmux --json tree: %v", err)
	}
	var out []terminal
	for _, w := range tree.Windows {
		for _, ws := range w.Workspaces {
			for _, p := range ws.Panes {
				for _, s := range p.Surfaces {
					if s.Type == "terminal" {
						out = append(out, terminal{ws.Ref, s.Ref, s.Title, s.TTY})
					}
				}
			}
		}
	}
	return out, nil
}

// screens reads the last 30 lines of every terminal tab in cmux. A tab whose read fails comes back with Err set.
func screens() ([]wakewatch.Screen, error) {
	tabs, err := terminals()
	if err != nil {
		return nil, err
	}
	var out []wakewatch.Screen
	for _, tab := range tabs {
		text, err := cmux("read-screen", "--surface", tab.Surface, "--lines", "30")
		if err != nil {
			out = append(out, wakewatch.Screen{Surface: tab.Surface, Title: tab.Title, Err: fmt.Sprintf("cmux read-screen: %v: %s", err, strings.TrimSpace(string(text)))})
			continue
		}
		out = append(out, wakewatch.Screen{Surface: tab.Surface, Title: tab.Title, Text: string(text)})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("cmux tree lists no terminal tab")
	}
	return out, nil
}

// send types text into Crius's own tab, then Enter. The tab is the one terminal in cmux titled "crius" (a file naming
// it went stale when Crius handed over to a new tab). None or several is loud and types nothing: a cmux send with no
// target goes to the caller's tab, and the alert is sent again on the next run.
func send(text string) error {
	tabs, err := terminals()
	if err != nil {
		return err
	}
	var found []terminal
	for _, tab := range tabs {
		if tab.Title == wakewatch.CriusTitle {
			found = append(found, tab)
		}
	}
	if len(found) != 1 {
		return fmt.Errorf("cmux tree --all has %d terminals titled %q, want one (Crius's own tab): nothing typed", len(found), wakewatch.CriusTitle)
	}
	if err := claudeInFront(found[0]); err != nil {
		return err
	}
	ws, surface := found[0].Workspace, found[0].Surface
	draft, err := inputBox(ws, surface)
	if err != nil {
		return err
	}
	// An earlier line whose Enter failed is still in the prompt. Enter goes on it only if the whole draft is that
	// line, as typed: a draft the owner edited or wrote is left alone, and nothing is typed over it.
	pending := filepath.Join(store.Home(), "state", "wakewatch-pending")
	last, err := os.ReadFile(pending)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if draft != "" {
		if err != nil || squash(draft) != squash(string(last)) {
			return fmt.Errorf("Crius's prompt holds a draft that is not wakewatch's: nothing typed")
		}
		if out, err := cmux("send-key", "--workspace", ws, "--surface", surface, "enter"); err != nil {
			return fmt.Errorf("cmux send-key: %v: %s", err, strings.TrimSpace(string(out)))
		}
		if draft, err := waitBox(ws, surface, ""); err != nil || draft != "" {
			return fmt.Errorf("Crius's prompt still holds %q after Enter on wakewatch's earlier line: %v", draft, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(pending), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(pending, []byte(text), 0o600); err != nil {
		return err
	}
	if out, err := cmux("send", "--workspace", ws, "--surface", surface, "--", text); err != nil {
		return fmt.Errorf("cmux send: %v: %s", err, strings.TrimSpace(string(out)))
	}
	// Someone may have typed at the same moment: Enter would send the mix.
	if draft, err = waitBox(ws, surface, text); err != nil {
		return err
	}
	if squash(draft) != squash(text) {
		return fmt.Errorf("Crius's prompt holds %q after typing, not the alert alone: Enter not pressed", draft)
	}
	if out, err := cmux("send-key", "--workspace", ws, "--surface", surface, "enter"); err != nil {
		return fmt.Errorf("cmux send-key: %v: %s", err, strings.TrimSpace(string(out)))
	}
	// The line stays pending until the box is empty, so the next send can finish it.
	if draft, err = waitBox(ws, surface, ""); err != nil || draft != "" {
		return fmt.Errorf("Crius's prompt still holds %q after Enter: %v", draft, err)
	}
	return os.Remove(pending)
}

var boxWait = 100 * time.Millisecond

// waitBox reads Crius's input box until its draft is want (white space aside), for up to 20 reads: Claude draws a
// key's effect a moment after it arrives. It gives the last draft it read.
func waitBox(workspace, surface, want string) (draft string, err error) {
	for i := 0; i < 20; i++ {
		if i > 0 {
			time.Sleep(boxWait)
		}
		if draft, err = inputBox(workspace, surface); err == nil && squash(draft) == squash(want) {
			return draft, nil
		}
	}
	return draft, err
}

// inputBox reads Crius's screen and gives the draft in Claude's input box. Without the box on screen (bash mode, a
// dialog, a picker) Enter would run or pick something, so that is an error.
func inputBox(workspace, surface string) (string, error) {
	screen, err := cmux("read-screen", "--workspace", workspace, "--surface", surface, "--lines", "40")
	if err != nil {
		return "", fmt.Errorf("cmux read-screen %s: %v: %s", surface, err, strings.TrimSpace(string(screen)))
	}
	draft, boxed := wakewatch.Prompt(string(screen))
	if !boxed {
		return "", fmt.Errorf("Crius's tab %s shows no Claude input box: nothing typed", surface)
	}
	return draft, nil
}

// squash drops all white space, so a line the input box wrapped equals the line as typed.
func squash(s string) string { return strings.Join(strings.Fields(s), "") }

// claudeInFront checks that Claude is the foreground process of the tab's terminal. If Claude has exited, the tab is a
// shell, and Enter would run the alert text there.
func claudeInFront(tab terminal) error {
	surface, tty := tab.Surface, tab.TTY
	if tty == "" {
		return fmt.Errorf("Crius's tab %s %s has no tty in cmux tree --all: nothing typed", tab.Workspace, surface)
	}
	out, err := exec.Command("ps", "-t", tty, "-o", "pid=,tpgid=,comm=").Output()
	if err != nil {
		return fmt.Errorf("ps -t %s: %v", tty, err)
	}
	front := ""
	for _, line := range strings.Split(string(out), "\n") {
		if f := strings.Fields(line); len(f) >= 3 && f[0] == f[1] {
			front = strings.Join(f[2:], " ")
		}
	}
	if filepath.Base(front) != "claude" {
		return fmt.Errorf("Crius's tab %s (%s) runs %q in front, not Claude: nothing typed", surface, tty, front)
	}
	return nil
}

// resources gives the free disk of the volume holding the home folder and the swap in use, both in GB.
func resources() (float64, float64, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return 0, 0, err
	}
	var fs syscall.Statfs_t
	if err := syscall.Statfs(home, &fs); err != nil {
		return 0, 0, fmt.Errorf("statfs %s: %v", home, err)
	}
	// launchd's PATH has no /usr/sbin.
	out, err := exec.Command("/usr/sbin/sysctl", "-n", "vm.swapusage").CombinedOutput()
	if err != nil {
		return 0, 0, fmt.Errorf("sysctl vm.swapusage: %v: %s", err, strings.TrimSpace(string(out)))
	}
	swap, err := swapUsed(string(out))
	if err != nil {
		return 0, 0, err
	}
	return float64(fs.Bavail) * float64(fs.Bsize) / (1 << 30), swap, nil
}

// swapUsed reads "used = 15997.06M" (or G) from sysctl vm.swapusage, in GB.
func swapUsed(s string) (float64, error) {
	m := swapRe.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("sysctl vm.swapusage: no used amount in %q", strings.TrimSpace(s))
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, err
	}
	if m[2] == "M" {
		n /= 1024
	}
	return n, nil
}

// toolChecks asks each tool the team needs for an answer that needs its login or daemon where it has one.
var toolChecks = map[string][]string{
	"cmux":   {"cmux", "ping"},
	"gh":     {"gh", "auth", "status", "--active"},
	"az":     {"az", "account", "show", "--query", "id", "-o", "tsv"},
	"bun":    {"bun", "--version"},
	"docker": {"docker", "version", "--format", "{{.Server.Version}}"},
}

var toolWait = 10 * time.Second

// tools runs the checks at once and gives each tool's failure, or nil.
func tools() map[string]error {
	res := map[string]error{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for name, c := range toolChecks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), toolWait)
			defer cancel()
			cmd := exec.CommandContext(ctx, c[0], c[1:]...)
			cmd.WaitDelay = time.Second // a killed tool's children may hold its output open
			out, err := cmd.CombinedOutput()
			if ctx.Err() != nil {
				err = fmt.Errorf("%s: no answer in %v", strings.Join(c, " "), toolWait)
			} else if err != nil {
				lines := strings.Split(strings.TrimSpace(string(out)), "\n")
				err = fmt.Errorf("%s: %v: %s", strings.Join(c, " "), err, strings.TrimSpace(lines[len(lines)-1]))
			}
			mu.Lock()
			res[name] = err
			mu.Unlock()
		}()
	}
	wg.Wait()
	return res
}

// toolPath adds the folders the team's tools live in to launchd's short PATH (/usr/bin:/bin and cmux's folder).
func toolPath() string {
	home, _ := os.UserHomeDir()
	return os.Getenv("PATH") + ":/opt/homebrew/bin:/usr/local/bin:" + filepath.Join(home, ".bun", "bin")
}

// sessions lists the live Claude Code sessions of every config folder (~/.claude*): name, id, and the birth time of
// the session's transcript (zero when it has none yet). A linked folder lists a process again; it counts once.
func sessions(home string) ([]wakewatch.Session, error) {
	files, err := filepath.Glob(filepath.Join(home, ".claude*", "sessions", "*.json"))
	if err != nil {
		return nil, err
	}
	var out []wakewatch.Session
	seen := map[int]bool{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if os.IsNotExist(err) { // the session ended after the listing
			continue
		}
		if err != nil {
			return nil, err
		}
		var s struct {
			PID       int    `json:"pid"`
			SessionID string `json:"sessionId"`
			Name      string `json:"name"`
		}
		if err := json.Unmarshal(b, &s); err != nil {
			return nil, fmt.Errorf("%s: %v", f, err)
		}
		// A file left by a session that ended; pid 0 would test our own process group.
		if s.PID <= 0 || seen[s.PID] || syscall.Kill(s.PID, 0) == syscall.ESRCH {
			continue
		}
		seen[s.PID] = true
		ses := wakewatch.Session{Name: s.Name, ID: s.SessionID}
		ts, err := filepath.Glob(filepath.Join(filepath.Dir(filepath.Dir(f)), "projects", "*", s.SessionID+".jsonl"))
		if err != nil {
			return nil, err
		}
		for _, t := range ts {
			var st syscall.Stat_t
			if err := syscall.Stat(t, &st); err != nil {
				return nil, err
			}
			ses.Started = time.Unix(st.Birthtimespec.Unix())
		}
		out = append(out, ses)
	}
	return out, nil
}
