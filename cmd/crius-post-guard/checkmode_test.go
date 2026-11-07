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
