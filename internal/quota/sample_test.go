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
	must(t, h, render("e", 0, 0, false), t0.Add(90*time.Minute)) // a known enterprise session heartbeats too
	must(t, h, render("e", 5, 6, true), t0.Add(91*time.Minute))
	if got, want := kinds(spoolLines(t, h)), "e:session:false e:heartbeat:false e:session:true"; got != want {
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

// Crius 18:48: rate_limits that measure nothing for the 7-day window (empty, or without its share or reset) fail
// loudly and write nothing, instead of passing as a Max sample.
func TestSampleRateLimitsWithoutSevenDayFail(t *testing.T) {
	h := home(t)
	for _, rl := range []string{`{}`, `{"five_hour":{"used_percentage":5,"resets_at":1760630400}}`,
		`{"seven_day":{"resets_at":1761206400}}`, `{"seven_day":{"used_percentage":6}}`} {
		in := []byte(`{"session_id":"x","cost":{"total_cost_usd":0.5,"total_api_duration_ms":900},"rate_limits":` + rl + `}`)
		if out, err := Sample(h, "personal", in, time.Now()); err == nil {
			t.Fatalf("rate_limits %s: want an error, got %q", rl, out)
		}
	}
	if ls := spoolLines(t, h); len(ls) != 0 {
		t.Fatalf("wrote %v", kinds(ls))
	}
}

// Each persona heartbeats on its own: personal lines must not keep an idle-valued work session from writing one, or
// the work persona reads as stale while it is sampled.
func TestSampleHeartbeatIsPerPersona(t *testing.T) {
	h := home(t)
	t0 := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	for _, r := range []struct {
		persona, sid string
		at           time.Duration
	}{{"personal", "a", 0}, {"work", "w", time.Minute}, {"personal", "a", 31 * time.Minute},
		{"work", "w", 32 * time.Minute}} {
		if _, err := Sample(h, r.persona, render(r.sid, 10, 40, true), t0.Add(r.at)); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := kinds(spoolLines(t, h)), "a:session:true w:session:true a:heartbeat:true w:heartbeat:true"; got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

// An idle session repeating a past epoch's window does not heartbeat: its line measures nothing now, and it would
// reset the heartbeat clock for an active session whose values did not change.
func TestSampleOldEpochDoesNotHeartbeat(t *testing.T) {
	h := home(t)
	t0 := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	old := []byte(`{"session_id":"idle","cost":{"total_cost_usd":0.5,"total_api_duration_ms":900},"rate_limits":` +
		`{"seven_day":{"used_percentage":90,"resets_at":1760000000}}}`)
	for _, r := range []struct {
		in []byte
		at time.Duration
	}{{render("a", 10, 40, true), 0}, {old, time.Minute}, {old, 31 * time.Minute}, {render("a", 10, 40, true), 32 * time.Minute}} {
		if _, err := Sample(h, "personal", r.in, t0.Add(r.at)); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := kinds(spoolLines(t, h)), "a:session:true idle:session:true a:heartbeat:true"; got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

// Crius 22:12: an enterprise-only persona (no rate_limits) heartbeats while a known enterprise session renders, so
// it is never stale (no line within Gap) while sessions are open. A session whose account is not known yet (resumed,
// no response) writes no enterprise line; the enterprise lines do not hold back a Max session's current-epoch
// heartbeat in the same persona.
func TestSampleEnterprisePersonaStaysAlive(t *testing.T) {
	h := home(t)
	t0 := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	work := func(in []byte, at time.Duration) {
		t.Helper()
		if _, err := Sample(h, "work", in, t0.Add(at)); err != nil {
			t.Fatal(err)
		}
	}
	work([]byte(`{"session_id":"e","cost":{"total_cost_usd":0,"total_api_duration_ms":0}}`), 0)
	work(render("e", 0, 0, false), time.Minute) // its first response, without rate_limits: enterprise
	for at := 5 * time.Minute; at <= 2*time.Hour; at += 5 * time.Minute {
		work(render("e", 0, 0, false), at)
		work(render("r", 0, 0, false), at) // resumed, no response yet: account unknown
	}
	prev := t0
	for _, l := range spoolLines(t, h) {
		ts, _ := time.Parse(tsFormat, l.TS)
		if ts.Sub(prev) > Gap || l.Session != "e" || l.RL {
			t.Fatalf("line %+v after %s: %s", l, prev.Format(tsFormat), kinds(spoolLines(t, h)))
		}
		prev = ts
	}
	if t0.Add(2*time.Hour).Sub(prev) > Gap || len(spoolLines(t, h)) > 5 { // a session line and a heartbeat per 30 min
		t.Fatalf("last line %s: %s", prev, kinds(spoolLines(t, h)))
	}

	h = home(t)
	for _, r := range []struct {
		in []byte
		at time.Duration
	}{{render("m", 10, 40, true), 0}, {[]byte(`{"session_id":"e","cost":{"total_cost_usd":0,"total_api_duration_ms":0}}`), 0},
		{render("e", 0, 0, false), time.Minute}, {render("e", 0, 0, false), 31 * time.Minute},
		{render("m", 10, 40, true), 31 * time.Minute},
		// m resumed later: same cost, no rate_limits before its next response; it is not an enterprise session.
		{[]byte(`{"session_id":"m","cost":{"total_cost_usd":0.5,"total_api_duration_ms":900}}`), 100 * time.Minute}} {
		work(r.in, r.at)
	}
	if got, want := kinds(spoolLines(t, h)), "m:session:true e:session:false e:heartbeat:false m:heartbeat:true"; got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}
