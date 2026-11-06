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
	criusSID        = "43e3c11e-7d5e-486e-8356-990c414d0be6" // the main session and its subagents share it
	leadSID         = "9f1d0c2a-0000-4000-8000-000000000001"
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

// crius runs the hook on a Bash command in session sid (with that session's transcript) and cwd, and gives the decision
// ("" when it writes nothing) and the reason.
func crius(t *testing.T, sid, cwd, cmd string) (string, string) {
	t.Helper()
	transcript := leadTranscript
	if sid == criusSID {
		transcript = criusTranscript
	}
	return hook(t, sid, transcript, cwd, cmd)
}

func hook(t *testing.T, sid, transcript, cwd, cmd string) (string, string) {
	t.Helper()
	b, err := json.Marshal(map[string]any{"tool_name": "Bash", "cwd": cwd, "session_id": sid, "transcript_path": transcript,
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

// criusDir is a folder for the call with a guard log of its own, criusSID as the Crius session and PRs read from targets
// instead of ADO.
func criusDir(t *testing.T, targets map[string]string) string {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "state", "crius"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "state", "crius", "session-id"), []byte(criusSID+"\n"), 0o644); err != nil {
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

// Crius mode is the session whose id Crius writes to $CRIUS_HOME/state/crius/session-id (review of 2a882aa, P2-7: a
// folder name is not a session); its subagents share the id. No file, another id or none means the lead rules.
func TestCriusModeIsTheCriusSession(t *testing.T) {
	const unreadable = "$AZ repos pr create --title t" + adoO
	dir := criusDir(t, nil)
	for _, c := range []struct {
		sid, transcript, want string
	}{
		{criusSID, criusTranscript, "deny"},
		{criusSID, criusSubagent, "deny"},
		{criusSID, leadTranscript, "deny"},
		{leadSID, criusTranscript, "ask"},
		{"", criusTranscript, "ask"},
		{criusSID[:8], criusTranscript, "ask"},
	} {
		if d, why := hook(t, c.sid, c.transcript, dir, unreadable); d != c.want {
			t.Errorf("session %q, transcript %s: want %s, got %s %q", c.sid, c.transcript, c.want, d, why)
		}
	}
	home := os.Getenv("CRIUS_HOME")
	idFile := filepath.Join(home, "state", "crius", "session-id")
	for name, body := range map[string]string{"another id": leadSID + "\n", "empty": "", "two lines": criusSID + "\n" + leadSID + "\n"} {
		write(t, idFile, body)
		if d, why := hook(t, criusSID, criusTranscript, dir, unreadable); d != "ask" {
			t.Errorf("session-id file %s: want ask, got %s %q", name, d, why)
		}
	}
	write(t, idFile, criusSID+"\n")
	if err := os.Chmod(idFile, 0o666); err != nil {
		t.Fatal(err)
	}
	if d, why := hook(t, criusSID, criusTranscript, dir, unreadable); d != "ask" {
		t.Errorf("a world-writable session-id file: want ask, got %s %q", d, why)
	}
	if err := os.Remove(idFile); err != nil {
		t.Fatal(err)
	}
	if d, why := hook(t, criusSID, criusTranscript, dir, unreadable); d != "ask" {
		t.Errorf("no session-id file: want ask, got %s %q", d, why)
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
		// a cd before the post leaves an absolute file readable (only a relative one is not known)
		"cd /tmp && az repos pr update --id 7848 --title 'Generation two' --description @" + dir + "/d.md" + adoO,
	} {
		if d, why := crius(t, criusSID, dir, cmd); d != "" {
			t.Errorf("Crius mode: want allow, got %s %q\n  %s", d, why, cmd)
		}
		// a lead passes the PR create from feature/96365-* into bN on the allow list (item 3); the rest keep the click
		if d, _ := crius(t, leadSID, dir, cmd); (d == "ask") == strings.HasPrefix(cmd, "az repos pr create") {
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
		d, why := crius(t, criusSID, dir, cmd)
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
		if d, why := crius(t, criusSID, dir, cmd); d != "" {
			t.Errorf("want allow, got %s %q\n  %s", d, why, cmd)
		}
	}
}

// What the guard cannot read denies, names why and says to rewrite it; it never asks (review of 2a882aa, P3-12: each
// case is checked for its own reason).
func TestCriusModeDeniesWhatTheGuardCannotRead(t *testing.T) {
	dir := criusDir(t, nil)
	write(t, filepath.Join(dir, "c.json"), `{"comments":[{"content":"ok"}]}`)
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "sub", "c.json"), `{"comments":[{"content":"ok"}]}`)
	for cmd, want := range map[string]string{
		"az repos pr create --title t --description \"$(git log -1 --format=%B)\" --source-branch feature/96365-x --target-branch integration/96365-b1" + adoO: "a word the guard cannot read",
		"$AZ repos pr create --title t" + adoO: "the guard cannot read",
		"az repos pr $VERB --id 7848" + adoO:   "the guard cannot read",
		thread + "missing.json":                "cannot read the file it sends",
		thread + "/dev/stdin":                  "stdin or a device",
		"cd sub && " + thread + "c.json":       "after a folder change",
		"python3 -c \"import subprocess; subprocess.run(['az','repos','pr','create','--title','t'])\"":                "az repos pr create",
		"echo '{}' | az devops invoke --in-file - --http-method POST --area git --resource pullRequestThreads" + adoO: "stdin",
	} {
		d, why := crius(t, criusSID, dir, cmd)
		if d != "deny" || !strings.Contains(why, want) || !strings.Contains(why, "write it so the guard can read it") {
			t.Errorf("want deny naming %q with the rewrite hint, got %s %q\n  %s", want, d, why, cmd)
		}
		if d, _ := crius(t, leadSID, dir, cmd); d != "ask" {
			t.Errorf("lead session: want ask, got %q\n  %s", d, cmd)
		}
	}
}

// A post inside a script the command runs gets the same hard checks: the reading goes on into the script.
func TestCriusModeChecksPostsInsideScripts(t *testing.T) {
	dir := criusDir(t, map[string]string{"7848": "refs/heads/develop", "7850": "refs/heads/integration/96365-b1"})
	write(t, filepath.Join(dir, "ok.sh"), "#!/bin/bash\naz repos pr update --id 7850 --status completed --org https://dev.azure.com/Appxite\n")
	write(t, filepath.Join(dir, "bad.sh"), "#!/bin/bash\naz repos pr update --id 7850 --title t --org https://dev.azure.com/Appxite\naz repos pr update --id 7848 --status completed --org https://dev.azure.com/Appxite\n")
	if d, why := crius(t, criusSID, dir, "bash ok.sh"); d != "" {
		t.Errorf("ok.sh: want allow, got %s %q", d, why)
	}
	if d, why := crius(t, criusSID, dir, "bash bad.sh"); d != "deny" || !strings.Contains(why, "merges into refs/heads/develop") {
		t.Errorf("bad.sh: want deny naming develop, got %s %q", d, why)
	}
}

// Review of 2a882aa, P1 1-6, P2-8, P2-9, P3-10, P3-11: hardCheck reads every source a post takes text from, as the
// parser that found the post sees it, and denies any source it did not read.
func TestCriusModeReadsEveryBodySource(t *testing.T) {
	dir := criusDir(t, map[string]string{"7848": "refs/heads/develop", "7850": "refs/heads/integration/96365-b1"})
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for n, body := range map[string]string{"credit.md": coAuthor + "\n", "done.json": `{"status":"completed"}`,
		"done3.json": `{"status": 3}`, "clean.md": "Clean text.\n", "title.json": `{"title":"Quotes: b1"}`,
		"c.cfg": "data = \"{\\\"status\\\":\\\"completed\\\"}\"\n", "sub/clean.md": coAuthor + "\n",
		"prodhost.json": `{"comments":[{"content":"see https://quotes.production.appxite.com/health"}]}`} {
		write(t, filepath.Join(dir, n), body)
	}
	const (
		pr     = " --source-branch feature/96365-x --target-branch integration/96365-b1" + adoO
		prURL  = "https://dev.azure.com/Appxite/P/_apis/git/repositories/R/pullRequests/7848?api-version=7.1"
		invoke = "az devops invoke --area git --resource pullRequests --route-parameters project=P repositoryId=R pullRequestId=7848 --http-method PATCH --api-version 7.1" + adoO
	)
	for cmd, want := range map[string]string{
		// P1-1: az expands --opt=@file (and any word's value after its first =)
		"az repos pr create --title t --description=@credit.md" + pr:       "AI credit",
		"az boards work-item update --id 1 --discussion=@credit.md" + adoO: "AI credit",
		"az rest --method patch --uri " + prURL + " --body=@done.json":     "completes a PR",
		// P1-2: az takes a prefix of --in-file
		invoke + " --in-fi done.json": "completes a PR",
		invoke + " --in done.json":    "completes a PR",
		invoke + " --i done.json":     "completes a PR",
		// P1-3: and of --auto-complete
		"az repos pr update --id 7850 --a true" + adoO:  "--auto-complete",
		"az repos pr update --id 7850 --au true" + adoO: "--auto-complete",
		// P1-4: a file the same command writes first
		"printf '" + coAuthor + "' > clean.md && az repos pr create --title t --description @clean.md" + pr:        "the command writes",
		"cat > clean.md <<'EOF'\n" + coAuthor + "\nEOF\naz repos pr create --title t --description @clean.md" + pr: "AI credit",
		// P1-5: a body on stdin
		"http PATCH " + prURL + " < done.json":          "stdin",
		"cat done.json | http PATCH " + prURL:           "stdin",
		"curl -X PATCH -d @- " + prURL + " < done.json": "stdin",
		// P1-6: curl's config file, -T in a cluster or attached, --data-urlencode name@file
		"curl -K c.cfg -X PATCH " + prURL:                          "config",
		"curl -sT credit.md " + prURL:                              "AI credit",
		"curl -Tcredit.md " + prURL:                                "AI credit",
		"curl -X POST --data-urlencode content@credit.md " + prURL: "AI credit",
		// P2-8: find -execdir runs in each match's folder; -exec and xargs fill in words
		"find sub -execdir az repos pr create --title t --description @clean.md" + pr + " ';'":        "folder change",
		"find . -maxdepth 0 -exec az repos pr create --title t --description @clean.md" + pr + " ';'": "folder change",
		// P2-9: the PR status as its number
		invoke + " --in-file done3.json": "completes a PR",
		// P3-10: a production host word
		thread + "prodhost.json": "production URL",
		// P3-11: the target is read in the command's own org, which must be Appxite's
		"az repos pr update --id 7850 --status completed --org https://dev.azure.com/Other": "Appxite",
		"az repos pr update --id 7850 --status completed":                                   "--org",
	} {
		d, why := crius(t, criusSID, dir, cmd)
		if d != "deny" || !strings.Contains(why, want) {
			t.Errorf("want deny naming %q, got %s %q\n  %s", want, d, why, cmd)
		}
	}
	// the allow side: the same sources with clean text, an absolute file after a move (outside the tree find may write
	// in), the heredoc's own clean text
	other := t.TempDir()
	write(t, filepath.Join(other, "clean.md"), "Clean text.\n")
	for _, cmd := range []string{
		"az repos pr create --title t --description=@clean.md" + pr,
		invoke + " --in-fi title.json",
		"curl -sT clean.md " + prURL,
		"curl -X POST --data-urlencode content@clean.md " + prURL,
		"cat > fresh.md <<'EOF'\nClean.\nEOF\naz repos pr create --title t --description @fresh.md" + pr,
		"find . -maxdepth 0 -exec az repos pr create --title t --description @" + other + "/clean.md" + pr + " ';'",
		"az repos pr update --id 7850 --status completed --org https://dev.azure.com/appxite/",
	} {
		if d, why := crius(t, criusSID, dir, cmd); d != "" {
			t.Errorf("want allow, got %s %q\n  %s", d, why, cmd)
		}
	}
}

// Review of 2a882aa, P3-13: Crius-mode allows go to their own log, so the ask log counts asks only.
func TestCriusModeLogsAllowsApart(t *testing.T) {
	dir := criusDir(t, map[string]string{"7850": "refs/heads/integration/96365-b1"})
	if d, why := crius(t, criusSID, dir, "az repos pr update --id 7850 --status completed"+adoO); d != "" {
		t.Fatalf("want allow, got %s %q", d, why)
	}
	state := filepath.Join(os.Getenv("CRIUS_HOME"), "state")
	if b, err := os.ReadFile(filepath.Join(state, "post-guard-asks.log")); err == nil && len(b) > 0 {
		t.Errorf("the ask log holds an allow: %s", b)
	}
	if b, err := os.ReadFile(filepath.Join(state, "post-guard-allows.log")); err != nil || !strings.Contains(string(b), "repos pr update") {
		t.Errorf("the allow log: %v %q", err, b)
	}
}
