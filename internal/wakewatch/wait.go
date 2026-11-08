package wakewatch

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// eventWait is how long a WAIT for an event (one naming no time) silences a role's idle flag at most, and maxWait how
// long any WAIT does, so a misread or mistyped time cannot silence a role for days (review of 8295577 P1-2).
const (
	eventWait = 12 * time.Hour
	maxWait   = 24 * time.Hour
)

// waitState is what who's newest WAIT line on its bus does to its idle flag.
type waitState struct {
	silent bool      // the wait runs: no idle flag
	from   time.Time // its idle spell counts from here: the wait's end, or the line to it that ended it
	once   bool      // the wait ran out with no line to it: flag it once (key waited:), not every 20 min
	note   string    // said in that flag
}

// waitOnBus reads who's bus (crius:lead DECISION 11-08 06:40: harness2:lead posted 'WAIT … waiting for the week reset
// (Sun 09:00)' at 05:38 and drew IDLE at 06:38). A WAIT line from who that names a time ("Sun 09:00", "11-09 09:00",
// "09:00", "15:15Z": the first one still ahead of the line, at most 24 h on) silences its idle flag until then; one
// naming no time waits for an event, until a line to who (or <project>:all) from another role, at most 12 h. A newer
// line from who ends any wait. A wait that ran out with no such line is flagged once.
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
		case !at.IsZero() && until.IsZero() && slices.ContainsFunc(recipients(m[4]), func(r string) bool {
			return busName(project, r) == who || strings.EqualFold(r, project+":all")
		}):
			at, w = time.Time{}, waitState{from: t} // the event came: a fresh idle spell from here (an ACK does not end a timed wait)
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
	if until.After(at.Add(maxWait)) {
		until = at.Add(maxWait)
	}
	if now.Before(until) {
		return waitState{silent: true}, nil
	}
	return waitState{from: until, once: true, note: "; its WAIT until " + until.Format("Mon 01-02 15:04") + " has passed (said once)"}, nil
}

var waitTimeRe = regexp.MustCompile(`(?i)\b(?:(mon|tue|wed|thu|fri|sat|sun)(?:[a-z]*day)?\.?\s+)?(?:(\d{1,2})-(\d{1,2})\s+)?([01]?\d|2[0-3]):([0-5]\d)\s*(z|utc|am|pm)?\b`)

var weekdays = map[string]time.Weekday{"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday}

// waitUntil gives the first time named in text that is still ahead of the line's own time at, or zero when it names
// none. A time is never moved on to the next day: "IDLE at 06:38 was wrong" quotes a time that has passed (review of
// 8295577 P1-1). A weekday names its next date, today included; a date after a year ("2025-12-01") is not read.
func waitUntil(text string, at time.Time) time.Time {
	for _, ix := range waitTimeRe.FindAllStringSubmatchIndex(text, -1) {
		if ix[0] > 0 && strings.ContainsAny(text[ix[0]-1:ix[0]], "0123456789-") {
			continue
		}
		m := make([]string, len(ix)/2)
		for i := range m {
			if ix[2*i] >= 0 {
				m[i] = text[ix[2*i]:ix[2*i+1]]
			}
		}
		loc := time.Local
		suffix := strings.ToLower(m[6])
		if suffix == "z" || suffix == "utc" {
			loc = time.UTC
		}
		base := at.In(loc)
		h, _ := strconv.Atoi(m[4])
		mi, _ := strconv.Atoi(m[5])
		switch {
		case suffix == "pm" && h < 12:
			h += 12
		case suffix == "am" && h == 12:
			h = 0
		}
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
			for i := 0; i < 7 && m[1] != "" && t.Weekday() != weekdays[strings.ToLower(m[1])]; i++ {
				t = t.AddDate(0, 0, 1)
			}
		}
		if t.After(at) {
			return t
		}
	}
	return time.Time{}
}
