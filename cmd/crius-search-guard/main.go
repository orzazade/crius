// Command crius-search-guard is a Claude Code PreToolUse hook for Bash and Grep, in both personas and their subagents.
// It refuses a content search that can print a config file into the transcript (10-28: an Explore subagent in
// RethinkBack printed two committed test secrets; 10-23: a cortex helper's git grep read Web.config). A config file
// is one whose name matches the owner's excludes *appsettings*, *.env*, *secrets* or *.config.
//
// A grep, rg, git grep or Grep tool search passes when it names all four excludes, prints file names or counts only
// (-l, -c, -q, the Grep tool's default mode), or reads no config file: the guard lists the files the search can read
// on disk (its folders, globs and named files, less its includes and excludes) and refuses only when one is a config
// file. It refuses when it cannot tell: a folder too big to list in time, a path or cd it cannot read ($DIR), a git
// revision, file names from xargs or find. An include list (--include, -g '*.cs') counts only as a filter on the
// files listed: appsettings.json is a *.json.
//
// It catches habits, not intent. Known gaps: other readers (ag, ack, awk, sed, cat, the Read tool), a search inside a
// script file, a function or an alias, git log -p and git show, and zsh text the bash parser cannot read when it names
// no search tool. Exit 2 refuses, with the reason on stderr; anything else passes.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"mvdan.cc/sh/v3/syntax"
)

type input struct {
	ToolName  string `json:"tool_name"`
	Cwd       string `json:"cwd"`
	ToolInput struct {
		Command    string `json:"command"`
		Pattern    string `json:"pattern"`
		Path       string `json:"path"`
		Glob       string `json:"glob"`
		OutputMode string `json:"output_mode"`
	} `json:"tool_input"`
}

func main() {
	var in input
	if err := json.NewDecoder(os.Stdin).Decode(&in); err != nil {
		fmt.Fprintln(os.Stderr, "crius-search-guard: unreadable hook input:", err)
		os.Exit(1) // a hook error, not a refusal: Claude Code shows it and runs the tool
	}
	if why := check(in); why != "" {
		fmt.Fprintln(os.Stderr, "Refused by crius-search-guard:", why)
		os.Exit(2)
	}
}

// configNames are the owner's excludes (~/.agents/AGENTS.md, Shell), as globs on a file name.
var configNames = []string{"*appsettings*", "*.env*", "*secrets*", "*.config"}

// isConfig says whether a path's file name is a config file's, in any case.
func isConfig(p string) bool {
	name := strings.ToLower(path.Base(filepath.ToSlash(p)))
	return slices.ContainsFunc(configNames, func(g string) bool { ok, _ := path.Match(g, name); return ok })
}

// pattern is one include or exclude glob as the tool reads it: rg's globs take {a,b} choices, grep's and git's do
// not; rg --iglob and git's icase ignore case, the rest do not.
type pattern struct {
	glob                  string
	include, braces, fold bool
	iglob                 bool // rg applies --iglob patterns after every -g
}

// alts gives the globs a pattern stands for, without a leading **/.
func (p pattern) alts() []string {
	g := strings.TrimPrefix(p.glob, "**/")
	if p.fold {
		g = strings.ToLower(g)
	}
	if !p.braces {
		return []string{g}
	}
	i, j := strings.IndexByte(g, '{'), strings.LastIndexByte(g, '}')
	if i < 0 || j < i {
		return []string{g}
	}
	var out []string
	for _, alt := range strings.Split(g[i+1:j], ",") {
		out = append(out, g[:i]+alt+g[j+1:])
	}
	return out
}

// matches says whether the pattern picks a file name. A glob with a / matches paths, which the guard does not
// follow: as an include it picks every file, as an exclude none.
func (p pattern) matches(name string) bool {
	if p.fold {
		name = strings.ToLower(name)
	}
	for _, g := range p.alts() {
		if strings.Contains(g, "/") {
			return p.include
		}
		if ok, _ := path.Match(g, name); ok {
			return true
		}
	}
	return false
}

// filter is a search's includes and excludes in the order written.
type filter []pattern

// reads says whether a search reads a listed file with this name: the last pattern that matches decides (rg and GNU
// grep; it reads at least what BSD grep reads, where an exclude wins). With none matching, a file is read unless the
// first pattern is an include.
func (f filter) reads(name string) bool {
	for i := len(f) - 1; i >= 0; i-- {
		if f[i].matches(name) {
			return f[i].include
		}
	}
	return len(f) == 0 || !f[0].include
}

// missing gives the config globs the filter does not exclude, as written; an include after the first of them may
// pick a config file again, so then all count as missing.
func (f filter) missing() []string {
	have := map[string]bool{}
	first := -1
	for i, p := range f {
		if p.include {
			continue
		}
		for _, g := range p.alts() {
			if slices.Contains(configNames, g) {
				have[g] = true
				if first < 0 {
					first = i
				}
			}
		}
	}
	if first >= 0 && slices.ContainsFunc(f[first:], func(p pattern) bool { return p.include }) {
		return configNames
	}
	var out []string
	for _, g := range configNames {
		if !have[g] {
			out = append(out, g)
		}
	}
	return out
}

func check(in input) string {
	switch in.ToolName {
	case "Bash":
		return checkBash(in.ToolInput.Command, []string{in.Cwd}, false)
	case "Grep":
		return checkGrepTool(in)
	}
	return ""
}

// The ways to add the excludes, per tool.
const (
	grepFix     = `--exclude='*appsettings*' --exclude='*.env*' --exclude='*secrets*' --exclude='*.config'`
	rgFix       = `-g '!*appsettings*' -g '!*.env*' -g '!*secrets*' -g '!*.config'`
	gitFix      = `-- . ':!*appsettings*' ':!*.env*' ':!*secrets*' ':!*.config'`
	grepToolFix = `glob: "!*appsettings* !*.env* !*secrets* !*.config" (your own globs go in the same string, space-separated)`
)

// unknownDir stands for a folder a cd moved to that the guard cannot read ($DIR). It holds a NUL, which no path can.
const unknownDir = "\x00unknown"

// search is what one command reads: paths (relative to every folder the command may be in), whether a folder is
// searched below, how a folder is listed, and the filter. unsure is why the guard cannot tell what it reads.
type search struct {
	tool, fix string
	dirs      []string
	recursive bool
	list      listing
	filter    filter
	unsure    string
	found     string
	named     string // a config file the command names: read whatever the filter says
}

// listing is how a tool reads a folder: every file (grep -r), the files git tracks (git grep), or the files git does
// not ignore (rg and the Grep tool). Outside a git work tree every listing is every file.
type listing int

const (
	everyFile listing = iota
	gitTracked
	gitVisible
)

// budget bounds the listing of one hook run: a folder bigger than this needs the excludes.
var budget = struct {
	entries  int
	deadline time.Time
}{}

var errBudget = errors.New("over budget")

// read records path p (a word the search names): a config file it reads, or a folder it lists below when recursive.
func (s *search) read(p string, glob bool) {
	if s.found != "" || s.unsure != "" || s.named != "" {
		return
	}
	for _, dir := range s.dirs {
		full := p
		if !filepath.IsAbs(p) {
			if dir == unknownDir {
				s.unsure = "it runs in a folder the guard cannot read (a cd to a variable)"
				return
			}
			full = filepath.Join(dir, p)
		}
		matches := []string{full}
		if glob {
			m, err := filepath.Glob(full)
			if err != nil || len(m) == 0 {
				s.unsure = fmt.Sprintf("the guard cannot list what %s names", p)
				return
			}
			matches = m
		}
		for _, m := range matches {
			s.readPath(m)
		}
	}
}

func (s *search) readPath(full string) {
	fi, err := os.Lstat(full)
	switch {
	case err != nil:
		return // nothing there to read
	case fi.Mode()&fs.ModeSymlink != 0 && s.recursive:
		if t, err := os.Stat(full); err == nil && t.IsDir() {
			s.unsure = full + " is a link to a folder"
		} else if isConfig(full) {
			s.named = full
		}
	case fi.IsDir():
		if !s.recursive {
			return
		}
		if s.list != everyFile {
			if files, err := gitFiles(full, s.list); err == nil {
				for _, rel := range files {
					if budget.entries--; budget.entries < 0 {
						s.unsure = fmt.Sprintf("%s is too big to list in time", full)
						return
					}
					if isConfig(rel) && s.filter.reads(path.Base(rel)) {
						s.found = filepath.Join(full, rel)
						return
					}
				}
				return
			}
		}
		err := filepath.WalkDir(full, func(p string, d fs.DirEntry, err error) error {
			budget.entries--
			if budget.entries < 0 || time.Now().After(budget.deadline) {
				return errBudget
			}
			if err != nil || d.IsDir() {
				return nil
			}
			if isConfig(p) && s.filter.reads(d.Name()) {
				s.found = p
				return fs.SkipAll
			}
			return nil
		})
		if err != nil {
			s.unsure = fmt.Sprintf("%s is too big to list in time", full)
		}
	default:
		// A file named on the command line: rg reads it whatever its globs say.
		if isConfig(full) {
			s.named = full
		}
	}
}

// gitFiles lists the files below dir that git tracks, or that it tracks or does not ignore, relative to dir.
func gitFiles(dir string, l listing) ([]string, error) {
	ctx, cancel := context.WithDeadline(context.Background(), budget.deadline)
	defer cancel()
	args := []string{"-C", dir, "ls-files", "-z"}
	if l == gitVisible {
		args = append(args, "--cached", "--others", "--exclude-standard")
	}
	out, err := exec.CommandContext(ctx, "git", append(args, "--", ".")...).Output()
	if err != nil {
		return nil, err
	}
	return strings.FieldsFunc(string(out), func(r rune) bool { return r == 0 }), nil
}

// verdict gives the refusal for a search, or "".
func (s *search) verdict() string {
	if s.named != "" {
		return fmt.Sprintf("this %s reads %s, a config file it names, and config files hold committed secrets "+
			"(10-28 RethinkBack). Leave config files out of searches.", s.tool, s.named)
	}
	if len(s.filter.missing()) == 0 {
		return ""
	}
	var why string
	switch {
	case s.found != "":
		why = fmt.Sprintf("this %s can print %s, a config file", s.tool, s.found)
	case s.unsure != "":
		why = fmt.Sprintf("the guard cannot tell whether this %s reads config files: %s", s.tool, s.unsure)
	default:
		return ""
	}
	return why + ", and config files hold committed secrets (10-28 RethinkBack). Add the excludes: " + s.fix +
		". Or search named files, or list file names only (-l)."
}

// checkGrepTool judges the Grep tool, which runs rg --hidden with each glob split as the tool splits it (on spaces,
// then on commas outside braces).
func checkGrepTool(in input) string {
	t := in.ToolInput
	if t.OutputMode != "content" {
		return ""
	}
	startBudget()
	s := &search{tool: "Grep", fix: grepToolFix, dirs: []string{in.Cwd}, recursive: true, list: gitVisible}
	for _, tok := range strings.Fields(t.Glob) {
		parts := []string{tok}
		if !strings.Contains(tok, "{") || !strings.Contains(tok, "}") {
			parts = strings.Split(tok, ",")
		}
		for _, g := range parts {
			if strings.HasPrefix(g, "!") {
				s.filter = append(s.filter, pattern{glob: g[1:], braces: true})
			} else if g != "" {
				s.filter = append(s.filter, pattern{glob: g, include: true, braces: true})
			}
		}
	}
	p := t.Path
	if p == "" {
		p = "."
	}
	s.read(p, false)
	return s.verdict()
}

// maxEntries is how many files and folders one hook run lists before it gives up.
var maxEntries = 50000

func startBudget() {
	budget.entries = maxEntries
	budget.deadline = time.Now().Add(1500 * time.Millisecond)
}

var searchWordRe = regexp.MustCompile(`(^|[\s;&|(/])(grep|egrep|fgrep|rg|git)\s`)

// checkBash judges every grep, rg and git grep in a command, also behind a wrapper (timeout, xargs, find -exec) and
// inside bash -c or eval text. dirs are the folders the command may be in: the start folder and each one a cd names,
// since a cd may sit in a subshell or a branch. fed: a wrapper (xargs, find) runs the text with file names of its own.
func checkBash(command string, dirs []string, fed bool) string {
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil {
		if searchWordRe.MatchString(command) {
			return "this command runs a search the guard cannot parse, so it cannot see whether config files are " +
				"excluded. Write it as plain bash."
		}
		return ""
	}
	startBudget()
	piped := map[*syntax.CallExpr]bool{}
	seq := map[*syntax.CallExpr]bool{}
	sequential(f.Stmts, seq)
	why := ""
	syntax.Walk(f, func(n syntax.Node) bool {
		if why != "" {
			return false
		}
		if b, ok := n.(*syntax.BinaryCmd); ok && (b.Op == syntax.Pipe || b.Op == syntax.PipeAll) {
			if c, ok := b.Y.Cmd.(*syntax.CallExpr); ok {
				piped[c] = true
			}
		}
		c, ok := n.(*syntax.CallExpr)
		if !ok || len(c.Args) == 0 {
			return true
		}
		args := make([]word, len(c.Args))
		for i, w := range c.Args {
			args[i] = literal(w)
		}
		why = checkCall(args, &dirs, piped[c], seq[c], fed)
		return true
	})
	return why
}

// word is one shell word: its text when it is literal (ok), and whether it holds an unquoted glob.
type word struct {
	text, src       string // src: the word as written
	ok, glob, split bool   // split: an unquoted $x or $(...) may be several words
}

func literal(w *syntax.Word) word {
	var b strings.Builder
	out := word{ok: true}
	for i, p := range w.Parts {
		switch p := p.(type) {
		case *syntax.Lit:
			v := p.Value
			if i == 0 && (v == "~" || strings.HasPrefix(v, "~/")) {
				home, _ := os.UserHomeDir()
				v = home + v[1:]
			}
			if strings.ContainsAny(v, "*?[") {
				out.glob = true
			}
			b.WriteString(unescape(v))
		case *syntax.SglQuoted:
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			for _, q := range p.Parts {
				if l, ok := q.(*syntax.Lit); ok {
					b.WriteString(l.Value)
				} else {
					out.ok = false
				}
			}
		default:
			out.ok, out.split = false, true
		}
	}
	out.text = b.String()
	var src bytes.Buffer
	syntax.NewPrinter().Print(&src, w)
	out.src = src.String()
	return out
}

func unescape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// wrappers run the command named in their arguments; fed marks those that hand it file names of their own.
var wrappers = map[string]bool{"timeout": false, "nice": false, "nohup": false, "command": false, "env": false,
	"sudo": false, "stdbuf": false, "caffeinate": false, "builtin": false, "exec": false,
	"xargs": true, "find": true, "parallel": true}

var shells = map[string]bool{"bash": true, "sh": true, "zsh": true, "dash": true, "ksh": true}

func checkCall(args []word, dirs *[]string, piped, seq, fed bool) string {
	if !args[0].ok {
		return ""
	}
	name := path.Base(args[0].text)
	switch {
	case name == "cd" || name == "pushd":
		// A cd moves the folder for the rest only when it surely runs and its folder exists; else it may or may not.
		to := cdTo(*dirs, args[1:])
		if seq && !slices.ContainsFunc(to, func(d string) bool { fi, err := os.Stat(d); return err != nil || !fi.IsDir() }) {
			*dirs = to
		} else {
			*dirs = append(*dirs, to...)
		}
		return ""
	case name == "eval":
		var parts []string
		for _, a := range args[1:] {
			if !a.ok {
				return ""
			}
			parts = append(parts, a.text)
		}
		return checkBash(strings.Join(parts, " "), *dirs, fed)
	case shells[name]:
		for i := 1; i+1 < len(args); i++ {
			a := args[i]
			if a.ok && strings.HasPrefix(a.text, "-") && !strings.HasPrefix(a.text, "--") && strings.Contains(a.text, "c") {
				if s := args[i+1]; s.ok {
					return checkBash(s.text, *dirs, fed)
				}
			}
		}
		return ""
	}
	if feeds, ok := wrappers[name]; ok {
		fed = fed || feeds
		for i := 1; i < len(args); i++ {
			if !args[i].ok {
				continue
			}
			switch n := path.Base(args[i].text); {
			case isSearch(n):
				return searchCall(args[i:], *dirs, fed, false)
			case shells[n] || n == "eval":
				return checkCall(args[i:], dirs, false, false, fed)
			}
		}
		return ""
	}
	if !isSearch(name) {
		return ""
	}
	return searchCall(args, *dirs, fed, piped)
}

// cdTo gives the folders a cd to args moves to from each of dirs; a folder it cannot read is unknownDir.
func cdTo(dirs []string, args []word) []string {
	for len(args) > 0 && args[0].ok && strings.HasPrefix(args[0].text, "-") && args[0].text != "-" {
		if strings.Trim(args[0].text, "-PLe@") != "" || strings.Count(args[0].text, "-") > 1 && args[0].text != "--" {
			return []string{unknownDir} // an option cd does not know: it fails and stays where it was
		}
		done := args[0].text == "--"
		args = args[1:] // -P, -L, -e, -@ and --
		if done {
			break
		}
	}
	switch {
	case len(args) == 0:
		home, _ := os.UserHomeDir()
		return []string{home}
	case !args[0].ok || args[0].glob || args[0].text == "-":
		return []string{unknownDir}
	case filepath.IsAbs(args[0].text):
		return []string{filepath.Clean(args[0].text)}
	}
	var out []string
	for _, d := range dirs {
		if d == unknownDir {
			out = append(out, d)
		} else {
			out = append(out, filepath.Join(d, args[0].text))
		}
	}
	return out
}

// sequential marks the commands that run in the command's own shell, one after another (; && and { }): a cd there
// moves the folder for the rest. A cd anywhere else (a subshell, a pipe, after ||, in a branch or a loop) may or may
// not have moved it.
func sequential(stmts []*syntax.Stmt, seq map[*syntax.CallExpr]bool) {
	for _, st := range stmts {
		chain(st, seq)
	}
}

// chain marks the commands of one && chain up to and including the first that is not a cd: a cd after any other
// command (false && cd src) may not run. It says whether every command it saw was a cd.
func chain(st *syntax.Stmt, seq map[*syntax.CallExpr]bool) bool {
	if st.Negated || st.Background || st.Coprocess {
		return false
	}
	switch c := st.Cmd.(type) {
	case *syntax.CallExpr:
		seq[c] = true
		return len(c.Args) > 0 && (c.Args[0].Lit() == "cd" || c.Args[0].Lit() == "pushd")
	case *syntax.Block:
		sequential(c.Stmts, seq)
	case *syntax.BinaryCmd:
		if c.Op == syntax.AndStmt && chain(c.X, seq) {
			return chain(c.Y, seq)
		}
	}
	return false
}

func isSearch(name string) bool {
	switch name {
	case "grep", "egrep", "fgrep", "ggrep", "rg", "git":
		return true
	}
	return false
}

// opts describes one tool's command line: the options that take a value, the options that print no file content,
// the options that set the pattern (so the first operand is a file), and the include and exclude options.
type opts struct {
	short     string          // short options that take a value
	long      map[string]bool // long options that take a value
	namesOnly map[string]bool
	patternBy map[string]bool
	glob      func(opt, val string) (pattern, bool)
}

func set(s ...string) map[string]bool {
	m := map[string]bool{}
	for _, x := range s {
		m[x] = true
	}
	return m
}

var grepOpts = opts{
	short: "efmABCdD",
	long: set("--regexp", "--file", "--max-count", "--after-context", "--before-context", "--context",
		"--directories", "--devices", "--include", "--exclude", "--exclude-dir", "--exclude-from", "--label",
		"--binary-files", "--group-separator"),
	namesOnly: set("-l", "-L", "-c", "-q", "--files-with-matches", "--files-without-match", "--count", "--quiet",
		"--silent"),
	patternBy: set("-e", "-f", "--regexp", "--file"),
	glob: func(opt, val string) (pattern, bool) {
		return pattern{glob: val, include: opt == "--include"}, opt == "--include" || opt == "--exclude"
	},
}

var rgOpts = opts{
	short: "efgtTmABCMrEjd",
	long: set("--regexp", "--file", "--glob", "--iglob", "--type", "--type-not", "--type-add", "--type-clear",
		"--max-count", "--after-context", "--before-context", "--context", "--max-columns", "--replace",
		"--encoding", "--threads", "--max-depth", "--maxdepth", "--max-filesize", "--ignore-file", "--sort",
		"--sortr", "--path-separator", "--pre", "--pre-glob", "--colors", "--context-separator",
		"--field-match-separator", "--field-context-separator", "--dfa-size-limit", "--regex-size-limit",
		"--engine", "--hostname-bin", "--hyperlink-format", "--generate", "--color"),
	namesOnly: set("-l", "-c", "-q", "--files-with-matches", "--files-without-match", "--count",
		"--count-matches", "--quiet", "--files", "--type-list"),
	patternBy: set("-e", "-f", "--regexp", "--file"),
	glob: func(opt, val string) (pattern, bool) {
		if opt != "-g" && opt != "--glob" && opt != "--iglob" {
			return pattern{}, false
		}
		g := pattern{glob: val, include: true, braces: true, fold: opt == "--iglob", iglob: opt == "--iglob"}
		if strings.HasPrefix(val, "!") {
			g.glob, g.include = val[1:], false
		}
		return g, true
	},
}

var gitGrepOpts = opts{
	short: "efmABCO",
	long:  set("--max-depth", "--threads", "--max-count", "--after-context", "--before-context", "--context"),
	namesOnly: set("-l", "-L", "-c", "-q", "--name-only", "--files-with-matches", "--files-without-match", "--count",
		"--quiet"),
	patternBy: set("-e", "-f"),
	glob:      func(string, string) (pattern, bool) { return pattern{}, false },
}

// parsed is a command line split into options and operands.
type parsed struct {
	namesOnly, recursive, dashDash bool
	content                        bool
	noIgnore, untracked, noIndex   bool
	stdinPattern, split            bool // -f -: the patterns come from stdin; split: a word may be several
	filter                         filter
	operands                       []word // after the pattern
	afterDash                      []word // after --
}

func parse(args []word, o opts) parsed {
	var p parsed
	patternSet, foldAll := false, false
	opt := func(name, val string) {
		if o.namesOnly[name] {
			p.namesOnly = true
		}
		if o.patternBy[name] {
			patternSet = true
			if (name == "-f" || name == "--file") && val == "-" {
				p.stdinPattern = true
			}
		}
		switch name {
		case "--glob-case-insensitive", "--no-glob-case-insensitive":
			foldAll = name == "--glob-case-insensitive" // the last one wins
		case "--json", "--vimgrep":
			p.content = true // rg prints matches with these even after -l
		case "-u", "--unrestricted", "--no-ignore", "--no-ignore-vcs", "--no-ignore-dot", "--no-ignore-files":
			p.noIgnore = true
		case "--untracked":
			p.untracked = true
		case "--no-index":
			p.noIndex = true
		}
		if name == "-r" || name == "-R" || name == "--recursive" || name == "--dereference-recursive" ||
			((name == "-d" || name == "--directories") && val == "recurse") {
			p.recursive = true
		}
		if g, ok := o.glob(name, val); ok {
			p.filter = append(p.filter, g)
		}
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a.split {
			p.split = true
		}
		next := func() string {
			if i+1 < len(args) {
				i++
				p.split = p.split || args[i].split
				return args[i].text
			}
			return ""
		}
		switch {
		case p.dashDash:
			p.afterDash = append(p.afterDash, a)
		case a.ok && a.text == "--":
			p.dashDash = true
		case a.ok && strings.HasPrefix(a.text, "--"):
			name, val, hasVal := strings.Cut(a.text, "=")
			if !hasVal && o.long[name] {
				val = next()
			}
			opt(name, val)
		case a.ok && strings.HasPrefix(a.text, "-") && len(a.text) > 1:
			for j := 1; j < len(a.text); j++ {
				c := a.text[j]
				if strings.IndexByte(o.short, c) >= 0 {
					val := a.text[j+1:]
					if val == "" {
						val = next()
					}
					opt("-"+string(c), val)
					break
				}
				opt("-"+string(c), "")
			}
		default:
			p.operands = append(p.operands, a)
		}
	}
	if !patternSet && len(p.operands) > 0 {
		p.operands = p.operands[1:]
	} else if !patternSet && len(p.afterDash) > 0 {
		p.afterDash = p.afterDash[1:] // grep -- -pattern file
	}
	p.namesOnly = p.namesOnly && !p.content
	if foldAll {
		for i := range p.filter {
			p.filter[i].fold = true
		}
	}
	slices.SortStableFunc(p.filter, func(a, b pattern) int {
		switch {
		case a.iglob == b.iglob:
			return 0
		case b.iglob:
			return -1
		}
		return 1
	})
	return p
}

// searchCall judges one grep, rg or git grep command line. fed: a wrapper (xargs, find) hands it file names; piped:
// its stdin is a pipe, which rg reads when it names no path.
func searchCall(args []word, dirs []string, fed, piped bool) string {
	name := path.Base(args[0].text)
	if name == "git" {
		return gitGrep(args[1:], dirs, fed)
	}
	o, tool, fix := grepOpts, "grep", grepFix
	if name == "rg" {
		o, tool, fix = rgOpts, "rg", rgFix
	}
	p := parse(args[1:], o)
	if p.namesOnly {
		return ""
	}
	s := &search{tool: tool, fix: fix, dirs: dirs, recursive: p.recursive || name == "rg", filter: p.filter}
	if name == "rg" && !p.noIgnore {
		s.list = gitVisible
	}
	files := append(p.operands, p.afterDash...)
	switch {
	case fed:
		s.unsure = "xargs or find hands it file names"
	case p.split && s.recursive:
		s.unsure = "an unquoted $x or $(...) may be several words, so the guard cannot tell its paths from its options"
	}
	if len(files) == 0 && s.recursive && !(name == "rg" && piped && !p.stdinPattern) {
		s.read(".", false)
	}
	for _, f := range files {
		switch {
		case f.ok:
			s.read(f.text, f.glob)
		case f.glob || s.recursive:
			// A non-recursive grep of one $FILE reads one named file; anything else may be a folder or a glob.
			s.unsure = fmt.Sprintf("it searches a path only the run shows (%s)", f.src)
		}
	}
	return s.verdict()
}

// gitGrep judges git's arguments when the subcommand is grep: git's own options come first (-C moves the folder).
func gitGrep(args []word, dirs []string, fed bool) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !a.ok {
			dirs = []string{unknownDir} // -C"$d" or a $x option: the folder or the repository is unknown
			continue
		}
		switch {
		case a.text == "-C" && i+1 < len(args):
			i++
			dirs = cdTo(dirs, args[i:i+1])
		case strings.HasPrefix(a.text, "-C") && len(a.text) > 2:
			dirs = cdTo(dirs, []word{{text: a.text[2:], ok: true}})
		case a.text == "--git-dir" || a.text == "--work-tree" || strings.HasPrefix(a.text, "--git-dir=") ||
			strings.HasPrefix(a.text, "--work-tree="):
			// Another repository or work tree: git ls-files run in the folder would list the wrong files.
			dirs = []string{unknownDir}
			if !strings.Contains(a.text, "=") {
				i++
			}
		case a.text == "-c" || a.text == "--namespace" || a.text == "--config-env":
			i++
		case strings.HasPrefix(a.text, "-"):
		case a.text == "grep":
			return gitGrepArgs(args[i+1:], dirs, fed)
		default:
			return ""
		}
	}
	return ""
}

var magicRe = regexp.MustCompile(`^:(\(([^)]*)\)|[!^/]*:?)`)

// gitGrepArgs judges git grep: it searches the folder it runs in, or the pathspecs it names, less the exclude
// pathspecs; in the work tree, or in the revisions it names, listed with git ls-tree.
func gitGrepArgs(args []word, dirs []string, fed bool) string {
	p := parse(args, gitGrepOpts)
	if p.namesOnly {
		return ""
	}
	s := &search{tool: "git grep", fix: gitFix, dirs: dirs, recursive: true, list: gitTracked}
	if fed {
		s.unsure = "xargs or find hands it file names"
	}
	switch {
	case p.noIndex:
		s.list = everyFile
	case p.untracked:
		s.list = gitVisible
	}
	var specs []word
	var revs []string
	rev := func(o word) {
		if o.ok && !o.glob {
			revs = append(revs, o.text)
		} else {
			s.unsure = "it searches a revision or a path only the run shows (" + o.src + ")"
		}
	}
	if p.dashDash {
		specs = p.afterDash
		for _, o := range p.operands {
			rev(o)
		}
	} else {
		for _, o := range p.operands {
			if o.ok && !o.glob && exists(dirs, o.text) {
				specs = append(specs, o)
			} else {
				rev(o)
			}
		}
	}
	var includes []word
	for _, w := range specs {
		if !w.ok {
			s.unsure = "it searches a path only the run shows (" + w.src + ")"
			continue
		}
		pat := w.text
		if m := magicRe.FindStringSubmatch(pat); m != nil {
			pat = pat[len(m[0]):]
			magic := m[2]
			if m[1] != "" && m[2] == "" {
				magic = m[1]
			}
			words := strings.Split(magic, ",")
			if strings.ContainsAny(magic, "!^") || slices.Contains(words, "exclude") {
				// git's exclude pathspecs win whatever their order: put them last.
				s.filter = append(s.filter, pattern{glob: pat, fold: slices.Contains(words, "icase")})
				continue
			}
			if strings.Contains(magic, "/") || slices.Contains(strings.Split(magic, ","), "top") {
				s.unsure = "it searches from the top of the repository (" + w.src + ")"
				continue
			}
		}
		includes = append(includes, word{text: pat, ok: true})
	}
	if len(includes) == 0 {
		includes = []word{{text: ".", ok: true}}
	}
	for _, w := range includes {
		// A pathspec glob may match below any folder: list the folder before its first glob character.
		p := w.text
		if i := strings.IndexAny(p, "*?["); i >= 0 {
			if isConfig(p) {
				s.named = p
			}
			p = path.Dir(p[:i] + "x")
		}
		if len(revs) > 0 {
			s.readRevs(revs, p)
		} else {
			s.read(p, false)
		}
	}
	return s.verdict()
}

// readRevs lists the files below p in each revision, from every folder the command may be in.
func (s *search) readRevs(revs []string, p string) {
	for _, dir := range s.dirs {
		if s.found != "" || s.unsure != "" {
			return
		}
		if dir == unknownDir {
			s.unsure = "it runs in a folder the guard cannot read (a cd to a variable)"
			return
		}
		for _, rev := range revs {
			ctx, cancel := context.WithDeadline(context.Background(), budget.deadline)
			out, err := exec.CommandContext(ctx, "git", "-C", dir, "ls-tree", "-r", "--name-only", "-z", rev, "--", p).Output()
			cancel()
			if err != nil {
				s.unsure = fmt.Sprintf("the guard cannot list revision %s (%v)", rev, err)
				return
			}
			for _, name := range strings.FieldsFunc(string(out), func(r rune) bool { return r == 0 }) {
				if budget.entries--; budget.entries < 0 {
					s.unsure = fmt.Sprintf("revision %s is too big to list in time", rev)
					return
				}
				if isConfig(name) && s.filter.reads(name) {
					s.found = rev + ":" + name
					return
				}
			}
		}
	}
}

func exists(dirs []string, p string) bool {
	for _, d := range dirs {
		full := p
		if !filepath.IsAbs(p) {
			full = filepath.Join(d, p)
		}
		if _, err := os.Stat(full); err == nil {
			return true
		}
	}
	return false
}
