package quota

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// The sweep's copy of a machine's spool, as Pick reads it: a good read keeps the lines and clears an older failure; a
// failed read keeps the older copy and is said, with the time; a copy older than HostStale is said OLD; a machine never
// read is said NEVER READ; lines that are not JSON are counted. None is ever quiet.
func TestHostLinesSaysEachMachinesCopyAndItsAge(t *testing.T) {
	home := t.TempDir()
	now := time.Date(2026, 11, 21, 2, 0, 0, 0, time.UTC)
	line := `{"ts":"2026-11-21T01:50:00Z","kind":"change","login":"c07a2a080f26","rl":true,"seven_pct":96,"seven_reset":1795269600,"responded":true}` + "\n"
	if err := SaveHostSpool(home, "legion", []byte(line+"{cut\n"), nil, now.Add(-4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := SaveHostSpool(home, "thinkpad", []byte(line), nil, now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := SaveHostSpool(home, "thinkpad", nil, errors.New("ssh: connect timed out"), now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	lines, notes, blind := HostLines(home, []string{"legion", "thinkpad", "box"}, now)
	if strings.Join(blind, ",") != "thinkpad,box" {
		t.Fatalf("blind: %q", blind)
	}
	if len(lines) != 2 {
		t.Fatalf("want the legion line and the thinkpad's older copy, got %d lines", len(lines))
	}
	want := []string{
		"legion spool read 4 min ago; 1 lines NOT JSON skipped",
		"thinkpad spool copy OLD: read 2 h ago; UNREAD since ",
		"box spool NEVER READ",
	}
	for i, w := range want {
		if i >= len(notes) || !strings.HasPrefix(notes[i], w) {
			t.Fatalf("note %d: want prefix %q, got %q", i, w, notes)
		}
	}
	if !strings.Contains(notes[1], "ssh: connect timed out") {
		t.Fatalf("the failed read's reason is said: %q", notes[1])
	}

	// A good read again clears the failure.
	if err := SaveHostSpool(home, "thinkpad", []byte(line), nil, now); err != nil {
		t.Fatal(err)
	}
	if _, notes, blind = HostLines(home, []string{"thinkpad"}, now); blind != nil || len(notes) != 1 || notes[0] != "thinkpad spool read under 1 min ago" {
		t.Fatalf("got %q", notes)
	}
}

// One answer per login: Weeks (the sweep) and a Report with the same lines (Pick) give the same share and reset. A line
// of a week that has reset leaves the login unknown in both; the newest window measured anywhere wins.
func TestWithLinesGivesTheSweepsWeek(t *testing.T) {
	now := time.Date(2026, 11, 21, 2, 0, 0, 0, time.UTC)
	reset := now.Add(12 * time.Hour).Unix()
	gone := now.Add(-7 * 24 * time.Hour).Unix()
	yes := true
	pct := func(v float64) *float64 { return &v }
	mk := func(ts time.Time, login string, seven float64, reset int64) Line {
		return Line{TS: ts.Format(time.RFC3339), Kind: "change", Login: login, RL: true, SevenPct: pct(seven), SevenReset: &reset, Responded: &yes}
	}
	store := &Report{Now: now, byLogin: map[string][]sample{}}
	old := lineSamples([]Line{mk(now.Add(-8*24*time.Hour), "c07a2a080f26", 40, gone)})
	store.byLogin["c07a2a080f26"] = old["c07a2a080f26"]
	store.Accounts = []AcctUse{weekOf(old["c07a2a080f26"], now).AcctUse}
	if store.Accounts[0].Ended.IsZero() {
		t.Fatal("the store alone: a week that has reset")
	}
	remote := []Line{mk(now.Add(-30*time.Minute), "c07a2a080f26", 96, reset), mk(now.Add(-20*time.Minute), "aaaaaaaaaaaa", 10, reset)}
	sweep := Weeks(append([]Line{mk(now.Add(-8*24*time.Hour), "c07a2a080f26", 40, gone)}, remote...), now)
	got := store.WithLines(remote, nil)
	for _, login := range []string{"c07a2a080f26", "aaaaaaaaaaaa"} {
		v := got.Account(login)
		if v == nil {
			t.Fatalf("%s: not in the report", login)
		}
		w := sweep[login]
		if v.SevenPct != w.SevenPct || !v.Reset.Equal(w.Reset) || !v.Ended.Equal(w.Ended) {
			t.Fatalf("%s: Pick reads 7d %v reset %v ended %v; the sweep %v %v %v", login, v.SevenPct, v.Reset, v.Ended, w.SevenPct, w.Reset, w.Ended)
		}
	}
	if v := got.Account("c07a2a080f26"); v.SevenPct != 96 {
		t.Fatalf("max3 at 96%%, got %v", v.SevenPct)
	}
	if store.Account("c07a2a080f26").SevenPct != 0 || len(store.Accounts) != 1 {
		t.Fatal("WithLines must not change the report it was given")
	}
	// A remote line from the week that has reset adds nothing: still unknown.
	if v := store.WithLines([]Line{mk(now.Add(-7*24*time.Hour-time.Hour), "c07a2a080f26", 99, gone)}, nil).Account("c07a2a080f26"); v.Ended.IsZero() {
		t.Fatalf("a reading from before the reset is unknown, got 7d %v reset %v", v.SevenPct, v.Reset)
	}
}
