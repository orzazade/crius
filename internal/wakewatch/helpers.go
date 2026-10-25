package wakewatch

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// helperList is <home>/helpers.toml, the helpers that should run: a process named by a pid file, a launchd job, or a
// helper that shows by a bus line (the Quotes lead's hourly report, a timer inside its session). Bus waiters have no
// pid file and end with their session; the idle-tab check covers a session that does not wait.
type helperList struct {
	PID []struct {
		Name, File string
	} `toml:"pid"`
	Launchd []struct {
		Name, Label string
	} `toml:"launchd"`
	Bus []struct {
		Name, Log, From string
		Has             []string // any of these, in any case, in the line's text
		WithinMin       int      `toml:"within_min"`
	} `toml:"bus"`
}

// BootDeps is what AfterBoot reads and does. Process gives a pid's start time, or false when no process has it.
type BootDeps struct {
	Home    string
	Now     time.Time
	Process func(pid int) (time.Time, bool, error)
	Launchd func(label string) (loaded bool, err error)
	Send    func(text string) error
}

// AfterBoot runs once after each login (launchd RunAtLoad): it checks every helper in helpers.toml and names the ones
// not running to Crius, by the wakewatch tab line (10-25: after the 02:26 account move and the 11:44 restart nobody
// listed what had to be started again; the Quotes hourly report stayed down 15 h). All running: nothing is sent. A
// list that does not read, or a check that fails, is sent too: failed to look is not found nothing.
func AfterBoot(d BootDeps) error {
	var l helperList
	path := filepath.Join(d.Home, "helpers.toml")
	md, err := toml.DecodeFile(path, &l)
	if err == nil && len(md.Undecoded()) > 0 {
		err = fmt.Errorf("unknown keys %v", md.Undecoded())
	}
	if err != nil {
		return d.Send(Prefix + Clean("after login, could not read the helper list "+path+": "+err.Error()))
	}
	var down []string
	n := 0
	for _, h := range l.PID {
		n++
		if why, err := pidDown(d, h.File); err != nil {
			down = append(down, h.Name+" (could not look: "+err.Error()+")")
		} else if why != "" {
			down = append(down, h.Name+" ("+why+")")
		}
	}
	for _, h := range l.Launchd {
		n++
		if loaded, err := d.Launchd(h.Label); err != nil {
			down = append(down, h.Name+" (could not look: "+err.Error()+")")
		} else if !loaded {
			down = append(down, h.Name+" (launchd "+h.Label+" not loaded)")
		}
	}
	for _, h := range l.Bus {
		n++
		at, err := lastLine(filepath.Join(d.Home, "bus", h.Log+".log"), h.From, h.Has, d.Now.Location())
		switch {
		case err != nil:
			down = append(down, h.Name+" (could not look: "+err.Error()+")")
		case at.IsZero():
			down = append(down, fmt.Sprintf("%s (no %s line with '%s' on %s.log)", h.Name, h.From, strings.Join(h.Has, "' or '"), h.Log))
		case d.Now.Sub(at) > time.Duration(h.WithinMin)*time.Minute:
			down = append(down, fmt.Sprintf("%s (last %s line with '%s' %v ago)", h.Name, h.From, strings.Join(h.Has, "' or '"),
				fmt.Sprintf("%dh%dm", int(d.Now.Sub(at).Hours()), int(d.Now.Sub(at).Minutes())%60)))
		}
	}
	if len(down) == 0 {
		fmt.Printf("%s after login: all %d helpers run\n", d.Now.Format(time.RFC3339), n)
		return nil
	}
	return d.Send(Prefix + Clean(fmt.Sprintf("after login, %d of %d helpers are not running: %s", len(down), n,
		strings.Join(down, "; "))))
}

// pidDown says why the process of a pid file is not running, or "" when it is: the pid must be a process that started
// no later than the file was written, else the system gave the pid to a later process (after a restart, often).
func pidDown(d BootDeps, file string) (string, error) {
	path := file
	if rest, ok := strings.CutPrefix(file, "~/"); ok {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, rest)
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "no " + file, nil
	}
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return "", fmt.Errorf("%s holds no pid: %q", file, Clean(string(b)))
	}
	start, ok, err := d.Process(pid)
	switch {
	case err != nil:
		return "", err
	case !ok:
		return fmt.Sprintf("pid %d from %s is gone", pid, file), nil
	case start.After(fi.ModTime().Add(2 * time.Second)):
		return fmt.Sprintf("pid %d from %s is another process now", pid, file), nil
	}
	return "", nil
}

// lastLine gives the stamp of the newest line on a bus log from sender whose text holds one of has (any case), or zero.
func lastLine(log, from string, has []string, loc *time.Location) (time.Time, error) {
	f, err := os.Open(log)
	if err != nil {
		return time.Time{}, err
	}
	defer f.Close()
	var last time.Time
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		_, text, ok := strings.Cut(sc.Text(), " — ")
		if !ok || len(fields) < 3 || fields[2] != from {
			continue
		}
		low := strings.ToLower(text)
		hit := false
		for _, h := range has {
			hit = hit || strings.Contains(low, strings.ToLower(h))
		}
		if !hit {
			continue
		}
		at, err := time.ParseInLocation("2006-01-02T15:04", fields[0], loc)
		if z, ok := strings.CutSuffix(fields[0], "Z"); ok {
			at, err = time.Parse("2006-01-02T15:04", z)
		}
		if err == nil && at.After(last) {
			last = at
		}
	}
	return last, sc.Err()
}
