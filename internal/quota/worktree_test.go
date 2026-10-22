package quota

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/store"
)

// racing is a store that an ingest writes to between the report's first query and its second.
type racing struct {
	db    *sql.DB
	n     int
	write func()
}

func (r *racing) Query(query string, args ...any) (*sql.Rows, error) {
	rows, err := r.db.Query(query, args...)
	if r.n++; r.n == 1 {
		r.write()
	}
	return rows, err
}

// Review round 4 of the 22:46 fix: each session is read with its worktree record. Read apart, an ingest that committed
// a session and its record between the two reads left the report with the session and no record, so it filed the
// session by its folder on the disk, where the worktree may be gone.
func TestASessionIsReadWithItsWorktreeRecord(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	add := func(id, cwd string) {
		for _, q := range []string{"insert into session (id, cwd) values (?, ?)",
			"insert into session_worktree (session, path, main) values (?, ?, '/w/svc')"} {
			if _, err := db.Exec(q, id, cwd); err != nil {
				t.Fatal(err)
			}
		}
	}
	add("before", "/w/.worktrees/a")
	got := map[string]*store.Worktree{}
	p := Projects{ForCwd: func(cwd string, recorded *store.Worktree) string {
		got[cwd] = recorded
		return ""
	}}
	if _, err := Build(&racing{db: db, write: func() { add("during", "/w/.worktrees/b") }}, p, time.Now()); err != nil {
		t.Fatal(err)
	}
	if w := got["/w/.worktrees/a"]; w == nil || w.Path != "/w/.worktrees/a" {
		t.Errorf("session recorded before the report: got %+v, want its record", w)
	}
	if w, ok := got["/w/.worktrees/b"]; ok && w == nil {
		t.Error("session recorded while the report read was filed with no record")
	}
}
