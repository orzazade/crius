// Command crius-bash-guard is a Claude Code PreToolUse hook for Bash. It refuses six commands that broke the team:
// team-wait.sh run detached with `&` (a detached waiter cannot wake the session; 10-21 22:44), and a line written
// straight onto a bus log instead of through bus-post (10-20 and 10-21: UTC time slip, VERIFIED before the run; 10-22:
// 332 quotes.log lines ingest cannot read), and a command that prints every variable with its value (env, set,
// printenv, export -p, declare -p or a bare local: 10-22 a bare zsh local put a token into a transcript), and a cmux
// send or send-key without --workspace and --surface as literal words (it types into the caller's own tab; 10-23
// twice), and a docker image build outside a lone heavy slot (HEAVY_SLOTS=1 bun .../heavy.ts run --: 10-30 a compose
// build of the AXI stack held the Docker VM at 11 cores), and a cmux send with no cmux send-key Enter to the same
// surface after it in the same command (10-30: axi's text sat unsent 18:16 to 21:45). Exit 2 refuses, with the reason on
// stderr; anything else passes.
//
// It catches habits, not intent: fifteen review rounds showed a list of shell forms is never complete, and the full
// check is the digest's count of lines it cannot read. Known gaps: function bodies run later, source and ., a script
// file a shell runs (bash <(...)), xargs, find -exec, coproc, other shells and languages (ruby -e, node -e,
// awk -i inplace), other writers (ln, rsync, truncate, sponge), a symlink to the bus folder, declare -n, a cd or
// assignment that fails or runs only on one branch, ${D:=x} inside [[ ]] or a case word, a computed part whose own
// text holds "..", a function or script named mktemp, $'...' quoting, a variable or cd set inside eval, an export
// behind a wrapper (command export), and $CRIUS_HOME. The value check misses one named variable (printenv X,
// declare -p X, echo $X), names from a $(...) (declare -p $(compgen -v)), typeset -m, zsh's set -A, integer, float and
// typeset -F, another machine's environment (kubectl exec ... -- env, docker exec c env, ssh host printenv), a computed
// command name, options inside one env -S string (env -S 'printenv -0'), zsh text the parser cannot read once ${(k)x},
// =(...) and glob qualifiers are put as bash, and other languages; it also refuses env | cut -d= -f1, xargs printenv,
// printenv --help, typeset +x and zsh text holding a *(...) with a quote in it, which bash reads as an extglob. The
// docker rule misses a compose up, run or watch with no --build that builds a missing image, a build inside a script the
// heavy.ts job runs (heavy.ts run -- sh -c '...'), a heavy.ts path from a variable, an export in a subshell or pipe
// part or undone by unset, sudo's clean environment, and docker build -h (help, refused). The Enter rule reads the
// command in order, not by branch (a send-key in an if that does not run counts), and a surface written two ways ("$S"
// and $S) is two surfaces.
package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

func main() {
	var in struct {
		ToolName  string `json:"tool_name"`
		ToolInput struct {
			Command string `json:"command"`
		} `json:"tool_input"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&in); err != nil {
		fmt.Fprintln(os.Stderr, "crius-bash-guard: unreadable hook input:", err)
		os.Exit(1) // a hook error, not a refusal: Claude Code shows it and runs the command
	}
	if in.ToolName != "Bash" {
		return
	}
	if why := check(in.ToolInput.Command); why != "" {
		fmt.Fprintln(os.Stderr, "Refused by crius-bash-guard:", why)
		os.Exit(2)
	}
}

var (
	loneAmpRe = regexp.MustCompile(`(^|[^&>|])&($|[^&>])`)
	writeRe   = regexp.MustCompile(`(^|[^-])>|\btee\b`)
	// The disk ignores case: ~/.CRIUS/bus is the bus (review round 12).
	busRe  = regexp.MustCompile(`(?i)(^|/)\.crius/bus(/|$)`)
	nameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	// zsh forms the bash parser cannot read, put as bash: ${(k)parameters} and ${=x} as ${x}, =(env) as <(env), a glob's
	// qualifier dropped (**/*.go(.) as **/*.go). No match holds a quote or $, so quotes and $(...) stay (third review).
	zshRe = regexp.MustCompile(`\$\{(\([^)'"$\s]*\)|[=~^])+|(^|[\s;&|(])=\(|` +
		`[^\s;&|()<>{}'"$]*[*?][^\s;&|()<>{}'"$]*\([^()'"$\s]*\)`)
	// zshSafeRe: a zshRe match whose bash form changes no command or value: ${= and ${^, the flags f, k, v, @, s:x: and
	// j:x:, =(, and a glob qualifier of file types and modes. Any other form may run code: (e) and (%) expand the value,
	// (#), (l:n:) and a qualifier's [n] do math, which runs a $(...) in an index, (P) reads another variable, ${~x} globs
	// the value with its qualifiers, and a qualifier's e: or + runs a command (review of 2f86353).
	zshSafeRe = regexp.MustCompile(`^(\$\{(\(([fkv@]|[sj]:[^:]*:)+\)|[=^])+|(^|[\s;&|(])=\(|[^\s;&|()<>{}'"$]*[*?][^\s;&|()<>{}'"$]*\([./@=*%NnDTFrwxRWX^-]*\))$`)
)

// unknown stands for text only the run shows: a $(...), $1, ${a[0]}, a variable read sets, a script on stdin. The
// markers hold a NUL, which no shell word can, so a command cannot type them (review round 10).
const unknown = "\x00unknown\x00"

// newDir comes before the unknown part a mktemp names: one new file or folder, so a path after it stays in it unless it
// climbs out with ".." ($T/.crius/bus is a test home, review round 10).
const newDir = "\x00new\x00"

// tempName stands for the one name a mktemp makes, so ".." out of it leaves a known path (review round 11).
const tempName = "\x00name\x00"

// check gives the reason to refuse command, or "".
func check(command string) string {
	w := &walker{vars: map[string]string{}, exported: map[string]bool{}, named: namesBus(command), unsent: map[string]bool{}}
	if why := w.check(command); why != "" || len(w.unsent) == 0 {
		return why
	}
	return fmt.Sprintf(unsent, strings.Join(slices.Sorted(maps.Keys(w.unsent)), ", "))
}

// setsGlobSubst: the text may turn on zsh's glob_subst. Option names ignore case and _, emulate sh, ksh or csh sets it,
// and setopt -m sets options by pattern (fourth review of the zsh fast path).
func setsGlobSubst(text string) bool {
	t := strings.ReplaceAll(fold(text), "_", "")
	return strings.Contains(t, "globsubst") || strings.Contains(t, "emulate") || strings.Contains(t, "setopt")
}

// namesBus says whether text names the bus folder, in any case.
func namesBus(text string) bool { return strings.Contains(fold(text), ".crius/bus") }

// fold is the name the disk opens: it ignores case, and ſ is s and K (Kelvin) is k (review round 13).
func fold(s string) string { return strings.ToLower(strings.ToUpper(s)) }

// walker follows a command in order: the folder cd moves to and the variables it sets, so each write is judged by the
// path it names. A variable it did not set stays as written ("$TMPDIR/v.txt" is not the bus). A write to a path only
// the run shows is refused when the command names the bus folder.
type walker struct {
	cwd  string // "" before a cd, or after one it cannot follow
	vars map[string]string
	// exported: names an export gave to the commands after it (a plain X=1; reaches no program); allExport: set -a
	exported  map[string]bool
	allExport bool
	named     bool // the command names .crius/bus
	// anyVar: a computed word named the variable a command set (export $(...), read "$N"), so any variable is unknown
	anyVar bool
	// globSubst: zsh's glob_subst may be on, so ${=x} globs its value: the command names it or emulate, or this is a
	// script a shell runs, which may start with any options (-o, ARGV0=sh: third review of the zsh fast path)
	globSubst bool
	// unsent: the surfaces a cmux send typed into with no send-key Enter to them after it; one map for the whole command
	unsent map[string]bool
	why    string
}

func (w *walker) check(command string) string {
	w.globSubst = w.globSubst || setsGlobSubst(command)
	f, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return checkText(command, err, w.globSubst)
	}
	syntax.Walk(f, func(n syntax.Node) bool {
		if s, ok := n.(*syntax.Stmt); ok && s.Background && runsWaiter(s) {
			w.why = detached
		}
		return w.why == ""
	})
	w.stmts(f.Stmts)
	return w.why
}

func (w *walker) stmts(ss []*syntax.Stmt) {
	for _, s := range ss {
		w.stmt(s)
	}
}

func (w *walker) stmt(s *syntax.Stmt) {
	if w.why != "" {
		return
	}
	if s.Background { // a job runs in a child shell: its cd and variables do not reach the commands after it
		cwd, vars := w.cwd, maps.Clone(w.vars)
		defer func() { w.cwd, w.vars = cwd, vars }()
	}
	switch c := s.Cmd.(type) {
	case *syntax.CallExpr:
		w.call(c, s.Redirs)
	case *syntax.BinaryCmd:
		if c.Op != syntax.Pipe && c.Op != syntax.PipeAll {
			w.stmt(c.X)
			w.stmt(c.Y)
			break
		}
		// A pipe's parts run in child shells, but zsh runs the last part in this one: what the parts before it set
		// does not reach the commands after the pipe, and what the last part changes is unknown (review round 8).
		cwd, vars := w.cwd, maps.Clone(w.vars)
		w.stmt(c.X)
		w.cwd, w.vars = cwd, maps.Clone(vars)
		w.stmt(c.Y)
		if w.cwd != cwd {
			w.cwd = unknown
		}
		for k, v := range w.vars {
			if old, ok := vars[k]; !ok || old != v {
				w.vars[k] = unknown
			}
		}
	case *syntax.Block:
		w.stmts(c.Stmts)
	case *syntax.Subshell: // a child shell: its cd and variables do not reach the commands after it
		cwd, vars := w.cwd, maps.Clone(w.vars)
		w.stmts(c.Stmts)
		w.cwd, w.vars = cwd, vars
	case *syntax.IfClause:
		for ; c != nil; c = c.Else {
			w.stmts(c.Cond)
			w.stmts(c.Then)
		}
	case *syntax.WhileClause:
		w.stmts(c.Cond)
		w.stmts(c.Do)
	case *syntax.ForClause:
		if it, ok := c.Loop.(*syntax.WordIter); ok && !it.InPos.IsValid() { // for f; do: the positional parameters
			w.vars[it.Name.Value] = unknown
		} else if ok {
			var items []string
			for _, i := range it.Items {
				w.subs(i)
				items = append(items, w.resolve(i))
			}
			w.vars[it.Name.Value] = strings.Join(items, " ")
		} else if cl, ok := c.Loop.(*syntax.CStyleLoop); ok { // for ((i=$(...); ...))
			w.nested(cl)
		}
		w.stmts(c.Do)
	case *syntax.CaseClause:
		w.subs(c.Word)
		for _, it := range c.Items {
			for _, p := range it.Patterns {
				w.subs(p)
			}
			w.stmts(it.Stmts)
		}
	case *syntax.TestClause: // [[ ]], (( )) and let run the $(...) in them
		w.nested(c.X)
	case *syntax.ArithmCmd:
		w.nested(c.X)
	case *syntax.LetClause:
		for _, e := range c.Exprs {
			w.nested(e)
		}
	case *syntax.FuncDecl:
		w.stmt(c.Body)
	case *syntax.TimeClause:
		if c.Stmt != nil {
			w.stmt(c.Stmt)
		}
	case *syntax.DeclClause:
		var words []string
		for _, a := range c.Args {
			switch {
			case a.Name != nil:
				words = append(words, a.Name.Value)
			case a.Value != nil:
				words = append(words, w.resolve(a.Value))
			}
		}
		if lists(c.Variant.Value, words) {
			w.why = dump
			return
		}
		// export X=1, declare -x X=1, typeset -x and export "X=1" give X to the commands after them
		exports := c.Variant.Value == "export" || slices.ContainsFunc(c.Args, func(a *syntax.Assign) bool {
			if a.Name != nil || a.Value == nil {
				return false
			}
			o, ok := literal(a.Value)
			return ok && strings.HasPrefix(o, "-") && strings.Contains(o, "x")
		})
		for _, a := range c.Args {
			w.assign(a)
			switch {
			case !exports:
			case a.Name != nil:
				w.exported[a.Name.Value] = true
			case a.Value != nil:
				if n, _, ok := strings.Cut(w.resolve(a.Value), "="); ok && nameRe.MatchString(n) {
					w.exported[n] = true
				}
			}
			// export D={/tmp,~/.crius/bus}: bash expands braces in a declaration's word and the last one wins (review
			// round 15); a plain D={a,b} keeps its text.
			if a.Name != nil && !a.Naked && a.Value != nil && slices.ContainsFunc(a.Value.Parts, braces) {
				w.vars[a.Name.Value] = unknown
			}
		}
	}
	for _, r := range s.Redirs {
		w.subs(r.Word)
		w.subs(r.Hdoc)
		switch r.Op {
		case syntax.RdrOut, syntax.AppOut, syntax.ClbOut, syntax.RdrAll, syntax.AppAll, syntax.RdrInOut:
			w.target(w.resolve(r.Word))
		case syntax.DplOut: // >&file, but not >&2 or >&-
			if t := w.resolve(r.Word); t != "-" && strings.Trim(t, "0123456789") != "" {
				w.target(t)
			}
		}
	}
}

// call judges one command: cd moves the folder, a shell's script (-c, a here-document, stdin) and eval's text are
// checked as commands, read sets variables to unknown, and tee, cp, mv, install, dd of= and sed or perl -i write to
// files.
func (w *walker) call(c *syntax.CallExpr, redirs []*syntax.Redirect) {
	// VAR=v cmd and env VAR=v cmd: the value is the command's own; after it, and in its redirects, the old one holds.
	old := map[string]*string{}
	keep := func(name string) {
		if _, ok := old[name]; ok {
			return
		}
		if v, ok := w.vars[name]; ok {
			old[name] = &v
		} else {
			old[name] = nil
		}
	}
	defer func() {
		for k, v := range old {
			if v == nil {
				delete(w.vars, k)
			} else {
				w.vars[k] = *v
			}
		}
	}()
	if len(c.Args) > 0 {
		for _, a := range c.Assigns {
			if a.Name != nil {
				keep(a.Name.Value)
			}
		}
	}
	for _, a := range c.Assigns {
		w.assign(a)
	}
	for _, a := range c.Args {
		w.subs(a)
	}
	args, envs, hides, child, last := unwrap(c.Args)
	if len(args) == 0 && (last == "env" || last == "genv") { // env, env -u X, sudo env: no command, so it lists them
		w.why = dump
		return
	}
	if hides { // env -C dir, env -S "line": judged as a path only the run shows (review round 10)
		if w.hiddenDump(args) {
			w.why = dump
			return
		}
		w.refuseUnknown()
		return
	}
	given := map[string]bool{} // the variables this command's program gets from VAR=v cmd and env VAR=v
	if len(c.Args) > 0 {
		for _, a := range c.Assigns {
			if a.Name != nil {
				given[a.Name.Value] = true
			}
		}
	}
	for _, e := range envs {
		if k, v, ok := strings.Cut(w.resolve(e), "="); ok && nameRe.MatchString(k) {
			keep(k)
			w.vars[k] = v
			given[k] = true
		}
	}
	if len(args) == 0 {
		return
	}
	var rest []string
	for _, a := range args[1:] {
		rest = append(rest, w.resolve(a))
	}
	name := base(args[0])
	if lists(name, rest) { // builtin declare -p, command export -p, printenv, set
		w.why = dump
		return
	}
	if lit, _ := literal(args[0]); child || lit != name {
		// CD, /usr/bin/cd and env cd run a program in a child process: the shell's folder and variables stay (review
		// round 14). Writers are still judged by the folded name.
		switch name {
		case "cd", "pushd", "popd", "read", "mapfile", "readarray", "printf":
			name = ""
		}
	}
	switch name {
	case "cd", "pushd":
		w.cd(rest)
	case "popd":
		w.cwd = unknown
	case "bash", "sh", "zsh", "eval":
		// the script gets this command's VAR=v too (HEAVY_SLOTS=1 bash -c '... heavy.ts run -- docker build .')
		exp, all := w.exported, w.allExport
		w.exported = maps.Clone(exp)
		maps.Copy(w.exported, given)
		w.allExport = all && name == "eval" // a child shell starts without set -a
		switch s := shellScript(args); {
		case name == "eval":
			w.script(strings.Join(rest, " "))
		case s != nil:
			w.script(w.resolve(s))
		case readsStdin(args):
			w.stdin(redirs)
		}
		w.exported, w.allExport = exp, all
	case "set": // set -a exports every variable set after it, until set +a
		for i, o := range rest {
			if o == "--" || o == "-" {
				break
			}
			switch on := strings.HasPrefix(o, "-"); {
			case o == "-o" || o == "+o":
				if i+1 < len(rest) && rest[i+1] == "allexport" {
					w.allExport = on
				}
			case len(o) > 1 && (on || o[0] == '+') && o[1] != '-' && strings.Contains(o, "a"):
				w.allExport = on
			}
		}
	case "read", "mapfile", "readarray":
		for _, a := range rest {
			if nameRe.MatchString(a) {
				w.vars[a] = unknown
			} else if !strings.HasPrefix(a, "-") && (strings.Contains(a, "$") || strings.Contains(a, unknown)) { // read "$(echo D)": any name
				w.setAny()
			}
		}
	case "printf":
		if len(rest) > 1 && rest[0] == "-v" {
			if nameRe.MatchString(rest[1]) {
				w.vars[rest[1]] = unknown
			} else {
				w.setAny()
			}
		}
	case "cmux":
		if !targeted(args[1:]) {
			w.why = untargeted
		}
		w.unenter(args[1:])
	case "docker", "docker-compose":
		if builds(args) {
			w.why = unslotted
		}
	case "bun", "node":
		alone := w.vars["HEAVY_SLOTS"] == "1" && (given["HEAVY_SLOTS"] || w.exported["HEAVY_SLOTS"])
		if job := heavyJob(args); len(job) > 0 && !alone && builds(job) {
			w.why = unslotted
		}
	case "tee":
		for _, a := range rest {
			if !strings.HasPrefix(a, "-") {
				w.target(a)
			}
		}
	case "cp", "mv", "install", "gcp", "gmv", "ginstall":
		if dir, ok := targetDir(rest); ok { // mv -t dir file...
			w.target(dir + "/")
		} else if len(rest) > 1 {
			w.target(rest[len(rest)-1])
		}
	case "dd":
		for _, a := range rest {
			if of, ok := strings.CutPrefix(a, "of="); ok {
				w.target(of)
			}
		}
	case "sed", "perl":
		for _, f := range edited(base(args[0]), rest) {
			w.target(f)
		}
	}
}

// targetDir is the folder of cp, mv or install -t: -t dir, -tdir, -at dir, --target-directory dir, --target-directory=dir
// or a prefix of it GNU takes (--target-dir, --t). Options end at --.
func targetDir(args []string) (string, bool) {
	for i, a := range args {
		long, v, eq := strings.Cut(a, "=")
		switch {
		case a == "--":
			return "", false
		case len(long) > 2 && strings.HasPrefix("--target-directory", long):
			if eq {
				return v, true
			}
			if i+1 < len(args) {
				return args[i+1], true
			}
		case strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--"):
			for j := 1; j < len(a); j++ {
				if a[j] == 't' && j+1 < len(a) {
					return a[j+1:], true
				} else if a[j] == 't' && i+1 < len(args) {
					return args[i+1], true
				}
				if strings.IndexByte("Smog", a[j]) >= 0 { // -S suffix, install -m 644: the rest is its value
					break
				}
			}
		}
	}
	return "", false
}

// setAny: a computed word names the variable a command sets, so every variable may now hold anything (review round 10).
func (w *walker) setAny() {
	w.anyVar = true
	for k := range w.vars {
		w.vars[k] = unknown
	}
}

func (w *walker) assign(a *syntax.Assign) {
	w.subs(a.Value) // export "$(...)" runs it too
	// export $(...) or "$X": the word may set any variable (review rounds 9 and 10); declare -a is an option
	if a.Name == nil {
		// export "D=$HOME/x": a quoted word is not split, so it sets the one variable it names (review round 11)
		// A brace outside quotes may expand to several words: export "D="{/tmp,~/.crius/bus} (review round 14).
		quoted := !slices.ContainsFunc(a.Value.Parts, func(p syntax.WordPart) bool {
			_, lit := p.(*syntax.Lit)
			_, sgl := p.(*syntax.SglQuoted)
			d, dbl := p.(*syntax.DblQuoted)
			return !lit && !sgl && !dbl || braces(p) || dbl && slices.ContainsFunc(d.Parts, splits)
		})
		if n, v, ok := strings.Cut(w.resolve(a.Value), "="); quoted && ok && nameRe.MatchString(n) {
			w.vars[n] = v
			return
		}
		if o, ok := literal(a.Value); !ok || !strings.HasPrefix(o, "-") {
			w.setAny()
		}
	}
	if a.Name == nil || a.Naked {
		return
	}
	if w.allExport {
		w.exported[a.Name.Value] = true
	}
	v := w.resolve(a.Value)
	switch {
	case a.Array != nil:
		for _, e := range a.Array.Elems {
			w.subs(e.Value)
		}
		v = unknown
	case a.Index != nil:
		v = unknown
	case a.Append:
		old, ok := w.vars[a.Name.Value]
		if !ok {
			old = "$" + a.Name.Value
		}
		v = old + v
	}
	w.vars[a.Name.Value] = v
}

// braces says whether a part outside quotes holds a brace, which may expand to several words.
func braces(p syntax.WordPart) bool {
	l, ok := p.(*syntax.Lit)
	return ok && strings.Contains(l.Value, "{")
}

// cd follows a move to a folder it can name; after "cd -" the folder is unknown.
func (w *walker) cd(args []string) {
	dir := "~"
	for i, a := range args { // cd -P dir, cd -- dir
		if a == "--" {
			if i+1 < len(args) {
				dir = args[i+1]
			}
			break
		}
		if a == "-" || !strings.HasPrefix(a, "-") {
			dir = a
			break
		}
	}
	switch {
	case dir == "-":
		w.cwd = unknown
		return
	// cd "$(...)/x": a computed folder is not a child of this one, and the text after it is kept (review round 10)
	case strings.HasPrefix(dir, "/"), strings.HasPrefix(dir, "~"), strings.HasPrefix(dir, "$"), strings.HasPrefix(dir, unknown),
		w.cwd == "":
	default:
		dir = w.cwd + "/" + dir
	}
	// After a computed part the folder is the run's, so it is the known folder before that part: a ".." after it must
	// not cancel it, and the text after it is kept as written (cd ~/x/$(...)/.., review rounds 8 and 9).
	if known, rest, cut := strings.Cut(dir, unknown); !cut {
		w.cwd = path.Clean(dir)
	} else if known == "" {
		w.cwd = unknown + rest
	} else if strings.HasSuffix(known, newDir) { // a mktemp name: the marker and its computed part are one name
		w.cwd = path.Clean(known) + unknown + rest
	} else {
		w.cwd = path.Clean(known) + "/" + unknown + rest
	}
}

// script checks the text a shell or eval runs, in the folder and with the variables reached so far.
func (w *walker) script(text string) {
	switch {
	case w.why != "":
	case strings.Contains(text, unknown): // a computed part may be any command
		w.refuseUnknown()
	default:
		w.why = (&walker{cwd: w.cwd, vars: maps.Clone(w.vars), exported: maps.Clone(w.exported), allExport: w.allExport,
			named: w.named || namesBus(text), anyVar: w.anyVar, globSubst: true, unsent: w.unsent}).check(text)
	}
}

// stdin checks the script a shell reads on stdin: a here-document or here-string; from a pipe or a file it is unknown.
func (w *walker) stdin(redirs []*syntax.Redirect) {
	for _, r := range redirs {
		switch r.Op {
		case syntax.Hdoc, syntax.DashHdoc:
			if d, ok := literal(r.Word); ok && d == print(r.Word) { // <<EOF expands; <<'EOF' and <<\EOF keep the text
				w.script(w.expand(r.Hdoc, true))
			} else {
				w.script(print(r.Hdoc))
			}
			return
		case syntax.WordHdoc:
			w.script(w.resolve(r.Word))
			return
		}
	}
	w.script(unknown)
}

func (w *walker) refuseUnknown() {
	if w.named {
		w.why = rawWrite
	}
}

// target refuses a write to a path in the bus folder: relative to the folder cd reached, with // and /./ cleaned away.
func (w *walker) target(p string) {
	if p == "" || w.why != "" {
		return
	}
	// A computed path ("$L" from $(ls ...)) may be absolute or relative to the folder cd reached, so it is judged both
	// ways: a cd to ~/Projects/x does not make it a child of x (review round 6).
	if strings.HasPrefix(p, unknown) {
		if w.cwd != "" && !strings.HasPrefix(w.cwd, unknown) {
			w.judge(w.cwd + "/" + p)
		}
		if w.why == "" {
			w.refuseUnknown()
		}
		return
	}
	if !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "~") && !strings.HasPrefix(p, "$") && w.cwd != "" {
		p = w.cwd + "/" + p
	}
	w.judge(p)
}

// judge refuses a write to a path that names the bus folder, or may.
func (w *walker) judge(p string) {
	known, rest, cut := strings.Cut(p, unknown)
	if !cut {
		if busRe.MatchString(path.Clean(p)) {
			w.why = rawWrite
		}
		return
	}
	// The rest of the path only the run shows, so it is judged by the known folder before it: "/tmp/n-$(date +%s).txt"
	// is not the bus, "~/.crius/$(...)" may be, and "~/$(...)" or a relative path in an unknown folder may be.
	dir, _ := path.Split(known)
	d := path.Clean(dir)
	for _, h := range []string{"$HOME", "${HOME}"} {
		if rest, ok := strings.CutPrefix(d, h); ok && (rest == "" || rest[0] == '/') {
			d = "~" + rest
		}
	}
	// A mktemp name and then a path in it: no other computed part, which may climb, and no name glued to an empty one.
	// The path is known but for that one name: in it, it is a new folder; out of it with "..", the path is known.
	if t, ok := strings.CutSuffix(known, newDir); ok && strings.HasPrefix(rest, "/") && !strings.Contains(rest, unknown) {
		full := path.Clean(t + tempName + rest)
		before, _, in := strings.Cut(full, tempName)
		if !in && busRe.MatchString(full) || in && (busRe.MatchString(before) || strings.EqualFold(path.Base(before), ".crius")) {
			w.why = rawWrite
		}
		return
	}
	climbs := slices.Contains(strings.Split(rest, "/"), "..")
	switch {
	// The computed part may climb anywhere (/tmp/$(...)/.crius/bus, review round 9).
	case busRe.MatchString(rest):
		w.why = rawWrite
	case dir == "":
		w.refuseUnknown()
	case busRe.MatchString(d), strings.EqualFold(path.Base(d), ".crius"):
		w.why = rawWrite
	case d == "/", strings.HasPrefix("~/.crius/bus", fold(d)+"/"),
		homeDir != "" && strings.HasPrefix(fold(homeDir+"/.crius/bus"), fold(d)+"/"),
		climbs: // climbs out of a folder only the run shows
		w.refuseUnknown()
	}
}

var homeDir, _ = os.UserHomeDir()

// subs checks the commands in a word's $(...) and <(...).
func (w *walker) subs(word *syntax.Word) {
	if word == nil {
		return
	}
	w.nested(word)
}

// nested checks the commands in each $(...) and <(...) under n, in a child shell.
func (w *walker) nested(n syntax.Node) {
	syntax.Walk(n, func(n syntax.Node) bool {
		var ss []*syntax.Stmt
		switch n := n.(type) {
		case *syntax.CmdSubst:
			ss = n.Stmts
		case *syntax.ProcSubst:
			ss = n.Stmts
		default:
			return true
		}
		cwd, vars := w.cwd, maps.Clone(w.vars)
		w.stmts(ss)
		w.cwd, w.vars = cwd, vars
		return false
	})
}

// resolve gives a word's text with the variables the command set put in and other variables as written; an unset
// variable's default (${D:-x}) is taken as its value. A $(...), `...`, $1, $@, ${a[i]}, ${!a} and an operation on a
// set variable (${a%x}) are unknown; $(mktemp) is an unknown name in its folder.
func (w *walker) resolve(word *syntax.Word) string { return w.expand(word, false) }

// expand is resolve, with a here-document's backslash rules when hdoc is set.
func (w *walker) expand(word *syntax.Word, hdoc bool) string {
	if word == nil {
		return ""
	}
	var b strings.Builder
	var part func(p syntax.WordPart, escapes string)
	part = func(p syntax.WordPart, escapes string) {
		switch p := p.(type) {
		case *syntax.Lit:
			b.WriteString(unescape(p.Value, escapes))
		case *syntax.SglQuoted:
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			for _, q := range p.Parts {
				part(q, quoted)
			}
		case *syntax.ParamExp:
			v, ok := w.vars[p.Param.Value]
			switch {
			case p.Index != nil || p.Excl || strings.Trim(p.Param.Value, "0123456789@*") == "",
				ok && (p.Slice != nil || p.Repl != nil || p.Exp != nil):
				b.WriteString(unknown)
			case ok:
				b.WriteString(v)
			case p.Exp != nil && p.Exp.Word != nil && (p.Exp.Op == syntax.DefaultUnset || p.Exp.Op == syntax.DefaultUnsetOrNull ||
				p.Exp.Op == syntax.AssignUnset || p.Exp.Op == syntax.AssignUnsetOrNull):
				start := b.Len()
				for _, q := range p.Exp.Word.Parts {
					part(q, escapes)
				}
				if p.Exp.Op == syntax.AssignUnset || p.Exp.Op == syntax.AssignUnsetOrNull { // ${X:=d} also sets X
					w.vars[p.Param.Value] = b.String()[start:]
				}
			case w.anyVar:
				b.WriteString(unknown)
			default:
				b.WriteString(print(p))
			}
		case *syntax.CmdSubst:
			if dir := w.tempDir(p); dir != "" {
				b.WriteString(dir + newDir)
			}
			b.WriteString(unknown)
		default:
			b.WriteString(print(p))
		}
	}
	escapes := ""
	if hdoc {
		escapes = hereDoc
	}
	for _, p := range word.Parts {
		part(p, escapes)
	}
	return b.String()
}

// The characters a backslash escapes inside double quotes and inside an unquoted here-document; outside both it
// escapes any character.
const (
	quoted  = "$`\"\\\n"
	hereDoc = "$`\\\n"
)

// unescape drops the backslashes bash removes from a word: before the characters in escapes, or before any when it
// is empty.
func unescape(s, escapes string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && (escapes == "" || strings.IndexByte(escapes, s[i+1]) >= 0) {
			i++
			if s[i] == '\n' {
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// tempDir gives the folder a $(...) that is a mktemp (-d, -q, -u, -t prefix, 2>/dev/null) names a new file in: a temp
// folder, or the folder of its one absolute template; "" for any other $(...).
func (w *walker) tempDir(c *syntax.CmdSubst) string {
	if len(c.Stmts) != 1 {
		return ""
	}
	for _, r := range c.Stmts[0].Redirs {
		if r.Op != syntax.RdrOut || r.N == nil || r.N.Value != "2" || w.resolve(r.Word) != "/dev/null" {
			return ""
		}
	}
	call, ok := c.Stmts[0].Cmd.(*syntax.CallExpr)
	if !ok || len(call.Assigns) > 0 || len(call.Args) == 0 || base(call.Args[0]) != "mktemp" {
		return ""
	}
	dir := "/tmp/"
	if v, ok := w.vars["TMPDIR"]; ok { // with no template mktemp makes the file in $TMPDIR
		dir = strings.TrimSuffix(v, "/") + "/"
	}
	for i, templates := 1, 0; i < len(call.Args); i++ {
		switch a := w.resolve(call.Args[i]); {
		case a == "-d", a == "-q", a == "-u":
		case a == "-t":
			i++
		case templates == 0 && (strings.HasPrefix(a, "/") || strings.HasPrefix(a, "~")) && !strings.Contains(a, unknown):
			templates++
			dir, _ = path.Split(a)
		default:
			return ""
		}
	}
	return dir
}

// hiddenDump reports whether the words after env -C dir or env -S list every variable: env -S printenv, and env -C
// keeps its folder as the first word, so the command is the first or second word, behind more wrappers (env -C /tmp
// nohup printenv, env -C /tmp env -C /tmp printenv: second and third reviews).
func (w *walker) hiddenDump(args []*syntax.Word) bool {
	for i := range min(2, len(args)) {
		cmd, _, hides, _, last := unwrap(args[i:])
		if len(cmd) == 0 && (last == "env" || last == "genv") {
			return true
		}
		if len(cmd) == 0 {
			continue
		}
		if hides && len(cmd) < len(args[i:]) && w.hiddenDump(cmd) {
			return true
		}
		var rest []string
		for _, a := range cmd[1:] {
			rest = append(rest, w.resolve(a))
		}
		if lists(base(cmd[0]), rest) {
			return true
		}
	}
	return false
}

// bashForm gives a zshRe match as bash writes it: ${(k)x} and ${=x} as ${x}, =( as <(, *(.) as *.
func bashForm(m string) string {
	if strings.HasPrefix(m, "${") {
		return "${"
	}
	if strings.HasSuffix(m, ")") {
		return m[:strings.LastIndex(m, "(")]
	}
	return strings.TrimSuffix(m, "=(") + "<("
}

// checkText judges a command the parser cannot read. Bash runs the lines before a syntax error's line (a cp, a sed -i),
// so a command naming the bus folder with an error past line 1 is refused, and on line 1 when it holds > or tee.
func checkText(text string, err error, globSubst bool) string {
	if strings.Contains(text, "team-wait.sh") && loneAmpRe.MatchString(text) {
		return detached
	}
	// A command that prints every variable is refused in zsh text too, read as bash: quoted text and a heredoc stay text
	// (second review of the rule). Text still unreadable gets no such check (a known gap). When the bash form reads and
	// changes no command or value (${=x} splits, ${(k)x} lists keys), the walker judges it whole (10-29: a for loop with
	// set -- ${=pair} writing run logs was refused as a bus write); any form not in zshSafeRe keeps the text rules, and
	// so does all text when glob_subst may be on: ${=x} then runs a qualifier's e: code from the value.
	same := !globSubst
	b := zshRe.ReplaceAllStringFunc(text, func(m string) string {
		if !zshSafeRe.MatchString(m) {
			same = false
		}
		return bashForm(m)
	})
	if b != text {
		if _, err := syntax.NewParser().Parse(strings.NewReader(b), ""); err == nil && same {
			return check(b)
		}
		if why := check(b); why == dump || why == untargeted {
			return why
		}
	}
	if !namesBus(text) {
		return ""
	}
	if pe, ok := err.(syntax.ParseError); !ok || pe.Pos.Line() > 1 {
		return unparsed
	}
	if writeRe.MatchString(text) {
		return rawWrite
	}
	return ""
}

// shellScript gives the script word of `sh -c script` (also -lc, -ec, --norc -c, -c -- script), or nil.
func shellScript(args []*syntax.Word) *syntax.Word {
	for i := 1; i < len(args); i++ {
		a, _ := literal(args[i])
		switch {
		case a == "-o", a == "+o", a == "-O", a == "+O", a == "--rcfile", a == "--init-file":
			i++
		case strings.HasPrefix(a, "--"), strings.HasPrefix(a, "+"):
		case strings.HasPrefix(a, "-") && strings.Contains(a, "c"):
			if n, _ := literal(args[min(i+1, len(args)-1)]); n == "--" {
				i++
			}
			if i+1 < len(args) {
				return args[i+1]
			}
			return nil
		case !strings.HasPrefix(a, "-"):
			return nil // the script file; later words are its arguments
		}
	}
	return nil
}

// readsStdin reports whether a shell with no -c script reads its script on stdin: it names no script file, or has -s.
func readsStdin(args []*syntax.Word) bool {
	for i := 1; i < len(args); i++ {
		a, _ := literal(args[i])
		switch {
		case a == "-o", a == "+o", a == "-O", a == "+O", a == "--rcfile", a == "--init-file":
			i++
		case a == "--":
			return i+1 == len(args)
		case strings.HasPrefix(a, "--"), strings.HasPrefix(a, "+"):
		case strings.HasPrefix(a, "-") && strings.Contains(a, "s"):
			return true
		case !strings.HasPrefix(a, "-"):
			return false
		}
	}
	return true
}

// edited gives the files sed or perl changes in place (-i, -i.bak, BSD sed -i with an empty word, perl -pi): the
// words that are not options, less the script a -e (sed -f, perl -E) names or, without one, the first such word.
func edited(tool string, args []string) []string {
	var files []string
	inPlace, script := false, false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			files = append(files, args[i+1:]...)
			i = len(args)
		case strings.HasPrefix(a, "--"):
			name, _, eq := strings.Cut(a[2:], "=")
			switch name {
			case "in-place":
				inPlace = true
			case "expression", "file":
				script = true
				if !eq {
					i++
				}
			}
		case strings.HasPrefix(a, "-") && len(a) > 1:
			for j := 1; j < len(a); j++ {
				c := a[j]
				if c == 'i' { // the rest of the word is the backup suffix; BSD sed takes it as the next word
					inPlace = true
					if tool == "sed" && j == len(a)-1 && i+1 < len(args) && (args[i+1] == "" || strings.HasPrefix(args[i+1], ".")) {
						i++
					}
					break
				}
				if c == 'e' || tool == "sed" && c == 'f' || tool == "perl" && c == 'E' { // the script: the rest or the next word
					script = true
					if j == len(a)-1 {
						i++
					}
					break
				}
			}
		default:
			files = append(files, a)
		}
	}
	if !inPlace {
		return nil
	}
	if !script && len(files) > 0 {
		files = files[1:]
	}
	return files
}

const (
	detached = "team-wait.sh must not run detached with '&': a detached waiter cannot wake this session. " +
		"Run it with the Bash tool's run_in_background: true instead."
	rawWrite = "a line goes onto the bus only through bus-post (~/.crius/bin/bus-post <log> <TYPE> <project:role> " +
		"<to> <text>): it stamps the time and checks the line parses (a bus type, a project:role sender, recipients " +
		"\"a, b\" or \"a (cc b)\") and, for VERIFIED, the run log."
	unparsed = "this command names .crius/bus but its shell syntax cannot be read, so it may write a bus line: fix the " +
		"syntax or split the command. A line goes onto the bus only through bus-post."
	dump = "this command prints every variable with its value (env, printenv, set, export, declare, typeset, local or " +
		"readonly with no name): a token in the environment would go into the transcript and so to the API. Print names " +
		"only (zsh: print -l ${(k)parameters}; bash: compgen -v) and test one value with [ -n \"$X\" ]."
	unslotted = "a docker image build (docker build, buildx build or bake, compose build, compose up, run or create " +
		"with --build) runs alone in a heavy slot: run it as HEAVY_SLOTS=1 bun " +
		"~/Projects/Appxite/svc_quote/channels/poc/ui-app/scripts/heavy.ts run -- <command>. HEAVY_SLOTS=1 waits until " +
		"no heavy job holds a slot, so two builds never run at once. Build in the slot (compose build or up -d --build), " +
		"then start the stack outside it: the slot is held while the command runs, and a job past HEAVY_MAX_S (3600 s) " +
		"is stopped (10-30 20:22: a compose build of the AXI stack held " +
		"the Docker VM at 11 cores and docker and az stopped answering)."
	untargeted = "cmux send and send-key need --workspace <ref> and --surface <ref> written as separate literal words: " +
		"without them the text goes to this session's own tab, and zsh does not split $S into several words."
	unsent = "cmux send typed text into %s with no cmux send-key --workspace <ref> --surface <same ref> Enter after it " +
		"in this command, so the text sits unsent in that tab (axi 10-30: 18:16 to 21:45). Add the send-key Enter to " +
		"the same surface after the send, in the same command."
)

// cmuxValued and cmuxFlags: cmux's global options that take a value, and those that take none (cmux --help).
var (
	cmuxValued = map[string]bool{"--socket": true, "--password": true, "--id-format": true}
	cmuxFlags  = map[string]bool{"--json": true, "--skill": true, "--help": true, "-h": true, "--version": true}
)

// targeted says whether cmux's arguments name both a workspace and a surface for send and send-key: the flags as literal
// words, each followed by a value, before any --. Other subcommands, one only the run shows, and --help pass. A word
// after a global option neither list names may be its value, so the words after it are read too (review of a648553:
// cmux --socket /path send-key enter).
func targeted(args []*syntax.Word) bool {
	sub, a := cmuxSend(args)
	return sub == "" || a.help || a.has["--workspace"] && a.has["--surface"]
}

// cmuxSend finds a send or send-key among cmux's arguments: the subcommand ("" for any other, or one only the run
// shows) and what follows it.
func cmuxSend(args []*syntax.Word) (string, sendArgs) {
	maybeValue := false
	for i := 0; i < len(args); i++ {
		sub, ok := literal(args[i])
		if !ok || sub == "--" {
			return "", sendArgs{}
		}
		if strings.HasPrefix(sub, "-") {
			if maybeValue = !strings.Contains(sub, "=") && !cmuxFlags[sub]; cmuxValued[sub] {
				i++
				maybeValue = false
			}
			continue
		}
		if sub != "send" && sub != "send-key" {
			if maybeValue {
				maybeValue = false
				continue
			}
			return "", sendArgs{}
		}
		return sub, readSend(args[i+1:])
	}
	return "", sendArgs{}
}

// sendArgs is what follows a send or send-key: the target flags given a value, the --surface value as written, the
// first word that is no option (send-key's key), and whether it asks for help (which types nothing).
type sendArgs struct {
	has           map[string]bool
	surface, word string
	help          bool
}

func readSend(rest []*syntax.Word) sendArgs {
	a := sendArgs{has: map[string]bool{}}
	for j := 0; j < len(rest); j++ {
		f, _ := literal(rest[j])
		if f == "--" {
			if j+1 < len(rest) && a.word == "" {
				a.word, _ = literal(rest[j+1])
			}
			break
		}
		a.help = a.help || f == "--help" || f == "-h"
		switch {
		case (f == "--workspace" || f == "--surface" || f == "--window") && j+1 < len(rest):
			if v, ok := literal(rest[j+1]); !ok || !strings.HasPrefix(v, "-") {
				a.has[f] = true
				if f == "--surface" {
					a.surface = print(rest[j+1])
				}
				j++
			}
		case !strings.HasPrefix(f, "-") && a.word == "":
			a.word = f
		}
	}
	return a
}

// unenter notes a cmux send's surface in unsent, and takes it off at a send-key Enter (or Return) to the same surface.
func (w *walker) unenter(args []*syntax.Word) {
	sub, a := cmuxSend(args)
	switch {
	case sub == "" || a.help:
	case sub == "send":
		w.unsent[a.surface] = true
	case strings.EqualFold(a.word, "enter") || strings.EqualFold(a.word, "return"):
		delete(w.unsent, a.surface)
	}
}

// dockerValued, composeValued and buildxValued: the options of docker, docker compose and docker buildx that take the
// next word as their value (docker --help); a value is never read as the subcommand.
var (
	dockerValued = map[string]bool{"-c": true, "--context": true, "--config": true, "-H": true, "--host": true, "-l": true,
		"--log-level": true, "--tlscacert": true, "--tlscert": true, "--tlskey": true}
	composeValued = map[string]bool{"-f": true, "--file": true, "-p": true, "--project-name": true, "--profile": true,
		"--env-file": true, "--project-directory": true, "--workdir": true, "--ansi": true, "--progress": true,
		"--parallel": true}
	// docker-compose (v1) also takes docker's connection options: -H, --context, --log-level, --tls*
	composeV1Valued = merge(composeValued, dockerValued)
	buildxValued    = map[string]bool{"--builder": true}
	// runValued: compose run options with a value, read before its service (-e A=1 is no service)
	runValued = map[string]bool{"-e": true, "--env": true, "-l": true, "--label": true, "-p": true, "--publish": true,
		"-v": true, "--volume": true, "-w": true, "--workdir": true, "-u": true, "--user": true, "--name": true,
		"--entrypoint": true, "--env-from-file": true, "--cap-add": true, "--cap-drop": true, "--pull": true}
	// computed stands for a word only the run shows: after an option it is the value (-f "$F"); where the subcommand goes
	// it ends the reading
	computed = "\x00computed\x00"
)

func merge(a, b map[string]bool) map[string]bool {
	m := maps.Clone(a)
	maps.Copy(m, b)
	return m
}

// builds says whether a docker or docker-compose command (args[0] is the program, after its wrappers) builds images:
// docker build, docker buildx, image or builder build (and bake), docker compose build, and compose up, create or run
// with --build before its service. --help and --dry-run build nothing, nor does bake --print. A word only the run
// shows is no subcommand.
func builds(args []*syntax.Word) bool {
	args, _, _, _, _ = unwrap(args)
	if len(args) == 0 {
		return false
	}
	prog := base(args[0])
	if prog != "docker" && prog != "docker-compose" {
		return false
	}
	var words []string
	for _, a := range args[1:] {
		v, ok := literal(a)
		if !ok {
			v = computed
			// an option with a computed value glued on (--file="$F", -p"$P") stays an option that takes no next word
			if l, lit := a.Parts[0].(*syntax.Lit); lit && strings.HasPrefix(l.Value, "-") {
				v = l.Value + computed
			}
		}
		if v == "--help" || v == "--dry-run" || v == "--print" { // not -h: compose run api df -h is a build
			return false
		}
		words = append(words, v)
	}
	if prog == "docker-compose" {
		return composeBuilds(words, composeV1Valued)
	}
	sub, rest := subcommand(words, dockerValued)
	switch sub {
	case "build":
		return true
	case "compose":
		return composeBuilds(rest, composeValued)
	case "buildx", "image", "builder":
		sub, _ = subcommand(rest, buildxValued)
		return sub == "build" || sub == "b" || sub == "bake"
	}
	return false
}

// composeBuilds reads the words after docker compose (or docker-compose), whose global options with a value are valued.
func composeBuilds(words []string, valued map[string]bool) bool {
	sub, rest := subcommand(words, valued)
	switch sub {
	case "build":
		return true
	case "up", "create":
		return slices.Contains(rest, "--build")
	case "run": // its options, then the service and the container's own command
		for i := 0; i < len(rest); i++ {
			switch o := rest[i]; {
			case o == "--build":
				return true
			case o == "--" || !strings.HasPrefix(o, "-"):
				return false
			case runValued[o]:
				i++
			}
		}
	}
	return false
}

// subcommand is the first word that is no option, skipping the value of an option in valued, and the words after it.
func subcommand(words []string, valued map[string]bool) (string, []string) {
	for i := 0; i < len(words); i++ {
		switch o := words[i]; {
		case o == "--":
			if i+1 < len(words) {
				return words[i+1], words[i+2:]
			}
			return "", nil
		case strings.HasPrefix(o, "-"):
			if valued[o] {
				i++
			}
		case o == computed: // docker $C build: the subcommand is only known when it runs
			return "", nil
		default:
			return o, words[i+1:]
		}
	}
	return "", nil
}

// heavyJob is the command a bun or node run of heavy.ts runs: the words after "heavy.ts run --" (the -- may be left out).
func heavyJob(args []*syntax.Word) []*syntax.Word {
	for i, a := range args[1:] {
		if base(a) != "heavy.ts" {
			continue
		}
		rest := args[i+2:]
		if len(rest) == 0 {
			return nil
		}
		if v, _ := literal(rest[0]); v != "run" {
			return nil
		}
		rest = rest[1:]
		if len(rest) > 0 {
			if v, _ := literal(rest[0]); v == "--" {
				rest = rest[1:]
			}
		}
		return rest
	}
	return nil
}

// lists reports whether a command with these words after its name prints every variable with its value: printenv or
// set with no name, or a declaration builtin with options only (declare -f and -F list functions, not values). A lone
// - or + is no option: local - keeps set options inside a function, typeset + lists names only.
func lists(name string, words []string) bool {
	opts := !slices.ContainsFunc(words, func(s string) bool {
		return s == "-" || s == "+" || !strings.HasPrefix(s, "-") && !strings.HasPrefix(s, "+")
	})
	switch name {
	case "set":
		return len(words) == 0
	case "printenv", "gprintenv":
		return opts
	case "export", "declare", "typeset", "local", "readonly":
		return opts && !slices.ContainsFunc(words, func(s string) bool { return strings.ContainsAny(s, "fF") })
	}
	return false
}

// runsWaiter reports whether stmt runs team-wait.sh itself, or a shell on it (not a grep that names it).
func runsWaiter(stmt *syntax.Stmt) bool {
	found := false
	syntax.Walk(stmt, func(n syntax.Node) bool {
		if c, ok := n.(*syntax.CallExpr); ok {
			args, _, _, _, _ := unwrap(c.Args)
			if len(args) > 0 && base(args[0]) == "team-wait.sh" {
				found = true
			}
			if len(args) > 1 && (base(args[0]) == "bash" || base(args[0]) == "sh" || base(args[0]) == "zsh") {
				script := shellScript(args)
				found = found || base(args[1]) == "team-wait.sh" || (script != nil && strings.Contains(print(script), "team-wait.sh"))
			}
		}
		return !found
	})
	return found
}

var (
	wrappers = map[string]bool{"command": true, "env": true, "nohup": true, "exec": true, "sudo": true, "timeout": true,
		"caffeinate": true, "nice": true, "setsid": true, "xargs": true, "gtimeout": true, "genv": true, "time": true,
		"builtin": true}
	envRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
)

var (
	// valued: a wrapper's short options that take the next word as their value (env -u X, timeout -s KILL, review round 10)
	valued = map[string]string{"env": "uPa", "exec": "a", "timeout": "sk", "nice": "n", "sudo": "aCcghpRrTtUuD",
		"xargs": "aEdILnPs", "caffeinate": "tw", "gtimeout": "sk", "genv": "uPa", "time": "f"}
	// opaque: options that run the command somewhere or as something the words do not show (env -C dir, env -S "line");
	// time -o writes a file the words name only as an option.
	opaque = map[string]string{"env": "CS", "sudo": "DR", "genv": "CS", "time": "o"}
	// Long options by wrapper; GNU takes any unique prefix of one (timeout --sig KILL, review round 11).
	valuedLong = map[string][]string{"env": {"--unset", "--argv0"}, "timeout": {"--signal", "--kill-after"}, "nice": {"--adjustment"},
		"sudo": {"--user", "--group", "--prompt", "--host", "--role", "--type", "--close-from", "--command-timeout",
			"--other-user", "--auth-type"},
		"xargs":    {"--arg-file", "--delimiter", "--max-args", "--max-procs", "--max-chars", "--process-slot-var"},
		"gtimeout": {"--signal", "--kill-after"}, "genv": {"--unset", "--argv0"}, "time": {"--format"}}
	opaqueLong = map[string][]string{"env": {"--chdir", "--split-string"}, "sudo": {"--chdir", "--chroot"},
		"genv": {"--chdir", "--split-string"}, "time": {"--output"}}
)

// option reads one option word of a wrapper: whether the next word is its value, and whether it hides the command.
func option(wrapper, o string) (next, hides bool) {
	if strings.HasPrefix(o, "--") {
		long, _, eq := strings.Cut(o, "=")
		prefix := func(n string) bool { return strings.HasPrefix(n, long) }
		return slices.ContainsFunc(valuedLong[wrapper], prefix) && !eq, slices.ContainsFunc(opaqueLong[wrapper], prefix)
	}
	for i := 1; i < len(o); i++ {
		if strings.IndexByte(opaque[wrapper], o[i]) >= 0 {
			return false, true
		}
		if strings.IndexByte(valued[wrapper], o[i]) >= 0 {
			return i == len(o)-1, false // -uX holds its value
		}
	}
	return false, false
}

// unwrap drops leading words that run the next word as the command: command, env (and its NAME=value), nohup, ...
// hides: a wrapper option runs it in a folder or line the words do not show (env -C dir, env -S "sh -c ...").
// child: a wrapper other than command or builtin runs it as a program, so a builtin such as cd cannot move the shell.
// last: the last wrapper read; env with no command after it lists the environment.
func unwrap(args []*syntax.Word) (cmd, envs []*syntax.Word, hides, child bool, last string) {
	for len(args) > 0 {
		w, _ := literal(args[0])
		switch name := base(args[0]); {
		// env D=~/.crius/bus/env: a word with "=" is an assignment, whatever its last path part (review round 15)
		case strings.Contains(w, "=") && !strings.HasPrefix(w, "-"), envRe.MatchString(print(args[0])): // env D=$HOME/x
			envs = append(envs, args[0])
			args = args[1:]
		case wrappers[name]: // /usr/bin/env and ENV too (review rounds 12 and 13)
			args = args[1:]
			last = name
			child = child || name != "command" && name != "builtin"
			// timeout's first word after its options is the duration, in any form strtod reads: 5, .5, 1e2, inf
			dur := name == "timeout" || name == "gtimeout"
			for len(args) > 0 { // the wrapper's options: nice -n 5
				o, ok := literal(args[0])
				if !ok {
					o = print(args[0])
				}
				if o == "--" || !strings.HasPrefix(o, "-") && o != "{}" {
					if o == "--" {
						args = args[1:]
					}
					if dur && len(args) > 0 {
						args = args[1:]
					}
					break
				}
				args = args[1:]
				if name == "command" && strings.ContainsAny(o, "vV") { // command -v cd only prints (review round 15)
					return nil, envs, hides, child, last
				}
				if strings.HasPrefix(o, "-") {
					next, h := option(name, o)
					hides = hides || h
					if next && len(args) > 0 {
						args = args[1:]
					}
				}
			}
		default:
			return args, envs, hides, child, last
		}
	}
	return args, envs, hides, child, last
}

// splits says whether a part makes several words even inside double quotes: "$@", "${a[@]}", "${!x@}" (review round 12),
// also inside a default or alternate: "${X:-$@}" (review round 13), and zsh's "$a[@]" (review round 14). A $(...)
// inside is one word.
func splits(p syntax.WordPart) bool {
	found := false
	syntax.Walk(p, func(n syntax.Node) bool {
		if _, sub := n.(*syntax.CmdSubst); sub {
			return false
		}
		if l, ok := n.(*syntax.Lit); ok && strings.Contains(l.Value, "[@]") {
			found = true
		}
		pe, ok := n.(*syntax.ParamExp)
		found = found || ok && (pe.Param.Value == "@" || pe.Names != 0 || pe.Index != nil && print(pe.Index) == "@")
		return !found
	})
	return found
}

// base is the last path part of a word's literal value, as the disk names it: "/usr/bin/tee", "\"tee\"" and "TEE" are
// all tee (review round 13).
func base(w *syntax.Word) string {
	s, ok := literal(w)
	if !ok {
		return ""
	}
	return fold(path.Base(s))
}

// literal gives a word's value when it holds no expansion: plain, 'single' or "double" quoted parts.
func literal(w *syntax.Word) (string, bool) {
	var b strings.Builder
	for _, p := range w.Parts {
		switch p := p.(type) {
		case *syntax.Lit:
			b.WriteString(unescape(p.Value, ""))
		case *syntax.SglQuoted:
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			for _, q := range p.Parts {
				l, ok := q.(*syntax.Lit)
				if !ok {
					return "", false
				}
				b.WriteString(unescape(l.Value, quoted))
			}
		default:
			return "", false
		}
	}
	return b.String(), true
}

func print(n syntax.Node) string {
	var b strings.Builder
	syntax.NewPrinter().Print(&b, n)
	return b.String()
}
