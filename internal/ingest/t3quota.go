package ingest

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/launch"
	"github.com/orzazade/crius/internal/quota"
	"github.com/orzazade/crius/internal/sessionstate"
)

// T3 Code threads run Claude Code with no statusline, so crius-statusline writes no quota sample for them (11-21: every
// role moved to T3 threads and max2 read "room" for 6 h on an old sample). T3 logs each Claude Code rate_limit_event it
// decodes in its provider logs, $T3CODE_HOME/userdata/logs/provider/events.<thread>.log (rotated to .log.N): the seven-day
// share as a fraction, its reset, the session id, and in providerSessionId the T3 provider instance that ran it.
// ingestT3Quota files each as a quota sample of that session's login.
//
// The login comes from two records that must agree, else the sample is skipped:
//   - the line's own provider instance, whose config folder T3's settings.json names (launch.T3Instances): the folder at
//     the time of the line, as each change of instance starts a new session id;
//   - the session's crius-hook record (sessions/<id>.json Config, the CLAUDE_CONFIG_DIR of its own process), written
//     from inside T3 (Thread set): a record written outside T3 means the session was resumed elsewhere.
//
// Both folders must be in one accounts.toml Max row signed into a login. The transcript's folder cannot tell the login:
// every Max folder on the Mac links its projects/ to one of two shared folders (11-22: each of the 70 sessions in the logs
// had its transcript under 2, 3 or 5 listed folders). Every sample not placed is skipped and counted by reason
// (Stats.T3Skipped, one warning per run), never filed under a guessed login.
//
// Each file's cursor is kept by its inode, so a rotation (each file renamed one up) reads only the new file; the sha of
// its first line tells a new file on a reused inode. A sample's key is its session and event uuid, so a re-read adds
// nothing twice. Nothing under $T3CODE_HOME is written.

// t3LogName is a provider log or one of its rotations.
var t3LogName = regexp.MustCompile(`^events\..+\.log(\.[0-9]+)?$`)

// t3Home is T3 Code's home: $T3CODE_HOME (~ expanded), default ~/.t3, as crius-spawn finds T3's settings.
func t3Home() (string, error) {
	h := os.Getenv("T3CODE_HOME")
	home, err := os.UserHomeDir()
	switch {
	case h != "" && h != "~" && !strings.HasPrefix(h, "~/"):
		return h, nil
	case err != nil:
		return "", err
	case h == "":
		return filepath.Join(home, ".t3"), nil
	}
	return filepath.Join(home, h[1:]), nil
}

func (r *runner) ingestT3Quota() {
	t3, err := t3Home()
	if err != nil {
		r.warn(true, "T3 quota logs: %v", err)
		return
	}
	if _, err := os.Stat(t3); errors.Is(err, fs.ErrNotExist) {
		return // no T3 Code on this machine: no thread to sample
	}
	dir := filepath.Join(t3, "userdata", "logs", "provider")
	entries, err := os.ReadDir(dir)
	if err != nil { // T3 is here but not its logs: said, never "nothing found". Logs read before and now gone are a failed look
		read := true
		if errors.Is(err, fs.ErrNotExist) {
			if qerr := r.inTx(func(tx *sql.Tx) error {
				return tx.QueryRow("select exists (select 1 from coverage where source = 't3quota')").Scan(&read)
			}); qerr != nil {
				r.warn(true, "T3 quota logs: %v", qerr)
			}
		}
		r.warn(read, "T3 quota logs: %v", err)
		return
	}
	// Until both are read no sample can be placed: the cursors wait.
	as, err := quota.LoadAccounts(r.home)
	if err == nil && len(as.Rows) == 0 {
		err = fmt.Errorf("%s lists no account", filepath.Join(r.home, "accounts.toml"))
	}
	if err != nil {
		r.warn(true, "T3 quota logs not read: %v", err)
		return
	}
	insts, err := launch.T3Instances(filepath.Join(t3, "userdata", "settings.json"))
	if err != nil {
		r.warn(true, "T3 quota logs not read: T3 provider instances: %v", err)
		return
	}
	pl := &t3Placer{dir: filepath.Join(r.home, "sessions"), as: as, insts: insts, seen: map[string]t3Place{}}
	skipped := map[string]int{}
	for _, e := range entries {
		if !e.Type().IsRegular() || !t3LogName.MatchString(e.Name()) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		var f t3File
		if err := r.inTx(func(tx *sql.Tx) error {
			f = t3File{skipped: map[string]int{}}
			return r.t3QuotaFile(tx, path, pl, &f)
		}); err != nil {
			r.warn(true, "cannot ingest %s: %v", path, err)
			continue
		}
		r.st.T3Samples += f.added
		r.st.BadLines += len(f.bad)
		for _, b := range f.bad {
			r.warn(true, "%s", b)
		}
		for k, n := range f.skipped {
			skipped[k] += n
		}
	}
	if len(skipped) == 0 {
		return
	}
	r.st.T3Skipped = skipped
	n, reasons := 0, make([]string, 0, len(skipped))
	for k, v := range skipped {
		n += v
		reasons = append(reasons, fmt.Sprintf("%s: %d", k, v))
	}
	sort.Strings(reasons)
	r.warn(false, "T3 quota: %d samples filed, %d skipped, no login known (%s)", r.st.T3Samples, n, strings.Join(reasons, ", "))
}

// t3File is what one file's read found; it counts only once its transaction commits.
type t3File struct {
	added   int
	skipped map[string]int
	bad     []string
}

// t3Line is the part of a provider log line a rate_limit_event sample needs.
type t3Line struct {
	Session string `json:"providerSessionId"`
	Event   struct {
		Payload struct {
			Type      string `json:"type"`
			UUID      string `json:"uuid"`
			SessionID string `json:"session_id"`
			Info      struct {
				Type    string `json:"rateLimitType"`
				Windows map[string]struct {
					Utilization *float64 `json:"utilization"`
					ResetsAt    *int64   `json:"resetsAt"`
				} `json:"unifiedWindows"`
			} `json:"rate_limit_info"`
		} `json:"payload"`
	} `json:"event"`
}

var (
	rateLimitEvent = []byte(`"rate_limit_event"`)
	// t3Instance is the provider instance in a providerSessionId: provider-session:provider-instance:<id>:thread:…
	t3Instance = regexp.MustCompile(`^provider-session:provider-instance:([^:]+):`)
)

// firstLine is the sha of a file's first whole line, "" while it has none.
func firstLine(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	l, err := bufio.NewReader(f).ReadBytes('\n')
	if err != nil { // io.EOF: no whole line yet
		return "", nil
	}
	return sha1Hex(l), nil
}

func (r *runner) t3QuotaFile(tx *sql.Tx, path string, pl *t3Placer, f *t3File) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	ino := inodeOf(fi)
	head, err := firstLine(path)
	if err != nil {
		return err
	}
	resource := fmt.Sprintf("inode %d", ino)
	c, err := getCursor(tx, "t3quota", resource)
	if err != nil {
		return err
	}
	if c.sha != head { // a new file on an inode used before, or a first line written since
		c = cursor{}
	}
	c.inode = ino
	next, n, err := scanLines(path, c, func(pos int64, raw []byte) error {
		if !bytes.Contains(raw, rateLimitEvent) {
			return nil
		}
		bad := func(why string) error {
			f.bad = append(f.bad, fmt.Sprintf("bad T3 rate_limit_event in %s at byte %d: %s", path, pos, why))
			return nil
		}
		// "[<RFC 3339 time>] <tag>: <json>": the time T3 decoded the event.
		end, brace := bytes.IndexByte(raw, ']'), bytes.IndexByte(raw, '{')
		if raw[0] != '[' || end < 0 || brace < end {
			return bad("no [time] before the JSON")
		}
		at, err := time.Parse(time.RFC3339Nano, string(raw[1:end]))
		if err != nil {
			return bad("bad time")
		}
		var l t3Line
		if err := json.Unmarshal(raw[brace:], &l); err != nil {
			return bad("not JSON") // a line T3 cut short, or another shape
		}
		p := l.Event.Payload
		if p.Type != "rate_limit_event" {
			return nil // another event that only names it (a message quoting the word)
		}
		w, ok := p.Info.Windows["seven_day"]
		switch {
		case !ok && p.Info.Type != "" && p.Info.Type != "seven_day":
			f.skipped["not a seven-day event"]++ // another window's event: it gives no weekly share
			return nil
		case !ok || w.Utilization == nil || w.ResetsAt == nil || *w.Utilization < 0 || *w.ResetsAt <= 0:
			return bad("no seven_day utilization and resetsAt")
		}
		if _, err := sessionstate.Path(pl.dir, p.SessionID); err != nil {
			return bad("session id is not a plain name")
		}
		inst := ""
		if m := t3Instance.FindStringSubmatch(l.Session); m != nil {
			inst = m[1]
		}
		place := pl.place(p.SessionID, inst, r)
		if place.skip != "" {
			f.skipped[place.skip]++
			return nil
		}
		id := p.UUID
		if id == "" {
			id = sha1Hex(raw)
		}
		key := "t3quota:" + sha1Hex([]byte(p.SessionID + " " + id))[:16]
		// A fraction: 0.15 is 15%. Whole percent, as the statusline gives it, so one share from both sources is one value.
		pct := math.Round(*w.Utilization * 100)
		res, err := tx.Exec("insert or ignore into quota (source_key, ts, account, session, persona, epoch, five_hour_pct,"+
			" five_reset, seven_day_pct, sample_kind, login) values (?,?,'max',?,null,?,null,null,?,'t3',?)", key,
			at.UTC().Format(time.RFC3339), r.text(p.SessionID), *w.ResetsAt, pct, r.text(place.login))
		if err != nil {
			return err
		}
		if k, err := res.RowsAffected(); err != nil {
			return err
		} else if k > 0 {
			f.added++
		}
		// The event comes with a response: its values are new.
		if _, err = tx.Exec("insert or ignore into quota_response (source_key, responded) values (?,1)", key); err != nil {
			return err
		}
		// It is the session's account line too, as a statusline line is. T3 names no persona: the report takes the session's.
		_, err = tx.Exec("insert or ignore into account_seen (source_key, session, persona, ts, rl, login) values (?,?,null,?,1,?)",
			key, r.text(p.SessionID), at.UTC().Format(time.RFC3339), r.text(place.login))
		return err
	})
	if err != nil {
		return err
	}
	if next.inode != ino { // the path now holds another file (rotated while read): its rows are in, its cursor next run
		return nil
	}
	next.sha = head
	if next == c {
		return nil
	}
	return saveCursor(tx, "t3quota", resource, next, n)
}

// t3Placer gives each session id and provider instance its login, read once per run.
type t3Placer struct {
	dir   string // crius-hook's session records
	as    *quota.Accounts
	insts map[string]string // T3 provider instance -> its config folder
	seen  map[string]t3Place
}

// t3Place is a session's login, or why it has none (skip).
type t3Place struct{ login, skip string }

func (pl *t3Placer) place(sid, inst string, r *runner) t3Place {
	k := sid + " " + inst
	if p, ok := pl.seen[k]; ok {
		return p
	}
	p := pl.read(sid, inst, r)
	pl.seen[k] = p
	return p
}

func (pl *t3Placer) read(sid, inst string, r *runner) t3Place {
	if inst == "" {
		return t3Place{skip: "line names no T3 provider instance"}
	}
	folder, ok := pl.insts[inst]
	if !ok {
		return t3Place{skip: "T3 provider instance not in T3's settings"}
	}
	s, err := sessionstate.Read(pl.dir, sid)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return t3Place{skip: "no crius-hook record"}
	case err != nil:
		r.warn(false, "T3 quota: crius-hook record of %s: %v", sid, err)
		return t3Place{skip: "unreadable crius-hook record"}
	case s.Thread == "":
		return t3Place{skip: "newest crius-hook record is outside T3"}
	case s.Config == "":
		return t3Place{skip: "crius-hook record names no config folder"}
	}
	a, b := pl.as.Dir(folder), pl.as.Dir(s.Config)
	switch {
	case a == nil || b == nil:
		return t3Place{skip: "folder not in accounts.toml"}
	case a != b:
		return t3Place{skip: "T3 instance and crius-hook record on two accounts"}
	case a.Plan != "max":
		return t3Place{skip: "folder of a row that is not Max"}
	case a.Login == "":
		return t3Place{skip: "folder signed into no login"}
	}
	return t3Place{login: a.Login}
}
