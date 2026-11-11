package wakewatch

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/testutil"
)

// Crius moves from its cmux tab into a T3 Code thread (owner, 2026-11-11). With ~/.crius/state/crius/t3-thread in place
// there is no cmux tab titled crius on purpose: the "Crius's own tab stuck 10 min" push would fire every 30 min for
// nothing. The check is left out, said once as an INFO on crius.log, and no push goes to the owner.
func TestCriusInAT3ThreadSkipsTheCmuxTabStuckCheck(t *testing.T) {
	home := promptHome(t)
	testutil.Write(t, filepath.Join(home, "state", "crius", "t3-thread"), "mcp:5bba4b79-b3e9-4b48-954e-0f5fabcd580b\n")
	f := &promptFake{tabs: []Screen{opsIdle()}} // no tab titled crius
	for min := 0; min <= 45; min++ {
		f.run(t, home, min)
	}
	if len(f.pushes) != 0 {
		t.Fatalf("pushed the owner about Crius's cmux tab while Crius runs in a T3 thread: %q", f.pushes)
	}
	b, err := os.ReadFile(filepath.Join(home, "bus", "crius.log"))
	if err != nil {
		t.Fatal(err)
	}
	var info []string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.Contains(l, "INFO") && strings.Contains(l, "mcp:5bba4b79-b3e9-4b48-954e-0f5fabcd580b") {
			info = append(info, l)
		}
	}
	if len(info) != 1 || !strings.Contains(info[0], "T3") {
		t.Fatalf("want one INFO line naming the T3 thread, got %q in %q", info, b)
	}
	// a new thread id is said again
	testutil.Write(t, filepath.Join(home, "state", "crius", "t3-thread"), "mcp:second\n")
	f.run(t, home, 46)
	f.run(t, home, 47)
	if b, _ := os.ReadFile(filepath.Join(home, "bus", "crius.log")); strings.Count(string(b), "mcp:second") != 1 {
		t.Fatalf("want one INFO for the new thread id, got %q", b)
	}
}

// Review P1-1: with the tab check off, a T3 thread that does not take Crius's messages (the app closed, t3's link
// broken, the thread gone, a thread file with no id) must still reach the owner: the same push after 10 min the stuck
// tab got. With the file gone, the cmux tab check is back.
func TestCriusInAT3ThreadThatTakesNothingPushesTheOwner(t *testing.T) {
	held := func() (time.Time, string, error) {
		return t0, "t3 thread send mcp:abc: exit status 1: Thread mcp:abc was not found.", nil
	}
	for _, c := range []struct {
		name, file string
		held       func() (time.Time, string, error)
		want       string
	}{
		{"t3 fails", "mcp:abc\n", held, "Crius's T3 Code thread mcp:abc has not taken a message held since"},
		{"outbox unread", "mcp:abc\n", func() (time.Time, string, error) { return time.Time{}, "", errors.New("permission denied") },
			"Crius's outbox cannot be read, so its T3 Code thread mcp:abc is not checked: permission denied"},
		{"bad file", "\n", nil, "Crius's T3 Code thread cannot be named, so its messages are held"},
	} {
		home := promptHome(t)
		testutil.Write(t, filepath.Join(home, "state", "crius", "t3-thread"), c.file)
		f := &promptFake{tabs: []Screen{opsIdle()}, held: c.held}
		for min := 0; min <= 9; min++ {
			f.run(t, home, min)
		}
		if len(f.pushes) != 0 {
			t.Fatalf("%s: pushed before 10 min: %q", c.name, f.pushes)
		}
		f.run(t, home, 10)
		if len(f.pushes) != 1 || !strings.Contains(f.pushes[0], c.want) || !strings.Contains(f.pushes[0], "stuck 10 min") {
			t.Fatalf("%s: want one push %q, got %q", c.name, c.want, f.pushes)
		}
	}
	// nothing held: no push; the file removed: the tab check is back (no tab titled crius)
	home := promptHome(t)
	testutil.Write(t, filepath.Join(home, "state", "crius", "t3-thread"), "mcp:abc\n")
	f := &promptFake{tabs: []Screen{opsIdle()}, held: func() (time.Time, string, error) { return time.Time{}, "", nil }}
	for min := 0; min <= 15; min++ {
		f.run(t, home, min)
	}
	if len(f.pushes) != 0 {
		t.Fatalf("pushed with nothing held: %q", f.pushes)
	}
	if err := os.Remove(filepath.Join(home, "state", "crius", "t3-thread")); err != nil {
		t.Fatal(err)
	}
	for min := 16; min <= 26; min++ {
		f.run(t, home, min)
	}
	if len(f.pushes) != 1 || !strings.Contains(f.pushes[0], "no cmux tab titled crius): stuck 10 min") {
		t.Fatalf("with the file gone, want the tab check's push, got %q", f.pushes)
	}
}
