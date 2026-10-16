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
		if err := r.logFile(filepath.Join(dir, n), "bus", true, strings.TrimSuffix(n, ".log")); err != nil {
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
		// A bus cursor keeps the type list it was read with; a new list reads the file again from the start.
		var types string
		prev, reread := c, false
		if strict {
			types = typesSum()
			if c.sha != types {
				c, reread = cursor{}, true
			}
		}
		anchored := false // a dated line came before in this pass, so undated dates do not rest on the birth date
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
				st = store.State{Date: b.Format("2006-01-02"), Time: "00:00"}
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
				return r.event(tx, event{kind: "malformed", key: key, project: project})
			}
			t, err := time.ParseInLocation("2006-01-02T15:04", p.Date+"T"+p.Time, time.Local)
			if err != nil {
				return err
			}
			e := event{ts: t.UTC().Format("2006-01-02T15:04:05Z"), kind: strings.ToLower(p.Type), key: key,
				project: project, actor: p.Role, text: p.Text, hasText: true}
			if p.Project != "" {
				e.project = p.Project
			}
			if len(p.Refs) > 0 {
				e.refs = p.Refs
			}
			if items := p.Refs["item"]; len(items) > 0 {
				e.item = items[0]
			}
			if err := r.event(tx, e); err != nil {
				return err
			}
			if !p.DatedByGuess {
				anchored = true
				return nil
			}
			if !reread || !anchored {
				return nil
			}
			// A type-list re-read can date an undated line differently (a line before it parses now); the stored
			// row follows when a dated line anchors it. A date that rests on the birth date stays as stored: a
			// replaced file's birth is not its lines' date.
			_, err = tx.Exec("update event set ts=? where source_key=? and ts is not ?", e.ts, key, e.ts)
			return err
		})
		if err != nil {
			return err
		}
		if strict {
			next.sha = types
		}
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
