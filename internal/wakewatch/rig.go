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
// 19:45 and the 17 rig services stayed down 11 hours until a probe hit them). The same set is said once; a new boot or
// another set says it again, and a rig with none down clears it. What it cannot look at is said to Crius.
func rigDown(d Deps, st *state, ss []Session, sesErr error, watch projects, park parked) [][2]string {
	var c struct {
		Projects map[string]struct{ Rig string }
	}
	if _, err := toml.DecodeFile(filepath.Join(d.Home, "projects.toml"), &c); errors.Is(err, fs.ErrNotExist) {
		return nil // no projects, no rig (the other checks say a missing projects.toml)
	} else if err != nil {
		return [][2]string{{"rig-failed", "the rig check could not read projects.toml: " + err.Error()}}
	}
	var failed []string
	boot, bootErr := d.Boot()
	if bootErr != nil {
		failed = append(failed, "boot time: "+bootErr.Error())
	}
	booted := bootErr == nil && boot.After(st.RigBoot)
	running := map[string]bool{}
	for _, s := range ss {
		running[strings.TrimSpace(project(s.Name))] = true
	}
	var procs []Proc
	var leads map[string]bool
	var leadErr error
	looked := false
	for _, p := range slices.Sorted(func(yield func(string) bool) {
		for k := range c.Projects {
			if !yield(k) {
				return
			}
		}
	}) {
		dir := c.Projects[p].Rig
		if dir == "" || !watch.has(p) || park.has(p, p) || !booted && (sesErr != nil || !running[p]) {
			continue
		}
		if !looked {
			looked = true
			var err error
			if procs, err = d.Procs(); err != nil {
				return append([][2]string{{"rig-failed", "the rig check failed to list processes: " + err.Error()}}, said(failed)...)
			}
		}
		if strings.HasPrefix(dir, "~/") {
			h, _ := os.UserHomeDir()
			dir = filepath.Join(h, dir[2:])
		}
		down, err := deadServices(dir, procs, boot)
		if err != nil {
			failed = append(failed, err.Error())
			continue
		}
		if len(down) == 0 {
			delete(st.Rig, p)
			continue
		}
		set := strings.Join(down, ", ")
		if st.Rig[p] == set && !booted {
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
		// the names last: the bus cuts a line at 300 runes, and the count and folder say what a cut list leaves out
		text := fmt.Sprintf("rig services down %s, %d of the pid files in %s; start them before the team's next probe or test: %s",
			why, len(down), c.Projects[p].Rig, set)
		if err := postLine(d, "ALERT", p, to, text); err != nil {
			failed = append(failed, err.Error())
			continue
		}
		st.Rig[p] = set
	}
	if bootErr == nil {
		st.RigBoot = boot
	}
	return said(failed)
}

func said(failed []string) [][2]string {
	if failed == nil {
		return nil
	}
	return [][2]string{{"rig-failed", "the rig check failed to look: " + strings.Join(failed, "; ")}}
}

// deadServices names, sorted, each <name>.pid in dir whose pid runs no process, or whose file is older than the boot.
func deadServices(dir string, procs []Proc, boot time.Time) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.pid"))
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		if _, err := os.Stat(dir); err != nil {
			return nil, fmt.Errorf("rig folder: %w", err)
		}
	}
	alive := map[int]bool{}
	for _, p := range procs {
		alive[p.PID] = true
	}
	var down []string
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".pid")
		b, err := os.ReadFile(f)
		if errors.Is(err, fs.ErrNotExist) {
			continue // removed since the glob: stopped on purpose
		}
		if err != nil {
			return nil, err
		}
		fi, err := os.Stat(f)
		if err != nil {
			return nil, err
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil || pid <= 0 || !alive[pid] || !boot.IsZero() && fi.ModTime().Before(boot) {
			down = append(down, name)
		}
	}
	slices.Sort(down)
	return down, nil
}
