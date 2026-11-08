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
	acked := bus + stamp(at.Add(7*time.Minute)) + " ACK crius:lead -> harness2:lead — noted\n"
	if got := idleRuns(t, acked, at, time.Date(2026, 11, 8, 8, 50, 0, 0, time.Local)); len(got) != 0 {
		t.Fatalf("an ACK ends a timed wait: %q", got)
	}
	if got := idleRuns(t, event+stamp(at.Add(7*time.Minute))+" ACK crius:lead -> harness2:lead — noted\n", at, at.Add(150*time.Minute)); len(got) != 0 {
		t.Fatalf("an ACK ends an event wait: %q", got)
	}
	ordered := bus + stamp(at.Add(7*time.Minute)) + " DECISION crius:lead -> harness2:lead — don't wait, do Y now\n"
	if got := idleRuns(t, ordered, at, time.Date(2026, 11, 8, 8, 50, 0, 0, time.Local)); len(got) == 0 {
		t.Fatalf("an order does not end a timed wait: %q", got)
	}
	toAll := event + stamp(at.Add(2*time.Hour)) + " ORDER crius:lead -> harness2:all — items 4-6\n"
	if got := idleRuns(t, toAll, at, at.Add(4*time.Hour)); len(got) == 0 {
		t.Fatalf("a line to the whole project ends an event wait: %q", got)
	}
	far := stamp(at) + " WAIT harness2:lead -> crius — waiting for CI until 12-31 09:00\n"
	if got := idleRuns(t, far, at, at.Add(26*time.Hour)); len(got) != 1 || !strings.Contains(got[0], "its WAIT until Mon 11-09 05:38 has passed") {
		t.Fatalf("a wait is capped at 24 h: %q", got)
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
		{"next check at 04:00", time.Time{}}, // passed: a time is never moved to the next day
		{"IDLE at 05:30 was wrong; waiting for the week reset (Sun 09:00)", time.Date(2026, 11, 8, 9, 0, 0, 0, time.Local)},
		{"rate limited since 05:10; reset at 9:30am", time.Date(2026, 11, 8, 9, 30, 0, 0, time.Local)},
		{"took 1:05 to run; waiting for QA", time.Time{}},
		{"Monitor 07:00 run, then QA", time.Date(2026, 11, 8, 7, 0, 0, 0, time.Local)},
		{"baseline 2025-12-01 10:00; waiting for CI", time.Time{}},
		{"back at 3:15 pm", time.Date(2026, 11, 8, 15, 15, 0, 0, time.Local)},
		{"until 2026-11-09 09:00", time.Date(2026, 11, 9, 9, 0, 0, 0, time.Local)},
		{"until Tues 09:00", time.Date(2026, 11, 10, 9, 0, 0, 0, time.Local)},
		{"until Thurs 09:00", time.Date(2026, 11, 12, 9, 0, 0, 0, time.Local)},
		{"until Monday 08:00", time.Date(2026, 11, 9, 8, 0, 0, 0, time.Local)},
		{"waiting for QA to judge item 3; bun test 135/0", time.Time{}},
	} {
		if got := waitUntil(c.text, at); !got.Equal(c.want) {
			t.Errorf("%q: %v, want %v", c.text, got, c.want)
		}
	}
	night := time.Date(2026, 11, 8, 22, 0, 0, 0, time.Local) // review r2 P2-A: a time after midnight
	if got, want := waitUntil("quota resets 09:00", night), time.Date(2026, 11, 9, 9, 0, 0, 0, time.Local); !got.Equal(want) {
		t.Errorf("at 22:00: %v, want %v", got, want)
	}
	if got := waitUntil("IDLE at 21:50 was wrong", night); !got.IsZero() {
		t.Errorf("a time quoted from the past: %v", got)
	}
}
