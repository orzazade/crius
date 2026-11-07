package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Review r2 of 34a1ff7: each P1 was a command the guard let through with no ask or deny.

// P1-1, P1-2: az takes an option's last copy in command-line order, and every alias and unambiguous prefix of a long
// option is that option. The guard read the first name it was given (--org before --organization, --target-branch
// before -t), and a prefix of an option it read only by its full name was another option.
func TestAzOptionLastCopyInOrderWinsOverAliasesAndPrefixes(t *testing.T) {
	dir := criusDir(t, map[string]string{"5": "refs/heads/integration/96365-b1"})
	for _, cmd := range []string{
		"az repos pr update --id 5 --status completed --org https://dev.azure.com/Appxite --organization https://dev.azure.com/Other",
		"az repos pr update --id 5 --status completed --org https://dev.azure.com/Appxite --organiz https://dev.azure.com/Other",
		"az repos pr update --id 5 --status completed --organization https://dev.azure.com/Other --org https://dev.azure.com/Appxite --orga https://dev.azure.com/Other",
	} {
		if d, why := crius(t, criusSID, dir, cmd); d != "deny" || !strings.Contains(why, "Appxite") {
			t.Errorf("Crius mode: want deny naming Appxite, got %s %q\n  %s", d, why, cmd)
		}
	}
	// the last copy is Appxite's: the call goes on
	if d, why := crius(t, criusSID, dir, "az repos pr update --id 5 --status completed --org https://dev.azure.com/Other --organization https://dev.azure.com/Appxite"); d != "" {
		t.Errorf("Crius mode: want allow, got %s %q", d, why)
	}
	const pr = "az repos pr create --title x --source-branch feature/96365-x --target-branch integration/96365-b1"
	for _, cmd := range []string{
		pr + " -t develop" + allowOrg,
		pr + " --target develop" + allowOrg,
		pr + " --target-b=develop" + allowOrg,
		pr + " -s main" + allowOrg,
		pr + " --sour main" + allowOrg,
	} {
		// owner 11-08: opening a PR into develop or from main is a post like any other; only completing one into develop
		// or main is refused
		leadCheck(t, dir, cmd, "")
	}
	leadCheck(t, dir, "az repos pr create --title x -s main -t develop --source-branch feature/96365-x --target-b integration/96365-b1"+allowOrg, "")
	// az rest: -m and --method, -u, --uri and --url are one option each
	for _, cmd := range []string{
		"az rest --method get -m patch --url https://dev.azure.com/Appxite/P/_apis/git/repositories/R/pullrequests/9",
		"az rest --url https://example.com --uri https://dev.azure.com/Appxite/P/_apis/git/repositories/R/pullrequests/9 -m patch",
		"az rest --meth get --method patch -u https://dev.azure.com/Appxite/P/_apis/git/repositories/R/pullrequests/9",
	} {
		if why := check(bash(cmd, dir)); !strings.Contains(why, "az rest PATCH") {
			t.Errorf("want an ask for az rest PATCH, got %q\n  %s", why, cmd)
		}
	}
}

// P1-4: bash splits an unquoted $O on IFS, so with IFS set anywhere in the text the guard does not know the words.
func TestAnUnquotedWordIsUnreadableWhenIFSIsSet(t *testing.T) {
	dir := criusDir(t, nil)
	write(t, filepath.Join(dir, "sep.sh"), "IFS=:\n")
	for _, cmd := range []string{
		`IFS=:; O="repos:pr:update:--id:9:--status:completed:--org:https://dev.azure.com/Appxite"; az $O`,
		`O="repos:pr:update:--id:9:--status:completed:--org:https://dev.azure.com/Appxite"; export I\FS=:; az $O`,
		`O="repos:pr:update:--id:9:--status:completed:--org:https://dev.azure.com/Appxite"; declare "I""FS=:"; az $O`,
		`O="repos:pr:update:--id:9:--status:completed:--org:https://dev.azure.com/Appxite"; read -r IFS <<< ":"; az $O`,
		`O="repos:pr:update:--id:9:--status:completed:--org:https://dev.azure.com/Appxite"; declare $'\x49FS=:'; az $O`,
		`source ./sep.sh; O="repos:pr:update:--id:9:--status:completed:--org:https://dev.azure.com/Appxite"; az $O`,
	} {
		if why := check(bash(cmd, dir)); !strings.Contains(why, reason) {
			t.Errorf("want an ask, got %q\n  %s", why, cmd)
		}
		if d, why := crius(t, criusSID, dir, cmd); d != "deny" {
			t.Errorf("Crius mode: want deny, got %s %q\n  %s", d, why, cmd)
		}
	}
	// the default IFS splits as before
	if why := check(bash(`O="--org https://dev.azure.com/Appxite"; az repos pr list $O`, dir)); why != "" {
		t.Errorf("want pass, got %q", why)
	}
}

// P1-7: a nameref (declare, local or typeset -n) or a setter whose name the guard cannot read (printf -v "$n", read
// "$n", declare "$n=…", unset "$n") may change any variable, so none keeps the literal value the guard saw.
func TestANamerefOrAComputedNameMakesVariablesUnknown(t *testing.T) {
	dir := criusDir(t, nil)
	const pr = ` --title t --source-branch feature/96365-x --target-branch integration/96365-b1` + allowOrg
	credit := strings.ReplaceAll(coAuthor, `"`, "")
	for _, cmd := range []string{
		`D="Fix"; declare -n R=D; R="` + credit + `"; az repos pr create --description "$D"` + pr,
		`D="Fix"; typeset -n R=D; R="` + credit + `"; az repos pr create --description "$D"` + pr,
		`D="Fix"; f() { local -n R=D; R="` + credit + `"; }; f; az repos pr create --description "$D"` + pr,
		`D="Fix"; builtin declare -n R=D; R="` + credit + `"; az repos pr create --description "$D"` + pr,
		`D="Fix"; n=D; printf -v "$n" '%s' "` + credit + `"; az repos pr create --description "$D"` + pr,
		`D="Fix"; n=D; read -r "$n" <<< "` + credit + `"; az repos pr create --description "$D"` + pr,
		`D="Fix"; n=D; declare "$n=` + credit + `"; az repos pr create --description "$D"` + pr,
		`D="Fix"; n=D; declare $'D=\x41'; az repos pr create --description "$D"` + pr,
	} {
		if d, why := crius(t, criusSID, dir, cmd); d != "deny" {
			t.Errorf("Crius mode: want deny, got %s %q\n  %s", d, why, cmd)
		}
		if why := check(bash(cmd, dir)); !strings.Contains(why, reason) {
			t.Errorf("lead: want an ask, got %q\n  %s", why, cmd)
		}
	}
	// a plain declare keeps the value
	if d, why := crius(t, criusSID, dir, `declare D="Fix"; az repos pr create --description "$D"`+pr); d != "" {
		t.Errorf("Crius mode: want allow, got %s %q", d, why)
	}
}

// P1-3 (and the P2 test gap): a script named by its full path after a cd runs in the cd's folder, so the relative
// files and scripts it names are that folder's. The guard read them in the start folder.
func TestAScriptRunAfterACdRunsInThatFolder(t *testing.T) {
	dir := criusDir(t, nil)
	moved, bin := t.TempDir(), t.TempDir()
	write(t, filepath.Join(dir, "d.md"), "Clean text.\n")
	write(t, filepath.Join(moved, "d.md"), "Done.\n\n"+coAuthor+"\n")
	write(t, filepath.Join(dir, "inner.sh"), "#!/bin/bash\necho hi\n")
	write(t, filepath.Join(moved, "inner.sh"), "#!/bin/bash\naz repos pr update --id 9 --status completed --org https://dev.azure.com/Appxite\n")
	write(t, filepath.Join(bin, "post.sh"), "#!/bin/bash\naz repos pr create --title t --description @d.md --source-branch feature/96365-x --target-branch integration/96365-b1 --org https://dev.azure.com/Appxite --project P\n")
	write(t, filepath.Join(bin, "outer.sh"), "#!/bin/bash\nbash inner.sh\n")
	for _, cmd := range []string{
		"cd " + moved + " && bash " + bin + "/post.sh",
		"cd " + moved + "; bash " + bin + "/post.sh",
		"cd " + moved + " && bash " + bin + "/outer.sh",
	} {
		if d, why := crius(t, criusSID, dir, cmd); d != "deny" {
			t.Errorf("Crius mode: want deny, got %s %q\n  %s", d, why, cmd)
		}
		if why := check(bash(cmd, dir)); !strings.Contains(why, reason) {
			t.Errorf("lead: want an ask, got %q\n  %s", why, cmd)
		}
	}
	// control: the same script with no cd reads the start folder's clean file
	if d, why := crius(t, criusSID, dir, "bash "+bin+"/post.sh"); d != "" {
		t.Errorf("Crius mode: want allow, got %s %q", d, why)
	}
}

// P1-5: a JSON body's escapes (a, -) and number forms (3e0) hid a completion or AI credit from the hard
// checks, which matched the raw text.
func TestJSONEscapesDoNotHideACompletionOrAICredit(t *testing.T) {
	dir := criusDir(t, nil)
	const pr = "https://dev.azure.com/Appxite/P/_apis/git/repositories/R/pullrequests/9?api-version=7.1"
	u := string(rune(92)) + "u" // a JSON escape, built here so no tool on the way decodes it
	bodies := map[string]string{
		"a.json": `{"st` + u + `0061tus":"completed"}`,
		"b.json": `{"status":"` + u + `0063ompleted"}`,
		"c.json": `{"status":3e0}`,
		"d.json": `{"status":"3"}`,
		// the lenient reader Azure DevOps uses takes single quotes, comments and hex numbers too
		"g.json": `{'st` + u + `0061tus':'completed'}`,
		"h.json": `{"status"/* x */:"completed"}`,
		"i.json": `{"status":0x3}`,
		"e.json": `{"description":"Done. ` + strings.Replace(coAuthor, "-", u+"002d", 1) + `"}`,
		"f.json": `{"description":"Done. ` + strings.Replace(robot, "\U0001F916", u+"d83e"+u+"dd16", 1) + `"}`,
	}
	for n, body := range bodies {
		write(t, filepath.Join(dir, n), body)
		cmds := []string{"curl -X PATCH '" + pr + "' -d @" + n}
		if !strings.Contains(body, "'") {
			cmds = append(cmds, "curl -X PATCH '"+pr+"' -d '"+body+"'") // the body in the words, as the shell passes it
		}
		for _, cmd := range cmds {
			if d, why := crius(t, criusSID, dir, cmd); d != "deny" {
				t.Errorf("Crius mode: want deny, got %s %q\n  %s", d, why, cmd)
			}
		}
	}
	// control: a clean body goes on
	write(t, filepath.Join(dir, "ok.json"), `{"title":"Partner quote: regenerated migration"}`)
	if d, why := crius(t, criusSID, dir, "curl -X PATCH '"+pr+"' -d @ok.json"); d != "" {
		t.Errorf("Crius mode: want allow, got %s %q", d, why)
	}
}

// P1-6: a pinned team script passed unread, so the scripts it runs passed too, pinned or not, there or not (merge.sh
// runs review-gate.sh and merge-ok.sh, since 11-07 from ~/.crius/state/quotes/build/bin/lead/). Its own posts still pass
// on the pin; what it runs is read as usual or passes on its own pin.
func TestAPinnedScriptsNestedScriptsAreReadOrPinned(t *testing.T) {
	home, _ := quotesHomePRs(t, map[string]prInfo{"7848": {Status: "active", Target: "refs/heads/integration/96365-b1"}})
	dir := t.TempDir()
	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join("testdata", "team", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
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
	merge, gate, ok := filepath.Join(lead, "merge.sh"), filepath.Join(lead, "review-gate.sh"), filepath.Join(lead, "merge-ok.sh")
	install(merge, read("merge.sh"), true)
	const cmd = "bash ~/.crius/state/quotes/build/bin/lead/merge.sh 7848 1"
	// the scripts it runs are not there: nothing runs (bash fails, merge.sh stops with rc 10); the pin vouches for where
	// merge.sh's text runs a file the guard does not find, and for nothing else of the files it runs (review of 07f23bd P1-2)
	leadCheck(t, dir, cmd, "")
	// a nested script that posts with a word the guard cannot read, and no pin covers it
	readOnly := "#!/bin/bash\naz repos pr show --id \"$1\" --org https://dev.azure.com/Appxite -o none\n"
	install(gate, "#!/bin/bash\naz repos pr update --id \"$1\" --title x --org https://dev.azure.com/Appxite\n", false)
	install(ok, readOnly, true)
	leadCheck(t, dir, cmd, "cannot read")
	// the shipped merge-ok.sh runs scripts/heavy.ts in a folder the guard does not follow, and the PR's own tests
	install(gate, readOnly, true)
	install(ok, read("merge-ok.sh"), false)
	leadCheck(t, dir, cmd, "merge-ok.sh")
	// the shipped scripts, pinned, and postmerge-scan.sh (read: it only lists PRs and runs merge-ok.sh): merge.sh passes
	install(gate, read("review-gate.sh"), true)
	install(ok, read("merge-ok.sh"), true)
	install(filepath.Join(lead, "postmerge-scan.sh"), read("postmerge-scan.sh"), false)
	leadCheck(t, dir, cmd, "")
}

// P1-8: python -m azure.cli is az; the guard read no code after -m.
func TestPythonMinusMAzureCLIIsAz(t *testing.T) {
	dir := criusDir(t, nil)
	for _, cmd := range []string{
		"python3 -m azure.cli repos pr update --id 9 --status completed --org https://dev.azure.com/Appxite",
		"python -m azure.cli.__main__ repos pr create --title t --org https://dev.azure.com/Appxite",
		"python3 -I -mazure.cli repos pr update --id 9 --title t --org https://dev.azure.com/Appxite",
		"/usr/bin/python3.12 -B -m azure.cli repos pr update --id 9 --title t --org https://dev.azure.com/Appxite",
	} {
		if why := check(bash(cmd, dir)); !strings.Contains(why, "(az repos pr") {
			t.Errorf("want an ask for az repos pr, got %q\n  %s", why, cmd)
		}
	}
	// another module still runs no az
	if why := check(bash("python3 -m json.tool x.json", dir)); why != "" {
		t.Errorf("want pass, got %q", why)
	}
}

// P1-9: bun run, npm, pnpm and yarn run package.json scripts, and bun loads the preloads its bunfig.toml (or a flag)
// names; the guard read none of them.
func TestPackageScriptsAndPreloadsAreRead(t *testing.T) {
	dir := criusDir(t, nil)
	const pr = "az repos pr create --title t --org https://dev.azure.com/Appxite"
	write(t, filepath.Join(dir, "package.json"), `{"name":"x","scripts":{"pr":"`+pr+`","prebuild":"`+pr+`","build":"tsc -b","test":"`+pr+`","lint":"eslint .","credit":"`+pr+` --description @d.md"}}`)
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	post := "await fetch('https://dev.azure.com/Appxite/P/_apis/git/repositories/R/pullRequests/9/threads?api-version=7.1', { method: 'POST', body: '{}' });\n"
	write(t, filepath.Join(dir, "p.ts"), post)
	write(t, filepath.Join(dir, "x.ts"), "console.log(1);\n")
	pre := t.TempDir()
	write(t, filepath.Join(pre, "bunfig.toml"), "[test]\npreload = [\n  \"./p.ts\",\n]\n")
	write(t, filepath.Join(pre, "p.ts"), post)
	write(t, filepath.Join(pre, "x.ts"), "console.log(1);\n")
	for _, c := range []struct{ cwd, cmd string }{
		{dir, "bun run pr"},
		{dir, "bun pr"},
		{dir, "npm run pr"},
		{dir, "npm run-script pr"},
		{dir, "npm test"},
		{dir, "npm run build"},
		{dir, "pnpm run pr"},
		{dir, "pnpm pr"},
		{dir, "yarn run pr"},
		{dir, "yarn pr"},
		{filepath.Join(dir, "sub"), "npm run pr"}, // npm finds the package.json above
		{t.TempDir(), "npm --prefix " + dir + " run pr"},
		{t.TempDir(), "cd " + dir + " && bun run pr"},
		{pre, "bun x.ts"},
		{pre, "bun test"},
		{dir, "bun --preload ./p.ts x.ts"},
		{dir, "node -r ./p.ts x.ts"},
	} {
		if why := check(bash(c.cmd, c.cwd)); !strings.Contains(why, reason) {
			t.Errorf("want an ask, got %q\n  %s (in %s)", why, c.cmd, c.cwd)
		}
	}
	// Crius mode: a script's post gets the hard checks, and code that posts denies
	write(t, filepath.Join(dir, "d.md"), "Done.\n\n"+coAuthor+"\n")
	for _, c := range []struct{ cwd, cmd string }{
		{dir, "bun run credit"},
		{dir, "npm run credit"},
		{pre, "bun x.ts"},
		{dir, "bun --preload ./p.ts x.ts"},
	} {
		if d, why := crius(t, criusSID, c.cwd, c.cmd); d != "deny" {
			t.Errorf("Crius mode: want deny, got %s %q\n  %s (in %s)", d, why, c.cmd, c.cwd)
		}
	}
	// a script that only builds, and bun with no bunfig, still pass
	for _, cmd := range []string{"bun run lint", "npm run lint", "bun x.ts"} {
		if why := check(bash(cmd, dir)); why != "" {
			t.Errorf("want pass, got %q\n  %s", why, cmd)
		}
	}
}

// P2: a program the guard does not know to leave files as they are (make) may write the body file before the post
// sends it; the guard read the file as it is now. Such a program before a post with a body file denies in Crius mode
// and asks for a lead.
func TestAnUnknownProgramBeforeAPostMayWriteItsBody(t *testing.T) {
	dir := criusDir(t, nil)
	write(t, filepath.Join(dir, "d.md"), "Clean text.\n")
	write(t, filepath.Join(dir, "gen.sh"), "#!/bin/bash\nmake docs\n")
	const pr = "az repos pr create --title t --description @d.md --source-branch feature/96365-x --target-branch integration/96365-b1 --org https://dev.azure.com/Appxite --project P"
	for _, cmd := range []string{
		"make docs && " + pr,
		"make docs; " + pr,
		"python3 gen.py && " + pr,
		"$M docs && " + pr,
		"bash gen.sh && " + pr, // the guard reads gen.sh for posts, not for what it writes
		"printf 'all:\\n' | make -f - && " + pr,
	} {
		if d, why := crius(t, criusSID, dir, cmd); d != "deny" {
			t.Errorf("Crius mode: want deny, got %s %q\n  %s", d, why, cmd)
		}
		if why := check(bash(cmd, dir)); !strings.Contains(why, reason) {
			t.Errorf("lead: want an ask, got %q\n  %s", why, cmd)
		}
	}
	// a command that leaves files as they are, or one after the post, still passes
	for _, cmd := range []string{
		"echo go && " + pr,
		"cat d.md && " + pr,
		pr + " && make docs",
	} {
		if d, why := crius(t, criusSID, dir, cmd); d != "" {
			t.Errorf("Crius mode: want allow, got %s %q\n  %s", d, why, cmd)
		}
	}
}
