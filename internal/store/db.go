package store

// record.db: one SQLite file in ~/.crius/state, WAL, one writer at a time (flock).

import (
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"syscall"

	_ "modernc.org/sqlite"
)

const schema = `
create table if not exists item (
  id text primary key, project text not null, n integer not null, parent text, kind text not null,
  title text not null, criteria text not null, opened text not null, state text not null default 'open',
  taint integer not null default 0, unique (project, n));
`

// Open opens (or creates, mode 600) the record store and applies the schema.
func Open(home string) (*sql.DB, error) {
	path := filepath.Join(home, "state", "record.db")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	f.Close()
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", sqliteURI(path, "_pragma=busy_timeout(30000)&_pragma=journal_mode(wal)"))
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// sqliteURI escapes the path, so '#', '?' or '%' in a folder name cannot point SQLite at another file.
func sqliteURI(path, query string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return (&url.URL{Scheme: "file", Path: path, RawQuery: query}).String()
}

// WriterLock is held by the ingest and by `record` while they write. With blocking=false it returns
// syscall.EWOULDBLOCK when another writer holds it.
func WriterLock(home string, blocking bool) (unlock func(), err error) {
	f, err := os.OpenFile(filepath.Join(home, "state", "record.lock"), os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	how := syscall.LOCK_EX
	if !blocking {
		how |= syscall.LOCK_NB
	}
	if err := syscall.Flock(int(f.Fd()), how); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil
}
