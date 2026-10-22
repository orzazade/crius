package ingest

import (
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/orzazade/crius/internal/store"
)

func sha1Hex(b []byte) string {
	s := sha1.Sum(b)
	return hex.EncodeToString(s[:])
}

// datingVersion names the rules that date a log line (store.ParseLine's undated-line steps and logFile's seeding and
// anchoring). Raise it by hand when they change: every log is read again, and the undated lines after each file's
// anchor (its first stored, not late, undated line, which keeps its date) take the new dates.
const datingVersion = "d3" // d3: a stamp ending in Z is UTC

// lineVersion names what a stored log line holds besides its date. Raise it by hand when that changes: every log is
// read again, and its stored rows take the refs the rules give now (Crius 10-17 04:23).
// l1: a VERIFIED line keeps its recipient in refs "to"; l2: several recipients ("a, b"); l3: a bare sender and a line with no "->" (Crius 10-22)
const lineVersion = "l3"

func typesSum() string {
	ts := make([]string, 0, len(store.Types))
	for t := range store.Types {
		ts = append(ts, t)
	}
	sort.Strings(ts)
	return sha1Hex([]byte(strings.Join(ts, " ")))[:12]
}

// born is the file's birth time (macOS), so the first undated line of a rescanned file gets a real date.
func born(path string) (time.Time, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}, err
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Birthtimespec.Sec > 0 {
		return time.Unix(st.Birthtimespec.Sec, st.Birthtimespec.Nsec), nil
	}
	return fi.ModTime(), nil
}

func (r *runner) ingestLogs() {
	dir := filepath.Join(r.home, "bus")
	es, err := os.ReadDir(dir)
	if err != nil {
		r.warn(true, "cannot read %s: %v", dir, err)
	}
	var names []string
	for _, e := range es {
		if strings.HasSuffix(e.Name(), ".log") && !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, n := range names {
		project := strings.TrimSuffix(n, ".log")
		if !store.Slug.MatchString(project) {
			r.warn(true, "bus file %s: its name is not a project slug; not read", n) // warn redacts
			continue
		}
		if err := r.logFile(filepath.Join(dir, n), "bus", true, r.name(project)); err != nil {
			r.warn(true, "cannot ingest %s: %v", n, err)
		}
	}
	for _, p := range r.cfg.projects {
		for _, path := range p.teamLogs {
			if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
				r.warn(true, "team log missing: %s", path)
				continue
			}
			if err := r.logFile(path, "team", false, p.slug); err != nil {
				r.warn(true, "cannot ingest %s: %v", path, err)
			}
		}
	}
}

// logFile reads new lines of a bus file (strict grammar) or a team log (lenient). Undated lines take their date
// from the line before; a new or rescanned file starts from its birth date.
func (r *runner) logFile(path, source string, strict bool, project string) error {
	return r.inTx(func(tx *sql.Tx) error {
		c, err := getCursor(tx, source, path)
		if err != nil {
			return err
		}
		// A cursor keeps the dating rules (and, for the bus, the type list) it was read with; new ones read the
		// file again from the start, and the stored dates follow.
		want := datingVersion + lineVersion
		if strict {
			want += typesSum()
		}
		prev, reread := c, false
		if c.sha != want {
			c, reread = cursor{}, true
		}
		anchored := false // a dated line or a stored date came before in this pass: dates do not rest on the birth
		fresh := 0        // lines past the old cursor: the re-read part is already in coverage.rows
		pathKey := sha1Hex([]byte(path))[:12]
		var st store.State
		if c.state != "" {
			if err := json.Unmarshal([]byte(c.state), &st); err != nil {
				return fmt.Errorf("bad saved state: %v", err)
			}
		}
		next, n, err := scanLines(path, c, func(pos int64, raw []byte) error {
			if pos >= prev.offset {
				fresh++
			}
			if pos == 0 { // a new, truncated or replaced file: the saved date state does not apply
				b, err := born(path)
				if err != nil {
					return err
				}
				st = store.State{Date: b.Format("2006-01-02")}
				anchored = false
			}
			line := strings.TrimRight(strings.ToValidUTF8(string(raw), "�"), "\r\n")
			if strings.TrimSpace(line) == "" || !strict && strings.HasPrefix(line, "#") {
				return nil
			}
			key := fmt.Sprintf("%s:%s:%d:%s", source, pathKey, pos, sha1Hex(raw)[:12])
			p := store.ParseLine(line, &st, strict)
			if p == nil {
				r.warn(false, "malformed line in %s at byte %d", path, pos)
				// Stamped when read: the goal report counts the lines not read since the last report.
				return r.event(tx, event{kind: "malformed", key: key, project: project, ts: utcNow()})
			}
			if !p.DatedByGuess {
				anchored = true
			} else if !anchored && !p.Late {
				// The first undated line the store holds keeps its date, and the lines after it date from there: a
				// re-read must not move a file to a new birth date (a replaced file's birth is not its lines' date).
				// A late line is no anchor: its date is not the state's.
				var stored sql.NullString
				// A malformed row's time is when it was read, not the line's date.
				err := tx.QueryRow("select ts from event where source_key=? and kind <> 'malformed'", key).Scan(&stored)
				if err != nil && err != sql.ErrNoRows {
					return err
				}
				if stored.Valid {
					t, err := time.Parse("2006-01-02T15:04:05Z", stored.String)
					if err != nil {
						return err
					}
					p.Date = t.In(time.Local).Format("2006-01-02")
					st.Date, anchored = p.Date, true
				}
			}
			t, err := time.ParseInLocation("2006-01-02T15:04", p.Date+"T"+p.Time, time.Local)
			if err != nil {
				return err
			}
			if !p.At.IsZero() { // a Z stamp: its instant, which a repeated local hour loses
				t = p.At
			}
			bare := p.Project == "" // a bare sender ("QA2") is a role on this log's bus, lower-cased unless it is redacted
			if bare {
				p.Project = project
			}
			writer := p.Project + ":" + p.Role
			n := r.name(writer) // once: each call counts a redaction
			if bare && strict && n == writer {
				p.Role = strings.ToLower(p.Role)
				writer = p.Project + ":" + p.Role
				n = r.name(writer)
			}
			if !p.DatedByGuess {
				if err := r.checkClock(tx, path, pathKey, p, t, &st); err != nil {
					return err
				}
			}
			e := event{ts: t.UTC().Format("2006-01-02T15:04:05Z"), kind: strings.ToLower(p.Type), key: key,
				project: project, actor: p.Role, text: p.Text, hasText: true}
			switch { // redacted as one name: split, password:hunter2 kept its value
			case n == writer:
				e.project = p.Project
			case strings.HasPrefix(n, p.Project+":"): // the role was redacted: the writer is no one (not qa-…)
				e.project, e.actor = p.Project, "[redacted]"
			default: // the project part too: the row stays under its log's project
				e.actor = "[redacted]"
			}
			if len(p.Refs) > 0 {
				e.refs = p.Refs
			}
			if p.Type == "VERIFIED" { // the recipient: the digest counts a VERIFIED line for the lead it is to (RULES 3.4)
				e.to = p.To
				if e.to == "" { // no "->": to all on this bus
					e.to = project + ":all"
				}
			}
			if items := p.Refs["item"]; len(items) > 0 {
				e.item = items[0]
			}
			if err := r.event(tx, e); err != nil {
				return err
			}
			if !p.DatedByGuess {
				return nil // its date is written on the line
			}
			// A line read before keeps its row; its guessed date follows this read (newer dating rules or type list).
			_, err = tx.Exec("update event set ts=? where source_key=? and ts is not ?", e.ts, key, e.ts)
			return err
		})
		if err != nil {
			return err
		}
		next.sha = want
		if reread && next.inode == prev.inode && next.offset >= prev.offset {
			n = fresh
		}
		if next == c {
			return nil
		}
		b, err := json.Marshal(st)
		if err != nil {
			return err
		}
		next.state = string(b)
		return saveCursor(tx, source, path, next, n)
	})
}

// checkClock: bus stamps are local time, UTC ends in Z (§6). A dated line more than an hour behind the newest stamp
// before it in the file (a writer stamping UTC without Z), or more than 10 minutes ahead of the clock, queues an alert,
// one per file, writer, day and kind; the line keeps its stamp. A writer flagged ahead that day, on this read or an
// earlier one (its alert is stored), does not move the newest stamp, so the good lines after it are not blamed.
func (r *runner) checkClock(tx *sql.Tx, path, pathKey string, p *store.Line, t time.Time, st *store.State) error {
	writer := p.Project + ":" + p.Role
	stamp := p.Date + "T" + p.Time
	// The key names the writer by hash: a role is any word and can be a token.
	key := func(kind string) string {
		return "bus-clock:" + kind + ":" + pathKey + ":" + sha1Hex([]byte(writer))[:12] + ":" + p.Date
	}
	alert := func(kind, why string) error {
		// Built from redacted parts, so not redacted again (that would take token-service:lead for a key); cut to
		// fit a bus line, as queueAlert does.
		_, err := tx.Exec("insert or ignore into alert (key, ts, text) values (?,?,?)", key(kind), utcNow(),
			fit(fmt.Sprintf("bus clock: %s line %s in %s is %s; stamps are local time, UTC ends in Z; it keeps its stamp",
				r.name(writer), stamp, r.text(path), why)))
		return err
	}
	if ahead := t.Sub(clock()); ahead > 10*time.Minute {
		return alert("ahead", span(ahead)+" ahead of the clock")
	}
	if st.Newest != "" {
		newest, err := time.Parse(time.RFC3339, st.Newest)
		if err != nil {
			return fmt.Errorf("bad saved newest stamp %q: %v", st.Newest, err)
		}
		if behind := newest.Sub(t); behind > time.Hour {
			return alert("behind", fmt.Sprintf("%s behind the newest stamp before it (%s)", span(behind),
				newest.Local().Format("2006-01-02T15:04")))
		}
		if !t.After(newest) {
			return nil
		}
	}
	var seen int
	if err := tx.QueryRow("select count(*) from alert where key = ?", key("ahead")).Scan(&seen); err != nil {
		return err
	}
	if seen == 0 {
		st.Newest = t.UTC().Format(time.RFC3339)
	}
	return nil
}

// span is a duration in whole minutes: 3h56m, 15m.
func span(d time.Duration) string {
	return strings.TrimSuffix(d.Truncate(time.Minute).String(), "0s")
}
