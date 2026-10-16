package quota

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func render(sid string, five, seven float64, rl bool) []byte {
	d := map[string]any{"session_id": sid, "cost": map[string]any{"total_cost_usd": 0.5, "total_api_duration_ms": 900}}
	if rl {
		d["rate_limits"] = map[string]any{
			"five_hour": map[string]any{"used_percentage": five, "resets_at": 1760630400},
			"seven_day": map[string]any{"used_percentage": seven, "resets_at": 1761206400}}
	}
	b, _ := json.Marshal(d)
	return b
}

func spoolLines(t *testing.T, home string) []Line {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, "spool", "quota.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []Line
	for _, s := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var l Line
		if err := json.Unmarshal([]byte(s), &l); err != nil {
			t.Fatal(err)
		}
		out = append(out, l)
	}
	return out
}

func kinds(ls []Line) string {
	var ks []string
	for _, l := range ls {
		ks = append(ks, fmt.Sprintf("%s:%s:%v", l.Session, l.Kind, l.RL))
	}
	return strings.Join(ks, " ")
}

func home(t *testing.T) string {
	h := t.TempDir()
	if err := os.Mkdir(filepath.Join(h, "spool"), 0o700); err != nil {
		t.Fatal(err)
	}
	return h
}

func must(t *testing.T, h string, in []byte, now time.Time) string {
	t.Helper()
	out, err := Sample(h, "personal", in, now)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A line per new session, per change of that session's values, and a heartbeat when no line came for 30 min; an
// idle session repeating older values writes nothing.
func TestSampleWritesChangesAndHeartbeatsOnly(t *testing.T) {
	h := home(t)
	t0 := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	must(t, h, render("a", 10, 40, true), t0)
	must(t, h, render("a", 10, 40, true), t0.Add(time.Minute))
	must(t, h, render("a", 12, 41, true), t0.Add(2*time.Minute))
	must(t, h, render("b", 12, 41, true), t0.Add(3*time.Minute))
	must(t, h, render("a", 12, 41, true), t0.Add(20*time.Minute)) // idle re-run: nothing new
	must(t, h, render("b", 12, 41, true), t0.Add(32*time.Minute)) // 29 min since the last line
	must(t, h, render("b", 12, 41, true), t0.Add(34*time.Minute)) // 31 min: heartbeat
	must(t, h, render("a", 12, 41, true), t0.Add(40*time.Minute)) // 6 min after the heartbeat: nothing
	got := kinds(spoolLines(t, h))
	want := "a:session:true a:change:true b:session:true b:heartbeat:true"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

// rate_limits come with the first response: before it nothing is written; after it, their absence is recorded
// once (rl=false, the enterprise sign), and their arrival later is recorded again.
func TestSampleRecordsRateLimitsPresence(t *testing.T) {
	h := home(t)
	t0 := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	if out := must(t, h, []byte(`{"session_id":"e","cost":{"total_cost_usd":0,"total_api_duration_ms":0}}`), t0); !strings.Contains(out, "no response yet") {
		t.Fatal(out)
	}
	out := must(t, h, render("e", 0, 0, false), t0.Add(time.Minute))
	must(t, h, render("e", 0, 0, false), t0.Add(2*time.Minute))
	must(t, h, render("e", 0, 0, false), t0.Add(90*time.Minute)) // no heartbeat without rate_limits
	must(t, h, render("e", 5, 6, true), t0.Add(91*time.Minute))
	if got, want := kinds(spoolLines(t, h)), "e:session:false e:session:true"; got != want {
		t.Fatalf("got %s want %s", got, want)
	}
	if !strings.Contains(out, "no plan limits") {
		t.Fatal(out)
	}
	if out := must(t, h, render("e", 5, 6, true), t0.Add(92*time.Minute)); !strings.Contains(out, "5h 5% · 7d 6%") {
		t.Fatal(out)
	}
}

func TestSampleFailsLoudly(t *testing.T) {
	h := home(t)
	if _, err := Sample(h, "personal", []byte(`{}`), time.Now()); err == nil {
		t.Fatal("no session_id must fail")
	}
	if err := os.WriteFile(filepath.Join(h, "spool", "quota.state.json"), []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Sample(h, "personal", render("a", 1, 1, true), time.Now()); err == nil {
		t.Fatal("a corrupt state file must fail")
	}
	if _, err := Sample(filepath.Join(h, "missing"), "personal", render("a", 1, 1, true), time.Now()); err == nil {
		t.Fatal("a missing spool folder must fail")
	}
}

// Codex round 2: a resumed session shows its old cost before its first new response, and a render can come before
// rate_limits do. Neither is an enterprise line; a response that arrives without rate_limits is.
func TestSampleWritesNoRateLimitsOnlyAfterAResponse(t *testing.T) {
	h := home(t)
	t0 := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	must(t, h, render("r", 0, 0, false), t0)                   // resumed: cost 0.5 already, no response yet
	must(t, h, render("r", 0, 0, false), t0.Add(time.Minute))  // re-render, same cost
	must(t, h, render("r", 5, 6, true), t0.Add(2*time.Minute)) // first response: Max
	must(t, h, render("m", 5, 6, true), t0.Add(3*time.Minute)) // a Max session...
	more := []byte(`{"session_id":"m","cost":{"total_cost_usd":0.9,"total_api_duration_ms":1900}}`)
	must(t, h, more, t0.Add(4*time.Minute)) // ...whose next response came without rate_limits: now API-billed
	if got, want := kinds(spoolLines(t, h)), "r:session:true m:session:true m:session:false"; got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}
