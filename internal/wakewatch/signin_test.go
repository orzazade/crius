package wakewatch

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/quota"
)

// signinNow is the fake clock of the sign-in tests: the fixtures' end dates are set against it.
var signinNow = time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "signin", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// reads gives every account the fixture named for it, "ok.json" when none is named; an error string makes the read fail.
func reads(t *testing.T, by map[string]string) func(Account) ([]byte, error) {
	return func(a Account) ([]byte, error) {
		name, ok := by[a.Name]
		if !ok {
			name = "ok.json"
		}
		if strings.HasPrefix(name, "err:") {
			return nil, errors.New(strings.TrimPrefix(name, "err:"))
		}
		return fixture(t, name), nil
	}
}

func TestSigninStateOfEachStatus(t *testing.T) {
	for _, c := range []struct{ file, state, why string }{
		{"ok.json", SigninOK, ""},
		{"ends-in-4-days.json", SigninOK, ""}, // exactly 4 days left is not under 4 days
		{"ends-soon.json", SigninWarn, "3d 23h left"},
		{"ended.json", SigninExpired, "ended"},
		{"signed-out.json", SigninExpired, "signed out"},
		{"no-end.json", SigninUnknown, "no loginExpiresAt"},
		{"no-loggedin.json", SigninUnread, "no loggedIn"},
		{"not-json.txt", SigninUnread, "not JSON"},
		{"err:ssh: connect to host 100.109.19.101 port 22: Operation timed out", SigninUnread, "timed out"},
	} {
		rows := Signins(SigninReads{Status: reads(t, map[string]string{"work-max3": c.file}), Login: logins(nil), Spool: spools(t, nil)}, t.TempDir(), signinNow).Rows
		var got *SigninRow
		for i := range rows {
			if rows[i].Name == "work-max3" {
				got = &rows[i]
			} else if rows[i].State != SigninOK {
				t.Fatalf("%s: %s is %s, want OK", c.file, rows[i].Name, rows[i].State)
			}
		}
		if got == nil {
			t.Fatal("no work-max3 row")
		}
		if got.State != c.state || !strings.Contains(got.Why, c.why) {
			t.Fatalf("%s: state %s why %q, want %s with %q", c.file, got.State, got.Why, c.state, c.why)
		}
	}
}

func TestSigninAccountsAreTheMaxDirsNeverTheEnterpriseOne(t *testing.T) {
	want := map[string]bool{
		":~/.claude": true, ":~/.claude-work-max": true, ":~/.claude-work-max3": true,
		"legion:~/.claude-work-max1": true, "legion:~/.claude-work-max3": true,
		"thinkpad:~/.claude": true, "thinkpad:~/.claude-max3": true,
	}
	names := map[string]bool{}
	for _, a := range Accounts {
		if strings.TrimSuffix(a.Dir, "/") == "~/.claude-work" {
			t.Fatalf("%s reads the enterprise dir", a.Name)
		}
		if !want[a.Host+":"+a.Dir] {
			t.Fatalf("unexpected account %+v", a)
		}
		delete(want, a.Host+":"+a.Dir)
		if names[a.Name] {
			t.Fatalf("two accounts named %s", a.Name)
		}
		names[a.Name] = true
	}
	if len(want) != 0 {
		t.Fatalf("accounts missing: %v", want)
	}
}

func TestSigninRenewCommandIsExact(t *testing.T) {
	seen := 0
	for _, a := range Accounts {
		switch a.Name {
		case "work-max3":
			seen++
			if got := a.Renew(); got != "CLAUDE_CONFIG_DIR=~/.claude-work-max3 ~/.local/bin/claude auth login" {
				t.Fatal(got)
			}
		case "legion-max1":
			seen++
			want := `~/.crius/state/quotes/build/legion/E -t crius@100.109.19.101 'c=$(ls -dv ~/.nvm/versions/node/*/bin/claude ~/.local/bin/claude 2>/dev/null | while read -r f; do [ -f "$f" ] && [ -x "$f" ] && echo "$f"; done | tail -1); ` +
				`[ -n "$c" ] || { echo "claude not found at ~/.nvm/versions/node/*/bin/claude ~/.local/bin/claude" >&2; exit 69; }; ` +
				`CLAUDE_CONFIG_DIR=~/.claude-work-max1 "$c" auth login'`
			if got := a.Renew(); got != want {
				t.Fatalf("got  %s\nwant %s", got, want)
			}
		}
	}
	if seen != 2 {
		t.Fatalf("checked %d of 2 accounts", seen)
	}
}

func runSignin(t *testing.T, home string, now time.Time, by map[string]string) []string {
	t.Helper()
	f := &fake{signin: reads(t, by)}
	if err := Run(f.deps(home, now)); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, s := range f.sent {
		if strings.Contains(s, "sign-in") {
			out = append(out, s)
		}
	}
	return out
}

func TestSigninWarnsOncePerAccountPerDayWithTheRenewCommand(t *testing.T) {
	home := setup(t, nil)
	soon := map[string]string{"work-max3": "ends-soon.json"}
	got := runSignin(t, home, signinNow, soon)
	if len(got) != 1 || !strings.Contains(got[0], "WARN") || !strings.Contains(got[0], "work-max3") ||
		!strings.Contains(got[0], "2026-11-19") ||
		!strings.Contains(got[0], "CLAUDE_CONFIG_DIR=~/.claude-work-max3 ~/.local/bin/claude auth login") {
		t.Fatalf("first run: %q", got)
	}
	// the 5 min runs after it, past the 20 min resend, say nothing more that day
	for _, m := range []int{5, 25, 60, 23*60 + 55} {
		if got := runSignin(t, home, signinNow.Add(time.Duration(m)*time.Minute), soon); len(got) != 0 {
			t.Fatalf("after %d min: %q", m, got)
		}
	}
	if got := runSignin(t, home, signinNow.Add(24*time.Hour), soon); len(got) != 1 {
		t.Fatalf("next day: %q", got)
	}
}

func TestSigninEndedSameDayAsAWarnAlertsAgain(t *testing.T) {
	home := setup(t, nil)
	if got := runSignin(t, home, signinNow, map[string]string{"work-max3": "ends-soon.json"}); len(got) != 1 {
		t.Fatal(got)
	}
	got := runSignin(t, home, signinNow.Add(time.Hour), map[string]string{"work-max3": "signed-out.json"})
	if len(got) != 1 || !strings.Contains(got[0], "EXPIRED") || !strings.Contains(got[0], "auth login") {
		t.Fatal(got)
	}
}

func TestSigninCouldNotReadIsLoudAndOncePerDay(t *testing.T) {
	withThirdLogin(t)
	home := setup(t, nil)
	down := map[string]string{"legion-max1": "err:ssh: connect timed out", "legion-third": "not-json.txt", "legion-max3": "no-end.json"}
	// UNKNOWN wakes at once; a failed read wakes on the second run in a row
	first := runSignin(t, home, signinNow, down)
	if len(first) != 1 || !strings.Contains(first[0], "UNKNOWN legion-max3") {
		t.Fatalf("first run, want only the UNKNOWN: %q", first)
	}
	got := append(first, runSignin(t, home, signinNow.Add(5*time.Minute), down)...)
	if len(got) != 3 {
		t.Fatalf("want 3 alerts: %q", got)
	}
	all := strings.Join(got, "\n")
	for _, w := range []string{"UNREAD legion-max1", "could not read", "connect timed out", "(2 reads in a row)", "UNREAD legion-third", "UNKNOWN legion-max3", "no loginExpiresAt"} {
		if !strings.Contains(all, w) {
			t.Fatalf("no %q in %q", w, all)
		}
	}
	if got := runSignin(t, home, signinNow.Add(30*time.Minute), down); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestSigninAllFineSaysNothing(t *testing.T) {
	home := setup(t, nil)
	if got := runSignin(t, home, signinNow, nil); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestSigninTableShowsEachAccount(t *testing.T) {
	out := SigninTable(Signins(SigninReads{Status: reads(t, map[string]string{"work-max3": "ends-soon.json", "legion-max3": "err:no route"}), Login: logins(nil), Spool: spools(t, nil)}, t.TempDir(), signinNow), time.UTC)
	for _, w := range []string{"work-max3", "2026-11-19 23:00", "WARN", "personal", "2026-12-11 11:03", "OK", "legion-max3", "UNREAD", "no route", "25d 11h left; WARN from 2026-12-07 11:03"} {
		if !strings.Contains(out, w) {
			t.Fatalf("no %q in\n%s", w, out)
		}
	}
	if n := strings.Count(strings.TrimSpace(out), "\n") + 1; n < len(Accounts)+1 {
		t.Fatalf("%d lines for %d accounts:\n%s", n, len(Accounts), out)
	}
}

func TestSigninNotJSONNeverQuotesTheOutput(t *testing.T) {
	// plain text from a future CLI could carry the account's email: the alert names only its size
	r := signin(Accounts[0], []byte("Logged in as someone@example.com\n"), nil, signinNow)
	if r.State != SigninUnread || strings.Contains(r.Why, "example.com") || !strings.Contains(r.Why, "33 bytes") {
		t.Fatal(r.State, r.Why)
	}
}

// runRemote runs a remote account's command as its machine's bash would, with home as HOME: no real claude is touched.
func runRemote(t *testing.T, a Account, home string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command("bash", "-c", a.Remote("status"))
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
	var o, e strings.Builder
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	var ee *exec.ExitError
	if err != nil && !errors.As(err, &ee) {
		t.Fatal(err)
	}
	return o.String(), e.String(), cmd.ProcessState.ExitCode()
}

func remoteAccount(t *testing.T) Account {
	t.Helper()
	for _, a := range Accounts {
		if a.Name == "thinkpad-claude" {
			return a
		}
	}
	t.Fatal("no thinkpad-claude account")
	return Account{}
}

// 11-17: during a CLI update the remote lookup found no binary for a moment, bash ran an empty command and the alert read
// "bash: line 1: : command not found". With no binary the command says where it looked and exits with its own code.
func TestRemoteClaudeMissingSaysWhereItLooked(t *testing.T) {
	_, stderr, code := runRemote(t, remoteAccount(t), t.TempDir())
	if code != ClaudeMissing {
		t.Fatalf("exit code %d, want 69 (claude not found); stderr %q", code, stderr)
	}
	if want := "claude not found at ~/.nvm/versions/node/*/bin/claude ~/.local/bin/claude"; !strings.Contains(stderr, want) {
		t.Fatalf("stderr %q does not say %q", stderr, want)
	}
}

// Positive control: with a claude there, the same command runs it with the account's dir.
func TestRemoteClaudeFoundRunsIt(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, ".nvm", "versions", "node", "v22.1.0", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\necho \"$CLAUDE_CONFIG_DIR $*\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, stderr, code := runRemote(t, remoteAccount(t), home)
	if want := home + "/.claude auth status\n"; code != 0 || out != want {
		t.Fatalf("exit %d, stdout %q (want %q), stderr %q", code, out, want, stderr)
	}
}

// A read that fails once and then works (11-17: five false wakes in 70 minutes, each read fine a minute later) wakes
// nobody; a later single failure starts the count again.
func TestSigninOneFailedReadThenAGoodOneWakesNobody(t *testing.T) {
	home := setup(t, nil)
	down := map[string]string{"thinkpad-claude": "err:exit status 69: claude not found at ~/.local/bin/claude"}
	for i, c := range []struct {
		by map[string]string
		at time.Duration
	}{{down, 0}, {nil, 5 * time.Minute}, {down, 10 * time.Minute}, {nil, 15 * time.Minute}} {
		if got := runSignin(t, home, signinNow.Add(c.at), c.by); len(got) != 0 {
			t.Fatalf("run %d: a single failed read woke Crius: %q", i+1, got)
		}
	}
}

// A failure that lasts wakes Crius on the second failed read in a row, once that day.
func TestSigninTwoFailedReadsInARowWakeOnce(t *testing.T) {
	home := setup(t, nil)
	down := map[string]string{"thinkpad-claude": "err:exit status 69: claude not found at ~/.local/bin/claude"}
	if got := runSignin(t, home, signinNow, down); len(got) != 0 {
		t.Fatalf("first failed read woke Crius: %q", got)
	}
	got := runSignin(t, home, signinNow.Add(5*time.Minute), down)
	if len(got) != 1 || !strings.Contains(got[0], "UNREAD thinkpad-claude") || !strings.Contains(got[0], "claude not found") {
		t.Fatalf("second failed read in a row: %q", got)
	}
	for _, m := range []int{10, 30, 60} {
		if got := runSignin(t, home, signinNow.Add(time.Duration(m)*time.Minute), down); len(got) != 0 {
			t.Fatalf("after %d min: %q", m, got)
		}
	}
}

// review 11-17: an update can leave the bin link pointing at a binary that is gone; that is "not found" too.
func TestRemoteClaudeDanglingLinkSaysWhereItLooked(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "gone", "claude"), filepath.Join(bin, "claude")); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runRemote(t, remoteAccount(t), home); code != ClaudeMissing || !strings.Contains(stderr, "claude not found at") {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
}

// Every Mac folder of a Max row of this machine's accounts.toml is in Accounts, so its sign-in and week are watched.
// Skipped where there is none (CI). A folder in Accounts that no row names is fine: its login is off the list and raises
// no alert (SigninRow.OffList; 11-24 max2 and 11-26 max3 woke Crius each night for a row taken out of accounts.toml).
func TestSigninMacAccountsCoverTheFoldersOfAccountsToml(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	crius := filepath.Join(home, ".crius")
	if _, err := os.Stat(filepath.Join(crius, "accounts.toml")); errors.Is(err, os.ErrNotExist) {
		t.Skip("no ~/.crius/accounts.toml on this machine")
	}
	as, err := quota.LoadAccounts(crius)
	if err != nil {
		t.Fatal(err)
	}
	inToml := map[string]bool{}
	for _, a := range as.Rows {
		if a.Plan != "max" { // the enterprise account is never watched (owner 11-07)
			continue
		}
		for _, d := range a.Dirs {
			inToml[filepath.Clean(d)] = true
		}
	}
	watched := map[string]bool{}
	for _, a := range Accounts {
		if a.Host != "" {
			continue
		}
		d := filepath.Join(home, strings.TrimPrefix(a.Dir, "~/"))
		watched[d] = true
	}
	for d := range inToml {
		if !watched[d] {
			t.Errorf("accounts.toml names %s, which Accounts does not watch on the Mac", d)
		}
	}
}
