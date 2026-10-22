package store

// Bus line grammar (design §6): `<YYYY-MM-DDTHH:MM> TYPE <project>:<role> -> crius — text [refs]`.
//
// Old `HH:MM` lines still parse: their date comes from a running state (the previous line's date, plus one day
// when the clock goes back more than a few minutes), which the caller seeds from the file. The guess can be wrong
// either way; a writer that dates its lines removes it.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Types are the Crius-bus types. The one written list is the "- Types" line in docs/MEMORY-AND-MEASUREMENT.md §6;
// a test keeps the two equal. Any other type stays malformed (a loud warning), on purpose.
var Types = map[string]bool{"DONE": true, "BLOCKED": true, "NEED-OWNER": true, "PAUSE": true, "PAUSED": true, "LIMIT": true,
	"ITEM": true, "DECISION": true, "RESUMED": true, "ALERT": true, "INFO": true, "NEED": true, "WAIT": true,
	"PARKED": true, "VERIFIED": true, "CORRECTED": true, "GO": true, "ACK": true,
	"ORDER": true, "ANSWER": true, "ASK": true, "START": true, "CLEAR-READY": true, "OWNER": true,
	"READY": true, "PASS": true, "FAIL": true, "LANDED": true, "CORRECTION": true}

const MaxLen = 300

// lateWithin is how many minutes behind the line before an undated line may be and still count as late, not as
// the next day. Out-of-order lines seen on the bus are 1 minute behind, midnights 1396 or more; a small window
// keeps the one-day silence case above rare.
const lateWithin = 10

// Slug is a project or role name. Go's `$` matches only at the very end, so "demo\n" does not pass.
var Slug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// nameRe is a bus name: a project, or project:role.
var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*(?::[a-z0-9][a-z0-9-]*)?$`)

var lineRe = regexp.MustCompile(`^(?:(\d{4}-\d\d-\d\d)T)?(\d\d:\d\d)(Z?) ([A-Z][A-Z-]*) (\S+)(?: -> (\S+(?:,? \S+)*?))? — (.*)$`)

var refRes = []struct {
	key string
	re  *regexp.Regexp
}{
	{"item", regexp.MustCompile(`\bitem=([a-z0-9-]+#\d+)`)},
	{"sha", regexp.MustCompile(`\bsha=([0-9a-f]{7,40})\b`)},
	{"pr", regexp.MustCompile(`\bpr=(\d+)\b`)},
	{"path", regexp.MustCompile(`\bpath=(\S+)`)},
	// The id must not follow a word character or '-'; RE2 has no look-behind, so the group skips that character.
	{"decision", regexp.MustCompile(`(?:^|[^\w-])([a-z0-9][a-z0-9-]*-D\d+)\b`)},
}

// State dates undated lines: the date and time of the line before. Newest is ingest's: the newest dated stamp in the
// file so far (RFC 3339, UTC), for the clock check.
type State struct{ Date, Time, Newest string }

type Line struct {
	Date, Time, Type string
	Project          string // "" for a sender with no ':' (strict: a Role, as written, on the log's own bus)
	Role, To, Text   string // To "" is a line with no "->": to all on the log's bus
	Refs             map[string][]string
	At               time.Time // a Z stamp's instant: its local Date and Time repeat in a DST fall-back hour
	DatedByGuess     bool
	Late             bool // undated and a few minutes behind the line before: its date does not move the state
}

func RefsOf(text string) map[string][]string {
	refs := map[string][]string{}
	for _, r := range refRes {
		for _, m := range r.re.FindAllStringSubmatch(text, -1) {
			refs[r.key] = append(refs[r.key], m[1])
		}
	}
	return refs
}

// ParseLine parses one line, or returns nil when it does not fit the grammar.
// st dates undated lines and is updated in place. strict limits TYPE to the bus types and the sender to
// <project>:<role> or a bare role; team logs use strict=false.
func ParseLine(line string, st *State, strict bool) *Line {
	m := lineRe.FindStringSubmatch(strings.TrimRight(line, "\n"))
	if m == nil {
		return nil
	}
	day, hhmm, utc, typ, sender, to, text := m[1], m[2], m[3] == "Z", m[4], m[5], m[6], m[7]
	if strict && !Types[typ] {
		return nil
	}
	// Several recipients are plain names, so a secret with a space in it ("password: hunter2") is never split into
	// names that each look harmless.
	if rs := (&Line{To: to}).Recipients(); len(rs) > 1 {
		for _, r := range rs {
			if !nameRe.MatchString(r) {
				return nil
			}
		}
	}
	project, role := "", sender
	if i := strings.LastIndex(sender, ":"); i >= 0 {
		project, role = sender[:i], sender[i+1:]
		if !Slug.MatchString(project) || role == "" {
			return nil
		}
	} else if strict {
		// A bare sender ("QA2") is a role on the log's own bus; the ingest files it as <log project>:<lower name>,
		// after redacting it as written (a key rule can be upper-case only).
		if !Slug.MatchString(strings.ToLower(sender)) {
			return nil
		}
	}
	check := day
	if check == "" {
		check = "2000-01-01"
	}
	at, err := time.Parse("2006-01-02T15:04", check+"T"+hhmm)
	if err != nil {
		return nil
	}
	var instant time.Time
	if utc { // a stamp is local time; one ending in Z is UTC, read as the same moment in local time
		if day == "" {
			return nil
		}
		instant, at = at, at.In(time.Local)
		day, hhmm = at.Format("2006-01-02"), at.Format("15:04")
	}
	guessed, late := day == "", false
	if guessed {
		if st == nil || st.Date == "" {
			return nil
		}
		day = st.Date
		d, err := time.Parse("2006-01-02", day)
		if err != nil {
			return nil
		}
		// Two writers can post a few minutes out of order (17:53 then 17:52, or 23:58 after 00:01): a line less
		// than lateWithin minutes behind the one before is late, on the nearer day, and leaves the state alone. Any
		// other step back is midnight; the ones seen on the bus step back 23 hours or more. The cost: after a day of
		// silence, a next line up to lateWithin minutes earlier on the clock is taken as late, and it and the lines
		// after it are a day early (as every line after 24 hours or more of silence always was). An empty state
		// time is a file with no line yet.
		back := (minutes(st.Time) - minutes(hhmm) + 24*60) % (24 * 60)
		switch {
		case st.Time != "" && back > 0 && back < lateWithin:
			late = true
			if hhmm > st.Time {
				day = d.AddDate(0, 0, -1).Format("2006-01-02")
			}
		case hhmm < st.Time:
			day = d.AddDate(0, 0, 1).Format("2006-01-02")
		}
	}
	if st != nil && !late {
		st.Date, st.Time = day, hhmm
	}
	return &Line{Date: day, Time: hhmm, Type: typ, Project: project, Role: role, To: to, Text: text,
		Refs: RefsOf(text), At: instant, DatedByGuess: guessed, Late: late}
}

// Recipients splits To, as written ("crius:lead, quotes:b1", "crius:lead quotes:all" or "quotes:p1 (cc quotes:p2)"), into
// names; a cc'd name is one too.
func (l *Line) Recipients() []string {
	to := l.To
	if main, cc, ok := strings.Cut(to, " (cc "); ok {
		if cc, ok = strings.CutSuffix(cc, ")"); ok && strings.TrimSpace(cc) != "" {
			to = main + "," + cc
		}
	}
	return strings.FieldsFunc(to, func(r rune) bool { return r == ',' || r == ' ' })
}

// minutes is the minute of the day of a checked "HH:MM", 0 for "".
func minutes(hhmm string) int {
	t, _ := time.Parse("15:04", hhmm)
	return t.Hour()*60 + t.Minute()
}

func FormatLine(typ, project, role, text, to string, now time.Time) (string, error) {
	if !Types[typ] {
		return "", refused("unknown bus type %q", typ)
	}
	if !Slug.MatchString(project) || !Slug.MatchString(role) {
		return "", refused("project and role must be lower-case slugs")
	}
	if strings.ContainsAny(text, "\n\r") {
		return "", refused("one line only")
	}
	if n := utf8.RuneCountInString(text); n > MaxLen {
		return "", refused("text is %d chars; the limit is %d", n, MaxLen)
	}
	line := now.Format("2006-01-02T15:04") + " " + typ + " " + project + ":" + role + " -> " + to + " — " + text
	if ParseLine(line, nil, true) == nil {
		return "", refused("line does not parse back")
	}
	return line, nil
}

func AppendLine(home, project, line string) error {
	if !Slug.MatchString(project) {
		return refused("bad project slug")
	}
	f, err := os.OpenFile(filepath.Join(home, "bus", project+".log"), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.WriteString(line + "\n"); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
