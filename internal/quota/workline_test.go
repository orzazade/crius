package quota

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func workRender(sid string, usd float64, rl bool) []byte {
	d := map[string]any{"session_id": sid, "cost": map[string]any{"total_cost_usd": usd, "total_api_duration_ms": usd * 1000},
		"model": map[string]any{"display_name": "Opus 5.5"}, "workspace": map[string]any{"current_dir": "/w"},
		"context_window": map[string]any{"remaining_percentage": 66.0}}
	if rl {
		d["rate_limits"] = map[string]any{"seven_day": map[string]any{"used_percentage": 40, "resets_at": 1761206400}}
	}
	b, _ := json.Marshal(d)
	return b
}

var readOK = EntUsage{Read: true, Included: Included{UsedUSD: 68, LimitUSD: 200, Expires: time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)},
	Spend: SpendLimit{UsedUSD: 12, LimitUSD: 500, Enabled: true}}

func writeCache(t *testing.T, h string, u EntUsage, at time.Time) {
	t.Helper()
	if err := RefreshEnt(h, func(time.Time) EntUsage { return u }, at); err != nil {
		t.Fatal(err)
	}
}

// work renders an API-billed work session twice (first sight, then a response), so Sample knows it is API-billed.
func work(t *testing.T, h string, now time.Time, refresh func() error) string {
	t.Helper()
	branch := func(dir string) string { return "feat/x@" + dir }
	Work(h, "", workRender("e", 0.1, false), now.Add(-time.Minute), "ENT", branch, refresh)
	return Work(h, "", workRender("e", 1.25, false), now, "ENT", branch, refresh)
}

// The red check of ORDER 15:25: an API-billed work session shows model + account, branch, cost, context % and the
// Enterprise fallback from the cached read, not "no plan limits".
func TestWorkLineShowsPartsAndFallback(t *testing.T) {
	h := home(t)
	now := time.Date(2026, 10, 19, 15, 0, 0, 0, time.UTC)
	writeCache(t, h, readOK, now.Add(-time.Minute))
	calls := 0
	out := work(t, h, now, func() error { calls++; return nil })
	for _, want := range []string{"Opus 5.5 ENT", "feat/x@/w", "$1.25", "ctx 34%", "included 34% ($68 of $200, expires " + day(readOK.Included.Expires) + ")", "spend $12 of $500"} {
		if !strings.Contains(out, want) {
			t.Errorf("line %q lacks %q", out, want)
		}
	}
	if strings.Contains(out, "no plan limits") {
		t.Errorf("line %q still says no plan limits", out)
	}
	if calls != 0 {
		t.Errorf("a fresh cache was refreshed %d times", calls)
	}
}

// A Max work session keeps its Max quota after the same parts.
func TestWorkLineMaxSession(t *testing.T) {
	h := home(t)
	out := Work(h, "", workRender("m", 1, true), time.Date(2026, 10, 19, 15, 0, 0, 0, time.UTC), "MAX", func(string) string { return "main" },
		func() error { t.Error("a Max session refreshed the fallback"); return nil })
	if !strings.HasPrefix(out, "Opus 5.5 MAX │ main │ $1.00 │ ") || !strings.Contains(out, "7d 40%") {
		t.Errorf("line %q", out)
	}
}

// Never blocks, never silent: no cache asks for a refresh and says not read yet; an old one is refreshed and, past
// EntStale, says how old; a failed refresh start is printed.
func TestWorkLineCacheStates(t *testing.T) {
	now := time.Date(2026, 10, 19, 15, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name       string
		age        time.Duration // 0: no cache
		refreshErr error
		want, not  string
		calls      int
	}{
		{name: "none", want: "fallback: not read yet", not: "0%", calls: 1},
		{name: "old", age: EntFresh + time.Second, want: "included 34%", not: "STALE", calls: 1},
		{name: "stale", age: 20 * time.Minute, want: "STALE, read 20 min ago", calls: 1},
		{name: "spawn fails", age: EntFresh + time.Second, refreshErr: syscall.EPERM, want: "refresh FAILED", calls: 1},
		{name: "future", age: -10 * time.Minute, want: "included 34%", calls: 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := home(t)
			if c.age != 0 {
				writeCache(t, h, readOK, now.Add(-c.age))
			}
			calls := 0
			out := work(t, h, now, func() error { calls++; return c.refreshErr })
			if !strings.Contains(out, c.want) || (c.not != "" && strings.Contains(out, c.not)) || calls != c.calls {
				t.Errorf("line %q, %d refreshes; want %q, not %q, %d refreshes", out, calls, c.want, c.not, c.calls)
			}
		})
	}
}

// A refresh that does not land (the cache cannot be written) is started once per EntFresh, not on every render;
// when the attempt cannot be recorded, nothing is started and the line says why (review of the work line, 2).
func TestWorkLineRefreshThrottled(t *testing.T) {
	now := time.Date(2026, 10, 19, 15, 0, 0, 0, time.UTC)
	h := home(t)
	writeCache(t, h, readOK, now.Add(-time.Hour))
	calls := 0
	count := func() error { calls++; return nil }
	work(t, h, now, count)
	work(t, h, now.Add(2*time.Minute), count)
	if calls != 1 {
		t.Errorf("%d refreshes within EntFresh, want 1", calls)
	}
	out := work(t, h, now.Add(EntFresh+time.Second), count)
	if calls != 2 || !strings.Contains(out, "see logs/ent-refresh.log") {
		t.Errorf("after EntFresh: %d refreshes, line %q", calls, out)
	}

	h = home(t)
	if err := os.Mkdir(filepath.Join(h, "spool", "ent-usage.try"), 0o700); err != nil { // cannot be written as a file
		t.Fatal(err)
	}
	calls = 0
	if err := os.WriteFile(filepath.Join(h, "spool", "ent-usage.try", "x"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	out = work(t, h, now, count)
	if calls != 0 || !strings.Contains(out, "refresh FAILED") {
		t.Errorf("unrecordable attempt: %d refreshes, line %q", calls, out)
	}
}

// An expired login is a loud "cannot read" with its reason, never 0%; a broken cache file is said so.
func TestWorkLineCannotRead(t *testing.T) {
	now := time.Date(2026, 10, 19, 15, 0, 0, 0, time.UTC)
	h := home(t)
	writeCache(t, h, EntUsage{Reason: "login expired 21 Oct 09:00; a ~/.claude-work Claude session renews it"}, now)
	out := work(t, h, now, func() error { return nil })
	if !strings.Contains(out, "fallback: cannot read (login expired 21 Oct 09:00") || strings.Contains(out, "%)") {
		t.Errorf("line %q", out)
	}
	if err := os.WriteFile(filepath.Join(h, "spool", "ent-usage.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	out = work(t, h, now, func() error { calls++; return nil })
	if !strings.Contains(out, "fallback: cache unreadable") || calls != 1 {
		t.Errorf("broken cache: line %q, %d refreshes", out, calls)
	}
}

// The refresh writes what the read says, a cannot-read too; a second refresh while one runs does not read again,
// nor one after a fresh write.
func TestRefreshEnt(t *testing.T) {
	h := home(t)
	now := time.Date(2026, 10, 19, 15, 0, 0, 0, time.UTC)
	reads := 0
	read := func(time.Time) EntUsage { reads++; return EntUsage{Reason: "no login"} }
	if err := RefreshEnt(h, read, now); err != nil || reads != 1 {
		t.Fatalf("err %v, %d reads", err, reads)
	}
	b, err := os.ReadFile(filepath.Join(h, "spool", "ent-usage.json"))
	if err != nil || !strings.Contains(string(b), `"no login"`) {
		t.Fatalf("cache %s, %v", b, err)
	}
	if err := RefreshEnt(h, read, now.Add(time.Minute)); err != nil || reads != 1 {
		t.Errorf("fresh cache read again: err %v, %d reads", err, reads)
	}
	lock, err := os.OpenFile(filepath.Join(h, "spool", "ent-usage.lock"), os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	if err := RefreshEnt(h, read, now.Add(time.Hour)); err != nil || reads != 1 {
		t.Errorf("refresh while one runs: err %v, %d reads", err, reads)
	}
}

func TestAccount(t *testing.T) {
	for _, c := range []struct{ file, want string }{
		{`{"oauthAccount":{"billingType":"stripe_subscription_enterprise_self_serve"}}`, "ENT"},
		{`{"oauthAccount":{"billingType":"stripe_subscription"}}`, "MAX"},
		{`{"oauthAccount":null}`, "NO-LOGIN"},
		{`{`, "ACCT?"},
		{"", "ACCT?"},
	} {
		d := t.TempDir()
		if c.file != "" {
			if err := os.WriteFile(filepath.Join(d, ".claude.json"), []byte(c.file), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if got := Account(d); got != c.want {
			t.Errorf("%s: %q, want %q", c.file, got, c.want)
		}
	}
}
