package wakewatch

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/quota"
)

// fixtureLogins are the logins each config dir holds: ThinkPad ~/.claude is max1's login (d94b…) and ThinkPad
// ~/.claude-max3 is max3's (c07a…). "third" and "legion-third" are test-only rows of a third login (0f3b…), added by
// withThirdLogin, for the cases that need a login with no fresh week sample.
var fixtureLogins = map[string]string{
	"personal": "d94b4cce88d8", "work-max": "d94b4cce88d8", "work-max3": "c07a2a080f26",
	"legion-max1": "d94b4cce88d8", "legion-max3": "c07a2a080f26",
	"thinkpad-claude": "d94b4cce88d8", "thinkpad-claude-max3": "c07a2a080f26",
	"third": "0f3b07b4c7f2", "legion-third": "0f3b07b4c7f2",
}

// withThirdLogin watches two more config dirs, a Mac one and a Legion one, that hold a third login, until the test ends.
func withThirdLogin(t *testing.T) {
	t.Helper()
	old := Accounts
	Accounts = append(append([]Account{}, old...), Account{"third", "", "~/.claude-third"}, Account{"legion-third", "legion", "~/.claude-third"})
	t.Cleanup(func() { Accounts = old })
}

func logins(by map[string]string) func(Account) (string, error) {
	return func(a Account) (string, error) {
		if v, ok := by[a.Name]; ok {
			if strings.HasPrefix(v, "err:") {
				return "", errors.New(strings.TrimPrefix(v, "err:"))
			}
			return v, nil
		}
		return fixtureLogins[a.Name], nil
	}
}

// spools gives the Mac spool-mac.jsonl and the ThinkPad spool-thinkpad.jsonl (the 11-15 limit of max1, read on the
// ThinkPad); the Legion keeps none. by names a host's other fixture, or "err:…" to fail its read.
func spools(t *testing.T, by map[string]string) func(string) ([]byte, error) {
	return func(host string) ([]byte, error) {
		name, ok := by[host]
		if !ok {
			name = map[string]string{"": "spool-mac.jsonl", "thinkpad": "spool-thinkpad.jsonl"}[host]
		}
		switch {
		case strings.HasPrefix(name, "err:"):
			return nil, errors.New(strings.TrimPrefix(name, "err:"))
		case name == "":
			return nil, nil
		}
		return fixture(t, name), nil
	}
}

func weekWatch(t *testing.T, home string, lg, sp map[string]string) SigninWatch {
	return Signins(SigninReads{Status: reads(t, nil), Login: logins(lg), Spool: spools(t, sp)}, home, signinNow)
}

func rowOf(t *testing.T, w SigninWatch, name string) SigninRow {
	t.Helper()
	for _, r := range w.Rows {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no row %s", name)
	return SigninRow{}
}

func TestWeekOfEachLoginIsReadFromEveryMachinesSpool(t *testing.T) {
	withThirdLogin(t)
	w := weekWatch(t, t.TempDir(), nil, nil)
	reset := time.Date(2026, 11, 18, 22, 0, 0, 0, time.UTC)
	for _, name := range []string{"personal", "work-max", "legion-max1", "thinkpad-claude"} {
		// max1's 100% was seen only on the ThinkPad; the Mac's later 12% heartbeat repeats an old reading
		if r := rowOf(t, w, name); r.Week != WeekLimit || r.WeekPct != 100 || !r.WeekReset.Equal(reset) {
			t.Fatalf("%s: week %s %v%% reset %v (%s)", name, r.Week, r.WeekPct, r.WeekReset, r.WeekWhy)
		}
	}
	if r := rowOf(t, w, "thinkpad-claude-max3"); r.Week != WeekOK || r.WeekPct != 34 {
		t.Fatalf("thinkpad-claude-max3: %s %v%% (%s)", r.Week, r.WeekPct, r.WeekWhy)
	}
	// the third login was last measured 12 h ago at 40%; its heartbeats since repeat that reading: not known, never OK
	for _, name := range []string{"third", "legion-third"} {
		if r := rowOf(t, w, name); r.Week != WeekUnknown || !strings.Contains(r.WeekWhy, "last measured 12h ago at 40%") {
			t.Fatalf("%s: %s (%s)", name, r.Week, r.WeekWhy)
		}
	}
}

func runWeek(t *testing.T, home string, now time.Time, f *fake) []string {
	t.Helper()
	if err := Run(f.deps(home, now)); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, s := range f.sent {
		if strings.Contains(s, "week ") {
			out = append(out, s)
		}
	}
	f.sent = nil
	return out
}

func TestWeekLimitWakesCriusOncePerLoginStateAndDayWithTheReset(t *testing.T) {
	withThirdLogin(t)
	home := setup(t, nil)
	f := &fake{login: logins(nil), spool: spools(t, nil)}
	got := runWeek(t, home, signinNow, f)
	all := strings.Join(got, "\n")
	if len(got) != 2 || strings.Count(all, "week LIMIT") != 1 || strings.Count(all, "week UNKNOWN") != 1 {
		t.Fatalf("want one LIMIT (max1) and one UNKNOWN (the third login): %q", got)
	}
	for _, w := range []string{"login d94b4cce88d8", "100%", "until 2026-11-18 22:00", "Mac ~/.claude", "thinkpad ~/.claude", "legion ~/.claude-work-max1"} {
		if !strings.Contains(all, w) {
			t.Fatalf("no %q in %q", w, all)
		}
	}
	for _, m := range []int{5, 25, 60, 5 * 60} {
		if got := runWeek(t, home, signinNow.Add(time.Duration(m)*time.Minute), f); len(got) != 0 {
			t.Fatalf("after %d min: %q", m, got)
		}
	}
	// max3's 34% was measured at 23:40 the day before; 6 h on it may have risen: a new state, said once
	got = runWeek(t, home, signinNow.Add(6*time.Hour), f)
	if len(got) != 1 || !strings.Contains(got[0], "week UNKNOWN login c07a2a080f26") || !strings.Contains(got[0], "at 34%") {
		t.Fatalf("max3 stale: %q", got)
	}
	if got := runWeek(t, home, signinNow.Add(23*time.Hour+55*time.Minute), f); len(got) != 0 {
		t.Fatalf("same day: %q", got)
	}
	if got := runWeek(t, home, signinNow.Add(24*time.Hour), f); len(got) != 3 {
		t.Fatalf("next day: %q", got)
	}
}

func oneLine(login string, pct float64, at, reset time.Time) []byte {
	return []byte(fmt.Sprintf(`{"ts":%q,"kind":"change","login":%q,"rl":true,"seven_pct":%v,"seven_reset":%d,"responded":true}`+"\n",
		at.UTC().Format(time.RFC3339), login, pct, reset.Unix()))
}

func TestWeekAt95IsNear(t *testing.T) {
	home := setup(t, nil)
	f := &fake{spool: func(host string) ([]byte, error) {
		if host != "" {
			return nil, nil
		}
		return oneLine("aaaaaaaaaaaa", 95, signinNow.Add(-time.Minute), signinNow.Add(30*time.Hour)), nil
	}}
	got := runWeek(t, home, signinNow, f)
	if len(got) != 1 || !strings.Contains(got[0], "week NEAR") || !strings.Contains(got[0], "95%") || !strings.Contains(got[0], "2026-11-17 06:00") {
		t.Fatalf("%q", got)
	}
}

func TestWeekWithNoSampleAnywhereIsUnknownNeverOK(t *testing.T) {
	home := setup(t, nil)
	f := &fake{spool: func(string) ([]byte, error) { return nil, nil }}
	got := runWeek(t, home, signinNow, f)
	if len(got) != 1 || !strings.Contains(got[0], "week UNKNOWN") || !strings.Contains(got[0], "no statusline sample") {
		t.Fatalf("%q", got)
	}
}

func TestWeekEndedWindowWithNoSampleSinceIsUnknown(t *testing.T) {
	w := Signins(SigninReads{Status: reads(t, nil), Login: logins(nil), Spool: func(host string) ([]byte, error) {
		if host != "" {
			return nil, nil
		}
		return oneLine("d94b4cce88d8", 50, signinNow.Add(-30*time.Hour), signinNow.Add(-2*time.Hour)), nil
	}}, t.TempDir(), signinNow)
	if r := rowOf(t, w, "personal"); r.Week != WeekUnknown || !strings.Contains(r.WeekWhy, "reset") {
		t.Fatalf("%s (%s)", r.Week, r.WeekWhy)
	}
}

func TestWeekSpoolThatCannotBeReadIsLoud(t *testing.T) {
	home := setup(t, nil)
	f := &fake{login: logins(nil), spool: spools(t, map[string]string{"thinkpad": "err:ssh: connect to host 100.117.3.61 port 22: Operation timed out"})}
	if got := runWeek(t, home, signinNow, f); len(got) != 0 {
		t.Fatalf("first failed read woke Crius: %q", got)
	}
	all := strings.Join(runWeek(t, home, signinNow.Add(5*time.Minute), f), "\n")
	if !strings.Contains(all, "week samples UNREAD thinkpad (2 reads in a row)") || !strings.Contains(all, "timed out") {
		t.Fatalf("%q", all)
	}
	// without the ThinkPad's spool max1's 100% is not seen: its rows say what was read, never OK on a guess
	w := weekWatch(t, t.TempDir(), nil, map[string]string{"thinkpad": "err:no route"})
	if r := rowOf(t, w, "thinkpad-claude"); r.Week != WeekUnknown || !strings.Contains(r.WeekWhy, "spool unread on thinkpad") {
		t.Fatalf("%s (%s)", r.Week, r.WeekWhy)
	}
	if out := SigninTable(w, time.UTC); !strings.Contains(out, "thinkpad") || !strings.Contains(out, "no route") {
		t.Fatal(out)
	}
}

func TestWeekLoginThatCannotBeReadIsUnknown(t *testing.T) {
	withThirdLogin(t)
	w := weekWatch(t, t.TempDir(), map[string]string{"legion-max1": "err:no answer in 20s", "legion-third": "not-a-hash"}, nil)
	if r := rowOf(t, w, "legion-max1"); r.Week != WeekUnknown || !strings.Contains(r.WeekWhy, "no answer in 20s") {
		t.Fatalf("%s (%s)", r.Week, r.WeekWhy)
	}
	if r := rowOf(t, w, "legion-third"); r.Week != WeekUnknown || !strings.Contains(r.WeekWhy, "not a login") {
		t.Fatalf("%s (%s)", r.Week, r.WeekWhy)
	}
	if r := rowOf(t, w, "legion-max3"); r.Week != WeekOK {
		t.Fatalf("%s (%s)", r.Week, r.WeekWhy)
	}
}

func TestSigninCoversTheThinkPad(t *testing.T) {
	seen := 0
	for _, a := range Accounts {
		if a.Host != "thinkpad" {
			continue
		}
		seen++
		if a.Name == "thinkpad-claude" {
			want := `~/.crius/state/staffy/thinkpad/T -t '` + RemoteClaude + `CLAUDE_CONFIG_DIR=~/.claude "$c" auth login'`
			if got := a.Renew(); got != want {
				t.Fatalf("got  %s\nwant %s", got, want)
			}
		}
	}
	if seen != 2 {
		t.Fatalf("%d ThinkPad accounts, want 2", seen)
	}
	// a ThinkPad that cannot be reached is a loud row
	w := Signins(SigninReads{Status: reads(t, map[string]string{"thinkpad-claude": "err:ssh: connect timed out"}), Login: logins(nil), Spool: spools(t, nil)}, t.TempDir(), signinNow)
	if r := rowOf(t, w, "thinkpad-claude"); r.State != SigninUnread || !strings.Contains(r.Why, "connect timed out") {
		t.Fatalf("%s (%s)", r.State, r.Why)
	}
}

func TestSigninTableShowsTheWeekAndTheThinkPad(t *testing.T) {
	withThirdLogin(t)
	out := SigninTable(weekWatch(t, t.TempDir(), nil, nil), time.UTC)
	for _, w := range []string{"LOGIN", "WEEK", "RESETS", "100% LIMIT", "2026-11-18 22:00", "34% OK", "UNKNOWN", "thinkpad-claude-max3", "thinkpad ~/.claude-max3", "login c07a2a080f26"} {
		if !strings.Contains(out, w) {
			t.Fatalf("no %q in\n%s", w, out)
		}
	}
}

func TestSpoolLinesSkipAndCountABadLineAndLeaveTheOneBeingWritten(t *testing.T) {
	good := `{"ts":"2026-11-15T23:40:00Z","kind":"change","login":"c07a2a080f26","rl":true,"seven_pct":34,"seven_reset":1795255200,"responded":true}`
	long := `{"ts":"2026-11-15T23:41:00Z","kind":"change","login":"c07a2a080f26","rl":true,"seven_pct":35,"seven_reset":1795255200,"responded":true,"pad":"` + strings.Repeat("x", 2<<20) + `"}`
	in := good + "\n" + `{"ts":"2026-11-15T2{"ts":"cut and joined"}` + "\n\n" + long + "\n" + `{"ts":"2026-11-15T23:5`
	ls, bad := quota.ParseSpool([]byte(in))
	if len(ls) != 2 || bad != 1 || *ls[1].SevenPct != 35 {
		t.Fatalf("%d lines, %d bad", len(ls), bad)
	}
	// a machine with a bad line is still read: its other lines count
	w := weekWatch(t, t.TempDir(), nil, map[string]string{"thinkpad": "spool-thinkpad-bad.jsonl"})
	if r := rowOf(t, w, "thinkpad-claude"); r.Week != WeekLimit {
		t.Fatalf("%s (%s)", r.Week, r.WeekWhy)
	}
	if w.Bad["thinkpad"] != 1 || len(w.Unread) != 0 || !strings.Contains(SigninTable(w, time.UTC), "spool thinkpad: 1 lines not JSON, skipped") {
		t.Fatalf("bad %v unread %v", w.Bad, w.Unread)
	}
}

// The remote commands run as the remote shell runs them (bash, a home of fixtures): a quoting slip would make every
// remote row UNKNOWN live and nowhere else.
func TestRemoteLoginAndSpoolCommandsRunInAShell(t *testing.T) {
	home := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(".claude-max2/.claude.json", `{"oauthAccount":{"accountUuid":"abc","emailAddress":"x@example.com"}}`)
	write(".claude.json", `{"oauthAccount":{"accountUuid":"abc"}}`) // ~/.claude keeps it beside itself
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".claude-empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(cmd string) string {
		t.Helper()
		c := exec.Command("bash", "-c", cmd)
		c.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v: %s", cmd, err, out)
		}
		return string(out)
	}
	// sha256("abc") starts ba7816bf8f01; never the id or the email
	for dir, want := range map[string]string{"~/.claude-max2": "ba7816bf8f01\n", "~/.claude": "ba7816bf8f01\n", "~/.claude-empty": "\n"} {
		if got := run(LoginRemote(dir)); got != want {
			t.Fatalf("%s: %q, want %q", dir, got, want)
		}
	}
	if got := run(SpoolRemote); got != "" {
		t.Fatalf("no spool: %q", got)
	}
	write(".crius/spool/quota.jsonl", "a\nb\n")
	if got := run(SpoolRemote); got != "a\nb\n" {
		t.Fatalf("spool: %q", got)
	}
}

// thinkpadDown fails every read on the ThinkPad the way an ssh blip does: each account's status and login, and its spool.
func thinkpadDown(t *testing.T, down bool) *fake {
	if !down {
		return &fake{login: logins(nil), spool: spools(t, nil)}
	}
	const blip = "err:ssh: connect to host 100.117.3.61 port 22: Operation timed out"
	by := map[string]string{"thinkpad-claude": blip, "thinkpad-claude-max3": blip}
	return &fake{signin: reads(t, by), login: logins(by), spool: spools(t, map[string]string{"thinkpad": blip})}
}

func runAll(t *testing.T, home string, now time.Time, f *fake) []string {
	t.Helper()
	if err := Run(f.deps(home, now)); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, s := range f.sent {
		if strings.Contains(s, "sign-in") || strings.Contains(s, "week ") {
			out = append(out, s)
		}
	}
	return out
}

// review 11-17: one ssh blip to a machine fails its spool and login reads too; it must wake nobody, like a failed status
// read. Two failed runs in a row wake for each.
func TestSigninReadBlipOnAMachineWakesNobodyTwoInARowWake(t *testing.T) {
	withThirdLogin(t)
	home := setup(t, nil)
	if got := runAll(t, home, signinNow, thinkpadDown(t, false)); len(got) != 2 {
		t.Fatalf("baseline, want max1's LIMIT and the third login's UNKNOWN: %q", got)
	}
	for i, down := range []bool{true, false, true} {
		if got := runAll(t, home, signinNow.Add(time.Duration(i+1)*5*time.Minute), thinkpadDown(t, down)); len(got) != 0 {
			t.Fatalf("run %d (ThinkPad down %v): a one-run blip woke Crius: %q", i+1, down, got)
		}
	}
	all := strings.Join(runAll(t, home, signinNow.Add(20*time.Minute), thinkpadDown(t, true)), "\n")
	for _, w := range []string{"sign-in UNREAD thinkpad-claude ", "sign-in UNREAD thinkpad-claude-max3", "week samples UNREAD thinkpad",
		"week UNKNOWN thinkpad-claude ", "week UNKNOWN thinkpad-claude-max3", "(2 reads in a row)"} {
		if !strings.Contains(all, w) {
			t.Fatalf("second failed run in a row, no %q in %q", w, all)
		}
	}
}

// 11-21: Pick must read the week the sweep reads. The sweep keeps a copy of each other machine's spool for it
// (quota.HostLines): the lines it read, or why the read failed; the Mac's own spool is in the store and is not copied.
func TestSigninsKeepsACopyOfEachOtherMachinesSpoolForPick(t *testing.T) {
	home := t.TempDir()
	tp := oneLine("c07a2a080f26", 96, signinNow.Add(-30*time.Minute), signinNow.Add(12*time.Hour))
	Signins(SigninReads{Status: reads(t, nil), Login: logins(nil), Spool: func(host string) ([]byte, error) {
		switch host {
		case "thinkpad":
			return tp, nil
		case "legion":
			return nil, errors.New("ssh: no route")
		}
		return nil, nil
	}}, home, signinNow)
	lines, notes, _ := quota.HostLines(home, quota.RemoteHosts, signinNow)
	if len(lines) != 1 || lines[0].Login != "c07a2a080f26" {
		t.Fatalf("want the thinkpad line, got %+v", lines)
	}
	all := strings.Join(notes, "\n")
	if !strings.Contains(all, "thinkpad spool read under 1 min ago") || !strings.Contains(all, "legion spool NEVER READ") || !strings.Contains(all, "ssh: no route") {
		t.Fatalf("%s", all)
	}
	if _, err := os.Stat(filepath.Join(home, "spool", "hosts", ".jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the Mac's spool is not copied")
	}
}
