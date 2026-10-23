package quota

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func signIn(t *testing.T, dir, uuid string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	b := `{"oauthAccount":{"accountUuid":"` + uuid + `","billingType":"stripe_subscription","emailAddress":"x@example.com"}}`
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(b), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeAccounts(t *testing.T, home, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(home, "accounts.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The login is a hash of the account id: never the id or the email (Crius 13:41).
func TestLoginOfHashesTheAccountID(t *testing.T) {
	d := t.TempDir()
	signIn(t, filepath.Join(d, "a"), "uuid-one")
	l, err := LoginOf(filepath.Join(d, "a"))
	if err != nil || len(l) != 12 || strings.Contains(l, "uuid") || strings.Contains(l, "example") {
		t.Fatalf("LoginOf = %q, %v; want 12 hex digits", l, err)
	}
	signIn(t, filepath.Join(d, "b"), "uuid-one")
	if l2, _ := LoginOf(filepath.Join(d, "b")); l2 != l {
		t.Fatalf("one login in two folders: %q and %q", l, l2)
	}
	signIn(t, filepath.Join(d, "c"), "uuid-two")
	if l3, _ := LoginOf(filepath.Join(d, "c")); l3 == l {
		t.Fatal("two logins share a hash")
	}
	if l, err := LoginOf(filepath.Join(d, "none")); l != "" || err != nil {
		t.Fatalf("a folder with no .claude.json: %q, %v; want no login, no error", l, err)
	}
	os.WriteFile(filepath.Join(d, "a", ".claude.json"), []byte("{bad"), 0o600)
	if _, err := LoginOf(filepath.Join(d, "a")); err == nil {
		t.Fatal("a damaged .claude.json reads as no login")
	}
}

// Several folders may share one row; the row of a login is the one with a folder signed into it.
func TestAccountsNameEachLogin(t *testing.T) {
	home, d := t.TempDir(), t.TempDir()
	signIn(t, filepath.Join(d, "claude"), "u-max1")
	signIn(t, filepath.Join(d, "work-max"), "u-max1")
	signIn(t, filepath.Join(d, "max2"), "u-max2")
	writeAccounts(t, home, `
[[account]]
name = "max1"
plan = "max"
dirs = ["`+filepath.Join(d, "claude")+`", "`+filepath.Join(d, "work-max")+`"]

[[account]]
name = "max2"
plan = "max"
dirs = ["`+filepath.Join(d, "max2")+`"]

[[account]]
name = "max3"
plan = "max"
dirs = ["`+filepath.Join(d, "not-signed-in")+`"]
`)
	as, err := LoadAccounts(home)
	if err != nil {
		t.Fatal(err)
	}
	l1, _ := LoginOf(filepath.Join(d, "work-max"))
	l2, _ := LoginOf(filepath.Join(d, "max2"))
	if a := as.Of(l1); a == nil || a.Name != "max1" || a.Plan != "max" {
		t.Fatalf("login of work-max: %+v; want max1", a)
	}
	if a := as.Of(l2); a == nil || a.Name != "max2" {
		t.Fatalf("login of max2: %+v; want max2", a)
	}
	if a := as.Of("0123456789ab"); a != nil {
		t.Fatalf("an unlisted login: %+v; want none", a)
	}
	if got := as.Name("0123456789ab"); got != "login 0123456789ab" {
		t.Fatalf("Name of an unlisted login = %q", got)
	}
	if got := as.Name(l2); got != "max2" {
		t.Fatalf("Name(max2's login) = %q", got)
	}
	if n := len(as.Rows); n != 3 || as.Rows[2].Login != "" {
		t.Fatalf("rows %+v; want 3, max3 with no login", as.Rows)
	}
	if a := as.Dir(filepath.Join(d, "max2")); a == nil || a.Name != "max2" {
		t.Fatalf("row of the max2 folder: %+v", a)
	}
}

func TestAccountsFileRefusesMistakes(t *testing.T) {
	d := t.TempDir()
	signIn(t, filepath.Join(d, "a"), "u1")
	signIn(t, filepath.Join(d, "b"), "u1")
	for name, body := range map[string]string{
		"unknown key": "[[account]]\nname = \"m\"\nplan = \"max\"\ndirs = [\"/x\"]\nreset = \"Fri\"\n",
		"bad plan":    "[[account]]\nname = \"m\"\nplan = \"pro\"\ndirs = [\"/x\"]\n",
		"no dirs":     "[[account]]\nname = \"m\"\nplan = \"max\"\n",
		"bad name":    "[[account]]\nname = \"M 1\"\nplan = \"max\"\ndirs = [\"/x\"]\n",
		"same name":   "[[account]]\nname = \"m\"\nplan = \"max\"\ndirs = [\"/x\"]\n[[account]]\nname = \"m\"\nplan = \"max\"\ndirs = [\"/y\"]\n",
		"same login":  "[[account]]\nname = \"m\"\nplan = \"max\"\ndirs = [\"" + filepath.Join(d, "a") + "\"]\n[[account]]\nname = \"n\"\nplan = \"max\"\ndirs = [\"" + filepath.Join(d, "b") + "\"]\n",
		"dir in two":  "[[account]]\nname = \"m\"\nplan = \"max\"\ndirs = [\"/x\"]\n[[account]]\nname = \"n\"\nplan = \"max\"\ndirs = [\"/x\"]\n",
		"not toml":    "[[account]\n",
	} {
		home := t.TempDir()
		writeAccounts(t, home, body)
		if _, err := LoadAccounts(home); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
	if as, err := LoadAccounts(t.TempDir()); err != nil || len(as.Rows) != 0 {
		t.Fatalf("no accounts file: %+v, %v; want no rows, no error", as, err)
	}
}

// Review 10-23: a folder and a link to it in one row (~/.claude-work-max and the folder it points to) were refused as
// "in accounts max1 and max1". In one row they are one folder; either spelling finds the row.
func TestALinkAndItsFolderInOneRowLoad(t *testing.T) {
	d, home := t.TempDir(), t.TempDir()
	dir, link := filepath.Join(d, "max1"), filepath.Join(d, "link")
	signIn(t, dir, "u1")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	writeAccounts(t, home, "[[account]]\nname = \"max1\"\nplan = \"max\"\ndirs = [\""+dir+"\", \""+link+"\"]\n")
	as, err := LoadAccounts(home)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{dir, link} {
		if a := as.Dir(p); a == nil || a.Name != "max1" {
			t.Errorf("%s: %+v, want max1", p, a)
		}
	}
}

// Review 10-23: Claude Code with no CLAUDE_CONFIG_DIR keeps its login in ~/.claude.json, beside ~/.claude, not in it.
// A row naming ~/.claude reads that file when the folder has none of its own.
func TestTheDefaultFolderReadsTheLoginBesideIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	signIn(t, home, "u1")
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	want, _ := LoginOf(home)
	if l, err := LoginOf(filepath.Join(home, ".claude")); err != nil || l == "" || l != want {
		t.Fatalf("LoginOf(~/.claude) = %q, %v; want %q from ~/.claude.json", l, err, want)
	}
	if l, err := LoginOf(filepath.Join(home, ".claude-max2")); l != "" || err != nil {
		t.Fatalf("another folder with no .claude.json: %q, %v; want no login", l, err)
	}
}
