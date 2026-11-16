package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/playbook"
	"github.com/orzazade/crius/internal/testutil"
)

// Seen 11-16: max1 hit its weekly limit and the run said only "7 failed model
// batches (parked)". The health line now names the cause, and it stays inside
// the 300 characters posted to the bus even with long lists before it.
func TestHealthNamesTheModelError(t *testing.T) {
	limit := "pick: claude -p: exit status 1: You've hit your weekly limit · resets Nov 19 at 2am (Asia/Baku)"
	res := &playbook.Result{Failed: []string{"check: rules: open AGENTS.md: no such file", limit, "check: claude -p: no answer in 10m0s"}}
	res.Review.Promote = []string{strings.Repeat("a-very-long-entry-id-", 6), strings.Repeat("another-long-entry-id-", 6)}
	posted := cut(report(res, time.Second, nil), 300)
	if !strings.Contains(posted, "You've hit your weekly limit") || !strings.Contains(posted, "3 failed model batches") {
		t.Fatalf("posted line has no cause: %s", posted)
	}
}

// The run picks its login as crius-spawn does for a crius session (quota.Pick
// over accounts.toml), and with no Max account to run on it fails, never runs
// on the default folder.
func TestRunPicksTheMaxAccount(t *testing.T) {
	home, d := t.TempDir(), t.TempDir()
	transcripts := filepath.Join(d, ".claude", "projects")
	m2 := filepath.Join(d, ".claude-max2-personal")
	for _, p := range []string{transcripts, m2, filepath.Join(home, "state")} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(transcripts, filepath.Join(m2, "projects")); err != nil {
		t.Fatal(err)
	}
	testutil.Write(t, filepath.Join(m2, ".claude.json"), `{"oauthAccount":{"accountUuid":"acct-2"}}`)
	testutil.Write(t, filepath.Join(home, "projects.toml"), "[transcripts]\npersonal = \""+transcripts+
		"\"\n[projects.crius]\npersona = \"personal\"\naccounts = [\"max\"]\n")
	if _, err := claudeFor(home); err == nil || !strings.Contains(err.Error(), "no Max account") {
		t.Fatalf("no accounts.toml: err %v, want a refusal naming no Max account", err)
	}
	testutil.Write(t, filepath.Join(home, "accounts.toml"), "[[account]]\nname = \"max2\"\nplan = \"max\"\ndirs = [\""+m2+"\"]\n")
	c, err := claudeFor(home)
	if err != nil || c.ConfigDir != m2 {
		t.Fatalf("ConfigDir %q, err %v: want max2's folder %s", c.ConfigDir, err, m2)
	}
}
