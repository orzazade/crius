package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"mvdan.cc/sh/v3/syntax"
)

// The Quotes team's ado-suite.ts (owner 11-06: guard changes are Crius's call). `report <build id>` only reads; `run
// <worktree> <branch>` pushes the worktree's HEAD to a feature branch and POSTs a pipeline run, the write the usual reading
// finds in it, so every call asked. Three rounds of passing it by listing what is dangerous around it each had new
// bypasses in review (a bunfig.toml or .env written first, a function or alias for bun, a submodule's fsmonitor, a
// hardlink into .git, more git config keys). This rule passes it only when the WHOLE command is one exact shape:
//
//	bun --no-env-file --config=<empty-bunfig.toml> <ado-suite.ts> report <digits>
//	bun --no-env-file --config=<empty-bunfig.toml> <ado-suite.ts> run ~/Projects/Appxite/svc_quote/wt/<name> <its branch>
//
// with both files at a SHA-256 the pin file lists (pins.go), the worktree a linked one of svc_quote on the branch named (feature/96365-*
// or wip/*), optionally followed only by one > or >> into the Quotes build notes/ or qa/ folder or the session's
// scratchpad (a new file, or a plain non-executable one with one link, in no git worktree or repo), 2>&1,
// `; echo rc=$?` (or ado_rc, quoted or not), and pipes into tail -N, head -N or cut -cA-B (LC_ALL=C allowed on those). Every word is plain text: no
// quote, variable, $(), glob, brace or ~ but a leading ~/ on the suite or worktree path. Anything else falls through to
// the usual reading. bun 1.3.10 (11-06): --config=<path> stops ./bunfig.toml, but `-c <path>`, `--config <path>` and
// `-c=<path>` do not (the space forms run <path> as the script); --no-env-file stops .env and .env.local; ~/.bunfig.toml
// and $XDG_CONFIG_HOME/.bunfig.toml are not read for `bun <file>`, and a tsconfig.json cannot remap node: imports; the
// session env BUN_OPTIONS can add a preload. So at hook time a global .bunfig.toml or BUN_OPTIONS asks. What the run
// pushes (no submodule, no off-list git config, no AI credit, git with fsmonitor and hooks off) ado-suite.ts checks
// itself (its ado-suite.test.ts).
// Known gaps (the guard reads the command, not the shell that runs it):
//   - The Bash tool runs zsh from a snapshot of the owner's start-up files (~/.zshenv, ~/.zprofile, ~/.zshrc, ~/.zlogin)
//     taken when the session starts: an alias, function, hash entry or option set there for bun, tail, head, cut or echo
//     runs instead, and a command that edits those files passes the guard if it posts nothing (it takes effect in the next
//     session's snapshot).
//   - Env the session already has: PATH (which bun, git or az runs), BUN_OPTIONS set after the hook, NODE_OPTIONS-like
//     variables of git or az, and proxy variables (HTTPS_PROXY, ALL_PROXY, NO_PROXY) that route the push and the ADO calls.
//   - A file changed between the hook and the run: the suite, the bunfig, the worktree's .git file, gitdir or HEAD, or a
//     redirect target (a new hardlink or symlink there).

var (
	// scratchRoot holds every session's scratchpad: <root>/<project>/<session id>/scratchpad.
	scratchRoot = func() string { return fmt.Sprintf("/private/tmp/claude-%d", os.Getuid()) }
)

var (
	exactTextRe   = regexp.MustCompile(`^[A-Za-z0-9._/=~ \n>&|;$?"-]+$`)
	exactWordRe   = regexp.MustCompile(`^[A-Za-z0-9._/=-]+$`)
	exactBranchRe = regexp.MustCompile(`^(feature/96365-|wip/)[A-Za-z0-9._/-]+$`)
	exactNameRe   = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	exactBuildRe  = regexp.MustCompile(`^[0-9]{1,12}$`)
	exactFilterRe = regexp.MustCompile(`^(tail|head) -[0-9]{1,6}$|^cut -c[0-9]{1,6}-[0-9]{1,6}$`)
	exactSidRe    = regexp.MustCompile(`^[A-Za-z0-9-]+$`)
	exactEchoes   = map[string]bool{"rc=$?": true, "ado_rc=$?": true, `"rc=$?"`: true, `"ado_rc=$?"`: true}
)

// adoExact says whether command is the exact shape (ok); why is "" when it passes, or the ask when a hook-time check
// fails. ok false: the usual reading judges the command.
func adoExact(command, sid string) (ok bool, why string) {
	// Only the characters the shapes use, $ only as $?: no comment (# that a zsh without interactive comments would not
	// drop), \ line joins, quotes but echo's "", or \r, whatever the bash parser below makes of them.
	if !exactTextRe.MatchString(command) || strings.Count(command, "$") != strings.Count(command, "$?") {
		return false, ""
	}
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil || len(f.Stmts) < 1 || len(f.Stmts) > 2 {
		return false, ""
	}
	if len(f.Stmts) == 2 && !echoRc(f.Stmts[1]) {
		return false, ""
	}
	parts, good := pipeline(f.Stmts[0])
	if !good {
		return false, ""
	}
	for _, st := range parts[1:] {
		if !filter(st) {
			return false, ""
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false, ""
	}
	bin := filepath.Join(home, ".crius/state/quotes/build/bin")
	suite, bunfig := filepath.Join(bin, "ado-suite.ts"), filepath.Join(bin, "empty-bunfig.toml")
	wtRoot := filepath.Join(home, "Projects/Appxite/svc_quote/wt")
	ce := parts[0].Cmd.(*syntax.CallExpr)
	if len(ce.Assigns) > 0 {
		return false, ""
	}
	args := make([]string, len(ce.Args))
	for i, w := range ce.Args {
		if args[i], good = plainWord(w, home); !good {
			return false, ""
		}
	}
	if len(args) < 6 || args[0] != "bun" || args[1] != "--no-env-file" || args[2] != "--config="+bunfig || args[3] != suite {
		return false, ""
	}
	wt := ""
	switch {
	case args[4] == "report" && len(args) == 6 && exactBuildRe.MatchString(args[5]):
	case args[4] == "run" && len(args) == 7 && filepath.Dir(args[5]) == wtRoot && filepath.Clean(args[5]) == args[5] &&
		exactNameRe.MatchString(filepath.Base(args[5])) && strings.Trim(filepath.Base(args[5]), ".") != "" &&
		exactBranchRe.MatchString(args[6]) && !strings.Contains(args[6], ".."):
		wt = args[5]
	default:
		return false, ""
	}
	target, good := redirects(parts[0], home)
	if !good {
		return false, ""
	}
	// The shape is exact; now what it relies on must hold at hook time.
	for _, p := range [][2]string{{suite, "ado-suite.ts"}, {bunfig, "empty-bunfig.toml"}} {
		if why := pinned(p[0], p[1]); why != "" {
			return true, why
		}
	}
	if why := bunEnv(home, "ado-suite.ts"); why != "" {
		return true, why
	}
	if wt != "" {
		if why := svcWorktree(wt, args[6], home); why != "" {
			return true, why
		}
	}
	if target != "" {
		if why := safeTarget(target, home, sid); why != "" {
			return true, why
		}
	}
	return true, ""
}

// bunEnv asks when a global .bunfig.toml or the session's BUN_OPTIONS could add code to bun, for the script who.
func bunEnv(home, who string) string {
	globals := []string{filepath.Join(home, ".bunfig.toml"), filepath.Join(home, ".config/.bunfig.toml")}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		globals = append(globals, filepath.Join(x, ".bunfig.toml"))
	}
	for _, g := range globals {
		if _, err := os.Lstat(g); !errors.Is(err, fs.ErrNotExist) {
			return asked(who + " with a global bun config at " + g + ": bun may read it, so the exact shape does not pass")
		}
	}
	if v := os.Getenv("BUN_OPTIONS"); v != "" {
		return asked(who + " with BUN_OPTIONS set in the session (" + v + "): it can add code to bun")
	}
	return ""
}

// svcWorktree asks unless wt is a linked worktree of svc_quote (its .git a file naming svc_quote's .git/worktrees/<x>,
// whose commondir is svc_quote's .git) on branch. It reads the files git would; it runs no git there.
func svcWorktree(wt, branch, home string) string {
	not := func(why string) string {
		return asked("ado-suite.ts run: " + wt + " is not a linked worktree of svc_quote (" + why + ")")
	}
	if fi, err := os.Lstat(wt); err != nil || !fi.IsDir() {
		return asked("ado-suite.ts run: " + wt + " is not a folder (missing or a symlink)")
	}
	if fi, err := os.Lstat(filepath.Join(wt, ".git")); err != nil || !fi.Mode().IsRegular() {
		return not("its .git is not a plain file")
	}
	b, err := os.ReadFile(filepath.Join(wt, ".git"))
	line := strings.TrimSuffix(string(b), "\n")
	if err != nil || !strings.HasPrefix(line, "gitdir: /") || strings.Contains(line, "\n") {
		return not("its .git does not name an absolute gitdir")
	}
	common, err := filepath.EvalSymlinks(filepath.Join(home, "Projects/Appxite/svc_quote/svc_quote/.git"))
	if err != nil {
		return not("svc_quote's .git cannot be resolved")
	}
	gitdir, err := filepath.EvalSymlinks(strings.TrimPrefix(line, "gitdir: "))
	if err != nil || filepath.Dir(gitdir) != filepath.Join(common, "worktrees") || !exactNameRe.MatchString(filepath.Base(gitdir)) {
		return not("its gitdir is not in " + filepath.Join(common, "worktrees"))
	}
	cd, err := os.ReadFile(filepath.Join(gitdir, "commondir"))
	c := strings.TrimSpace(string(cd))
	if err == nil && !filepath.IsAbs(c) {
		c = filepath.Join(gitdir, c)
	}
	if real, rerr := filepath.EvalSymlinks(c); err != nil || rerr != nil || real != common {
		return not("its commondir is not svc_quote's .git")
	}
	head, err := os.ReadFile(filepath.Join(gitdir, "HEAD"))
	h := strings.TrimSpace(string(head))
	if err != nil || !strings.HasPrefix(h, "ref: refs/heads/") {
		return asked("ado-suite.ts run: " + wt + " is not on a branch (detached HEAD): name a branch only from a worktree on it")
	}
	if on := strings.TrimPrefix(h, "ref: refs/heads/"); on != branch {
		return asked("ado-suite.ts run: " + wt + " is on " + on + ", not " + branch)
	}
	return ""
}

// plainStmt is a statement with no !, &, coprocess or redirect.
func plainStmt(st *syntax.Stmt) bool {
	return !st.Negated && !st.Background && !st.Coprocess
}

// pipeline gives the calls of a statement that is one call or calls joined by | (not |&); only the first may redirect.
func pipeline(st *syntax.Stmt) ([]*syntax.Stmt, bool) {
	if !plainStmt(st) {
		return nil, false
	}
	switch c := st.Cmd.(type) {
	case *syntax.CallExpr:
		return []*syntax.Stmt{st}, len(c.Args) > 0
	case *syntax.BinaryCmd:
		if c.Op != syntax.Pipe || len(st.Redirs) > 0 {
			return nil, false
		}
		l, ok := pipeline(c.X)
		if !ok {
			return nil, false
		}
		r, ok := pipeline(c.Y)
		if !ok {
			return nil, false
		}
		for _, s := range r {
			if len(s.Redirs) > 0 {
				return nil, false
			}
		}
		return append(l, r...), true
	}
	return nil, false
}

// filter is tail -N, head -N or cut -cA-B, with LC_ALL=C or nothing before it, and no redirect.
func filter(st *syntax.Stmt) bool {
	ce, ok := st.Cmd.(*syntax.CallExpr)
	if !ok || len(st.Redirs) > 0 || len(ce.Assigns) > 1 || len(ce.Args) != 2 {
		return false
	}
	if len(ce.Assigns) == 1 {
		a := ce.Assigns[0]
		if v, ok := plainWord(a.Value, ""); a.Append || a.Naked || a.Index != nil || a.Array != nil || a.Name.Value != "LC_ALL" ||
			!ok || v != "C" {
			return false
		}
	}
	name, ok1 := plainWord(ce.Args[0], "")
	opt, ok2 := plainWord(ce.Args[1], "")
	return ok1 && ok2 && exactFilterRe.MatchString(name+" "+opt)
}

// echoRc is `echo rc=$?` or `echo ado_rc=$?`, the word quoted or not, alone.
func echoRc(st *syntax.Stmt) bool {
	ce, ok := st.Cmd.(*syntax.CallExpr)
	if !ok || !plainStmt(st) || len(st.Redirs) > 0 || len(ce.Assigns) > 0 || len(ce.Args) != 2 {
		return false
	}
	if n, ok := plainWord(ce.Args[0], ""); !ok || n != "echo" {
		return false
	}
	return exactEchoes[literal(ce.Args[1]).src]
}

// plainWord is a word of one unquoted literal in plain characters; with home set, a leading ~/ is home.
func plainWord(w *syntax.Word, home string) (string, bool) {
	if w == nil || len(w.Parts) != 1 {
		return "", false
	}
	l, ok := w.Parts[0].(*syntax.Lit)
	if !ok {
		return "", false
	}
	v := l.Value
	if home != "" && strings.HasPrefix(v, "~/") {
		v = home + v[1:]
	}
	return v, exactWordRe.MatchString(v)
}

// redirects allows on the bun call only 2>&1 and at most one > or >> (fd 1) to a plain path; it gives that path or "".
func redirects(st *syntax.Stmt, home string) (string, bool) {
	target := ""
	for _, r := range st.Redirs {
		w, ok := plainWord(r.Word, home)
		switch {
		case !ok:
			return "", false
		case r.Op == syntax.DplOut && r.N != nil && r.N.Value == "2" && w == "1":
		case (r.Op == syntax.RdrOut || r.Op == syntax.AppOut) && (r.N == nil || r.N.Value == "1") && target == "" &&
			filepath.IsAbs(w) && filepath.Clean(w) == w:
			target = w
		default:
			return "", false
		}
	}
	return target, true
}

// isDotGit is true for .git in any case: the Mac's disk does not tell .GIT from .git.
func isDotGit(name string) bool { return strings.EqualFold(name, ".git") }

// safeTarget asks unless the redirect target is in the Quotes build folder or this session's scratchpad (by its real
// folder), in no git worktree or .git folder below there, and is new or a plain file with one link.
func safeTarget(p, home, sid string) string {
	dir, err := filepath.EvalSymlinks(filepath.Dir(p))
	if err != nil {
		return asked("ado-suite.ts output to " + p + ": the guard cannot resolve its folder")
	}
	full := filepath.Join(dir, filepath.Base(p))
	if isDotGit(filepath.Base(p)) {
		return asked("ado-suite.ts output to " + p + ": a file named .git")
	}
	var roots []string
	for _, sub := range []string{"notes", "qa"} {
		// only a real folder: a symlinked notes/ or qa/ would make its target (say a home folder) a root
		r := filepath.Join(home, ".crius/state/quotes/build", sub)
		if real, err := filepath.EvalSymlinks(r); err == nil && real == r {
			roots = append(roots, r)
		}
	}
	if exactSidRe.MatchString(sid) {
		if sr, err := filepath.EvalSymlinks(scratchRoot()); err == nil {
			if rel, err := filepath.Rel(sr, dir); err == nil {
				if s := strings.Split(rel, "/"); len(s) >= 3 && s[0] != ".." && s[1] == sid && s[2] == "scratchpad" {
					roots = append(roots, filepath.Join(sr, s[0], sid, "scratchpad"))
				}
			}
		}
	}
	root := ""
	for _, r := range roots {
		if strings.HasPrefix(full, r+"/") {
			root = r
		}
	}
	if root == "" {
		return asked("ado-suite.ts output to " + p + ": outside the Quotes build notes/ and qa/ folders and this session's scratchpad")
	}
	for d := dir; ; d = filepath.Dir(d) {
		if isDotGit(filepath.Base(d)) {
			return asked("ado-suite.ts output to " + p + ": inside a git worktree's .git")
		}
		if _, err := os.Lstat(filepath.Join(d, ".git")); !errors.Is(err, fs.ErrNotExist) {
			return asked("ado-suite.ts output to " + p + ": inside a git worktree (" + d + ")")
		}
		// HEAD with commondir, objects or refs of any kind (a symlinked objects included) is a git folder
		_, herr := os.Lstat(filepath.Join(d, "HEAD"))
		gitLike := false
		for _, n := range []string{"commondir", "objects", "refs"} {
			if _, err := os.Lstat(filepath.Join(d, n)); err == nil {
				gitLike = true
			}
		}
		if herr == nil && gitLike {
			return asked("ado-suite.ts output to " + p + ": inside a git repository folder (" + d + ")")
		}
		if d == root || len(d) <= len(root) {
			break
		}
	}
	fi, err := os.Lstat(full)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return ""
	case err != nil || !fi.Mode().IsRegular():
		return asked("ado-suite.ts output to " + p + ": it is not a plain file")
	case fi.Mode()&0o111 != 0:
		return asked("ado-suite.ts output to " + p + ": an executable file")
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || st.Nlink != 1 {
		n := "an unknown number of"
		if ok {
			n = fmt.Sprint(st.Nlink)
		}
		return asked("ado-suite.ts output to " + p + ": it has " + n + " links, so writing it may write another file")
	}
	return ""
}
