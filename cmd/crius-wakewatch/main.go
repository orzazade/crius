// Command crius-wakewatch runs one missed-wake check (internal/wakewatch) and exits 1 when it could not finish or not
// reach Crius's tab. launchd starts it every 5 min. "crius-wakewatch after-boot" checks the helpers in
// ~/.crius/helpers.toml instead and names the dead ones to Crius (wakewatch.AfterBoot); launchd starts it once per login.
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
	"github.com/orzazade/crius/internal/redact"
	"github.com/orzazade/crius/internal/sessionstate"
	"github.com/orzazade/crius/internal/store"
	"github.com/orzazade/crius/internal/wakewatch"
	"github.com/orzazade/crius/internal/watchdog"
	"golang.org/x/sys/unix"
)

var swapRe = regexp.MustCompile(`used = ([\d.]+)([MG])`)

func main() {
	os.Setenv("PATH", toolPath())
	if len(os.Args) == 2 && os.Args[1] == "after-boot" {
		err := wakewatch.AfterBoot(wakewatch.BootDeps{Home: store.Home(), Now: time.Now(), Process: process,
			Launchd: launchd, Send: send})
		if err != nil {
			fmt.Fprintln(os.Stderr, time.Now().Format(time.RFC3339), "crius-wakewatch after-boot:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 {
		fmt.Fprintln(os.Stderr, "usage: crius-wakewatch [after-boot]")
		os.Exit(2)
	}
	// Messages held on an earlier run go first, so a new alert never jumps ahead of an older one.
	if err := flushOutbox(store.Home(), time.Now()); err != nil {
		fmt.Fprintln(os.Stderr, time.Now().Format(time.RFC3339), "crius-wakewatch: outbox:", err)
	}
	err := wakewatch.Run(wakewatch.Deps{Home: store.Home(), Now: time.Now(), Screens: screens, Send: send, HandedOver: handedOver,
		Workspaces: workspaces, Resources: resources, Load: loadAvg, Tools: tools, Sessions: func() ([]wakewatch.Session, error) {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, err
			}
			return wakewatch.LiveSessions(home)
		}, States: func() ([]sessionstate.State, error) {
			return sessionstate.List(filepath.Join(store.Home(), "sessions"))
		}, Restore: restore})
	if err != nil {
		fmt.Fprintln(os.Stderr, time.Now().Format(time.RFC3339), "crius-wakewatch:", err)
		os.Exit(1)
	}
}

// flushOutbox types what earlier runs held. Each message or file the outbox set aside goes to Crius's bus, as nobody
// else hears of it (a crius-say sender has gone); what it still holds is only logged, as every run would post it again.
// One the bus does not take is named in the error, so the log still has it: it has already left the outbox. The error is
// redacted as the bus line is: it can quote the text a message was typed onto.
func flushOutbox(home string, now time.Time) error {
	aside, held := outbox.Flush(home)
	errs := []error{held}
	for _, e := range aside {
		if err := watchdog.Post(home, now, "outbox set aside a message: "+e.Error()); err != nil {
			errs = append(errs, fmt.Errorf("not posted to the bus (%v): outbox set aside a message: %w", err, e))
		}
	}
	if err := errors.Join(errs...); err != nil {
		text, _ := redact.Redact(err.Error())
		return errors.New(text)
	}
	return nil
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

// restore is the config folder cmux would restore a tab's Claude in after a restart (restoreFolder).
func restore(workspace, surface string) (string, bool, error) {
	args := []string{"surface", "resume", "show", "--json", "--surface", surface}
	if workspace != "" {
		args = append(args, "--workspace", workspace)
	}
	b, err := outbox.Cmux(args...)
	if err != nil {
		return "", false, fmt.Errorf("cmux surface resume show --surface %s: %v: %s", surface, err, strings.TrimSpace(string(b)))
	}
	return restoreFolder(b)
}

// restoreFolder reads cmux's resume record of a tab: the CLAUDE_CONFIG_DIR its Claude binding restores with, "" when it
// sets none; found is false when the tab keeps no Claude binding.
func restoreFolder(b []byte) (config string, found bool, err error) {
	var r struct {
		Binding *struct {
			Kind        string
			Environment map[string]string
		} `json:"resume_binding"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return "", false, fmt.Errorf("cmux surface resume show --json: %v", err)
	}
	if r.Binding == nil || r.Binding.Kind != "claude" {
		return "", false, nil
	}
	return r.Binding.Environment["CLAUDE_CONFIG_DIR"], true, nil
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

// process gives the start time of pid, or false when no process has it.
func process(pid int) (time.Time, bool, error) {
	if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		return time.Time{}, false, nil
	}
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) { // it ended while we looked
			return time.Time{}, false, nil
		}
		return time.Time{}, false, fmt.Errorf("pid %d: %w", pid, err)
	}
	return time.Unix(k.Proc.P_starttime.Sec, int64(k.Proc.P_starttime.Usec)*1000), true, nil
}

// launchd tells whether a launchd job is loaded in the user's session; launchctl print exits 113 for one that is not.
func launchd(label string) (bool, error) {
	out, err := exec.Command("launchctl", "print", fmt.Sprintf("gui/%d/%s", os.Getuid(), label)).CombinedOutput()
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 113 {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("launchctl print %s: %v %s", label, err, strings.TrimSpace(string(out)))
	}
	return true, nil
}

// send stores text for Crius's own tab and types it there (internal/outbox). The tab is the one terminal in cmux
// titled "crius" (a file naming it went stale when Crius handed over to a new tab). Text held by a draft in Crius's
// box stays stored, and the next run types it first.
func send(text string) error { return outbox.Send(store.Home(), wakewatch.CriusTitle, text) }

// handedOver: outbox.Send kept the text (typed by a later run) or set it aside after it may have gone.
func handedOver(err error) bool {
	return errors.Is(err, outbox.ErrHeld) || errors.Is(err, outbox.ErrOnto) || errors.Is(err, outbox.ErrMaybeSent)
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

// loadAvg reads the 1-minute load from sysctl vm.loadavg ("{ 6.02 5.81 5.40 }").
func loadAvg() (float64, error) {
	out, err := exec.Command("/usr/sbin/sysctl", "-n", "vm.loadavg").CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("sysctl vm.loadavg: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return parseLoad(string(out))
}

func parseLoad(s string) (float64, error) {
	f := strings.Fields(strings.Trim(strings.TrimSpace(s), "{}"))
	if len(f) == 0 {
		return 0, fmt.Errorf("sysctl vm.loadavg: no load in %q", strings.TrimSpace(s))
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 0, fmt.Errorf("sysctl vm.loadavg: %v", err)
	}
	return v, nil
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

// toolRetry: a tool that missed is asked again this much later (Crius 10-23 22:36: az and gh missed the 10 s once under
// load 100-180 on 14 cores and answered in a second on the recheck).
var toolRetry = 30 * time.Second

// tools runs the checks at once and gives each tool's failure, or nil. A tool fails only when it misses twice, toolRetry
// apart; its failure names both misses and the 1-minute load.
func tools() map[string]error {
	res := ask(toolChecks)
	again := map[string][]string{}
	for name, err := range res {
		if err != nil {
			again[name] = toolChecks[name]
		}
	}
	if len(again) == 0 {
		return res
	}
	time.Sleep(toolRetry)
	for name, err := range ask(again) {
		if err != nil {
			err = fmt.Errorf("%v; again %v later: %v (1-min load %s)", res[name], toolRetry, err, load())
		}
		res[name] = err
	}
	return res
}

// load is the 1-minute load average, or why it is not known.
func load() string {
	out, err := exec.Command("/usr/sbin/sysctl", "-n", "vm.loadavg").Output() // "{ 1.62 1.80 1.94 }"
	if f := strings.Fields(string(out)); err == nil && len(f) > 1 {
		return f[1]
	}
	return fmt.Sprintf("unknown (%v)", err)
}

// ask runs the checks at once and gives each tool's failure, or nil.
func ask(checks map[string][]string) map[string]error {
	res := map[string]error{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for name, c := range checks {
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
