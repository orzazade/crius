// Package ingest is crius-ingest v1 (design §5): transcripts, git, bus and team logs, files -> durable rows.
// It uses no model tokens.
//
// Rules: a cursor moves only in the same transaction as its rows (and the session roll-up and alerts those rows
// raise); a line without its newline waits; a shorter file or a new inode is rescanned (keys dedupe); a request
// keeps the max of each token field over its records; unknown line shapes are counted and alerted, never skipped
// quietly. Prompt text, tool output and assistant text are never stored: only counts, times, ids and the
// allowlisted provenance tag. Every stored text is redacted.
package ingest

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/orzazade/crius/internal/redact"
	"github.com/orzazade/crius/internal/store"
)

const (
	exitPartial     = 3
	exitUnavailable = 4
	exitBusy        = 75
)

type Stats struct {
	Status                                           string
	Warnings                                         []string
	UnknownTypes                                     map[string]int
	BadLines, Redactions, Synthetic, SessionsTouched int
}

type runner struct {
	home    string
	db      *sql.DB
	cfg     *config
	st      *Stats
	partial bool
	touched map[string]bool
}

func utcNow() string { return time.Now().UTC().Format("2006-01-02T15:04:05Z") }

func (r *runner) warn(partial bool, format string, args ...any) {
	msg, _ := redact.Redact(fmt.Sprintf(format, args...))
	r.st.Warnings = append(r.st.Warnings, msg)
	r.partial = r.partial || partial
}

// text redacts and shortens a value before it is stored.
func (r *runner) text(s string) string { return fit(r.redacted(s)) }

func (r *runner) redacted(s string) string {
	s, n := redact.Redact(strings.ToValidUTF8(s, "�"))
	r.st.Redactions += n
	return s
}

// fit cuts a value to a bus line's text limit.
func fit(s string) string {
	if utf8.RuneCountInString(s) > store.MaxLen {
		s = string([]rune(s)[:store.MaxLen])
	}
	return s
}

type cursor struct {
	inode, offset int64
	state, sha    string
}

func getCursor(tx *sql.Tx, source, resource string) (cursor, error) {
	var c cursor
	var state, sha sql.NullString
	var inode sql.NullInt64
	err := tx.QueryRow("select inode, offset, state, sha from coverage where source=? and resource=?",
		source, resource).Scan(&inode, &c.offset, &state, &sha)
	if errors.Is(err, sql.ErrNoRows) {
		return cursor{}, nil
	}
	c.inode, c.state, c.sha = inode.Int64, state.String, sha.String
	return c, err
}

func saveCursor(tx *sql.Tx, source, resource string, c cursor, rows int) error {
	_, err := tx.Exec("insert into coverage (source, resource, inode, offset, state, sha, last_ok, rows)"+
		" values (?,?,?,?,?,?,?,?) on conflict(source, resource) do update set inode=excluded.inode,"+
		" offset=excluded.offset, state=excluded.state, sha=excluded.sha, last_ok=excluded.last_ok,"+
		" rows=coverage.rows+excluded.rows", source, resource, c.inode, c.offset, nullable(c.state), nullable(c.sha),
		utcNow(), rows)
	return err
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

type event struct {
	ts, kind, key, session, project, actor, item, text string
	to                                                 string // a VERIFIED line's recipient, kept in refs "to"
	hasText                                            bool
	refs                                               map[string][]string
}

func (r *runner) event(tx *sql.Tx, e event) error {
	var refs, text any
	clean := map[string][]string{}
	for k, vs := range e.refs {
		for _, v := range vs {
			clean[k] = append(clean[k], r.text(v))
		}
	}
	if e.to != "" {
		clean["to"] = []string{r.name(e.to)}
	}
	if len(clean) > 0 {
		b, err := json.Marshal(clean)
		if err != nil {
			return err
		}
		refs = string(b)
	}
	if e.hasText {
		text = r.text(e.text)
	}
	// A key stored as malformed takes the parse when the line reads well now (a newer bus type list); any other
	// stored row stays as it is.
	_, err := tx.Exec("insert into event (ts, project, kind, actor, session, item, refs, text, taint,"+
		" verified, source_key) values (?,?,?,?,?,?,?,?,0,0,?) on conflict(source_key) do update set ts=excluded.ts,"+
		" project=excluded.project, kind=excluded.kind, actor=excluded.actor, session=excluded.session,"+
		" item=excluded.item, refs=excluded.refs, text=excluded.text where event.kind='malformed'",
		nullable(e.ts), nullable(e.project), e.kind, nullable(r.text(e.actor)), nullable(e.session),
		nullable(e.item), refs, text, e.key)
	return err
}

// name redacts a bus sender or recipient. A configured project's name is not a secret, so only its role is redacted:
// redaction of the whole would take token-service:lead for a key and its value. Any other name is redacted whole, so
// password:hunter2 loses its value and a slug-shaped token (xoxb-…) is caught. A name is never cut: a cut would make
// two recipients one.
func (r *runner) name(s string) string {
	if p, role, ok := strings.Cut(s, ":"); ok && r.cfg.known(p) {
		return p + ":" + r.redacted(role)
	}
	return r.redacted(s)
}

// queueAlert records an alert in the caller's transaction; flushAlerts posts it. One alert per key, ever.
func (r *runner) queueAlert(tx *sql.Tx, key, text string) error {
	_, err := tx.Exec("insert or ignore into alert (key, ts, text) values (?,?,?)", key, utcNow(), r.text(text))
	return err
}

func (r *runner) flushAlerts() {
	rows, err := r.db.Query("select key, text from alert where sent is null order by ts, key")
	if err != nil {
		r.warn(true, "cannot read the alert queue: %v", err)
		return
	}
	var pending [][2]string
	for rows.Next() {
		var k, t string
		if err := rows.Scan(&k, &t); err != nil {
			rows.Close()
			r.warn(true, "cannot read the alert queue: %v", err)
			return
		}
		pending = append(pending, [2]string{k, t})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		r.warn(true, "cannot read the alert queue: %v", err)
		return
	}
	for _, a := range pending {
		// Posted as one bus line whatever the row holds: a long or multi-line row must not block the queue.
		text := fit(strings.NewReplacer("\r", " ", "\n", " ").Replace(a[1]))
		line, err := store.FormatLine("ALERT", "crius", "ingest", text, "crius", time.Now())
		if err == nil {
			err = store.AppendLine(r.home, "crius", line)
		}
		if err == nil {
			_, err = r.db.Exec("update alert set sent=? where key=?", utcNow(), a[0])
		}
		if err != nil {
			r.warn(true, "cannot post alert %s: %v", a[0], err)
			return
		}
	}
}

func inodeOf(fi os.FileInfo) int64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return int64(st.Ino)
	}
	return 0
}

// scanLines calls fn for each complete line after the cursor; a line without its newline is left for later.
// It rescans from 0 when the file was replaced (new inode) or truncated (shorter than the cursor).
func scanLines(path string, c cursor, fn func(pos int64, line []byte) error) (next cursor, n int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return c, 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return c, 0, err
	}
	ino, offset := inodeOf(fi), c.offset
	if ino != c.inode || fi.Size() < offset {
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return c, 0, err
	}
	br := bufio.NewReaderSize(f, 1<<20)
	pos := offset
	for {
		line, err := br.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return c, n, err
		}
		if err := fn(pos, line); err != nil {
			return c, n, err
		}
		pos += int64(len(line))
		n++
	}
	return cursor{inode: ino, offset: pos, state: c.state, sha: c.sha}, n, nil
}

// inTx runs fn in one transaction: commit on success, rollback on error.
func (r *runner) inTx(fn func(tx *sql.Tx) error) error {
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Run reads every source once. The caller holds the writer lock.
func Run(home string) (*Stats, error) {
	cfg, err := loadConfig(home)
	if err != nil {
		return nil, err
	}
	db, err := store.Open(home)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := repriceIfChanged(db); err != nil {
		return nil, fmt.Errorf("reprice: %w", err)
	}
	r := &runner{home: home, db: db, cfg: cfg, st: &Stats{UnknownTypes: map[string]int{}}, touched: map[string]bool{}}
	started := utcNow()
	r.ingestTranscripts()
	r.ingestGit()
	r.ingestLogs()
	r.ingestFiles()
	r.ingestQuota()
	r.flushAlerts()
	var unpriced int
	if err := db.QueryRow("select count(*) from request where cost_list_usd is null").Scan(&unpriced); err != nil {
		return nil, err
	}
	if unpriced > 0 {
		r.warn(false, "%d requests have a model with no list price", unpriced)
	}
	r.st.SessionsTouched = len(r.touched)
	r.st.Status = "complete"
	if r.partial {
		r.st.Status = "partial"
	}
	stats, err := json.Marshal(r.st)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec("insert into source_run (started, finished, status, stats) values (?,?,?,?)",
		started, utcNow(), r.st.Status, string(stats)); err != nil {
		return nil, err
	}
	return r.st, nil
}

// Main is crius-ingest: leak test, writer lock, one run, the daily backup, and the heartbeat after a complete run.
func Main(args []string, stdout, stderr io.Writer) int {
	stdout, stderr = redact.Writer{W: stdout}, redact.Writer{W: stderr}
	fs := flag.NewFlagSet("crius-ingest", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", os.Getenv("CRIUS_REPO"), "the crius checkout the leak test scans")
	if fs.Parse(args) != nil || fs.NArg() > 0 {
		return 2
	}
	if *repo == "" {
		fmt.Fprintln(stderr, "crius-ingest needs --repo or $CRIUS_REPO")
		return 2
	}
	home := store.Home()
	if fi, err := os.Stat(filepath.Join(home, "state")); err != nil || !fi.IsDir() {
		fmt.Fprintf(stderr, "unavailable: %s is missing; run crius-rec init\n", home)
		return exitUnavailable
	}
	if problems := store.LeakProblems(home, *repo); len(problems) > 0 {
		fmt.Fprintln(stderr, "unavailable: git-leak test failed; ingest refused")
		for _, p := range problems {
			fmt.Fprintf(stderr, "  %s\n", p)
		}
		return exitUnavailable
	}
	unlock, err := store.WriterLock(home, false)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		fmt.Fprintln(stderr, "busy: another writer holds the lock")
		return exitBusy
	}
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	defer unlock()
	st, err := Run(home)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if _, err := BackupIfDue(home, time.Now()); err != nil {
		st.Status = "partial"
		st.Warnings = append(st.Warnings, fmt.Sprintf("backup failed: %v", err))
	}
	if st.Status == "complete" {
		hb, tmp := filepath.Join(home, "heartbeat"), filepath.Join(home, ".heartbeat.tmp")
		if err := os.WriteFile(tmp, []byte(utcNow()+"\n"), 0o600); err != nil {
			fmt.Fprintf(stderr, "error: heartbeat: %v\n", err)
			return 1
		}
		if err := os.Rename(tmp, hb); err != nil {
			fmt.Fprintf(stderr, "error: heartbeat: %v\n", err)
			return 1
		}
	}
	types := make([]string, 0, len(st.UnknownTypes))
	for t, n := range st.UnknownTypes {
		types = append(types, fmt.Sprintf("%s=%d", t, n))
	}
	sort.Strings(types)
	fmt.Fprintf(stdout, "%s %s sessions=%d unknown=%v bad=%d synthetic=%d redactions=%d warnings=%d\n", utcNow(),
		st.Status, st.SessionsTouched, types, st.BadLines, st.Synthetic, st.Redactions, len(st.Warnings))
	for i, w := range st.Warnings {
		if i == 20 {
			fmt.Fprintf(stdout, "  ... %d more\n", len(st.Warnings)-20)
			break
		}
		fmt.Fprintf(stdout, "  %s\n", w)
	}
	if st.Status != "complete" {
		return exitPartial
	}
	return 0
}
