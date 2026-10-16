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
create table if not exists request (
  request_id text primary key, session text not null, ts_first text, ts_last text, model text, query_source text,
  tok_in integer not null default 0, tok_out integer not null default 0, tok_cache_read integer not null default 0,
  tok_cache_write integer not null default 0, tok_cw_1h integer not null default 0, tok_cw_5m integer not null default 0,
  cost_list_usd real, transcript_path text, line_uuid text);
create index if not exists request_session on request (session);
create table if not exists session (
  id text primary key, account text not null default 'unknown', persona text, project text,
  role text not null default 'unknown', parent_id text, item text, attrib_source text not null default 'unknown',
  title text, model_main text, started text, ended text, end_reason text, peak_ctx integer, compactions integer,
  human_prompts integer, peer_prompts integer, interrupts integer, hook_blocks integer, hook_errors integer,
  taint integer not null default 0, cwd text, transcript_path text);
create table if not exists assignment (
  session text not null, item text, role text, account text, project text, parent text, from_ts text not null,
  to_ts text, source text not null, unique (session, from_ts));
create table if not exists event (
  id integer primary key, ts text, project text, kind text not null, actor text, session text, item text, refs text,
  text text, taint integer not null default 0, verified integer not null default 0, source_key text not null unique);
create index if not exists event_session on event (session, kind);
create table if not exists decision (
  id text primary key, project text, ts text, title text, decided_by text, supersedes text, file text, line integer,
  session text, taint integer not null default 0);
create table if not exists git_commit (
  sha text primary key, repo text, project text, ts text, session text, files text, is_revert integer not null default 0,
  reverts_sha text, ci text);
create table if not exists evidence (
  id integer primary key, kind text not null, ref text, revision text, ts text, source_key text not null unique);
create table if not exists artifact (
  path text primary key, project text, sha256 text, title text, headings text, mtime text,
  taint integer not null default 0);
create table if not exists coverage (
  source text not null, resource text not null, inode integer, offset integer not null default 0, state text,
  sha text, last_ok text, rows integer not null default 0, primary key (source, resource));
-- Quota samples from the statusline spool (design §3c): one row per spool line with rate_limits.
create table if not exists quota (
  source_key text primary key, ts text not null, account text not null, session text, persona text, epoch integer,
  five_hour_pct real, five_reset integer, seven_day_pct real, sample_kind text not null);
create index if not exists quota_ts on quota (ts);
-- What the statusline showed per session: rl=1 when rate_limits were present (Max), 0 after a response without them
-- (enterprise). The account guard checks the launch tag against it.
create table if not exists account_seen (
  session text primary key, persona text, rl integer not null, first_ts text not null, last_ts text not null);
create table if not exists source_run (
  id integer primary key, started text, finished text, status text, stats text);
-- One row per distinct alert, written in the same transaction as the rows that raised it; sent is set once the
-- ALERT line is on the bus, so an alert that could not be posted is posted by the next run.
create table if not exists alert (key text primary key, ts text not null, text text not null, sent text);
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
	db, err := sql.Open("sqlite", URI(path, "_pragma=busy_timeout(30000)&_pragma=journal_mode(wal)"))
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// URI escapes the path, so '#', '?' or '%' in a folder name cannot point SQLite at another file.
func URI(path, query string) string {
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
