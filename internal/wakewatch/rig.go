package wakewatch

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// rigDown posts one ALERT to a team's leads naming the services of its rig that are down, for each project with a rig
// folder in projects.toml (rig = "<folder of the services' <name>.pid files>"): after a boot (the boot time newer than
// the last look), or whenever the team has a live session. A service is down when its pid file names no running
// process, or was written before the boot, as a reboot reuses pids (crius:lead DECISION 11-08: the Mac rebooted 11-07
// 19:45 and the 17 rig services stayed down 11 hours until a probe hit them). A running team hears a set once: fewer of
// the same services down (being started) stays quiet, a new one down or a new boot says it again, and a team that is
// not running forgets it, so its next session hears it. What it cannot look at is said to Crius.
func rigDown(d Deps, st *state, ss []Session, sesErr error, watch projects, park parked) [][2]string {
	var c struct {
		Projects map[string]struct{ Rig string }
	}
	if _, err := toml.DecodeFile(filepath.Join(d.Home, "projects.toml"), &c); errors.Is(err, fs.ErrNotExist) {
		return nil // no projects, no rig (the other checks say a missing projects.toml)
	} else if err != nil {
		return [][2]string{{"rig-failed", "the rig check could not read projects.toml: " + err.Error()}}
	}
	var rigs []string
	for p, v := range c.Projects {
		if v.Rig != "" && watch.has(p) && !park.has(p, p) {
			rigs = append(rigs, p)
		}
	}
	if len(rigs) == 0 {
		return nil
	}
	slices.Sort(rigs)
	var failed []string
	boot, bootErr := d.Boot()
	if bootErr != nil {
		failed = append(failed, "boot time: "+bootErr.Error())
	}
	running := map[string]bool{}
	for _, s := range ss {
		running[project(BusName(s.Name))] = true // "quotes · frontend" is quotes:frontend
	}
	var procs []Proc
	var leads map[string]bool
	var leadErr error
	looked := false
	for _, p := range rigs {
		// Each project keeps the boot it was told about, so one that could not be told is told on the next run and no
		// other is told twice (review r2 of 4ebed9c P2). kern.boottime moves by the size of a clock step (a time sync, a
		// wake): only a minute or more is a boot.
		booted := bootErr == nil && boot.Sub(st.RigBoot[p]) > time.Minute
		if sesErr == nil && !running[strings.ToLower(p)] {
			delete(st.Rig, p) // heard by no session: the team's next one hears it
			if !booted {
				continue
			}
		} else if sesErr != nil && !booted {
			continue
		}
		if !looked {
			looked = true
			var err error
			if procs, err = d.Procs(); err != nil {
				return said(append(failed, "process list: "+err.Error()))
			}
		}
		dir := c.Projects[p].Rig
		if strings.HasPrefix(dir, "~/") {
			h, _ := os.UserHomeDir()
			dir = filepath.Join(h, dir[2:])
		}
		down, err := deadServices(dir, procs, boot, d.Now)
		if err != nil {
			failed = append(failed, err.Error())
			continue
		}
		if len(down) == 0 {
			delete(st.Rig, p)
			if bootErr == nil {
				st.RigBoot[p] = boot
			}
			continue
		}
		if !booted && subset(down, st.Rig[p]) {
			if sesErr == nil {
				st.Rig[p] = strings.Join(down, ", ")
			}
			continue
		}
		if leads == nil && leadErr == nil {
			_, _, _, leads, leadErr = finishedLeads(d.Home)
		}
		to := []string{p + ":lead"}
		if leadErr == nil && len(leadsOf(leads, p)) > 0 {
			to = leadsOf(leads, p)
		}
		why := "while the team runs"
		if booted {
			why = "since the boot at " + boot.Local().Format("01-02 15:04")
		}
		// the names last and fitted to the bus's 300-rune cut, the count and folder first: a long list loses only names
		head := fmt.Sprintf("rig services down %s, %d of the pid files in %s; start them before the team's next probe or test: ",
			why, len(down), c.Projects[p].Rig)
		if err := postLine(d, "ALERT", p, to, head+fit(down, 300-len([]rune(head)))); err != nil {
			failed = append(failed, err.Error()) // its boot stays unsaid: tried again on the next run
			continue
		}
		if sesErr == nil && running[strings.ToLower(p)] {
			st.Rig[p] = strings.Join(down, ", ")
		}
		if bootErr == nil {
			st.RigBoot[p] = boot
		}
	}
	return said(failed)
}

// subset says whether every name in down is in the set said before (", " joined).
func subset(down []string, said string) bool {
	if said == "" {
		return false
	}
	before := strings.Split(said, ", ")
	for _, n := range down {
		if !slices.Contains(before, n) {
			return false
		}
	}
	return true
}

// fit joins names with ", " in at most room runes, ending in "+N more" when they do not all fit.
func fit(names []string, room int) string {
	all := strings.Join(names, ", ")
	if len([]rune(all)) <= room {
		return all
	}
	out := ""
	for i, n := range names {
		next := n
		if out != "" {
			next = out + ", " + n
		}
		more := fmt.Sprintf(" +%d more", len(names)-i-1)
		if i == len(names)-1 {
			more = ""
		}
		if len([]rune(next+more)) > room {
			return out + fmt.Sprintf(" +%d more", len(names)-i)
		}
		out = next
	}
	return out
}

func said(failed []string) [][2]string {
	if failed == nil {
		return nil
	}
	return [][2]string{{"rig-failed", "the rig check failed to look: " + strings.Join(failed, "; ")}}
}

// deadServices names, sorted, each <name>.pid in dir whose pid runs no process, or whose file is older than the boot. A
// file written in the last 10 s that holds no pid is being written (echo $! > x.pid empties it first): it waits a run.
func deadServices(dir string, procs []Proc, boot, now time.Time) ([]string, error) {
	entries, err := os.ReadDir(dir) // unlike a glob, a folder it cannot list is an error
	if err != nil {
		return nil, fmt.Errorf("rig folder: %w", err)
	}
	alive := map[int]bool{}
	for _, p := range procs {
		alive[p.PID] = true
	}
	var down []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".pid") {
			continue
		}
		f := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(f)
		if errors.Is(err, fs.ErrNotExist) {
			continue // removed since the listing: stopped on purpose
		}
		if err != nil {
			return nil, err
		}
		fi, err := os.Stat(f)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil && now.Sub(fi.ModTime()) < 10*time.Second {
			continue
		}
		if err != nil || pid <= 0 || !alive[pid] || !boot.IsZero() && fi.ModTime().Before(boot) {
			down = append(down, strings.TrimSuffix(e.Name(), ".pid"))
		}
	}
	slices.Sort(down)
	return down, nil
}
