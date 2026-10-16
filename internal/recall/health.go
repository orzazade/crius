package recall

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

// Expected is what one ingest run should read (from projects.toml and the folders it names): files per coverage
// source, and the teams whose leads must post (projects with a bus file or team logs).
type Expected struct {
	Files    map[string][]string
	Teams    []string
	Problems []string // what could not be listed (an unreadable root or folder)
}

// silentAfter is how long a team with active sessions may go without a bus or team-log line (design §5).
const silentAfter = 3 * time.Hour

// backupAge is the newest backup's limit: the backup runs daily after 03:00, so 26 h allows for a late run.
const backupAge = 26 * time.Hour

type SourceHealth struct {
	Name     string   `json:"name"`
	Expected int      `json:"expected"` // -1: no expectation for this source
	Seen     int      `json:"seen"`     // expected resources with a coverage row (all rows when Expected is -1)
	Newest   string   `json:"newest,omitempty"`
	Missing  []string `json:"missing,omitempty"` // expected, older than the last run, never read
	Behind   []string `json:"behind,omitempty"`  // complete lines older than the last run, not read
}

type TeamHealth struct {
	Project  string `json:"project"`
	LastLine string `json:"last_line,omitempty"` // newest bus or team-log line
	LastCall string `json:"last_call,omitempty"` // newest API call of the project's sessions
	Silent   bool   `json:"silent"`
}

type Health struct {
	Status      string         `json:"status"`
	Problems    []string       `json:"problems,omitempty"`
	LastRun     string         `json:"last_run"`
	Runs24h     int            `json:"runs_24h"`
	Failed24h   int            `json:"not_complete_24h"`
	Heartbeat   string         `json:"heartbeat"`
	Backup      string         `json:"backup"`
	RestoreTest string         `json:"restore_test"`
	Sources     []SourceHealth `json:"sources"`
	Teams       []TeamHealth   `json:"teams"`
}

func short(d time.Duration) string {
	if s := d.Round(time.Minute).String(); s != "0s" {
		return strings.TrimSuffix(s, "0s")
	}
	return "0m"
}

// CheckHealth reports per source and per resource (design §4 `health`). Any problem makes it partial.
func CheckHealth(db *sql.DB, home string, exp Expected, now time.Time) (*Health, error) {
	h := &Health{RestoreTest: "never run (the weekly restore-after-expiry test comes with step 6)"}
	status, reasons, err := Freshness(db, now)
	if err != nil {
		return nil, err
	}
	if status != "complete" {
		h.Problems = append(h.Problems, reasons...)
	}
	h.Problems = append(h.Problems, exp.Problems...)
	var started, finished, runStatus sql.NullString
	var id int
	err = db.QueryRow("select id, started, finished, status from source_run order by id desc limit 1").Scan(&id, &started,
		&finished, &runStatus)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	runStart := time.Time{}
	if err == nil {
		h.LastRun = fmt.Sprintf("run %d %s, finished %s", id, runStatus.String, finished.String)
		if runStart, err = time.Parse(time.RFC3339, started.String); err != nil {
			return nil, fmt.Errorf("run %d has a bad start time %q", id, started.String)
		}
	}
	if err := db.QueryRow("select count(*), count(*) filter (where status <> 'complete') from source_run where started >= ?",
		now.Add(-24*time.Hour).UTC().Format(time.RFC3339)).Scan(&h.Runs24h, &h.Failed24h); err != nil {
		return nil, err
	}
	h.heartbeat(home, now)
	if err := h.backup(home, now); err != nil {
		return nil, err
	}
	if err := h.sources(db, exp, runStart); err != nil {
		return nil, err
	}
	if err := h.teams(db, exp, now); err != nil {
		return nil, err
	}
	h.Status = "complete"
	if len(h.Problems) > 0 {
		h.Status = "partial"
	}
	return h, nil
}

func (h *Health) heartbeat(home string, now time.Time) {
	b, err := os.ReadFile(filepath.Join(home, "heartbeat"))
	if err != nil {
		h.Heartbeat = "none"
		h.Problems = append(h.Problems, fmt.Sprintf("no heartbeat (%v)", err))
		return
	}
	stamp := strings.TrimSpace(string(b))
	t, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		h.Heartbeat = stamp
		h.Problems = append(h.Problems, fmt.Sprintf("heartbeat unreadable: %q", stamp))
		return
	}
	age := now.Sub(t)
	h.Heartbeat = fmt.Sprintf("%s (%s old)", stamp, short(age))
	if age > maxAge {
		h.Problems = append(h.Problems, fmt.Sprintf("heartbeat %s old (the watchdog alerts after %s)", short(age), short(maxAge)))
	}
}

func (h *Health) backup(home string, now time.Time) error {
	names, err := filepath.Glob(filepath.Join(home, "backup", "record-*.db.gz"))
	if err != nil {
		return err
	}
	var newest os.FileInfo
	for _, n := range names {
		fi, err := os.Stat(n)
		if err != nil {
			return err
		}
		if newest == nil || fi.ModTime().After(newest.ModTime()) {
			newest = fi
		}
	}
	if newest == nil {
		h.Backup = "none"
		h.Problems = append(h.Problems, "no backup in "+filepath.Join(home, "backup"))
		return nil
	}
	age := now.Sub(newest.ModTime())
	h.Backup = fmt.Sprintf("%s (%s old)", newest.Name(), short(age))
	if age > backupAge {
		h.Problems = append(h.Problems, fmt.Sprintf("newest backup %s is %s old", newest.Name(), short(age)))
	}
	return nil
}

// sources compares what a run should read with the coverage rows. A file is only judged when it is older than the
// last run's start: newer data belongs to the next run.
func (h *Health) sources(db *sql.DB, exp Expected, runStart time.Time) error {
	type cov struct {
		inode, offset int64
		lastOK        string
	}
	seen := map[string]map[string]cov{}
	rows, err := db.Query("select source, resource, coalesce(inode, 0), offset, coalesce(last_ok,'') from coverage")
	if err != nil {
		return err
	}
	for rows.Next() {
		var src, res string
		var c cov
		if err := rows.Scan(&src, &res, &c.inode, &c.offset, &c.lastOK); err != nil {
			rows.Close()
			return err
		}
		if seen[src] == nil {
			seen[src] = map[string]cov{}
		}
		seen[src][res] = c
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	var names []string
	for s := range seen {
		names = append(names, s)
	}
	for s := range exp.Files {
		names = append(names, s)
	}
	slices.Sort(names)
	for _, name := range slices.Compact(names) {
		sh := SourceHealth{Name: name, Expected: -1}
		for _, c := range seen[name] {
			if c.lastOK > sh.Newest {
				sh.Newest = c.lastOK
			}
		}
		files, expected := exp.Files[name]
		if !expected {
			sh.Seen = len(seen[name])
		} else {
			sh.Expected = len(files)
			for _, f := range files {
				c, ok := seen[name][f]
				if ok {
					sh.Seen++
				}
				fi, err := os.Stat(f)
				if err != nil {
					h.Problems = append(h.Problems, fmt.Sprintf("%s: cannot read %s: %v", name, f, err))
					continue
				}
				if runStart.IsZero() || !fi.ModTime().Before(runStart) {
					continue
				}
				// A replaced file (another inode) is read from its start, as the ingest does.
				if st, isUnix := fi.Sys().(*syscall.Stat_t); isUnix && c.inode != 0 && int64(st.Ino) != c.inode {
					c.offset = 0
				}
				switch {
				case !ok:
					sh.Missing = append(sh.Missing, f)
				case fi.Mode().IsRegular() && fi.Size() > c.offset:
					full, err := hasNewline(f, c.offset)
					if err != nil {
						return err
					}
					if full {
						sh.Behind = append(sh.Behind, f)
					}
				}
			}
		}
		if len(sh.Missing) > 0 {
			h.Problems = append(h.Problems, fmt.Sprintf("%s: %d never read, e.g. %s", name, len(sh.Missing), sh.Missing[0]))
		}
		if len(sh.Behind) > 0 {
			h.Problems = append(h.Problems, fmt.Sprintf("%s: %d behind (complete lines the last run did not read), e.g. %s",
				name, len(sh.Behind), sh.Behind[0]))
		}
		h.Sources = append(h.Sources, sh)
	}
	return nil
}

// hasNewline says whether the bytes after offset hold a complete line (a partial last line waits by design).
func hasNewline(path string, offset int64) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	buf := make([]byte, 64<<10)
	for off := offset; ; {
		n, err := f.ReadAt(buf, off)
		if bytes.IndexByte(buf[:n], '\n') >= 0 {
			return true, nil
		}
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		off += int64(n)
	}
}

// teams marks a lead silent: the team's sessions made an API call within 3 h, but no bus or team-log line came.
func (h *Health) teams(db *sql.DB, exp Expected, now time.Time) error {
	since := stamp(now.Add(-silentAfter))
	for _, p := range exp.Teams {
		t := TeamHealth{Project: p}
		var line, call sql.NullString
		if err := db.QueryRow("select max("+norm+") from event where project = ? and (source_key like 'bus:%' or"+
			" source_key like 'team:%')", p).Scan(&line); err != nil {
			return err
		}
		if err := db.QueryRow("select max(r.ts_last) from request r join session s on s.id = r.session where s.project = ?",
			p).Scan(&call); err != nil {
			return err
		}
		t.LastLine, t.LastCall = line.String, call.String
		t.Silent = call.String >= since && line.String < since
		if t.Silent {
			last := "never"
			if line.Valid {
				last = "since " + line.String
			}
			h.Problems = append(h.Problems, fmt.Sprintf("lead silent: %s has sessions active (last call %s) but no bus or "+
				"team-log line %s", p, call.String, last))
		}
		h.Teams = append(h.Teams, t)
	}
	return nil
}

// Text is the answer for people: status, each problem, then the per-source and per-team lines.
func (h *Health) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s · health", h.Status)
	if len(h.Problems) > 0 {
		fmt.Fprintf(&b, " (%d problems)", len(h.Problems))
	}
	b.WriteString("\n")
	for _, p := range h.Problems {
		b.WriteString("PROBLEM " + p + "\n")
	}
	run := h.LastRun
	if run == "" {
		run = "no run recorded"
	}
	fmt.Fprintf(&b, "ingest %s · last 24 h: %d runs, %d not complete\n", run, h.Runs24h, h.Failed24h)
	fmt.Fprintf(&b, "heartbeat %s · backup %s · restore test: %s\n", h.Heartbeat, h.Backup, h.RestoreTest)
	for _, s := range h.Sources {
		if s.Expected < 0 {
			fmt.Fprintf(&b, "source %s %d read", s.Name, s.Seen)
		} else {
			fmt.Fprintf(&b, "source %s %d/%d read", s.Name, s.Seen, s.Expected)
		}
		if s.Newest != "" {
			b.WriteString(" · newest " + s.Newest)
		}
		b.WriteString("\n")
	}
	for _, t := range h.Teams {
		fmt.Fprintf(&b, "team %s · last line %s · last call %s", t.Project, or(t.LastLine, "never"), or(t.LastCall, "never"))
		if t.Silent {
			b.WriteString(" · SILENT")
		}
		b.WriteString("\n")
	}
	return b.String()
}

func or(s, dflt string) string {
	if s == "" {
		return dflt
	}
	return s
}
