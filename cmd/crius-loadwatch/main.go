// Command crius-loadwatch sets the heavy-slot count of the Appxite rig from the machine load; launchd runs it every
// minute. It follows the Mac load rule in Crius's AGENTS.md (owner 11-08; crius:lead DECISION 11-08):
//
//   - the 5-min load is over 28: 1 slot, at once;
//   - else memory is at least 40% free (memory_pressure) and the 15-min load is under 12: 4 slots, under 18: 3 slots
//     (crius:lead DECISIONs 11-08: 10 heavy jobs waited on 2 slots at load 13, then 15 on 3 slots at load 14.5);
//   - else the 15-min load is under 28: 2 slots;
//   - else the count stays (easing on the 5-min load flipped 4 times on 11-05).
//
// Those are the up-steps. The extra slots come down only past a dead band: 4 to 3 at a 15-min load of 16, 3 to 2 at 22
// (4 goes straight to 2 at 22), or at once to 2 when memory is under 40% free.
//
// The 5-min load over 28 while 3 or 4 slots are set turns the extra slots off (the most it sets is then 2) and posts the
// numbers to crius:lead. They come back, with one more line to crius:lead, once the 15-min load has been under 18 for 30
// minutes after that; a minute at 18 or more starts the 30 minutes again (crius:lead DECISION 11-08).
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

const (
	limit = 28.0
	quiet = 18.0 // the 15-min load under this, with memory free at least minFree, allows a third slot
	idle  = 12.0 // and under this, a fourth
	// the dead band: the extra slots come down only at these 15-min loads (crius:lead DECISION 11-08)
	downFour  = 16.0
	downThree = 22.0
	minFree   = 40 // percent
)

// state is what one run leaves the next: whether a failure was already posted, whether the extra slots are off, and
// since when the 15-min load has been under 18 while they are.
type state struct {
	Failed     bool   `json:"failed,omitempty"`
	NoExtra    bool   `json:"noExtra,omitempty"`
	QuietSince string `json:"quietSince,omitempty"` // RFC3339
	QuietLast  string `json:"quietLast,omitempty"`  // the last quiet reading, RFC3339
}

// backAfter is how long the 15-min load must stay under quiet before tripped extra slots come back.
const backAfter = 30 * time.Minute

// maxGap is the longest time between two quiet readings that keeps the quiet clock running (the job runs every minute).
const maxGap = 3 * time.Minute

type deps struct {
	home, slots, state string
	now                func() time.Time
	load               func() (load5, load15 float64, err error)
	mem                func() (freePct int, err error)
	out                io.Writer
}

func main() {
	home := store.Home()
	os.Exit(run(deps{home: home, slots: filepath.Join(os.Getenv("HOME"), "AppxiteRig", "heavy", "slots"),
		state: filepath.Join(home, "state", "loadwatch.json"), now: time.Now, load: sysLoad, mem: memFree, out: os.Stdout}))
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

// memFree reads the free-memory percentage ("System-wide memory free percentage: 83%").
func memFree() (int, error) {
	b, err := exec.Command("/usr/bin/memory_pressure").Output()
	if err != nil {
		return 0, fmt.Errorf("memory_pressure: %w", err)
	}
	const key = "System-wide memory free percentage:"
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), key); ok {
			n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(v), "%"))
			if err != nil {
				return 0, fmt.Errorf("memory_pressure: %q: %w", l, err)
			}
			return n, nil
		}
	}
	return 0, fmt.Errorf("memory_pressure: no %q line", key)
}

// want gives the slot count the load asks for (cur is "1" to "4"). Up-steps go straight to the level; the extra slots
// come down only past a dead band: 4 to 3 at load15 16, 3 to 2 at 22 (the up-steps alone flipped 2 -> 3 -> 2 -> 3 in
// 10 min at load15 ~18 on 11-08). 2 to 1 is the over-28 rule only.
func want(l5, l15 float64, free int, cur string, noExtra bool) string {
	if l5 > limit {
		return "1"
	}
	extra := free >= minFree && !noExtra
	up := 0
	switch {
	case l15 < idle && extra:
		up = 4
	case l15 < quiet && extra:
		up = 3
	case l15 < limit:
		up = 2
	}
	n, _ := strconv.Atoi(cur)
	if up > n {
		return strconv.Itoa(up)
	}
	if n == 4 && (!extra || l15 >= downFour) {
		n = 3
	}
	if n == 3 && (!extra || l15 >= downThree) {
		n = 2
	}
	return strconv.Itoa(n)
}

func run(d deps) int {
	now := d.now()
	var st state
	var late error // failures that must not stop the load rules: the run applies them, then ends loud
	// every failure carries the ones before it, so none is dropped (review r2 P2-2)
	fail := func(err error) int { return failed(d, &st, now, errors.Join(late, err)) }
	b, err := os.ReadFile(d.state)
	if err == nil {
		err = json.Unmarshal(b, &st)
	} else if errors.Is(err, fs.ErrNotExist) {
		err = nil
	}
	if err != nil {
		// a state it cannot read or parse may have held a trip: keep the extras off and start a fresh quiet clock
		// (review P2, r2 P2-1)
		st = state{NoExtra: true, Failed: st.Failed}
		late = fmt.Errorf("state %s: %w; extra slots kept off", d.state, err)
	}
	l5, l15, err := d.load()
	if err != nil {
		return fail(err)
	}
	// memory it cannot read only rules out the extra slots: the load rules still hold, and the run ends loud (review P2)
	free, memErr := d.mem()
	mem := fmt.Sprintf("%d%%", free)
	if memErr != nil {
		free, mem = -1, "unknown"
		late = errors.Join(late, memErr)
	}
	b, err = os.ReadFile(d.slots)
	if err != nil {
		return fail(err)
	}
	cur := strings.TrimSpace(string(b))
	loads := fmt.Sprintf("load5 %.2f, load15 %.2f, memory free %s", l5, l15, mem)
	if n, err := strconv.Atoi(cur); err != nil || n < 0 { // empty or broken: failing to look, never a hand value (review P3-1)
		return fail(fmt.Errorf("slots file %s holds %q, not a slot count", d.slots, cur))
	}
	if cur != "1" && cur != "2" && cur != "3" && cur != "4" {
		fmt.Fprintf(d.out, "%s slots file %s holds %q, not 1 to 4: left alone (%s)\n", now.Format(time.RFC3339), d.slots, cur, loads)
		if late != nil {
			return fail(nil)
		}
		return save(d, &st, now)
	}
	if st.NoExtra {
		back, err := quietLongEnough(&st, l15, now)
		late = errors.Join(late, err)
		if back {
			line := fmt.Sprintf("extra heavy slots allowed again: load15 under %g for %d min (%s)", quiet, int(backAfter.Minutes()), loads)
			fmt.Fprintf(d.out, "%s %s\n", now.Format(time.RFC3339), line)
			if err := watchdog.PostOn(d.home, "crius", "INFO", "crius:loadwatch", "crius:lead", now, line); err != nil {
				return fail(fmt.Errorf("extra slots back, but the bus line failed: %w", err))
			}
		}
	}
	// the trip is kept before anything can fail: failed() saves the state too (review P1, P3)
	trip := (cur == "3" || cur == "4") && l5 > limit && !st.NoExtra
	w := want(l5, l15, free, cur, st.NoExtra)
	if trip {
		st.NoExtra = true
	}
	if w != cur {
		if err := writeAtomic(d.slots, w+"\n"); err != nil {
			return fail(err)
		}
		line := fmt.Sprintf("heavy slots %s -> %s (%s)", cur, w, loads)
		fmt.Fprintf(d.out, "%s %s\n", now.Format(time.RFC3339), line)
		if err := watchdog.PostOn(d.home, "crius", "INFO", "crius:loadwatch", "all", now, line); err != nil {
			return fail(fmt.Errorf("slots set, but the bus line failed: %w", err))
		}
	}
	if trip {
		line := fmt.Sprintf("extra heavy slots off: load5 %.2f over %g with %s slots (load15 %.2f, memory free %s); the most is 2 until load15 has been under %g for %d min",
			l5, limit, cur, l15, mem, quiet, int(backAfter.Minutes()))
		fmt.Fprintf(d.out, "%s %s\n", now.Format(time.RFC3339), line)
		if err := watchdog.PostOn(d.home, "crius", "INFO", "crius:loadwatch", "crius:lead", now, line); err != nil {
			return fail(fmt.Errorf("extra slots off, but the bus line failed: %w", err))
		}
	}
	if late != nil {
		return fail(nil)
	}
	return save(d, &st, now)
}

// quietLongEnough keeps the quiet clock of tripped extra slots: a 15-min load at quiet or more stops it, the first
// minute under starts it, and once it has run backAfter the extras are back on (true). A gap of more than maxGap since
// the last quiet reading (Mac asleep, missed or failed runs), a clock set back, or a stamp it cannot parse (said in the
// error) starts it again (review P2, P3).
func quietLongEnough(st *state, l15 float64, now time.Time) (bool, error) {
	if l15 >= quiet {
		st.QuietSince, st.QuietLast = "", ""
		return false, nil
	}
	var err error
	restart := st.QuietSince == ""
	if !restart {
		since, e1 := time.Parse(time.RFC3339, st.QuietSince)
		last, e2 := time.Parse(time.RFC3339, st.QuietLast)
		gap := now.Sub(last)
		switch {
		case e1 != nil || e2 != nil:
			err = fmt.Errorf("state quiet clock (%q, %q) cannot be read: %w; started again", st.QuietSince, st.QuietLast, errors.Join(e1, e2))
			restart = true
		case gap < 0 || gap > maxGap:
			restart = true
		case now.Sub(since) >= backAfter:
			st.NoExtra, st.QuietSince, st.QuietLast = false, "", ""
			return true, nil
		}
	}
	if restart {
		st.QuietSince = now.Format(time.RFC3339)
	}
	st.QuietLast = now.Format(time.RFC3339)
	return false, err
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
		} else {
			st.Failed = true
		}
	}
	// saved even when the ALERT failed, so tripped extra slots stay off (review P1); best effort: an unwritable
	// state posts again next run
	if b, merr := json.Marshal(st); merr == nil {
		_ = writeAtomic(d.state, string(b)+"\n")
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
