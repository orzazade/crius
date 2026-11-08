package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/ingest"
	"github.com/orzazade/crius/internal/watchdog"
)

// The hung-holder check (crius:lead 11-08): a heavy slot held over heldMin by a job that used no CPU for idleMin and
// wrote nothing for idleMin gets one INFO to its project's lead. The gate stops a job at 20 min (its HEAVY_MAX_S
// ceiling), so a holder at 45 min has already outlived its own kill; 15 min of no CPU in the whole job group and no
// output is far past any pause of a working build or suite, yet leaves the lead half an hour to act before the queue
// has waited an hour on it.
const (
	heldMin = 45 * time.Minute // the owner's line
	idleMin = 15 * time.Minute
	// cpuEps: CPU-time growth over a whole idle spell that still counts as none. ps shows centiseconds and an idle
	// event loop wakes now and then; a job at work uses whole seconds a minute.
	cpuEps = 0.5
)

// lookTimeout bounds each ps and lsof: a stuck lsof (a hung network or FUSE mount) would hold the run, and with it the
// slot rules, since launchd starts no run while one goes (review of 9227798 P2-1). lsofPath is a var for its test.
var (
	lookTimeout = 10 * time.Second
	lsofPath    = "/usr/sbin/lsof"
)

// proc is one process as ps shows it: cpu is its cumulative CPU time in seconds, start its lstart in single spaces.
type proc struct {
	pid, ppid, pgid int
	cpu             float64
	start, cmd      string
}

// holderSeen is what one run leaves the next of a slot's holder: its job's CPU time and processes when its idle
// clock started, and whether its line went out.
type holderSeen struct {
	CPU       float64 `json:"cpu"`
	PIDs      string  `json:"pids"`
	IdleSince string  `json:"idleSince"`
	Last      string  `json:"last"` // the last run that read it: a gap over maxGap (the Mac asleep) restarts the clock
	Said      bool    `json:"said,omitempty"`
}

// holder is a slot-<n>.json as the gate (heavy.ts) writes it: pid the wrapper, child its job, which runs in its own
// process group (group id = child), each with its ps lstart.
type holder struct {
	PID        int    `json:"pid"`
	PIDStart   string `json:"pidStart"`
	Child      int    `json:"child"`
	ChildStart string `json:"childStart"`
	Cmd        string `json:"cmd"`
	Cwd        string `json:"cwd"`
	At         string `json:"at"`
	Token      string `json:"token"`
}

// hung judges each slot holder beside the slots file. ps runs before the holder files are read, so a holder that
// let go in between is gone, not stale. What it cannot read is returned, never taken as "no hung job".
func hung(d deps, st *state, now time.Time) error {
	files, err := filepath.Glob(filepath.Join(filepath.Dir(d.slots), "slot-*.json"))
	if err != nil {
		return fmt.Errorf("hung-holder check: %w", err)
	}
	if len(files) == 0 {
		st.Holders = nil
		return nil
	}
	procs, err := d.ps()
	if err != nil {
		return fmt.Errorf("hung-holder check: %w", err)
	}
	byPID := map[int]proc{}
	kids := map[int][]int{}
	for _, p := range procs {
		byPID[p.pid] = p
		kids[p.ppid] = append(kids[p.ppid], p.pid)
	}
	var errs []error
	seen := map[string]holderSeen{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if errors.Is(err, fs.ErrNotExist) {
			continue // let go since the glob
		}
		var h holder
		if err == nil {
			err = json.Unmarshal(b, &h)
		}
		at, terr := time.Parse(time.RFC3339, h.At)
		if err = errors.Join(err, terr); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", f, err))
			continue
		}
		slot := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(f), "slot-"), ".json")
		key := slot + ":" + h.Token
		w := st.Holders[key]
		held := now.Sub(at)
		wrapper := alive(byPID, h.PID, h.PIDStart)
		tree := group(procs, byPID, kids, h.Child, h.ChildStart)
		// what to stop: the job's group, or the wrapper's pid once the job is gone, as that group id may name another
		// job by the time the lead acts (review of 9227798 P2-2)
		stop := fmt.Sprintf("stop its group %d", h.Child)
		if tree == nil && wrapper { // the job is gone and the wrapper still holds: the wrapper is what to watch
			tree = descend(kids, []int{h.PID})
			stop = fmt.Sprintf("stop pid %d (its job ended; the wrapper holds the slot)", h.PID)
		}
		if tree == nil { // neither the wrapper nor its job: a stale holder
			if held >= heldMin && !w.Said {
				text := fmt.Sprintf("heavy slot %s holder pid %d (%s) is gone or its pid reused, yet its slot file stays (held %d min): a stale holder",
					slot, h.PID, base(h.Cmd), int(held.Minutes()))
				sent, err := post(d, now, h.Cwd, text)
				errs = append(errs, err)
				w.Said = sent
			}
			seen[key] = w
			continue
		}
		cpu := 0.0
		for _, pid := range tree {
			cpu += byPID[pid].cpu
		}
		pids := fmt.Sprint(tree)
		since, e1 := time.Parse(time.RFC3339, w.IdleSince)
		last, e2 := time.Parse(time.RFC3339, w.Last)
		if gap := now.Sub(last); e1 != nil || e2 != nil || gap < 0 || gap > maxGap || w.PIDs != pids || cpu < w.CPU || cpu-w.CPU > cpuEps {
			w = holderSeen{CPU: cpu, PIDs: pids, IdleSince: now.Format(time.RFC3339), Said: w.Said}
			since = now
		}
		w.Last = now.Format(time.RFC3339)
		seen[key] = w
		idle := now.Sub(since)
		if w.Said || held < heldMin || idle < idleMin {
			continue
		}
		look := tree
		if wrapper && !slices.Contains(tree, h.PID) { // a reused wrapper pid's fresh stdout would read as output (P3-5)
			look = append([]int{h.PID}, tree...)
		}
		outs, err := d.outputs(look)
		if err != nil {
			errs = append(errs, fmt.Errorf("hung-holder check: output of slot %s: %w", slot, err))
			continue
		}
		var wrote time.Time
		wroteName := ""
		for _, o := range outs {
			fi, err := os.Stat(o)
			if errors.Is(err, fs.ErrNotExist) {
				continue // deleted while open: no time to read
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("hung-holder check: %w", err))
				continue
			}
			if fi.ModTime().After(wrote) {
				wrote, wroteName = fi.ModTime(), filepath.Base(o)
			}
		}
		// a pipe or terminal shows no output time: said plainly, as then only the CPU judged it (review of 9227798 P2-4)
		out := "output unseen (stdout is no file): judged on CPU only"
		if wroteName != "" {
			if now.Sub(wrote) < idleMin {
				continue // writing: working
			}
			out = fmt.Sprintf("last output %s (%s)", wrote.Local().Format("15:04"), wroteName)
		}
		// the advice before the output detail: the bus cuts a line at 300 runes, and a long log name must not cut it
		text := fmt.Sprintf("heavy slot %s looks hung: pid %d (%s) held %d min, no CPU in its job for %d min; check it or %s; %s",
			slot, h.PID, base(h.Cmd), int(held.Minutes()), int(idle.Minutes()), stop, out)
		sent, err := post(d, now, h.Cwd, text)
		errs = append(errs, err)
		w.Said = sent
		seen[key] = w
	}
	st.Holders = seen
	if len(seen) == 0 {
		st.Holders = nil
	}
	return errors.Join(errs...)
}

// alive: pid runs and is the process that started at start (ps lstart, compared word by word); a pid with no start
// on record counts when it runs.
func alive(byPID map[int]proc, pid int, start string) bool {
	p, ok := byPID[pid]
	return ok && pid > 0 && (start == "" || strings.Join(strings.Fields(p.start), " ") == strings.Join(strings.Fields(start), " "))
}

// group is the job's processes, sorted: its process group (id child) and every descendant of a member, so one that
// left the group still counts; nil when the group is gone or its leader's pid now names another process.
func group(procs []proc, byPID map[int]proc, kids map[int][]int, child int, start string) []int {
	if child <= 0 {
		return nil
	}
	if _, ok := byPID[child]; ok && !alive(byPID, child, start) {
		return nil
	}
	var roots []int
	for _, p := range procs {
		if p.pgid == child {
			roots = append(roots, p.pid)
		}
	}
	return descend(kids, roots)
}

// descend gives roots and all their descendants, sorted, or nil for no roots.
func descend(kids map[int][]int, roots []int) []int {
	var out []int
	have := map[int]bool{}
	for len(roots) > 0 {
		p := roots[0]
		roots = roots[1:]
		if have[p] {
			continue
		}
		have[p] = true
		out = append(out, p)
		roots = append(roots, kids[p]...)
	}
	slices.Sort(out)
	return out
}

// base names a job by its script: the first word's base name, or the second's after a shell or runtime.
func base(cmd string) string {
	w := strings.Fields(cmd)
	if len(w) == 0 {
		return "?"
	}
	if len(w) > 1 && slices.Contains([]string{"bash", "sh", "zsh", "bun", "node", "env"}, filepath.Base(w[0])) {
		return filepath.Base(w[1])
	}
	return filepath.Base(w[0])
}

// post sends the line to the leads of the holder's project on its bus, or to Crius when the project or its bus is
// not known; sent says the line went out. A lookup failure is said in the error even when the line went to Crius.
func post(d deps, now time.Time, cwd, text string) (sent bool, err error) {
	project, leads, lerr := d.lead(cwd)
	if lerr == nil && project != "" {
		_, serr := os.Stat(filepath.Join(d.home, "bus", project+".log"))
		if serr == nil {
			if len(leads) == 0 {
				leads = []string{project + ":lead"}
			}
			err := watchdog.PostOn(d.home, project, "INFO", "crius:loadwatch", strings.Join(leads, ", "), now, text)
			return err == nil, err
		}
		if !errors.Is(serr, fs.ErrNotExist) {
			lerr = serr
		}
	}
	err = watchdog.PostOn(d.home, "crius", "INFO", "crius:loadwatch", "crius:lead", now, text)
	sent = err == nil
	if lerr != nil {
		err = errors.Join(err, fmt.Errorf("hung-holder check: the project of %s: %w", cwd, lerr))
	}
	return sent, err
}

// sysPS lists every process: pid, ppid, pgid, CPU time, lstart and command. Like the gate, it runs ps in the machine's
// zone (no TZ) and the C locale, so lstart reads as the gate wrote it.
func sysPS() ([]proc, error) {
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "TZ=") && !strings.HasPrefix(e, "LC_") && !strings.HasPrefix(e, "LANG=") {
			env = append(env, e)
		}
	}
	b, err := timed(append(env, "LC_ALL=C"), "/bin/ps", "-axww", "-o", "pid=,ppid=,pgid=,time=,lstart=,command=")
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	return parsePS(b)
}

// timed runs a tool within lookTimeout and gives its stdout; env nil keeps this process's environment.
func timed(env []string, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), lookTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	cmd.WaitDelay = time.Second // a child left holding its pipe cannot keep the read open
	b, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("%s gave no answer in %s", filepath.Base(name), lookTimeout)
	}
	return b, err
}

// parsePS reads sysPS's lines; a line it cannot read, or no line, is an error.
func parsePS(b []byte) ([]proc, error) {
	var out []proc
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) == 0 {
			continue
		}
		if len(f) < 10 {
			return nil, fmt.Errorf("ps: cannot read %q", l)
		}
		pid, e1 := strconv.Atoi(f[0])
		ppid, e2 := strconv.Atoi(f[1])
		pgid, e3 := strconv.Atoi(f[2])
		cpu, e4 := cpuTime(f[3])
		if err := errors.Join(e1, e2, e3, e4); err != nil {
			return nil, fmt.Errorf("ps: cannot read %q: %w", l, err)
		}
		out = append(out, proc{pid: pid, ppid: ppid, pgid: pgid, cpu: cpu, start: strings.Join(f[4:9], " "), cmd: strings.Join(f[9:], " ")})
	}
	if len(out) == 0 {
		return nil, errors.New("ps: no processes listed")
	}
	return out, nil
}

// cpuTime reads ps time ([d-][h:]m:s[.cc]) as seconds.
func cpuTime(s string) (float64, error) {
	days := 0.0
	if d, rest, ok := strings.Cut(s, "-"); ok {
		n, err := strconv.Atoi(d)
		if err != nil {
			return 0, fmt.Errorf("cpu time %q: %w", s, err)
		}
		days, s = float64(n), rest
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, fmt.Errorf("cpu time %q: want [h:]m:s", s)
	}
	total := 0.0
	for i, p := range parts {
		v, err := strconv.ParseFloat(p, 64)
		if err != nil || v < 0 || i < len(parts)-1 && strings.Contains(p, ".") {
			return 0, fmt.Errorf("cpu time %q: bad part %q", s, p)
		}
		total = total*60 + v
	}
	return days*86400 + total, nil
}

// sysOutputs names the regular files the given processes have open as stdout or stderr (lsof). The gate passes its
// own stdout and stderr to the job (stdio inherit) and names no log in the slot file, and callers send them to a log
// file, so the newest change to those files is the job's last output; a pipe or terminal is no file and gives none.
// lsof exits 1 when a pid went away meanwhile: what it found still counts.
func sysOutputs(pids []int) ([]string, error) {
	ids := make([]string, len(pids))
	for i, p := range pids {
		ids[i] = strconv.Itoa(p)
	}
	// -b: no kernel call that can block on a mount; -n -P: a socket on fd 1 or 2 never waits on DNS (P2-1)
	b, err := timed(nil, lsofPath, "-w", "-b", "-n", "-P", "-a", "-p", strings.Join(ids, ","), "-d", "1,2", "-Ftn")
	var ee *exec.ExitError
	if err != nil && !(errors.As(err, &ee) && ee.ExitCode() == 1 && len(ee.Stderr) == 0) {
		return nil, fmt.Errorf("lsof: %w", err)
	}
	var out []string
	reg := false
	for _, l := range strings.Split(string(b), "\n") {
		switch {
		case strings.HasPrefix(l, "t"):
			reg = l == "tREG"
		case strings.HasPrefix(l, "n") && reg:
			if n := l[1:]; !slices.Contains(out, n) {
				out = append(out, n)
			}
			reg = false
		}
	}
	return out, nil
}

// leadOf files a folder as ingest does and names that project's leads from projects.toml.
func leadOf(home string) func(cwd string) (string, []string, error) {
	return func(cwd string) (string, []string, error) {
		project, _, err := ingest.Filing(home, cwd, "")
		if err != nil || project == "" {
			return "", nil, err
		}
		ls, err := ingest.Leads(home)
		if err != nil {
			return project, nil, err
		}
		var leads []string
		for _, l := range ls {
			if strings.HasPrefix(l.Name, project+":") {
				leads = append(leads, l.Name)
			}
		}
		return project, leads, nil
	}
}
