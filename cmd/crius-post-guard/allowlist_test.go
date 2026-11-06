package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The lead allow list (Crius 11-06 12:16, items 1-5): an allow and a deny for each item. A deny keeps the owner's click
// (the guard asks) and names why the list refused it.

const allowOrg = " --org https://dev.azure.com/Appxite --project P"

// leadCheck runs the lead reading on cmd in cwd: want "" for a pass, else a text the ask must hold.
func leadCheck(t *testing.T, cwd, cmd, want string) {
	t.Helper()
	why := check(bash(cmd, cwd))
	switch {
	case want == "" && why != "":
		t.Errorf("want pass, got %q\n  %s", why, cmd)
	case want != "" && (!strings.Contains(why, "owner's click") || !strings.Contains(why, want)):
		t.Errorf("want an ask naming %q, got %q\n  %s", want, why, cmd)
	}
}

// Item 1: az pipelines run with only ids, a known branch and output options passes; free text, a PR merge build, no
// branch or --variables asks.
func TestLeadAllowListPipelinesRun(t *testing.T) {
	dir := t.TempDir()
	for _, cmd := range []string{
		"az pipelines run --id 1119 --branch feature/96365-partner-quote" + allowOrg + " -o none",
		"az pipelines run --id 1119 --branch refs/heads/integration/96365-b2" + allowOrg + " --query id -o tsv",
		"az pipelines run --id 1119 --branch develop" + allowOrg,
	} {
		leadCheck(t, dir, cmd, "")
	}
	for cmd, want := range map[string]string{
		"az pipelines run --id 1119 --branch refs/pull/7848/merge" + allowOrg:          "a PR merge build goes through pr-build.sh",
		"az pipelines run --id 1119" + allowOrg:                                        "without --branch",
		"az pipelines run --id 1119 --branch develop --parameters a=b" + allowOrg:      "only --id, --branch",
		"az pipelines run --id 1119 --branch develop --variables x=1" + allowOrg:       "only --id, --branch",
		"az pipelines run --name quotes --branch develop" + allowOrg:                   "only --id, --branch",
		"az pipelines run --id 1119 --branch main" + allowOrg:                          "a PR merge build goes through pr-build.sh",
		"az pipelines run --id 1119 --branch develop --org https://x-prod.example.com": "production URL",
	} {
		leadCheck(t, dir, cmd, want)
	}
}

// Item 3: a PR from feature/96365-* into integration/96365-bN passes when its text is clean and it sets no
// auto-complete; into develop or main, from another branch, with AI credit or --auto-complete it asks.
func TestLeadAllowListPRCreate(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "d.md"), "Partner quote detail: regenerated migration.\n")
	write(t, filepath.Join(dir, "credit.md"), "Done.\n\n"+coAuthor+"\n")
	const pr = "az repos pr create --title 'Quotes: partner quote' --source-branch feature/96365-partner-quote"
	leadCheck(t, dir, pr+" --description @d.md --target-branch integration/96365-b1"+allowOrg, "")
	for cmd, want := range map[string]string{
		pr + " --description @d.md --target-branch develop" + allowOrg:                                       "develop and main keep the click",
		pr + " --description @d.md --target-branch main" + allowOrg:                                          "develop and main keep the click",
		"az repos pr create --title t --source-branch wip/x --target-branch integration/96365-b1" + allowOrg: "needs --source-branch feature/96365-*",
		pr + " --description @credit.md --target-branch integration/96365-b1" + allowOrg:                     "AI credit",
		pr + " --description @d.md --target-branch integration/96365-b1 --auto-complete true" + allowOrg:     "--auto-complete",
		pr + " --description @d.md --target-branch integration/96365-b1 --a true" + allowOrg:                 "--auto-complete",
	} {
		leadCheck(t, dir, cmd, want)
	}
}

// Item 4: reads never ask.
func TestLeadReadsNeverAsk(t *testing.T) {
	dir := t.TempDir()
	for _, cmd := range []string{
		"az repos pr show --id 7848" + allowOrg + " --query status -o tsv",
		"az repos pr list --status active" + allowOrg,
		"az pipelines runs show --id 126862" + allowOrg,
		"az pipelines runs list --pipeline-ids 1119 --branch refs/pull/7848/merge" + allowOrg,
		"az repos ref list --filter heads/integration" + allowOrg + " --repository R",
		"az devops invoke --area git --resource pullRequests --route-parameters project=P repositoryId=R pullRequestId=7848" + allowOrg,
		"az devops invoke --area git --resource pullRequests --route-parameters project=P repositoryId=R pullRequestId=7848 --http-method GET" + allowOrg,
		`git -c http.extraheader="AUTHORIZATION: bearer $(az account get-access-token --resource 499b84ac-1321-427f-aa17-267ca6975798 --query accessToken -o tsv)" push origin feature/96365-partner-quote`,
	} {
		leadCheck(t, dir, cmd, "")
	}
	// the write next to them still asks
	leadCheck(t, dir, "az devops invoke --area git --resource pullRequests --route-parameters project=P repositoryId=R pullRequestId=7848 --http-method PATCH --in-file x.json"+allowOrg, "owner's click")
}

// Item 5: a pinned team script passes unread, inside the Quotes build folder, this user's alone; a changed one, one
// that is group-writable or one elsewhere asks and names the file. merge.sh passes only for a PR into bN; ado-suite.ts
// only as report <id>; bun takes no flag that loads code first.
func TestLeadPinnedTeamScripts(t *testing.T) {
	home, _ := quotesHomePRs(t, map[string]prInfo{"7848": {Status: "active", Target: "refs/heads/integration/96365-b1"},
		"7849": {Status: "active", Target: "refs/heads/develop"}})
	dir := t.TempDir()
	b, err := os.ReadFile(filepath.Join("testdata", "team", "merge.sh"))
	if err != nil {
		t.Fatal(err)
	}
	merge := filepath.Join(home, ".crius/state/quotes/build/merge.sh")
	write(t, merge, string(b))
	if err := os.Chmod(merge, 0o755); err != nil {
		t.Fatal(err)
	}
	pinBody(t, "merge.sh", string(b))
	// the shipped copy runs $NS/lead/review-gate.sh, which is not there (review r2 P1-6: it passed unread); a merge.sh whose
	// nested scripts are pinned passes in TestAPinnedScriptsNestedScriptsAreReadOrPinned
	leadCheck(t, dir, "bash ~/.crius/state/quotes/build/merge.sh 7848", "review-gate.sh")
	leadCheck(t, dir, "bash ~/.crius/state/quotes/build/merge.sh 7849", "merge.sh completes PR 7849 into refs/heads/develop")

	// elsewhere
	other := filepath.Join(dir, "merge.sh")
	write(t, other, string(b))
	leadCheck(t, dir, "bash "+other+" 7848", "outside the Quotes build folder")
	// group-writable
	if err := os.Chmod(merge, 0o775); err != nil {
		t.Fatal(err)
	}
	leadCheck(t, dir, "bash ~/.crius/state/quotes/build/merge.sh 7848", "is not this user's alone")
	if err := os.Chmod(merge, 0o755); err != nil {
		t.Fatal(err)
	}
	// changed after the pin
	write(t, merge, string(b)+"\n# changed\n")
	leadCheck(t, dir, "bash ~/.crius/state/quotes/build/merge.sh 7848", "merge.sh at "+merge+" does not match the SHA-256")

	// ado-suite.ts (exactHome's: a POST in run) passes as report and asks as run outside the exact rule
	bun := "bun --no-env-file --config=" + home + "/.crius/state/quotes/build/bin/empty-bunfig.toml " + home + "/.crius/state/quotes/build/bin/ado-suite.ts"
	leadCheck(t, dir, bun+" report 126862 && echo done", "")
	leadCheck(t, dir, bun+" run "+dir+" feature/96365-x", "ado-suite.ts passes on its pin only as `report <id>`")
	pre := "bun --preload " + dir + "/x.ts --no-env-file --config=" + home + "/.crius/state/quotes/build/bin/empty-bunfig.toml " + home + "/.crius/state/quotes/build/bin/ado-suite.ts report 126862"
	write(t, filepath.Join(dir, "x.ts"), "console.log(1)\n")
	leadCheck(t, dir, pre, "only --no-env-file and --config")
}
