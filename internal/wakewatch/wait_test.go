package wakewatch

import (
	"strings"
	"testing"
	"time"
)

// idleRuns runs wakewatch each 10 min from start to end on one idle lead tab and gives the minute of each idle alert.
func idleRuns(t *testing.T, bus string, start, end time.Time) []string {
	t.Helper()
	home := setup(t, map[string]string{"harness2.log": bus})
	f := &fake{}
	var got []string
	for at := start; !at.After(end); at = at.Add(10 * time.Minute) {
		f.screens = []Screen{screen("harness2", tab("harness2 · lead", "Waiting for the week reset.", "", start))}
		n := len(f.sent)
		if err := Run(f.deps(home, at)); err != nil {
			t.Fatal(err)
		}
		for _, s := range f.sent[n:] {
			if strings.Contains(s, "harness2:lead") && strings.Contains(s, "idle") {
				got = append(got, at.Format("15:04")+" "+s)
			}
		}
	}
	return got
}

// crius:lead DECISION 11-08 06:40: harness2:lead posted 'WAIT … waiting for the week reset (Sun 09:00)' at 05:38 and
// was still flagged IDLE at 06:38. A WAIT line naming a time silences its idle flag until then, and it is flagged once
// after; a WAIT for an event silences it until a line to it (at most 12 h).
func TestAWaitLineSilencesTheIdleFlagUntilItsTime(t *testing.T) {
	at := time.Date(2026, 11, 8, 5, 38, 0, 0, time.Local) // a Sunday
	bus := stamp(at) + " WAIT harness2:lead -> crius — Conditions done in ff9fcbc, bun test 135/0. Zero-spend work done;" +
		" waiting for the week reset (Sun 09:00). Then round 1, then leader.md. Next steps in STATUS.md.\n"
	got := idleRuns(t, bus, at, time.Date(2026, 11, 8, 11, 0, 0, 0, time.Local))
	if len(got) != 1 || !strings.HasPrefix(got[0], "09:") || !strings.Contains(got[0], "its WAIT until Sun 11-08 09:00 has passed") {
		t.Fatalf("want one idle flag after 09:00: %q", got)
	}

	event := stamp(at) + " WAIT harness2:lead -> crius:lead — waiting for QA to judge item 3\n"
	got = idleRuns(t, event, at, at.Add(150*time.Minute))
	if len(got) != 0 {
		t.Fatalf("a wait for an event: %q", got)
	}
	answered := event + stamp(at.Add(2*time.Hour)) + " ANSWER crius:lead -> harness2:lead — QA passed item 3; go on\n"
	got = idleRuns(t, answered, at, at.Add(4*time.Hour))
	if len(got) == 0 || got[0] < at.Add(2*time.Hour+idleAfter).Format("15:04") {
		t.Fatalf("the line that ends the wait starts a fresh idle spell: %q", got)
	}
	got = idleRuns(t, event, at, at.Add(14*time.Hour))
	if len(got) != 1 || !strings.Contains(got[0], "its WAIT for an event has lasted 12 h") {
		t.Fatalf("an event wait ends after 12 h, flagged once: %q", got)
	}
}

func TestAWaitNamesItsTimeInTheFormsLeadsWrite(t *testing.T) {
	at := time.Date(2026, 11, 8, 5, 38, 0, 0, time.Local) // a Sunday
	for _, c := range []struct {
		text string
		want time.Time
	}{
		{"waiting for the week reset (Sun 09:00)", time.Date(2026, 11, 8, 9, 0, 0, 0, time.Local)},
		{"recheck after 15:15Z", time.Date(2026, 11, 8, 15, 15, 0, 0, time.UTC)},
		{"until 11-09 09:30, then round 2", time.Date(2026, 11, 9, 9, 30, 0, 0, time.Local)},
		{"next check at 04:00", time.Date(2026, 11, 9, 4, 0, 0, 0, time.Local)}, // passed today: tomorrow's
		{"until Monday 08:00", time.Date(2026, 11, 9, 8, 0, 0, 0, time.Local)},
		{"waiting for QA to judge item 3; bun test 135/0", time.Time{}},
	} {
		if got := waitUntil(c.text, at); !got.Equal(c.want) {
			t.Errorf("%q: %v, want %v", c.text, got, c.want)
		}
	}
}
