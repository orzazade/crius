package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testdata/provider-demo/threads.json: failedQuery's columns as `sqlite3 -json` prints them, for four real failed or
// interrupted runs in T3 Code's store (paths under the owner's home shortened): AXI · ms-flow 11-13 09:46 (403
// Cloudflare), its 11-14 00:27 sign-in failure, Cortex 11-15 19:41 (usage limit), AXI · api-split 11-15 06:24
// (interrupted, no error item).
func TestParseFailedThreadsReadsT3sStore(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "provider-demo", "threads.json"))
	if err != nil {
		t.Fatal(err)
	}
	ths, err := parseFailedThreads(b)
	if err != nil || len(ths) != 4 {
		t.Fatalf("%d threads, err %v", len(ths), err)
	}
	got := map[string]string{}
	for _, th := range ths {
		got[th.Title+" "+th.Ended.UTC().Format("01-02 15:04")] = th.Status + " | " + th.Class + " | " + th.Message
	}
	for k, want := range map[string]string{
		"AXI · ms-flow 11-13 09:46":                                   "failed | provider_error | Failed to authenticate. API Error: 403 Attention Required! | Cloudflare",
		"Cortex: remove old VM path + check sync via MCP 11-15 19:41": "failed | usage_limit | Claude API rate limit reached. Try again later.",
		"AXI · api-split 11-15 06:24":                                 "interrupted |  | ",
	} {
		if got[k] != want {
			t.Errorf("%s: %q, want %q", k, got[k], want)
		}
	}
	if th, _ := parseFailedThreads(nil); th != nil {
		t.Errorf("no rows: %v", th)
	}
}

// The morning demo: a fixture run (its clock from now.txt: 11-13 09:51, 5 min after the 403) prints the restarts it
// would send, and only for the provider errors.
func TestProviderCmdFixtureRun(t *testing.T) {
	var out strings.Builder
	code := providerCmd([]string{"-fixtures", filepath.Join("testdata", "provider-demo")}, &out)
	s := out.String()
	if code != 0 || !strings.Contains(s, "4 failed or interrupted T3 thread(s) read, 2 remote tab(s) read, 2 stopped by a provider error") ||
		!strings.Contains(s, `would restart T3 thread "AXI · ms-flow" (AXI, mcp:launch:5a5f14c172d168c6f392c40cd7b2b5f3a67407a80288f350f5efdc62ff3899d7) after a provider error (Failed to authenticate. API Error: 403 Attention Required! | Cloudflare)`) ||
		!strings.Contains(s, "would restart tab legion quotes:dev after a provider error (Please run /login · API Error: 403 Attention Required! | Cloudflare)") ||
		strings.Count(s, "would restart") != 2 {
		t.Fatalf("exit %d:\n%s", code, s)
	}
}

// The remote-ask demo (testdata/ask-demo, the 11-19 18:49 shape): the state a 5-min run left at 18:55 holds the
// builder's ask, first seen at 18:50; at 19:00 the ask wakes Crius, the ⏳ tab says nothing, and the tab whose box cannot
// be reached (its second miss in a row) is a loud "could not look" and exit 1, never a quiet pass.
func TestProviderCmdAskDemo(t *testing.T) {
	var out strings.Builder
	code := providerCmd([]string{"-fixtures", filepath.Join("testdata", "ask-demo")}, &out)
	s := out.String()
	if code != 1 || !strings.Contains(s, "2 remote tab(s) read, 0 stopped by a provider error, 0 on a prompt, 1 asking") ||
		!strings.Contains(s, `ALERT: question unanswered for 10 min in tab thinkpad staffy:builder: first seen at `) ||
		!strings.Contains(s, `its last reply ends "❓ Need: where the test database copy is, or a go to make one."; no NEED, NEED-OWNER or ASK line from staffy:builder on the bus since `) ||
		!strings.Contains(s, "tab thinkpad staffy:qa: no prompt, no provider stop, no open ask") ||
		!strings.Contains(s, "ALERT: the provider-error sweep could not look at tab legion quotes:qa, so a session stopped there, waiting on a prompt or asking a question is not seen") ||
		strings.Count(s, "ALERT:") != 2 {
		t.Fatalf("exit %d:\n%s", code, s)
	}
}

func TestProviderCmdFixtureRunSaysWhatItCouldNotRead(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "provider-tabs.txt"), []byte("legion quotes:dev\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if code := providerCmd([]string{"-fixtures", dir}, &out); code != 1 || !strings.Contains(out.String(), "could not look at the T3 Code threads") ||
		!strings.Contains(out.String(), "could not look at tab legion quotes:dev") {
		t.Fatalf("exit %d:\n%s", code, out.String())
	}
}

// Review P2-8: one row it cannot read is named, and the others are still read.
func TestParseFailedThreadsKeepsGoodRows(t *testing.T) {
	ths, err := parseFailedThreads([]byte(`[{"id":"x","ended":"yesterday"},{"id":"y","status":"failed","ended":"2026-11-13T09:46:58.871Z"}]`))
	if err == nil || !strings.Contains(err.Error(), "T3 thread x") || len(ths) != 1 || ths[0].ID != "y" {
		t.Fatalf("%v %v", ths, err)
	}
}
