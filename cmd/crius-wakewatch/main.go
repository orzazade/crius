// Command crius-wakewatch runs one missed-wake check (internal/wakewatch) and exits 1 when it could not finish or not
// reach Crius's tab. launchd starts it every 5 min.
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
	"sync"
	"syscall"
	"time"

	"github.com/orzazade/crius/internal/outbox"
	"github.com/orzazade/crius/internal/sessionstate"
	"github.com/orzazade/crius/internal/store"
	"github.com/orzazade/crius/internal/wakewatch"
	"github.com/orzazade/crius/internal/watchdog"
)

var swapRe = regexp.MustCompile(`used = ([\d.]+)([MG])`)

func main() {
	os.Setenv("PATH", toolPath())
	// Messages held on an earlier run go first, so a new alert never jumps ahead of an older one.
	if err := flushOutbox(store.Home(), time.Now()); err != nil {
		fmt.Fprintln(os.Stderr, time.Now().Format(time.RFC3339), "crius-wakewatch: outbox:", err)
	}
	err := wakewatch.Run(wakewatch.Deps{Home: store.Home(), Now: time.Now(), Screens: screens, Send: send,
		Workspaces: workspaces, Resources: resources, Tools: tools, Sessions: func() ([]wakewatch.Session, error) {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, err
			}
			return sessions(home)
		}, States: func() ([]sessionstate.State, error) {
			return sessionstate.List(filepath.Join(store.Home(), "sessions"))
		}})
	if err != nil {
		fmt.Fprintln(os.Stderr, time.Now().Format(time.RFC3339), "crius-wakewatch:", err)
		os.Exit(1)
	}
}

// flushOutbox types what earlier runs held. Each message or file the outbox set aside goes to Crius's bus, as nobody
// else hears of it (a crius-say sender has gone); what it still holds is only logged, as every run would post it again.
func flushOutbox(home string, now time.Time) error {
	aside, held := outbox.Flush(home)
	for _, e := range aside {
		if err := watchdog.Post(home, now, "outbox set aside a message: "+e.Error()); err != nil {
			return errors.Join(held, err)
		}
	}
	return held
}

// workspaces lists every workspace of every cmux window with its name and colour, which the JSON tree does not show.
func workspaces() ([]wakewatch.Workspace, error) {
	b, err := outbox.Cmux("--json", "tree", "--all")
	if err != nil {
		return nil, fmt.Errorf("cmux tree: %v: %s", err, strings.TrimSpace(string(b)))
	}
	var tree struct{ Windows []struct{ Ref string } }
	if err := json.Unmarshal(b, &tree); err != nil {
		return nil, fmt.Errorf("cmux --json tree: %v", err)
	}
	var out []wakewatch.Workspace
	for _, w := range tree.Windows {
		b, err := outbox.Cmux("workspace", "list", "--window", w.Ref, "--json")
		if err != nil {
			return nil, fmt.Errorf("cmux workspace list --window %s: %v: %s", w.Ref, err, strings.TrimSpace(string(b)))
		}
		var list struct {
			Workspaces []struct {
				Ref, Title     string
				HasCustomTitle bool   `json:"has_custom_title"`
				CustomColor    string `json:"custom_color"`
			}
		}
		if err := json.Unmarshal(b, &list); err != nil {
			return nil, fmt.Errorf("cmux workspace list --window %s --json: %v", w.Ref, err)
		}
		for _, ws := range list.Workspaces {
			out = append(out, wakewatch.Workspace{Ref: ws.Ref, Title: ws.Title, Named: ws.HasCustomTitle, Color: ws.CustomColor})
		}
	}
	return out, nil
}

// screens reads the last 30 lines of every terminal tab in cmux. A tab whose read fails comes back with Err set.
func screens() ([]wakewatch.Screen, error) {
	tabs, err := outbox.Terminals()
	if err != nil {
		return nil, err
	}
	var out []wakewatch.Screen
	for _, tab := range tabs {
		text, err := outbox.Cmux("read-screen", "--surface", tab.Surface, "--lines", "30")
		if err != nil {
			out = append(out, wakewatch.Screen{Surface: tab.Surface, ID: tab.ID, Title: tab.Title, Err: fmt.Sprintf("cmux read-screen: %v: %s", err, strings.TrimSpace(string(text)))})
			continue
		}
		out = append(out, wakewatch.Screen{Surface: tab.Surface, ID: tab.ID, Title: tab.Title, Text: string(text)})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("cmux tree lists no terminal tab")
	}
	return out, nil
}

// send stores text for Crius's own tab and types it there (internal/outbox). The tab is the one terminal in cmux
// titled "crius" (a file naming it went stale when Crius handed over to a new tab). Text held by a draft in Crius's
// box stays stored, and the next run types it first.
func send(text string) error { return outbox.Send(store.Home(), wakewatch.CriusTitle, text) }

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
