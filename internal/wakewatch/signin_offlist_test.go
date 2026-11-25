package wakewatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/quota"
	"github.com/orzazade/crius/internal/testutil"
)

// offListHome is a home whose accounts.toml has one row, max1, on a folder signed into its own login; the max3 rows of
// Accounts (login c07a2a080f26) are in no row, as on 11-26 when max3's row came out of accounts.toml (payment past due).
func offListHome(t *testing.T) (home, max1 string) {
	t.Helper()
	home = setup(t, nil)
	dir := t.TempDir()
	testutil.Write(t, filepath.Join(dir, ".claude.json"), `{"oauthAccount":{"accountUuid":"uuid-max1-test"}}`)
	testutil.Write(t, filepath.Join(home, "accounts.toml"), "[[account]]\nname = \"max1\"\nplan = \"max\"\ndirs = ["+`"`+dir+`"`+"]\n")
	l, err := quota.LoginOf(dir)
	if err != nil || l == "" {
		t.Fatalf("login of the test folder: %q %v", l, err)
	}
	return home, l
}

// 11-26 00:0x: wakewatch kept sending "week UNKNOWN login c07a2a080f26" for max3, whose row is out of accounts.toml (unpaid).
// A config dir signed into a login no accounts.toml row names is not Crius's account: no week and no sign-in alert for it,
// and the morning table says it is not watched. The rows of a listed login still alert (a sign-in WARN of personal).
func TestALoginOutOfAccountsTomlRaisesNoAlert(t *testing.T) {
	home, max1 := offListHome(t)
	lg := map[string]string{"personal": max1, "work-max": max1, "legion-max1": max1, "thinkpad-claude": max1}
	f := &fake{login: logins(lg), signin: reads(t, map[string]string{"personal": "ends-soon.json", "work-max3": "signed-out.json", "legion-max3": "ended.json"}),
		spool: func(host string) ([]byte, error) {
			if host != "" {
				return nil, nil
			}
			return oneLine(max1, 40, signinNow.Add(-time.Minute), signinNow.Add(30*time.Hour)), nil
		}}
	all := strings.Join(runAll(t, home, signinNow, f), "\n")
	for _, bad := range []string{"c07a2a080f26", "max3"} {
		if strings.Contains(all, bad) {
			t.Errorf("an alert names %s, whose login is in no accounts.toml row:\n%s", bad, all)
		}
	}
	if !strings.Contains(all, "sign-in WARN personal") {
		t.Errorf("positive control: the listed login's WARN is missing:\n%s", all)
	}
	w := Signins(SigninReads{Status: reads(t, nil), Login: logins(lg), Spool: func(string) ([]byte, error) { return nil, nil }}, home, signinNow)
	out := SigninTable(w, time.UTC)
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "work-max3 ") && !strings.Contains(l, "not in accounts.toml") {
			t.Errorf("the table does not say work-max3 is not watched: %q", l)
		}
	}
}

// With no accounts.toml (or no rows) no login is known to be off the list: every row still alerts, never a silent skip.
func TestWithNoAccountsTomlEveryLoginStillAlerts(t *testing.T) {
	home := setup(t, nil)
	if _, err := os.Stat(filepath.Join(home, "accounts.toml")); err == nil {
		t.Fatal("setup wrote an accounts.toml")
	}
	f := &fake{login: logins(nil), signin: reads(t, map[string]string{"work-max3": "signed-out.json"}), spool: spools(t, nil)}
	if all := strings.Join(runAll(t, home, signinNow, f), "\n"); !strings.Contains(all, "sign-in EXPIRED work-max3") {
		t.Fatalf("no accounts.toml: work-max3's sign-out is silent:\n%s", all)
	}
}

// The dry run gives the same alerts a run would, from a fresh state: the listed login's WARN, nothing for max3.
func TestSigninAlertsDryRunSkipsTheOffListLogin(t *testing.T) {
	home, max1 := offListHome(t)
	lg := map[string]string{"personal": max1, "work-max": max1, "legion-max1": max1, "thinkpad-claude": max1}
	got := strings.Join(SigninAlerts(Deps{Home: home, Now: signinNow, Login: logins(lg),
		Signin: reads(t, map[string]string{"personal": "ends-soon.json", "work-max3": "signed-out.json"}),
		Spool: func(string) ([]byte, error) {
			return oneLine(max1, 40, signinNow.Add(-time.Minute), signinNow.Add(30*time.Hour)), nil
		}}), "\n")
	if !strings.Contains(got, "sign-in WARN personal") || strings.Contains(got, "max3") || strings.Contains(got, "c07a2a080f26") {
		t.Fatalf("%s", got)
	}
}
