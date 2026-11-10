package board

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 10, 16, 0, 0, 0, time.FixedZone("+04", 4*3600))

func boardPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "state", "demo", "BOARD.toml")
}

func add(t *testing.T, path, id string, files ...string) {
	t.Helper()
	err := Update(path, t0, func(b *Board) error {
		return b.Add(Row{ID: id, Text: "the criterion " + id, Files: files}, t0)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func move(path, id, to string, min int, set func(*Row)) error {
	return Update(path, t0.Add(time.Duration(min)*time.Minute), func(b *Board) error {
		return b.Move(id, to, "test", "step "+to, t0.Add(time.Duration(min)*time.Minute), set)
	})
}

func row(t *testing.T, path, id string) Row {
	t.Helper()
	b, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range b.Rows {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no row %s", id)
	return Row{}
}

// A criterion goes open -> check-written -> ready -> building -> pr -> merged -> proven; each move is one line in the
// event log (the pilot's hours per criterion come from it) and the row keeps its session and PR.
func TestTheHappyPathMovesAndLogsEachStep(t *testing.T) {
	path := boardPath(t)
	add(t, path, "AC1", "src/quotes/**")
	steps := []struct {
		to  string
		set func(*Row)
	}{
		{CheckWritten, func(r *Row) { r.Check = "notes/pilot/AC1/check.md" }},
		{Ready, nil},
		{Building, func(r *Row) { r.Session = "demo:pilot-ac1" }},
		{PR, func(r *Row) { r.PR = 8101 }},
		{Merged, nil},
		{Proven, nil},
	}
	for i, s := range steps {
		if err := move(path, "AC1", s.to, 10*(i+1), s.set); err != nil {
			t.Fatalf("%s: %v", s.to, err)
		}
	}
	r := row(t, path, "AC1")
	if r.State != Proven || r.Session != "demo:pilot-ac1" || r.PR != 8101 || r.Check != "notes/pilot/AC1/check.md" ||
		!r.Opened.Equal(t0) || !r.Updated.Equal(t0.Add(60*time.Minute)) {
		t.Fatalf("row %+v", r)
	}
	b, err := os.ReadFile(EventsPath(path))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 7 || lines[0] != "2026-11-10T16:00:00+04:00 AC1 - -> open by add" ||
		lines[6] != "2026-11-10T17:00:00+04:00 AC1 merged -> proven by test: step proven" {
		t.Fatalf("events %q", lines)
	}
}

// A move the table does not allow is refused, names the moves that are allowed, and changes nothing on disk.
func TestIllegalMovesAreRefused(t *testing.T) {
	path := boardPath(t)
	add(t, path, "AC1")
	before, _ := os.ReadFile(path)
	err := move(path, "AC1", Building, 1, nil)
	if err == nil || !strings.Contains(err.Error(), "AC1: open -> building is not allowed (from open: check-written, blocked)") {
		t.Fatalf("want a refusal naming the allowed moves, got %v", err)
	}
	if err := move(path, "AC9", Ready, 1, nil); err == nil || !strings.Contains(err.Error(), "no row AC9") {
		t.Fatalf("want no row AC9, got %v", err)
	}
	if err := move(path, "AC1", "done", 1, nil); err == nil || !strings.Contains(err.Error(), `no state "done"`) {
		t.Fatalf("want an unknown state refused, got %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatalf("a refused move changed the board:\n%s\n%s", before, after)
	}
	if err := Update(path, t0, func(b *Board) error { return b.Add(Row{ID: "AC1", Text: "again"}, t0) }); err == nil ||
		!strings.Contains(err.Error(), "AC1 is on the board already") {
		t.Fatalf("want a duplicate id refused, got %v", err)
	}
	if err := Update(path, t0, func(b *Board) error { return b.Add(Row{ID: "AC2"}, t0) }); err == nil ||
		!strings.Contains(err.Error(), "AC2: no text") {
		t.Fatalf("want a row with no text refused, got %v", err)
	}
}

// Blocked needs the question it waits on, and goes back only to the state it was blocked from; the question is kept
// until then.
func TestBlockedGoesBackOnlyWhereItCameFrom(t *testing.T) {
	path := boardPath(t)
	add(t, path, "AC1")
	for i, s := range []string{CheckWritten, Ready, Building} {
		if err := move(path, "AC1", s, i+1, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := move(path, "AC1", Blocked, 5, nil); err == nil || !strings.Contains(err.Error(), "AC1: blocked needs the question it waits on") {
		t.Fatalf("want a question required, got %v", err)
	}
	if err := move(path, "AC1", Blocked, 5, func(r *Row) { r.Question = "may I add a rig row?" }); err != nil {
		t.Fatal(err)
	}
	if err := move(path, "AC1", Ready, 6, nil); err == nil || !strings.Contains(err.Error(), "AC1: blocked -> ready is not allowed (from blocked: building)") {
		t.Fatalf("want only the state it came from, got %v", err)
	}
	if r := row(t, path, "AC1"); r.Question != "may I add a rig row?" || r.From != Building {
		t.Fatalf("row %+v", r)
	}
	if err := move(path, "AC1", Building, 7, nil); err != nil {
		t.Fatal(err)
	}
	if r := row(t, path, "AC1"); r.Question != "" || r.From != "" || r.State != Building {
		t.Fatalf("unblocked row %+v", r)
	}
}

// A criterion already built is proven by its live check alone (ready -> proven, building -> proven); a merged or
// proven row that fails again goes back to building and counts a reopen; a PR sent back by review or CI is no reopen.
func TestProveWithoutAPRAndCountReopens(t *testing.T) {
	path := boardPath(t)
	add(t, path, "AC12")
	for i, s := range []string{CheckWritten, Ready, Proven, Building, PR, Building, PR, Merged, Building} {
		if err := move(path, "AC12", s, i+1, func(r *Row) { r.PR = 8100 + i }); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if r := row(t, path, "AC12"); r.Reopens != 2 || r.State != Building {
		t.Fatalf("want 2 reopens (proven and merged sent back), got %+v", r)
	}
}

// Next gives the oldest ready row whose files overlap no row being built or in a PR, so two builders never edit the
// same files; none ready and free gives false.
func TestNextSkipsRowsWhoseFilesOverlapWork(t *testing.T) {
	path := boardPath(t)
	add(t, path, "AC1", "src/quotes/**")
	add(t, path, "AC2", "src/quotes/list.ts")
	add(t, path, "AC3", "src/orders/*.ts", "server/Orders/**")
	add(t, path, "AC4", "docs/a.md")
	for _, id := range []string{"AC1", "AC2", "AC3"} {
		for i, s := range []string{CheckWritten, Ready} {
			if err := move(path, id, s, i+1, nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := move(path, "AC1", Building, 3, nil); err != nil {
		t.Fatal(err)
	}
	b, _ := Load(path)
	if r, ok := b.Next(); !ok || r.ID != "AC3" {
		t.Fatalf("want AC3 (AC2 overlaps AC1's files, AC4 is not ready), got %v %v", r.ID, ok)
	}
	if err := move(path, "AC3", Building, 4, nil); err != nil {
		t.Fatal(err)
	}
	b, _ = Load(path)
	if r, ok := b.Next(); ok {
		t.Fatalf("want none free, got %s", r.ID)
	}
}

func TestOverlap(t *testing.T) {
	for _, c := range []struct {
		a, b []string
		want bool
	}{
		{[]string{"src/a/**"}, []string{"src/a/b.ts"}, true},
		{[]string{"src/a/*.ts"}, []string{"src/b/*.ts"}, false},
		{[]string{"src/a/x.ts"}, []string{"src/a/y.ts"}, false},
		{[]string{"**"}, []string{"docs/x.md"}, true},
		{[]string{"src/a*"}, []string{"src/ab/c.ts"}, true},
		{nil, []string{"src/a.ts"}, true}, // a row that names no files may touch anything
		{[]string{"src/a.ts"}, []string{"src/a.ts"}, true},
	} {
		if got := Overlap(c.a, c.b); got != c.want {
			t.Errorf("Overlap(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// Two writers at once (the dispatcher and a person's crius-board set) lose no change: the board file is locked for
// each read-change-write.
func TestTwoWritersLoseNoChange(t *testing.T) {
	path := boardPath(t)
	var wg sync.WaitGroup
	errs := make(chan error, 100)
	for w := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 50 {
				id := fmt.Sprintf("W%dR%d", w, i)
				errs <- Update(path, t0, func(b *Board) error { return b.Add(Row{ID: id, Text: id}, t0) })
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	b, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Rows) != 100 {
		t.Fatalf("want 100 rows, got %d", len(b.Rows))
	}
	ev, _ := os.ReadFile(EventsPath(path))
	if n := strings.Count(string(ev), " -> open by add"); n != 100 {
		t.Fatalf("want 100 add events, got %d", n)
	}
}

// A board that cannot be read is an error, never an empty board that the next write would save over it.
func TestABrokenBoardIsAnError(t *testing.T) {
	path := boardPath(t)
	add(t, path, "AC1")
	if err := os.WriteFile(path, []byte("[[row]\nid = "), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("a broken board loaded")
	}
	if err := Update(path, t0, func(b *Board) error { return b.Add(Row{ID: "AC2", Text: "x"}, t0) }); err == nil {
		t.Fatal("a broken board was written over")
	}
	if b, _ := os.ReadFile(path); string(b) != "[[row]\nid = " {
		t.Fatalf("the broken board was changed: %q", b)
	}
}

// Review P2-1: every move to building checks the files, not only Next, so a person's crius-board set (or a race with
// the dispatcher) cannot put two builders on the same files.
func TestBuildingIsRefusedOnFilesHeldByOtherWork(t *testing.T) {
	path := boardPath(t)
	add(t, path, "AC1", "src/quotes/**")
	add(t, path, "AC2", "src/quotes/list.ts")
	add(t, path, "AC3", "src/orders/**")
	for _, id := range []string{"AC1", "AC2", "AC3"} {
		for i, s := range []string{CheckWritten, Ready} {
			if err := move(path, id, s, i+1, nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := move(path, "AC1", Building, 3, nil); err != nil {
		t.Fatal(err)
	}
	if err := move(path, "AC2", Building, 4, nil); err == nil || !strings.Contains(err.Error(), "AC2: its files overlap AC1 (building)") {
		t.Fatalf("want the overlap refused, got %v", err)
	}
	if err := move(path, "AC3", Building, 4, nil); err != nil {
		t.Fatal(err)
	}
	// AC1 blocked from building still holds its files, and goes back to building itself
	if err := move(path, "AC1", Blocked, 5, func(r *Row) { r.Question = "q" }); err != nil {
		t.Fatal(err)
	}
	if err := move(path, "AC2", Building, 6, nil); err == nil || !strings.Contains(err.Error(), "AC2: its files overlap AC1 (blocked)") {
		t.Fatalf("want a blocked builder's files held, got %v", err)
	}
	if err := move(path, "AC1", Building, 7, nil); err != nil {
		t.Fatal(err)
	}
}

// Review P2-2: a bare folder, a brace glob, ./ or / in front, and a change of case all name the same files.
func TestOverlapCatchesFoldersBracesDotsAndCase(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"src/{quotes,orders}/**", "src/quotes/list.ts", true},
		{"src/quotes", "src/quotes/list.ts", true},
		{"src/quotes/", "src/quotes/list.ts", true},
		{"./src/a.ts", "src/a.ts", true},
		{"/src/a.ts", "src/a.ts", true},
		{"src/Quotes/**", "src/quotes/a.ts", true},
		{"src/quotes/**", "src", true},
		{"src/quotes", "src/quotes2/a.ts", false},
		{"src/a.ts", "src/a.tsx", false},
	} {
		if got := Overlap([]string{c.a}, []string{c.b}); got != c.want {
			t.Errorf("Overlap(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
		if got := Overlap([]string{c.b}, []string{c.a}); got != c.want {
			t.Errorf("Overlap(%q, %q) = %v, want %v", c.b, c.a, got, c.want)
		}
	}
}

// Review P3-6/7/8: a move's set fills what the move needs and nothing Move owns; a question lives only on a blocked
// row; a move to pr needs its PR number; a reopen drops the old PR so the next pr move needs the new one.
func TestMoveKeepsWhatItOwns(t *testing.T) {
	path := boardPath(t)
	add(t, path, "AC1")
	add(t, path, "AC2")
	if err := move(path, "AC1", CheckWritten, 1, func(r *Row) {
		r.ID, r.Reopens, r.Opened, r.From, r.Question = "AC2", 9, t0.Add(time.Hour), Ready, "stray"
	}); err != nil {
		t.Fatal(err)
	}
	if r := row(t, path, "AC1"); r.Reopens != 0 || !r.Opened.Equal(t0) || r.From != "" || r.Question != "" {
		t.Fatalf("set changed what Move owns: %+v", r)
	}
	for i, s := range []string{Ready, Building} {
		if err := move(path, "AC1", s, i+2, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := move(path, "AC1", PR, 4, nil); err == nil || !strings.Contains(err.Error(), "AC1: pr needs its PR number") {
		t.Fatalf("want a PR number required, got %v", err)
	}
	for i, s := range []string{PR, Merged, Building} {
		if err := move(path, "AC1", s, i+5, func(r *Row) {
			if s == PR {
				r.PR = 8101
			}
		}); err != nil {
			t.Fatal(err)
		}
	}
	if r := row(t, path, "AC1"); r.PR != 0 || r.Reopens != 1 {
		t.Fatalf("a reopen kept the merged PR: %+v", r)
	}
}

// Review P3-4: keys the board does not know (a typo, [[rows]]) and an empty file are errors, never rows dropped by
// the next write; a run that changes nothing writes nothing (the dispatcher runs every minute).
func TestUnknownKeysAndEmptyFilesAreErrorsAndNoChangeWritesNothing(t *testing.T) {
	path := boardPath(t)
	add(t, path, "AC1")
	for _, bad := range []string{"", "[[rows]]\nid = \"AC1\"\n", "[[row]]\nid = \"AC1\"\ntext = \"x\"\nstate = \"open\"\nfile = [\"a\"]\n"} {
		if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Errorf("%q loaded", bad)
		}
	}
	empty := boardPath(t)
	if err := Update(empty, t0, func(*Board) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(empty); !os.IsNotExist(err) {
		t.Fatalf("a run with no change wrote a board: %v", err)
	}
}

// Review P3-5: who moved a row is one word in the event line, so a newline or space in it cannot forge or split one.
func TestTheMoverIsOneWordInTheEventLine(t *testing.T) {
	path := boardPath(t)
	add(t, path, "AC1")
	if err := Update(path, t0, func(b *Board) error {
		return b.Move("AC1", CheckWritten, "crius lead\n2026-11-10T16:00:00+04:00 AC9 - -> proven by x", "", t0, nil)
	}); err != nil {
		t.Fatal(err)
	}
	ev, _ := os.ReadFile(EventsPath(path))
	if lines := strings.Split(strings.TrimSpace(string(ev)), "\n"); len(lines) != 2 ||
		lines[1] != "2026-11-10T16:00:00+04:00 AC1 open -> check-written by crius_lead_2026-11-10T16:00:00+04:00_AC9_-_->_proven_by_x" {
		t.Fatalf("events %q", lines)
	}
}

// Review round 2 P2-4: a row id becomes a session role, so it is letters, digits and dashes, and unique without case.
func TestRowIdsAreRoleSafeAndUniqueWithoutCase(t *testing.T) {
	path := boardPath(t)
	add(t, path, "AC1")
	for _, id := range []string{"AC_1", "F.1", "-x", "ac1"} {
		if err := Update(path, t0, func(b *Board) error { return b.Add(Row{ID: id, Text: "x"}, t0) }); err == nil {
			t.Errorf("%q was added", id)
		}
	}
}
