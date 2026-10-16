// crius-watchdog: the local check on the ingest heartbeat (see internal/watchdog). launchd runs it every 5 min.
// It never fails quietly: every error goes to stderr (logs/watchdog.log) and, once per distinct error, to an
// ALERT line on Crius's bus.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/orzazade/crius/internal/watchdog"
)

const maxAge = 20 * time.Minute // two missed 10-minute ingest runs

func main() {
	home := os.Getenv("CRIUS_HOME")
	if home == "" {
		u, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintln(os.Stderr, "crius-watchdog:", err)
			os.Exit(1)
		}
		home = filepath.Join(u, ".crius")
	}
	now := time.Now()
	var wake time.Time
	if tv, err := unix.SysctlTimeval("kern.waketime"); err != nil {
		fmt.Fprintln(os.Stderr, "crius-watchdog: cannot read the wake time, counting from the stamp only:", err)
	} else {
		wake = time.Unix(tv.Unix())
	}
	last := filepath.Join(home, "watchdog.error")
	err := watchdog.Check(home, now, wake, maxAge)
	if err == nil {
		if err := os.Remove(last); err != nil && !os.IsNotExist(err) {
			fmt.Fprintln(os.Stderr, "crius-watchdog:", err)
			os.Exit(1)
		}
		return
	}
	msg := err.Error()
	fmt.Fprintf(os.Stderr, "%s crius-watchdog: %s\n", now.Format(time.RFC3339), msg)
	if b, rerr := os.ReadFile(last); rerr != nil || strings.TrimSpace(string(b)) != msg {
		if perr := watchdog.Post(home, now, "watchdog cannot check the ingest: "+msg); perr != nil {
			fmt.Fprintln(os.Stderr, "crius-watchdog: cannot post the alert:", perr)
		} else if werr := os.WriteFile(last, []byte(msg+"\n"), 0o600); werr != nil {
			fmt.Fprintln(os.Stderr, "crius-watchdog:", werr)
		}
	}
	os.Exit(1)
}
