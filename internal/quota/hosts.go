package quota

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// RemoteHosts are the machines besides the Mac whose statusline spool the sign-in sweep (wakewatch.Signins) reads over
// ssh every run. The sweep keeps a copy of each in the Mac's spool/hosts/, so Pick reads the same lines with no ssh of
// its own (11-21: the sweep saw max3 at 96% on the Legion while Pick, reading the Mac's store only, called it unknown).
var RemoteHosts = []string{"legion", "thinkpad"}

// HostStale is the age past which a machine's copy is said to be old: the sweep runs every 5 minutes.
const HostStale = 20 * time.Minute

func hostDir(home string) string { return filepath.Join(home, "spool", "hosts") }

// HostSpoolPath is the copy of host's spool the sweep last read; its time is when that read was.
func HostSpoolPath(home, host string) string { return filepath.Join(hostDir(home), host+".jsonl") }

// hostErrPath holds why the sweep's last read of host failed; it is newer than the copy while reads fail.
func hostErrPath(home, host string) string { return filepath.Join(hostDir(home), host+".err") }

// SaveHostSpool keeps what the sweep read of host at now: its spool lines (b) when readErr is nil, else why it could not
// be read, with the older copy left in place (its age then shows how old the lines are). Written to a temp file of its
// own and renamed, so a reader never sees half a copy, also when two sweeps save at once.
func SaveHostSpool(home, host string, b []byte, readErr error, now time.Time) error {
	if err := os.MkdirAll(hostDir(home), 0o700); err != nil {
		return err
	}
	path, data := HostSpoolPath(home, host), b
	if readErr != nil {
		path, data = hostErrPath(home, host), []byte(readErr.Error())
	}
	f, err := os.CreateTemp(hostDir(home), host+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chtimes(tmp, now, now)
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if readErr == nil {
		if err := os.Remove(hostErrPath(home, host)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// HostLines reads the sweep's copies of hosts' spools: their lines, one note per host saying how old its copy is, and
// blind, the hosts whose lines may be missing or old (copy missing, unreadable or OLD, or last read failed). A host never read, whose last read
// failed, whose copy is older than HostStale or that holds lines that are not JSON is said in its note in capitals, so a
// Pick made blind to a machine says so (never a quiet "unknown").
func HostLines(home string, hosts []string, now time.Time) (lines []Line, notes []string, blind []string) {
	for _, h := range hosts {
		b, copyAt, err := readCopy(HostSpoolPath(home, h))
		if err != nil {
			notes = append(notes, fmt.Sprintf("%s spool UNREAD (copy: %v)", h, err))
			blind = append(blind, h)
			continue
		}
		age := now.Sub(copyAt)
		note := fmt.Sprintf("%s spool read %s ago", h, roundAge(age))
		switch {
		case copyAt.IsZero():
			note = fmt.Sprintf("%s spool NEVER READ (no copy at %s)", h, HostSpoolPath(home, h))
			blind = append(blind, h)
		case age > HostStale:
			note = fmt.Sprintf("%s spool copy OLD: read %s ago", h, roundAge(age))
			blind = append(blind, h)
		}
		if fi, err := os.Stat(hostErrPath(home, h)); err == nil && fi.ModTime().After(copyAt) {
			why, _ := os.ReadFile(hostErrPath(home, h))
			note += fmt.Sprintf("; UNREAD since %s: %s", fi.ModTime().Local().Format("01-02 15:04"), clip(strings.TrimSpace(string(why)), 80))
			if !slices.Contains(blind, h) {
				blind = append(blind, h)
			}
		}
		ls, bad := ParseSpool(b)
		if bad > 0 {
			note += fmt.Sprintf("; %d lines NOT JSON skipped", bad)
		}
		lines = append(lines, ls...)
		notes = append(notes, note)
	}
	return lines, notes, blind
}

// readCopy is a copy's lines and its time, read from one open file so the two match; nothing and a zero time when the
// sweep has made none.
func readCopy(path string) ([]byte, time.Time, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, time.Time{}, nil
	} else if err != nil {
		return nil, time.Time{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, time.Time{}, err
	}
	b, err := io.ReadAll(f)
	return b, fi.ModTime(), err
}

func roundAge(d time.Duration) string {
	if d < time.Minute {
		return "under 1 min"
	}
	return ago(d)
}

// ParseSpool parses a statusline spool. A last line with no newline is being written and is left. A line that is not
// JSON (a write cut short when the disk filled, and joined to the next) is skipped and counted, as ingest does: one bad
// line must not blind the whole machine for good. Only the count is ever said, never the line.
func ParseSpool(b []byte) (out []Line, bad int) {
	if i := bytes.LastIndexByte(b, '\n'); i < len(b)-1 {
		b = b[:i+1]
	}
	for _, ln := range bytes.Split(b, []byte("\n")) {
		if len(bytes.TrimSpace(ln)) == 0 {
			continue
		}
		var l Line
		if err := json.Unmarshal(ln, &l); err != nil {
			bad++
			continue
		}
		out = append(out, l)
	}
	return out, bad
}

// WithHosts is r read with the other machines' week (HostLines of RemoteHosts at r.Now, then WithLines): the one reading
// Pick, the PAUSE guard and quota --room take (11-21: the guard and --room read the Mac's store alone and said room for
// an account at 96% on the Legion). notes and blind are HostLines'.
func (r *Report) WithHosts(home string, as *Accounts) (rep *Report, notes, blind []string) {
	lines, notes, blind := HostLines(home, RemoteHosts, r.Now)
	return r.WithLines(lines, as), notes, blind
}

// WithLines is r with each login's window read from its store lines and lines together, by the rule Weeks uses (weekOf):
// the latest window a measurement on any machine shows, its highest share. lines are other machines' spool lines
// (HostLines); the Mac's own are in the store already. r comes from Build, which keeps each login's store lines; as names
// a login only measured elsewhere. Every other figure of r stays the store's.
func (r *Report) WithLines(lines []Line, as *Accounts) *Report {
	extra := lineSamples(lines)
	if len(extra) == 0 {
		return r
	}
	v := *r
	v.Accounts = slices.Clone(r.Accounts)
	v.byLogin = maps.Clone(r.byLogin)
	if v.byLogin == nil {
		v.byLogin = map[string][]sample{}
	}
	at := map[string]int{}
	for i, a := range v.Accounts {
		at[a.Login] = i
	}
	for _, login := range slices.Sorted(maps.Keys(extra)) {
		ss := append(slices.Clone(v.byLogin[login]), extra[login]...)
		v.byLogin[login] = ss
		u := weekOf(ss, r.Now).AcctUse
		u.Name = "login " + login
		if as != nil {
			u.Name = as.Name(login)
		}
		if i, ok := at[login]; ok {
			v.Accounts[i] = u
		} else {
			v.Accounts = append(v.Accounts, u)
		}
	}
	sort.SliceStable(v.Accounts, func(i, j int) bool { // as Build orders them: listed rows first, then by name
		a, b := v.Accounts[i], v.Accounts[j]
		if ua, ub := as.Of(a.Login) == nil, as.Of(b.Login) == nil; ua != ub {
			return ub
		}
		return a.Name < b.Name
	})
	v.restale()
	return &v
}

// restale judges the logins' Stale entries again with every machine's samples (loginStale): a login measured on another
// machine within the gap is not stale. Persona entries stay as Build found them (they are of the Mac's statusline).
func (v *Report) restale() {
	if v.personaStale {
		return
	}
	var keep []string
	for _, st := range v.Stale {
		if _, login := v.StaleProjects[st]; !login {
			keep = append(keep, st)
		}
	}
	v.Stale, v.StaleAt, v.StaleProjects = keep, maps.Clone(v.StaleAt), nil
	v.Reasons = slices.DeleteFunc(slices.Clone(v.Reasons), func(r string) bool { return strings.HasPrefix(r, staleReason) })
	newest := map[string]time.Time{}
	for login, ss := range v.byLogin {
		ss = slices.Clone(ss)
		slices.SortStableFunc(ss, func(a, b sample) int { return a.ts.Compare(b.ts) })
		if cur, u := reduce(ss, v.Now); len(cur) > 0 && u.Ended.IsZero() {
			newest[login] = cur[len(cur)-1].ts
		}
	}
	v.loginStale(newest)
	for _, st := range v.Stale {
		v.Reasons = append(v.Reasons, staleReason+st)
	}
	v.Status = "complete" // as Build sets it
	if len(v.Reasons) > 0 {
		v.Status = "partial"
	}
}
