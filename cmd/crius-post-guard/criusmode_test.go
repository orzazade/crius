package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	criusTranscript = "/Users/o/.claude-max2-personal/projects/-Users-orkhanrzazade-Projects-scifi-crius/43e3c11e.jsonl"
	criusSubagent   = "/Users/o/.claude/projects/-Users-orkhanrzazade-Projects-scifi-crius/43e3c11e/subagents/agent-a1.jsonl"
	leadTranscript  = "/Users/o/.claude-work/projects/-Users-orkhanrzazade-Projects-Appxite-svc-quote/9f1.jsonl"
	adoO            = " --org https://dev.azure.com/Appxite"
	thread          = "az devops invoke --area git --resource pullRequestThreads --route-parameters project=P repositoryId=R pullRequestId=7848 --http-method POST --api-version 7.1" + adoO + " --in-file "
)

// AI credit in its real forms, built from parts so no file here carries it.
var (
	coAuthor = "Co-" + "Authored-By: " + "Claude <noreply@" + "anthropic.com>"
	genWith  = "Generated with " + "[Claude" + " Code](x)"
	genPlain = "Generated with " + "Claude"
	robot    = "\U0001F916 done"
	aiLink   = "see https://claude" + ".ai/x"
	comLink  = "see https://claude" + ".com/x"
)

// crius runs the hook on a Bash command with transcript and cwd, and gives the decision ("" when it writes nothing) and
// the reason.
func crius(t *testing.T, transcript, cwd, cmd string) (string, string) {
	t.Helper()
	b, err := json.Marshal(map[string]any{"tool_name": "Bash", "cwd": cwd, "transcript_path": transcript,
		"tool_input": map[string]any{"command": cmd}})
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := run(strings.NewReader(string(b)), &out); err != nil {
		t.Fatal(err)
	}
	if out.Len() == 0 {
		return "", ""
	}
	var v struct {
		HookSpecificOutput struct{ PermissionDecision, PermissionDecisionReason string }
	}
	if err := json.Unmarshal([]byte(out.String()), &v); err != nil {
		t.Fatalf("%v: %s", err, out.String())
	}
	return v.HookSpecificOutput.PermissionDecision, v.HookSpecificOutput.PermissionDecisionReason
}

// criusDir is a folder for the call with a guard log of its own and PRs read from targets instead of ADO.
func criusDir(t *testing.T, targets map[string]string) string {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CRIUS_HOME", home)
	t.Setenv("HOME", t.TempDir())
	old := prState
	prState = func(id string) (prInfo, error) {
		if tg, ok := targets[id]; ok {
			return prInfo{Head: head7848, Status: "active", Target: tg}, nil
		}
		return prInfo{}, errors.New("az: PR not found")
	}
	t.Cleanup(func() { prState = old })
	return t.TempDir()
}

func TestCriusModeIsTheCriusProjectFolder(t *testing.T) {
	for p, want := range map[string]bool{
		criusTranscript: true,
		criusSubagent:   true,
		leadTranscript:  false,
		"/Users/o/.claude/projects/-Users-orkhanrzazade-Projects-scifi-crius-build/1.jsonl": false,
		"/Users/o/.claude/projects/-Users-orkhanrzazade-Projects-scifi-crius":               false,
		"/Users/o/.claude/projects/x/../-Users-orkhanrzazade-Projects-scifi-crius/1.jsonl":  true,
		"/tmp/-Users-orkhanrzazade-Projects-scifi-crius/1.jsonl":                            false,
		"projects/-Users-orkhanrzazade-Projects-scifi-crius/1.jsonl":                        false,
		"": false,
		"/Users/o/.claude/projects/-Users-orkhanrzazade-Projects-Appxite-svc-quote/crius/1.jsonl":      false,
		"/Users/o/.claude/projects/-Users-orkhanrzazade-Projects-scifi-crius/43e3c11e/tool-results/x":  true,
		"/Users/o/.claude/projects/-Users-orkhanrzazade-Projects-scifi-crius-build/-Users-orkhanrzaza": false,
	} {
		if got := criusSession(p); got != want {
			t.Errorf("criusSession(%q) = %v, want %v", p, got, want)
		}
	}
}

// A readable post that passes the hard checks goes on in Crius mode; the same call in a lead session still asks.
func TestCriusModeAllowsAReadablePostAndALeadStillAsks(t *testing.T) {
	dir := criusDir(t, map[string]string{"7848": "refs/heads/integration/96365-b1"})
	write(t, filepath.Join(dir, "c.json"), `{"comments":[{"content":"Migration regenerated; tests pass on https://quote-p3-test.appxite.com"}]}`)
	write(t, filepath.Join(dir, "d.md"), "Migration regenerated after the rebase.\n")
	for _, cmd := range []string{
		thread + "c.json",
		thread + dir + "/c.json",
		"az repos pr create --title 'Quotes: regenerated migration' --description @d.md --source-branch feature/96365-x --target-branch integration/96365-b1" + adoO,
		"az repos pr update --id 7848 --status completed" + adoO,
		"az repos pr set-vote --id 7848 --vote approve" + adoO + " -o none",
		"cd /tmp && az repos pr update --id 7848 --title 'Generation two'" + adoO,
	} {
		if d, why := crius(t, criusTranscript, dir, cmd); d != "" {
			t.Errorf("Crius mode: want allow, got %s %q\n  %s", d, why, cmd)
		}
		if d, _ := crius(t, criusSubagent, dir, cmd); d != "" {
			t.Errorf("Crius subagent: want allow, got %s\n  %s", d, cmd)
		}
		// a lead passes the PR create from feature/96365-* into bN on the allow list (item 3); the rest keep the click
		if d, _ := crius(t, leadTranscript, dir, cmd); (d == "ask") == strings.HasPrefix(cmd, "az repos pr create") {
			t.Errorf("lead session: got %q\n  %s", d, cmd)
		}
	}
}

// Each hard check denies with its reason; nothing in Crius mode asks.
func TestCriusModeDeniesEachHardCheck(t *testing.T) {
	dir := criusDir(t, map[string]string{"7848": "refs/heads/develop", "7849": "refs/heads/main", "7850": "refs/heads/integration/96365-b1"})
	for n, body := range map[string]string{"co.json": coAuthor, "gen.json": genWith, "plain.json": genPlain, "robot.json": robot,
		"ai.json": aiLink, "com.json": comLink, "prod.json": "see https://quote-api-prod.appxite.com/health",
		"done.json": `{"status": "completed"}`, "auto.json": `{"autoCompleteSetBy": {"id": "x"}}`} {
		write(t, filepath.Join(dir, n), body)
	}
	for cmd, want := range map[string]string{
		thread + "co.json":    "AI credit",
		thread + "gen.json":   "AI credit",
		thread + "plain.json": "AI credit",
		thread + "robot.json": "AI credit",
		thread + "ai.json":    "AI credit",
		thread + "com.json":   "AI credit",
		"az repos pr create --title t --description '" + genWith + "' --source-branch feature/96365-x --target-branch integration/96365-b1" + adoO: "AI credit",
		"az repos pr update --id 7848 --status completed" + adoO:                                                                                   "merges into refs/heads/develop",
		"az repos pr update --id 7849 --status=completed" + adoO:                                                                                   "merges into refs/heads/main",
		"az repos pr update --id 7851 --status completed" + adoO:                                                                                   "cannot read the target of PR 7851",
		"az repos pr update --id 7850 --title t" + adoO + " && az repos pr update --id 7848 --status completed" + adoO:                             "merges into refs/heads/develop",
		strings.Replace(thread, "--http-method POST", "--http-method PATCH", 1) + "done.json":                                                      "completes a PR",
		thread + "auto.json": "completes a PR or sets auto-complete",
		"az repos pr create --title t --source-branch feature/96365-x --target-branch integration/96365-b1 --auto-complete true" + adoO: "--auto-complete",
		"az repos pr update --id 7850 --auto-compl true" + adoO:                                                                         "--auto-complete",
		thread + "prod.json": "production URL",
		"curl -X POST -H 'Content-Type: application/json' -d @prod.json https://dev.azure.com/Appxite/P/_apis/git/repositories/R/pullRequests/7848/threads?api-version=7.1": "production URL",
	} {
		d, why := crius(t, criusTranscript, dir, cmd)
		if d != "deny" || !strings.Contains(why, want) {
			t.Errorf("want deny naming %q, got %s %q\n  %s", want, d, why, cmd)
		}
		if strings.Contains(why, "write it so the guard can read it") != strings.Contains(want, "cannot read") {
			t.Errorf("rewrite hint only on what the guard cannot read: %q", why)
		}
	}
	// the allow side of each check: no credit (a bare "regenerated"), another target, no auto-complete, a test URL
	write(t, filepath.Join(dir, "ok.json"), `{"comments":[{"content":"Generation 2: migration regenerated, see https://quote-p3-test.appxite.com"}]}`)
	for _, cmd := range []string{thread + "ok.json", "az repos pr update --id 7850 --status completed" + adoO,
		"az repos pr create --title t --source-branch feature/96365-x --target-branch integration/96365-b1" + adoO} {
		if d, why := crius(t, criusTranscript, dir, cmd); d != "" {
			t.Errorf("want allow, got %s %q\n  %s", d, why, cmd)
		}
	}
}

// What the guard cannot read denies and says to rewrite it; it never asks.
func TestCriusModeDeniesWhatTheGuardCannotRead(t *testing.T) {
	dir := criusDir(t, nil)
	for _, cmd := range []string{
		"az repos pr create --title t --description \"$(git log -1 --format=%B)\" --source-branch feature/96365-x --target-branch integration/96365-b1" + adoO,
		"$AZ repos pr create --title t" + adoO,
		"az repos pr $VERB --id 7848" + adoO,
		thread + "missing.json",
		thread + "/dev/stdin",
		"cd sub && " + thread + "c.json",
		"python3 -c \"import subprocess; subprocess.run(['az','repos','pr','create','--title','t'])\"",
		"echo '{}' | az devops invoke --in-file - --http-method POST --area git --resource pullRequestThreads" + adoO,
	} {
		d, why := crius(t, criusTranscript, dir, cmd)
		if d != "deny" || !strings.Contains(why, "write it so the guard can read it") {
			t.Errorf("want deny with the rewrite hint, got %s %q\n  %s", d, why, cmd)
		}
		if d, _ := crius(t, leadTranscript, dir, cmd); d != "ask" {
			t.Errorf("lead session: want ask, got %q\n  %s", d, cmd)
		}
	}
}

// A post inside a script the command runs gets the same hard checks: the reading goes on into the script.
func TestCriusModeChecksPostsInsideScripts(t *testing.T) {
	dir := criusDir(t, map[string]string{"7848": "refs/heads/develop", "7850": "refs/heads/integration/96365-b1"})
	write(t, filepath.Join(dir, "ok.sh"), "#!/bin/bash\naz repos pr update --id 7850 --status completed --org https://dev.azure.com/Appxite\n")
	write(t, filepath.Join(dir, "bad.sh"), "#!/bin/bash\naz repos pr update --id 7850 --title t --org https://dev.azure.com/Appxite\naz repos pr update --id 7848 --status completed --org https://dev.azure.com/Appxite\n")
	if d, why := crius(t, criusTranscript, dir, "bash ok.sh"); d != "" {
		t.Errorf("ok.sh: want allow, got %s %q", d, why)
	}
	if d, why := crius(t, criusTranscript, dir, "bash bad.sh"); d != "deny" || !strings.Contains(why, "merges into refs/heads/develop") {
		t.Errorf("bad.sh: want deny naming develop, got %s %q", d, why)
	}
}
