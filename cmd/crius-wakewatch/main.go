// Command crius-wakewatch runs one missed-wake check (internal/wakewatch) and exits 1 when it could not finish or not
// reach Crius's tab. launchd starts it every 5 min.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/store"
	"github.com/orzazade/crius/internal/wakewatch"
)

var (
	surfaceRe = regexp.MustCompile(`surface (surface:\d+) \[terminal\] "(.*)"`)
	tabRe     = regexp.MustCompile(`^(workspace:\d+)\s+(surface:\d+)$`)
	cmux      = func(args ...string) ([]byte, error) { return exec.Command("cmux", args...).CombinedOutput() }
)

func main() {
	err := wakewatch.Run(wakewatch.Deps{Home: store.Home(), Now: time.Now(), Screens: screens, Send: send})
	if err != nil {
		fmt.Fprintln(os.Stderr, time.Now().Format(time.RFC3339), "crius-wakewatch:", err)
		os.Exit(1)
	}
}

// screens reads the last 30 lines of every terminal tab in cmux. A tab whose read fails comes back with Err set.
func screens() ([]wakewatch.Screen, error) {
	tree, err := cmux("tree", "--all")
	if err != nil {
		return nil, fmt.Errorf("cmux tree: %v: %s", err, strings.TrimSpace(string(tree)))
	}
	var out []wakewatch.Screen
	for _, m := range surfaceRe.FindAllStringSubmatch(string(tree), -1) {
		text, err := cmux("read-screen", "--surface", m[1], "--lines", "30")
		if err != nil {
			out = append(out, wakewatch.Screen{Surface: m[1], Title: m[2], Err: fmt.Sprintf("cmux read-screen: %v: %s", err, strings.TrimSpace(string(text)))})
			continue
		}
		out = append(out, wakewatch.Screen{Surface: m[1], Title: m[2], Text: string(text)})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("cmux tree lists no terminal tab")
	}
	return out, nil
}

// send types text into Crius's own tab, then Enter. The tab is in ~/.crius/crius-tab: "workspace:N surface:N" on one
// line (# starts a comment). Without both, nothing is typed: a cmux send with no target goes to the caller's tab.
func send(text string) error {
	path := filepath.Join(store.Home(), "crius-tab")
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("Crius's tab is not known: %v (crius-tab holds \"workspace:N surface:N\")", err)
	}
	var ref []string
	for _, line := range strings.Split(string(b), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			ref = append(ref, line)
		}
	}
	m := tabRe.FindStringSubmatch(strings.Join(ref, "\n"))
	if m == nil {
		return fmt.Errorf("%s: want one line \"workspace:N surface:N\" (crius-tab), got %q", path, strings.Join(ref, " | "))
	}
	if out, err := cmux("send", "--workspace", m[1], "--surface", m[2], "--", text); err != nil {
		return fmt.Errorf("cmux send: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := cmux("send-key", "--workspace", m[1], "--surface", m[2], "enter"); err != nil {
		return fmt.Errorf("cmux send-key: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
