package wakewatch

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/sessionstate"
	"github.com/orzazade/crius/internal/testutil"
)

// A dead role of a team whose bus log here is a copy of a bus on another machine: the check did look, so it is no
// "failed to look", on any run. As with no bus log, Crius alone is told, once, with the reason its lead was not.
func TestADeadRoleOfACopiedBusTellsCriusAloneAndIsNoFailedLook(t *testing.T) {
	const pulled = "2026-11-18T09:00 INFO far:lead -> crius — the last pulled line\n"
	home := setup(t, map[string]string{"far.log": pulled})
	testutil.Write(t, filepath.Join(home, "bus", "far.log.remote"), "use /opt/wrap/bus-post-far (live log is on another machine)\n")
	testutil.Write(t, filepath.Join(home, "projects.toml"), "[projects.far]\nleads = { lead = \"/nowhere/STATUS.md\" }\n")
	fileRole(t, home, sessionstate.State{SessionID: "s1", Project: "far", Role: "qa", Time: t0.Add(-time.Hour),
		State: sessionstate.Idle})
	f := &fake{}
	deadAt(t, home, f, func(m int) []Session {
		if m == 0 {
			return []Session{{Name: "far · qa", ID: "s1"}}
		}
		return nil
	}, 0, 16, 21, 36, 41, 56)
	for _, s := range f.sent {
		if strings.Contains(s, "the dead-role check failed to look") {
			t.Errorf("a copied bus is no failed look: %q", s)
		}
	}
	if got := testutil.Read(t, filepath.Join(home, "bus", "far.log")); got != pulled {
		t.Errorf("the copy was written:\n%s", got)
	}
	w := deadWakes(f)
	if len(w) != 1 || strings.Contains(w[0], "Told far:lead") || !strings.Contains(w[0], "far:lead was not told") ||
		!strings.Contains(w[0], "/opt/wrap/bus-post-far") {
		t.Fatalf("want one wake to Crius saying far:lead was not told and naming the wrapper, got %q", w)
	}
	t.Logf("to Crius: %s", w[0])
}
