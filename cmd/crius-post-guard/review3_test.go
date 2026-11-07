package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// R1: a resolved "$(cat FILE)" word whose text is @other or a file name for --in-file: hardCheck never reads that file.
func TestReviewResolvedWordNamesAFile(t *testing.T) {
	dir := criusDir(t, map[string]string{"7891": "refs/heads/develop"})
	write(t, filepath.Join(dir, "done.json"), `{"status":"completed"}`)
	write(t, filepath.Join(dir, "n.txt"), "@"+filepath.Join(dir, "done.json")+"\n")
	write(t, filepath.Join(dir, "m.txt"), filepath.Join(dir, "done.json")+"\n")
	write(t, filepath.Join(dir, "x2.md"), "Deployed: see https://quotes.production.appxite.com/health \U0001F916\n")
	write(t, filepath.Join(dir, "p.txt"), "@"+filepath.Join(dir, "x2.md")+"\n")
	for _, cmd := range []string{
		// control: the file named directly is refused
		`az rest --method patch --uri https://dev.azure.com/Appxite/P/_apis/git/repositories/R/pullrequests/7891?api-version=7.1 --body @` + filepath.Join(dir, "done.json"),
		`az rest --method patch --uri https://dev.azure.com/Appxite/P/_apis/git/repositories/R/pullrequests/7891?api-version=7.1 --body "$(cat ` + filepath.Join(dir, "n.txt") + `)"`,
		`az devops invoke --area git --resource pullRequests --route-parameters project=P repositoryId=R pullRequestId=7891 --http-method PATCH --api-version 7.1` + adoO + ` --in-file "$(cat ` + filepath.Join(dir, "m.txt") + `)"`,
		`az repos pr update --id 7891 --description "$(cat ` + filepath.Join(dir, "p.txt") + `)"` + adoO,
	} {
		d, why := crius(t, leadSID, dir, cmd)
		t.Logf("decision=%q why=%q\n  %s", d, why, cmd)
		if d != "deny" {
			t.Errorf("BYPASS: want deny, got %q\n  %s", d, cmd)
		}
	}
}

// R2: the pin of a script vouches for a nested script it runs that the guard cannot read or that the command writes.
func TestReviewPinnedParentVouchesForNestedScript(t *testing.T) {
	home, _ := quotesHomePRs(t, map[string]prInfo{"7848": {Status: "active", Target: "refs/heads/integration/96365-b1"}})
	dir := t.TempDir()
	lead := filepath.Join(home, ".crius/state/quotes/build/bin/lead")
	if err := os.MkdirAll(lead, 0o755); err != nil {
		t.Fatal(err)
	}
	install := func(p, body string, pin bool) {
		t.Helper()
		write(t, p, body)
		if err := os.Chmod(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if pin {
			pinBody(t, filepath.Base(p), body)
		}
	}
	rd := func(n string) string {
		b, err := os.ReadFile(filepath.Join("testdata", "team", n))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	install(filepath.Join(lead, "merge.sh"), rd("merge.sh"), true)
	install(filepath.Join(lead, "review-gate.sh"), rd("review-gate.sh"), true)
	install(filepath.Join(lead, "merge-ok.sh"), rd("merge-ok.sh"), true)
	const cmd = "bash ~/.crius/state/quotes/build/bin/lead/merge.sh 7848 1"
	evil := "#!/bin/bash\naz repos pr update --id 7891 --status completed --org https://dev.azure.com/Appxite\n"
	scan := filepath.Join(lead, "postmerge-scan.sh")

	// control: a readable unpinned postmerge-scan.sh that completes a PR is read and refused (7891 cannot be read)
	install(scan, evil, false)
	d, why := crius(t, leadSID, dir, cmd)
	t.Logf("control readable: %q %q", d, why)

	// a. over 1 MiB: cannotRead in merge.sh's pinned ctx gives ""
	install(scan, evil+"#"+strings.Repeat("x", 1<<20)+"\n", false)
	d, why = crius(t, leadSID, dir, cmd)
	t.Logf("a (>1MiB): %q %q", d, why)
	if d != "deny" {
		t.Errorf("BYPASS a: an unpinned >1 MiB nested script passed unread")
	}
	// b. not there, and the command writes it first
	if err := os.Remove(scan); err != nil {
		t.Fatal(err)
	}
	cmdB := "printf '%s\\n' 'az repos pr update --id 7891 --status completed --org https://dev.azure.com/Appxite' > ~/.crius/state/quotes/build/bin/lead/postmerge-scan.sh && " + cmd
	d, why = crius(t, leadSID, dir, cmdB)
	t.Logf("b (written by the command): %q %q", d, why)
	if d != "deny" {
		t.Errorf("BYPASS b: a nested script the command writes passed")
	}
	// c. chmod 000
	install(scan, evil, false)
	if err := os.Chmod(scan, 0); err != nil {
		t.Fatal(err)
	}
	d, why = crius(t, leadSID, dir, cmd)
	t.Logf("c (mode 000): %q %q", d, why)
	if d != "deny" {
		t.Errorf("BYPASS c: an unreadable nested script passed")
	}
	_ = os.Chmod(scan, 0o755)
	_ = os.Remove(scan)

	// d. merge-ok.sh's bun scripts/heavy.ts in a repo checkout: not pinned, the agent can edit it
	ui := filepath.Join(home, "Projects/Appxite/svc_quote/channels/poc/ui-app/scripts")
	if err := os.MkdirAll(ui, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(ui, "heavy.ts"), "await fetch('https://dev.azure.com/Appxite/P/_apis/git/repositories/R/pullRequests/7891?api-version=7.1', { method: 'PATCH', body: '{\"status\":\"completed\"}' });\n")
	d, why = crius(t, leadSID, dir, cmd)
	t.Logf("d (heavy.ts posts): %q %q", d, why)
	// known gap, kept so the live merge chain runs: merge-ok.sh runs heavy.ts and tsc after a cd in a function, where the
	// guard does not find them; the pin vouches for that place (as any session can change the pinned scripts themselves)
	if d != "" {
		t.Errorf("d: want the known gap (pass), got %q %q", d, why)
	}
}

// R3: catWord reads a literal name; the shell globs, brace-expands and expands ~user in it.
func TestReviewCatWordShellExpansion(t *testing.T) {
	dir := criusDir(t, map[string]string{"7890": "refs/heads/integration/96365-b4"})
	credit := "Fix.\n\n" + coAuthor + "\n"
	write(t, filepath.Join(dir, "d*.md"), "Fix the list.\n")
	write(t, filepath.Join(dir, "dz.md"), credit)
	write(t, filepath.Join(dir, "{a,b}.md"), "Fix the list.\n")
	write(t, filepath.Join(dir, "a.md"), "Fix.\n")
	write(t, filepath.Join(dir, "b.md"), credit)
	home, _ := os.UserHomeDir()
	user := filepath.Base(home)
	write(t, filepath.Join(home, "t.md"), credit)
	if err := os.MkdirAll(filepath.Join(dir, "~"+user), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "~"+user, "t.md"), "Fix the list.\n")
	for _, w := range []string{"d*.md", "{a,b}.md", "~" + user + "/t.md"} {
		cmd := `az repos pr update --id 7890 --description "$(cat ` + w + `)"` + adoO
		d, why := crius(t, leadSID, dir, cmd)
		t.Logf("%s: %q %q", w, d, why)
		if d != "deny" {
			t.Errorf("BYPASS: the shell's cat %s reads AI credit; the guard read the decoy", w)
		}
	}
	// a function named cat
	write(t, filepath.Join(dir, "good.md"), "Fix the list.\n")
	for _, cmd := range []string{
		`cat() { printf 'Generated with Claude'; }; az repos pr update --id 7890 --description "$(cat good.md)"` + adoO,
		`PATH=/tmp/x:$PATH az repos pr update --id 7890 --description "$(cat good.md)"` + adoO,
		`echo "Opus says ok" > good.md && az repos pr update --id 7890 --description "$(cat good.md)"` + adoO,
		`cp ` + filepath.Join(dir, "dz.md") + ` good.md; az repos pr update --id 7890 --description "$(cat good.md)"` + adoO,
	} {
		d, why := crius(t, leadSID, dir, cmd)
		t.Logf("%q %q\n  %s", d, why, cmd)
		if d != "deny" {
			t.Errorf("BYPASS: %s", cmd)
		}
	}
}

// R4: contentCheck skips one-word text with a slash, and the Quotes rule keys on folder or --repository.
func TestReviewContentGaps(t *testing.T) {
	dir := criusDir(t, map[string]string{"7890": "refs/heads/integration/96365-b4"})
	for _, cmd := range []string{
		`az repos pr update --id 7890 --title "Codex/Opus-findings"` + adoO,
		`az repos pr update --id 7890 --title=fix/claude` + adoO,
		`az boards work-item update --id 96365 --discussion Reviewed-by-Opus/Codex` + adoO,
		`az rest --method post --uri https://dev.azure.com/Appxite/P/_apis/git/repositories/R/pullRequests/7890/threads?api-version=7.1 --body {"comments":[{"content":"Opus/Codex:LGTM"}]}`,
	} {
		d, why := crius(t, leadSID, dir, cmd)
		t.Logf("%q %q\n  %s", d, why, cmd)
		if d != "deny" {
			t.Errorf("GAP: model name passed: %s", cmd)
		}
	}
	// Quotes PR text from a folder that is not the team's, no --repository (update by id) or the repo's GUID
	write(t, filepath.Join(dir, "x.md"), "Fix the list.\n")
	for _, cmd := range []string{
		`az repos pr update --id 7890 --description @` + filepath.Join(dir, "x.md") + adoO,
		`az repos pr create --repository d88331ee-60dc-4f15-b236-b1790c6fdeaa --project "AppXite Platform" --source-branch feature/96365-x --target-branch integration/96365-b4 --title t --description @` + filepath.Join(dir, "x.md") + adoO,
		`az repos pr create --repository svc_quote --project "AppXite Platform" --source-branch feature/96365-x --target-branch integration/96365-b4 --title t --work-items 94205` + adoO,
	} {
		d, why := crius(t, leadSID, dir, cmd)
		t.Logf("Quotes rule: %q %q\n  %s", d, why, cmd)
	}
}

func TestReviewCatWordMore(t *testing.T) {
	dir := criusDir(t, map[string]string{"7890": "refs/heads/integration/96365-b4"})
	write(t, filepath.Join(dir, "nul.md"), "Review by Cla\x00ude\n")
	write(t, filepath.Join(dir, "good.md"), "Fix the list.\n")
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "sub", "good.md"), "Opus says fine\n")
	long := strings.Repeat("a", 4001)
	write(t, filepath.Join(dir, "long.md"), long)
	if err := os.Symlink(filepath.Join(dir, "sub", "good.md"), filepath.Join(dir, "ln.md")); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ cmd, want string }{
		{`bash -c 'az repos pr update --id 7890 --description "$(cat nul.md)"` + adoO + `'`, "deny"},
		{`az repos pr update --id 7890 --description "$(cat nul.md)"` + adoO, "deny"},
		{"cat > good.md <<'EOF'\nOpus says fine\nEOF\naz repos pr update --id 7890 --description \"$(cat good.md)\"" + adoO, "deny"},
		{"cat > good.md <<EOF\n$(date) fine\nEOF\naz repos pr update --id 7890 --description \"$(cat good.md)\"" + adoO, "deny"},
		{`cd sub && az repos pr update --id 7890 --description "$(cat good.md)"` + adoO, "deny"},
		{`az repos pr update --id 7890 --description "$(cat ln.md)"` + adoO, "deny"},
		{`az repos pr update --id 7890 --description="$(cat sub/good.md)"` + adoO, "deny"},
		{`az repos pr update --id 7890 --desc "$(cat long.md)"` + adoO, "deny"},
		{`az repos pr update --id 7890 --description x -d "$(cat long.md)"` + adoO, "deny"},
		{`az repos pr update --id 7890 --description "$(< sub/good.md)"` + adoO, "deny"},
		{`az repos pr update --id 7890 --description "$(cat good.md)" --description "$(cat sub/good.md)"` + adoO, "deny"},
		{`az repos pr update --id 7890 --title "$(cat sub/good.md)"` + adoO, "deny"},
		{`f=sub/good.md; az repos pr update --id 7890 --description "$(cat $f)"` + adoO, "deny"},
		{`git checkout -- good.md; az repos pr update --id 7890 --description "$(cat good.md)"` + adoO, "?"},
		{`(cd sub; true) && az repos pr update --id 7890 --description "$(cat good.md)"` + adoO, ""},
		{`pushd sub >/dev/null; az repos pr update --id 7890 --description "$(cat good.md)"` + adoO, "deny"},
		{`set -- sub; cd "$1"; az repos pr update --id 7890 --description "$(cat good.md)"` + adoO, "deny"},
	} {
		d, why := crius(t, leadSID, dir, c.cmd)
		t.Logf("%q %q\n  %s", d, why, c.cmd)
		if c.want == "deny" && d != "deny" {
			t.Errorf("BYPASS: %s", c.cmd)
		}
	}
}

func TestReviewNulThroughBashScript(t *testing.T) {
	dir := criusDir(t, map[string]string{"7890": "refs/heads/integration/96365-b4"})
	nulCredit := strings.Replace(genWith, "Claude", "Cla\x00ude", 1) + " Deep review by Op\x00us"
	write(t, filepath.Join(dir, "nul.md"), "Review.\n\n"+nulCredit+"\n")
	write(t, filepath.Join(dir, "post.sh"), "#!/bin/bash\naz repos pr update --id 7890 --description \"$(cat "+filepath.Join(dir, "nul.md")+")\""+adoO+"\n")
	for _, cmd := range []string{"bash post.sh", "bash " + filepath.Join(dir, "post.sh")} {
		d, why := crius(t, leadSID, dir, cmd)
		t.Logf("%q %q\n  %s", d, why, cmd)
		if d != "deny" {
			t.Errorf("BYPASS: %s", cmd)
		}
	}
}

func TestReviewHomeOverrideUnderPin(t *testing.T) {
	home, _ := quotesHomePRs(t, map[string]prInfo{"7848": {Status: "active", Target: "refs/heads/integration/96365-b1"}})
	dir := t.TempDir()
	lead := filepath.Join(home, ".crius/state/quotes/build/bin/lead")
	if err := os.MkdirAll(lead, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"merge.sh", "review-gate.sh", "merge-ok.sh"} {
		b, err := os.ReadFile(filepath.Join("testdata", "team", n))
		if err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(lead, n), string(b))
		pinBody(t, n, string(b))
	}
	evil := t.TempDir()
	el := filepath.Join(evil, ".crius/state/quotes/build/bin/lead")
	if err := os.MkdirAll(el, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(el, "review-gate.sh"), "#!/bin/bash\naz repos pr update --id 7891 --status completed --org https://dev.azure.com/Appxite\n")
	for _, cmd := range []string{
		"HOME=" + evil + " bash ~/.crius/state/quotes/build/bin/lead/merge.sh 7848 1",
		"export HOME=" + evil + "; bash " + lead + "/merge.sh 7848 1",
	} {
		d, why := crius(t, leadSID, dir, cmd)
		t.Logf("%q %q\n  %s", d, why, cmd)
		if d != "deny" {
			t.Errorf("BYPASS: %s", cmd)
		}
	}
}
