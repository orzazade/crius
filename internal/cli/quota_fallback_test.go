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
	readEnt = func(time.Time) quota.EntUsage { return quota.EntUsage{Reason: "test stub"} }
	os.Exit(m.Run())
}

func stubEnt(t *testing.T, m quota.EntUsage) {
	old := readEnt
	readEnt = func(time.Time) quota.EntUsage { return m }
	t.Cleanup(func() { readEnt = old })
}

// Owner 10-19 14:44 and Crius NEED 15:06: `quota` shows the Enterprise account's included usage (expiry) and spend
// limit, and `quota --room` says whether the fallback has room; an expired login is "cannot read" with no verdict and
// no numbers, and the text answer is partial, never 0%.
func TestQuotaShowsEnterpriseFallback(t *testing.T) {
	testutil.Temp(t)
	run("init")
	stubEnt(t, quota.EntUsage{Reason: "login expired 21 Oct 10:00"})
	rc, out := run("quota", "--room")
	var r struct {
		Verdict  string
		Fallback struct {
			Verdict, Reason string
			Included        *quota.Included   `json:"included"`
			Spend           *quota.SpendLimit `json:"spend_limit"`
		}
	}
	if err := json.Unmarshal([]byte(strings.SplitN(out, "\n", 2)[0]), &r); err != nil || rc != exitUnavailable ||
		r.Fallback.Verdict != "unknown" || !strings.Contains(r.Fallback.Reason, "cannot read: login expired") ||
		r.Fallback.Included != nil || r.Fallback.Spend != nil || strings.Contains(out, `"included":{`) {
		t.Fatal(rc, err, out)
	}
	rc, out = run("quota")
	if rc != exitPartial || !strings.Contains(out, "  enterprise fallback: cannot read (login expired 21 Oct 10:00)\n") ||
		!strings.Contains(out, "  fallback: cannot read (login expired") || strings.Contains(out, "monthly") {
		t.Fatal(rc, out)
	}

	exp := time.Date(2026, 11, 14, 18, 28, 47, 0, time.UTC)
	stubEnt(t, quota.EntUsage{Read: true, Included: quota.Included{UsedUSD: 2000, LimitUSD: 2000, Expires: exp},
		Spend: quota.SpendLimit{UsedUSD: 0, LimitUSD: 600, Enabled: true}})
	rc, out = run("quota", "--room")
	if err := json.Unmarshal([]byte(strings.SplitN(out, "\n", 2)[0]), &r); err != nil || rc != exitUnavailable ||
		r.Verdict != "unknown" || r.Fallback.Verdict != "room" || r.Fallback.Included == nil ||
		r.Fallback.Included.UsedUSD != 2000 || !r.Fallback.Included.Expires.Equal(exp) || r.Fallback.Spend == nil ||
		r.Fallback.Spend.LimitUSD != 600 || !r.Fallback.Spend.Enabled {
		t.Fatal(rc, err, out)
	}
	if _, out = run("quota"); !strings.Contains(out, "fallback: included usage $2000 of $2000 used (100%), expires") ||
		!strings.Contains(out, "spend limit $0 of $600 used, reset date not in the reply") {
		t.Fatal(out)
	}
}
