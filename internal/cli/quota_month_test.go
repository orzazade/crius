package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/quota"
	"github.com/orzazade/crius/internal/testutil"
)

// No test reads the real keychain or the network: the Enterprise month is a stub unless a test sets its own.
func TestMain(m *testing.M) {
	readMonth = func(time.Time) quota.Monthly { return quota.Monthly{Reason: "test stub"} }
	os.Exit(m.Run())
}

func stubMonth(t *testing.T, m quota.Monthly) {
	old := readMonth
	readMonth = func(time.Time) quota.Monthly { return m }
	t.Cleanup(func() { readMonth = old })
}

// Owner 10-19 14:44: `quota` shows the Enterprise month (used vs limit, reset) and `quota --room` says whether the
// fallback has room; an expired login is "cannot read" with no verdict, and the text answer is partial, never 0%.
func TestQuotaShowsEnterpriseMonth(t *testing.T) {
	testutil.Temp(t)
	run("init")
	stubMonth(t, quota.Monthly{Reason: "login expired 21 Oct 10:00"})
	rc, out := run("quota", "--room")
	var r struct {
		Verdict  string
		Fallback struct {
			Verdict, Reason, Reset string
			Used, Limit            *float64
		}
	}
	if err := json.Unmarshal([]byte(strings.SplitN(out, "\n", 2)[0]), &r); err != nil || rc != exitUnavailable ||
		r.Fallback.Verdict != "unknown" || !strings.Contains(r.Fallback.Reason, "cannot read: login expired") ||
		r.Fallback.Used != nil || r.Fallback.Limit != nil || r.Fallback.Reset != "" {
		t.Fatal(rc, err, out)
	}
	rc, out = run("quota")
	if rc != exitPartial || !strings.Contains(out, "  enterprise monthly limit: cannot read (login expired 21 Oct 10:00)\n") ||
		!strings.Contains(out, "monthly limit: cannot read (login expired") {
		t.Fatal(rc, out)
	}

	reset := time.Date(2026, 11, 14, 18, 28, 47, 0, time.UTC)
	stubMonth(t, quota.Monthly{Read: true, UsedUSD: 706.94, LimitUSD: 2000, Reset: reset})
	rc, out = run("quota", "--room")
	if err := json.Unmarshal([]byte(strings.SplitN(out, "\n", 2)[0]), &r); err != nil || rc != exitUnavailable ||
		r.Verdict != "unknown" || r.Fallback.Verdict != "room" || r.Fallback.Used == nil || *r.Fallback.Used != 706.94 ||
		*r.Fallback.Limit != 2000 || r.Fallback.Reset != "2026-11-14T18:28:47Z" {
		t.Fatal(rc, err, out)
	}
	if _, out = run("quota"); !strings.Contains(out, "monthly limit: $707 of $2000 used (35%), resets") {
		t.Fatal(out)
	}
}
