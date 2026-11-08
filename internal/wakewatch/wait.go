package wakewatch

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// eventWait is how long a WAIT for an event (one naming no time) silences a role's idle flag at most.
const eventWait = 12 * time.Hour

// waitState is what who's newest WAIT line on its bus does to its idle flag.
type waitState struct {
	silent bool      // the wait runs: no idle flag
	from   time.Time // its idle spell counts from here: the wait's end, or the line to it that ended it
	once   bool      // the wait ran out with no line to it: flag it once (key waited:), not every 20 min
	note   string    // said in that flag
}

// waitOnBus reads who's bus (crius:lead DECISION 11-08 06:40: harness2:lead posted 'WAIT … waiting for the week reset
// (Sun 09:00)' at 05:38 and drew IDLE at 06:38). A WAIT line from who that names a time ("Sun 09:00", "11-09 09:00",
// "09:00", "15:15Z": the first one after the line) silences its idle flag until then; one naming no time waits for an
// event, until a line to who from another role, at most 12 h. A newer line from who, or a line to it, ends the wait.
// A wait that ran out with no line to who is flagged once.
func waitOnBus(home, who string, now time.Time) (waitState, error) {
	lines, _, err := busLines(home, who)
	if err != nil {
		return waitState{}, err
	}
	project := strings.SplitN(who, ":", 2)[0]
	var at, until time.Time // the newest WAIT from who, and when it ends (zero: an event)
	var w waitState
	for _, m := range lines {
		from := busName(project, m[3])
		if m[3] == "crius:wakewatch" {
			continue // a machine alert gives no work
		}
		t, err := lineTime(m[1], now)
		if err != nil {
			continue
		}
		switch {
		case from == who && m[2] == "WAIT":
			at, until, w = t, waitUntil(m[5], t), waitState{}
		case from == who:
			at, w = time.Time{}, waitState{}
		case !at.IsZero() && slices.ContainsFunc(recipients(m[4]), func(r string) bool { return busName(project, r) == who }):
			at, w = time.Time{}, waitState{from: t} // the wait got its answer: a fresh idle spell from here
		}
	}
	if at.IsZero() {
		return w, nil
	}
	if until.IsZero() {
		if end := at.Add(eventWait); now.Before(end) {
			return waitState{silent: true}, nil
		} else {
			return waitState{from: end, once: true, note: "; its WAIT for an event has lasted 12 h with no line to it (said once)"}, nil
		}
	}
	if now.Before(until) {
		return waitState{silent: true}, nil
	}
	return waitState{from: until, once: true, note: "; its WAIT until " + until.Format("Mon 01-02 15:04") + " has passed (said once)"}, nil
}

var waitTimeRe = regexp.MustCompile(`(?i)\b(?:(mon|tue|wed|thu|fri|sat|sun)[a-z]*\.?\s+)?(?:(\d{1,2})-(\d{1,2})\s+)?([01]?\d|2[0-3]):([0-5]\d)\s*(z|utc)?\b`)

var weekdays = map[string]time.Weekday{"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday}

// waitUntil gives the first time named in text that falls after the line's own time at, or zero when it names none.
func waitUntil(text string, at time.Time) time.Time {
	for _, m := range waitTimeRe.FindAllStringSubmatch(text, -1) {
		loc := time.Local
		if m[6] != "" {
			loc = time.UTC
		}
		base := at.In(loc)
		h, _ := strconv.Atoi(m[4])
		mi, _ := strconv.Atoi(m[5])
		var t time.Time
		switch {
		case m[2] != "":
			mo, _ := strconv.Atoi(m[2])
			d, _ := strconv.Atoi(m[3])
			if mo < 1 || mo > 12 || d < 1 || d > 31 {
				continue
			}
			t = time.Date(base.Year(), time.Month(mo), d, h, mi, 0, 0, loc)
			if t.Before(base.AddDate(0, -6, 0)) { // "01-02" written in December is next year's
				t = t.AddDate(1, 0, 0)
			}
		default:
			t = time.Date(base.Year(), base.Month(), base.Day(), h, mi, 0, 0, loc)
			for i := 0; i < 8 && (!t.After(base) || m[1] != "" && t.Weekday() != weekdays[strings.ToLower(m[1])]); i++ {
				t = t.AddDate(0, 0, 1)
			}
		}
		if t.After(at) {
			return t
		}
	}
	return time.Time{}
}
