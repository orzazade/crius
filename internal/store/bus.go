package store

// Bus line grammar (design §6): `<YYYY-MM-DDTHH:MM> TYPE <project>:<role> -> crius — text [refs]`.
//
// Old `HH:MM` lines still parse: their date comes from a running state (the previous line's date, plus one day
// when the clock goes back an hour or more), which the caller seeds from the file.

import (
	"crypto/sha1"
	_ "embed"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Types are the Crius-bus types. The one written list is the "- Types" line in docs/MEMORY-AND-MEASUREMENT.md §6;
// a test keeps the two equal. Any other type stays malformed (a loud warning), on purpose.
var Types = map[string]bool{"DONE": true, "BLOCKED": true, "NEED-OWNER": true, "PAUSED": true, "LIMIT": true,
	"ITEM": true, "DECISION": true, "RESUMED": true, "ALERT": true, "INFO": true, "NEED": true, "WAIT": true,
	"PARKED": true}

const MaxLen = 300

//go:embed bus.go
var parser []byte

// ParserSum changes whenever this file does, so the ingest reads its logs again and stored dates follow the parser.
func ParserSum() string {
	s := sha1.Sum(parser)
	return hex.EncodeToString(s[:6])
}

// Slug is a project or role name. Go's `$` matches only at the very end, so "demo\n" does not pass.
var Slug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

var lineRe = regexp.MustCompile(`^(?:(\d{4}-\d\d-\d\d)T)?(\d\d:\d\d) ([A-Z][A-Z-]*) (\S+) -> (\S+) — (.*)$`)

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

// State dates undated lines: the date and time of the line before.
type State struct{ Date, Time string }

type Line struct {
	Date, Time, Type string
	Project          string // "" only for a lenient line whose sender has no ':'
	Role, To, Text   string
	Refs             map[string][]string
	DatedByGuess     bool
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
// <project>:<role>; team logs use strict=false.
func ParseLine(line string, st *State, strict bool) *Line {
	m := lineRe.FindStringSubmatch(strings.TrimRight(line, "\n"))
	if m == nil {
		return nil
	}
	day, hhmm, typ, sender, to, text := m[1], m[2], m[3], m[4], m[5], m[6]
	if strict && !Types[typ] {
		return nil
	}
	project, role := "", sender
	if i := strings.LastIndex(sender, ":"); i >= 0 {
		project, role = sender[:i], sender[i+1:]
		if !Slug.MatchString(project) || role == "" {
			return nil
		}
	} else if strict {
		return nil
	}
	check := day
	if check == "" {
		check = "2000-01-01"
	}
	if _, err := time.Parse("2006-01-02T15:04", check+"T"+hhmm); err != nil {
		return nil
	}
	guessed := day == ""
	if guessed {
		if st == nil || st.Date == "" {
			return nil
		}
		day = st.Date
		// Two writers can post a few minutes out of order (17:53 then 17:52); that is the same day. The midnights
		// seen on the bus step back 23 hours or more.
		if hhmm < st.Time && minutes(st.Time)-minutes(hhmm) >= 60 {
			d, err := time.Parse("2006-01-02", day)
			if err != nil {
				return nil
			}
			day = d.AddDate(0, 0, 1).Format("2006-01-02")
		}
	}
	if st != nil {
		st.Date, st.Time = day, hhmm
	}
	return &Line{Date: day, Time: hhmm, Type: typ, Project: project, Role: role, To: to, Text: text,
		Refs: RefsOf(text), DatedByGuess: guessed}
}

// minutes is the minute of the day of a checked "HH:MM".
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
