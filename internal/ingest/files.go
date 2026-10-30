package ingest

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/ownerqueue"
	"github.com/orzazade/crius/internal/store"
)

func (r *runner) ingestFiles() {
	state := filepath.Join(r.home, "state")
	es, err := os.ReadDir(state)
	if err != nil {
		r.warn(true, "cannot read %s: %v", state, err)
	}
	for _, e := range es {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(state, e.Name(), "DECISIONS.md")
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err == nil {
			err = r.decisionsFile(path, data)
		}
		if err != nil {
			r.warn(true, "cannot ingest %s: %v", path, err)
		}
	}
	for _, p := range r.cfg.projects {
		for _, dir := range p.artifactDirs {
			real, err := filepath.EvalSymlinks(dir) // WalkDir does not follow a root that is a symlink
			if fi, serr := os.Stat(real); err != nil || serr != nil || !fi.IsDir() {
				r.warn(true, "artifact folder missing: %s", dir)
				continue
			}
			dir := real
			if err := r.inTx(func(tx *sql.Tx) error { return r.artifactDir(tx, p.slug, dir) }); err != nil {
				r.warn(true, "cannot ingest %s: %v", dir, err)
			}
		}
	}
	r.ownerQueue()
}

func (r *runner) decisionsFile(path string, data []byte) error {
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	return r.inTx(func(tx *sql.Tx) error {
		c, err := getCursor(tx, "decisions", path)
		if err != nil || c.sha == sha {
			return err
		}
		entries, problems := store.ParseDecisions(strings.ToValidUTF8(string(data), "�"))
		for _, p := range problems {
			r.warn(false, "%s: %s", path, p)
		}
		for _, d := range entries {
			if _, err := tx.Exec("insert into decision (id, project, ts, title, decided_by, supersedes, file, line, taint)"+
				" values (?,?,?,?,?,?,?,?,?) on conflict(id) do update set ts=excluded.ts, title=excluded.title,"+
				" decided_by=excluded.decided_by, supersedes=excluded.supersedes, file=excluded.file, line=excluded.line,"+
				" taint=excluded.taint", d.ID, d.Project, d.Date, r.text(d.Title), r.text(d.DecidedBy),
				nullable(r.text(d.Supersedes)), path, d.Line, d.Taint); err != nil {
				return err
			}
		}
		c.sha = sha
		return saveCursor(tx, "decisions", path, c, len(entries))
	})
}

// artifactDir keeps one pointer row per markdown file (path, sha256, title, headings), never the body.
// Files and folders whose name starts with '.' are skipped.
func (r *runner) artifactDir(tx *sql.Tx, slug, dir string) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			r.warn(true, "cannot read %s: %v", path, err)
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if path != dir && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		fi, err := os.Stat(path) // the target, when the entry is a symlink
		if err != nil {
			return err
		}
		stamp := fmt.Sprintf("%d:%d", fi.ModTime().UnixNano(), fi.Size())
		c, err := getCursor(tx, "artifact", path)
		if err != nil || c.state == stamp {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		title, heads := d.Name(), []string{}
		found := false
		for _, ln := range strings.Split(strings.ToValidUTF8(string(data), "�"), "\n") {
			ln = strings.TrimSuffix(ln, "\r")
			if !found && strings.HasPrefix(ln, "# ") {
				title, found = strings.TrimSpace(ln[2:]), true
			} else if strings.HasPrefix(ln, "## ") {
				heads = append(heads, strings.TrimSpace(ln[3:]))
			}
		}
		sum := sha256.Sum256(data)
		if _, err := tx.Exec("insert into artifact (path, project, sha256, title, headings, mtime) values (?,?,?,?,?,?)"+
			" on conflict(path) do update set project=excluded.project, sha256=excluded.sha256, title=excluded.title,"+
			" headings=excluded.headings, mtime=excluded.mtime", path, slug, hex.EncodeToString(sum[:]), r.text(title),
			r.text(strings.Join(heads, " | ")), fi.ModTime().UTC().Format(time.RFC3339)); err != nil {
			return err
		}
		c.state = stamp
		return saveCursor(tx, "artifact", path, c, 1)
	})
}

// ownerQueue copies the owner queue (internal/ownerqueue: one JSON row per line, the whole file replaced at each
// change) into owner_decision when it changed: every question put to the owner and its answer, for the review of how
// the owner decides (crius-rec decisions). A queue never set up has no file (the review says it has no rows); a file
// gone after the queue was set up is lost rows, and the run is partial.
func (r *runner) ownerQueue() {
	path := filepath.Join(r.home, "state", "owner-queue.jsonl")
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		if _, serr := os.Stat(filepath.Join(r.home, "state", "owner-queue.sync.json")); serr == nil {
			r.warn(true, "owner queue rows missing: %s (the queue was set up; restore the file)", path)
		}
		return
	}
	if err == nil {
		err = r.ownerQueueFile(path, data)
	}
	if err != nil {
		r.warn(true, "cannot ingest the owner queue %s: %v", path, err)
	}
}

func (r *runner) ownerQueueFile(path string, data []byte) error {
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	utc := func(local string) any { // the queue keeps local "YYYY-MM-DDTHH:MM"
		t, err := time.ParseInLocation("2006-01-02T15:04", local, time.Local)
		if err != nil {
			return nil
		}
		return t.UTC().Format(time.RFC3339)
	}
	return r.inTx(func(tx *sql.Tx) error {
		c, err := getCursor(tx, "owner-queue", path)
		if err != nil || c.sha == sha {
			return err
		}
		n := 0
		for i, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
			if line == "" {
				continue
			}
			var row ownerqueue.Row
			if err := json.Unmarshal([]byte(line), &row); err != nil {
				return fmt.Errorf("line %d: %w", i+1, err)
			}
			options, err := json.Marshal(row.Options)
			if err != nil {
				return err
			}
			if len(row.Options) == 0 {
				options = nil
			}
			if _, err := tx.Exec("insert into owner_decision (id, asked, asked_by, title, todo, why, unblocks, options, state,"+
				" answer, answered_at, answered_by) values (?,?,?,?,?,?,?,?,?,?,?,?) on conflict(id) do update set"+
				" asked=excluded.asked, asked_by=excluded.asked_by, title=excluded.title, todo=excluded.todo, why=excluded.why,"+
				" unblocks=excluded.unblocks, options=excluded.options, state=excluded.state, answer=excluded.answer,"+
				" answered_at=excluded.answered_at, answered_by=excluded.answered_by",
				row.ID, utc(row.Opened), r.text(row.From), r.text(row.Title), nullable(r.text(row.Do)), nullable(r.text(row.Why)),
				nullable(r.text(row.Unblocks)), nullable(r.text(string(options))), row.State, nullable(r.text(row.Answer)),
				utc(row.AnsweredAt), nullable(r.text(row.AnsweredBy))); err != nil {
				return err
			}
			n++
		}
		c.sha, c.offset = sha, int64(len(data))
		return saveCursor(tx, "owner-queue", path, c, n)
	})
}
