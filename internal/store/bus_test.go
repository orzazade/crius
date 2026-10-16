package store

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
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
		{"17:54 DONE axi:leader -> crius — x", "2026-10-16"}, {"16:55 DONE axi:leader -> crius — x", "2026-10-16"},
		{"16:54 DONE axi:leader -> crius — x", "2026-10-17"}} {
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
	line := "09:20 ACK api -> leader — schema?"
	if ParseLine(line, &State{Date: "2026-10-16", Time: "09:00"}, true) != nil {
		t.Fatal("strict parse took an unknown type")
	}
	p := ParseLine(line, &State{Date: "2026-10-16", Time: "09:00"}, false)
	if p == nil || p.Type != "ACK" || p.Project != "" || p.Role != "api" {
		t.Fatalf("%+v", p)
	}
}

func TestNewTypesAreAccepted(t *testing.T) {
	for _, typ := range []string{"NEED-OWNER", "PAUSED", "LIMIT", "ITEM", "DECISION", "RESUMED"} {
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
		"ALERT", "INFO", "NEED", "WAIT", "PARKED"} {
		if ParseLine("2026-10-16T09:20 "+typ+" demo:leader -> crius — x", nil, true) == nil {
			t.Error(typ)
		}
	}
	for _, typ := range []string{"ACK", "CLEAR-READY", "QUEUE"} { // team-only: stay loud on the Crius bus
		if ParseLine("2026-10-16T09:20 "+typ+" demo:leader -> crius — x", nil, true) != nil {
			t.Error("team-only type parsed strict:", typ)
		}
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
