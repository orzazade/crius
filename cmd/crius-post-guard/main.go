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
// op run, ...), bash -c, eval, a shell reading its script from stdin, a script file a shell runs (its name as written or
// through variables the command set to a literal: S=/x; bash $S/say.sh), ; && | chains, $(...), line continuations,
// quotes and $'...'. az takes the last copy of an option and reads a value from @file; the guard does the same. When it
// cannot tell (a method, resource or fields value from a variable or @file, eval or bash -c text it cannot read, a
// command the bash parser cannot read that names az or an ADO host) it asks. Unreadable hook input also asks: a guard
// on an outward-facing write never lets one through silently.
//
// Known gaps: field-only work-item updates (state, title, description) pass by design for now; az repos pr create or
// update with --title only; a command name from a variable or a function ($AZ, f() { az "$@"; }); a script run by its
// own name ($S/x.sh, ./x.sh), and one a shell runs whose variable was set in a subshell; python -m azure.cli; ssh or
// kubectl exec to another machine; code in a script file (inline -c/-e code in python, node, bun, ruby, perl or pwsh
// asks when it names Azure DevOps and a write method); a curl config file (-K); an in-file written by a script; Azure
// DevOps MCP tools (they are not Bash calls).
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
	vars       map[string]setVar // variables this command sets to a literal value before a shell runs a script
	written    map[string]bool   // last path parts of the files this command writes (a script it writes is not read)
	moved      bool              // the command changes folder, so a relative script name is not known
	depth      int               // scripts read inside scripts
}

// setVar is a variable's literal value and where its assignment ends; lost: set more than once, computed, read or a
// loop's, so its value when a script runs is not known.
type setVar struct {
	text string
	end  uint
	lost bool
}

func check(in input) string {
	if in.ToolName != "Bash" {
		return ""
	}
	return checkBash(in.ToolInput.Command, ctx{cwd: in.Cwd, whole: in.ToolInput.Command})
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
	sawC, sawS := false, false
	i := 1
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
	switch {
	case sawC && i < len(args):
		if args[i].ok {
			return checkBash(args[i].text, c)
		}
		return unsure(c, "bash -c text")
	case sawC:
		return ""
	case i < len(args) && !sawS:
		return checkScript(args[i], c)
	case stdin:
		return unsure(c, "a script on the shell's stdin")
	}
	return ""
}

// checkScript checks the script file a shell runs, by its name as written or through variables the command set to a
// literal before it (Crius 10-30 20:27: S=...; bash $S/lead/say.sh INFO "... az blob list ..." asked the owner, as the
// name came from $S and the bus text named az). The script's commands are checked with the command around it, which may
// hand it a URL or a method. A name the guard cannot work out, a file it cannot open, and a file this command writes
// (cat > x.sh, cp, tee) are scripts it cannot read.
func checkScript(a word, c ctx) string {
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
	if c.depth >= 4 || c.written[filepath.Base(name)] {
		return unsure(c, "a shell script")
	}
	fi, err := os.Stat(name)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > 1<<20 {
		return unsure(c, "a shell script")
	}
	body, err := os.ReadFile(name)
	if err != nil {
		return unsure(c, "a shell script")
	}
	return checkBash(string(body), ctx{cwd: c.cwd, whole: string(body) + "\n" + c.whole, written: c.written, depth: c.depth + 1})
}

// writers are the commands whose words may name a file they write.
var writers = map[string]bool{"tee": true, "cp": true, "mv": true, "ln": true, "install": true, "rsync": true, "sed": true,
	"perl": true, "dd": true, "truncate": true, "curl": true, "wget": true, "patch": true, "tar": true, "unzip": true,
	"git": true}

// scan reads what a command sets up for the scripts it runs: the variables it sets once to a literal value (a shell
// a script starts is a new process, so they are this command's own), the files it writes, and whether it changes folder.
func (c ctx) scan(f *syntax.File) ctx {
	c.vars = map[string]setVar{}
	written := map[string]bool{}
	for k := range c.written {
		written[k] = true
	}
	lose := func(name string) { c.vars[name] = setVar{lost: true} }
	set := func(a *syntax.Assign, prefix bool) {
		if a.Name == nil || a.Naked {
			return
		}
		name := a.Name.Value
		v := word{ok: true}
		if a.Value != nil {
			v = literal(a.Value)
		}
		// VAR=v cmd sets it for cmd only; a second assignment may run on another branch
		if _, twice := c.vars[name]; twice || prefix || !v.ok || a.Append || a.Index != nil || a.Array != nil {
			lose(name)
			return
		}
		c.vars[name] = setVar{text: v.text, end: a.End().Offset()}
	}
	mark := func(w *syntax.Word) {
		if l := literal(w); l.ok {
			written[filepath.Base(l.text)] = true
		} else {
			written[filepath.Base(l.src)] = true
		}
	}
	syntax.Walk(f, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.CallExpr:
			for _, a := range n.Assigns {
				set(a, len(n.Args) > 0)
			}
			if len(n.Args) == 0 {
				break
			}
			switch name := path.Base(literal(n.Args[0]).text); {
			case name == "cd" || name == "pushd" || name == "popd":
				c.moved = true
			case name == "read" || name == "mapfile" || name == "readarray" || name == "unset" || name == "getopts" ||
				name == "printf":
				for _, w := range n.Args[1:] {
					if l := literal(w); l.ok {
						lose(l.text)
					}
				}
			case writers[name]:
				for _, w := range n.Args[1:] {
					mark(w)
				}
			}
		case *syntax.DeclClause:
			for _, a := range n.Args {
				set(a, false)
			}
		case *syntax.ForClause:
			if wi, ok := n.Loop.(*syntax.WordIter); ok {
				lose(wi.Name.Value)
			}
		case *syntax.Redirect:
			switch n.Op {
			case syntax.RdrOut, syntax.AppOut, syntax.RdrAll, syntax.AppAll, syntax.ClbOut, syntax.RdrInOut:
				mark(n.Word)
			}
		}
		return true
	})
	c.written = written
	return c
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
		if !ok || v.lost || v.end > w.Pos().Offset() {
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
