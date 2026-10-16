package recall

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/testutil"
)

// healthy is a home where everything is fine: a fresh run, heartbeat and backup, one team (demo) that posted a bus
// line 30 min ago and made an API call 10 min ago, and one transcript read to its end.
func healthy(t *testing.T) (db *sql.DB, home string, exp Expected) {
	t.Helper()
	db = open(t)
	home = os.Getenv("CRIUS_HOME")
	testutil.Write(t, filepath.Join(home, "heartbeat"), "2026-10-16T11:59:00Z\n")
	backup := filepath.Join(home, "backup", "record-2026-10-16.db.gz")
	testutil.Write(t, backup, "x")
	os.Chtimes(backup, now.Add(-8*time.Hour), now.Add(-8*time.Hour))
	tx := filepath.Join(home, "tx", "-p", "s1.jsonl")
	testutil.Write(t, tx, "{}\n{}\n")
	old := now.Add(-time.Hour)
	os.Chtimes(tx, old, old)
	exec(t, db, "insert into coverage (source, resource, offset, last_ok) values ('transcript', ?, 6, '2026-10-16T11:59:00Z')", tx)
	busFile := filepath.Join(home, "bus", "demo.log")
	testutil.Write(t, busFile, "line\n")
	exec(t, db, "insert into coverage (source, resource, offset, last_ok) values ('bus', ?, 5, '2026-10-16T11:59:00Z')", busFile)
	exec(t, db, "insert into event (ts, project, kind, text, source_key) values ('2026-10-16T11:30:00Z','demo','info','x','bus:a:1:b')")
	exec(t, db, "insert into session (id, project) values ('s1','demo')")
	exec(t, db, "insert into request (request_id, session, ts_first, ts_last) values ('r1','s1','2026-10-16T11:50:00Z','2026-10-16T11:50:01.500Z')")
	exp = Expected{Files: map[string][]string{"transcript": {tx}, "bus": {busFile}}, Teams: []string{"demo"}}
	return db, home, exp
}

func health(t *testing.T, db *sql.DB, home string, exp Expected) *Health {
	t.Helper()
	h, err := CheckHealth(db, home, exp, now)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestHealthCompleteWhenAllIsWell(t *testing.T) {
	db, home, exp := healthy(t)
	// A file written after the last run started belongs to the next run: not "never read".
	fresh := filepath.Join(home, "tx", "-p", "s3.jsonl")
	testutil.Write(t, fresh, "{}\n")
	os.Chtimes(fresh, now, now)
	exp.Files["transcript"] = append(exp.Files["transcript"], fresh)
	h := health(t, db, home, exp)
	txt := h.Text()
	for _, want := range []string{"complete · health", "heartbeat 2026-10-16T11:59:00Z (1m old)", "record-2026-10-16.db.gz (8h0m old)",
		"restore test: never run", "transcript 1/2 read", "bus 1/1 read", "team demo"} {
		if !strings.Contains(txt, want) {
			t.Fatalf("missing %q in\n%s", want, txt)
		}
	}
	if h.Status != "complete" || strings.Contains(txt, "PROBLEM") {
		t.Fatal(txt)
	}
}

// Step 4: a team with active sessions and no bus or team-log line within 3 h shows as a silent lead.
func TestASilentLeadShowsInHealth(t *testing.T) {
	db, home, exp := healthy(t)
	exec(t, db, "update event set ts = '2026-10-16T08:30:00Z'")
	h := health(t, db, home, exp)
	if h.Status != "partial" || !strings.Contains(h.Text(), "PROBLEM lead silent: demo") {
		t.Fatal(h.Text())
	}
	// No API call within 3 h: the team is idle, not silent.
	exec(t, db, "update request set ts_last = '2026-10-16T08:00:00Z'")
	if h = health(t, db, home, exp); h.Status != "complete" {
		t.Fatal(h.Text())
	}
	// A team-log line counts like a bus line.
	exec(t, db, "update request set ts_last = '2026-10-16T11:50:00Z'")
	exec(t, db, "insert into event (ts, project, kind, text, source_key) values ('2026-10-16T11:00:00Z','demo','info','x','team:a:1:b')")
	if h = health(t, db, home, exp); h.Status != "complete" {
		t.Fatal(h.Text())
	}
}

func TestHealthFlagsUnreadAndBehindFiles(t *testing.T) {
	db, home, exp := healthy(t)
	never := filepath.Join(home, "tx", "-p", "s2.jsonl")
	testutil.Write(t, never, "{}\n")
	old := now.Add(-time.Hour)
	os.Chtimes(never, old, old)
	exp.Files["transcript"] = append(exp.Files["transcript"], never)
	busFile := exp.Files["bus"][0]
	testutil.Write(t, busFile, "line\nline 2\n")
	os.Chtimes(busFile, old, old)
	txt := health(t, db, home, exp).Text()
	if !strings.Contains(txt, "PROBLEM transcript: 1 never read") || !strings.Contains(txt, "PROBLEM bus: 1 behind") {
		t.Fatal(txt)
	}
	// A partial last line (no newline yet) waits by design: not behind.
	testutil.Write(t, busFile, "line\nhalf")
	os.Chtimes(busFile, old, old)
	if txt = health(t, db, home, exp).Text(); strings.Contains(txt, "bus: 1 behind") {
		t.Fatal(txt)
	}
}

func TestHealthFlagsOldHeartbeatMissingBackupAndAStaleRun(t *testing.T) {
	db, home, exp := healthy(t)
	testutil.Write(t, filepath.Join(home, "heartbeat"), "2026-10-16T10:00:00Z\n")
	os.Remove(filepath.Join(home, "backup", "record-2026-10-16.db.gz"))
	exec(t, db, "update source_run set finished = '2026-10-16T10:00:00Z'")
	txt := health(t, db, home, exp).Text()
	for _, want := range []string{"PROBLEM heartbeat 2h0m old", "PROBLEM no backup", "PROBLEM last ingest run finished 2h0m0s ago"} {
		if !strings.Contains(txt, want) {
			t.Fatalf("missing %q in\n%s", want, txt)
		}
	}
	os.Remove(filepath.Join(home, "heartbeat"))
	if txt = health(t, db, home, exp).Text(); !strings.Contains(txt, "PROBLEM no heartbeat") {
		t.Fatal(txt)
	}
}
