// crius-watchdog: the local check on the ingest heartbeat, and on the zone launchd's calendar jobs fire in (see
// internal/watchdog). launchd runs it every 5 min. It never fails quietly: every error goes to stderr
// (logs/watchdog.log) and, once per distinct error, to an ALERT line on Crius's bus.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/orzazade/crius/internal/redact"
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
	os.Exit(run(home, time.Now(), wake, err, calendarZone(), os.Stderr))
}

func calendarZone() error {
	fi, err := os.Lstat("/etc/localtime")
	if err != nil {
		return err
	}
	ps := exec.Command("ps", "-axww", "-o", "uid=,lstart=,command=")
	ps.Env = append(os.Environ(), "LC_ALL=C")
	out, err := ps.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return fmt.Errorf("ps: %w: %s", err, strings.TrimSpace(string(exit.Stderr)))
		}
		return fmt.Errorf("ps: %w", err)
	}
	return watchdog.CalendarZone(out, os.Getuid(), fi.ModTime())
}

// run checks once. wakeErr is a failure to read the wake time; the check then counts from the stamp only. zoneErr is
// the calendar zone check's result.
func run(home string, now, wake time.Time, wakeErr, zoneErr error, stderr io.Writer) int {
	stderr = redact.Writer{W: stderr}
	err := watchdog.Check(home, now, wake, maxAge)
	if err == nil && wakeErr != nil {
		err = fmt.Errorf("cannot read the wake time, checked from the stamp only: %v", wakeErr)
	}
	rc := alertOnce(home, "watchdog.error", "watchdog cannot check the ingest: ", now, err, stderr)
	if alertOnce(home, "watchdog.zone.error", "launchd calendar jobs: ", now, zoneErr, stderr) != 0 {
		rc = 1
	}
	return rc
}

// alertOnce says err on stderr and, unless the same error was posted last time (kept in home/name), on the bus with
// prefix; no error clears home/name. It gives the exit code.
func alertOnce(home, name, prefix string, now time.Time, err error, stderr io.Writer) int {
	last := filepath.Join(home, name)
	if err == nil {
		if err = os.Remove(last); err == nil || os.IsNotExist(err) {
			return 0
		}
	}
	msg, _ := redact.Redact(err.Error()) // also kept in home/name
	fmt.Fprintf(stderr, "%s crius-watchdog: %s\n", now.Format(time.RFC3339), msg)
	if b, rerr := os.ReadFile(last); rerr != nil || strings.TrimSpace(string(b)) != msg {
		if perr := watchdog.Post(home, now, prefix+msg); perr != nil {
			fmt.Fprintln(stderr, "crius-watchdog: cannot post the alert:", perr)
		} else if werr := os.WriteFile(last, []byte(msg+"\n"), 0o600); werr != nil {
			fmt.Fprintln(stderr, "crius-watchdog:", werr)
		}
	}
	return 1
}
