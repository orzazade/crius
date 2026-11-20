package cli

import (
	"encoding/json"
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

// Night 11-21: Pick read the other machines' spools and counted an account with no reading this week as no room, while
// `quota --room` read the Mac's store alone and said "room" for it (max3's newest Mac reading was from 11-13 while it ran
// at 96% on the Legion). --room reads quota as Pick does: the Legion's 96% makes it full, and an account with no
// reading since its week reset, or none at all, is unknown (aitch then uses its fallback), never room.
func TestQuotaRoomReadsTheOtherMachinesAsPickDoes(t *testing.T) {
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
	far, gone, never := dir("cfg-far", "acct-far"), dir("cfg-gone", "acct-gone"), dir("cfg-never", "acct-never")
	if err := os.WriteFile(filepath.Join(home, "accounts.toml"), []byte(fmt.Sprintf(
		"[[account]]\nname = \"max2\"\nplan = \"max\"\ndirs = [%q]\n\n"+
			"[[account]]\nname = \"max3\"\nplan = \"max\"\ndirs = [%q]\n\n"+
			"[[account]]\nname = \"max4\"\nplan = \"max\"\ndirs = [%q]\n", far, gone, never)), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	// The Mac's store: max2 and max3 last seen in a week that has since reset, at 40%.
	for i, d := range []string{far, gone} {
		login, err := quota.LoginOf(d)
		if err != nil || login == "" {
			t.Fatal(login, err)
		}
		if _, err := db.Exec("insert into quota (source_key, ts, account, epoch, seven_day_pct, five_hour_pct,"+
			" five_reset, sample_kind, login) values (?, ?, 'max', ?, 40, 10, ?, 'session', ?)", fmt.Sprintf("q%d", i),
			now.Add(-8*24*time.Hour).UTC().Format(time.RFC3339), now.Add(-24*time.Hour).Unix(),
			now.Add(-8*24*time.Hour+2*time.Hour).Unix(), login); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	// The Legion's copy, read 3 minutes ago: max2 at 96% this week.
	login, err := quota.LoginOf(far)
	if err != nil {
		t.Fatal(err)
	}
	seven, five, reset, fiveReset := 96.0, 10.0, now.Add(50*time.Hour).Unix(), now.Add(2*time.Hour).Unix()
	yes := true
	j, err := json.Marshal(quota.Line{TS: now.Add(-30 * time.Minute).UTC().Format(time.RFC3339), Kind: "change",
		Session: "legion-m2", Persona: "work", RL: true, SevenPct: &seven, SevenReset: &reset, FivePct: &five,
		FiveReset: &fiveReset, Login: login, Dir: "~/.claude-work-max2", Responded: &yes})
	if err != nil {
		t.Fatal(err)
	}
	p := quota.HostSpoolPath(home, "legion")
	testutil.Write(t, p, string(append(j, '\n')))
	if err := os.Chtimes(p, now.Add(-3*time.Minute), now.Add(-3*time.Minute)); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		primary string
		rc      int
		want    string
	}{
		{far, exitPartial, `"verdict":"full","reason":"max2: 7d 96%`},
		{gone, exitUnavailable, `"verdict":"unknown","reason":"max3 quota unknown: no reading since its week reset`},
		{never, exitUnavailable, `"verdict":"unknown","reason":"max4 quota unknown: not measured yet`},
	} {
		t.Setenv("AITCH_PRIMARY", c.primary)
		if rc, out := run("quota", "--room"); rc != c.rc || !strings.Contains(out, c.want) {
			t.Errorf("AITCH_PRIMARY=%s: rc %d (want %d, %s): %s", filepath.Base(c.primary), rc, c.rc, c.want, out)
		}
	}
}
