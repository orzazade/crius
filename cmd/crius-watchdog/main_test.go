package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/testutil"
)

func setup(t *testing.T) string {
	root, home := testutil.Temp(t)
	os.MkdirAll(filepath.Join(home, "bus"), 0o700)
	tx := filepath.Join(root, "projects")
	testutil.Write(t, filepath.Join(home, "projects.toml"), "[transcripts]\npersonal = \""+tx+"\"\n")
	testutil.Write(t, filepath.Join(tx, "-x", "s.jsonl"), "{}\n")
	testutil.Write(t, filepath.Join(home, "heartbeat"), "x")
	return home
}

func busLines(t *testing.T, home string) []string {
	b, err := os.ReadFile(filepath.Join(home, "bus", "crius.log"))
	if os.IsNotExist(err) {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// Codex P2 on the Go version: a failure to read the wake time went to stderr only and the run exited 0.
func TestAWakeTimeErrorIsPostedOnceAndExits1(t *testing.T) {
	home := setup(t)
	now := time.Now()
	for i := 0; i < 2; i++ {
		if rc := run(home, now, time.Time{}, errors.New("sysctl failed"), io.Discard); rc != 1 {
			t.Fatal(rc)
		}
	}
	got := busLines(t, home)
	if len(got) != 1 || !strings.Contains(got[0], "wake time") {
		t.Fatal(got)
	}
}

func TestAHealthyCheckExits0AndPostsNothing(t *testing.T) {
	home := setup(t)
	if rc := run(home, time.Now(), time.Time{}, nil, io.Discard); rc != 0 || busLines(t, home) != nil {
		t.Fatal(rc, busLines(t, home))
	}
}

// Step 4 P1: an error can quote a secret; neither stderr nor the ALERT line may carry it.
func TestAnErrorQuotingASecretIsRedacted(t *testing.T) {
	home := setup(t)
	key := "ghp_" + strings.Repeat("C", 36)
	var errb strings.Builder
	if rc := run(home, time.Now(), time.Time{}, errors.New("sysctl failed token "+key), &errb); rc != 1 {
		t.Fatal(rc)
	}
	got := strings.Join(busLines(t, home), "\n") + errb.String() + testutil.Read(t, filepath.Join(home, "watchdog.error"))
	if strings.Contains(got, key) || !strings.Contains(got, "[redacted]") {
		t.Fatal(got)
	}
}
