package dispatch

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/board"
)

var t0 = time.Date(2026, 11, 10, 16, 0, 0, 0, time.FixedZone("+04", 4*3600))

type fake struct {
	home     string
	spawned  []string // "role: prompt"
	closed   []string
	posts    []string // "TYPE to: text"
	dead     map[string]bool
	live     map[string]bool // roles started once
	failing  map[string]bool // roles whose spawn fails
	during   func()          // runs inside each Spawn
	prs      map[int]string
	proofs   map[int]string // ci-proof's line per PR; none is NONE no-run-on-merge
	proofErr error
	proofed  []string // "pr pipeline" per Proof call
	postErr  error    // a MERGE post fails with it
	at       time.Time
	err      error
	allowErr bool
}

func newFake(t *testing.T, cap int) *fake {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, "state", "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "bus"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "cap = " + string(rune('0'+cap)) + "\nprefix = \"pilot-\"\nprompt = \"Your criterion: {id}. Read ~/brief.md first.\"\npipeline = 42\n"
	if err := os.WriteFile(filepath.Join(dir, "DISPATCH.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	return &fake{home: home, dead: map[string]bool{}, live: map[string]bool{}, failing: map[string]bool{}, prs: map[int]string{}, proofs: map[int]string{}}
}

func (f *fake) run(t *testing.T, min int) {
	t.Helper()
	f.at = t0.Add(time.Duration(min) * time.Minute)
	err := Run(Deps{Home: f.home, Project: "demo", Now: f.at,
		Spawn: func(role, prompt string) error {
			f.spawned = append(f.spawned, role+": "+prompt)
			f.live[role] = true
			if f.during != nil {
				f.during()
			}
			if f.failing[role] {
				f.dead[role] = true
				return errors.New("the first prompt did not show in workspace:9 surface:99")
			}
			delete(f.dead, role)
			return nil
		},
		Close: func(role string) error {
			f.closed = append(f.closed, role)
			if f.during != nil {
				f.during()
			}
			return nil
		},
		Alive: func(role string) (bool, error) { return f.live[role] && !f.dead[role], nil },
		PRState: func(pr int) (string, error) {
			if s, ok := f.prs[pr]; ok {
				return s, nil
			}
			return "active", nil
		},
		Proof: func(pr, pipeline int) (string, error) {
			f.proofed = append(f.proofed, strconv.Itoa(pr)+" "+strconv.Itoa(pipeline))
			if f.proofErr != nil {
				return "", f.proofErr
			}
			if l, ok := f.proofs[pr]; ok {
				return l, nil
			}
			return "NONE pr=" + strconv.Itoa(pr) + " head=00000000 merge=00000000 build=none result=no-run-on-merge", nil
		},
		Post: func(typ, to, text string) error {
			if typ == "MERGE" && f.postErr != nil {
				return f.postErr
			}
			f.posts = append(f.posts, typ+" "+to+": "+text)
			return nil
		},
	})
	f.err = err
	if err != nil && !f.allowErr {
		t.Fatal(err)
	}
}

func (f *fake) bus(t *testing.T, lines ...string) {
	t.Helper()
	fh, err := os.OpenFile(filepath.Join(f.home, "bus", "demo.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	for _, l := range lines {
		fh.WriteString(l + "\n")
	}
}

func (f *fake) ready(t *testing.T, id string, files ...string) {
	t.Helper()
	err := board.Update(filepath.Join(f.home, "state", "demo", "BOARD.toml"), t0, func(b *board.Board) error {
		if err := b.Add(board.Row{ID: id, Text: "criterion " + id, Files: files}, t0); err != nil {
			return err
		}
		for _, s := range []string{board.CheckWritten, board.Ready} {
			if err := b.Move(id, s, "test", "", t0, nil); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (f *fake) row(t *testing.T, id string) board.Row {
	t.Helper()
	b, err := board.Load(filepath.Join(f.home, "state", "demo", "BOARD.toml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range b.Rows {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no row %s", id)
	return board.Row{}
}

// Each run starts the oldest ready rows free of others' files, up to the cap, one fresh session per row with the
// project's prompt, and tells Crius.
func TestStartsFreeReadyRowsUpToTheCap(t *testing.T) {
	f := newFake(t, 2)
	f.ready(t, "AC1", "src/a/**")
	f.ready(t, "AC2", "src/a/list.ts")
	f.ready(t, "AC3", "src/b/**")
	f.ready(t, "AC4", "src/c/**")
	f.run(t, 1)
	if strings.Join(f.spawned, "|") != "pilot-ac1: Your criterion: AC1. Read ~/brief.md first.|pilot-ac3: Your criterion: AC3. Read ~/brief.md first." {
		t.Fatalf("spawned %q", f.spawned)
	}
	if r := f.row(t, "AC1"); r.State != board.Building || r.Session != "demo:pilot-ac1" {
		t.Fatalf("AC1 %+v", r)
	}
	if r := f.row(t, "AC4"); r.State != board.Ready {
		t.Fatalf("AC4 started over the cap: %+v", r)
	}
	if len(f.posts) != 1 || f.posts[0] != "INFO crius:lead: started demo:pilot-ac1 on AC1, demo:pilot-ac3 on AC3 (cap 2)" {
		t.Fatalf("posts %q", f.posts)
	}
	f.run(t, 2)
	if len(f.spawned) != 2 {
		t.Fatalf("started again with the cap full: %q", f.spawned)
	}
}

// The row follows its session's bus lines: a NEED, ASK or BLOCKED from it blocks the row with the question; Crius's
// DECISION or ANSWER to it unblocks; its DONE with "PR <n>" moves it to pr; Crius's VERIFIED to it proves it and closes
// the session. A line read twice (a lost cursor) changes nothing more.
func TestTheSessionsBusLinesMoveItsRow(t *testing.T) {
	f := newFake(t, 1)
	f.ready(t, "AC20")
	f.run(t, 1)
	f.bus(t, "2026-11-10T16:09 ASK demo:pilot-ac20 -> crius:lead — AC20: may I add one sample item to the test data?")
	f.run(t, 2)
	if r := f.row(t, "AC20"); r.State != board.Blocked || r.Question != "AC20: may I add one sample item to the test data?" {
		t.Fatalf("after ASK %+v", r)
	}
	f.bus(t, "2026-11-10T16:10 DECISION crius:lead -> demo:pilot-ac20 — AC20: yes, add it as rig data")
	f.run(t, 3)
	if r := f.row(t, "AC20"); r.State != board.Building || r.Question != "" {
		t.Fatalf("after DECISION %+v", r)
	}
	f.bus(t, "2026-11-10T17:10 DONE demo:pilot-ac20 -> crius:lead — AC20 PR 8120 @ 1a2b3c4d · live check notes/pilot/AC20.md")
	f.run(t, 4)
	if r := f.row(t, "AC20"); r.State != board.PR || r.PR != 8120 {
		t.Fatalf("after DONE %+v", r)
	}
	f.prs[8120] = "completed"
	f.run(t, 5)
	if r := f.row(t, "AC20"); r.State != board.Merged {
		t.Fatalf("after the merge %+v", r)
	}
	f.bus(t, "2026-11-10T18:00 VERIFIED crius:lead -> demo:pilot-ac20 — row AC20 · 1a2b3c4d · live on the merged code")
	f.run(t, 6)
	if r := f.row(t, "AC20"); r.State != board.Proven {
		t.Fatalf("after VERIFIED %+v", r)
	}
	if strings.Join(f.closed, ",") != "pilot-ac20" {
		t.Fatalf("closed %q", f.closed)
	}
	// the cursor is lost: every line is read again, and the board stays as it is
	if err := os.Remove(filepath.Join(f.home, "state", "demo", "dispatch.cursor")); err != nil {
		t.Fatal(err)
	}
	f.run(t, 7)
	if r := f.row(t, "AC20"); r.State != board.Proven || r.Reopens != 0 {
		t.Fatalf("a re-read moved the row: %+v", r)
	}
}

// A pilot that walks an AC already built posts DONE with no PR and waits for Crius's VERIFIED; one that ends PARTIAL
// (it waits on another team) is blocked with its line as the question.
func TestNoPRDoneWaitsAndPartialBlocks(t *testing.T) {
	f := newFake(t, 2)
	f.ready(t, "AC12", "src/subscriptions/**")
	f.ready(t, "AC19", "src/promotions/**")
	f.run(t, 1)
	f.bus(t,
		"2026-11-10T16:20 DONE demo:pilot-ac12 -> crius:lead — AC12 walked live, every sentence passes · notes/pilot/AC12.md",
		"2026-11-10T16:04 DONE demo:pilot-ac19 -> crius:lead — AC19 walked live: PARTIAL, waits on ticket 5151")
	f.run(t, 2)
	if r := f.row(t, "AC12"); r.State != board.Building {
		t.Fatalf("AC12 moved before Crius verified: %+v", r)
	}
	if r := f.row(t, "AC19"); r.State != board.Blocked || !strings.Contains(r.Question, "PARTIAL, waits on ticket 5151") {
		t.Fatalf("AC19 %+v", r)
	}
	f.bus(t, "2026-11-10T16:25 VERIFIED crius:lead -> demo:pilot-ac12 — row AC12 · screenshots match every sentence")
	f.run(t, 3)
	if r := f.row(t, "AC12"); r.State != board.Proven {
		t.Fatalf("AC12 %+v", r)
	}
}

// A building row whose session is gone for 2 minutes (crius-spawn -move ends and resumes a session in that time) is
// started again once with the same prompt; gone a second time, the row is blocked and Crius is told.
func TestADeadSessionIsRestartedOnce(t *testing.T) {
	f := newFake(t, 1)
	f.ready(t, "AC8")
	f.run(t, 1)
	f.dead["pilot-ac8"] = true
	f.run(t, 2)
	if len(f.spawned) != 1 {
		t.Fatalf("restarted with no grace: %q", f.spawned)
	}
	f.run(t, 3)
	if len(f.spawned) != 1 {
		t.Fatalf("restarted after 1 min: %q", f.spawned)
	}
	f.run(t, 4)
	if len(f.spawned) != 2 || f.spawned[1] != "pilot-ac8: Your criterion: AC8. Read ~/brief.md first. Your last session ended: read your log and go on." {
		t.Fatalf("spawned %q", f.spawned)
	}
	if r := f.row(t, "AC8"); r.State != board.Building || r.Restarts != 1 {
		t.Fatalf("after one restart %+v", r)
	}
	f.dead["pilot-ac8"] = true
	f.run(t, 5)
	f.run(t, 7)
	if len(f.spawned) != 2 {
		t.Fatalf("restarted twice: %q", f.spawned)
	}
	if r := f.row(t, "AC8"); r.State != board.Blocked || r.Question != "its session ended twice with no result" {
		t.Fatalf("after a second death %+v", r)
	}
	if !strings.Contains(strings.Join(f.posts, "\n"), "NEED crius:lead: AC8: demo:pilot-ac8 ended twice with no result; the row is blocked") {
		t.Fatalf("posts %q", f.posts)
	}
}

// An abandoned PR sends the row back to building and tells its session; a failed read of a PR is an error, never
// "still active".
func TestAnAbandonedPRGoesBackToBuilding(t *testing.T) {
	f := newFake(t, 1)
	f.ready(t, "AC27")
	f.run(t, 1)
	f.bus(t, "2026-11-10T17:00 DONE demo:pilot-ac27 -> crius:lead — AC27 PR 8130 @ 9f8e7d6c")
	f.run(t, 2)
	f.prs[8130] = "abandoned"
	f.run(t, 3)
	if r := f.row(t, "AC27"); r.State != board.Building || r.PR != 0 {
		t.Fatalf("after abandon %+v", r)
	}
	if !strings.Contains(strings.Join(f.posts, "\n"), "INFO demo:pilot-ac27: AC27: PR 8130 was abandoned; the row is back to building") {
		t.Fatalf("posts %q", f.posts)
	}
	f.bus(t, "2026-11-10T17:30 DONE demo:pilot-ac27 -> crius:lead — AC27 PR 8131 @ 0a0b0c0d")
	f.run(t, 4)
	err := Run(Deps{Home: f.home, Project: "demo", Now: t0.Add(5 * time.Minute),
		Spawn: func(string, string) error { return nil }, Close: func(string) error { return nil },
		Alive: func(string) (bool, error) { return true, nil }, Post: func(string, string, string) error { return nil },
		PRState: func(int) (string, error) { return "", errors.New("az: no answer in 30s") }})
	if err == nil || !strings.Contains(err.Error(), "PR 8131: az: no answer in 30s") {
		t.Fatalf("want the failed read said, got %v", err)
	}
}

// A session may ask its team lead, not only Crius (seen live 11-10 16:22: a pilot asked quotes:lead for a stack slot
// and got an ANSWER, then an ORDER). Any answer to the session from someone else unblocks its row; an INFO does not.
func TestAnAnswerFromTheTeamLeadUnblocks(t *testing.T) {
	f := newFake(t, 1)
	f.ready(t, "AC20")
	f.run(t, 1)
	f.bus(t, "2026-11-10T16:22 ASK demo:pilot-ac20 -> demo:lead — AC20: may I have the stack slot for the live re-check?")
	f.run(t, 2)
	f.bus(t, "2026-11-10T16:23 INFO demo:lead -> demo:pilot-ac20 — another session holds it; you are next")
	f.run(t, 3)
	if r := f.row(t, "AC20"); r.State != board.Blocked {
		t.Fatalf("an INFO unblocked it: %+v", r)
	}
	f.bus(t, "2026-11-10T16:24 ORDER demo:lead -> demo:pilot-ac20 — the slot is yours from 16:24")
	f.run(t, 4)
	if r := f.row(t, "AC20"); r.State != board.Building || r.Question != "" {
		t.Fatalf("after the ORDER %+v", r)
	}
}

// Review round 2 P1: a spawn that fails (or half-works: the tab opened, the proof missed) has claimed its row already,
// so the next run never opens another tab for it; the row gets one restart, then waits on Crius. At most two tabs.
// A failing row does not stop the rows behind it.
func TestAFailedSpawnClaimsTheRowAndOpensAtMostTwoTabs(t *testing.T) {
	f := newFake(t, 2)
	f.allowErr = true
	f.ready(t, "AC1", "src/a/**")
	f.ready(t, "AC2", "src/b/**")
	f.failing["pilot-ac1"] = true
	f.run(t, 1)
	if f.err == nil || !strings.Contains(f.err.Error(), "AC1: starting demo:pilot-ac1: the first prompt did not show") {
		t.Fatalf("want the failed spawn said, got %v", f.err)
	}
	if r := f.row(t, "AC1"); r.State != board.Building {
		t.Fatalf("the row was not claimed: %+v", r)
	}
	if r := f.row(t, "AC2"); r.State != board.Building {
		t.Fatalf("AC1's failure stopped AC2: %+v", r)
	}
	for m := 2; m <= 12; m++ {
		f.run(t, m)
	}
	n := 0
	for _, s := range f.spawned {
		if strings.HasPrefix(s, "pilot-ac1:") {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("want 2 tabs for AC1 (start, one restart), got %d: %q", n, f.spawned)
	}
	if r := f.row(t, "AC1"); r.State != board.Blocked {
		t.Fatalf("AC1 %+v", r)
	}
}

// Review round 2 P2-7: no spawn runs while the board is locked, so crius-board set from a session never waits on it.
func TestNoSpawnRunsUnderTheBoardLock(t *testing.T) {
	f := newFake(t, 1)
	f.ready(t, "AC1")
	path := filepath.Join(f.home, "state", "demo", "BOARD.toml")
	var lockErr error
	f.during = func() {
		done := make(chan error, 1)
		go func() { done <- board.Update(path, t0, func(*board.Board) error { return nil }) }()
		select {
		case lockErr = <-done:
		case <-time.After(2 * time.Second):
			lockErr = errors.New("the board stayed locked during the spawn")
		}
	}
	f.run(t, 1)
	if lockErr != nil {
		t.Fatal(lockErr)
	}
}

// Review round 2 P2-2: a session that asked, then went on and opened its PR, is followed to its PR; a VERIFIED that
// comes while the row is still in pr (az is slow) proves it all the same.
func TestLinesInAnOddStateAreNotLost(t *testing.T) {
	f := newFake(t, 1)
	f.ready(t, "AC20")
	f.run(t, 1)
	f.bus(t,
		"2026-11-10T16:09 ASK demo:pilot-ac20 -> demo:lead — AC20: may I have the slot?",
		"2026-11-10T16:20 DONE demo:pilot-ac20 -> crius:lead — AC20 PR #9002 @ 1a2b3c4d")
	f.run(t, 2)
	if r := f.row(t, "AC20"); r.State != board.PR || r.PR != 9002 {
		t.Fatalf("the DONE of a blocked session was lost: %+v", r)
	}
	f.bus(t, "2026-11-10T16:30 VERIFIED crius:lead -> demo:pilot-ac20 — row AC20 · 1a2b3c4d · live on the merged code")
	f.run(t, 3)
	if r := f.row(t, "AC20"); r.State != board.Proven || strings.Join(f.closed, ",") != "pilot-ac20" {
		t.Fatalf("a VERIFIED on a pr row was lost: %+v closed %q", r, f.closed)
	}
}

// Review round 2 P2-3: Crius's answer to a row blocked as "ended twice" starts its session again.
func TestAnAnswerAfterTwoEndsStartsItAgain(t *testing.T) {
	f := newFake(t, 1)
	f.ready(t, "AC8")
	f.run(t, 1)
	for _, m := range []int{2, 4, 5, 7} {
		f.dead["pilot-ac8"] = true
		f.run(t, m)
	}
	if r := f.row(t, "AC8"); r.State != board.Blocked {
		t.Fatalf("setup: %+v", r)
	}
	f.bus(t, "2026-11-10T17:00 DECISION crius:lead -> demo:pilot-ac8 — AC8: the rig is back, go on")
	f.run(t, 8)
	if r := f.row(t, "AC8"); r.State != board.Building || r.Restarts != 1 || len(f.spawned) != 3 {
		t.Fatalf("after the answer %+v spawned %q", r, f.spawned)
	}
}

// Review round 2 P2-6 and P3: "partial" in product words does not block; PARTIAL does. A VERIFIED names its row; one
// on a ready row (proven by its check alone, no session) closes nothing.
func TestPartialWordsAndVerifiedForTheRow(t *testing.T) {
	f := newFake(t, 2)
	f.ready(t, "AC6", "src/a/**")
	f.ready(t, "AC7", "src/b/**")
	f.ready(t, "AC9", "src/c/**")
	f.run(t, 1)
	f.bus(t,
		"2026-11-10T16:20 DONE demo:pilot-ac6 -> crius:lead — AC6 partial refunds show on the invoice; PR 9006",
		"2026-11-10T16:21 VERIFIED crius:lead -> demo:pilot-ac7 — row AC6 · the screenshot of AC6",
		"2026-11-10T16:22 VERIFIED crius:lead -> demo:pilot-ac9 — row AC9 · already built, the check passes")
	f.run(t, 2)
	if r := f.row(t, "AC6"); r.State != board.PR || r.PR != 9006 {
		t.Fatalf("AC6 %+v", r)
	}
	if r := f.row(t, "AC7"); r.State != board.Building {
		t.Fatalf("a VERIFIED naming another row proved AC7: %+v", r)
	}
	if r := f.row(t, "AC9"); r.State != board.Proven || len(f.closed) != 0 {
		t.Fatalf("AC9 %+v closed %q", r, f.closed)
	}
}

// Review round 2 P3: the first run on a project starts at the end of its bus (old lines of an earlier pilot never move
// rows seeded at their current state); a cursor that cannot be read is an error, never a replay of the whole bus.
func TestTheFirstRunStartsAtTheEndAndABadCursorIsAnError(t *testing.T) {
	f := newFake(t, 1)
	f.bus(t, "2026-11-10T15:00 ASK demo:pilot-ac1 -> crius:lead — AC1: an old question")
	f.ready(t, "AC1")
	f.run(t, 1)
	if r := f.row(t, "AC1"); r.State != board.Building {
		t.Fatalf("an old line moved the row: %+v", r)
	}
	if err := os.WriteFile(filepath.Join(f.home, "state", "demo", "dispatch.cursor"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.allowErr = true
	f.run(t, 2)
	if f.err == nil || !strings.Contains(f.err.Error(), "dispatch.cursor") {
		t.Fatalf("want the bad cursor said, got %v", f.err)
	}
}

// Review round 2 P2-4: only Crius or the project's lead, writing straight to the session, unblocks it; a cc or a line
// from another member does not.
func TestOnlyADirectAnswerFromALeadUnblocks(t *testing.T) {
	f := newFake(t, 1)
	f.ready(t, "AC20")
	f.run(t, 1)
	f.bus(t, "2026-11-10T16:22 ASK demo:pilot-ac20 -> crius:lead — AC20: may I add a rig row?",
		"2026-11-10T16:23 ORDER demo:qa -> demo:pilot-ac20 — rebase first",
		"2026-11-10T16:24 DECISION crius:lead -> demo:other (cc demo:pilot-ac20) — a rule for everyone")
	f.run(t, 2)
	if r := f.row(t, "AC20"); r.State != board.Blocked {
		t.Fatalf("a cc or another member unblocked it: %+v", r)
	}
}

// Review round 3 P2-1: the cursor moves only after the board is saved, so a board write that fails (a full disk)
// loses no bus line: the next run reads it again.
func TestAFailedBoardWriteLosesNoLine(t *testing.T) {
	f := newFake(t, 1)
	f.ready(t, "AC20")
	f.run(t, 1)
	f.bus(t, "2026-11-10T17:10 DONE demo:pilot-ac20 -> crius:lead — AC20 PR #57 @ 1a2b3c4d")
	blocker := filepath.Join(f.home, "state", "demo", "BOARD.toml.new")
	if err := os.MkdirAll(filepath.Join(blocker, "x"), 0o755); err != nil { // the board's new file cannot be written
		t.Fatal(err)
	}
	f.allowErr = true
	f.run(t, 2)
	if f.err == nil {
		t.Fatal("the failed board write was not said")
	}
	if err := os.RemoveAll(filepath.Join(f.home, "state", "demo", "BOARD.toml.new")); err != nil {
		t.Fatal(err)
	}
	f.allowErr = false
	f.run(t, 3)
	if r := f.row(t, "AC20"); r.State != board.PR || r.PR != 57 {
		t.Fatalf("the DONE was lost: %+v", r)
	}
}

// Review round 3 P3-3: closing a session runs with no board lock held, like a spawn.
func TestNoCloseRunsUnderTheBoardLock(t *testing.T) {
	f := newFake(t, 1)
	f.ready(t, "AC1")
	f.run(t, 1)
	f.bus(t, "2026-11-10T17:10 VERIFIED crius:lead -> demo:pilot-ac1 — row AC1 · 1a2b3c4d · walked live")
	path := filepath.Join(f.home, "state", "demo", "BOARD.toml")
	var lockErr error
	f.during = func() {
		done := make(chan error, 1)
		go func() { done <- board.Update(path, t0, func(*board.Board) error { return nil }) }()
		select {
		case lockErr = <-done:
		case <-time.After(2 * time.Second):
			lockErr = errors.New("the board stayed locked during the close")
		}
	}
	f.run(t, 2)
	if lockErr != nil || strings.Join(f.closed, ",") != "pilot-ac1" {
		t.Fatal(lockErr, f.closed)
	}
}

// Review round 3 P3-1: a row answered back to ready and claimed carries no dead mark, so its new session gets the
// full grace.
func TestAClaimedRowHasNoDeadMark(t *testing.T) {
	f := newFake(t, 1)
	f.ready(t, "AC5")
	if err := board.Update(filepath.Join(f.home, "state", "demo", "BOARD.toml"), t0, func(b *board.Board) error {
		return b.Move("AC5", board.Blocked, "test", "", t0, func(r *board.Row) { r.Question = "which list?" })
	}); err != nil {
		t.Fatal(err)
	}
	f.run(t, 1)
	f.bus(t, "2026-11-10T16:02 DECISION crius:lead -> demo:pilot-ac5 — AC5: the seller list")
	f.run(t, 2)
	if r := f.row(t, "AC5"); r.State != board.Building || r.Dead != nil {
		t.Fatalf("%+v", r)
	}
}

// Review round 3 P3-5: a DONE that names two PR numbers is not guessed at: the row stays and Crius is asked.
func TestADoneWithTwoPRsAsksCrius(t *testing.T) {
	f := newFake(t, 1)
	f.ready(t, "AC20")
	f.run(t, 1)
	f.bus(t, "2026-11-10T17:10 DONE demo:pilot-ac20 -> crius:lead — AC20 rebased on PR #41, opened PR #57")
	f.run(t, 2)
	if r := f.row(t, "AC20"); r.State != board.Building {
		t.Fatalf("%+v", r)
	}
	if !strings.Contains(strings.Join(f.posts, "\n"), "NEED crius:lead: AC20: its DONE names PR 41 and PR 57; set the right one with crius-board demo set AC20 pr --pr <n>") {
		t.Fatalf("posts %q", f.posts)
	}
}

func (f *fake) merges() []string {
	var m []string
	for _, p := range f.posts {
		if strings.HasPrefix(p, "MERGE ") {
			m = append(m, p)
		}
	}
	return m
}

// Plan step 3 (crius:lead DECISION 11-10 17:04: no program completes PRs yet): the queue names to the project's lead one
// PR at a time, the oldest (lowest number) whose build passed on its current merge, and says it once; the next one
// only after that one leaves the queue. A new head on the named PR is a new merge to name.
func TestTheMergeQueueNamesTheOldestGreenPROnce(t *testing.T) {
	f := newFake(t, 3)
	for _, id := range []string{"AC1", "AC2", "AC3"} {
		f.ready(t, id, "src/"+id+".go")
	}
	f.run(t, 1)
	f.proofs[8001] = "STALE pr=8001 head=aaaaaaaa merge=a1a1a1a1 build=4 result=target-moved"
	f.proofs[8002] = "PASS pr=8002 head=bbbbbbbb merge=b1b1b1b1 build=5 result=succeeded suites=X 1/1"
	f.proofs[8003] = "PASS pr=8003 head=cccccccc merge=c1c1c1c1 build=6 result=succeeded suites=X 1/1"
	f.bus(t, "2026-11-10T17:00 DONE demo:pilot-ac1 -> crius:lead — AC1 PR 8003 @ cccccccc",
		"2026-11-10T17:00 DONE demo:pilot-ac2 -> crius:lead — AC2 PR 8001 @ aaaaaaaa",
		"2026-11-10T17:00 DONE demo:pilot-ac3 -> crius:lead — AC3 PR 8002 @ bbbbbbbb")
	f.run(t, 2) // the rows reach pr in this run; their PRs are read from the next one
	if m := f.merges(); len(m) != 0 {
		t.Fatalf("named a PR before reading its proof: %q", m)
	}
	f.run(t, 3)
	want := "MERGE demo:lead: merge queue: next AC3 PR 8002 @ bbbbbbbb (build 5 PASS): yours to merge. After it: AC2 PR 8001 STALE (target-moved), AC1 PR 8003 PASS."
	if m := f.merges(); len(m) != 1 || m[0] != want {
		t.Fatalf("merges\n got %q\nwant %q", m, want)
	}
	if !slices.Contains(f.proofed, "8002 42") {
		t.Fatalf("Proof not given the pipeline: %q", f.proofed)
	}
	f.run(t, 4)
	if m := f.merges(); len(m) != 1 {
		t.Fatalf("said twice: %q", m)
	}
	f.prs[8002] = "completed"
	f.run(t, 5) // the run that sees the merge decides nothing (review P2-2)
	if m := f.merges(); len(m) != 1 {
		t.Fatalf("decided in the run that saw the merge: %q", m)
	}
	f.run(t, 6)
	if m := f.merges(); len(m) != 2 || !strings.Contains(m[1], "next AC1 PR 8003 @ cccccccc (build 6 PASS)") || !strings.Contains(m[1], "After it: AC2 PR 8001 STALE (target-moved).") {
		t.Fatalf("after the merge: %q", m)
	}
	f.proofs[8003] = "PASS pr=8003 head=dddddddd merge=d1d1d1d1 build=7 result=succeeded suites=X 1/1"
	f.run(t, 7)
	if m := f.merges(); len(m) != 3 || !strings.Contains(m[2], "next AC1 PR 8003 @ dddddddd (build 7 PASS)") {
		t.Fatalf("a new head: %q", m)
	}
	f.prs[8003] = "completed"
	f.run(t, 8)
	f.run(t, 9)
	if m := f.merges(); len(m) != 4 || m[3] != "MERGE demo:lead: merge queue: none ready: AC2 PR 8001 STALE (target-moved)." {
		t.Fatalf("none ready after a merge: %q", m)
	}
	f.run(t, 10)
	if m := f.merges(); len(m) != 4 {
		t.Fatalf("none ready said twice: %q", m)
	}
}

// A PR still building wakes nobody: "none ready" is said only when something was ready before.
func TestNoneReadyIsQuietUntilSomethingWasReady(t *testing.T) {
	f := newFake(t, 1)
	f.ready(t, "AC1")
	f.run(t, 1)
	f.proofs[8001] = "NONE pr=8001 head=aaaaaaaa merge=a1a1a1a1 build=9 result=inProgress"
	f.bus(t, "2026-11-10T17:00 DONE demo:pilot-ac1 -> crius:lead — AC1 PR 8001 @ aaaaaaaa")
	f.run(t, 2)
	f.run(t, 3)
	if m := f.merges(); len(m) != 0 {
		t.Fatalf("woke the lead for a build in progress: %q", m)
	}
}

// Failing to look, or to tell, is never taken as said: the run errs and the next run tries again.
func TestTheQueueRetriesWhatFailed(t *testing.T) {
	f := newFake(t, 1)
	f.ready(t, "AC1")
	f.run(t, 1)
	f.proofs[8001] = "PASS pr=8001 head=aaaaaaaa merge=a1a1a1a1 build=9 result=succeeded suites=X 1/1"
	f.bus(t, "2026-11-10T17:00 DONE demo:pilot-ac1 -> crius:lead — AC1 PR 8001 @ aaaaaaaa")
	f.run(t, 2)
	f.allowErr = true
	f.proofErr = errors.New("LOUD ci-proof: PR 8001: cannot read the PR: az: no answer")
	f.run(t, 3)
	if f.err == nil || !strings.Contains(f.err.Error(), "AC1 PR 8001: LOUD ci-proof") || len(f.merges()) != 0 {
		t.Fatalf("err %v merges %q", f.err, f.merges())
	}
	f.proofErr, f.postErr = nil, errors.New("bus is read-only")
	f.run(t, 4)
	if f.err == nil || !strings.Contains(f.err.Error(), "bus is read-only") {
		t.Fatalf("a failed post not said: %v", f.err)
	}
	f.postErr = nil
	f.run(t, 5)
	if f.err != nil || len(f.merges()) != 1 {
		t.Fatalf("not tried again: err %v merges %q", f.err, f.merges())
	}
	// a PR that is not active is not read
	f.proofed = nil
	f.prs[8001] = "completed"
	f.run(t, 6)
	if len(f.proofed) != 0 {
		t.Fatalf("read the proof of a completed PR: %q", f.proofed)
	}
}

// Review P2-1, P2-3: the named PR stays named until it leaves pr. A lower PR turning green, or the named one reading
// NONE for a minute while its merge is computed, says nothing; only a new head or build on it is said again.
func TestTheNamedPRStaysNamed(t *testing.T) {
	f := newFake(t, 2)
	f.ready(t, "AC1", "src/a.go")
	f.ready(t, "AC2", "src/b.go")
	f.run(t, 1)
	f.proofs[8001] = "NONE pr=8001 head=aaaaaaaa merge=a1a1a1a1 build=4 result=inProgress"
	f.proofs[8002] = "PASS pr=8002 head=bbbbbbbb merge=b1b1b1b1 build=5 result=succeeded suites=X 1/1"
	f.bus(t, "2026-11-10T17:00 DONE demo:pilot-ac1 -> crius:lead — AC1 PR 8001 @ aaaaaaaa",
		"2026-11-10T17:00 DONE demo:pilot-ac2 -> crius:lead — AC2 PR 8002 @ bbbbbbbb")
	f.run(t, 2)
	f.run(t, 3)
	if m := f.merges(); len(m) != 1 || !strings.Contains(m[0], "next AC2 PR 8002 @ bbbbbbbb") {
		t.Fatalf("first: %q", m)
	}
	f.proofs[8001] = "PASS pr=8001 head=aaaaaaaa merge=a1a1a1a1 build=4 result=succeeded suites=X 1/1"
	f.run(t, 4)
	f.proofs[8002] = "NONE pr=8002 head=bbbbbbbb merge=b1b1b1b1 build=none result=merge-queued"
	f.run(t, 5)
	f.proofs[8002] = "PASS pr=8002 head=bbbbbbbb merge=b1b1b1b1 build=5 result=succeeded suites=X 1/1"
	f.run(t, 6)
	if m := f.merges(); len(m) != 1 {
		t.Fatalf("a second PR named while the first is in pr: %q", m)
	}
	f.proofs[8002] = "PASS pr=8002 head=eeeeeeee merge=e1e1e1e1 build=8 result=succeeded suites=X 1/1"
	f.run(t, 7)
	if m := f.merges(); len(m) != 2 || !strings.Contains(m[1], "next AC2 PR 8002 @ eeeeeeee (build 8 PASS)") {
		t.Fatalf("a new head on the named PR: %q", m)
	}
}

// Review P2-2: proofs read before a merge are measured against the old target; the run that sees a queued PR complete
// decides nothing, and the next run reads them again.
func TestARunThatSawAMergeDecidesNothing(t *testing.T) {
	f := newFake(t, 2)
	f.ready(t, "AC1", "src/a.go")
	f.ready(t, "AC2", "src/b.go")
	f.run(t, 1)
	f.proofs[8001] = "PASS pr=8001 head=aaaaaaaa merge=a1a1a1a1 build=4 result=succeeded suites=X 1/1"
	f.proofs[8002] = "PASS pr=8002 head=bbbbbbbb merge=b1b1b1b1 build=5 result=succeeded suites=X 1/1"
	f.bus(t, "2026-11-10T17:00 DONE demo:pilot-ac1 -> crius:lead — AC1 PR 8001 @ aaaaaaaa",
		"2026-11-10T17:00 DONE demo:pilot-ac2 -> crius:lead — AC2 PR 8002 @ bbbbbbbb")
	f.run(t, 2)
	f.run(t, 3)
	f.prs[8001] = "completed"
	f.run(t, 4)
	if m := f.merges(); len(m) != 1 {
		t.Fatalf("named a PR on a proof read before the merge: %q", m)
	}
	f.proofs[8002] = "STALE pr=8002 head=bbbbbbbb merge=b1b1b1b1 build=5 result=target-moved"
	f.run(t, 5)
	if m := f.merges(); len(m) != 2 || m[1] != "MERGE demo:lead: merge queue: none ready: AC2 PR 8002 STALE (target-moved)." {
		t.Fatalf("after the merge: %q", m)
	}
}

// Review P3-2: a PR state the dispatcher does not know is said, never a queue that waits in silence.
func TestAnUnknownPRStateIsAnError(t *testing.T) {
	f := newFake(t, 1)
	f.ready(t, "AC1")
	f.run(t, 1)
	f.bus(t, "2026-11-10T17:00 DONE demo:pilot-ac1 -> crius:lead — AC1 PR 8001 @ aaaaaaaa")
	f.run(t, 2)
	f.prs[8001] = "notSet"
	f.allowErr = true
	f.run(t, 3)
	if f.err == nil || !strings.Contains(f.err.Error(), `AC1 PR 8001: state "notSet"`) {
		t.Fatalf("err %v", f.err)
	}
}

// Review r2 P2-A: the hold is only for a NONE on the named head (its merge or build being worked out). A FAIL, a STALE
// or a new head that is not yet green ends the name, and the lead hears it: the earlier "yours to merge" is void.
func TestANamedPRThatFailsOrMovesIsNoLongerNamed(t *testing.T) {
	f := newFake(t, 2)
	f.ready(t, "AC1", "src/a.go")
	f.ready(t, "AC2", "src/b.go")
	f.run(t, 1)
	f.proofs[8001] = "NONE pr=8001 head=aaaaaaaa merge=a1a1a1a1 build=4 result=inProgress"
	f.proofs[8002] = "PASS pr=8002 head=bbbbbbbb merge=b1b1b1b1 build=5 result=succeeded suites=X 1/1"
	f.bus(t, "2026-11-10T17:00 DONE demo:pilot-ac1 -> crius:lead — AC1 PR 8001 @ aaaaaaaa",
		"2026-11-10T17:00 DONE demo:pilot-ac2 -> crius:lead — AC2 PR 8002 @ bbbbbbbb")
	f.run(t, 2)
	f.run(t, 3)
	f.proofs[8002] = "FAIL pr=8002 head=cccccccc merge=c1c1c1c1 build=6 result=failed suites=X 0/1 (1 failed)"
	f.run(t, 4)
	want := "MERGE demo:lead: merge queue: none ready: AC1 PR 8001 NONE (inProgress), AC2 PR 8002 FAIL (failed)."
	if m := f.merges(); len(m) != 2 || m[1] != want {
		t.Fatalf("a failed named PR\n got %q\nwant %q", m, want)
	}
	f.proofs[8001] = "PASS pr=8001 head=aaaaaaaa merge=a1a1a1a1 build=4 result=succeeded suites=X 1/1"
	f.run(t, 5)
	if m := f.merges(); len(m) != 3 || !strings.Contains(m[2], "next AC1 PR 8001 @ aaaaaaaa (build 4 PASS)") {
		t.Fatalf("the next green one: %q", m)
	}
	f.proofs[8001] = "NONE pr=8001 head=dddddddd merge=d1d1d1d1 build=9 result=inProgress" // a new head, building
	f.run(t, 6)
	if m := f.merges(); len(m) != 4 || !strings.HasPrefix(m[3], "MERGE demo:lead: merge queue: none ready: AC1 PR 8001 NONE (inProgress)") {
		t.Fatalf("a new head not yet green: %q", m)
	}
}

// Review r2 P3-A: a run in which a row that was in pr left it (here a VERIFIED before az says merged) decides
// nothing: the target may have moved under the proofs it read.
func TestARunInWhichARowLeftPRDecidesNothing(t *testing.T) {
	f := newFake(t, 2)
	f.ready(t, "AC1", "src/a.go")
	f.ready(t, "AC2", "src/b.go")
	f.run(t, 1)
	f.proofs[8001] = "PASS pr=8001 head=aaaaaaaa merge=a1a1a1a1 build=4 result=succeeded suites=X 1/1"
	f.proofs[8002] = "PASS pr=8002 head=bbbbbbbb merge=b1b1b1b1 build=5 result=succeeded suites=X 1/1"
	f.bus(t, "2026-11-10T17:00 DONE demo:pilot-ac1 -> crius:lead — AC1 PR 8001 @ aaaaaaaa",
		"2026-11-10T17:00 DONE demo:pilot-ac2 -> crius:lead — AC2 PR 8002 @ bbbbbbbb")
	f.run(t, 2)
	f.run(t, 3)
	f.bus(t, "2026-11-10T17:10 VERIFIED crius:lead -> demo:pilot-ac1 — AC1 walked live on the merged code")
	f.run(t, 4)
	if m := f.merges(); len(m) != 1 {
		t.Fatalf("named a PR in the run a row left pr: %q", m)
	}
	f.run(t, 5)
	if m := f.merges(); len(m) != 2 || !strings.Contains(m[1], "next AC2 PR 8002") {
		t.Fatalf("the next run: %q", m)
	}
}

// Review r3 P2-1, P2-2: the key holds no state, so a STALE or FAIL on the named head and build (the usual state after
// a merge into the target) must still end the name; a NONE holds it only while its merge or build is in progress, never
// on a merge conflict or with no run on the merge.
func TestOnlyAPassOrAnInProgressNoneHoldsTheName(t *testing.T) {
	for _, c := range []struct{ name, line, want string }{
		{"stale on the same build", "STALE pr=8001 head=aaaaaaaa merge=a1a1a1a1 build=4 result=target-moved", "AC1 PR 8001 STALE (target-moved)"},
		{"fail on the same build", "FAIL pr=8001 head=aaaaaaaa merge=a1a1a1a1 build=4 result=failed suites=X 0/1", "AC1 PR 8001 FAIL (failed)"},
		{"conflicts", "NONE pr=8001 head=aaaaaaaa merge=a1a1a1a1 build=none result=merge-conflicts", "AC1 PR 8001 NONE (merge-conflicts)"},
		{"no run", "NONE pr=8001 head=aaaaaaaa merge=a1a1a1a1 build=none result=no-run-on-merge", "AC1 PR 8001 NONE (no-run-on-merge)"},
	} {
		f := newFake(t, 1)
		f.ready(t, "AC1")
		f.run(t, 1)
		f.proofs[8001] = "PASS pr=8001 head=aaaaaaaa merge=a1a1a1a1 build=4 result=succeeded suites=X 1/1"
		f.bus(t, "2026-11-10T17:00 DONE demo:pilot-ac1 -> crius:lead — AC1 PR 8001 @ aaaaaaaa")
		f.run(t, 2)
		f.run(t, 3)
		f.proofs[8001] = c.line
		f.run(t, 4)
		if m := f.merges(); len(m) != 2 || m[1] != "MERGE demo:lead: merge queue: none ready: "+c.want+"." {
			t.Errorf("%s: %q", c.name, m)
		}
	}
	for _, res := range []string{"inProgress", "notStarted", "merge-queued"} {
		f := newFake(t, 1)
		f.ready(t, "AC1")
		f.run(t, 1)
		f.proofs[8001] = "PASS pr=8001 head=aaaaaaaa merge=a1a1a1a1 build=4 result=succeeded suites=X 1/1"
		f.bus(t, "2026-11-10T17:00 DONE demo:pilot-ac1 -> crius:lead — AC1 PR 8001 @ aaaaaaaa")
		f.run(t, 2)
		f.run(t, 3)
		f.proofs[8001] = "NONE pr=8001 head=aaaaaaaa merge=a1a1a1a1 build=7 result=" + res
		f.run(t, 4)
		if m := f.merges(); len(m) != 1 {
			t.Errorf("%s did not hold: %q", res, m)
		}
	}
}
