// crius-tidy: the daily disk cleanup (see internal/tidy). launchd runs it at 04:30. Each run writes one line to
// logs/tidy.log with how many copies it removed and kept, what they held and the free space the disk gained; a failure
// also goes to Crius's bus as an ALERT line.
package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/orzazade/crius/internal/tidy"
	"github.com/orzazade/crius/internal/watchdog"
)

// chromeClones is where Chrome leaves its copies: the per-user X folder macOS keeps beside the temp folder.
const chromeClones = "/private/var/folders/*/*/X/com.google.Chrome.code_sign_clone"

func main() {
	home := os.Getenv("CRIUS_HOME")
	if home == "" {
		u, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintln(os.Stderr, "crius-tidy:", err)
			os.Exit(1)
		}
		home = filepath.Join(u, ".crius")
	}
	ps := exec.Command("ps", "-axww", "-o", "lstart=,args=")
	ps.Env = append(os.Environ(), "LC_ALL=C")
	os.Exit(run(home, chromeClones, time.Now(), ps.Output, os.Stdout))
}

// run cleans once and says what it did on out. ps lists the processes, for the copies a running Chrome still uses.
func run(home, pattern string, now time.Time, ps func() ([]byte, error), out io.Writer) int {
	stamp := now.Format(time.RFC3339)
	r, err := clean(pattern, now, ps)
	// The copies' blocks may be shared with the app, so what they held is not what the disk got back.
	fmt.Fprintf(out, "%s chrome clones: removed %d, kept %d, they held %.1f GB, disk free %+.1f GB\n", stamp,
		len(r.Removed), r.Kept, float64(r.Bytes)/1e9, float64(r.Freed)/1e9)
	if err == nil {
		return 0
	}
	fmt.Fprintf(out, "%s crius-tidy: %v\n", stamp, err)
	if perr := watchdog.Post(home, now, "crius-tidy: the Chrome clone cleanup failed: "+err.Error()); perr != nil {
		fmt.Fprintf(out, "%s crius-tidy: the ALERT line was not posted: %v\n", stamp, perr)
	}
	return 1
}

type result struct {
	tidy.Result
	Freed int64 // free space after minus before, on the disk that holds the folders
}

func clean(pattern string, now time.Time, ps func() ([]byte, error)) (result, error) {
	var r result
	b, err := ps()
	if err != nil {
		return r, fmt.Errorf("ps: %w", err)
	}
	running, err := tidy.ChromeStarts(b)
	if err != nil {
		return r, err
	}
	before, err := free(pattern)
	if err != nil {
		return r, err
	}
	r.Result, err = tidy.ChromeClones(pattern, now, running)
	after, ferr := free(pattern)
	if ferr != nil && err == nil {
		err = ferr
	}
	r.Freed = after - before
	return r, err
}

// free gives the bytes free on the disk that holds the folders pattern names: its part before any * or ?, or the
// nearest folder above that which exists.
func free(pattern string) (int64, error) {
	dir := pattern
	if i := strings.IndexAny(dir, "*?["); i >= 0 {
		dir = dir[:i]
	}
	dir = filepath.Clean(dir)
	for _, err := os.Stat(dir); err != nil && dir != filepath.Dir(dir); _, err = os.Stat(dir) {
		dir = filepath.Dir(dir)
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, fmt.Errorf("free space of %s: %w", dir, err)
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}
