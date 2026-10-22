package store

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/testutil"
)

const dated = "2026-10-16T09:20 DONE demo:leader -> crius — item=demo#41 sha=abc1234 Inc 4 QA-verified; see demo-D12"

func TestParseDatedLine(t *testing.T) {
	p := ParseLine(dated, nil, true)
	if p == nil {
		t.Fatal("did not parse")
	}
	got := []string{p.Date, p.Time, p.Type, p.Project, p.Role, p.To}
	want := []string{"2026-10-16", "09:20", "DONE", "demo", "leader", "crius"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if !strings.HasPrefix(p.Text, "item=demo#41") || p.DatedByGuess {
		t.Fatalf("text %q guess %v", p.Text, p.DatedByGuess)
	}
	wantRefs := map[string][]string{"item": {"demo#41"}, "sha": {"abc1234"}, "decision": {"demo-D12"}}
	if !reflect.DeepEqual(p.Refs, wantRefs) {
		t.Fatalf("refs %v", p.Refs)
	}
}

// Crius 10-17 03:03: a stamp is local time; one ending in Z is UTC and reads as the same moment in local time, so
// the lines after it date on from there. An undated stamp with Z has no day and is malformed.
func TestAZStampIsUTC(t *testing.T) {
	local := time.Local
	time.Local = time.FixedZone("UTC+4", 4*3600)
	t.Cleanup(func() { time.Local = local })
	st := State{}
	p := ParseLine("2026-10-16T22:23Z DONE falcon:lead -> crius:lead — x", &st, true)
	if p == nil || p.Date != "2026-10-17" || p.Time != "02:23" || st != (State{Date: "2026-10-17", Time: "02:23"}) {
		t.Fatalf("%+v %+v", p, st)
	}
	if p := ParseLine("02:30 INFO crius:lead -> falcon:lead — y", &st, true); p == nil || p.Date != "2026-10-17" {
		t.Fatalf("%+v", p)
	}
	if ParseLine("22:40Z DONE falcon:lead -> crius — z", &st, true) != nil {
		t.Fatal("an undated Z stamp parsed")
	}
	// Review of 944e44a: in a DST fall-back hour the local time repeats, so a Z line keeps its instant.
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	time.Local = ny
	p = ParseLine("2026-11-01T06:30Z INFO demo:lead -> crius — x", &st, true)
	if p == nil || p.Time != "01:30" || !p.At.Equal(time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC)) {
		t.Fatalf("%+v", p)
	}
	if p := ParseLine("2026-11-01T01:30 INFO demo:lead -> crius — x", &st, true); p == nil || !p.At.IsZero() {
		t.Fatalf("a local stamp has an instant: %+v", p)
	}
}

func TestUndatedLineStillParses(t *testing.T) {
	st := &State{Date: "2026-10-15", Time: "08:00"}
	p := ParseLine("09:20 BLOCKED axi:leader -> crius — waiting on CI", st, true)
	if p == nil || p.Date != "2026-10-15" || p.Time != "09:20" || p.Type != "BLOCKED" || !p.DatedByGuess {
		t.Fatalf("%+v", p)
	}
}

func TestUndatedLineAfterMidnightMovesToNextDay(t *testing.T) {
	st := &State{Date: "2026-10-15", Time: "23:50"}
	p := ParseLine("00:10 DONE axi:leader -> crius — x", st, true)
	if p == nil || p.Date != "2026-10-16" {
		t.Fatalf("%+v", p)
	}
	if *st != (State{Date: "2026-10-16", Time: "00:10"}) {
		t.Fatalf("state %+v", *st)
	}
}

// Two writers can post a minute out of order (build.log 17:53 then 17:52); that is the same day, not midnight.
func TestUndatedLineAMinuteEarlierStaysOnTheDay(t *testing.T) {
	st := &State{Date: "2026-10-16", Time: "17:53"}
	for _, c := range []struct{ line, day string }{{"17:52 DONE axi:leader -> crius — x", "2026-10-16"},
		{"17:54 DONE axi:leader -> crius — x", "2026-10-16"}, {"17:45 DONE axi:leader -> crius — x", "2026-10-16"},
		{"17:44 DONE axi:leader -> crius — x", "2026-10-17"}} {
		if p := ParseLine(c.line, st, true); p == nil || p.Date != c.day {
			t.Fatalf("%s: %+v", c.line, p)
		}
	}
}

// Opus P2 on 277f507: the same race across midnight. A late 23:58 after 00:01 is the day before, and the line
// after it is not pushed a day on.
func TestUndatedLineLateAcrossMidnight(t *testing.T) {
	st := &State{Date: "2026-10-16", Time: "23:59"}
	for _, c := range []struct{ line, day string }{{"00:01 DONE axi:leader -> crius — x", "2026-10-17"},
		{"23:58 DONE axi:leader -> crius — x", "2026-10-16"}, {"00:02 DONE axi:leader -> crius — x", "2026-10-17"}} {
		if p := ParseLine(c.line, st, true); p == nil || p.Date != c.day {
			t.Fatalf("%s: %+v", c.line, p)
		}
	}
}

func TestUndatedLineWithoutStateIsNotParsed(t *testing.T) {
	if ParseLine("09:20 DONE axi:leader -> crius — x", nil, true) != nil {
		t.Fatal("an undated line needs a date to start from")
	}
}

func TestDatedLineResetsTheState(t *testing.T) {
	st := &State{Date: "2026-10-01", Time: "10:00"}
	ParseLine(dated, st, true)
	if *st != (State{Date: "2026-10-16", Time: "09:20"}) {
		t.Fatalf("state %+v", *st)
	}
}

func TestMalformedLines(t *testing.T) {
	for _, line := range []string{"hello", "09:20 DONE no arrow here", "2026-10-16T09:20 done demo:leader -> crius — x", ""} {
		if ParseLine(line, nil, true) != nil {
			t.Errorf("parsed %q", line)
		}
	}
}

func TestImpossibleDateOrTimeIsMalformedAndKeepsState(t *testing.T) {
	st := &State{Date: "2026-10-15", Time: "08:00"}
	for _, line := range []string{"2026-99-99T09:20 DONE demo:leader -> crius — x", "25:61 DONE demo:leader -> crius — x"} {
		if ParseLine(line, st, true) != nil {
			t.Errorf("parsed %q", line)
		}
	}
	if *st != (State{Date: "2026-10-15", Time: "08:00"}) {
		t.Fatalf("state %+v", *st)
	}
}

func TestUnknownTypeStrictAndLenient(t *testing.T) {
	line := "09:20 HELLO api -> leader — schema?"
	if ParseLine(line, &State{Date: "2026-10-16", Time: "09:00"}, true) != nil {
		t.Fatal("strict parse took an unknown type")
	}
	p := ParseLine(line, &State{Date: "2026-10-16", Time: "09:00"}, false)
	if p == nil || p.Type != "HELLO" || p.Project != "" || p.Role != "api" {
		t.Fatalf("%+v", p)
	}
}

func TestNewTypesAreAccepted(t *testing.T) {
	for _, typ := range []string{"NEED-OWNER", "PAUSE", "PAUSED", "LIMIT", "ITEM", "DECISION", "RESUMED", "VERIFIED", "CORRECTED",
		"GO", "ACK", "OWNER"} {
		if ParseLine("2026-10-16T09:20 "+typ+" demo:leader -> crius — x", nil, true) == nil {
			t.Error(typ)
		}
	}
}

func TestFormatIsDatedAndParsesBack(t *testing.T) {
	line, err := FormatLine("DONE", "demo", "leader", "item=demo#41 sha=abc1234 done", "crius",
		time.Date(2026, 10, 16, 9, 5, 0, 0, time.Local))
	if err != nil {
		t.Fatal(err)
	}
	if line != "2026-10-16T09:05 DONE demo:leader -> crius — item=demo#41 sha=abc1234 done" {
		t.Fatal(line)
	}
	if ParseLine(line, nil, true) == nil {
		t.Fatal("does not parse back")
	}
}

func TestFormatRefusesBadInput(t *testing.T) {
	for _, c := range [][4]string{{"DONE", "demo", "leader", strings.Repeat("x", 301)}, {"NOPE", "demo", "leader", "x"},
		{"DONE", "Demo!", "leader", "x"}, {"DONE", "demo", "leader", "two\nlines"}} {
		if _, err := FormatLine(c[0], c[1], c[2], c[3], "crius", time.Now()); !IsRefused(err) {
			t.Errorf("%v: err %v", c[:3], err)
		}
	}
}

func TestTextLimitCountsCharactersNotBytes(t *testing.T) {
	if _, err := FormatLine("DONE", "demo", "leader", strings.Repeat("é", 300), "crius", time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestAppendLineGoesToProjectLog(t *testing.T) {
	_, home := testutil.Temp(t)
	initHome(t, home)
	line, err := FormatLine("DONE", "demo", "leader", "x", "crius", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := AppendLine(home, "demo", line); err != nil {
			t.Fatal(err)
		}
	}
	if got := testutil.Read(t, filepath.Join(home, "bus", "demo.log")); got != line+"\n"+line+"\n" {
		t.Fatalf("%q", got)
	}
	if !regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d DONE `).MatchString(line) {
		t.Fatal(line)
	}
}

// Real ~/.crius/bus lines (INFO, NEED, WAIT) were warned as malformed: the Go list and the written list drifted.
func TestEveryCriusBusTypeParsesStrict(t *testing.T) {
	for _, typ := range []string{"DONE", "BLOCKED", "NEED-OWNER", "PAUSED", "LIMIT", "ITEM", "DECISION", "RESUMED",
		"ALERT", "INFO", "NEED", "WAIT", "PARKED", "ACK", "CLEAR-READY"} { // CLEAR-READY: Crius TYPE rule 10-19 16:58
		if ParseLine("2026-10-16T09:20 "+typ+" demo:leader -> crius — x", nil, true) == nil {
			t.Error(typ)
		}
	}
	for _, typ := range []string{"QUEUE", "NEED-QA", "BUG", "LESSON"} { // team-only: loud on the Crius bus
		if ParseLine("2026-10-16T09:20 "+typ+" demo:leader -> crius — x", nil, true) != nil {
			t.Error("team-only type parsed strict:", typ)
		}
	}
}

// Crius 10-17 03:39: the types list drifted from use (RULES says post CORRECTED; the list had no CORRECTED, so those
// lines were dropped as malformed). Every type RULES.md names is in the list.
func TestRulesUseOnlyListedTypes(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), "docs", "RULES.md"))
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, m := range regexp.MustCompile("`([A-Z][A-Z-]+)[ `]").FindAllStringSubmatch(string(b), -1) {
		found++
		if !Types[m[1]] {
			t.Errorf("RULES.md names %s, which is not a bus type", m[1])
		}
	}
	if found == 0 {
		t.Fatal("no types found in RULES.md: the pattern no longer reads it")
	}
}

// The cmux-team skill's type line (section 2, team logs and the bus) names only bus types or the team-only types the
// design doc lists. The skill lives outside the repo, in the owner's Claude config.
func TestTeamSkillUsesOnlyListedTypes(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(home, ".claude", "skills", "cmux-team", "SKILL.md"))
	if errors.Is(err, fs.ErrNotExist) {
		t.Skip("no cmux-team skill on this machine")
	} else if err != nil {
		t.Fatal(err)
	}
	doc, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), "docs", "MEMORY-AND-MEASUREMENT.md"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`Team-only types \(([^)]*)\)`).FindSubmatch(doc)
	if m == nil {
		t.Fatal("the design doc has no \"Team-only types (...)\" list")
	}
	teamOnly := map[string]bool{}
	for _, typ := range strings.Split(string(m[1]), ", ") {
		teamOnly[typ] = true
	}
	_, rest, ok := strings.Cut(string(b), "\nTypes: ")
	if !ok {
		t.Fatal("the skill has no \"Types: \" line")
	}
	rest, _, _ = strings.Cut(rest, "\n**")
	found := 0
	for _, m := range regexp.MustCompile("`([A-Z][A-Z-]+)[ `]").FindAllStringSubmatch(rest, -1) {
		found++
		if !Types[m[1]] && !teamOnly[m[1]] {
			t.Errorf("the cmux-team skill names %s: not a bus type nor a team-only type in the design doc", m[1])
		}
	}
	if found < 10 {
		t.Fatalf("%d types found in the skill's type line: the pattern no longer reads it", found)
	}
}

// The design doc's "- Types:" line is the one written list; the Go list must equal it.
func TestTypesEqualTheDesignDocList(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), "docs", "MEMORY-AND-MEASUREMENT.md"))
	if err != nil {
		t.Fatal(err)
	}
	// The bullet and its indented continuation lines.
	var bullets []string
	in := false
	for _, l := range strings.Split(string(b), "\n") {
		switch {
		case strings.HasPrefix(l, "- Types"):
			bullets, in = append(bullets, l), true
		case in && strings.HasPrefix(l, "  "):
			bullets[len(bullets)-1] += " " + l
		default:
			in = false
		}
	}
	if len(bullets) != 1 {
		t.Fatalf("want one \"- Types\" bullet in the doc, got %d", len(bullets))
	}
	var doc []string
	for _, m := range regexp.MustCompile("`([A-Z][A-Z-]*)`").FindAllStringSubmatch(bullets[0], -1) {
		doc = append(doc, m[1])
	}
	var code []string
	for typ := range Types {
		code = append(code, typ)
	}
	sort.Strings(doc)
	sort.Strings(code)
	if !reflect.DeepEqual(doc, code) {
		t.Fatalf("doc %v\ncode %v", doc, code)
	}
}

// Live 10-22: 72 of 95 malformed lines on the buses name several recipients ("-> crius:lead, quotes:b1", also
// "-> crius:lead quotes:all"). They are good lines: To keeps what was written, Recipients splits it.
func TestSeveralRecipients(t *testing.T) {
	for line, want := range map[string][]string{
		"2026-10-20T23:27 INFO quotes:rig -> crius:lead, quotes:lead — Cache waits — found": {"crius:lead", "quotes:lead"},
		"2026-10-20T23:28 ORDER crius:lead -> crius:lead quotes:all — go":                   {"crius:lead", "quotes:all"},
		"2026-10-20T23:29 DONE quotes:lead -> crius:lead — a — b":                           {"crius:lead"},
	} {
		p := ParseLine(line, nil, true)
		if p == nil {
			t.Errorf("not parsed: %q", line)
			continue
		}
		if got := p.Recipients(); !slices.Equal(got, want) {
			t.Errorf("%q: recipients %q, want %q", line, got, want)
		}
	}
	if p := ParseLine("2026-10-20T23:29 DONE quotes:lead -> crius:lead — a — b", nil, true); p.Text != "a — b" {
		t.Errorf("text %q", p.Text)
	}
	for _, line := range []string{
		"2026-10-20T23:31 VERIFIED crius:lead -> password: hunter2 — x",
		"2026-10-20T23:32 INFO crius:lead -> crius:build (when free) — x",
	} {
		if ParseLine(line, nil, true) != nil {
			t.Errorf("parsed %q", line)
		}
	}
}

// Live quotes.log 10-22: 11 lines copy others in, "-> quotes:p1 (cc quotes:p2, quotes:p6)", and the digest counted
// them malformed. bus-post and the digest share this parser, so both take the form; a cc'd name is a recipient.
func TestCcRecipients(t *testing.T) {
	for line, want := range map[string][]string{
		"2026-10-22T16:25 ANSWER quotes:lead -> quotes:p1 (cc quotes:p2, quotes:p6) — your NEED": {"quotes:p1", "quotes:p2", "quotes:p6"},
		"2026-10-22T16:26 ORDER quotes:lead -> quotes:p1, quotes:p3 (cc crius:lead) — go":        {"quotes:p1", "quotes:p3", "crius:lead"},
	} {
		p := ParseLine(line, nil, true)
		if p == nil {
			t.Errorf("not parsed: %q", line)
			continue
		}
		if got := p.Recipients(); !slices.Equal(got, want) {
			t.Errorf("%q: recipients %q, want %q", line, got, want)
		}
	}
	if _, err := FormatLine("ANSWER", "quotes", "lead", "x", "quotes:p1 (cc quotes:p2)", time.Now()); err != nil {
		t.Errorf("bus-post refuses a cc line: %v", err)
	}
	for _, line := range []string{
		"2026-10-22T16:27 INFO crius:lead -> quotes:p1 (cc password: hunter2) — x",
		"2026-10-22T16:28 INFO crius:lead -> quotes:p1 (cc quotes:p2 — x",
		"2026-10-22T16:29 INFO crius:lead -> quotes:p1 (cc ) — x",
	} {
		if ParseLine(line, nil, true) != nil {
			t.Errorf("parsed %q", line)
		}
	}
}

// A team bus is read as its writers write it (Crius 10-22 19:43): their own types, a bare sender (the ingest files it
// as <log project>:<lower name>) and no "->" (to all).
func TestTeamWritersShapesParse(t *testing.T) {
	for line, want := range map[string]Line{
		"2026-10-22T10:00 READY demo:p1 -> demo:lead — x":   {Type: "READY", Project: "demo", Role: "p1", To: "demo:lead"},
		"2026-10-22T10:01 PASS demo:qa1 -> demo:lead — x":   {Type: "PASS", Project: "demo", Role: "qa1", To: "demo:lead"},
		"2026-10-22T10:02 FAIL demo:qa1 -> demo:lead — x":   {Type: "FAIL", Project: "demo", Role: "qa1", To: "demo:lead"},
		"2026-10-22T10:03 LANDED demo:lead -> demo:all — x": {Type: "LANDED", Project: "demo", Role: "lead", To: "demo:all"},
		"2026-10-22T10:04 CORRECTION demo:lead -> demo:all — x": {Type: "CORRECTION", Project: "demo", Role: "lead",
			To: "demo:all"},
		"2026-10-22T10:05 DONE QA2 -> demo:lead — x": {Type: "DONE", Project: "", Role: "QA2", To: "demo:lead"},
		"2026-10-22T10:06 INFO demo:p1 — x":          {Type: "INFO", Project: "demo", Role: "p1", To: ""},
		"2026-10-22T10:07 READY P6 — x":              {Type: "READY", Project: "", Role: "P6", To: ""},
	} {
		p := ParseLine(line, nil, true)
		if p == nil {
			t.Errorf("not parsed: %q", line)
			continue
		}
		if p.Type != want.Type || p.Project != want.Project || p.Role != want.Role || p.To != want.To || p.Text != "x" {
			t.Errorf("%q: got %s %q:%q -> %q — %q, want %s %q:%q -> %q — x", line, p.Type, p.Project, p.Role, p.To,
				p.Text, want.Type, want.Project, want.Role, want.To)
		}
	}
	for _, line := range []string{
		"2026-10-22T10:08 DONE Q_A -> demo:lead — x",        // a bare sender is still a name
		"2026-10-22T10:09 DONE demo:p1 -> — x",              // an arrow with no recipient
		"2026-10-22T10:10 DONE password:hunter2 secret — x", // no arrow: the sender is one word, then the separator
	} {
		if p := ParseLine(line, nil, true); p != nil {
			t.Errorf("parsed %q as %+v", line, *p)
		}
	}
}
