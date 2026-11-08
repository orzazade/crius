package wakewatch

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orzazade/crius/internal/testutil"
)

// Owner 11-08, the hardest rule: every model run uses one of the three Max subscriptions, never an API key, usage
// credits or extra paid usage. Each Claude Code process is S1 when its environment or its settings carry a credential,
// a cloud provider or an apiKeyHelper, when the sign-in of its config folder is not on a Max plan or has none, or when
// that sign-in has extra usage (usage credits) on. The plan is the sign-in's own (oauthAccount in the folder's
// .claude.json, as Claude Code 2.1.319 writes it), never the folder's name: the Max sign-in below lives in a folder
// named like the enterprise one, and the enterprise one in a folder named max.
func TestAClaudeOnPaidUsageIsS1(t *testing.T) {
	home := setup(t, nil)
	user := t.TempDir()
	managedSettings = filepath.Join(user, "managed-settings.json")
	profile := func(dir, org string, extra bool) {
		x := "false"
		if extra {
			x = "true"
		}
		testutil.Write(t, filepath.Join(dir, ".claude.json"), `{"numStartups":3,"oauthAccount":{"accountUuid":"u-1",`+
			`"organizationType":"`+org+`","billingType":"stripe_subscription","hasExtraUsageEnabled":`+x+`}}`)
	}
	profile(user, "claude_max", false) // ~/.claude.json: the folder of a claude with no CLAUDE_CONFIG_DIR
	maxDir, entDir, creditsDir, newDir, setDir := filepath.Join(user, ".claude-work"), filepath.Join(user, ".claude-max9"),
		filepath.Join(user, ".claude-max2"), filepath.Join(user, ".claude-new"), filepath.Join(user, ".claude-max5")
	profile(maxDir, "claude_max", false)
	testutil.Write(t, filepath.Join(maxDir, "settings.json"), `{"env":{"CLAUDE_CODE_USE_VERTEX":"0","DISABLE_TELEMETRY":"1"}}`)
	profile(entDir, "claude_enterprise", true)
	profile(creditsDir, "claude_max", true)
	testutil.Write(t, filepath.Join(newDir, ".claude.json"), `{"numStartups":1}`) // never signed in
	profile(setDir, "claude_max", false)
	testutil.Write(t, filepath.Join(setDir, "settings.json"), `{"env":{"ANTHROPIC_API_KEY":"sk-set"}}`)
	env := func(kv ...string) map[string]string {
		m := map[string]string{"HOME": user, "PATH": "/usr/bin"}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	f := &fake{
		ses: []Session{{Name: "quotes · backend-core · max1", ID: "0082c3a7", PID: 201}},
		claudes: []Claude{
			{PID: 200, Env: env()},
			{PID: 201, Env: env("CLAUDE_CONFIG_DIR", maxDir, "CLAUDE_CODE_USE_BEDROCK", "false")},
			{PID: 202, Env: env("CLAUDE_CONFIG_DIR", maxDir, "ANTHROPIC_API_KEY", "sk-test")},
			{PID: 203, Env: env("CLAUDE_CONFIG_DIR", entDir)},
			{PID: 213, Env: env("CLAUDE_CONFIG_DIR", entDir)},
			{PID: 204, Env: env("CLAUDE_CONFIG_DIR", creditsDir)},
			{PID: 205, Env: env("CLAUDE_CONFIG_DIR", newDir)},
			{PID: 206, Env: env("ANTHROPIC_AUTH_TOKEN", "tok")},
			{PID: 207, Env: env("CLAUDE_CODE_USE_BEDROCK", "1")},
			{PID: 208, Env: env("CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat01-x")},
			{PID: 209, Env: env("CLAUDE_CONFIG_DIR", setDir)},
			{PID: 210, Args: []string{"claude", "--settings", `{"apiKeyHelper":"/usr/local/bin/key"}`}, Env: env("CLAUDE_CONFIG_DIR", maxDir)},
			{PID: 211, Err: errors.New("sysctl kern.procargs2: operation not permitted")},
		}}
	if err := Run(f.deps(home, t0)); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(f.sent, "\n")
	for _, want := range []string{
		"S1 paid usage: claude pid 202 runs with ANTHROPIC_API_KEY set",
		"S1 paid usage: claude pid 203, claude pid 213 on " + entDir + ", signed in on a claude_enterprise plan, not Max",
		"S1 paid usage: claude pid 204 on " + creditsDir + ", whose sign-in has extra usage (usage credits) on",
		"S1 paid usage: claude pid 205 on " + newDir + ", which has no sign-in",
		"S1 paid usage: claude pid 206 runs with ANTHROPIC_AUTH_TOKEN set",
		"S1 paid usage: claude pid 207 runs with CLAUDE_CODE_USE_BEDROCK set",
		"S1 paid usage: claude pid 208 runs with CLAUDE_CODE_OAUTH_TOKEN set",
		"S1 paid usage: claude pid 209 runs with ANTHROPIC_API_KEY set in " + filepath.Join(setDir, "settings.json"),
		"S1 paid usage: claude pid 210 runs with apiKeyHelper set in its inline --settings;",
		"the paid-usage check failed to look: claude pid 211: sysctl kern.procargs2: operation not permitted",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("no %q in %q", want, all)
		}
	}
	if n := strings.Count(all, "on "+entDir+", signed in"); n != 1 {
		t.Errorf("one folder, %d plan alerts: %q", n, all)
	}
	for _, clean := range []string{"pid 200 ", "pid 201 ", "pid 201,", "pid 201)"} {
		if strings.Contains(all, clean) {
			t.Errorf("a claude on a Max sign-in with no key alerted (%s): %q", clean, all)
		}
	}
	// a session's name is said with its pid, so Crius knows which tab to stop or move
	f = &fake{ses: f.ses, claudes: []Claude{{PID: 201, Env: env("CLAUDE_CONFIG_DIR", entDir)}}}
	if err := Run(f.deps(setup(t, nil), t0)); err != nil {
		t.Fatal(err)
	}
	if all := strings.Join(f.sent, "\n"); !strings.Contains(all, "claude pid 201 (quotes · backend-core · max1) on "+entDir) {
		t.Errorf("no session name: %q", all)
	}
	// failed to look is not found nothing
	f = &fake{claudeErr: errors.New("ps -axww -o pid=,ppid=,command=: signal: killed")}
	if err := Run(f.deps(setup(t, nil), t0)); err != nil {
		t.Fatal(err)
	}
	if all := strings.Join(f.sent, "\n"); !strings.Contains(all, "the paid-usage check failed to look: ps -axww") {
		t.Errorf("a failed look is silent: %q", all)
	}
}
