package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// repo makes a folder with code and config files and returns its path.
func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range []string{"src/Api.cs", "src/appsettings.Test.json", "Web.config", "main.go", ".env.local"} {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func bash(cmd, cwd string) input {
	in := input{ToolName: "Bash", Cwd: cwd}
	in.ToolInput.Command = cmd
	return in
}

// 10-28: an Explore subagent in RethinkBack printed two committed test secrets with these two shapes: a recursive grep
// whose config lines were filtered only after grep had read and printed them, and a recursive grep with folder excludes
// only. 10-23: a cortex helper's git grep read Web.config and App.config.
func TestPastConfigLeakingSearchesAreRefused(t *testing.T) {
	dir := repo(t)
	for _, cmd := range []string{
		`grep -rn -i -E "ASPNETCORE_ENVIRONMENT|swagger|environmentName" --include='*.yml' --include='*.json' . 2>/dev/null | grep -v -E "/bin/|/obj/|node_modules|appsettings|\.env|secret|\.config" | cut -c1-220 | head -80`,
		`cd ` + dir + ` && timeout 100 grep -rIn -E "uiconfig|uisettings" --exclude-dir=node_modules --exclude-dir=bin --exclude-dir=obj --exclude-dir=.git . 2>/dev/null | cut -c1-220 | head -40`,
		`git grep -n -i 'connectionString' -- '*.config'`,
	} {
		if why := check(bash(cmd, dir)); !strings.Contains(why, "config files") {
			t.Errorf("%q: %q", cmd, why)
		}
	}
}

func TestSearchesThatCanReadConfigFilesAreRefused(t *testing.T) {
	dir := repo(t)
	for _, cmd := range []string{
		"rg -n password",
		"rg -n password src",
		"rg -n -g '!*appsettings*' -g '!*.env*' -g '!*secrets*' password", // *.config is missing
		"grep -R -n password src",
		"grep -n password *",                        // a shell glob may name a config file
		"grep -n password Web.config",               // a named config file
		"rg password src/appsettings.Test.json",     // a named config file
		"git grep -n password",                      // the whole tree
		"git -C " + dir + " --no-pager grep -n key", // git options before grep
		"git grep -n password HEAD -- src",
		"find . -name '*.json' | xargs grep -n password",
		"find . -type f -exec grep -n password {} +",
		`bash -c "grep -rn password ."`,
		"eval grep -rn password .",
		"/usr/bin/grep -rn password .",
		`rg -n password "$DIR"`,
		"egrep -r 'a|b' .",
		"grep --recursive -n password",
		"grep -d recurse -n password .",
		"grep -n --include='*.json' -r password .", // appsettings.Test.json is a *.json
		"rg -n -tcs password",                      // a type is not an exclude list
	} {
		if why := check(bash(cmd, dir)); !strings.Contains(why, "config files") {
			t.Errorf("%q: %q", cmd, why)
		}
	}
}

func TestSearchesWithTheExcludesPass(t *testing.T) {
	dir := repo(t)
	for _, cmd := range []string{
		`git grep -n -i 'connectionString' -- . ':!*appsettings*' ':!*.env*' ':!*secrets*' ':!*.config'`,
		`git grep -n key -- ':^*appsettings*' ':(exclude)*.env*' ':(exclude,icase)*secrets*' ':!*.config'`,
		`rg -n password -g '!*appsettings*' -g '!*.env*' -g '!*secrets*' -g '!*.config'`,
		`rg -n password --glob='!*appsettings*' --glob '!*.env*' --iglob '!*secrets*' -g'!*.config' src`,
		`rg -n password -g '!{*appsettings*,*.env*,*secrets*,*.config}'`,
		`grep -rn password --exclude='*appsettings*' --exclude='*.env*' --exclude '*secrets*' --exclude=*.config .`,
		`cd ` + dir + ` && timeout 100 grep -rIn uiconfig --exclude-dir=node_modules --exclude='*appsettings*' --exclude='*.env*' --exclude='*secrets*' --exclude='*.config' . | head`,
	} {
		if why := check(bash(cmd, dir)); why != "" {
			t.Errorf("%q refused: %s", cmd, why)
		}
	}
}

func TestSearchesOfNamedFilesOrNamesOnlyPass(t *testing.T) {
	dir := repo(t)
	for _, cmd := range []string{
		"grep -n password src/Api.cs",
		"rg -n password main.go src/Api.cs",
		"grep -rn password src/Api.cs",
		"git grep -n password -- main.go",
		"cd src && rg -n password Api.cs",
		"grep -n 'getConfigurationFile' src/Api.cs | cut -c1-200",
		// names only: no file content reaches the transcript
		"grep -rIl uiconfig .",
		"rg -l password",
		"rg --files",
		"git grep -l -i connectionString -- '*.config'",
		"grep -rc password .",
		// not a file search
		"ps aux | grep crius",
		"git log --oneline | grep fix",
		"echo grep -rn password .",
		"grep -n x", // reads stdin
	} {
		if why := check(bash(cmd, dir)); why != "" {
			t.Errorf("%q refused: %s", cmd, why)
		}
	}
}

func grepTool(cwd string, fields map[string]string) input {
	in := input{ToolName: "Grep", Cwd: cwd}
	in.ToolInput.Pattern = "password"
	in.ToolInput.Path = fields["path"]
	in.ToolInput.Glob = fields["glob"]
	in.ToolInput.OutputMode = fields["output_mode"]
	return in
}

func TestTheGrepToolNeedsTheExcludesForContent(t *testing.T) {
	dir := repo(t)
	for _, f := range []map[string]string{
		{"output_mode": "content"},
		{"output_mode": "content", "path": filepath.Join(dir, "src")},
		{"output_mode": "content", "glob": "*.json"},
		{"output_mode": "content", "glob": "!*appsettings* !*.env* !*secrets*"},
		{"output_mode": "content", "path": filepath.Join(dir, "Web.config")},
	} {
		if why := check(grepTool(dir, f)); !strings.Contains(why, "config files") {
			t.Errorf("%v: %q", f, why)
		}
	}
	for _, f := range []map[string]string{
		{"output_mode": "content", "glob": "!*appsettings* !*.env* !*secrets* !*.config"},
		{"output_mode": "content", "glob": "*.cs,!*appsettings*,!*.env*,!*secrets*,!*.config"},
		{"output_mode": "content", "glob": "!{*appsettings*,*.env*,*secrets*,*.config}"},
		{"output_mode": "content", "path": filepath.Join(dir, "src/Api.cs")},
		{"output_mode": "content", "path": "main.go"},
		{}, // files_with_matches is the default
		{"output_mode": "count"},
	} {
		if why := check(grepTool(dir, f)); why != "" {
			t.Errorf("%v refused: %s", f, why)
		}
	}
}

func TestOtherToolsPass(t *testing.T) {
	if why := check(input{ToolName: "Read"}); why != "" {
		t.Error(why)
	}
}

// The guard lists what a search reads: a folder with no config file in it needs no excludes.
func TestAFolderWithoutConfigFilesPasses(t *testing.T) {
	dir := repo(t)
	for _, cmd := range []string{
		"grep -rn password --include='*.cs' .", // the one .cs file is not a config file
		"rg -n password -g '*.go'",
		"rg -n password -g '!*appsettings*' src",
		"grep -n password *.go src/*.cs", // the globs name no config file
		"cd src && rg -n -g '!*appsettings*' password",
		`grep -n password "$F"`, // a non-recursive grep of one file
		"cd " + filepath.Join(dir, "src") + " && grep -rn Api --exclude='*appsettings*' .",
	} {
		if why := check(bash(cmd, dir)); why != "" {
			t.Errorf("%q refused: %s", cmd, why)
		}
	}
	// A search of the folder the command moved to does not list the folder the session is in.
	clean := t.TempDir()
	if err := os.WriteFile(filepath.Join(clean, "a.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if why := check(bash("cd "+clean+" && rg -n password", dir)); why != "" {
		t.Errorf("cd to a clean folder: refused: %s", why)
	}
	if why := check(bash("rg -n password", clean)); why != "" {
		t.Errorf("rg in a clean folder: refused: %s", why)
	}
}

func TestWhatTheGuardCannotListIsRefused(t *testing.T) {
	dir := repo(t)
	clean := t.TempDir()
	for _, cmd := range []string{
		"cd $REPO && rg -n password",                 // a folder only the run shows
		"(cd " + clean + "); rg -n password",         // the subshell's cd does not move the next command
		"cd " + clean + " || cd src; rg -n password", // a cd after || may not run
		"grep -n password $DIR/*",
		"git grep -n password origin/main", // no git repository here: the revision cannot be listed
		"git grep -n password -- ':/'",     // the top of the repository
	} {
		if why := check(bash(cmd, dir)); !strings.Contains(why, "config file") {
			t.Errorf("%q: %q", cmd, why)
		}
	}
	defer func(n int) { maxEntries = n }(maxEntries)
	maxEntries = 3
	for _, f := range []string{"a.go", "b.go", "c.go", "d.go"} {
		if err := os.WriteFile(filepath.Join(clean, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if why := check(bash("rg -n password -g '!*.env*'", clean)); !strings.Contains(why, "too big to list") {
		t.Errorf("a small budget: %q", why)
	}
}

// gitRepo makes a git work tree: a tracked code file, an ignored node_modules with a config-named file in it, and
// the files in extra, untracked.
func gitRepo(t *testing.T, extra ...string) string {
	t.Helper()
	dir := t.TempDir()
	write := func(f string) {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"main.go", ".gitignore", "node_modules/x/secrets.mdx"} {
		write(f)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("node_modules/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "main.go", ".gitignore"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	for _, f := range extra {
		write(f)
	}
	return dir
}

// rg skips what git ignores and git grep reads only tracked files, so a config-named file in an ignored
// node_modules is no reason to refuse them; grep -r reads it.
func TestTheGuardListsAFolderTheWayTheToolReadsIt(t *testing.T) {
	dir := gitRepo(t)
	for _, cmd := range []string{"rg -n password", "git grep -n password", "git grep -n password -- ."} {
		if why := check(bash(cmd, dir)); why != "" {
			t.Errorf("%q refused: %s", cmd, why)
		}
	}
	if why := check(grepTool(dir, map[string]string{"output_mode": "content"})); why != "" {
		t.Errorf("Grep tool refused: %s", why)
	}
	for _, cmd := range []string{"grep -rn password .", "rg -n -u password", "rg -n --no-ignore password",
		"git grep --no-index -n password"} {
		if why := check(bash(cmd, dir)); !strings.Contains(why, "secrets.mdx") {
			t.Errorf("%q: %q", cmd, why)
		}
	}
	// An untracked file git does not ignore: rg reads it, git grep does not (unless --untracked).
	dir = gitRepo(t, "appsettings.json")
	for _, cmd := range []string{"rg -n password", "git grep --untracked -n password"} {
		if why := check(bash(cmd, dir)); !strings.Contains(why, "appsettings.json") {
			t.Errorf("%q: %q", cmd, why)
		}
	}
	if why := check(bash("git grep -n password", dir)); why != "" {
		t.Errorf("git grep of tracked files refused: %s", why)
	}
	// A tracked config file.
	if out, err := exec.Command("git", "-C", dir, "add", "appsettings.json").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if why := check(bash("git grep -n password", dir)); !strings.Contains(why, "appsettings.json") {
		t.Errorf("git grep with a tracked config file: %q", why)
	}
	// The refusal quotes a path only the run shows as written.
	if why := check(bash(`git grep -n x "${R}" -- '*.cs'`, dir)); !strings.Contains(why, `"${R}"`) {
		t.Errorf("revision text: %q", why)
	}
}

// git grep of a revision reads the files in that revision: the guard lists them with git ls-tree.
func TestGitGrepOfARevisionListsThatRevision(t *testing.T) {
	dir := gitRepo(t)
	commit := func(msg string) {
		if out, err := exec.Command("git", "-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", msg).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	commit("clean")
	if out, err := exec.Command("git", "-C", dir, "tag", "clean").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(dir, "Web.config"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", dir, "add", "Web.config").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	commit("config")
	if err := os.Remove(filepath.Join(dir, "Web.config")); err != nil { // gone from disk, still in HEAD
		t.Fatal(err)
	}
	for _, cmd := range []string{"git grep -n password HEAD", "git grep -n password HEAD -- .", "git grep -n x clean HEAD"} {
		if why := check(bash(cmd, dir)); !strings.Contains(why, "HEAD:Web.config") {
			t.Errorf("%q: %q", cmd, why)
		}
	}
	for _, cmd := range []string{"git grep -n password clean", "git grep -n password clean -- main.go",
		"git grep -n password HEAD -- . ':!*.config'"} {
		if why := check(bash(cmd, dir)); why != "" {
			t.Errorf("%q refused: %s", cmd, why)
		}
	}
	if why := check(bash("git grep -n password nosuchrev", dir)); !strings.Contains(why, "cannot list revision nosuchrev") {
		t.Errorf("unknown revision: %q", why)
	}
}

// Review round 1 (parsing): shapes that passed while the search read Web.config.
func TestReviewRound1ParsingBypassesAreRefused(t *testing.T) {
	dir := gitRepo(t, "Web.config")
	if out, err := exec.Command("git", "-C", dir, "add", "Web.config").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if err := os.Mkdir(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{
		"cd -- . && rg needle .",            // a cd option is no folder
		"cd -P src && cd .. && rg needle .", // -P too
		"false && cd src; rg needle .",      // a cd that may not run
		"cd src || true; rg needle",
		"timeout 10 bash -c 'rg needle .'", // a wrapper around a shell
		"xargs -I{} sh -c 'grep -rn needle .'",
		"printf 'needle\\n' | rg -f -",   // patterns from stdin: rg searches the folder
		"cd src && git -C.. grep needle", // git -C attached
		"git --work-tree=" + dir + " grep needle",
		"git --git-dir=" + dir + "/.git grep needle",
		"rg -l --json needle Web.config", // --json prints matches after -l
		"rg -c --vimgrep needle",
		"rg $FLAGS needle", // $FLAGS may hold options: needle may be the pattern
		"grep -rn needle $(git ls-files)",
	} {
		if why := check(bash(cmd, dir)); !strings.Contains(why, "config file") {
			t.Errorf("%q: %q", cmd, why)
		}
	}
	for _, cmd := range []string{
		"cd src && rg needle", // a cd that surely runs to a folder that exists
		"cd src && cd . && rg needle",
		`rg -n "$PAT" src`,           // a quoted $PAT is one word
		"cat Web.config | rg needle", // rg reads stdin
		"grep -n needle $FILES",      // a non-recursive grep reads named files
	} {
		if why := check(bash(cmd, dir)); why != "" {
			t.Errorf("%q refused: %s", cmd, why)
		}
	}
}

// Review round 1 (listing): the filter follows each tool's rules for case, braces, order and named files.
func TestReviewRound1FilterBypassesAreRefused(t *testing.T) {
	dir := repo(t) // src/appsettings.Test.json, src/Api.cs, Web.config, main.go, .env.local
	for _, cmd := range []string{
		"rg --no-ignore -g '!*.JSON' -g '!*.CONFIG' -g '!*.env*' x .",       // rg -g matches case
		"grep -r --exclude='{*appsettings*,*.env*,*secrets*,*.config}' x .", // grep takes no braces
		"git grep x -- ':!{*appsettings*,*.env*,*secrets*,*.config}'",       // nor git
		"rg -g '*.cs' x src/appsettings.Test.json",                          // rg reads a named file
		"rg -g '!*appsettings*' -g '!*.env*' -g '!*secrets*' -g '!*.config' x Web.config",
		"rg --no-ignore -g 'src/*.json' x src",                                                                  // a glob with a / matches paths
		"rg --no-ignore -g '!*appsettings*' -g '!*.env*' -g '!*secrets*' -g '!*.config' -g '*appsettings*' x .", // the last glob wins
		"grep -r --exclude='*appsettings*' --exclude='*.env*' --exclude='*secrets*' --exclude='*.config' --include='*.config' x .",
	} {
		if why := check(bash(cmd, dir)); !strings.Contains(why, "config file") {
			t.Errorf("%q: %q", cmd, why)
		}
	}
	for _, f := range []map[string]string{
		{"output_mode": "content", "path": "src/appsettings.Test.json", "glob": "*.cs"},
		{"output_mode": "content", "path": "src", "glob": "src/*.json"},
	} {
		if why := check(grepTool(dir, f)); !strings.Contains(why, "config file") {
			t.Errorf("Grep %v: %q", f, why)
		}
	}
	for _, cmd := range []string{
		"rg --no-ignore --iglob '!*.JSON' --iglob '!*.CONFIG' --iglob '!*.ENV*' x .", // --iglob ignores case
		"rg --no-ignore --glob-case-insensitive -g '!*.JSON' -g '!*.CONFIG' -g '!*.ENV*' x .",
		"rg --no-ignore -g '!{*.json,*.config,*.env*}' x .", // rg takes braces
		"git grep x -- ':(exclude,icase)*APPSETTINGS*' ':!*.env*' ':!*secrets*' ':!*.config'",
		"rg --no-ignore -g '*appsettings*' -g '!*appsettings*' -g '!*.env*' -g '!*secrets*' -g '!*.config' x .",
	} {
		if why := check(bash(cmd, dir)); why != "" {
			t.Errorf("%q refused: %s", cmd, why)
		}
	}
}

// Review round 2: shapes that passed while the search read Web.config.
func TestReviewRound2BypassesAreRefused(t *testing.T) {
	dir := repo(t)
	for _, cmd := range []string{
		"cd --bad-option src; rg . Web.config",
		"cd --bad-option src && cd .. ; rg needle",
		"printf '%s\\n' Web.config | xargs -I{} bash -c 'rg . {}'",
		`bash -c 'x=". Web.config"; rg -e $x main.go'`,
		`d=src; git -C"$d" grep --no-index .`,
		"rg -u --iglob '*.config' -g '!*.config' . .",
		"rg -u -g '*.config' -g '!*.CONFIG' --glob-case-insensitive --no-glob-case-insensitive . .",
		"find . -type f -exec git grep --no-index token -- {} +", // round 3
		"git ls-files | xargs git grep -n token --",
	} {
		if why := check(bash(cmd, dir)); !strings.Contains(why, "config file") {
			t.Errorf("%q: %q", cmd, why)
		}
	}
	for _, cmd := range []string{
		"cd -P src && rg needle Api.cs",
		"rg -u -g '!*.config' --iglob '!*.CONFIG' -g '!*.env*' -g '!*appsettings*' needle .",
	} {
		if why := check(bash(cmd, dir)); why != "" {
			t.Errorf("%q refused: %s", cmd, why)
		}
	}
}
