package wakewatch

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
		rows := Signins(SigninReads{Status: reads(t, map[string]string{"max2": c.file}), Login: logins(nil), Spool: spools(t, nil)}, t.TempDir(), signinNow).Rows
		var got *SigninRow
		for i := range rows {
			if rows[i].Name == "max2" {
				got = &rows[i]
			} else if rows[i].State != SigninOK {
				t.Fatalf("%s: %s is %s, want OK", c.file, rows[i].Name, rows[i].State)
			}
		}
		if got == nil {
			t.Fatal("no max2 row")
		}
		if got.State != c.state || !strings.Contains(got.Why, c.why) {
			t.Fatalf("%s: state %s why %q, want %s with %q", c.file, got.State, got.Why, c.state, c.why)
		}
	}
}

func TestSigninAccountsAreTheMaxDirsNeverTheEnterpriseOne(t *testing.T) {
	want := map[string]bool{
		":~/.claude": true, ":~/.claude-max2-personal": true, ":~/.claude-work-max": true, ":~/.claude-work-max3": true,
		"legion:~/.claude-work-max1": true, "legion:~/.claude-work-max2": true, "legion:~/.claude-work-max3": true,
		"thinkpad:~/.claude": true, "thinkpad:~/.claude-max2": true,
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
		case "max2":
			seen++
			if got := a.Renew(); got != "CLAUDE_CONFIG_DIR=~/.claude-max2-personal ~/.local/bin/claude auth login" {
				t.Fatal(got)
			}
		case "legion-max1":
			seen++
			want := `~/.crius/state/quotes/build/legion/E -t crius@100.109.19.101 'CLAUDE_CONFIG_DIR=~/.claude-work-max1 "$(ls -dv ~/.nvm/versions/node/*/bin/claude ~/.local/bin/claude 2>/dev/null | tail -1)" auth login'`
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
	soon := map[string]string{"max2": "ends-soon.json"}
	got := runSignin(t, home, signinNow, soon)
	if len(got) != 1 || !strings.Contains(got[0], "WARN") || !strings.Contains(got[0], "max2") ||
		!strings.Contains(got[0], "2026-11-19") ||
		!strings.Contains(got[0], "CLAUDE_CONFIG_DIR=~/.claude-max2-personal ~/.local/bin/claude auth login") {
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
	if got := runSignin(t, home, signinNow, map[string]string{"max2": "ends-soon.json"}); len(got) != 1 {
		t.Fatal(got)
	}
	got := runSignin(t, home, signinNow.Add(time.Hour), map[string]string{"max2": "signed-out.json"})
	if len(got) != 1 || !strings.Contains(got[0], "EXPIRED") || !strings.Contains(got[0], "auth login") {
		t.Fatal(got)
	}
}

func TestSigninCouldNotReadIsLoudAndOncePerDay(t *testing.T) {
	home := setup(t, nil)
	down := map[string]string{"legion-max1": "err:ssh: connect timed out", "legion-max2": "not-json.txt", "legion-max3": "no-end.json"}
	got := runSignin(t, home, signinNow, down)
	if len(got) != 3 {
		t.Fatalf("want 3 alerts: %q", got)
	}
	all := strings.Join(got, "\n")
	for _, w := range []string{"UNREAD legion-max1", "could not read", "connect timed out", "UNREAD legion-max2", "UNKNOWN legion-max3", "no loginExpiresAt"} {
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
	out := SigninTable(Signins(SigninReads{Status: reads(t, map[string]string{"max2": "ends-soon.json", "legion-max3": "err:no route"}), Login: logins(nil), Spool: spools(t, nil)}, t.TempDir(), signinNow), time.UTC)
	for _, w := range []string{"max2", "2026-11-19 23:00", "WARN", "personal", "2026-12-11 11:03", "OK", "legion-max3", "UNREAD", "no route", "25d 11h left; WARN from 2026-12-07 11:03"} {
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
