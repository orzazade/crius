package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/watchdog"
)

// A CORRECTED names its cause from docs/RULES.md §2.5 as "cause: <name>", so a second mistake of one cause (§2.6: stop
// chasing, build a check) is seen when it is posted, not found by hand. Of 175 CORRECTED lines 11-01..11-21, 4 named a
// cause. The idea is harness2's watch (crius-harness2 9bcc3e1, 63b92cf): every correction under one cause of the RULES
// list, and a fix queued when a cause repeats; there a model labelled the cause, here the poster names it.
var (
	causes  = []string{"missing test", "wrong assumption", "stale context", "silent failure", "misread request", "harness or tool", "review gap", "bad data"}
	causeRe = regexp.MustCompile(`(?i)\bcause:\s*(missing test|wrong assumption|stale context|silent failure|misread request|harness or tool|harness|tool|review gap|bad data)s?\b`)
	// a CORRECTED bus line: its stamp, sender and text
	correctedRe = regexp.MustCompile(`^(\d{4}-\d\d-\d\dT\d\d:\d\d)(Z?) CORRECTED (\S+) .*? — (.*)$`)
)

// repeatDays is how far back a CORRECTED of the same cause on the same bus counts as the first of two.
const repeatDays = 30

// causeOf gives the cause a text names, "" for none.
func causeOf(text string) string {
	m := causeRe.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	c := strings.ToLower(m[1])
	if c == "harness" || c == "tool" {
		c = "harness or tool"
	}
	return c
}

// noCause is the refusal for a CORRECTED that names no cause.
func noCause() string {
	return "a CORRECTED names its cause (docs/RULES.md §2.5) as cause: <one of " + strings.Join(causes, ", ") +
		">, so a second mistake of one cause is seen (§2.6)"
}

// sameCause looks in log, before the last copy of line (the one just written; lines other writers added after it are
// not read), for a CORRECTED of the same cause in the last
// repeatDays. It gives the earlier line's stamp and sender, or "" when there is none.
func sameCause(log, line, cause string, now time.Time) (string, error) {
	b, err := os.ReadFile(log)
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	own := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if lines[i] == line {
			own = i
			break
		}
	}
	if own < 0 {
		return "", fmt.Errorf("the line just posted is not in %s", log)
	}
	for i := own - 1; i >= 0; i-- {
		m := correctedRe.FindStringSubmatch(lines[i])
		if m == nil || causeOf(m[4]) != cause {
			continue
		}
		loc := now.Location()
		if m[2] == "Z" {
			loc = time.UTC
		}
		at, err := time.ParseInLocation("2006-01-02T15:04", m[1], loc)
		if err != nil || now.Sub(at) > repeatDays*24*time.Hour {
			continue
		}
		return m[1] + m[2] + " " + m[3], nil
	}
	return "", nil
}

// sayRepeat tells the poster and wakes Crius (an ALERT on crius.log) when this CORRECTED's cause was seen before on
// its bus. home is the Crius home of the log.
func sayRepeat(home, log, line, cause, from string, now time.Time, stderr io.Writer) {
	first, err := sameCause(log, line, cause, now)
	if err != nil {
		fmt.Fprintf(stderr, "bus-post: posted, but could not look for an earlier CORRECTED of the same cause: %v\n", err)
		return
	}
	if first == "" {
		return
	}
	text := fmt.Sprintf("same cause twice on %s: %s (%s, then %s now). RULES §2.6: build a check that goes red on both",
		filepath.Base(log), cause, first, from)
	fmt.Fprintln(stderr, "bus-post: "+text)
	if err := watchdog.PostOn(home, "crius", "ALERT", "crius:bus-post", "crius:lead", now, text); err != nil {
		fmt.Fprintf(stderr, "bus-post: could not post the repeat ALERT on crius.log: %v\n", err)
	}
}
