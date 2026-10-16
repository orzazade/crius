package digest

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const good = `# Status: falcon

Some notes before the block.

## GOAL (2026-10-17 02:40)
Goal: Falcon is Crius's senses.
- [x] Runs can see and think · 9c644de, bus 10-15T11:05
- [~] Sources flow again · mail · run 24628
- [ ] Mail stays live for 7 days

Done: 33% (1 of 3 milestones, equal weight)
Next: Sources flow again
Needs owner: nothing

## Open
- [ ] not a milestone of the block
`

// Step 5 P2: the GOAL block of RULES 3.1 is read field by field; prose before it and sections after it are not.
func TestParseGoalReadsTheBlock(t *testing.T) {
	g, err := ParseGoal(good)
	if err != nil {
		t.Fatal(err)
	}
	want := &Goal{
		Updated: time.Date(2026, 10, 17, 2, 40, 0, 0, time.Local),
		Goal:    "Falcon is Crius's senses.",
		Milestones: []Milestone{
			{State: Done, Text: "Runs can see and think", Refs: "9c644de, bus 10-15T11:05"},
			{State: Doing, Text: "Sources flow again · mail", Refs: "run 24628"},
			{State: Todo, Text: "Mail stays live for 7 days"},
		},
		Done: 33, DoneHow: "1 of 3 milestones, equal weight", Next: "Sources flow again", NeedsOwner: "nothing",
	}
	if !reflect.DeepEqual(g, want) {
		t.Fatalf("%+v", g)
	}
}

// No block is LOST (RULES 3.3); a block outside the grammar is unreadable with the line and why, never a guess.
func TestParseGoalTellsLostFromUnreadable(t *testing.T) {
	if _, err := ParseGoal("# Status\n\n**Goal:** prose, no block\n"); !errors.Is(err, ErrLost) {
		t.Fatal(err)
	}
	for _, c := range []struct{ old, new, why string }{
		{"## GOAL (2026-10-17 02:40)", "## GOAL (updated 2026-10-16)", "line 5: heading needs (YYYY-MM-DD HH:MM)"},
		{"## GOAL (2026-10-17 02:40)", "## GOAL (updated 2026-10-17 02:40)", "line 5: heading needs (YYYY-MM-DD HH:MM)"},
		{"Goal: Falcon is Crius's senses.\n", "", "line 6: want Goal:"},
		{"Goal: Falcon is Crius's senses.", "Goal: \t", "line 6: want Goal:"},
		{"- [ ] Mail stays live for 7 days", "  wrapped milestone text", "line 9: not in the grammar"},
		{"- [x] Runs can see and think · 9c644de, bus 10-15T11:05", "- [x] Runs can see and think", "line 7: a done milestone needs refs after ·"},
		{"- [x] Runs", "- [v] Runs", "line 7: not in the grammar"},
		{"Done: 33% (1 of 3 milestones, equal weight)", "Done: 33%", "line 11: want Done: N% (how it was counted)"},
		{"Done: 33% (", "Done: 133% (", "line 11: want Done: N% (how it was counted)"},
		{"Next: Sources flow again\n", "", "line 12: want Next:"},
		{"Needs owner: nothing\n", "", "line 14: want Needs owner:"},
		{"- [x] Runs can see and think · 9c644de, bus 10-15T11:05\n- [~] Sources flow again · mail · run 24628\n- [ ] Mail stays live for 7 days\n", "", "line 8: want at least one milestone"},
	} {
		if !strings.Contains(good, c.old) {
			t.Fatal("bad case", c.old)
		}
		_, err := ParseGoal(strings.Replace(good, c.old, c.new, 1))
		var u *Unreadable
		if !errors.As(err, &u) || u.Error() != "GOAL block unreadable: "+c.why {
			t.Errorf("%q: %v", c.new, err)
		}
	}
}

// A file that cannot be read is a failure to look, not LOST (shared rules: "found nothing" and "failed to look"
// are different states).
func TestReadGoalFailsLoudOnAnUnreadableFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadGoal(filepath.Join(dir, "missing.md")); err == nil || errors.Is(err, ErrLost) || !strings.Contains(err.Error(), "missing.md") {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "STATUS.md")
	if err := os.WriteFile(p, []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	if g, err := ReadGoal(p); err != nil || g.Done != 33 {
		t.Fatal(g, err)
	}
}
