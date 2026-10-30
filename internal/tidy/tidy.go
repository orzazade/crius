// Package tidy removes what other programs leave behind on the disk. Chrome copies itself into a code_sign_clone
// folder on every start and leaves the copy (Crius 10-31 00:12: 92 copies since 10-27, up to ~60 GB).
package tidy

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Result is what one cleanup did: the folders it removed, how many copies it kept, and the bytes it freed.
type Result struct {
	Removed []string
	Kept    int
	Bytes   int64
}

const (
	keepNewest = 3
	keepAge    = 24 * time.Hour
)

// ChromeClones removes each code_sign_clone.* folder in the folders pattern names that is older than a day, never the
// newest three of a folder, and never the copy of a Chrome still running. Which side of its start Chrome makes the
// copy is not known, so for each start time in running both stay: the newest copy made by then and the oldest made
// since, each with a minute's slack (Crius 00:25). lsof cannot show that use: the copy's main file is a hard link to
// the app's own (review of S). A folder it cannot read or a copy it cannot remove is an error naming it; the others go on. No folder
// at all is an error too: Chrome moved its copies, and a cleanup that finds nothing every day would say nothing.
func ChromeClones(pattern string, now time.Time, running []time.Time) (Result, error) {
	var r Result
	roots, err := filepath.Glob(pattern)
	if err != nil {
		return r, err
	}
	if len(roots) == 0 {
		return r, fmt.Errorf("no folder matches %s", pattern)
	}
	var errs []error
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		type found struct {
			path string
			at   time.Time
		}
		var copies []found
		for _, e := range entries {
			if !e.IsDir() || !strings.HasPrefix(e.Name(), "code_sign_clone.") { // a link is no copy
				continue
			}
			fi, err := e.Info()
			if errors.Is(err, fs.ErrNotExist) { // Chrome removed it on quit
				continue
			}
			if err != nil {
				errs = append(errs, err)
				continue
			}
			copies = append(copies, found{filepath.Join(root, e.Name()), fi.ModTime()})
		}
		sort.Slice(copies, func(i, j int) bool { return copies[i].at.After(copies[j].at) })
		inUse := map[int]bool{}
		for _, start := range running {
			for i := range copies { // newest first
				if !copies[i].at.After(start.Add(time.Minute)) {
					inUse[i] = true
					break
				}
			}
			for i := len(copies) - 1; i >= 0; i-- { // oldest first
				if !copies[i].at.Before(start.Add(-time.Minute)) {
					inUse[i] = true
					break
				}
			}
		}
		for i, c := range copies {
			if i < keepNewest || now.Sub(c.at) <= keepAge || inUse[i] {
				r.Kept++
				continue
			}
			n, err := size(c.path)
			if err == nil {
				err = os.RemoveAll(c.path)
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", c.path, err))
				r.Kept++
				continue
			}
			r.Removed = append(r.Removed, c.path)
			r.Bytes += n
		}
	}
	return r, errors.Join(errs...)
}

// size gives the bytes of the files in a folder. Their blocks may be shared with the app (hard links, clones), so this
// is what the copies hold, not what removing them frees: the command measures that on the disk.
func size(dir string) (int64, error) {
	var n int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			fi, err := d.Info()
			if err != nil {
				return err
			}
			n += fi.Size()
		}
		return nil
	})
	return n, err
}

// ChromeStarts reads `ps -axww -o lstart=,args=` (LC_ALL=C) and gives the start time of each main Chrome process
// (its program is .../Contents/MacOS/Google Chrome; helpers are not). A line it cannot read is an error: a cleanup that
// cannot tell which copies are in use removes none.
func ChromeStarts(ps []byte) ([]time.Time, error) {
	var starts []time.Time
	for _, line := range strings.Split(string(ps), "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if len(f) < 6 {
			return nil, fmt.Errorf("ps line %q", line)
		}
		args := strings.Join(f[5:], " ")
		if !strings.HasSuffix(args, "/Contents/MacOS/Google Chrome") && !strings.Contains(args, "/Contents/MacOS/Google Chrome --") {
			continue
		}
		at, err := time.ParseInLocation("Mon Jan 2 15:04:05 2006", strings.Join(f[:5], " "), time.Local)
		if err != nil {
			return nil, fmt.Errorf("ps line %q: %w", line, err)
		}
		starts = append(starts, at)
	}
	return starts, nil
}
