package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// exactHome lays out a home with ado-suite.ts (a POST to ADO in it, as the real one has), the empty bunfig and a
// worktree, pins both hashes, and makes a session scratchpad. It gives the home, the bunfig path and the scratchpad.
func exactHome(t *testing.T) (home, bunfig, sp string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home = filepath.Join(root, "home")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("BUN_OPTIONS", "")
	bin := filepath.Join(home, ".crius/state/quotes/build/bin")
	sp = filepath.Join(root, "claude-501", "-Users-x-Projects-Appxite-svc-quote-channels", "sid-1", "scratchpad")
	for _, d := range []string{bin, sp, filepath.Join(home, ".crius/state/quotes/build/notes"), filepath.Join(home, ".crius/state/quotes/build/qa")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	suite := "const r = await fetch('https://dev.azure.com/Appxite/P/_apis/pipelines/1119/runs?api-version=7.1', { method: 'POST', body: '{}' });\n"
	bunfig = filepath.Join(bin, "empty-bunfig.toml")
	for p, body := range map[string]string{filepath.Join(bin, "ado-suite.ts"): suite, bunfig: "# empty\n"} {
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pinBody(t, "ado-suite.ts", suite)
	pinBody(t, "empty-bunfig.toml", "# empty\n")
	oldRoot := scratchRoot
	scratchRoot = func() string { return filepath.Join(root, "claude-501") }
	t.Cleanup(func() { scratchRoot = oldRoot })
	linkedWT(t, home, "core-2", "feature/96365-partner-quote-detail")
	linkedWT(t, home, "core-3", "wip/x.y_z-1")
	return home, bunfig, sp
}

// linkedWT makes wt/<name> as git worktree add does: a .git file naming svc_quote's .git/worktrees/<name>, which holds
// commondir (../..) and HEAD on branch.
func linkedWT(t *testing.T, home, name, branch string) {
	t.Helper()
	common := filepath.Join(home, "Projects/Appxite/svc_quote/svc_quote/.git")
	gitdir := filepath.Join(common, "worktrees", name)
	wt := filepath.Join(home, "Projects/Appxite/svc_quote/wt", name)
	for _, d := range []string{gitdir, wt, filepath.Join(common, "objects")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(wt, ".git"), "gitdir: "+gitdir+"\n")
	write(t, filepath.Join(gitdir, "commondir"), "../..\n")
	write(t, filepath.Join(gitdir, "HEAD"), "ref: refs/heads/"+branch+"\n")
}

func exactIn(cmd string) input {
	in := bash(cmd, "/Users/x/Projects/Appxite/svc_quote/channels")
	in.SessionID = "sid-1"
	return in
}

// The exact shapes pass: report <digits> and run <worktree> <feature|wip branch>, with the pinned bunfig and flags, and
// only the redirects, echo and filters the design names.
func TestADOSuiteExactShapesPass(t *testing.T) {
	home, bunfig, sp := exactHome(t)
	pre := "bun --no-env-file --config=" + bunfig + " ~/.crius/state/quotes/build/bin/ado-suite.ts"
	abs := "bun --no-env-file --config=" + bunfig + " " + home + "/.crius/state/quotes/build/bin/ado-suite.ts"
	run := " run ~/Projects/Appxite/svc_quote/wt/core-2 feature/96365-partner-quote-detail"
	for _, cmd := range []string{
		pre + " report 126862",
		abs + " report 126862",
		pre + " report 126862 2>&1 | cut -c1-160",
		pre + " report 126862 2>&1 | tail -10 | LC_ALL=C cut -c1-220",
		pre + " report 126863 > " + home + "/.crius/state/quotes/build/notes/ado-126863.out 2>&1; echo rc=$?",
		pre + " report 126863 > ~/.crius/state/quotes/build/notes/ado-126863.out 2>&1",
		pre + run,
		pre + run + " 2>&1 | tail -5",
		pre + run + " > " + sp + "/ado-suite.log 2>&1; echo ado_rc=$?",
		pre + run + " >> " + sp + "/ado-suite.log 2>&1; echo \"rc=$?\"",
		pre + " run " + home + "/Projects/Appxite/svc_quote/wt/core-3 wip/x.y_z-1",
		pre + " report 126863 > ~/.crius/state/quotes/build/qa/ado-126863.out 2>&1",
		pre + run + " 2>&1 | head -40",
	} {
		if why := check(exactIn(cmd)); why != "" {
			t.Errorf("want pass, asked: %s\n  %s", why, cmd)
		}
	}
}

// Round 3's P1 bypasses, and every other step off the exact shape, ask.
func TestADOSuiteOffShapeAsks(t *testing.T) {
	home, bunfig, sp := exactHome(t)
	pre := "bun --no-env-file --config=" + bunfig + " ~/.crius/state/quotes/build/bin/ado-suite.ts"
	rep := pre + " report 126862"
	run := pre + " run ~/Projects/Appxite/svc_quote/wt/core-2 feature/x"
	wtGit := home + "/Projects/Appxite/svc_quote/wt/core-2/.git"
	for _, cmd := range []string{
		// P1-1: a bunfig.toml, .env or ~/.bunfig.toml written earlier in the same command
		`printf 'preload = ["/tmp/p.ts"]\n' > bunfig.toml; ` + rep,
		`printf 'preload = ["/tmp/p.ts"]\n' > bunfig.toml && ` + rep,
		`echo X=1 > .env; ` + rep,
		`printf 'preload = ["/tmp/p.ts"]\n' > ~/.bunfig.toml; ` + rep,
		`cd /tmp && ` + rep,
		// P1-2: a function, zsh functions[], hash or alias redefines bun
		`bun() { /tmp/evil "$@"; }; ` + rep,
		`function bun { /tmp/evil; }; ` + rep,
		`functions[bun]='/tmp/evil'; ` + rep,
		`hash -p /tmp/evil bun; ` + rep,
		`alias bun=/tmp/evil; ` + rep,
		`enable -n echo; ` + rep,
		// P1-4: a hardlink made in the same command lets a redirect write into .git
		`ln ` + wtGit + `/config ` + sp + `/out.log; ` + rep + ` > ` + sp + `/out.log`,
		`ln -s ` + wtGit + `/config ` + sp + `/out.log && ` + rep + ` > ` + sp + `/out.log`,
		// run beside anything that may move HEAD, write git config or redefine bun
		`git -C ~/Projects/Appxite/svc_quote/wt/core-2 commit --amend --no-edit; ` + run,
		`git -C ~/Projects/Appxite/svc_quote/wt/core-2 config core.fsmonitor /tmp/x && ` + run,
		`bun() { /tmp/evil "$@"; }; ` + run,
		run + `; git push`,
		// env, wrappers, substitutions, groups
		`X=1 ` + rep,
		`LC_ALL=C ` + rep,
		`BUN_OPTIONS=--preload=/tmp/p.ts ` + rep,
		`env ` + rep,
		`command ` + rep,
		`exec ` + rep,
		`timeout 60 ` + rep,
		`(` + rep + `)`,
		`{ ` + rep + `; }`,
		rep + ` &`,
		`! ` + rep,
		`echo $(` + rep + `)`,
		"echo `" + rep + "`",
		pre + " report $(echo 1)",
		pre + " report `id -u`",
		pre + ` report "126862"`,
		pre + ` report 1\2`,
		"S=" + sp + "; " + rep + " > $S/x.log",
		// flags: the space forms run the bunfig as the script and still read ./bunfig.toml (bun 1.3.10)
		"bun --no-env-file -c " + bunfig + " ~/.crius/state/quotes/build/bin/ado-suite.ts report 1",
		"bun --no-env-file --config " + bunfig + " ~/.crius/state/quotes/build/bin/ado-suite.ts report 1",
		"bun --no-env-file -c=" + bunfig + " ~/.crius/state/quotes/build/bin/ado-suite.ts report 1",
		"bun --config=" + bunfig + " ~/.crius/state/quotes/build/bin/ado-suite.ts report 1",
		"bun --no-env-file ~/.crius/state/quotes/build/bin/ado-suite.ts report 1",
		"bun ~/.crius/state/quotes/build/bin/ado-suite.ts report 1",
		"bun --no-env-file --config=" + bunfig + " --preload=/tmp/p.ts ~/.crius/state/quotes/build/bin/ado-suite.ts report 1",
		"bun --no-env-file --config=/tmp/other.toml ~/.crius/state/quotes/build/bin/ado-suite.ts report 1",
		"bun --no-env-file --config=~/.crius/state/quotes/build/bin/empty-bunfig.toml ~/.crius/state/quotes/build/bin/ado-suite.ts report 1",
		"bun --no-env-file --config=" + bunfig + " ./ado-suite.ts report 1",
		"bun --no-env-file --config=" + bunfig + " ~/.crius/state/quotes/build/bin/../bin/ado-suite.ts report 1",
		"/usr/local/bin/bun --no-env-file --config=" + bunfig + " ~/.crius/state/quotes/build/bin/ado-suite.ts report 1",
		"bunx --no-env-file --config=" + bunfig + " ~/.crius/state/quotes/build/bin/ado-suite.ts report 1",
		// arguments
		pre + " report",
		pre + " report 12a",
		pre + " report 1 2",
		pre + " report -1",
		pre,
		pre + " run ~/Projects/Appxite/svc_quote/wt/core-2",
		pre + " run ~/Projects/Appxite/svc_quote/wt/core-2 main",
		pre + " run ~/Projects/Appxite/svc_quote/wt/core-2 develop",
		pre + " run ~/Projects/Appxite/svc_quote/wt/core-2 fix/x",
		pre + " run ~/Projects/Appxite/svc_quote/wt/core-2 feature/a..b",
		pre + " run ~/Projects/Appxite/svc_quote/wt/core-2 feature/{a,b}",
		pre + " run ~/Projects/Appxite/svc_quote/wt/core-2 feature/x extra",
		pre + " run ~/Projects/Appxite/svc_quote/wt/../wt/core-2 feature/x",
		pre + " run ~/Projects/Appxite/svc_quote/wt/core-2/ feature/x",
		pre + " run ~/Projects/Appxite/svc_quote/wt/core-2/src feature/x",
		pre + " run ~/Projects/Appxite/svc_quote/wt/missing feature/x",
		pre + " run /tmp/core-2 feature/x",
		pre + " run core-2 feature/x",
		pre + " push ~/Projects/Appxite/svc_quote/wt/core-2 feature/x",
		// what may follow
		rep + "; git push",
		rep + " && git push origin HEAD",
		rep + "; echo rc=$?; git push",
		rep + "; echo rc=$? > /tmp/x",
		rep + "; echo hi",
		rep + "; echo rc=$?; echo rc=$?",
		rep + " | tee /tmp/x",
		rep + " | sh",
		rep + " | tail -n 5",
		rep + " | cut -d, -f1",
		rep + " |& tail -5",
		rep + " | PATH=/tmp tail -5",
		rep + " | tail -5 > /tmp/x",
		rep + " 2>/tmp/err",
		rep + " &> " + sp + "/x.log",
		rep + " > /tmp/x.log",
		rep + " > ~/.ssh/authorized_keys",
		rep + " > " + wtGit + "/config",
		rep + " > " + sp + "/a.log > " + sp + "/b.log",
		rep + " 3> " + sp + "/a.log",
		rep + " < /tmp/in",
		rep + " <<< x",
		rep + " > " + sp + "/../other/x.log",
		rep + " > " + strings.Replace(sp, "sid-1", "sid-2", 1) + "/x.log",
		rep + " > " + sp,
		rep + "\n" + "git push",
		// bash reads # as a comment; a zsh without interactive comments would run what follows
		rep + " #; git push",
		rep + " # note",
		"# note\n" + rep,
		rep + " \\\n",
		rep + "\r",
	} {
		if ok, why := adoExact(cmd, "sid-1"); ok && why == "" {
			t.Errorf("the exact rule passed an off-shape command:\n  %s", cmd)
		}
		// three keep today's verdict, which passes them for its own reasons: bun -c X runs X (the empty bunfig), not
		// the suite; ./ado-suite.ts is not in the folder and the command names no az; bunx is not read as bun
		if strings.Contains(cmd, " -c "+bunfig) || strings.Contains(cmd, " ./ado-suite.ts") || strings.HasPrefix(cmd, "bunx ") {
			continue
		}
		if why := check(exactIn(cmd)); why == "" {
			t.Errorf("want ask, passed:\n  %s", cmd)
		}
	}
}

// The exact shape asks when what it relies on is not so at hook time, and says why.
func TestADOSuiteExactShapeHookTimeChecks(t *testing.T) {
	cases := map[string]struct {
		set  func(t *testing.T, home, bunfig, sp string)
		cmd  string // "" = report; "R" = a redirect into the scratchpad's out.log
		want string
	}{
		"suite edited": {set: func(t *testing.T, home, _, _ string) {
			write(t, filepath.Join(home, ".crius/state/quotes/build/bin/ado-suite.ts"), "console.log(1)\n")
		}, want: "ado-suite.ts does not match the SHA-256 the guard pins"},
		"bunfig edited": {set: func(t *testing.T, _, bunfig, _ string) {
			write(t, bunfig, "preload = [\"/tmp/p.ts\"]\n")
		}, want: "empty-bunfig.toml does not match the SHA-256 the guard pins"},
		"suite a symlink": {set: func(t *testing.T, home, _, sp string) {
			p := filepath.Join(home, ".crius/state/quotes/build/bin/ado-suite.ts")
			b, _ := os.ReadFile(p)
			write(t, filepath.Join(sp, "copy.ts"), string(b))
			os.Remove(p)
			if err := os.Symlink(filepath.Join(sp, "copy.ts"), p); err != nil {
				t.Fatal(err)
			}
		}, want: "is not a plain file"},
		"global bunfig in HOME": {set: func(t *testing.T, home, _, _ string) {
			write(t, filepath.Join(home, ".bunfig.toml"), "")
		}, want: ".bunfig.toml"},
		"global bunfig in XDG_CONFIG_HOME": {set: func(t *testing.T, _, _, sp string) {
			t.Setenv("XDG_CONFIG_HOME", sp)
			write(t, filepath.Join(sp, ".bunfig.toml"), "")
		}, want: ".bunfig.toml"},
		"BUN_OPTIONS set": {set: func(t *testing.T, _, _, _ string) {
			t.Setenv("BUN_OPTIONS", "--preload=/tmp/p.ts")
		}, want: "BUN_OPTIONS"},
		// P1-4: a hardlink made by an earlier command
		"redirect target hardlinked": {set: func(t *testing.T, home, _, sp string) {
			cfg := filepath.Join(home, "Projects/Appxite/svc_quote/wt/core-2/.git-config")
			write(t, cfg, "[core]\n")
			if err := os.Link(cfg, filepath.Join(sp, "out.log")); err != nil {
				t.Fatal(err)
			}
		}, cmd: "R", want: "has 2 links"},
		"redirect target a symlink": {set: func(t *testing.T, home, _, sp string) {
			if err := os.Symlink(filepath.Join(home, ".crius/x"), filepath.Join(sp, "out.log")); err != nil {
				t.Fatal(err)
			}
		}, cmd: "R", want: "is not a plain file"},
		"redirect target a folder": {set: func(t *testing.T, _, _, sp string) {
			if err := os.Mkdir(filepath.Join(sp, "out.log"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, cmd: "R", want: "is not a plain file"},
		"redirect into a git worktree in the scratchpad": {set: func(t *testing.T, _, _, sp string) {
			if err := os.MkdirAll(filepath.Join(sp, "repo"), 0o755); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(sp, "repo/.git"), "gitdir: /x\n")
		}, cmd: "repo/out.log", want: "inside a git worktree"},
		"redirect into a .git folder": {set: func(t *testing.T, _, _, sp string) {
			if err := os.MkdirAll(filepath.Join(sp, ".git/refs"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, cmd: ".git/refs/out.log", want: "inside a git worktree"},
		"redirect through a symlinked folder": {set: func(t *testing.T, home, _, sp string) {
			if err := os.Symlink(filepath.Join(home, "Projects"), filepath.Join(sp, "link")); err != nil {
				t.Fatal(err)
			}
		}, cmd: "link/out.log", want: "outside"},
		"redirect into a missing folder": {cmd: "nothere/out.log", want: "cannot resolve"},
		"worktree a symlink": {set: func(t *testing.T, home, _, sp string) {
			p := filepath.Join(home, "Projects/Appxite/svc_quote/wt/core-2")
			os.RemoveAll(p)
			if err := os.Symlink(sp, p); err != nil {
				t.Fatal(err)
			}
		}, cmd: "RUN", want: "is not a folder"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			home, bunfig, sp := exactHome(t)
			if c.set != nil {
				c.set(t, home, bunfig, sp)
			}
			cmd := "bun --no-env-file --config=" + bunfig + " ~/.crius/state/quotes/build/bin/ado-suite.ts report 1"
			switch c.cmd {
			case "":
			case "R":
				cmd += " > " + sp + "/out.log 2>&1"
			case "RUN":
				cmd = "bun --no-env-file --config=" + bunfig + " ~/.crius/state/quotes/build/bin/ado-suite.ts run ~/Projects/Appxite/svc_quote/wt/core-2 feature/96365-partner-quote-detail"
			default:
				cmd += " > " + sp + "/" + c.cmd
			}
			why := check(exactIn(cmd))
			if !strings.Contains(why, c.want) {
				t.Errorf("want an ask naming %q, got %q", c.want, why)
			}
		})
	}
}

// With no session id the scratchpad is unknown: a redirect there asks; the build folder still passes.
func TestADOSuiteExactNoSession(t *testing.T) {
	_, bunfig, sp := exactHome(t)
	pre := "bun --no-env-file --config=" + bunfig + " ~/.crius/state/quotes/build/bin/ado-suite.ts report 1"
	in := exactIn(pre + " > " + sp + "/x.log")
	in.SessionID = ""
	if why := check(in); why == "" {
		t.Error("want ask with no session id")
	}
	in = exactIn(pre + " > ~/.crius/state/quotes/build/notes/x.log")
	in.SessionID = ""
	if why := check(in); why != "" {
		t.Errorf("want pass into the build folder, asked: %s", why)
	}
}

func write(t *testing.T, p, body string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// review 11-06 part 1, P2: run passes only for a linked svc_quote worktree whose HEAD is the branch named, and only a
// feature/96365-* or wip/* branch.
func TestADOSuiteRunNeedsTheSvcQuoteWorktreeAndBranch(t *testing.T) {
	cases := map[string]struct {
		set    func(t *testing.T, home, wtDir string)
		wt, br string
		want   string
	}{
		"a git init folder under wt": {set: func(t *testing.T, home, _ string) {
			if err := os.MkdirAll(filepath.Join(home, "Projects/Appxite/svc_quote/wt/fresh/.git/objects"), 0o755); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(home, "Projects/Appxite/svc_quote/wt/fresh/.git/HEAD"), "ref: refs/heads/feature/96365-x\n")
		}, wt: "fresh", br: "feature/96365-x", want: "not a linked worktree of svc_quote"},
		"someone else's branch": {set: func(t *testing.T, home, _ string) {
			linkedWT(t, home, "pr", "feature/someones-pr")
		}, wt: "pr", br: "feature/someones-pr"},
		"a branch other than HEAD's": {wt: "core-2", br: "feature/96365-other", want: "is on feature/96365-partner-quote-detail"},
		"a detached HEAD": {set: func(t *testing.T, home, _ string) {
			linkedWT(t, home, "det", "x")
			write(t, filepath.Join(home, "Projects/Appxite/svc_quote/svc_quote/.git/worktrees/det/HEAD"), "754a722dfc50b2a6166a906e22fc1c4ecfcfe91e\n")
		}, wt: "det", br: "feature/96365-x", want: "not on a branch"},
		"a gitfile naming another repo": {set: func(t *testing.T, home, _ string) {
			other := filepath.Join(home, "other/.git/worktrees/o")
			if err := os.MkdirAll(other, 0o755); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(other, "HEAD"), "ref: refs/heads/feature/96365-x\n")
			write(t, filepath.Join(other, "commondir"), "../..\n")
			linkedWT(t, home, "o", "feature/96365-x")
			write(t, filepath.Join(home, "Projects/Appxite/svc_quote/wt/o/.git"), "gitdir: "+other+"\n")
		}, wt: "o", br: "feature/96365-x", want: "not a linked worktree of svc_quote"},
		"a worktrees entry whose commondir names another repo": {set: func(t *testing.T, home, _ string) {
			linkedWT(t, home, "cd", "feature/96365-x")
			if err := os.MkdirAll(filepath.Join(home, "evil/.git"), 0o755); err != nil {
				t.Fatal(err)
			}
			write(t, filepath.Join(home, "Projects/Appxite/svc_quote/svc_quote/.git/worktrees/cd/commondir"), filepath.Join(home, "evil/.git")+"\n")
		}, wt: "cd", br: "feature/96365-x", want: "not a linked worktree of svc_quote"},
		"a .git symlink": {set: func(t *testing.T, home, _ string) {
			linkedWT(t, home, "ln", "feature/96365-x")
			p := filepath.Join(home, "Projects/Appxite/svc_quote/wt/ln/.git")
			os.Remove(p)
			if err := os.Symlink(filepath.Join(home, "Projects/Appxite/svc_quote/svc_quote/.git/worktrees/ln"), p); err != nil {
				t.Fatal(err)
			}
		}, wt: "ln", br: "feature/96365-x", want: "not a linked worktree of svc_quote"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			home, bunfig, _ := exactHome(t)
			if c.set != nil {
				c.set(t, home, "")
			}
			cmd := "bun --no-env-file --config=" + bunfig + " ~/.crius/state/quotes/build/bin/ado-suite.ts run ~/Projects/Appxite/svc_quote/wt/" + c.wt + " " + c.br
			why := check(exactIn(cmd))
			if why == "" || c.want != "" && !strings.Contains(why, c.want) {
				t.Errorf("want an ask naming %q, got %q", c.want, why)
			}
		})
	}
}

// review 11-06 part 1, P3: output goes only to the session's scratchpad or build/notes and build/qa; never a file named
// .git, an executable, or into a bare repo folder.
func TestADOSuiteRedirectPlaces(t *testing.T) {
	home, bunfig, sp := exactHome(t)
	build := home + "/.crius/state/quotes/build"
	write(t, build+"/BUILD-LOG.md", "log\n")
	write(t, build+"/STATUS.md", "s\n")
	write(t, build+"/bin/pr-build.sh", "#!/bin/sh\n")
	write(t, build+"/notes/run.sh", "#!/bin/sh\n")
	if err := os.Chmod(build+"/notes/run.sh", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sp+"/bare/objects", 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, sp+"/bare/HEAD", "ref: refs/heads/main\n")
	rep := "bun --no-env-file --config=" + bunfig + " ~/.crius/state/quotes/build/bin/ado-suite.ts report 1 > "
	for target, want := range map[string]string{
		build + "/BUILD-LOG.md":         "outside",
		build + "/STATUS.md":            "outside",
		build + "/x.log":                "outside",
		build + "/bin/pr-build.sh":      "outside",
		build + "/bin/new.log":          "outside",
		sp + "/.git":                    "named .git",
		build + "/notes/.git":           "named .git",
		build + "/notes/run.sh":         "executable",
		sp + "/bare/out.log":            "a git repository folder",
		sp + "/bare/objects/out.log":    "a git repository folder",
		build + "/notes/ado-report.log": "",
		build + "/qa/ado-report.log":    "",
		sp + "/ado.log":                 "",
	} {
		why := check(exactIn(rep + target))
		switch {
		case want == "" && why != "":
			t.Errorf("want pass for %s, asked: %s", target, why)
		case want != "" && !strings.Contains(why, want):
			t.Errorf("want an ask naming %q for %s, got %q", want, target, why)
		}
	}
}

// review 11-06 round 2. P2-1: notes/ or qa/ as a symlink would make its target (a home folder) a place to write, so
// the roots count only as their own real path. P3-1: .git in any case, and a folder holding HEAD and commondir (a
// linked worktree's git folder), are git places too.
func TestADOSuiteRedirectRootsAndGitFoldersRound2(t *testing.T) {
	home, bunfig, sp := exactHome(t)
	build := home + "/.crius/state/quotes/build"
	other := t.TempDir()
	if err := os.RemoveAll(build + "/qa"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, build+"/qa"); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{sp + "/wtgit", sp + "/up/.GIT"} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, sp+"/wtgit/HEAD", "ref: refs/heads/feature/x\n")
	write(t, sp+"/wtgit/commondir", "../..\n")
	write(t, sp+"/up/.GIT/keep", "x\n")
	rep := "bun --no-env-file --config=" + bunfig + " ~/.crius/state/quotes/build/bin/ado-suite.ts report 1 > "
	for target, want := range map[string]string{
		build + "/qa/.zshenv":           "outside",
		build + "/qa/ado-report.log":    "outside",
		sp + "/.GIT":                    "named .git",
		build + "/notes/.Git":           "named .git",
		sp + "/wtgit/out.log":           "a git repository folder",
		sp + "/up/.GIT/out.log":         "git worktree",
		build + "/notes/ado-report.log": "",
		sp + "/ado.log":                 "",
	} {
		why := check(exactIn(rep + target))
		switch {
		case want == "" && why != "":
			t.Errorf("want pass for %s, asked: %s", target, why)
		case want != "" && !strings.Contains(why, want):
			t.Errorf("want an ask naming %q for %s, got %q", want, target, why)
		}
	}
}

// review 11-06 part 1, P3: each passing shape, read the usual way (a ctx_batch_execute command, or checkBash), asks only
// for ado-suite.ts's own write: the exact rule hides nothing else the usual reading would find.
func TestADOSuitePassingShapesAskOnlyForTheSuiteTheUsualWay(t *testing.T) {
	home, bunfig, sp := exactHome(t)
	pre := "bun --no-env-file --config=" + bunfig + " ~/.crius/state/quotes/build/bin/ado-suite.ts"
	run := " run ~/Projects/Appxite/svc_quote/wt/core-2 feature/96365-partner-quote-detail"
	for _, cmd := range []string{
		pre + " report 126862 2>&1 | tail -10 | LC_ALL=C cut -c1-220",
		pre + " report 126863 > ~/.crius/state/quotes/build/notes/ado-126863.out 2>&1; echo rc=$?",
		pre + run + " > " + sp + "/ado-suite.log 2>&1; echo ado_rc=$?",
		pre + " run " + home + "/Projects/Appxite/svc_quote/wt/core-3 wip/x.y_z-1 2>&1 | head -40",
	} {
		if why := check(exactIn(cmd)); why != "" {
			t.Errorf("want pass on Bash, asked: %s\n  %s", why, cmd)
		}
		why := checkBash(cmd, ctx{cwd: "/", whole: cmd, reads: new(int), read: new([]readText)})
		if !strings.Contains(why, "a write to Azure DevOps; "+home+"/.crius/state/quotes/build/bin/ado-suite.ts") {
			t.Errorf("want the usual reading to ask for the suite's write only, got %q\n  %s", why, cmd)
		}
		in := input{ToolName: "mcp__plugin_context-mode_context-mode__ctx_batch_execute", Cwd: "/"}
		in.ToolInput.Commands = batchCommands{cmd}
		// report passes there on its pin (item 5); run posts and keeps the click
		if why := check(in); why == "" && strings.Contains(cmd, " run ") {
			t.Errorf("want ask as a ctx_batch_execute command:\n  %s", cmd)
		}
	}
}

// review 11-06 round 3, P3: a git folder whose objects is a symlink, or one with refs, is a git folder too.
func TestADOSuiteRedirectGitFolderWithSymlinkedObjectsOrRefs(t *testing.T) {
	_, bunfig, sp := exactHome(t)
	elsewhere := t.TempDir()
	for _, d := range []string{sp + "/symobj", sp + "/withrefs/refs"} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, sp+"/symobj/HEAD", "ref: refs/heads/main\n")
	if err := os.Symlink(elsewhere, sp+"/symobj/objects"); err != nil {
		t.Fatal(err)
	}
	write(t, sp+"/withrefs/HEAD", "ref: refs/heads/main\n")
	rep := "bun --no-env-file --config=" + bunfig + " ~/.crius/state/quotes/build/bin/ado-suite.ts report 1 > "
	for _, target := range []string{sp + "/symobj/out.log", sp + "/withrefs/out.log"} {
		if why := check(exactIn(rep + target)); !strings.Contains(why, "a git repository folder") {
			t.Errorf("want an ask naming a git repository folder for %s, got %q", target, why)
		}
	}
	if why := check(exactIn(rep + sp + "/plain.log")); why != "" {
		t.Errorf("want pass for a plain scratchpad file, asked: %s", why)
	}
}
