// Command crius-loadwatch sets the heavy-slot count of the Appxite rig from the machine load; launchd runs it every
// minute. It follows the Mac load rule in Crius's AGENTS.md (owner 11-08; crius:lead DECISION 11-08):
//
//   - the 5-min load is over 28: 1 slot, at once;
//   - else the 15-min load is under 28: 2 slots;
//   - else the count stays (easing on the 5-min load flipped 4 times on 11-05).
//
// It writes the slots file (~/AppxiteRig/heavy/slots) only on a change, and then posts one INFO line on the Crius bus.
// A file that holds another whole number was set by hand: it is left alone and said in the log; an empty or broken
// one is a failure.
// A load, state or slots file it cannot read or write is posted once as an ALERT (and again after it recovered), and
// the run exits 2.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/store"
	"github.com/orzazade/crius/internal/watchdog"
)

const limit = 28.0

// state is what one run leaves the next: whether a failure was already posted.
type state struct {
	Failed bool `json:"failed,omitempty"`
}

type deps struct {
	home, slots, state string
	now                func() time.Time
	load               func() (load5, load15 float64, err error)
	out                io.Writer
}

func main() {
	home := store.Home()
	os.Exit(run(deps{home: home, slots: filepath.Join(os.Getenv("HOME"), "AppxiteRig", "heavy", "slots"),
		state: filepath.Join(home, "state", "loadwatch.json"), now: time.Now, load: sysLoad, out: os.Stdout}))
}

// sysLoad reads the 5- and 15-min load averages ("{ 12.62 12.36 16.85 }").
func sysLoad() (float64, float64, error) {
	b, err := exec.Command("/usr/sbin/sysctl", "-n", "vm.loadavg").Output()
	if err != nil {
		return 0, 0, fmt.Errorf("sysctl vm.loadavg: %w", err)
	}
	f := strings.Fields(strings.Trim(strings.TrimSpace(string(b)), "{}"))
	if len(f) != 3 {
		return 0, 0, fmt.Errorf("sysctl vm.loadavg: %q is not three numbers", b)
	}
	l5, err5 := strconv.ParseFloat(f[1], 64)
	l15, err15 := strconv.ParseFloat(f[2], 64)
	if err := errors.Join(err5, err15); err != nil {
		return 0, 0, fmt.Errorf("sysctl vm.loadavg: %w", err)
	}
	return l5, l15, nil
}

// want gives the slot count the load asks for, cur when it asks for no change.
func want(l5, l15 float64, cur string) string {
	switch {
	case l5 > limit:
		return "1"
	case l15 < limit:
		return "2"
	}
	return cur
}

func run(d deps) int {
	now := d.now()
	var st state
	if b, err := os.ReadFile(d.state); err == nil {
		if err := json.Unmarshal(b, &st); err != nil {
			return failed(d, &st, now, fmt.Errorf("state %s: %w", d.state, err))
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return failed(d, &st, now, err)
	}
	l5, l15, err := d.load()
	if err != nil {
		return failed(d, &st, now, err)
	}
	b, err := os.ReadFile(d.slots)
	if err != nil {
		return failed(d, &st, now, err)
	}
	cur := strings.TrimSpace(string(b))
	loads := fmt.Sprintf("load5 %.2f, load15 %.2f", l5, l15)
	if n, err := strconv.Atoi(cur); err != nil || n < 0 { // empty or broken: failing to look, never a hand value (review P3-1)
		return failed(d, &st, now, fmt.Errorf("slots file %s holds %q, not a slot count", d.slots, cur))
	}
	if cur != "1" && cur != "2" {
		fmt.Fprintf(d.out, "%s slots file %s holds %q, not 1 or 2: left alone (%s)\n", now.Format(time.RFC3339), d.slots, cur, loads)
		return save(d, &st, now)
	}
	w := want(l5, l15, cur)
	if w != cur {
		if err := writeAtomic(d.slots, w+"\n"); err != nil {
			return failed(d, &st, now, err)
		}
		line := fmt.Sprintf("heavy slots %s -> %s (%s)", cur, w, loads)
		fmt.Fprintf(d.out, "%s %s\n", now.Format(time.RFC3339), line)
		if err := watchdog.PostOn(d.home, "crius", "INFO", "crius:loadwatch", "all", now, line); err != nil {
			return failed(d, &st, now, fmt.Errorf("slots set, but the bus line failed: %w", err))
		}
	}
	return save(d, &st, now)
}

// save writes the state and clears a posted failure.
func save(d deps, st *state, now time.Time) int {
	st.Failed = false
	b, _ := json.Marshal(st)
	if err := writeAtomic(d.state, string(b)+"\n"); err != nil {
		return failed(d, st, now, err)
	}
	return 0
}

// failed says the failure in the log every run and on the bus once per spell, and exits 2.
func failed(d deps, st *state, now time.Time, err error) int {
	fmt.Fprintf(d.out, "%s LOUD crius-loadwatch failed to look or set: %v\n", now.Format(time.RFC3339), err)
	if !st.Failed {
		if perr := watchdog.PostOn(d.home, "crius", "ALERT", "crius:loadwatch", "crius:lead", now,
			"the heavy-slot load watcher failed; slots are not following the load: "+err.Error()); perr != nil {
			fmt.Fprintf(d.out, "%s LOUD the bus line failed too: %v\n", now.Format(time.RFC3339), perr)
			return 2
		}
		st.Failed = true
		if b, merr := json.Marshal(st); merr == nil {
			_ = writeAtomic(d.state, string(b)+"\n") // best effort: an unwritable state posts again next run
		}
	}
	return 2
}

// writeAtomic replaces path with text through a temp file in its folder, so a reader never sees half a file.
func writeAtomic(path, text string) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	_, err = f.WriteString(text)
	if err == nil {
		err = f.Sync() // after a crash the file holds the new count, not nothing (review P3-2)
	}
	if err != nil {
		f.Close()
		os.Remove(f.Name())
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return err
	}
	if err := os.Chmod(f.Name(), 0o644); err != nil {
		os.Remove(f.Name())
		return err
	}
	return os.Rename(f.Name(), path)
}
