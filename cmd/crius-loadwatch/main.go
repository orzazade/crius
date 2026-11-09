// Command crius-loadwatch sets the heavy-slot count of the Appxite rig from the machine load; launchd runs it every
// minute. It follows the Mac load rule (owner 11-08; crius:lead DECISIONs 11-08, 11-09):
//
//   - the 5-min load is over 28: 1 slot, at once;
//   - else the 15-min load is 28 or more: no step up (easing on the 5-min load flipped 4 times on 11-05);
//   - else, by CPU idle (top's second sample) with memory at least 40% free (memory_pressure): up to 4 slots at idle
//     40% or more, 3 at 25% or more; else 2 slots.
//
// The up-steps follow CPU idle, not the load average: on 11-09 a load of 17-28 came with CPU 56% idle and 53% memory
// free, 18 jobs waited 25 min on 2 slots, and the slots changed 111 times that day. The extra slots come down, whatever
// the 15-min load, only past a dead band of 10 points held for three readings in a row (one 5 s reading fell from 60%
// to 19% here): 4 to 3 under 30% idle, 3 to 2 under 15% (4 goes straight to 2 there); at once to 2 when memory is under
// 40% free. CPU idle or memory it cannot read rules the extra slots out, and the run is loud.
//
// The 5-min load over 28 while 3 or 4 slots are set turns the extra slots off (the most it sets is then 2) and posts the
// numbers to crius:lead. They come back, with one more line to crius:lead, once the 15-min load has been under 28 for 10
// minutes after that; a minute at 28 or more starts the 10 minutes again.
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
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/store"
	"github.com/orzazade/crius/internal/watchdog"
)

const (
	limit = 28.0
	// CPU idle percent for the up-steps (crius:lead 11-09) and, 10 points lower, the dead band the extras come down past
	fourIdle  = 40.0
	threeIdle = 25.0
	downFour  = 30.0
	downThree = 15.0
	minFree   = 40 // percent
	downRuns  = 3  // readings in a row under the band before a step down
)

// state is what one run leaves the next: whether a failure was already posted, whether the extra slots are off, and
// since when the 15-min load has been under 28 while they are.
type state struct {
	Failed     bool   `json:"failed,omitempty"`
	HungFailed bool   `json:"hungFailed,omitempty"` // the hung-holder check's failure was posted (hungLoud)
	NoExtra    bool   `json:"noExtra,omitempty"`
	QuietSince string `json:"quietSince,omitempty"` // RFC3339
	QuietLast  string `json:"quietLast,omitempty"`  // the last quiet reading, RFC3339
	// Idle: the last downRuns CPU idle readings, newest last (a step down needs all of them under the band)
	Idle []float64 `json:"idle,omitempty"`
	// Holders: each slot holder's job as last seen, by slot file and token (hung.go)
	Holders map[string]holderSeen `json:"holders,omitempty"`
}

// backAfter is how long the 15-min load must stay under limit before tripped extra slots come back.
const backAfter = 10 * time.Minute

// maxGap is the longest time between two quiet readings that keeps the quiet clock running (the job runs every minute).
const maxGap = 3 * time.Minute

type deps struct {
	home, slots, state string
	now                func() time.Time
	load               func() (load5, load15 float64, err error)
	mem                func() (freePct int, err error)
	cpu                func() (idlePct float64, err error)
	ps                 func() ([]proc, error)             // every process (hung.go)
	outputs            func(pids []int) ([]string, error) // the regular files those processes have as stdout or stderr
	lead               func(cwd string) (project string, leads []string, err error)
	out                io.Writer
}

func main() {
	home := store.Home()
	os.Exit(run(deps{home: home, slots: filepath.Join(os.Getenv("HOME"), "AppxiteRig", "heavy", "slots"),
		state: filepath.Join(home, "state", "loadwatch.json"), now: time.Now, load: sysLoad, mem: memFree, cpu: cpuIdle,
		ps: sysPS, outputs: sysOutputs, lead: leadOf(home), out: os.Stdout}))
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

// cpuIdle reads the CPU idle percentage from top's second sample over 5 seconds (one second is too noisy to set slots by).
func cpuIdle() (float64, error) {
	b, err := timed(nil, "/usr/bin/top", "-l", "2", "-n", "0", "-s", "5") // a stuck top would hold every run (review P2-2)
	if err != nil {
		return 0, fmt.Errorf("top: %w", err)
	}
	return parseTopIdle(string(b))
}

// parseTopIdle reads idle from the last of top's two "CPU usage: 30.25% user, 13.50% sys, 56.25% idle" lines; the first
// covers the time since boot.
func parseTopIdle(out string) (float64, error) {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "CPU usage:") {
			lines = append(lines, strings.TrimSpace(l))
		}
	}
	if len(lines) < 2 {
		return 0, fmt.Errorf("top: %d CPU usage lines, want 2", len(lines))
	}
	l := lines[len(lines)-1]
	for _, f := range strings.Split(strings.TrimPrefix(l, "CPU usage:"), ",") {
		if v, ok := strings.CutSuffix(strings.TrimSpace(f), "% idle"); ok {
			n, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return 0, fmt.Errorf("top: %q: %w", l, err)
			}
			return n, nil
		}
	}
	return 0, fmt.Errorf("top: no idle in %q", l)
}

// want gives the slot count the machine asks for (cur is "1" to "4"; idle is CPU idle %, -1 when unread; held is the
// highest of the last downRuns readings, -1 while there are fewer). Up-steps go straight to the level on this reading;
// the extra slots come down only when every recent reading is past the dead band. 2 to 1 is the over-28 rule only.
func want(l5, l15, idle, held float64, free int, cur string, noExtra bool) string {
	if l5 > limit {
		return "1"
	}
	extra := free >= minFree && idle >= 0 && !noExtra
	up := 0
	switch {
	case l15 >= limit:
	case idle >= fourIdle && extra:
		up = 4
	case idle >= threeIdle && extra:
		up = 3
	default:
		up = 2
	}
	n, _ := strconv.Atoi(cur)
	if up > n {
		return strconv.Itoa(up)
	}
	if n == 4 && (!extra || held >= 0 && held < downFour) {
		n = 3
	}
	if n == 3 && (!extra || held >= 0 && held < downThree) {
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
		st = state{NoExtra: true, Failed: st.Failed, HungFailed: st.HungFailed}
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
	// CPU idle it cannot read rules out the extra slots too, and the run ends loud
	idle, cpuErr := d.cpu()
	cpu := fmt.Sprintf("%.1f%%", idle) // one decimal: 39.6% must not read as the 40% of a step it did not take
	if cpuErr != nil {
		idle, cpu = -1, "unknown"
		late = errors.Join(late, cpuErr)
		st.Idle = nil
	} else if st.Idle = append(st.Idle, idle); len(st.Idle) > downRuns {
		st.Idle = st.Idle[len(st.Idle)-downRuns:]
	}
	held := -1.0
	if len(st.Idle) == downRuns {
		held = slices.Max(st.Idle)
	}
	b, err = os.ReadFile(d.slots)
	if err != nil {
		return fail(err)
	}
	cur := strings.TrimSpace(string(b))
	loads := fmt.Sprintf("load5 %.2f, load15 %.2f, cpu idle %s, memory free %s", l5, l15, cpu, mem)
	if n, err := strconv.Atoi(cur); err != nil || n < 0 { // empty or broken: failing to look, never a hand value (review P3-1)
		return fail(fmt.Errorf("slots file %s holds %q, not a slot count", d.slots, cur))
	}
	if cur != "1" && cur != "2" && cur != "3" && cur != "4" {
		fmt.Fprintf(d.out, "%s slots file %s holds %q, not 1 to 4: left alone (%s)\n", now.Format(time.RFC3339), d.slots, cur, loads)
		return end(d, &st, now, late, fail)
	}
	if st.NoExtra {
		back, err := quietLongEnough(&st, l15, now)
		late = errors.Join(late, err)
		if back {
			line := fmt.Sprintf("extra heavy slots allowed again: load15 under %g for %d min (%s)", limit, int(backAfter.Minutes()), loads)
			fmt.Fprintf(d.out, "%s %s\n", now.Format(time.RFC3339), line)
			if err := watchdog.PostOn(d.home, "crius", "INFO", "crius:loadwatch", "crius:lead", now, line); err != nil {
				return fail(fmt.Errorf("extra slots back, but the bus line failed: %w", err))
			}
		}
	}
	// the trip is kept before anything can fail: failed() saves the state too (review P1, P3)
	trip := (cur == "3" || cur == "4") && l5 > limit && !st.NoExtra
	w := want(l5, l15, idle, held, free, cur, st.NoExtra)
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
		line := fmt.Sprintf("extra heavy slots off: load5 %.2f over %g with %s slots (load15 %.2f, cpu idle %s, memory free %s); the most is 2 until load15 has been under %g for %d min",
			l5, limit, cur, l15, cpu, mem, limit, int(backAfter.Minutes()))
		fmt.Fprintf(d.out, "%s %s\n", now.Format(time.RFC3339), line)
		if err := watchdog.PostOn(d.home, "crius", "INFO", "crius:loadwatch", "crius:lead", now, line); err != nil {
			return fail(fmt.Errorf("extra slots off, but the bus line failed: %w", err))
		}
	}
	return end(d, &st, now, late, fail)
}

// end runs the hung-holder check, then ends the run: loud on a slot-rule failure (late), and exit 2 on a failed
// hung-holder check, which hungLoud says apart from the slot rules (review of 9227798 P2-3).
func end(d deps, st *state, now time.Time, late error, fail func(error) int) int {
	herr := hungLoud(d, st, now)
	if late != nil {
		return fail(nil)
	}
	if rc := save(d, st, now); rc != 0 || herr == nil {
		return rc
	}
	return 2
}

// hungLoud runs the hung-holder check. Its failure is said in the log every run and on the bus once per spell, in its
// own words and with its own flag: it is no failure of the slot rules, and a repeating one must not hide one of those.
func hungLoud(d deps, st *state, now time.Time) error {
	err := hung(d, st, now)
	if err == nil {
		st.HungFailed = false
		return nil
	}
	fmt.Fprintf(d.out, "%s LOUD crius-loadwatch hung-holder check failed to look: %v\n", now.Format(time.RFC3339), err)
	if !st.HungFailed {
		if perr := watchdog.PostOn(d.home, "crius", "ALERT", "crius:loadwatch", "crius:lead", now,
			"the hung-holder check failed to look (the slots still follow the load): "+err.Error()); perr != nil {
			fmt.Fprintf(d.out, "%s LOUD the bus line failed too: %v\n", now.Format(time.RFC3339), perr)
		} else {
			st.HungFailed = true
		}
	}
	return err
}

// quietLongEnough keeps the quiet clock of tripped extra slots: a 15-min load at limit or more stops it, the first
// minute under starts it, and once it has run backAfter the extras are back on (true). A gap of more than maxGap since
// the last quiet reading (Mac asleep, missed or failed runs), a clock set back, or a stamp it cannot parse (said in the
// error) starts it again (review P2, P3).
func quietLongEnough(st *state, l15 float64, now time.Time) (bool, error) {
	if l15 >= limit {
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
