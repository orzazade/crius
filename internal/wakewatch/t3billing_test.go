package wakewatch

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/testutil"
)

// billingReply is the reply T3 showed on 11-25 02:0x in each AXI thread on max3 (payment past due).
const billingReply = "Claude Code failed: You can't use your Claude subscription right now because your payment is past due. " +
	"Update your payment method at claude.ai/settings/billing to continue."

// onConfig gives a role file and session file to each role, the role on the Claude config folder dirs[role].
func onConfig(t *testing.T, home string, dirs map[string]string) {
	t.Helper()
	for r, dir := range dirs {
		testutil.Write(t, filepath.Join(home, "roles", "quotes."+r+".json"),
			`{"session_id":"s-`+r+`","project":"quotes","role":"`+r+`","time":"2026-10-21T21:00:00Z","config":"`+dir+`"}`)
		testutil.Write(t, filepath.Join(home, "sessions", "s-"+r+".json"), `{"session_id":"s-`+r+`","t3_thread":"th-`+r+`"}`)
	}
}

func accountDown(sent []string) []string {
	var out []string
	for _, s := range sent {
		if strings.Contains(s, "ACCOUNT-DOWN") {
			out = append(out, s)
		}
	}
	return out
}

// FU-WAKEWATCH-BILLING (11-25 02:0x): max3's payment was past due; 4 AXI threads on it failed with the same billing reply
// and wakewatch sent 4 idle alerts, one per thread. A billing failure is the account's, not the thread's: one ACCOUNT-DOWN
// line per account (config folder) naming its threads, no idle alert for them, said once. Another account with the same
// failure is its own line; a thread idle for another reason still wakes Crius as before.
func TestABillingFailureIsOneAccountDownLinePerAccount(t *testing.T) {
	home := threadHome(t, "lead", "ok")
	m3, m1 := "/Users/x/.claude-work-max3", "/Users/x/.claude-work-max"
	onConfig(t, home, map[string]string{"a1": m3, "a2": m3, "a3": m3, "a4": m3, "b1": m1})
	threads := map[string]T3Thread{
		"th-ok":   {Status: "completed", Updated: t0.Add(-20 * time.Minute), LastReply: longReply},
		"th-lead": {Status: "completed", Updated: t0.Add(-time.Hour), LastReply: "done ✅"},
	}
	for _, r := range []string{"a1", "a2", "a3", "a4", "b1"} {
		threads["th-"+r] = T3Thread{Status: "error", Updated: t0.Add(-20 * time.Minute), LastReply: billingReply}
	}
	f := &fake{threads: threads}
	runOnce(t, home, f, t0)
	down := accountDown(f.sent)
	if len(down) != 2 {
		t.Fatalf("want 2 ACCOUNT-DOWN lines (max3, max), got %d: %q\nall: %q", len(down), down, f.sent)
	}
	var max3 string
	for _, d := range down {
		if strings.Contains(d, m3) {
			max3 = d
		}
	}
	for _, want := range []string{"quotes · a1", "quotes · a2", "quotes · a3", "quotes · a4", "payment is past due"} {
		if !strings.Contains(max3, want) {
			t.Errorf("max3 line %q lacks %q", max3, want)
		}
	}
	for _, r := range []string{"a1", "a2", "a3", "a4", "b1"} {
		if w := t3Wakes(f, "quotes · "+r+" (T3 thread th-"+r+") is"); len(w) != 0 {
			t.Errorf("an idle alert for %s, whose account is down: %q", r, w)
		}
	}
	if w := t3Wakes(f, "quotes · ok (T3 thread"); len(w) != 1 {
		t.Errorf("positive control: the plain idle thread did not wake Crius: %q", f.sent)
	}
	f.sent = nil
	runOnce(t, home, f, t0.Add(5*time.Minute))
	if d := accountDown(f.sent); len(d) != 0 {
		t.Fatalf("said again 5 min later: %q", d)
	}
}
