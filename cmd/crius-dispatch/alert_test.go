package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/dispatch"
)

// fakeDeps is a dispatcher with no tabs, no az and no posts: only the board and its lock are real.
func fakeDeps(home, project string, now time.Time) dispatch.Deps {
	return dispatch.Deps{Home: home, Project: project, Now: now,
		Spawn: func(string, string) error { return nil }, Close: func(string) error { return nil },
		Alive: func(string) (bool, error) { return true, nil }, PRState: func(int) (string, error) { return "active", nil },
		Post: func(string, string, string) error { return nil }}
}

// oneOpenRow is a board with one row nobody may pick yet.
const oneOpenRow = "[[row]]\nid = \"AC1\"\ntext = \"x\"\nstate = \"open\"\nopened = 2026-11-10T16:00:00Z\nupdated = 2026-11-10T16:00:00Z\n"

// project makes <home>/state/<p> with a good DISPATCH.toml and a board of one open row.
func project(t *testing.T, home, p string) string {
	t.Helper()
	dir := filepath.Join(home, "state", p)
	os.MkdirAll(dir, 0o755)
	os.MkdirAll(filepath.Join(home, "bus"), 0o755)
	os.WriteFile(filepath.Join(dir, "DISPATCH.toml"), []byte("cap = 1\nprefix = \"p-\"\nprompt = \"do {id}\"\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "BOARD.toml"), []byte(oneOpenRow), 0o644)
	return dir
}

func wakes(t *testing.T, home string) []string {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(home, "bus", "crius.log"))
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.Contains(l, " ALERT crius:dispatch -> crius:lead — ") {
			out = append(out, l)
		}
	}
	return out
}

// 11-19 to 11-21: quotes failed on BOARD.toml.lock 2760 times and only the log saw it. The first failure wakes Crius
// with the project, the error and the log; the same error next runs is quiet; after a clean run, or with a new error,
// it wakes Crius again. Another project's errors do not change quotes' mark.
func TestARepeatedErrorWakesCriusOnce(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root opens a 0400 file for writing")
	}
	home := t.TempDir()
	dir := project(t, home, "quotes")
	lock := filepath.Join(dir, "BOARD.toml.lock")
	os.WriteFile(lock, nil, 0o400)
	for i := 0; i < 3; i++ {
		if err := run(home, t0.Add(time.Duration(i)*time.Minute), fakeDeps); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Fatalf("run %d: want the lock's error, got %v", i, err)
		}
	}
	w := wakes(t, home)
	if len(w) != 1 {
		t.Fatalf("want 1 wake line after 3 failing runs, got %d: %q", len(w), w)
	}
	for _, want := range []string{"quotes:", "BOARD.toml.lock: permission denied", filepath.Join(home, "logs", "dispatch.log")} {
		if !strings.Contains(w[0], want) {
			t.Fatalf("the wake line has no %q: %s", want, w[0])
		}
	}

	// another project starts failing: it is said once, and quotes stays quiet
	other := project(t, home, "alpha")
	os.WriteFile(filepath.Join(other, "DISPATCH.toml"), []byte("cap = 0\n"), 0o644)
	run(home, t0.Add(3*time.Minute), fakeDeps)
	run(home, t0.Add(4*time.Minute), fakeDeps)
	if w := wakes(t, home); len(w) != 2 || !strings.Contains(w[1], "alpha: DISPATCH.toml needs cap") {
		t.Fatalf("want quotes once and alpha once, got %q", w)
	}
	os.WriteFile(filepath.Join(other, "DISPATCH.toml"), []byte("cap = 1\nprefix = \"p-\"\nprompt = \"do {id}\"\n"), 0o644)

	// a clean run clears the mark; the same error after it is said again
	os.Chmod(lock, 0o600)
	if err := run(home, t0.Add(5*time.Minute), fakeDeps); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(markPath(home, "quotes")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the mark stayed after a clean run: %v", err)
	}
	os.Chmod(lock, 0o400)
	run(home, t0.Add(6*time.Minute), fakeDeps)
	if w := wakes(t, home); len(w) != 3 || !strings.Contains(w[2], "quotes:") {
		t.Fatalf("want quotes said again after a clean run, got %q", w)
	}

	// a different error is said again
	os.WriteFile(filepath.Join(dir, "DISPATCH.toml"), []byte("cap = 1\nprefix = \"p-\"\nprompt = \"no id\"\n"), 0o644)
	run(home, t0.Add(7*time.Minute), fakeDeps)
	run(home, t0.Add(8*time.Minute), fakeDeps)
	if w := wakes(t, home); len(w) != 4 || !strings.Contains(w[3], "quotes: DISPATCH.toml needs cap") {
		t.Fatalf("want the new error said once, got %q", w)
	}
}

// 11-17: Crius switched quotes off after the one ALERT, the file came back (legion-pull) and the same error ran for
// days with no line. An error still there after resay is said again, with when it started.
func TestAnErrorStillThereIsSaidAgainAfterResay(t *testing.T) {
	home := t.TempDir()
	dir := project(t, home, "quotes")
	os.WriteFile(filepath.Join(dir, "DISPATCH.toml"), []byte("cap = 0\n"), 0o644)
	run(home, t0, fakeDeps)
	run(home, t0.Add(resay-time.Minute), fakeDeps)
	if w := wakes(t, home); len(w) != 1 {
		t.Fatalf("want 1 line before resay, got %q", w)
	}
	run(home, t0.Add(resay), fakeDeps)
	run(home, t0.Add(resay+time.Minute), fakeDeps)
	w := wakes(t, home)
	if len(w) != 2 || !strings.Contains(w[1], "still failing since 2026-11-10T17:00") {
		t.Fatalf("want one 'still failing' line at resay, got %q", w)
	}
}

// The mark that keeps the line to once cannot be written: the line says so, and it is posted every run (loud), and
// run's error names it.
func TestAMarkThatCannotBeWrittenIsLoud(t *testing.T) {
	home := t.TempDir()
	dir := project(t, home, "quotes")
	os.WriteFile(filepath.Join(dir, "DISPATCH.toml"), []byte("cap = 0\n"), 0o644)
	os.MkdirAll(markPath(home, "quotes"), 0o755) // a directory where the mark file goes
	for i := 0; i < 2; i++ {
		err := run(home, t0.Add(time.Duration(i)*time.Minute), fakeDeps)
		if err == nil || !strings.Contains(err.Error(), "the alert mark was not saved") {
			t.Fatalf("run %d: want the mark's error, got %v", i, err)
		}
	}
	w := wakes(t, home)
	if len(w) != 2 || !strings.Contains(w[0], "could not save the alert mark") || !strings.Contains(w[0], "repeats every run") {
		t.Fatalf("want the line every run, saying the mark failed, got %q", w)
	}
}

// A mark that cannot be cleared after a clean run is an error that is said (it would hide the next failure).
func TestAMarkThatCannotBeClearedIsSaid(t *testing.T) {
	home := t.TempDir()
	project(t, home, "quotes")
	m := markPath(home, "quotes")
	os.MkdirAll(filepath.Join(m, "x"), 0o755) // a non-empty directory: os.Remove fails
	err := run(home, t0, fakeDeps)
	if err == nil || !strings.Contains(err.Error(), "could not clear the alert mark") {
		t.Fatalf("want the clear error, got %v", err)
	}
	if w := wakes(t, home); len(w) != 1 || !strings.Contains(w[0], "could not clear the alert mark") {
		t.Fatalf("want one line naming the clear error, got %q", w)
	}
}

// An ALERT that cannot be posted leaves no mark, so the next run tries again, and run says it was not posted.
func TestAnAlertNotPostedIsTriedAgain(t *testing.T) {
	home := t.TempDir()
	dir := project(t, home, "quotes")
	os.WriteFile(filepath.Join(dir, "DISPATCH.toml"), []byte("cap = 0\n"), 0o644)
	os.RemoveAll(filepath.Join(home, "bus"))
	os.WriteFile(filepath.Join(home, "bus"), nil, 0o644) // a file where the bus folder goes
	err := run(home, t0, fakeDeps)
	if err == nil || !strings.Contains(err.Error(), "the ALERT was not posted") {
		t.Fatalf("want the post error, got %v", err)
	}
	if _, err := os.Stat(markPath(home, "quotes")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a mark stayed for an ALERT never posted: %v", err)
	}
	os.Remove(filepath.Join(home, "bus"))
	os.MkdirAll(filepath.Join(home, "bus"), 0o755)
	run(home, t0.Add(time.Minute), fakeDeps)
	if w := wakes(t, home); len(w) != 1 {
		t.Fatalf("the next run did not post the ALERT: %q", w)
	}
}

// Quotes 11-21: BOARD.toml is gone and only its lock is left (the board moved to the Legion). The line says the board
// is gone and how to switch dispatch off, not the lock's permission error.
func TestABoardThatIsGoneSaysSwitchDispatchOff(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root may write a 0400 file")
	}
	home := t.TempDir()
	dir := project(t, home, "quotes")
	os.Remove(filepath.Join(dir, "BOARD.toml"))
	os.WriteFile(filepath.Join(dir, "BOARD.toml.lock"), nil, 0o400)
	err := run(home, t0, fakeDeps)
	want := "quotes: this project's board is gone (no BOARD.toml, only a read-only BOARD.toml.lock); switch dispatch off: mv DISPATCH.toml DISPATCH.toml.off-1110"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("want %q, got %v", want, err)
	}
	if w := wakes(t, home); len(w) != 1 || !strings.Contains(w[0], want) {
		t.Fatalf("want one line with %q, got %q", want, w)
	}
}

// A new project has no board: its first run makes only the lock, and its next runs are not "gone".
func TestANewProjectWithNoBoardIsNotGone(t *testing.T) {
	home := t.TempDir()
	dir := project(t, home, "fresh")
	os.Remove(filepath.Join(dir, "BOARD.toml"))
	for i := 0; i < 2; i++ {
		if err := run(home, t0.Add(time.Duration(i)*time.Minute), fakeDeps); err != nil {
			t.Fatalf("run %d: a new project failed: %v", i, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "BOARD.toml.lock")); err != nil {
		t.Fatalf("the first run made no lock, so the second run did not test a lock with no board: %v", err)
	}
}

// Review P2-1: the ALERT was not posted and the old mark cannot be put back: the run's error says so (a mark for an
// ALERT never said would keep the next runs quiet).
func TestAMarkNotPutBackIsInTheError(t *testing.T) {
	home := t.TempDir()
	dir := project(t, home, "quotes")
	os.WriteFile(filepath.Join(dir, "DISPATCH.toml"), []byte("cap = 0\n"), 0o644)
	os.RemoveAll(filepath.Join(home, "bus"))
	os.WriteFile(filepath.Join(home, "bus"), nil, 0o644) // a file where the bus folder goes: the post fails
	os.WriteFile(markPath(home, "quotes"), []byte(`{"key":"an older error"}`), 0o644)
	n := 0
	writeFile = func(name string, b []byte, perm os.FileMode) error {
		if n++; n > 1 {
			return errors.New("disk full")
		}
		return os.WriteFile(name, b, perm)
	}
	defer func() { writeFile = os.WriteFile }()
	err := run(home, t0, fakeDeps)
	if err == nil || !strings.Contains(err.Error(), "the old alert mark was not put back") || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("want the put-back error, got %v", err)
	}
}

// Review P2-2: the board-gone line carries today's date; the next day it is the same error, still quiet, and its
// "still failing since" keeps the first day.
func TestTheBoardGoneLineIsTheSameErrorTheNextDay(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root may write a 0400 file")
	}
	home := t.TempDir()
	dir := project(t, home, "quotes")
	os.Remove(filepath.Join(dir, "BOARD.toml"))
	os.WriteFile(filepath.Join(dir, "BOARD.toml.lock"), nil, 0o400)
	late := t0.Add(6*time.Hour + 30*time.Minute) // 23:30
	run(home, late, fakeDeps)
	run(home, late.Add(time.Minute), fakeDeps)
	run(home, late.Add(time.Hour), fakeDeps) // 00:30 the next day
	if w := wakes(t, home); len(w) != 1 {
		t.Fatalf("want 1 line across midnight, got %q", w)
	}
	run(home, late.Add(resay), fakeDeps)
	if w := wakes(t, home); len(w) != 2 || !strings.Contains(w[1], "still failing since 2026-11-10T23:30") {
		t.Fatalf("want 'still failing since' the first day, got %q", w)
	}
}
