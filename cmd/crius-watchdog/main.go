// crius-watchdog: the local check on the ingest heartbeat (see internal/watchdog). launchd runs it every 5 min.
// It never fails quietly: every error goes to stderr (logs/watchdog.log) and, once per distinct error, to an
// ALERT line on Crius's bus.
package main

import (
	"fmt"
	"io"
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
	var wake time.Time
	tv, err := unix.SysctlTimeval("kern.waketime")
	if err == nil {
		wake = time.Unix(tv.Unix())
	}
	os.Exit(run(home, time.Now(), wake, err, os.Stderr))
}

// run checks once. wakeErr is a failure to read the wake time; the check then counts from the stamp only.
func run(home string, now, wake time.Time, wakeErr error, stderr io.Writer) int {
	last := filepath.Join(home, "watchdog.error")
	err := watchdog.Check(home, now, wake, maxAge)
	if err == nil && wakeErr != nil {
		err = fmt.Errorf("cannot read the wake time, checked from the stamp only: %v", wakeErr)
	}
	if err == nil {
		if err = os.Remove(last); err == nil || os.IsNotExist(err) {
			return 0
		}
	}
	msg := err.Error()
	fmt.Fprintf(stderr, "%s crius-watchdog: %s\n", now.Format(time.RFC3339), msg)
	if b, rerr := os.ReadFile(last); rerr != nil || strings.TrimSpace(string(b)) != msg {
		if perr := watchdog.Post(home, now, "watchdog cannot check the ingest: "+msg); perr != nil {
			fmt.Fprintln(stderr, "crius-watchdog: cannot post the alert:", perr)
		} else if werr := os.WriteFile(last, []byte(msg+"\n"), 0o600); werr != nil {
			fmt.Fprintln(stderr, "crius-watchdog:", werr)
		}
	}
	return 1
}
