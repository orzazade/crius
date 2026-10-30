// Command crius-post-guard is a Claude Code PreToolUse hook for Bash, in both personas and their subagents. It makes
// every Azure DevOps write that posts text people read wait for the owner's click: it answers "ask", which Claude Code
// keeps even in bypass mode (a headless probe on 10-30 refused the call). 10-30: a lead session posted a QA-notes
// comment and a reply to a colleague on AB#94205 under the owner's name, because its brief said to; the owner had both
// deleted. No brief can grant this now.
//
// It keys on the property (an az or REST write to Azure DevOps that carries text for people), not on one spelling:
//   - az devops invoke with a write method (--http-method POST, PUT, PATCH or DELETE, any case, = form) on a resource
//     that holds text: comments, threads, pull requests, wiki pages; or on workitems with an --in-file that sets
//     History (the discussion) or that the guard cannot read as it will be when az runs;
//   - az boards work-item create or update with --discussion, or with --fields that set History;
//   - az repos pr create or update with --description (-d); az devops wiki page create or update; any az command
//     whose words name a comment, thread or discussion and end in a write verb (create, add, update, reply, ...);
//   - a REST call to Azure DevOps (dev.azure.com, *.visualstudio.com, an /_apis/ path, or a URL from a variable in a
//     command that names an ADO host) with a write method through az rest, curl, wget, httpie or xh. A POST to the
//     read endpoints (wit/wiql, wit/workitemsbatch, search/) is a read.
//
// It looks through env prefixes (GH_TOKEN=... az), wrappers (env, env -S, timeout, sudo, xargs, find -exec, watch,
// op run, ...), bash -c, eval, a shell reading its script from stdin, ; && | chains, $(...), line continuations, quotes
// and $'...'. A script file a shell runs passes when the guard can read it and every command in it only prints, tests or
// reads, or is bus-post (S=/x; bash $S/say.sh), and every command around it is on a list too, setting nothing the
// script would run (PS4, BASH_ENV, PATH) and writing nothing into its folder or bus-post's; any other script is checked
// like a command and asks when it or the command names az or Azure DevOps. az takes the last copy of an option and reads a value from @file; the guard does the same. When it
// cannot tell (a method, resource or fields value from a variable or @file, eval or bash -c text it cannot read, a
// command the bash parser cannot read that names az or an ADO host) it asks. Unreadable hook input also asks: a guard
// on an outward-facing write never lets one through silently.
//
// Known gaps: field-only work-item updates (state, title, description) pass by design for now; az repos pr create or
// update with --title only; a command name from a variable or a function ($AZ, f() { az "$@"; }); a script run by its
// own name ($S/x.sh, ./x.sh); the code a script runs through source or an interpreter (python) when neither names
// az or Azure DevOps; a script's arguments used in arithmetic or [[ ]] (bash runs a $(...) found in the value); a
// script that hides az (a'z', a PS4 prompt) when neither it nor the command names az, as for a command itself (and
// PS4='$(az ...)' bash -xc : with no script); a script past the 16th one a call reads or 4 levels down;
// python -m azure.cli; ssh or kubectl exec to another machine; code in a script file (inline -c/-e
// code in python, node, bun, ruby, perl or pwsh asks when it names Azure DevOps and a write method); a curl config file
// (-K); an in-file written by a script; Azure DevOps MCP tools (they are not Bash calls).
// Output: nothing (the call goes on) or one JSON line with permissionDecision "ask".
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

type input struct {
	ToolName  string `json:"tool_name"`
	Cwd       string `json:"cwd"`
	ToolInput struct {
		Command string `json:"command"`
	} `json:"tool_input"`
}

const reason = "Posts text to Azure DevOps under the owner's name: needs the owner's click. Show the owner the exact text first."

func main() {
	if err := run(os.Stdin, os.Stdout); err != nil {
		// The ask could not be written: refuse instead, so the write cannot go through unseen.
		fmt.Fprintln(os.Stderr, "Refused by crius-post-guard:", err)
		os.Exit(2)
	}
}

// run reads the hook input and writes an ask when the call needs the owner's click; it writes nothing otherwise.
func run(r io.Reader, w io.Writer) error {
	var in input
	if err := json.NewDecoder(r).Decode(&in); err != nil {
		return ask(w, "crius-post-guard could not read the hook input ("+err.Error()+"), so it cannot tell whether "+
			"this posts text to Azure DevOps: needs the owner's click.")
	}
	if why := check(in); why != "" {
		return ask(w, why)
	}
	return nil
}

func ask(w io.Writer, why string) error {
	var out struct {
		HookSpecificOutput struct {
			HookEventName            string `json:"hookEventName"`
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	out.HookSpecificOutput.HookEventName = "PreToolUse"
	out.HookSpecificOutput.PermissionDecision = "ask"
	out.HookSpecificOutput.PermissionDecisionReason = why
	return json.NewEncoder(w).Encode(out)
}

// ctx is what every part of one hook call shares: the start folder and the whole command as the agent wrote it.
type ctx struct {
	cwd, whole string
	vars       map[string]setVar // variables the command sets to a literal value at its top level, before a script runs
	files      []runs            // this command's parse and those of the commands around it (bash -c text)
	moved      bool              // the command changes folder, so a relative script name is not known
	depth      int               // scripts read to get here (a script that runs itself stops at 4)
	reads      *int              // scripts read for this hook call, at most 16 (15 lines of bash self.sh took 19 s)
}

// setVar is a variable's literal value and where its assignment ends; lost: also set elsewhere, computed, read, a loop's
// or under eval or source, so its value when a script runs is not known.
type setVar struct {
	text string
	end  uint
	lost bool
	in   *syntax.Stmt // set after a command in a && chain: known only inside that chain (cd /tmp && S=x && bash $S/y)
}

// runs is a parsed command and the words in it that name a script a shell runs.
type runs struct {
	file    *syntax.File
	scripts map[*syntax.Word]bool // the words that name a script a shell runs
}

func check(in input) string {
	if in.ToolName != "Bash" {
		return ""
	}
	return checkBash(in.ToolInput.Command, ctx{cwd: in.Cwd, whole: in.ToolInput.Command, reads: new(int)})
}

// adoMarkRe finds a sign of Azure DevOps in a command: a host, an API path, or a variable that only an ADO call uses.
var adoMarkRe = regexp.MustCompile(`(?i)dev\.azure\.com|\.visualstudio\.com|/_apis/|AZURE_DEVOPS|SYSTEM_ACCESSTOKEN|SYSTEM_COLLECTIONURI`)

// adoRe finds an az call or a sign of Azure DevOps in text the guard cannot read.
var adoRe = regexp.MustCompile(`(?i)(^|[^\w.-])az(\s|$)|` + adoMarkRe.String()[4:])

func asked(what string) string { return reason + " (" + what + ")" }

// unsure asks when text the guard cannot read sits in a command that names az or Azure DevOps.
func unsure(c ctx, what string) string {
	if adoRe.MatchString(c.whole) {
		return asked(what + " the guard cannot read")
	}
	return ""
}

func checkBash(command string, c ctx) string {
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil {
		if adoRe.MatchString(command) {
			return asked("the guard cannot parse this command, so it cannot tell whether it writes")
		}
		return ""
	}
	c = c.scan(f)
	piped := map[*syntax.CallExpr]bool{}
	why := ""
	syntax.Walk(f, func(n syntax.Node) bool {
		if why != "" {
			return false
		}
		switch n := n.(type) {
		case *syntax.BinaryCmd:
			if n.Op == syntax.Pipe || n.Op == syntax.PipeAll {
				if ce, ok := n.Y.Cmd.(*syntax.CallExpr); ok {
					piped[ce] = true
				}
			}
		case *syntax.Stmt:
			ce, ok := n.Cmd.(*syntax.CallExpr)
			if !ok || len(ce.Args) == 0 {
				return true
			}
			stdin := piped[ce]
			for _, r := range n.Redirs {
				switch r.Op {
				case syntax.RdrIn, syntax.Hdoc, syntax.DashHdoc, syntax.WordHdoc:
					stdin = true
				}
			}
			args := make([]word, len(ce.Args))
			for i, w := range ce.Args {
				args[i] = literal(w)
			}
			why = checkCall(args, stdin, c)
		}
		return true
	})
	return why
}

// word is one shell word: its text, the word as written (src), whether the text is all literal (ok), and the word.
type word struct {
	text, src string
	ok        bool
	node      *syntax.Word
}

func literal(w *syntax.Word) word {
	var b strings.Builder
	out := word{ok: true, node: w}
	for i, p := range w.Parts {
		switch p := p.(type) {
		case *syntax.Lit:
			v := unescape(p.Value, "")
			if i == 0 && (v == "~" || strings.HasPrefix(v, "~/")) {
				home, _ := os.UserHomeDir()
				v = home + v[1:]
			}
			b.WriteString(v)
		case *syntax.SglQuoted:
			if p.Dollar {
				b.WriteString(ansiC(p.Value))
			} else {
				b.WriteString(p.Value)
			}
		case *syntax.DblQuoted:
			for _, q := range p.Parts {
				if l, ok := q.(*syntax.Lit); ok {
					b.WriteString(unescape(l.Value, "\"\\$`"))
				} else {
					out.ok = false
				}
			}
		default:
			out.ok = false
		}
	}
	out.text = b.String()
	var src bytes.Buffer
	syntax.NewPrinter().Print(&src, w)
	out.src = src.String()
	return out
}

// unescape drops the backslash before a character; inside double quotes (only non-empty) only before those characters.
func unescape(s, only string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && (only == "" || strings.IndexByte(only, s[i+1]) >= 0) {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// ansiC decodes the escapes of $'...' text that can change how a command reads: \n \t \r \\ \' \" and \xHH.
func ansiC(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case 'x':
			j := i + 1
			for j < len(s) && j < i+3 && strings.IndexByte("0123456789abcdefABCDEF", s[j]) >= 0 {
				j++
			}
			if n, err := strconv.ParseUint(s[i+1:j], 16, 8); err == nil {
				b.WriteByte(byte(n))
				i = j - 1
			} else {
				b.WriteString(`\x`)
			}
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// wrappers run the command named in their arguments.
var wrappers = map[string]bool{"timeout": true, "gtimeout": true, "nice": true, "nohup": true, "command": true,
	"env": true, "sudo": true, "doas": true, "stdbuf": true, "caffeinate": true, "builtin": true, "exec": true,
	"time": true, "watch": true, "xargs": true, "find": true, "parallel": true, "op": true, "script": true,
	"unbuffer": true, "flock": true, "chronic": true, "ionice": true, "taskpolicy": true, "noglob": true,
	"direnv": true, "mise": true, "arch": true, "setsid": true, "su": true, "uv": true, "poetry": true, "pipenv": true,
	"doppler": true, "dotenvx": true, "dotenv": true}

// lineRunners are the wrappers that run one word as a shell command line (watch 'az ...', env -S, script -c, su -c).
var lineRunners = map[string]bool{"watch": true, "env": true, "script": true, "su": true}

// feeders add words of their own (from stdin) to the command they run.
var feeders = map[string]bool{"xargs": true, "parallel": true}

var shells = map[string]bool{"bash": true, "sh": true, "zsh": true, "dash": true, "ksh": true}

// interpreters run inline code given with -c, -e or -Command.
var interpreters = map[string]bool{"python": true, "python3": true, "node": true, "bun": true, "deno": true,
	"ruby": true, "perl": true, "pwsh": true, "powershell": true}

var writeWordRe = regexp.MustCompile(`(?i)\b(post|put|patch|delete)\b`)

var httpTools = map[string]bool{"curl": true, "wget": true, "http": true, "https": true, "xh": true, "xhs": true}

// stdin: the command reads a pipe, a file or a heredoc on its standard input.
func checkCall(args []word, stdin bool, c ctx) string {
	if !args[0].ok {
		return ""
	}
	name := path.Base(args[0].text)
	switch {
	case name == "eval":
		var parts []string
		for _, a := range args[1:] {
			if !a.ok {
				return unsure(c, "eval text")
			}
			parts = append(parts, a.text)
		}
		return checkBash(strings.Join(parts, " "), c)
	case shells[name]:
		return checkShell(args, stdin, c)
	case interpreters[name]:
		for i := 1; i+1 < len(args); i++ {
			switch args[i].text {
			case "-c", "-e", "--eval", "-Command", "-command", "-p", "--print":
				code := args[i+1]
				if adoMarkRe.MatchString(code.src) && writeWordRe.MatchString(code.src) {
					return asked(name + " inline code that writes to Azure DevOps")
				}
				return ""
			}
		}
		// Code comes from stdin only with no module (-m) and no script file, or with the script -.
		for _, a := range args[1:] {
			if a.text == "-m" || !strings.HasPrefix(a.text, "-") {
				return ""
			}
			if a.text == "-" {
				break
			}
		}
		if stdin && adoMarkRe.MatchString(c.whole) && writeWordRe.MatchString(c.whole) {
			return asked(name + " code on stdin that may write to Azure DevOps")
		}
		return ""
	case wrappers[name]:
		for i := 1; i < len(args); i++ {
			a := args[i]
			if !a.ok {
				continue
			}
			if n := path.Base(a.text); n == "az" || n == "eval" || shells[n] || httpTools[n] || interpreters[n] {
				inner := args[i:]
				if feeders[name] {
					// xargs adds words from its stdin: they may be the method or the URL
					inner = append(slices.Clone(inner), word{src: "$XARGS"})
				}
				return checkCall(inner, stdin, c)
			}
			if lineRunners[name] && strings.ContainsAny(a.text, " \t\n") && adoRe.MatchString(a.text) {
				if why := checkBash(a.text, c); why != "" {
					return why
				}
			}
		}
		return ""
	case name == "az":
		return checkAz(args[1:], c)
	case name == "curl":
		return checkCurl(args[1:], c)
	case name == "wget":
		return checkWget(args[1:], c)
	case httpTools[name]:
		return checkHTTPie(args[1:], stdin, c)
	}
	return ""
}

// checkShell reads bash [options] [-c text | -s | file] [args]: -c runs the first word after the options, -s or no file
// reads the script from stdin; -o and -O take a value; - and -- end the options.
func checkShell(args []word, stdin bool, c ctx) string {
	i, sawC, sawS := shellOpts(args)
	switch {
	case sawC && i < len(args):
		if args[i].ok {
			return checkBash(args[i].text, c)
		}
		return unsure(c, "bash -c text")
	case sawC:
		return ""
	case i < len(args) && !sawS:
		// zsh reads a start-up file (.zshenv, ZDOTDIR) whatever its options; bash and sh only with -i, -l or BASH_ENV
		plain := plainShell(args[1:i]) && !strings.Contains(path.Base(args[0].text), "zsh") &&
			!strings.Contains(path.Base(args[0].text), "ksh")
		return checkScript(args[i], c, plain)
	case stdin:
		return unsure(c, "a script on the shell's stdin")
	}
	return ""
}

// shellOpts reads a shell's options: the index of the first word after them, and whether -c or -s was given.
func shellOpts(args []word) (i int, sawC, sawS bool) {
	i = 1
	for ; i < len(args); i++ {
		a := args[i]
		if !a.ok {
			break
		}
		if a.text == "-" || a.text == "--" {
			i++
			break
		}
		if a.text == "--rcfile" || a.text == "--init-file" {
			i++
			continue
		}
		if strings.HasPrefix(a.text, "--") {
			continue
		}
		if len(a.text) < 2 || (a.text[0] != '-' && a.text[0] != '+') {
			break
		}
		flags := a.text[1:]
		sawC = sawC || strings.Contains(flags, "c")
		sawS = sawS || strings.Contains(flags, "s")
		i += strings.Count(flags, "o") + strings.Count(flags, "O")
	}
	return i, sawC, sawS
}

// checkScript judges the script file a shell runs. Its name is read as written or through variables the command set to a
// literal at its top level before it (Crius 10-30 20:27: S=...; bash $S/lead/say.sh INFO "... az blob list ..." asked
// the owner, as the name came from $S and the bus text named az). The file is read only to show that it cannot post:
// a script whose every command is on a list (safeScript) passes when the command around it is held to a list too
// (safeAround). Any other script is checked like a command; it, one the guard cannot find, and one the command names
// relative to a folder it left ask when the script or the command names az or Azure DevOps. A script is not passed on
// its list either when the shell takes options that run other code (plain: no --rcfile or -i; zsh and ksh read a
// start-up file always).
func checkScript(a word, c ctx, plain bool) string {
	name, ok := a.text, a.ok
	if !ok && a.node != nil {
		name, ok = c.resolve(a.node)
	}
	if !ok || !filepath.IsAbs(name) && (c.moved || c.cwd == "") {
		return unsure(c, "a shell script name")
	}
	if !filepath.IsAbs(name) {
		name = filepath.Join(c.cwd, name)
	}
	fi, err := os.Stat(name)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > 1<<20 {
		return unsure(c, "a shell script")
	}
	if c.reads != nil {
		if *c.reads++; *c.reads > 16 {
			return unsure(c, "more shell scripts than the guard reads")
		}
	}
	body, err := os.ReadFile(name)
	if err != nil {
		return unsure(c, "a shell script")
	}
	if plain && c.safeAround(name) && safeScript(string(body)) {
		return ""
	}
	// any other script gets the command check ("az" in quotes, review of ace0c14) and asks when unsure
	if c.depth >= 4 {
		return unsure(c, "a shell script four levels down")
	}
	if why := checkBash(string(body), ctx{whole: string(body), depth: c.depth + 1, reads: c.reads}); why != "" {
		return why
	}
	return unsure(ctx{whole: string(body) + "\n" + c.whole}, "a shell script")
}

// safeAround says whether the command around a script run leaves the script nothing that runs code. A script on the
// list runs only listed commands, so only what reaches it from the shell that starts it could post: a variable it
// inherits that runs code (PS4 with set -x, BASH_ENV, PATH, XDG_CONFIG_HOME for git, an exported function) or a file
// it or bus-post is read from. Proving that the commands around write no such file failed six reviews (awk > file,
// git --output, a glob or ~user redirect target), so the command around may hold only what writes nothing and sets
// nothing: commands that print or read (aroundCmds), cd, a shell running a script (read on its own), and an
// assignment, export or declare of a name a script word uses ($S) and codeNameRe does not name (no option: -f, -n).
// No loop, function, ${X:=}, arithmetic or [[ ]] (bash runs a $(...) in a value they read), and no assignment in front of a
// command (it reaches that command). A file is written only by a redirect, whose target must be a path the guard
// reads as bash does (no glob, no ~ but ~/), outside the script's folder and bus-post's, and no link.
func (c ctx) safeAround(script string) bool {
	home, _ := os.UserHomeDir()
	keep := []string{realDir(filepath.Dir(script)), filepath.Dir(realDir(script)), realDir(home + "/.crius/bin")}
	for _, r := range c.files {
		used := map[string]bool{} // the names the script words use
		for w := range r.scripts {
			syntax.Walk(w, func(n syntax.Node) bool {
				if pe, ok := n.(*syntax.ParamExp); ok && pe.Param != nil {
					used[pe.Param.Value] = true
				}
				return true
			})
		}
		named := func(a *syntax.Assign) bool {
			return a.Name != nil && used[a.Name.Value] && !codeNameRe.MatchString(a.Name.Value)
		}
		safe := true
		syntax.Walk(r.file, func(n syntax.Node) bool {
			switch n := n.(type) {
			case *syntax.ParamExp:
				safe = safe && n.Exp == nil && n.Slice == nil && n.Repl == nil && n.Index == nil && n.Names == 0 &&
					!n.Excl && !n.Width
			case *syntax.ForClause, *syntax.WhileClause, *syntax.FuncDecl, *syntax.ArithmExp, *syntax.ArithmCmd,
				*syntax.LetClause, *syntax.TestClause, *syntax.CoprocClause, *syntax.TimeClause:
				safe = false
			case *syntax.DeclClause:
				for _, a := range n.Args {
					safe = safe && named(a)
				}
				return false
			case *syntax.Redirect:
				safe = safe && n.N == nil && c.safeWrite(n, keep)
			case *syntax.CallExpr:
				if len(n.Args) == 0 {
					for _, a := range n.Assigns {
						safe = safe && named(a)
					}
				} else {
					safe = safe && len(n.Assigns) == 0 && aroundCall(n.Args, r)
				}
			}
			return safe
		})
		if !safe {
			return false
		}
	}
	return true
}

// codeNameRe is a variable that runs code or finds commands for a shell or git started later (codeVarRe's, and ZDOTDIR).
var codeNameRe = regexp.MustCompile(`^(PS[0-4]|BASH_ENV|ENV|ZDOTDIR|PATH|HOME|IFS|CDPATH|GLOBIGNORE|SHELLOPTS|BASHOPTS|` +
	`PROMPT_COMMAND|(GIT|BASH|LD|DYLD|XDG)_[A-Za-z0-9_]*)$`)

// aroundCmds are the commands that may sit around a script run: they print or read, and none has an option that
// writes a file or runs a command (awk, git, sort -o, tee and find do).
var aroundCmds = map[string]bool{":": true, "true": true, "false": true, "echo": true, "printf": true, "test": true,
	"[": true, "date": true, "cat": true, "head": true, "tail": true, "wc": true, "grep": true, "ls": true, "tr": true,
	"cut": true, "basename": true, "dirname": true, "sleep": true, "cd": true}

// aroundCall says whether one command around a script run may pass: a shell running a script (that script is judged
// on its own), or one of aroundCmds, bare or in a system folder; printf takes no option (-v sets a variable).
func aroundCall(args []*syntax.Word, r runs) bool {
	k := 0
	for k < len(args)-1 && (literal(args[k]).text == "builtin" || literal(args[k]).text == "command") {
		k++
	}
	words := make([]word, len(args)-k)
	for i, w := range args[k:] {
		words[i] = literal(w)
	}
	a := words[0]
	if !a.ok || strings.Contains(a.text, "/") && !sysDirs[path.Dir(a.text)] {
		return false
	}
	name := path.Base(a.text)
	switch {
	case shells[name]:
		for _, w := range args[k+1:] {
			if r.scripts[w] {
				return true
			}
		}
		return false
	case name == "printf":
		return len(words) > 1 && words[1].ok && !strings.HasPrefix(words[1].text, "-")
	}
	return aroundCmds[name] && (name != "cd" || !strings.Contains(a.text, "/"))
}

// safeWrite says whether a redirect writes nowhere a script run or bus-post is read from: input and copies of a numbered
// descriptor pass; a file must be a path the guard reads, absolute or from the command's own folder, no link, and not
// in the script's folder or bus-post's.
func (c ctx) safeWrite(rd *syntax.Redirect, keep []string) bool {
	switch rd.Op {
	case syntax.RdrOut, syntax.AppOut, syntax.RdrAll, syntax.AppAll, syntax.ClbOut, syntax.RdrInOut, syntax.DplOut:
	default:
		return true
	}
	p, ok := c.resolve(rd.Word)
	if !ok {
		return false
	}
	// bash expands a glob in a target and ~user, ~+ and ~-, which resolve keeps as text (review of 26148a0)
	if l, ok := rd.Word.Parts[0].(*syntax.Lit); ok && strings.HasPrefix(l.Value, "~") && l.Value != "~" &&
		!strings.HasPrefix(l.Value, "~/") || strings.ContainsAny(p, "*?[{") {
		return false
	}
	if rd.Op == syntax.DplOut && (p == "-" || p != "" && strings.Trim(p, "0123456789") == "") {
		return true
	}
	if !filepath.IsAbs(p) {
		if c.moved || c.cwd == "" {
			return false
		}
		p = filepath.Join(c.cwd, p)
	}
	if devFiles[p] {
		return true
	}
	if fi, err := os.Lstat(p); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return false
	}
	dir := realDir(filepath.Dir(p))
	for _, k := range keep {
		if dir == k || strings.HasPrefix(dir, k+"/") {
			return false
		}
	}
	return true
}

// realDir gives a folder with its links followed, or cleaned when it does not exist.
func realDir(d string) string {
	if r, err := filepath.EvalSymlinks(d); err == nil {
		return r
	}
	return filepath.Clean(d)
}

// safeCmds are the commands a script may run and still pass: they print, test or read, and run no command their
// arguments name.
var safeCmds = map[string]bool{":": true, "true": true, "false": true, "echo": true, "printf": true, "return": true,
	"exit": true, "continue": true, "break": true, "shift": true, "test": true, "[": true, "wc": true, "tr": true,
	"date": true, "grep": true, "df": true, "basename": true, "dirname": true, "cat": true, "head": true, "tail": true,
	"cut": true, "sleep": true, "mkdir": true, "set": true}

// sysDirs are the folders a safeCmds name may be written with (/usr/bin/grep).
var sysDirs = map[string]bool{"/bin": true, "/usr/bin": true, "/sbin": true, "/usr/sbin": true}

// busPostRe is the bus-post binary as a script names it: "$HOME/.crius/bin/bus-post".
var busPostRe = regexp.MustCompile(`^"?\$\{?HOME\}?/\.crius/bin/bus-post"?$`)

// codeVarRe finds an assignment to a variable that makes bash or git run code or find commands elsewhere: a prompt run
// by set -x (PS4), a file sourced at start (BASH_ENV, ENV), PATH, HOME (bus-post's path), IFS, the shell's options and
// any GIT_, BASH_, LD_ or DYLD_ variable (GIT_EXTERNAL_DIFF, GIT_CONFIG_*). Review of 377cc0a.
var codeVarRe = regexp.MustCompile(`(^|[^A-Za-z0-9_])(PS[0-4]|BASH_ENV|ENV|PATH|HOME|IFS|CDPATH|GLOBIGNORE|SHELLOPTS|` +
	`BASHOPTS|PROMPT_COMMAND|(GIT|BASH|LD|DYLD|XDG)_[A-Za-z0-9_]*)\+?=`)

// plainFlagRe is a shell option that runs nothing else: -c, -e, -u, -x and their mixes (not -i or -l, which read a
// start-up file, nor --rcfile or -o).
var plainFlagRe = regexp.MustCompile(`^-[ceux]+$`)

// plainShell says whether a shell's options are all plainFlagRe ones, with - or -- ending them.
func plainShell(opts []word) bool {
	for _, o := range opts {
		if !o.ok || !plainFlagRe.MatchString(o.text) && o.text != "--" && o.text != "-" {
			return false
		}
	}
	return true
}

// devFiles are the only files a script may write and still pass.
var devFiles = map[string]bool{"/dev/null": true, "/dev/stderr": true, "/dev/stdout": true, "/dev/tty": true}

// gitReads are the git subcommands a script may run: they read.
var gitReads = map[string]bool{"cat-file": true, "grep": true, "rev-parse": true, "log": true, "show": true,
	"status": true, "diff": true, "ls-files": true}

// safeScript says whether a script cannot post to Azure DevOps whatever its arguments. A list of banned words cannot
// show that (review of ace0c14: "az" in quotes, c=$1; "$c", trap "$1"), so every command it runs must be on a list: it
// parses as bash, names no az or Azure DevOps host, sets no codeVarRe variable and no name it computes (declare "$1"),
// writes no file but devFiles, and each command is a literal name that safeCall lets pass.
func safeScript(body string) bool {
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(body), "")
	if err != nil || adoRe.MatchString(body) || codeVarRe.MatchString(body) {
		return false
	}
	funcs := map[string]bool{}
	syntax.Walk(f, func(n syntax.Node) bool {
		if fd, ok := n.(*syntax.FuncDecl); ok {
			funcs[fd.Name.Value] = true
		}
		return true
	})
	safe := true
	syntax.Walk(f, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.Assign:
			safe = safe && n.Name != nil
		case *syntax.Redirect:
			switch n.Op {
			case syntax.RdrOut, syntax.AppOut, syntax.RdrAll, syntax.AppAll, syntax.ClbOut, syntax.RdrInOut:
				w := literal(n.Word)
				safe = safe && w.ok && devFiles[w.text]
			case syntax.DplOut:
				w := literal(n.Word)
				safe = safe && w.ok && (w.text == "-" || strings.Trim(w.text, "0123456789") == "")
			}
		case *syntax.CallExpr:
			if len(n.Args) > 0 {
				args := make([]word, len(n.Args))
				for i, w := range n.Args {
					args[i] = literal(w)
				}
				safe = safe && safeCall(args, funcs)
			}
		}
		return safe
	})
	return safe
}

// safeCall says whether one command of a script may pass: bus-post; one of safeCmds, bare or in a system folder; a
// function the script defines (its body is read too); exec with only redirects or of a command that may pass; a shell
// -c whose literal text is a
// safeScript; lockf running a command that may pass; awk and git on their own rules.
func safeCall(args []word, funcs map[string]bool) bool {
	a := args[0]
	home, _ := os.UserHomeDir()
	if busPostRe.MatchString(a.src) || a.ok && a.text == home+"/.crius/bin/bus-post" {
		return true
	}
	if !a.ok || strings.Contains(a.text, "/") && !sysDirs[path.Dir(a.text)] {
		return false
	}
	name := path.Base(a.text)
	switch {
	case !strings.Contains(a.text, "/") && funcs[name]:
		return true
	case name == "exec":
		i := 1
		for i < len(args) && args[i].ok && strings.HasPrefix(args[i].text, "-") {
			if args[i].text == "-a" {
				i++
			}
			i++
		}
		return i >= len(args) || safeCall(args[i:], funcs)
	case shells[name]:
		i, sawC, _ := shellOpts(args)
		return sawC && i < len(args) && plainShell(args[1:i]) && args[i].ok && safeScript(args[i].text)
	case name == "lockf":
		i := 1
		for i < len(args) && args[i].ok && strings.HasPrefix(args[i].text, "-") {
			if args[i].text == "-t" {
				i++
			}
			i++
		}
		return i+1 < len(args) && safeCall(args[i+1:], funcs)
	case name == "awk":
		return safeAwk(args[1:])
	case name == "git":
		return safeGit(args[1:])
	}
	return safeCmds[name]
}

// safeAwk passes an awk whose program is literal and runs no command: no system(), no pipe to or from one, no -f file.
func safeAwk(args []word) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case !a.ok:
			return false
		case a.text == "-F" || a.text == "-v":
			i++
		case strings.HasPrefix(a.text, "-F") || strings.HasPrefix(a.text, "-v"):
		case strings.HasPrefix(a.text, "-"):
			return false
		default:
			p := strings.ReplaceAll(a.text, "||", "")
			return !strings.Contains(p, "|") && !strings.Contains(p, "system")
		}
	}
	return false
}

// safeGit passes a git that reads: no -c, --config-env or --exec-path, a literal subcommand from gitReads, no option
// that starts a program or writes (grep -O, diff --ext-diff, log --output, or a shortening of one), and a computed
// word only after -- or as -e's value.
func safeGit(args []word) bool {
	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		if !a.ok {
			return false
		}
		if !strings.HasPrefix(a.text, "-") {
			break
		}
		if strings.HasPrefix(a.text, "-c") || strings.HasPrefix(a.text, "--config-env") ||
			strings.HasPrefix(a.text, "--exec-path") {
			return false
		}
		if a.text == "-C" || a.text == "--git-dir" || a.text == "--work-tree" || a.text == "--namespace" {
			i++
		}
	}
	if i >= len(args) || !args[i].ok || !gitReads[args[i].text] {
		return false
	}
	ends := false // after --: paths
	for j, a := range args[i+1:] {
		switch {
		case !a.ok:
			// a computed word could be an option (-O<cmd>, --output=<file>): only a path or -e's pattern (Crius 10-30 22:55)
			if !ends && (j == 0 || !args[i+j].ok || args[i+j].text != "-e") {
				return false
			}
		case ends:
		case a.text == "--":
			ends = true
		case strings.HasPrefix(a.text, "-O") || runsOption(a.text):
			return false
		}
	}
	return true
}

// gitRuns are the long options of the git reads that start a program or write a file.
var gitRuns = []string{"--open-files-in-pager", "--output", "--ext-diff"}

// runsOption says whether a literal word is one of gitRuns or a shortening git takes for one (--open, --out).
func runsOption(w string) bool {
	w, _, _ = strings.Cut(w, "=")
	for _, r := range gitRuns {
		if len(w) > 2 && strings.HasPrefix(r, w) || strings.HasPrefix(w, r) {
			return true
		}
	}
	return false
}

// scan reads what a command sets up for the scripts it runs: the variables it sets once, to a literal value, as a
// statement of its own at its top level (a shell a script starts is a new process, so they are this command's own), the
// words that name a script a shell runs, and whether it changes folder.
func (c ctx) scan(f *syntax.File) ctx {
	c.vars = map[string]setVar{}
	r := runs{file: f, scripts: map[*syntax.Word]bool{}}
	count := map[string]int{}
	all := false // eval or source may set any variable
	syntax.Walk(f, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.Assign:
			if n.Name != nil {
				count[n.Name.Value]++
			}
		case *syntax.ForClause:
			if wi, ok := n.Loop.(*syntax.WordIter); ok {
				count[wi.Name.Value] += 2
			}
		case *syntax.CallExpr:
			if len(n.Args) == 0 {
				break
			}
			args := make([]word, len(n.Args))
			for i, w := range n.Args {
				args[i] = literal(w)
			}
			k := 0
			for k < len(args)-1 && (args[k].text == "builtin" || args[k].text == "command") {
				k++
			}
			switch name := path.Base(args[k].text); {
			case name == "cd" || name == "pushd" || name == "popd":
				c.moved = true
			case name == "eval" || name == "source" || name == ".":
				all = true
			case name == "read" || name == "mapfile" || name == "readarray" || name == "unset" || name == "getopts" ||
				name == "printf" || name == "declare" || name == "typeset" || name == "local" || name == "export":
				for _, a := range args[k+1:] {
					if v, _, _ := strings.Cut(a.text, "="); a.ok {
						count[v] += 2
					}
				}
			case shells[name]:
				if i, sawC, sawS := shellOpts(args[k:]); !sawC && !sawS && k+i < len(args) {
					r.scripts[n.Args[k+i]] = true
				}
			}
		}
		return true
	})
	// in a && b, b's assignment holds after the chain only when every statement before it is one too (false && S=x
	// leaves S unset); otherwise only inside the chain, where it ran when what follows runs (review of a4106c1)
	type stmtIn struct{ st, in *syntax.Stmt }
	var top []stmtIn
	for _, st := range f.Stmts {
		var in *syntax.Stmt
		for _, s := range andChain(st) {
			top = append(top, stmtIn{s, in})
			if !assignOnly(s) {
				in = st
			}
		}
	}
	for _, t := range top {
		var as []*syntax.Assign
		switch x := t.st.Cmd.(type) {
		case *syntax.CallExpr:
			if len(x.Args) == 0 {
				as = x.Assigns
			}
		case *syntax.DeclClause:
			as = x.Args
		}
		for _, a := range as {
			if a.Name == nil || a.Naked || a.Append || a.Index != nil || a.Array != nil {
				continue
			}
			v := word{ok: true}
			if a.Value != nil {
				v = literal(a.Value)
			}
			if v.ok && count[a.Name.Value] == 1 && !all {
				c.vars[a.Name.Value] = setVar{text: v.text, end: a.End().Offset(), in: t.in}
			}
		}
	}
	c.files = append(slices.Clone(c.files), r)
	return c
}

// andChain gives the statements of a top-level a && b && c, each of which runs in this shell before the next (S=x &&
// bash $S/say.sh, review of ace0c14); nothing for one run in the background, where an assignment stays in a subshell.
func andChain(st *syntax.Stmt) []*syntax.Stmt {
	if st.Background || st.Coprocess {
		return nil
	}
	if b, ok := st.Cmd.(*syntax.BinaryCmd); ok && b.Op == syntax.AndStmt && !st.Negated {
		return append(andChain(b.X), andChain(b.Y)...)
	}
	return []*syntax.Stmt{st}
}

// assignOnly says whether a statement only sets variables (S=x, export S=x).
func assignOnly(st *syntax.Stmt) bool {
	switch x := st.Cmd.(type) {
	case *syntax.CallExpr:
		return len(x.Args) == 0
	case *syntax.DeclClause:
		return true
	}
	return false
}

// resolve gives a word's value when its only expansions are plain $X or ${X} of variables the command set before it.
func (c ctx) resolve(w *syntax.Word) (string, bool) {
	var b strings.Builder
	param := func(pe *syntax.ParamExp) bool {
		if pe.Excl || pe.Length || pe.Width || pe.Index != nil || pe.Slice != nil || pe.Repl != nil || pe.Names != 0 ||
			pe.Exp != nil || pe.Param == nil {
			return false
		}
		v, ok := c.vars[pe.Param.Value]
		if !ok || v.lost || v.end > w.Pos().Offset() || v.in != nil && w.End().Offset() > v.in.End().Offset() {
			return false
		}
		b.WriteString(v.text)
		return true
	}
	for i, p := range w.Parts {
		switch p := p.(type) {
		case *syntax.Lit:
			v := unescape(p.Value, "")
			if i == 0 && (v == "~" || strings.HasPrefix(v, "~/")) {
				home, _ := os.UserHomeDir()
				v = home + v[1:]
			}
			b.WriteString(v)
		case *syntax.SglQuoted:
			if p.Dollar {
				return "", false
			}
			b.WriteString(p.Value)
		case *syntax.ParamExp:
			if !param(p) {
				return "", false
			}
		case *syntax.DblQuoted:
			for _, q := range p.Parts {
				switch q := q.(type) {
				case *syntax.Lit:
					b.WriteString(unescape(q.Value, "\"\\$`"))
				case *syntax.ParamExp:
					if !param(q) {
						return "", false
					}
				default:
					return "", false
				}
			}
		default:
			return "", false
		}
	}
	return b.String(), true
}

// isADO says whether a word that names a URL points at Azure DevOps: an ADO host or API path as written, or a URL from a
// variable in a command that names an ADO host.
func isADO(w word, c ctx) bool {
	s := strings.ToLower(w.src)
	return strings.Contains(s, "dev.azure.com") || strings.Contains(s, ".visualstudio.com") ||
		strings.Contains(s, "/_apis/") || !w.ok && adoMarkRe.MatchString(c.whole)
}

// readPostRe are the Azure DevOps endpoints where a POST is a query, not a write; it is matched on the path alone.
var readPostRe = regexp.MustCompile(`(?i)/_apis/(wit/wiql|wit/workitemsbatch|search/|git/repositories/[^/]+/(commitsbatch|itemsbatch)|graph/subjectquery|contribution/hierarchyquery)`)

func readPost(u word) bool {
	p, _, _ := strings.Cut(u.src, "?")
	return u.ok && readPostRe.MatchString(p)
}

// method is an HTTP method a command sets: set says it names one, ok says the guard can read it.
type method struct {
	word
	set bool
}

func methodOf(w word) method { return method{word: w, set: true} }

// writesTo says whether a call with this method to these Azure DevOps URLs writes: a method the guard cannot read
// counts as a write; a POST counts as a read only when every URL is a read endpoint.
func writesTo(m method, urls []word) bool {
	if !m.ok {
		return true
	}
	switch strings.ToUpper(m.text) {
	case "PUT", "PATCH", "DELETE":
		return true
	case "POST":
		return len(urls) == 0 || slices.ContainsFunc(urls, func(u word) bool { return !readPost(u) })
	}
	return false
}

func methodName(m method) string {
	if !m.ok {
		return "an unknown method"
	}
	return strings.ToUpper(m.text)
}

// azOpts is an az command line: its command words and the values of each option, by option name.
type azOpts struct {
	cmd  string
	opts map[string][]word
}

// azLong are the long options the guard reads; az may take an unambiguous prefix of one.
var azLong = []string{"--http-method", "--resource", "--area", "--in-file", "--discussion", "--fields", "--description",
	"--method", "--url", "--uri", "--body", "--content", "--file-path"}

// azGlobals are az's global options, which may come before the command words, with the number of values each takes.
var azGlobals = map[string]int{"--debug": 0, "--verbose": 0, "--only-show-errors": 0, "-h": 0, "--help": 0,
	"-o": 1, "--output": 1, "--query": 1, "--subscription": 1}

// azValue is one option value as az reads it: a value from @file is one the guard cannot read.
func azValue(w word) word {
	if strings.HasPrefix(w.text, "@") {
		w.ok = false
	}
	return w
}

func parseAz(args []word) azOpts {
	o := azOpts{opts: map[string][]word{}}
	i := 0
	var words []string
	for ; i < len(args); i++ {
		a := args[i]
		name, _, eq := strings.Cut(a.text, "=")
		if n, ok := azGlobals[name]; a.ok && ok {
			if !eq {
				i += n
			}
			continue
		}
		if !a.ok || strings.HasPrefix(a.text, "-") {
			break
		}
		words = append(words, strings.ToLower(a.text))
	}
	o.cmd = strings.Join(words, " ")
	var cur []string
	for ; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a.text, "-") || len(a.text) < 2 || (!a.ok && !strings.Contains(a.text, "=") && len(a.text) > 2) {
			for _, k := range cur {
				o.opts[k] = append(o.opts[k], azValue(a))
			}
			continue
		}
		var name, val string
		has := false
		if strings.HasPrefix(a.text, "--") {
			name, val, has = strings.Cut(a.text, "=")
		} else {
			// -d"text" and -dtext: a short option with its value attached
			name, val, has = a.text[:2], a.text[2:], len(a.text) > 2 || !a.ok
		}
		name = strings.ToLower(name)
		cur = []string{name}
		if strings.HasPrefix(name, "--") && !slices.Contains(azLong, name) && len(name) >= 4 {
			var hits []string
			for _, l := range azLong {
				if strings.HasPrefix(l, name) {
					hits = append(hits, l)
				}
			}
			if len(hits) > 0 {
				cur = hits // an ambiguous prefix counts as every option it may be
			}
		}
		for _, k := range cur {
			if has {
				o.opts[k] = append(o.opts[k], azValue(word{text: val, src: val, ok: a.ok}))
			} else if _, seen := o.opts[k]; !seen {
				o.opts[k] = []word{}
			}
		}
	}
	return o
}

// last gives the last value of the first of names that is set: az takes the last copy of an option. set is false when
// none of names is on the command line; a name with no value gives a literal empty word.
func (o azOpts) last(names ...string) (word, bool) {
	for _, n := range names {
		if v, ok := o.opts[n]; ok {
			if len(v) == 0 {
				return word{ok: true}, true
			}
			return v[len(v)-1], true
		}
	}
	return word{ok: true}, false
}

// textResourceRe are the resources whose writes carry text people read (a status has a description).
var textResourceRe = regexp.MustCompile(`(?i)comment|thread|pullrequest|wiki|^pages$|status`)

var writeVerbs = map[string]bool{"create": true, "add": true, "update": true, "reply": true, "post": true, "set": true,
	"edit": true, "append": true, "new": true}

var textWordRe = regexp.MustCompile(`comment|thread|discussion`)

func checkAz(args []word, c ctx) string {
	o := parseAz(args)
	words := strings.Fields(o.cmd)
	verb := ""
	if len(words) > 0 {
		verb = words[len(words)-1]
	}
	switch o.cmd {
	case "devops invoke":
		return checkInvoke(o, c)
	case "rest":
		m := method{word: word{text: "GET", ok: true}}
		if w, set := o.last("--method", "-m"); set {
			m = methodOf(w)
		}
		u, ok := o.last("--url", "--uri", "-u")
		r, _ := o.last("--resource")
		ado := ok && isADO(u, c) || strings.Contains(strings.ToLower(r.text), "499b84ac-1321-427f-aa17-267ca6975798")
		if ado && writesTo(m, []word{u}) {
			return asked("az rest " + methodName(m) + " to Azure DevOps")
		}
		return ""
	case "boards work-item update", "boards work-item create":
		if _, ok := o.opts["--discussion"]; ok {
			return asked("az " + o.cmd + " --discussion")
		}
		for _, f := range append(o.opts["--fields"], o.opts["-f"]...) {
			key, _, _ := strings.Cut(f.text, "=")
			if !f.ok || strings.HasSuffix(strings.ToLower(strings.TrimSpace(key)), "history") {
				return asked("az " + o.cmd + " --fields that may set History, the discussion")
			}
		}
		return ""
	case "repos pr create", "repos pr update":
		if _, ok := o.last("--description", "-d"); ok {
			return asked("az " + o.cmd + " --description")
		}
		return ""
	case "devops wiki page create", "devops wiki page update":
		return asked("az " + o.cmd)
	}
	if writeVerbs[verb] && textWordRe.MatchString(o.cmd) {
		return asked("az " + o.cmd)
	}
	return ""
}

func checkInvoke(o azOpts, c ctx) string {
	w, set := o.last("--http-method")
	if !set || w.ok && w.text == "" {
		return "" // GET
	}
	m := methodOf(w)
	if !writesTo(m, nil) {
		return ""
	}
	r, _ := o.last("--resource")
	a, _ := o.last("--area")
	what := "az devops invoke " + methodName(m)
	switch res := strings.ToLower(r.text); {
	case !r.ok:
		return asked(what + " to a resource the guard cannot read")
	case !a.ok || textResourceRe.MatchString(res) || strings.EqualFold(a.text, "wiki"):
		return asked(what + " --resource " + r.text)
	case res == "workitems":
		f, set := o.last("--in-file")
		if !set {
			return ""
		}
		if why := inFileUnsure(f, c); why != "" {
			return asked(what + " workitems: " + why)
		}
		p := f.text
		if !filepath.IsAbs(p) {
			p = filepath.Join(c.cwd, p)
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return asked(what + " workitems: cannot read --in-file " + f.text + " to see whether it sets History")
		}
		if bytes.Contains(bytes.ToLower(body), []byte("history")) {
			return asked(what + " workitems with an --in-file that sets History, the discussion")
		}
	}
	return ""
}

var cdRe = regexp.MustCompile(`(^|[\s;&|(])(cd|pushd)(\s|$)`)

// inFileUnsure says why the guard cannot trust the in-file on disk to be what az will read, or "".
func inFileUnsure(f word, c ctx) string {
	if !f.ok || f.text == "" {
		return "an --in-file the guard cannot read"
	}
	if !filepath.IsAbs(f.text) && (c.cwd == "" || cdRe.MatchString(c.whole)) {
		return "a relative --in-file the guard cannot place (a cd, or no start folder)"
	}
	if strings.Count(c.whole, filepath.Base(f.text)) > 1 {
		return "the same command names the --in-file again and may write it first"
	}
	p := f.text
	if !filepath.IsAbs(p) {
		p = filepath.Join(c.cwd, p)
	}
	if fi, err := os.Stat(p); err == nil && !fi.Mode().IsRegular() {
		return "an --in-file that is not a regular file (" + f.text + ")"
	}
	return ""
}

// curlShortArg are curl's short options that take a value: in a cluster (-sSd x) the rest of it, or the next word.
const curlShortArg = "dFHXTouAebcxwKmryYzECUPQtD"

// curlLongArg are curl's long options that take a value, so the value is not read as a URL.
var curlLongArg = map[string]bool{"--header": true, "--user": true, "--output": true, "--url": true, "--request": true,
	"--upload-file": true, "--cookie": true, "--cookie-jar": true, "--user-agent": true, "--referer": true,
	"--max-time": true, "--connect-timeout": true, "--retry": true, "--write-out": true, "--config": true,
	"--proxy": true, "--cacert": true, "--cert": true, "--key": true, "--oauth2-bearer": true, "--resolve": true,
	"--interface": true, "--limit-rate": true, "--range": true, "--output-dir": true, "--unix-socket": true,
	"--retry-delay": true, "--retry-max-time": true, "--url-query": true, "--variable": true, "--aws-sigv4": true}

func isCurlData(name string) bool {
	return strings.HasPrefix(name, "--data") || strings.HasPrefix(name, "--json") || strings.HasPrefix(name, "--form") ||
		strings.HasPrefix(name, "--expand-data") || strings.HasPrefix(name, "--expand-json")
}

func checkCurl(args []word, c ctx) string {
	var m method
	var urls []word
	data, upload, get := false, false, false
	for i := 0; i < len(args); i++ {
		a := args[i]
		next := func() word {
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return word{ok: true}
		}
		if !strings.HasPrefix(a.text, "-") || a.text == "-" {
			urls = append(urls, a)
			continue
		}
		if strings.HasPrefix(a.text, "--") {
			name, val, eq := strings.Cut(a.text, "=")
			value := func() word {
				if eq {
					return word{text: val, src: val, ok: a.ok}
				}
				return next()
			}
			switch {
			case name == "--request":
				m = methodOf(value())
			case isCurlData(name):
				value()
				data = true
			case name == "--upload-file":
				value()
				upload = true
			case name == "--get":
				get = true
			case name == "--url":
				urls = append(urls, value())
			case curlLongArg[name]:
				value()
			}
			continue
		}
		for j := 1; j < len(a.text); j++ {
			ch := a.text[j]
			if ch == 'G' {
				get = true
			}
			if strings.IndexByte(curlShortArg, ch) < 0 {
				continue
			}
			v := word{text: a.text[j+1:], src: a.text[j+1:], ok: a.ok}
			if v.text == "" && a.ok {
				v = next()
			}
			switch ch {
			case 'X':
				m = methodOf(v)
			case 'd', 'F':
				data = true
			case 'T':
				upload = true
			}
			break
		}
	}
	var ado []word
	for _, u := range urls {
		if isADO(u, c) {
			ado = append(ado, u)
		}
	}
	if len(ado) == 0 {
		return ""
	}
	if !m.set {
		switch {
		case upload:
			m = methodOf(word{text: "PUT", ok: true})
		case data && !get:
			m = methodOf(word{text: "POST", ok: true})
		default:
			return ""
		}
	}
	if writesTo(m, ado) {
		return asked("curl " + methodName(m) + " to Azure DevOps")
	}
	return ""
}

func checkWget(args []word, c ctx) string {
	var m method
	var ado []word
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a.text, "-") {
			if isADO(a, c) {
				ado = append(ado, a)
			}
			continue
		}
		name, val, eq := strings.Cut(a.text, "=")
		switch {
		case len(name) >= 5 && strings.HasPrefix("--method", name):
			if eq {
				m = methodOf(word{text: val, ok: a.ok})
			} else if i+1 < len(args) {
				i++
				m = methodOf(args[i])
			}
		case len(name) >= 7 && (strings.HasPrefix("--post-data", name) || strings.HasPrefix("--post-file", name)):
			if !m.set {
				m = methodOf(word{text: "POST", ok: true})
			}
			if !eq {
				i++
			}
		}
	}
	if len(ado) > 0 && m.set && writesTo(m, ado) {
		return asked("wget " + methodName(m) + " to Azure DevOps")
	}
	return ""
}

// httpieArg are httpie and xh options that take a value.
var httpieArg = map[string]bool{"-a": true, "--auth": true, "-A": true, "--auth-type": true, "--session": true,
	"--session-read-only": true, "-o": true, "--output": true, "-p": true, "--print": true, "-P": true,
	"--history-print": true, "-s": true, "--style": true, "--pretty": true, "--format-options": true, "--proxy": true,
	"--verify": true, "--cert": true, "--cert-key": true, "--cert-key-pass": true, "--ssl": true, "--ciphers": true,
	"--timeout": true, "--max-redirects": true, "--max-headers": true, "--boundary": true, "--default-scheme": true,
	"--bearer": true}

var httpMethods = []string{"GET", "HEAD", "OPTIONS", "POST", "PUT", "PATCH", "DELETE"}

// checkHTTPie reads httpie and xh: [METHOD] URL [items]. A data item (a=b, a:=b, a@file), --form, --raw or a body on
// stdin makes it a POST; a header (a:b) or query item (a==b) does not.
func checkHTTPie(args []word, stdin bool, c ctx) string {
	var m method
	var url *word
	data, ignoreStdin := false, false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a.text, "-") && a.text != "-" {
			name, _, eq := strings.Cut(a.text, "=")
			switch {
			case name == "--form" || name == "-f" || name == "--multipart":
				data = true
			case name == "--raw":
				data = true
				if !eq {
					i++
				}
			case name == "--ignore-stdin" || name == "-I":
				ignoreStdin = true
			case httpieArg[name] && !eq:
				i++
			}
			continue
		}
		// The URL is the first ADO word: the value of an option the guard does not know comes before it.
		switch {
		case url == nil && a.ok && slices.Contains(httpMethods, strings.ToUpper(a.text)):
			m = methodOf(a)
		case url == nil && isADO(a, c):
			url = &args[i]
		case url != nil && httpieData(a):
			data = true
		}
	}
	if url == nil {
		return ""
	}
	if !m.set {
		if !data && (!stdin || ignoreStdin) {
			return ""
		}
		m = methodOf(word{text: "POST", ok: true})
	}
	if writesTo(m, []word{*url}) {
		return asked("HTTP " + methodName(m) + " to Azure DevOps")
	}
	return ""
}

// httpieData says whether a request item sends data: its first separator decides (a:b header, a==b query).
func httpieData(a word) bool {
	if !a.ok {
		return true
	}
	i := strings.IndexAny(a.text, ":=@")
	if i < 0 {
		return false
	}
	rest := a.text[i:]
	switch {
	case strings.HasPrefix(rest, ":="), strings.HasPrefix(rest, "=@"), strings.HasPrefix(rest, ":=@"):
		return true
	case strings.HasPrefix(rest, "=="):
		return false
	case rest[0] == ':':
		return false // a header, or a header from a file (:@)
	}
	return true // a=b or a@file
}
