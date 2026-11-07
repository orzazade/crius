package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Owner 11-08 ("why we have such guards at all, it is too much"): the guard checks Azure DevOps posts and never asks. A
// post whose text passes goes on at once; one that fails a check is refused with the reason, so the session fixes it and
// tries again. These run the hook itself in a lead session (not Crius's), as the leads' tabs do.

const draftPR = "7890"

// checkCall runs the hook on cmd in a lead session and fails the test unless the decision is want ("" passes, else
// "deny") and the reason holds every one of says.
func guardCall(t *testing.T, cwd, cmd, want string, says ...string) {
	t.Helper()
	d, why := crius(t, leadSID, cwd, cmd)
	if d != want {
		t.Errorf("%s:\nwant %q, got %q %q", cmd, want, d, why)
		return
	}
	for _, s := range says {
		if !strings.Contains(why, s) {
			t.Errorf("%s:\nthe reason %q does not say %q", cmd, why, s)
		}
	}
}

func TestTonightsCommandsAreCheckedNotAsked(t *testing.T) {
	dir := criusDir(t, map[string]string{draftPR: "refs/heads/integration/96365-b4", "7891": "refs/heads/develop"})
	empty := filepath.Join(dir, "empty.md")
	good := filepath.Join(dir, "good.md")
	credit := filepath.Join(dir, "credit.md")
	write(t, empty, "")
	write(t, good, "## Why\nThe quote list loads the org once.\n")
	write(t, credit, "Fix the list.\n\n"+coAuthor+"\n")
	// a pipeline run on a draft PR's merge ref (refused by the allow list until now: a click each time)
	guardCall(t, dir, "az pipelines run --id 1119 --branch refs/pull/"+draftPR+"/merge"+adoO+` --project "AppXite Platform" --query id -o tsv`, "")
	// a PR update from an empty file: refused, it would wipe the description
	guardCall(t, dir, `az repos pr update --id `+draftPR+` --description "$(cat `+empty+`)"`+adoO, "deny", "empty")
	guardCall(t, dir, `az repos pr update --id `+draftPR+` --description @`+empty+adoO, "deny", "empty")
	// the same from a file with text: goes on
	guardCall(t, dir, `az repos pr update --id `+draftPR+` --description "$(cat `+good+`)"`+adoO, "")
	guardCall(t, dir, `az repos pr update --id `+draftPR+` --description @`+good+adoO, "")
	// AI credit in the text: refused, in the words or in the file
	guardCall(t, dir, `az repos pr update --id `+draftPR+` --description "$(cat `+credit+`)"`+adoO, "deny", "AI credit")
	guardCall(t, dir, `az repos pr create --title "Fix list" --description "Fix. `+coAuthor+`" --source-branch feature/96365-x --target-branch integration/96365-b4`+adoO, "deny", "AI credit")
	// completing into develop: refused, never asked
	guardCall(t, dir, "az repos pr update --id 7891 --status completed"+adoO, "deny", "develop")
	// into a bN integration branch: goes on
	guardCall(t, dir, "az repos pr update --id "+draftPR+" --status completed"+adoO, "")
}

func TestOpsClickCheckRunsInItsOwnTab(t *testing.T) {
	_, _, _ = opsHome(t)
	dir := t.TempDir()
	guardCall(t, dir, "~/.crius/state/crius/ops-bin/click-check.sh workspace:1000070003 surface:1000070012", "")
}

func TestReadOnlyAzureDevOpsCallsGoOn(t *testing.T) {
	dir := criusDir(t, nil)
	for _, cmd := range []string{
		"az repos pr list --status active" + adoO + " -o json",
		"az repos pr show --id 7890" + adoO,
		"az pipelines show --id 1119" + adoO,
		"az pipelines runs list --pipeline-ids 1119 --top 5" + adoO,
		"az pipelines runs show --id 130001" + adoO,
		"az boards work-item show --id 96365" + adoO,
	} {
		guardCall(t, dir, cmd, "")
	}
}

func TestPostTextIsCheckedForWhatTheTeamsRequire(t *testing.T) {
	dir := criusDir(t, map[string]string{draftPR: "refs/heads/integration/96365-b4"})
	long := filepath.Join(dir, "long.md")
	model := filepath.Join(dir, "model.md")
	write(t, long, strings.Repeat("a", 4001))
	write(t, model, "Deep review by Opus found nothing.\n")
	guardCall(t, dir, `az repos pr update --id `+draftPR+` --description "$(cat `+long+`)"`+adoO, "deny", "4001", "4000")
	guardCall(t, dir, `az repos pr update --id `+draftPR+` --description @`+model+adoO, "deny", "Opus")
	guardCall(t, dir, `az repos pr update --id `+draftPR+` --title "Codex fixes"`+adoO, "deny", "Codex")
	guardCall(t, dir, `az repos pr update --id `+draftPR+` --description ""`+adoO, "deny", "empty")
	guardCall(t, dir, `az boards work-item update --id 96365 --discussion ""`+adoO, "deny", "empty")
	// a path that holds a tool's name is not text people read (leads write their PR text in /private/tmp/claude-501/…)
	tool := filepath.Join(dir, "claude-501", "pr.md")
	if err := os.MkdirAll(filepath.Dir(tool), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, tool, "The quote list loads the org once.\n")
	guardCall(t, dir, `az repos pr update --id `+draftPR+` --description @`+tool+adoO, "")

	// the Quotes team: a PR description names AB#96365 and no other work item (TEAM.md, pr-text-check.sh rc 5)
	home, _ := os.UserHomeDir()
	wt := filepath.Join(home, "Projects", "Appxite", "svc_quote", "wt", "lead")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	guardCall(t, wt, `az repos pr update --id `+draftPR+` --description "$(cat `+tool+`)"`+adoO, "deny", "AB#96365")
	with := filepath.Join(dir, "with.md")
	write(t, with, "Loads the org once.\n\nAB#96365\n")
	guardCall(t, wt, `az repos pr update --id `+draftPR+` --description "$(cat `+with+`)"`+adoO, "")
	other := filepath.Join(dir, "other.md")
	write(t, other, "Loads the org once.\n\nAB#96365 AB#94205\n")
	guardCall(t, wt, `az repos pr update --id `+draftPR+` --description "$(cat `+other+`)"`+adoO, "deny", "AB#94205")
	// another team's folder has no such rule
	guardCall(t, dir, `az repos pr update --id `+draftPR+` --description "$(cat `+tool+`)"`+adoO, "")
}

func TestTextTheGuardCannotResolveIsRefusedWithTheWayToWriteIt(t *testing.T) {
	dir := criusDir(t, nil)
	for _, cmd := range []string{
		`az repos pr update --id 7890 --description "$DESC"` + adoO,
		`az repos pr update --id 7890 --description "$(printf '%s' x)"` + adoO,
		`az repos pr update --id 7890 --description $(cat d.md)` + adoO, // unquoted: the file's words become az's words
	} {
		guardCall(t, dir, cmd, "deny", `--description "$(cat file)"`)
	}
}

func TestHardBlocksAreRefusedNotAsked(t *testing.T) {
	dir := criusDir(t, map[string]string{draftPR: "refs/heads/integration/96365-b4"})
	guardCall(t, dir, "az repos pr update --id "+draftPR+" --auto-complete true"+adoO, "deny", "auto-complete")
	guardCall(t, dir, "az repos pr create --title T --description D --auto-complete --source-branch feature/96365-x --target-branch integration/96365-b4"+adoO, "deny", "auto-complete")
	guardCall(t, dir, `az boards work-item update --id 96365 --discussion "see https://quotes.production.appxite.com"`+adoO, "deny", "production")
}

const allowOrg = " --org https://dev.azure.com/Appxite --project P"

// leadCheck runs the hook in a lead session: want "" for a pass, else a text the deny must hold.
func leadCheck(t *testing.T, cwd, cmd, want string) {
	t.Helper()
	if want == "" {
		guardCall(t, cwd, cmd, "")
		return
	}
	guardCall(t, cwd, cmd, "deny", want)
}

// Review of 07f23bd P2-2: the Quotes rule keys on the PR's repository too (an update by id from another folder, the
// repository's id on create), and a svc_quote PR links work item 96365 and no other.
func TestTheQuotesRuleKeysOnThePRsRepository(t *testing.T) {
	dir := criusDir(t, nil)
	old := prState
	prState = func(id string) (prInfo, error) {
		return prInfo{Head: head7848, Status: "active", Target: "refs/heads/integration/96365-b4", Repo: map[string]string{"7890": "svc_quote", "7892": "svc_other"}[id]}, nil
	}
	t.Cleanup(func() { prState = old })
	none, with := filepath.Join(dir, "none.md"), filepath.Join(dir, "with.md")
	write(t, none, "Fix the list.\n")
	write(t, with, "Fix the list.\n\nAB#96365\n")
	const create = `az repos pr create --project "AppXite Platform" --source-branch feature/96365-x --target-branch integration/96365-b4 --title t`
	guardCall(t, dir, `az repos pr update --id 7890 --description @`+none+adoO, "deny", "AB#96365")
	guardCall(t, dir, `az repos pr update --id 7890 --description @`+with+adoO, "")
	guardCall(t, dir, `az repos pr update --id 7892 --description @`+none+adoO, "")
	guardCall(t, dir, create+` --repository d88331ee-60dc-4f15-b236-b1790c6fdeaa --description @`+none+adoO, "deny", "AB#96365")
	guardCall(t, dir, create+` --repository D88331EE-60DC-4F15-B236-B1790C6FDEAA --description @`+with+adoO, "")
	guardCall(t, dir, create+` --repository svc_quote --work-items 94205`+adoO, "deny", "94205")
	guardCall(t, dir, create+` --repository svc_quote --work-items 96365`+adoO, "")
	guardCall(t, dir, `az repos pr work-item add --id 7890 --work-items 94205`+adoO, "deny", "94205")
	guardCall(t, dir, `az repos pr work-item add --id 7892 --work-items 94205`+adoO, "")
}

// Review of 07f23bd P2-1: a model or tool name in one word with a slash is text people read; paths, URLs and @files
// are not.
func TestAModelNameInAnyTextWordIsRefused(t *testing.T) {
	dir := criusDir(t, map[string]string{"7890": "refs/heads/integration/96365-b4"})
	for _, cmd := range []string{
		`az repos pr update --id 7890 --title "Codex/Opus-findings"` + adoO,
		`az repos pr update --id 7890 --title=fix/claude` + adoO,
		`az boards work-item update --id 96365 --discussion Reviewed-by-Opus/Codex` + adoO,
		`az rest --method post --uri https://dev.azure.com/Appxite/P/_apis/git/repositories/R/pullRequests/7890/threads?api-version=7.1 --body {"comments":[{"content":"Opus/Codex:LGTM"}]}`,
	} {
		guardCall(t, dir, cmd, "deny", "names a model or tool")
	}
	pr := filepath.Join(dir, "claude-501", "pr.md")
	if err := os.MkdirAll(filepath.Dir(pr), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, pr, "Fix the list.\n")
	guardCall(t, dir, `az repos pr update --id 7890 --source-branch feature/claude-fix --description @`+pr+adoO, "")
	guardCall(t, dir, `az devops invoke --area git --resource pullRequests --http-method PATCH --in-file `+pr+` --org https://dev.azure.com/Appxite`, "")
}

// Reviews of 1a2069f and 3a9092d: zsh can make "cat" another program in ways no list of tricks covers (path=(…),
// functions[cat]=…, hash cat=/x, a glob qualifier *(e:…:) in any word, printf -v path, for path in, repeat 1 hash), so
// a "$(cat FILE)" word is read only when the command is the post alone, or cd DIR && the post, with plain words; else
// the reason says to pass @FILE. A wiki page's --path is its title; a value glued to a short option is text.
func TestCatIsReadOnlyWhenNothingMayChangeTheShell(t *testing.T) {
	dir := criusDir(t, map[string]string{"7890": "refs/heads/integration/96365-b4"})
	good := filepath.Join(dir, "good.md")
	write(t, good, "Fix the list.\n")
	post := `az repos pr update --id 7890 --description "$(cat ` + good + `)"` + adoO
	for _, before := range []string{
		"path=(/x $path); ",
		"functions[cat]='print -r -- x'; ",
		"hash cat=/x; ",
		"PATH=/x:$PATH; ",
		"export PATH=/x; ",
		"alias cat=/x; ",
		"cat() { printf x; }; ",
		"source env.sh; ",
		"eval \"$S\"; ",
		"echo /*(e:'path=(/x $path)':) >/dev/null; ",
		"cd /tmp && : /*(e:'path=(/x $path)':); ",
		"printf -v path /x; ",
		"for path in /x; do :; done; ",
		"repeat 1 hash cat=/x; ",
		"echo go; ",
		"set -e; ",
	} {
		guardCall(t, dir, before+post, "deny") // some for an earlier reason (an unknown program, eval); the rest say "pass @FILE"
	}
	guardCall(t, dir, "echo go; "+post, "deny", "pass @FILE")
	guardCall(t, dir, `az repos pr update --id 7890 /*(e:'path=(/x $path)':) --description "$(cat `+good+`)"`+adoO, "deny", "pass @FILE")
	for _, cmd := range []string{post, "cd " + dir + " && " + post, post + " -o none 2>/dev/null", post + ` --query "[id, title]"`} {
		guardCall(t, dir, cmd, "")
	}
	for _, cmd := range []string{
		`az devops wiki page create --wiki W --path "Claude review notes" --content "Fix the list."` + adoO,
		`az repos pr update --id 7890 -d"Opus review: no findings"` + adoO,
		`az repos pr update --id 7890 -d"Opus review: LGTM (score=9)"` + adoO,
		`curl -X POST https://dev.azure.com/Appxite/P/_apis/git/repositories/R/pullRequests/7890/threads?api-version=7.1 -sdOpus-says-LGTM`,
		`wget --no-config --post-data x -OdOpus-says https://dev.azure.com/Appxite/P/_apis/git/repositories/R/pullRequests/7890/threads?api-version=7.1`,
		`curl -X POST https://dev.azure.com/Appxite/P/_apis/git/repositories/R/pullRequests/7890/threads?api-version=7.1 -d'{"comments":[{"content":"Opus: LGTM"}]}'`,
	} {
		guardCall(t, dir, cmd, "deny", "names a model or tool")
	}
	guardCall(t, dir, `az devops wiki page create --wiki W --path "Quote list notes" --content "Fix the list."`+adoO, "")
}
