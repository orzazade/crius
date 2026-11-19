package wakewatch_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/testutil"
	"github.com/orzazade/crius/internal/wakewatch"
)

// testdata/provider/thinkpad-builder-asks.txt is the 11-19 shape (reply text reworded, public repo): at 18:49 the Staffy
// builder, a Claude Code tab in tmux on the ThinkPad (staffy:builder), ended its turn on "❓ Need: …" in its own tab and
// posted no bus line for it. It sat idle 80 minutes until Crius saw it by chance: wakewatch's 10-min ask rule read only
// the Mac's cmux tabs. thinkpad-builder-waits.txt is the same tab at 11-20 00:05, a reply that names a decision for
// later and ends on ⏳.

// staffyBus is the Staffy bus as it stood at 18:50 on 11-19: the turn's last lines, none of them a NEED or an ASK.
const staffyBus = "2026-11-19T18:33 PASS staffy:qa -> staffy:builder,crius:lead — d4548058 · A17b race fix: green 44/44\n" +
	"2026-11-19T18:33 LANDED staffy:builder -> staffy:qa,crius:lead — A17b race fix: integration b07999ac -> d4548058\n" +
	"2026-11-19T18:33 VERIFIED staffy:qa -> staffy:builder,crius:lead — d4548058 · A17b race fix land\n"

const askAlert = `question unanswered for 10 min in tab thinkpad staffy:builder: first seen at 18:50, its last reply ends "❓ Need: where the test database copy is, or a go to make one."; no NEED, NEED-OWNER or ASK line from staffy:builder on the bus since 18:35`

func askHome(t *testing.T, bus string) string {
	t.Helper()
	home := t.TempDir()
	testutil.Write(t, wakewatch.ProviderTabsFile(home), "thinkpad staffy:builder\n")
	testutil.Write(t, filepath.Join(home, "bus", "staffy.log"), bus)
	return home
}

// 11-19 18:49: a ThinkPad tab whose last reply asks wakes Crius once, after 10 min with its screen unchanged, in the
// remote prompt's words; it is never typed into.
func TestRemoteTabAskWakesCriusOnce(t *testing.T) {
	home := askHome(t, staffyBus)
	f := &fakeSweep{screens: map[string]string{"thinkpad staffy:builder": pane(t, "thinkpad-builder-asks.txt")}}
	t0 := time.Date(2026, 11, 19, 18, 50, 0, 0, time.Local)
	sweep := func(min int) wakewatch.ProviderReport {
		t.Helper()
		rep, err := wakewatch.ProviderErrors(f.deps(home, t0.Add(time.Duration(min)*time.Minute)))
		if err != nil {
			t.Fatalf("sweep at +%d min: %v", min, err)
		}
		return rep
	}
	sweep(0)
	sweep(5)
	if len(f.crius) != 0 {
		t.Fatalf("open 5 min: crius %q", f.crius)
	}
	sweep(10)
	if len(f.crius) != 1 || f.crius[0] != askAlert || len(f.posts) != 1 || f.posts[0] != "ALERT "+askAlert {
		t.Fatalf("no wake for the ThinkPad tab that asked 10 min ago:\n crius %q\n posts %q\n want %q", f.crius, f.posts, askAlert)
	}
	for _, min := range []int{15, 20, 80} { // the same ask: one alert, not one per sweep
		sweep(min)
	}
	if len(f.crius) != 1 {
		t.Fatalf("the same ask was said again: %q", f.crius)
	}
	if len(f.sentTab) != 0 {
		t.Fatalf("an ask is never typed into: sent %q", f.sentTab)
	}

	// answered (the tab works on and ends ⏳), then a new ask: timed afresh, said once it is 10 min old
	f.screens["thinkpad staffy:builder"] = pane(t, "thinkpad-builder-waits.txt")
	sweep(85)
	f.screens["thinkpad staffy:builder"] = strings.Replace(pane(t, "thinkpad-builder-asks.txt"), "a go to make one", "a go to make a new one", 2)
	sweep(90)
	sweep(95)
	if len(f.crius) != 1 {
		t.Fatalf("a new ask open 5 min: %q", f.crius)
	}
	sweep(100)
	if len(f.crius) != 2 || !strings.HasPrefix(f.crius[1], "question unanswered for 10 min in tab thinkpad staffy:builder: first seen at 20:20,") {
		t.Fatalf("the new ask was not said: %q", f.crius)
	}
}

// A reply that ends ⏳ or ✅ never wakes Crius, even when it mentions a decision for later or holds ✅ further up.
func TestRemoteTabWaitingOrDoneNeverAlerts(t *testing.T) {
	asks := pane(t, "thinkpad-builder-asks.txt")
	for name, screen := range map[string]string{
		"⏳ (live 11-20 00:05)": pane(t, "thinkpad-builder-waits.txt"),
		"✅ at the end":         strings.Replace(asks, "❓ Need: where the test database copy is, or a go to make one.", "Next: A17d. ✅", 1),
		"⏳ at the end":         strings.Replace(asks, "❓ Need: where the test database copy is, or a go to make one.", "Waiting for qa. ⏳", 1),
	} {
		home := askHome(t, staffyBus)
		f := &fakeSweep{screens: map[string]string{"thinkpad staffy:builder": screen}}
		t0 := time.Date(2026, 11, 19, 18, 50, 0, 0, time.Local)
		for min := 0; min <= 90; min += 5 {
			if _, err := wakewatch.ProviderErrors(f.deps(home, t0.Add(time.Duration(min)*time.Minute))); err != nil {
				t.Fatalf("%s: sweep at +%d min: %v", name, min, err)
			}
		}
		if len(f.crius) != 0 || len(f.posts) != 0 {
			t.Errorf("%s: woke Crius: %q", name, f.crius)
		}
	}
}

// An ask the role also posted on the bus in that turn (a NEED, NEED-<ROLE> or ASK line from it) already woke Crius
// there: the tab says nothing more. A NEED from an earlier turn does not speak for this ask.
func TestRemoteTabAskWithABusLineIsQuiet(t *testing.T) {
	for _, c := range []struct {
		name, line string
		alerts     bool
	}{
		{"NEED-OWNER in the turn", "2026-11-19T18:49 NEED-OWNER staffy:builder -> crius:lead — A17c: where is the test DB copy?\n", false},
		{"NEED in the turn", "2026-11-19T18:48 NEED staffy:builder -> crius:lead — A17c needs a test DB copy\n", false},
		{"ASK in the turn", "2026-11-19T18:49 ASK staffy:builder -> crius:lead — test DB copy?\n", false},
		{"NEED posted after", "2026-11-19T18:57 NEED staffy:builder -> crius:lead — A17c needs a test DB copy\n", false},
		{"NEED-OWNER to its lead", "2026-11-19T18:49 NEED-OWNER staffy:builder -> staffy:lead — test DB copy?\n", false},
		{"NEED to Crius in a list", "2026-11-19T18:49 NEED staffy:builder -> staffy:qa,crius:lead — test DB copy?\n", false},
		{"NEED from qa", "2026-11-19T18:49 NEED staffy:qa -> crius:lead — a test DB copy\n", true},
		// review P1: a NEED to another role woke no one but that role; one Crius answered speaks for no later ask
		{"NEED to qa only", "2026-11-19T18:45 NEED staffy:builder -> staffy:qa — please check A17b\n", true},
		{"NEED Crius answered", "2026-11-19T18:40 NEED staffy:builder -> crius:lead — A17b gate?\n" +
			"2026-11-19T18:42 DECISION crius:lead -> staffy:builder — A17b: run the gate\n", true},
		{"NEED after Crius's line", "2026-11-19T18:40 DECISION crius:lead -> staffy:builder — A17b: run the gate\n" +
			"2026-11-19T18:49 NEED staffy:builder -> crius:lead — test DB copy?\n", false},
		// review round 2: Crius posting as bare "crius" answers too; a NEED-OWNER stays held until its row is closed
		{"NEED answered by bare crius", "2026-11-19T18:40 NEED staffy:builder -> crius:lead — A17b gate?\n" +
			"2026-11-19T18:42 DECISION crius -> staffy:builder — A17b: run the gate\n", true},
		{"NEED-OWNER Crius only acked", "2026-11-19T18:44 NEED-OWNER staffy:builder -> crius:lead — test DB copy?\n" +
			"2026-11-19T18:46 INFO crius:lead -> staffy:builder — queued as q3\n", false},
		{"NEED-OWNER closed", "2026-11-19T18:40 NEED-OWNER staffy:builder -> crius:lead — A17b gate?\n" +
			"2026-11-19T18:42 DECISION crius:lead -> staffy:builder — [queue q3] owner: go\n", true},
		{"NEED from an earlier turn", "2026-11-19T15:10 NEED staffy:builder -> crius:lead — old ask\n", true},
		{"INFO in the turn", "2026-11-19T18:49 INFO staffy:builder -> crius:lead — A17b landed\n", true},
	} {
		home := askHome(t, staffyBus+c.line)
		f := &fakeSweep{screens: map[string]string{"thinkpad staffy:builder": pane(t, "thinkpad-builder-asks.txt")}}
		t0 := time.Date(2026, 11, 19, 18, 50, 0, 0, time.Local)
		for _, min := range []int{0, 5, 10, 15} {
			if _, err := wakewatch.ProviderErrors(f.deps(home, t0.Add(time.Duration(min)*time.Minute))); err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
		}
		if got := len(f.crius) == 1 && strings.HasPrefix(f.crius[0], "question unanswered for 10 min in tab thinkpad staffy:builder"); got != c.alerts || len(f.crius) > 1 {
			t.Errorf("%s: want alert %v, crius %q", c.name, c.alerts, f.crius)
		}
	}
}

// Review round 2 P2: the bus held the ask at the 10-min mark, then Crius answered it there at 19:02; a bus line does not
// wake a tab, so the tab still on the same ask wakes Crius at the next sweep, once.
func TestRemoteTabAskWakesOnceTheBusAnswerLeavesItOpen(t *testing.T) {
	need := "2026-11-19T18:49 NEED staffy:builder -> crius:lead — A17c: where is the test DB copy?\n"
	home := askHome(t, staffyBus+need)
	f := &fakeSweep{screens: map[string]string{"thinkpad staffy:builder": pane(t, "thinkpad-builder-asks.txt")}}
	t0 := time.Date(2026, 11, 19, 18, 50, 0, 0, time.Local)
	sweep := func(min int) {
		t.Helper()
		if _, err := wakewatch.ProviderErrors(f.deps(home, t0.Add(time.Duration(min)*time.Minute))); err != nil {
			t.Fatalf("+%d min: %v", min, err)
		}
	}
	for _, min := range []int{0, 5, 10} {
		sweep(min)
	}
	if len(f.crius) != 0 {
		t.Fatalf("the bus holds the ask: crius %q", f.crius)
	}
	testutil.Write(t, filepath.Join(home, "bus", "staffy.log"), staffyBus+need+
		"2026-11-19T19:02 DECISION crius:lead -> staffy:builder — A17c: use the copy at /srv/qa\n")
	sweep(15)
	sweep(20)
	if len(f.crius) != 1 || !strings.HasPrefix(f.crius[0], "question unanswered for 15 min in tab thinkpad staffy:builder: first seen at 18:50,") {
		t.Fatalf("want one wake once the bus answer left the tab's ask open, got %q", f.crius)
	}
}

// With no bus log to check (or one that cannot be read) the ask still wakes Crius, and the alert says the bus was not
// checked: "failed to look" is never "no bus line".
func TestRemoteTabAskWithNoBusToCheckStillAlerts(t *testing.T) {
	home := t.TempDir()
	testutil.Write(t, wakewatch.ProviderTabsFile(home), "thinkpad staffy:builder\n")
	f := &fakeSweep{screens: map[string]string{"thinkpad staffy:builder": pane(t, "thinkpad-builder-asks.txt")}}
	t0 := time.Date(2026, 11, 19, 18, 50, 0, 0, time.Local)
	for _, min := range []int{0, 10} {
		if _, err := wakewatch.ProviderErrors(f.deps(home, t0.Add(time.Duration(min)*time.Minute))); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.crius) != 1 || !strings.HasSuffix(f.crius[0], "; its bus was not checked: no bus log for staffy here") {
		t.Fatalf("crius %q", f.crius)
	}
}

// Review P2/P3 shapes: the status emoji is read as ownerqueue reads it (a ✅ on a checklist line or in a table is no
// status, a final ⏳ under an ask label is); a right-aligned "✔ Update installed" notice under the reply does not hide a
// last line that is a question; a reply that quotes "esc to interrupt" still asks.
func TestRemoteTabAskShapes(t *testing.T) {
	// the heading's ✅ would be the status of a reply with no ❓ under it (ownerqueue.Status, the Mac rule): taken out
	asks := strings.Replace(pane(t, "thinkpad-builder-asks.txt"), "**A17b is done ✅**", "**A17b is done**", 1)
	need := "❓ Need: where the test database copy is, or a go to make one."
	for _, c := range []struct {
		name, screen string
		alerts       bool
	}{
		{"label, then a checklist ✅", strings.Replace(asks, need, "Needs you: where the test database copy is.\n  - [x] A17b landed ✅", 1), true},
		{"question under a table ✅", strings.Replace(asks, need, "| A17b | ✅ |\n\n  Where is the test database copy?", 1), true},
		{"label, then a final ⏳", strings.Replace(asks, need, "Needs you: where the test database copy is.\n\n  Gate running. ⏳", 1), false},
		{"question above the update notice", strings.Replace(asks, need, "Where is the test database copy?", 1), true},
		{"quotes esc to interrupt", strings.Replace(asks, need, "The gate printed esc to interrupt and stopped.\n\n  ❓ Need: a go to rerun it.", 1), true},
	} {
		home := askHome(t, staffyBus)
		f := &fakeSweep{screens: map[string]string{"thinkpad staffy:builder": c.screen}}
		t0 := time.Date(2026, 11, 19, 18, 50, 0, 0, time.Local)
		for _, min := range []int{0, 5, 10, 15} {
			if _, err := wakewatch.ProviderErrors(f.deps(home, t0.Add(time.Duration(min)*time.Minute))); err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
		}
		if got := len(f.crius) == 1; got != c.alerts || len(f.crius) > 1 {
			t.Errorf("%s: want alert %v, crius %q", c.name, c.alerts, f.crius)
		}
	}
}

// A box that cannot be reached is a loud "could not look" that says asks there are not seen, never a quiet pass; the ask's
// timer goes on through the failed looks, and the wake comes once the tab can be read again.
func TestRemoteTabAskUnreachableBoxCouldNotLook(t *testing.T) {
	home := askHome(t, staffyBus)
	f := &fakeSweep{screens: map[string]string{"thinkpad staffy:builder": pane(t, "thinkpad-builder-asks.txt")}}
	t0 := time.Date(2026, 11, 19, 18, 50, 0, 0, time.Local)
	if _, err := wakewatch.ProviderErrors(f.deps(home, t0)); err != nil {
		t.Fatal(err)
	}
	delete(f.screens, "thinkpad staffy:builder") // the ThinkPad is off the network
	for _, min := range []int{5, 10} {
		if _, err := wakewatch.ProviderErrors(f.deps(home, t0.Add(time.Duration(min)*time.Minute))); err == nil ||
			!strings.Contains(err.Error(), "could not look at tab thinkpad staffy:builder") {
			t.Fatalf("+%d min: a failed look must fail the run, got %v", min, err)
		}
	}
	want := "ALERT the provider-error sweep could not look at tab thinkpad staffy:builder, so a session stopped there, waiting on a prompt or asking a question is not seen: ssh: connect to host 100.109.19.101 port 22: Operation timed out"
	if len(f.posts) != 1 || f.posts[0] != want {
		t.Fatalf("two failed looks must say an ask there is not seen:\n posts %q\n want %q", f.posts, want)
	}
	f.screens["thinkpad staffy:builder"] = pane(t, "thinkpad-builder-asks.txt")
	if _, err := wakewatch.ProviderErrors(f.deps(home, t0.Add(15*time.Minute))); err != nil {
		t.Fatal(err)
	}
	if len(f.crius) != 2 || !strings.HasPrefix(f.crius[1], "question unanswered for 15 min in tab thinkpad staffy:builder: first seen at 18:50,") {
		t.Fatalf("the failed looks reset the ask's timer or lost it: crius %q", f.crius)
	}
}
