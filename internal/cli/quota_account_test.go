package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/quota"
	"github.com/orzazade/crius/internal/store"
	"github.com/orzazade/crius/internal/testutil"
)

// Owner 10-23 (several Max accounts): aitch names the primary folder it asks about in AITCH_PRIMARY, and `quota
// --room` answers for that folder's account alone: a full account is full though another has room, an account with
// room is room though another is full, a listed Max account signed in but not measured yet has room (a session on it
// is what measures it), and a login no row lists and no line measured is unknown. Without AITCH_PRIMARY the answer
// is the one for every login together, as before.
func TestQuotaRoomAnswersForTheNamedFolder(t *testing.T) {
	root, home := testutil.Temp(t)
	if rc, out := run("init"); rc != 0 {
		t.Fatal(rc, out)
	}
	dir := func(name, id string) string {
		d := filepath.Join(root, name)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, ".claude.json"),
			[]byte(`{"oauthAccount":{"accountUuid":"`+id+`"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		return d
	}
	full, roomy, fresh, stray := dir("cfg-full", "acct-full"), dir("cfg-room", "acct-room"),
		dir("cfg-fresh", "acct-fresh"), dir("cfg-stray", "acct-stray")
	old := dir("cfg-old", "acct-old")
	if err := os.WriteFile(filepath.Join(home, "accounts.toml"), []byte(fmt.Sprintf(
		"[[account]]\nname = \"max1\"\nplan = \"max\"\ndirs = [%q]\n\n"+
			"[[account]]\nname = \"max2\"\nplan = \"max\"\ndirs = [%q]\n\n"+
			"[[account]]\nname = \"max3\"\nplan = \"max\"\ndirs = [%q]\n\n"+
			"[[account]]\nname = \"max4\"\nplan = \"max\"\ndirs = [%q]\n", full, roomy, fresh, old)), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i, s := range []struct {
		dir   string
		seven float64
		reset time.Duration
	}{{full, 100, 72 * time.Hour}, {roomy, 40, 72 * time.Hour}, {old, 95, -24 * time.Hour}} {
		login, err := quota.LoginOf(s.dir)
		if err != nil || login == "" {
			t.Fatal(login, err)
		}
		if _, err := db.Exec("insert into quota (source_key, ts, account, epoch, seven_day_pct, five_hour_pct,"+
			" five_reset, sample_kind, login) values (?, ?, 'max', ?, ?, 10, ?, 'session', ?)", fmt.Sprintf("q%d", i),
			now.Add(-5*time.Minute).UTC().Format(time.RFC3339), now.Add(s.reset).Unix(), s.seven,
			now.Add(2*time.Hour).Unix(), login); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	for _, c := range []struct {
		primary string
		rc      int
		want    string
	}{
		{"", exitPartial, `"verdict":"full"`},
		{full, exitPartial, `"verdict":"full","reason":"max1: 7d 100%`},
		{roomy, 0, `"verdict":"room","reason":"max2: 7d 40%`},
		{fresh, 0, `"verdict":"room","reason":"max3 not measured yet"`},
		{old, 0, `"verdict":"room","reason":"max4 new window`},
		{stray, exitUnavailable, `"verdict":"unknown","reason":"login `},
		{filepath.Join(root, "cfg-none"), exitUnavailable, `"verdict":"unknown","reason":"no login in `},
	} {
		t.Setenv("AITCH_PRIMARY", c.primary)
		if c.primary == "" {
			os.Unsetenv("AITCH_PRIMARY")
		}
		if rc, out := run("quota", "--room"); rc != c.rc || !strings.Contains(out, c.want) {
			t.Errorf("AITCH_PRIMARY=%s: rc %d (want %d): %s", c.primary, rc, c.rc, out)
		}
	}
}
