// Command crius-post-guard is a Claude Code PreToolUse hook for Bash and context-mode's ctx_execute, ctx_execute_file and
// ctx_batch_execute, in both personas and their subagents. It makes
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
// script would run (PS4, BASH_ENV, PATH) and writing nothing into its folder or bus-post's. Any other script is checked
// like a command; it then passes when the command leaves it as read (no write to it, a heredoc's included, no word
// the guard cannot read in a command that may write a file, nothing set that runs code for it) and every az and Azure
// DevOps word in it and in the commands around it sits in an az or HTTP call that was read and sends no file, a
// shell's -c text, a comment, or (an address only) a literal assignment; otherwise it asks when it or the command names
// az or Azure DevOps. A relative script name follows one cd to a full path, and also means the start folder's file
// where that cd may have failed; after an eval, source, trap or $X command it is not known. A wrapper (exec, timeout)
// with a word it cannot read before its command asks like a command name.
// In az text writes (devops invoke, rest, work-item create/update, pr create/update, wiki page create/update), a
// word the guard cannot read asks when it may split into several words or does not follow an option that takes a
// value; variables set to a literal are put in first, and a for loop's literal words are read in bash -c and eval text.
// az takes the last copy of an option and reads a value from @file; the guard does the same. When it
// cannot tell (a method, resource or fields value from a variable or @file, eval or bash -c text it cannot read, a
// command the bash parser cannot read that names az or an ADO host) it asks. Unreadable hook input also asks: a guard
// on an outward-facing write never lets one through silently.
//
// Known gaps: field-only work-item updates (state, title, description) pass by design for now; az repos pr create or
// update with --title only; a command name from a variable or a function ($AZ, f() { az "$@"; }) when the command
// names neither az nor Azure DevOps; a script run by its
// own name ($S/x.sh, ./x.sh); a command that makes bash read quoted code as a number or a name ((( 'a[$(az …)]' )),
// printf -v $'a[\x24(az …)]' x, (( $(printf …) ))): only a script run is held to text with no $, ` or \ (quotedCode);
// a $ that reaches a script's arithmetic from a file an earlier command wrote (around a script run, a $(...) runs only
// date); the code a script runs through source or an interpreter (python) when neither names az or Azure DevOps; a
// script that hides az (a'z', a PS4 prompt) when neither it nor the command names az, as for a command itself (and
// PS4='$(az ...)' bash -xc : with no script); a script past the 16th one a call reads or 4 levels down; a command
// that rewrites a script without naming it (tar, rsync, git checkout, a glob); for the pass above, az joined to other
// text (az/en/ru, "az:") is no az word, as bash never runs it as az;
// python -m azure.cli; ssh or kubectl exec to another machine; code in a script file (inline -c/-e
// code in python, node, bun, ruby, perl or pwsh asks when it names Azure DevOps and a write method); a curl config file
// (-K); an in-file written by a script; Azure DevOps MCP tools (they are not Bash calls). ctx tools: code of another
// language that runs the file it is handed by a spelling runnerRe lacks (require(path), runpy) or runs a shell command
// that reads a script (execSync('bash post.sh')); the plugin's project folder is taken to be the session's folder.
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
	"syscall"
	"time"

	"mvdan.cc/sh/v3/syntax"
)

type input struct {
	ToolName  string `json:"tool_name"`
	Cwd       string `json:"cwd"`
	ToolInput struct {
		Command  string        `json:"command"`
		Code     string        `json:"code"`     // ctx_execute, ctx_execute_file
		Language string        `json:"language"` // of Code: shell, javascript, python, …
		Path     string        `json:"path"`     // ctx_execute_file: the file the code gets as FILE_CONTENT
		Cwd      string        `json:"cwd"`      // ctx tools: where the code runs
		Commands batchCommands `json:"commands"` // ctx_batch_execute
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
		why := "crius-post-guard could not read the hook input (" + err.Error() + "), so it cannot tell whether " +
			"this posts text to Azure DevOps: needs the owner's click."
		err := ask(w, why)
		logAsk(in, why)
		return err
	}
	if why := check(in); why != "" {
		err := ask(w, why)
		logAsk(in, why)
		return err
	}
	return nil
}

// logAsk keeps each ask in $CRIUS_HOME/state/post-guard-asks.log (mode 600): the time, the tool, the folder, the reason
// and the first 200 characters of what was asked on, newlines as ⏎ (Crius 11-01 22:35: an ask on code the tab no longer
// showed was lost). It runs after the ask is written and opens the log without waiting: a FIFO or any file that is
// not a regular one is not written (review: a log that blocks would hang the hook and lose the ask). A log that cannot
// be written is said on stderr; the ask goes out either way.
func logAsk(in input, why string) {
	t := in.ToolInput
	var parts []string
	for _, p := range append([]string{t.Command, t.Path, t.Code}, t.Commands...) {
		if p != "" {
			parts = append(parts, p)
		}
	}
	oneLine := strings.NewReplacer("\r\n", "⏎", "\n", "⏎", "\r", "⏎")
	text := []rune(oneLine.Replace(strings.Join(parts, " ⏎ ")))
	if len(text) > 200 {
		text = text[:200]
	}
	home := os.Getenv("CRIUS_HOME")
	if home == "" {
		u, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintln(os.Stderr, "crius-post-guard: ask log:", err)
			return
		}
		home = filepath.Join(u, ".crius")
	}
	f, err := os.OpenFile(filepath.Join(home, "state", "post-guard-asks.log"), os.O_WRONLY|os.O_APPEND|os.O_CREATE|syscall.O_NONBLOCK, 0o600)
	if err == nil {
		if fi, serr := f.Stat(); serr != nil {
			err = serr
		} else if !fi.Mode().IsRegular() {
			err = fmt.Errorf("%s is not a regular file", f.Name())
		} else {
			_, err = fmt.Fprintf(f, "%s %s %s — %s — %s\n", time.Now().Format(time.RFC3339), in.ToolName, oneLine.Replace(in.Cwd),
				oneLine.Replace(why), string(text))
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "crius-post-guard: ask log:", err)
	}
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
	vars       map[string]setVar  // variables the command sets to a literal value at its top level, before a script runs
	loops      map[string]loopVar // variables only a for loop over literal words sets
	evals      []uint             // where each call that may set any variable (eval, source, $X) ends
	files      []runs             // this command's parse and those of the commands around it (bash -c text, a script's caller)
	moved      bool               // the command changes folder, so a relative script name is not known
	cd         *cdTo              // the one cd of this command's text the guard follows for a script's name
	movedAt    uint               // >0: an eval, source, trap or $X ending here may change folder for what follows
	depth      int                // scripts read to get here (a script that runs itself stops at 4)
	reads      *int               // scripts read for this hook call, at most 16 (15 lines of bash self.sh took 19 s)
}

// cdTo is a cd that runs before what follows it in the command's own text, up to to (the end of the text, or of the
// ( … ) it is in): a script named in there runs in dir, and up to sure only when the cd went there (… && cd D && …,
// cd D || exit; …), elsewhere maybe still in the start folder. From to on, the start folder is the folder again.
type cdTo struct {
	dir          string
	at, sure, to uint
}

// setVar is a variable's literal value and where its assignment ends; lost: also set elsewhere, computed, read, a loop's
// or under eval or source, so its value when a script runs is not known.
type setVar struct {
	text string
	end  uint
	lost bool
	in   *syntax.Stmt // set after a command in a && chain: known only inside that chain (cd /tmp && S=x && bash $S/y)
}

// loopVar is a variable that only one for loop over literal words sets: its values inside the loop's body.
type loopVar struct {
	vals     []string
	from, to uint
}

// runs is a parsed command, its text, the variables it sets to a literal, and the words in it that name a script a
// shell runs.
type runs struct {
	file    *syntax.File
	src     string
	vars    map[string]setVar
	scripts map[*syntax.Word]bool // the words that name a script a shell runs
}

// check reads a Bash command, and the code context-mode's tools run (Crius 11-01 19:39: a lead ran an az write as
// ctx_execute shell code): shell code and each ctx_batch_execute command get the Bash check (checkCode). The tool is known
// by its name's ending, whatever the plugin's server is called. The plugin runs shell code in tool_input.cwd, else in its
// project folder, and reads ctx_execute_file's path from the project folder (it takes no cwd there): the session's folder
// stands for the project folder, and a relative cwd is taken from it.
func check(in input) string {
	t := in.ToolInput
	dir := t.Cwd
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(in.Cwd, dir)
	}
	switch name := in.ToolName; {
	case name == "Bash":
		return checkBash(t.Command, ctx{cwd: in.Cwd, whole: t.Command, reads: new(int)})
	case strings.HasSuffix(name, "__ctx_batch_execute"):
		for _, command := range t.Commands {
			if why := checkBash(command, ctx{cwd: dir, whole: command, reads: new(int)}); why != "" {
				return why
			}
		}
	case strings.HasSuffix(name, "__ctx_execute"):
		return checkCode(t.Language, t.Code, "", dir, false)
	case strings.HasSuffix(name, "__ctx_execute_file"):
		return checkCode(t.Language, t.Code, t.Path, in.Cwd, true)
	}
	return ""
}

// batchCommands is ctx_batch_execute's commands as the plugin takes them: objects with a command, plain strings, either
// as JSON text, or one command as a string.
type batchCommands []string

func (b *batchCommands) UnmarshalJSON(data []byte) error {
	var s string
	if json.Unmarshal(data, &s) == nil {
		if !json.Valid([]byte(s)) {
			*b = append(*b, s)
			return nil
		}
		data = []byte(s)
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	for _, r := range raw {
		var c struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(r, &s) == nil {
			*b = append(*b, s)
			continue
		}
		if err := json.Unmarshal(r, &c); err != nil {
			return err
		}
		*b = append(*b, c.Command)
	}
	return nil
}

// azWordRe finds az as a word in code of another language: 'az' in a list, or a string that starts the CLI.
var azWordRe = regexp.MustCompile(`(?i)(^|[^\w.-])az([^\w.-]|$)`)

// runnerRe finds code of another language that starts a command or runs text.
var runnerRe = regexp.MustCompile(`\b(exec\w*|spawn\w*|system|popen|Popen|subprocess|child_process|eval|shell_exec|passthru|Deno\.Command)\b`)

// unreadFile stands for a file ctx_execute_file names that the guard cannot read: it may hold an az command.
const unreadFile = "az (a file the guard cannot read)"

// checkCode reads the code of ctx_execute and ctx_execute_file. Shell code gets the Bash check; code in another language
// asks when it names az (execSync('az …'), the plugin's own pattern) or an ADO host with a write word. ctx_execute_file
// hands the code the file at path as FILE_CONTENT and FILE_CONTENT_PATH: shell code gets the plugin's own lines that set
// them first, so bash or source "$FILE_CONTENT_PATH" reads the file as in Bash, and where the guard cannot read what it
// runs (eval "$FILE_CONTENT") the file's text counts as part of the command; other code that starts commands asks when
// the file names az or ADO. A file that is not a regular one of at most 1 MiB is not read (a FIFO would hang the hook).
func checkCode(lang, code, file, cwd string, hasFile bool) string {
	text := ""
	if hasFile {
		text = unreadFile
		if !filepath.IsAbs(file) && cwd != "" {
			file = filepath.Join(cwd, file)
		}
		if fi, err := os.Stat(file); err == nil && fi.Mode().IsRegular() && fi.Size() <= 1<<20 && filepath.IsAbs(file) {
			if b, err := os.ReadFile(file); err == nil {
				text = string(b)
			}
		}
	}
	if lang == "shell" {
		if hasFile {
			q := "'" + strings.ReplaceAll(file, "'", `'\''`) + "'"
			code = "FILE_CONTENT_PATH=" + q + "\nfile_path=" + q + "\nFILE_CONTENT=$(cat " + q + ")\n" + code
		}
		return checkBash(code, ctx{cwd: cwd, whole: code + "\n" + text, reads: new(int)})
	}
	switch {
	case azWordRe.MatchString(code) || adoMarkRe.MatchString(code):
		// review r3: a write needs no method word (urllib data=, the Azure DevOps SDKs)
		return asked(lang + " code that runs az or calls Azure DevOps")
	case hasFile && runnerRe.MatchString(code) && (azWordRe.MatchString(text) || adoMarkRe.MatchString(text)):
		return asked(lang + " code that starts a command and a file that names az or Azure DevOps")
	}
	return ""
}

// adoMarkRe finds a sign of Azure DevOps in a command: a host, an API path, or a variable that only an ADO call uses.
var adoMarkRe = regexp.MustCompile(`(?i)dev\.azure\.com|\.visualstudio\.com|/_apis/|AZURE_DEVOPS|SYSTEM_ACCESSTOKEN|SYSTEM_COLLECTIONURI`)

// adoRe finds an az call or a sign of Azure DevOps in text the guard cannot read.
var adoRe = regexp.MustCompile(`(?i)(^|[^\w.-])az(\s|$)|` + adoMarkRe.String()[4:])

func asked(what string) string { return reason + " (" + what + ")" }

// unsure asks when text the guard cannot read sits in a command that names az or Azure DevOps.
func unsure(c ctx, what string) string {
	// review r4: az next to ), `, ] or " counts too ($(which az), ${commands[az]})
	if azWordRe.MatchString(c.whole) || adoMarkRe.MatchString(c.whole) {
		return asked(what + " the guard cannot read")
	}
	return ""
}

func checkBash(command string, c ctx) string {
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil {
		// c.whole holds the file ctx_execute_file hands the code (review r3: zsh code that reads it does not parse)
		if adoRe.MatchString(command + "\n" + c.whole) {
			return asked("the guard cannot parse this command, so it cannot tell whether it writes")
		}
		return ""
	}
	c = c.scan(f, command)
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

// quotedCode says whether a command holds literal text with $, ` or \ in it. Bash runs a $(...) in text it reads as a
// number or a name ((( 'a[$(…)]' )), [[ … -eq 0 ]], a script's (( n > 0 )) on its $1), and a \ makes one at run time
// ($'\x24', printf '\x24'); reviews 8-11 found such places one by one, so a script run passes only when the command's
// own text can make no $ or ` at all (review of f0e77a8: a rule for every command asked on az … | awk '{print $1}').
func quotedCode(f *syntax.File) bool {
	found := false
	syntax.Walk(f, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.SglQuoted:
			found = found || strings.ContainsAny(n.Value, "$`\\")
		case *syntax.Lit:
			found = found || strings.ContainsAny(n.Value, "$`\\")
		}
		return !found
	})
	return found
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

// envSetRe is a word that starts NAME=: env takes it as a setting, and no command has such a name.
var envSetRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

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
		if name, ok := c.resolve(args[0].node); ok {
			args = append([]word{{text: name, src: args[0].src, ok: true, node: args[0].node}}, args[1:]...)
		} else {
			// a command name from text the guard cannot read ($FILE_CONTENT, X=$(cat f); $X), review r2 of the ctx tools check
			return unsure(c, "a command name")
		}
	}
	name := path.Base(args[0].text)
	switch {
	case (name == "source" || name == ".") && len(args) > 1:
		if args[1].text == "--" && len(args) > 2 {
			return checkScript(args[2], c, false)
		}
		return checkScript(args[1], c, false)
	case name == "eval":
		// each text the words may make, when their variables are set to a literal or by a loop over literal words
		lines := []string{""}
		for i, a := range args[1:] {
			vals := []string{a.text}
			if !a.ok {
				var ok bool
				if vals, ok = c.texts(a.node); !ok {
					return unsure(c, "eval text")
				}
			}
			var next []string
			for _, l := range lines {
				for _, v := range vals {
					if i > 0 {
						v = " " + v
					}
					next = append(next, l+v)
				}
			}
			if lines = next; len(lines) > maxTexts {
				return unsure(c, "eval text")
			}
		}
		for _, l := range lines {
			if why := checkBash(l, c); why != "" {
				return why
			}
		}
		return ""
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
		runsNext := name != "find" // find runs a command only after -exec, -execdir, -ok or -okdir
		for i := 1; i < len(args); i++ {
			a := args[i]
			if !a.ok && a.node != nil {
				if t, known := c.resolve(a.node); known {
					a = word{text: t, src: a.src, ok: true, node: a.node}
					args = append(slices.Clone(args[:i]), append([]word{a}, args[i+1:]...)...)
				}
			}
			if !a.ok {
				// review r1: a word it cannot read may be the command it runs (exec $X …), so it asks like a command name;
				// not an env setting (NAME=$v) or a find word before its command
				if !runsNext || envSetRe.MatchString(a.src) {
					continue
				}
				return unsure(c, "a command name")
			}
			if name == "find" {
				runsNext = runsNext || a.text == "-exec" || a.text == "-execdir" || a.text == "-ok" || a.text == "-okdir"
			}
			if n := path.Base(a.text); n == "az" || n == "eval" || (n == "source" || n == ".") && (name == "builtin" || name == "command") || shells[n] || httpTools[n] || interpreters[n] {
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
	case noExec(args[1:min(i, len(args))]):
		return "" // bash -n reads the commands and runs none
	case sawC && i < len(args):
		if args[i].ok {
			return checkBash(args[i].text, c)
		}
		texts, ok := c.texts(args[i].node)
		if !ok {
			return unsure(c, "bash -c text")
		}
		for _, t := range texts {
			if why := checkBash(t, c); why != "" {
				return why
			}
		}
		return ""
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

// noExec says whether a shell's options make it read its commands and run none: -n or -o noexec, last one winning, not
// interactive (an interactive shell ignores -n), every option a literal.
func noExec(opts []word) bool {
	on, interactive := false, false
	for j := 0; j < len(opts); j++ {
		o := opts[j]
		if !o.ok {
			return false
		}
		if len(o.text) < 2 || (o.text[0] != '-' && o.text[0] != '+') || strings.HasPrefix(o.text, "--") {
			continue
		}
		set := o.text[0] == '-'
		for _, ch := range o.text[1:] {
			switch ch {
			case 'n':
				on = set
			case 'i':
				interactive = true
			case 'o', 'O':
				if j++; j < len(opts) {
					if !opts[j].ok {
						return false
					}
					if ch == 'o' && opts[j].text == "noexec" {
						on = set
					}
				}
			}
		}
	}
	return on && !interactive
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
// (safeAround). Any other script is checked like a command (scriptAt); it, one the guard cannot find, and one the
// command names relative to a folder it left (but the one cd followCd follows) ask when the script or the command names
// az or Azure DevOps. A script is not passed on
// its list either when the shell takes options that run other code (plain: no --rcfile or -i; zsh and ksh read a
// start-up file always).
func checkScript(a word, c ctx, plain bool) string {
	name, ok := a.text, a.ok
	if !ok && a.node != nil {
		name, ok = c.resolve(a.node)
	}
	if !ok {
		return unsure(c, "a shell script name")
	}
	switch {
	case filepath.IsAbs(name):
		return scriptAt(name, c, plain, false)
	case c.movedAt > 0 && (a.node == nil || a.node.End().Offset() > c.movedAt):
		return unsure(c, "a shell script name")
	case c.cwd != "" && (!c.moved || c.cd != nil && a.node != nil && a.node.Pos().Offset() >= c.cd.to):
		return scriptAt(filepath.Join(c.cwd, name), c, plain, false)
	case c.cd == nil || a.node == nil || a.node.Pos().Offset() < c.cd.at || a.node.End().Offset() > c.cd.to:
		return unsure(c, "a shell script name")
	}
	// 11-05: after cd D the script is D's; where the cd may have failed, it is the start folder's too, and one that is
	// not there runs nothing
	if why := scriptAt(filepath.Join(c.cd.dir, name), c, plain, false); why != "" || a.node.End().Offset() <= c.cd.sure {
		return why
	}
	if c.cwd == "" {
		return unsure(c, "a shell script name")
	}
	return scriptAt(filepath.Join(c.cwd, name), c, plain, true)
}

// scriptAt judges the script at the path name; orNone: when no file is there and nothing in the command writes one,
// bash runs nothing.
func scriptAt(name string, c ctx, plain, orNone bool) string {
	if c.reads != nil {
		if *c.reads++; *c.reads > 16 {
			return unsure(c, "more shell scripts than the guard reads")
		}
	}
	var texts []string // each text the script may hold when it runs
	fi, err := os.Stat(name)
	missing := os.IsNotExist(err)
	if err == nil && fi.Mode().IsRegular() && fi.Size() <= 1<<20 {
		var body []byte
		if body, err = os.ReadFile(name); err == nil {
			texts = append(texts, string(body))
			if plain && c.safeAround(name, string(body)) && safeScript(string(body)) {
				return ""
			}
		}
	}
	// any other script gets the command check ("az" in quotes, review of ace0c14); it sees the command around it (its
	// arguments, a file the command writes), so its unsure text is its own and the command's
	if c.depth >= 4 {
		return unsure(c, "a shell script four levels down")
	}
	same := c.untouched(name)
	if len(texts) == 0 {
		if missing && same && orNone {
			return ""
		}
		return unsure(c, "a shell script")
	}
	for _, t := range texts {
		if why := checkBash(t, ctx{whole: t + "\n" + c.whole, depth: c.depth + 1, reads: c.reads, files: c.files}); why != "" {
			return why
		}
		// review r2: the script's own text is held to the same rule as the command around it (printf … >> "$0",
		// cp "$1" "$0")
		f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(t), "")
		if err != nil || !(ctx{cwd: c.cwd, moved: c.moved}).scan(f, t).untouched(name) {
			same = false
		}
	}
	// 11-05: a script whose every az and Azure DevOps word is in a call the check read passes (113 asks in a day were
	// scripts that read through az, test or post to the bus). It asks as before when it may be other than the guard read
	// (a shell that reads a start-up file, a command that writes it in another way or sets PATH or PS4), or az or Azure
	// DevOps text sits where a script may run it: an argument, a value it inherits, a loop's words, a heredoc to python.
	if !plain || !same || !c.mentionsRead(texts) {
		return unsure(ctx{whole: strings.Join(texts, "\n") + "\n" + c.whole}, "a shell script")
	}
	return ""
}

// maxTexts is the most texts a word or a script is checked as (each value of a loop's variable, each heredoc).
const maxTexts = 16

// texts gives each value a word may have when its only expansions are plain $X of a variable the command set to a
// literal before it, or of a for loop's variable inside that loop, whose words are literal (for n in 7730 7736).
func (c ctx) texts(w *syntax.Word) ([]string, bool) {
	if w == nil {
		return nil, false
	}
	out := []string{""}
	add := func(vals ...string) bool {
		var next []string
		for _, o := range out {
			for _, v := range vals {
				next = append(next, o+v)
			}
		}
		out = next
		return len(out) <= maxTexts
	}
	param := func(pe *syntax.ParamExp) bool {
		if s, ok := c.resolve(&syntax.Word{Parts: []syntax.WordPart{pe}}); ok {
			return add(s)
		}
		if pe.Excl || pe.Length || pe.Width || pe.Index != nil || pe.Slice != nil || pe.Repl != nil || pe.Names != 0 ||
			pe.Exp != nil || pe.Param == nil {
			return false
		}
		lv, ok := c.loops[pe.Param.Value]
		at := pe.Pos().Offset()
		for _, e := range c.evals {
			ok = ok && (e < lv.from || e > at)
		}
		return ok && at >= lv.from && pe.End().Offset() <= lv.to && add(lv.vals...)
	}
	for _, p := range w.Parts {
		switch p := p.(type) {
		case *syntax.Lit, *syntax.SglQuoted:
			s, ok := c.resolve(&syntax.Word{Parts: []syntax.WordPart{p}})
			if !ok || !add(s) {
				return nil, false
			}
		case *syntax.ParamExp:
			if !param(p) {
				return nil, false
			}
		case *syntax.DblQuoted:
			for _, q := range p.Parts {
				switch q := q.(type) {
				case *syntax.Lit:
					if !add(unescape(q.Value, "\"\\$`")) {
						return nil, false
					}
				case *syntax.ParamExp:
					if !param(q) {
						return nil, false
					}
				default:
					return nil, false
				}
			}
		default:
			return nil, false
		}
	}
	return out, true
}

// untouched says whether the command around a script run leaves the script as the guard reads it: no word in it names the
// script's file (by name, through a variable, or as the same file) but the run itself, an assignment, a read (cat, grep,
// bash -n, a test); no write the guard cannot place (what cat or a heredoc writes into it is never taken as its text,
// review r1); only commands shown to write no file (a command may write the script without naming it: cp -R, tar x,
// review r2); and nothing sets a variable that runs code or finds commands for the script (PATH, PS4, BASH_ENV, an
// exported function). scriptAt holds the script's own text to the same rule.
func (c ctx) untouched(script string) bool {
	base := strings.ToLower(filepath.Base(script))
	for i := range c.files {
		r := &c.files[i]
		if codeSet(r.src) {
			return false
		}
		rc := ctx{cwd: c.cwd, vars: r.vars, moved: c.moved}
		names := func(w *syntax.Word) bool {
			if strings.Contains(strings.ToLower(literal(w).src), base) {
				return true
			}
			s, ok := rc.resolve(w)
			if !ok {
				return false
			}
			if !filepath.IsAbs(s) && !c.moved && c.cwd != "" {
				s = filepath.Join(c.cwd, s)
			}
			a, aerr := os.Stat(s)
			b, berr := os.Stat(script)
			return strings.Contains(strings.ToLower(s), base) || aerr == nil && berr == nil && os.SameFile(a, b)
		}
		fine := map[*syntax.Word]bool{} // words that may name the script and leave it as it is
		ok := true
		funcs := map[string]bool{} // a call to one runs its body, which is read here as well
		syntax.Walk(r.file, func(n syntax.Node) bool {
			if fd, isFn := n.(*syntax.FuncDecl); isFn && fd.Name != nil {
				funcs[fd.Name.Value] = true
			}
			return true
		})
		syntax.Walk(r.file, func(n syntax.Node) bool {
			switch n := n.(type) {
			case *syntax.Assign:
				if n.Value != nil {
					fine[n.Value] = true
				}
			case *syntax.TestClause:
				syntax.Walk(n, func(m syntax.Node) bool {
					if w, isWord := m.(*syntax.Word); isWord {
						fine[w] = true
					}
					return true
				})
			case *syntax.Stmt:
				ce, isCall := n.Cmd.(*syntax.CallExpr)
				name := ""
				around := false // a shell that only reads (bash -n), or runs the -c text read as one of c.files
				if isCall && len(ce.Args) > 0 {
					name, _ = cmdName(ce.Args[0])
					if k, _, _ := shellOpts(literalWords(ce.Args)); shells[name] && noExec(literalWords(ce.Args[1:min(k, len(ce.Args))])) {
						around = true
					}
					for j, w := range ce.Args[1:] {
						if readCmds[name] || noFileCmds[name] || shells[name] && noExec(literalWords(ce.Args[1:j+1])) {
							fine[w] = true
						}
					}
					// bash -c text: when it runs the script it is a later one of c.files, read there word by word; any other
					// text the guard reads is held to these rules on its own, and none it cannot read stands around a script
					if k, sawC, _ := shellOpts(literalWords(ce.Args)); shells[name] && sawC && k < len(ce.Args) {
						t, known := rc.resolve(ce.Args[k])
						switch {
						case known && slices.ContainsFunc(c.files[i+1:], func(f runs) bool { return f.src == t }):
							fine[ce.Args[k]] = true
							around = true
						case known && k == len(ce.Args)-1:
							if f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(t), ""); err == nil &&
								(ctx{cwd: c.cwd, moved: c.moved}).scan(f, t).untouched(script) {
								fine[ce.Args[k]] = true
								around = true
							}
						}
					}
				}
				// review r1, r2: a command may write the script without naming it (cp x ${S%.x}, cp -R new/. dir/, tar x), so
				// only commands shown to write no file may be around it: writesNoFile, mkdir and touch (which leave a
				// file's text as it is), and around
				if isCall && len(ce.Args) > 0 && !writesNoFile(ce, r) && !keepCmds[name] && !around && !funcs[name] &&
					!shownNoWrite(name, literalWords(ce.Args[1:])) {
					ok = false
				}
				for _, rd := range n.Redirs {
					switch rd.Op {
					case syntax.RdrOut, syntax.AppOut, syntax.RdrAll, syntax.AppAll, syntax.ClbOut, syntax.RdrInOut:
					case syntax.DplOut:
						if l := literal(rd.Word); l.ok && (l.text == "-" || strings.Trim(l.text, "0123456789") == "") {
							continue
						}
					default:
						continue
					}
					// a write the guard cannot place may be the script; one to it makes it other than read (review r1: what
					// cat or a heredoc writes is not taken as the script's text)
					if _, placed := rc.resolve(rd.Word); !placed || names(rd.Word) {
						ok = false
					}
					fine[rd.Word] = true
				}
			}
			return ok
		})
		if !ok {
			return false
		}
		syntax.Walk(r.file, func(n syntax.Node) bool {
			if w, isWord := n.(*syntax.Word); isWord && ok && !r.scripts[w] && !fine[w] && names(w) {
				ok = false
			}
			return ok
		})
		if !ok {
			return false
		}
	}
	return true
}

// shownNoWrite says whether a find, sed or sort call is shown to write no file: every word readable, and no option or
// program part that writes or runs (find -delete, -fprint, -fls, -exec, -ok; sed -i, -f, a long option, or a w, W or e
// in its program; sort -o).
func shownNoWrite(name string, args []word) bool {
	if name != "find" && name != "sed" && name != "sort" {
		return false
	}
	for _, a := range args {
		t := a.text
		short := strings.HasPrefix(t, "-") && !strings.HasPrefix(t, "--")
		switch {
		case !a.ok:
			return false
		case name == "find" && (t == "-delete" || t == "-fls" || strings.HasPrefix(t, "-fprint") ||
			strings.HasPrefix(t, "-exec") || strings.HasPrefix(t, "-ok")):
			return false
		case name == "sed" && (short && strings.ContainsAny(t, "iIf") || strings.HasPrefix(t, "--") ||
			!strings.HasPrefix(t, "-") && strings.ContainsAny(t, "wWe")):
			return false
		case name == "sort" && (short && strings.Contains(t, "o") || strings.HasPrefix(t, "--o")):
			return false
		}
	}
	return true
}

// keepCmds may make a file or folder but leave every file's text as it is, and may stand around a script.
var keepCmds = map[string]bool{"mkdir": true, "touch": true, "date": true, "seq": true, "dirname": true, "basename": true}

// noFileCmds write no file a word of theirs names (beside readCmds).
var noFileCmds = map[string]bool{"echo": true, "printf": true, "sleep": true, "true": true, "false": true, ":": true,
	"jq": true, "tr": true, "cut": true, "kill": true, "cd": true, "set": true, "read": true, "shift": true, "exit": true, "break": true, "continue": true,
	"return": true, "wait": true}

// writesNoFile says whether a call writes no file its words name: a read or print command, a shell that runs a script
// of r's (the words after it reach the script, which is checked), a git that reads (safeGit), or an az command that
// only reads (readVerb, devops invoke with GET) with no option that names a file or folder.
func writesNoFile(ce *syntax.CallExpr, r *runs) bool {
	if len(ce.Args) == 0 {
		return true
	}
	name, ok := cmdName(ce.Args[0])
	switch {
	case !ok:
		return false
	case readCmds[name] || noFileCmds[name]:
		return true
	case shells[name]:
		return slices.ContainsFunc(ce.Args[1:], func(w *syntax.Word) bool { return r.scripts[w] })
	case name == "git":
		return safeGit(literalWords(ce.Args[1:]))
	case name == "az":
		// what it posts is checkBash's to rule on; a variable set to a literal is read as bash expands it (--org $ORG,
		// and O="--org URL" unquoted is two words)
		var args []word
		for _, w := range ce.Args[1:] {
			l := literal(w)
			v, known := (ctx{vars: r.vars}).resolve(w)
			switch {
			case l.ok || !known:
				args = append(args, l)
			case oneWord(w):
				args = append(args, word{text: v, src: l.src, ok: true, node: w})
			case strings.ContainsAny(v, "*?["):
				args = append(args, l)
			default:
				for _, f := range strings.Fields(v) {
					args = append(args, word{text: f, src: f, ok: true})
				}
			}
		}
		return azNoFile(args)
	case name == "exec":
		return len(ce.Args) == 1 // exec > log: only its redirects, read on their own
	}
	return false
}

// cmdName gives a command's name when it is a literal bare name or a path in a system folder (/usr/bin/grep), so the
// command is the one its name says (review r2: ./cat is any program).
func cmdName(w *syntax.Word) (string, bool) {
	l := literal(w)
	if !l.ok || strings.Contains(l.text, "/") && !sysDirs[path.Dir(l.text)] {
		return "", false
	}
	return path.Base(l.text), true
}

// azReads says whether an az command is shown to only read: a readVerb command or devops invoke with GET, every command
// word readable, and no option that names a file (azFileOpt).
func azReads(args []word) bool {
	o := parseAz(args)
	words := strings.Fields(o.cmd)
	m, set := o.last("--http-method")
	get := o.cmd == "devops invoke" && (!set || m.ok && strings.EqualFold(m.text, "get"))
	return len(words) > 0 && (readVerb(words[len(words)-1]) || get) && !o.unreadCmd && !azFileOpt(o)
}

// azNoFile says whether an az command is shown to name no local file: every command word readable; no word the guard
// cannot read that may split into more words (az devops invoke … $X may be --out-file x) or that stands where no option
// name is before it ("$X" after a command word or --a=b), unless it starts with a literal (commitId="$3"); and no option
// that names a file (azFileOpt). Known gap: a quoted word after a flag that holds a whole option (--out-file=x); az
// takes it as an option, and only after a flag.
func azNoFile(args []word) bool {
	o := parseAz(args)
	if o.unreadCmd || azFileOpt(o) {
		return false
	}
	for i, a := range args {
		if a.ok {
			continue
		}
		afterName := i > 0 && args[i-1].ok && strings.HasPrefix(args[i-1].text, "-") && !strings.Contains(args[i-1].text, "=")
		if !oneWord(a.node) || !afterName && !fixedStart(a.node) {
			return false
		}
	}
	return true
}

// azFileOpt says whether an az command has an option (or an abbreviation of one) that names a file or folder (--in-file,
// --out-file, --path, -f).
func azFileOpt(o azOpts) bool {
	for k := range o.opts {
		if k == "--output" || k == "-o" {
			continue // the output format
		}
		if k == "-f" {
			return true
		}
		for _, p := range strings.Split(strings.TrimLeft(k, "-"), "-") {
			for _, w := range []string{"file", "path", "dest", "dir", "body", "folder", "out"} {
				// a part that holds one of these words, or starts one (--ou, --f), may name a file (review r2:
				// --route-parameters is not one)
				if strings.HasPrefix(w, p) || strings.HasPrefix(p, w) || w != "out" && strings.Contains(p, w) {
					return true
				}
			}
		}
	}
	return false
}

// httpReads says whether a curl or wget call is shown to only read: no option that sets a method, sends data or a
// file, or reads a config (-X, -d, --data*, --json, -F, -T, -K, --post-*, --method, --body*), also in a short cluster,
// and no option word the guard cannot read. Other HTTP tools are never shown to read.
func httpReads(name string, args []word) bool {
	if name != "curl" && name != "wget" {
		return false
	}
	for _, a := range args {
		if !a.ok {
			if strings.HasPrefix(a.src, "-") || strings.HasPrefix(a.src, "\"-") || strings.HasPrefix(a.src, "$") {
				return false
			}
			continue
		}
		t := a.text
		for _, p := range []string{"--request", "--data", "--json", "--form", "--upload", "--config", "--post", "--method",
			"--body", "--expand"} {
			if strings.HasPrefix(t, p) {
				return false
			}
		}
		if name == "curl" && strings.HasPrefix(t, "-") && !strings.HasPrefix(t, "--") && strings.ContainsAny(t, "dFTXK") {
			return false
		}
		if strings.HasPrefix(t, "@") {
			return false
		}
	}
	return true
}

// toolAt gives the place and name of a call's az or HTTP tool, also after a wrapper (timeout 60 az …); k is -1 when
// there is none.
func toolAt(args []*syntax.Word) (int, string) {
	k := 0
	if len(args) > 0 {
		if n, ok := cmdName(args[0]); ok && wrappers[n] {
			k = slices.IndexFunc(args, func(w *syntax.Word) bool {
				n, ok := cmdName(w)
				return ok && (n == "az" || httpTools[n])
			})
		}
	}
	if k < 0 || k >= len(args) {
		return -1, ""
	}
	if n, ok := cmdName(args[k]); ok && (n == "az" || httpTools[n]) {
		return k, n
	}
	return -1, ""
}

// readCall says whether a call is an az or HTTP call shown to only read.
func readCall(args []*syntax.Word) bool {
	k, n := toolAt(args)
	switch {
	case k < 0:
		return false
	case n == "az":
		return azReads(literalWords(args[k+1:]))
	}
	return httpReads(n, literalWords(args[k+1:]))
}

// readCmds are the commands that only read the files they name.
var readCmds = map[string]bool{"cat": true, "head": true, "tail": true, "grep": true, "wc": true, "ls": true, "test": true,
	"[": true, "shellcheck": true, "file": true, "stat": true, "cmp": true, "diff": true, "shasum": true, "md5": true}

func literalWords(ws []*syntax.Word) []word {
	out := make([]word, len(ws))
	for i, w := range ws {
		out[i] = literal(w)
	}
	return out
}

// codeNameWordRe finds a variable name codeNameRe holds as a word, with a $ or ${ before it and := or = after it.
var codeNameWordRe = regexp.MustCompile(`(\$\{?)?\b(PS[0-4]|BASH_ENV|ENV|ZDOTDIR|PATH|HOME|IFS|CDPATH|GLOBIGNORE|SHELLOPTS|` +
	`BASHOPTS|PROMPT_COMMAND|[A-Z_]*PAGER|EDITOR|VISUAL|LESSOPEN|LESSCLOSE|(GIT|BASH|LD|DYLD|XDG)_[A-Za-z0-9_]*)\b(:?=)?`)

// ifsReadRe is IFS= in front of read, at a command's start.
var ifsReadRe = regexp.MustCompile(`(^|[;&|(\s])IFS= read `)

// exportFnRe finds an exported function (export -f, declare -fx) or set -a.
var exportFnRe = regexp.MustCompile(`\b((export|declare|typeset)\s+-[a-zA-Z]*f|set\s+-[a-zA-Z]*a)`)

// codeSet says whether text may set a variable that runs code or finds commands for a script it starts (PATH=, read PS4,
// ${BASH_ENV:=x}, a name like that in any other place) or export a function. Only $NAME and ${NAME} read one.
func codeSet(text string) bool {
	text = ifsReadRe.ReplaceAllString(text, "${1}read ") // IFS= read sets IFS for read alone
	for _, m := range codeNameWordRe.FindAllStringSubmatch(text, -1) {
		if m[1] == "" || m[1] == "${" && m[4] != "" {
			return true
		}
	}
	return exportFnRe.MatchString(text)
}

// mentionsRead says whether every az and Azure DevOps word in a script's texts and in the commands around it is in a call
// the command check read: an az or HTTP call (also after a wrapper), a comment, or for an Azure DevOps address a literal
// assignment with no $, ` or \.
func (c ctx) mentionsRead(texts []string) bool {
	for _, t := range texts {
		if !mentionsIn(t) {
			return false
		}
	}
	for i := range c.files {
		if !mentionsIn(c.files[i].src) {
			return false
		}
	}
	return true
}

// azWords gives the place of each az that azWordRe finds, also next to one another (az az), which FindAll would skip.
func azWords(src string) []int {
	var out []int
	word := func(i int) bool {
		if i < 0 || i >= len(src) {
			return false
		}
		ch := src[i]
		return ch == '_' || ch == '.' || ch == '-' || ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z'
	}
	for i := 0; i+1 < len(src); i++ {
		if strings.EqualFold(src[i:i+2], "az") && !word(i-1) && !word(i+2) {
			out = append(out, i)
		}
	}
	return out
}

// azToken says whether the az at p may be a word bash runs as az: quotes and backslashes aside, a blank, the start or
// end, or a character that ends a word (;|&()<>`) on its right, and one of those, / or = on its left. Text such as
// az/en/ru, "az:" or "az," (the Azerbaijani locale in staffy's copy, 11-05) is a word bash never runs as az; a script
// that splits or slices it into one runs a command name or eval the check cannot read, and that asks on its own.
func azToken(src string, p int) bool {
	ends := func(ch byte) bool { return strings.IndexByte(" \t\n\r;|&()<>`", ch) >= 0 }
	r := p + 2
	for r < len(src) && strings.IndexByte(`'"\`, src[r]) >= 0 {
		r++
	}
	l := p - 1
	for l >= 0 && strings.IndexByte(`'"\`, src[l]) >= 0 {
		l--
	}
	return (r == len(src) || ends(src[r])) && (l < 0 || ends(src[l]) || src[l] == '/' || src[l] == '=')
}

func mentionsIn(src string) bool {
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash), syntax.KeepComments(true)).Parse(strings.NewReader(src), "")
	if err != nil {
		return !adoRe.MatchString(src)
	}
	type span struct {
		from, to uint
		url      bool // only an Azure DevOps address may sit here
	}
	spans := []span{}
	syntax.Walk(f, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.Comment:
			spans = append(spans, span{n.Pos().Offset(), n.End().Offset(), false})
		case *syntax.Assign:
			if n.Value != nil {
				if l := literal(n.Value); l.ok && !strings.ContainsAny(l.src, "$`\\") {
					spans = append(spans, span{n.Value.Pos().Offset(), n.Value.End().Offset(), true})
				}
			}
		case *syntax.CallExpr:
			// an az or HTTP call shown to only read (review r2: not one the check only failed to see as Azure DevOps)
			if readCall(n.Args) {
				spans = append(spans, span{n.Pos().Offset(), n.End().Offset(), false})
			}
			// a shell's -c text, also after a wrapper: checkShell read it as a command of its own, and a script it runs
			// sees it as one of the commands around (its arguments after the text are not read here)
			isShell := func(w *syntax.Word) bool { l := literal(w); return l.ok && shells[path.Base(l.text)] }
			k := 0
			if len(n.Args) > 0 && wrappers[path.Base(literal(n.Args[0]).text)] {
				k = slices.IndexFunc(n.Args, isShell)
			}
			if k >= 0 && k < len(n.Args) && isShell(n.Args[k]) {
				if i, sawC, _ := shellOpts(literalWords(n.Args[k:])); sawC && k+i < len(n.Args) {
					w := n.Args[k+i]
					spans = append(spans, span{w.Pos().Offset(), w.End().Offset(), false})
				}
			}
		}
		return true
	})
	in := func(p int, url bool) bool {
		for _, s := range spans {
			if uint(p) >= s.from && uint(p) < s.to && (!s.url || url) {
				return true
			}
		}
		return false
	}
	for _, p := range azWords(src) {
		if !azToken(src, p) {
			continue
		}
		if !in(p, false) {
			return false
		}
	}
	for _, m := range adoMarkRe.FindAllStringIndex(src, -1) {
		if !in(m[0], true) {
			return false
		}
	}
	return true
}

// safeAround says whether the command around a script run leaves the script nothing that runs code. A script on the
// list runs only listed commands, so only what reaches it from the shell that starts it could post: a variable it
// inherits that runs code (PS4 with set -x, BASH_ENV, PATH, XDG_CONFIG_HOME for git, an exported function) or a file
// it or bus-post is read from. Proving that the commands around write no such file failed six reviews (awk > file,
// git --output, a glob or ~user redirect target), so the command around may hold only what writes nothing and sets
// nothing: commands that print or read (aroundCmds, git as safeGit reads it), cd, a shell running a script (read on
// its own), an export or declare of a name a script word uses ($S), and a plain assignment of that name or another;
// codeNameRe names none of them, the script reads none, and export and declare take no option (-f, -n). No loop,
// function, ${X:=}, arithmetic or [[ ]] (bash runs a $(...) in a value they read), and no assignment in front of a
// command (it reaches that command). A file is written only by a redirect, whose target must be a path the guard
// reads as bash does (no glob, no ~ but ~/), outside the script's folder and bus-post's, and no link.
func (c ctx) safeAround(script, body string) bool {
	home, _ := os.UserHomeDir()
	keep := []string{realDir(filepath.Dir(script)), filepath.Dir(realDir(script)), realDir(home + "/.crius/bin")}
	reads, readsAny := scriptReads(body)
	for _, r := range c.files {
		if quotedCode(r.file) {
			return false
		}
		used := map[string]bool{} // the names the script words use
		for w := range r.scripts {
			syntax.Walk(w, func(n syntax.Node) bool {
				if pe, ok := n.(*syntax.ParamExp); ok && pe.Param != nil {
					used[pe.Param.Value] = true
				}
				return true
			})
		}
		// an export reaches the script, so only a name its path uses; a plain assignment reaches it too when the session
		// exported the name before, which the guard cannot see, so no name the script reads may be set (review of
		// 3d38316: SAY_FREE_GB='x[$(…)]' runs in say.sh's (( free >= 25 ))), and bash reads one with no $ in (( N )),
		// [[ N -gt 0 ]], ${a[N]} and ${!r} (review of fd71df9): none of scriptReads.
		// M='text'; bash $S/say.sh "$M" passes
		named := func(a *syntax.Assign, export bool) bool {
			if a.Name == nil || codeNameRe.MatchString(a.Name.Value) ||
				reads[a.Name.Value] || readsAny {
				return false
			}
			return used[a.Name.Value] || !export
		}
		safe := true
		syntax.Walk(r.file, func(n syntax.Node) bool {
			switch n := n.(type) {
			case *syntax.ParamExp:
				safe = safe && n.Exp == nil && n.Slice == nil && n.Repl == nil && n.Index == nil && n.Names == 0 &&
					!n.Excl && !n.Width
			case *syntax.CmdSubst:
				// its output may reach what the script reads, and tr, cut or head make a $ from text with none (review of
				// 260216b: tr '"-%' '#-&' turns # into $): only date with literal words, whose output holds no $ or `
				safe = safe && dateOnly(n)
			case *syntax.ProcSubst:
				safe = false
			case *syntax.ForClause, *syntax.WhileClause, *syntax.FuncDecl, *syntax.ArithmExp, *syntax.ArithmCmd,
				*syntax.LetClause, *syntax.TestClause, *syntax.CoprocClause, *syntax.TimeClause:
				safe = false
			case *syntax.DeclClause:
				for _, a := range n.Args {
					safe = safe && named(a, true)
				}
			case *syntax.Redirect:
				// {NAME}> sets a variable; 2>&1 does not (review of 73f3942)
				safe = safe && (n.N == nil || !strings.HasPrefix(n.N.Value, "{")) && c.safeWrite(n, keep)
			case *syntax.CallExpr:
				if len(n.Args) == 0 {
					for _, a := range n.Assigns {
						safe = safe && named(a, false)
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

// scriptReads gives the names a script reads: each $NAME, and each word in arithmetic, [[ ]] and an array index or
// slice, where bash reads a name with no $ and runs a $(...) in its value (review of fd71df9: (( N > 0 ))), also in the
// text a shell in it runs; any, when it reads a name it computes (${!r}; safeScript refuses declare -n) or does not
// parse.
func scriptReads(body string) (map[string]bool, bool) {
	names := map[string]bool{}
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(body), "")
	if err != nil {
		return names, true
	}
	any := false
	words := func(n syntax.Node) {
		syntax.Walk(n, func(m syntax.Node) bool {
			v := ""
			switch m := m.(type) {
			case *syntax.CmdSubst, *syntax.ProcSubst: // code it runs, walked as code: $(date +%M) reads no M
				return false
			case *syntax.Lit:
				v = m.Value
			case *syntax.SglQuoted: // [[ 'N' -gt 0 ]] reads N
				v = m.Value
			}
			for _, w := range regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`).FindAllString(v, -1) {
				names[w] = true
			}
			return true
		})
	}
	syntax.Walk(f, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.ParamExp:
			if n.Param != nil {
				names[n.Param.Value] = true
			}
			any = any || n.Excl
			if n.Index != nil {
				words(n.Index)
			}
			if n.Slice != nil {
				if n.Slice.Offset != nil {
					words(n.Slice.Offset)
				}
				if n.Slice.Length != nil {
					words(n.Slice.Length)
				}
			}
		case *syntax.ArithmExp, *syntax.ArithmCmd, *syntax.LetClause, *syntax.TestClause:
			words(n)
		case *syntax.CallExpr:
			if len(n.Args) > 1 && shells[path.Base(literal(n.Args[0]).text)] {
				for _, w := range n.Args[1:] {
					if t := literal(w); t.ok {
						if inner, a := scriptReads(t.text); a && !strings.HasPrefix(t.text, "-") {
							any = true
						} else {
							for k := range inner {
								names[k] = true
							}
						}
					}
				}
			}
		}
		return true
	})
	return names, any
}

// dateOnly says whether a $(...) runs only date with literal words (Lead $(date +%H:%M): …).
func dateOnly(cs *syntax.CmdSubst) bool {
	if len(cs.Stmts) != 1 || len(cs.Stmts[0].Redirs) > 0 {
		return false
	}
	ce, ok := cs.Stmts[0].Cmd.(*syntax.CallExpr)
	if !ok || len(ce.Assigns) > 0 || len(ce.Args) == 0 {
		return false
	}
	for i, w := range ce.Args {
		if l := literal(w); !l.ok || i == 0 && l.text != "date" && l.text != "/bin/date" {
			return false
		}
	}
	return true
}

// codeNameRe is a variable that runs code or finds commands for a shell or git started later (codeVarRe's, ZDOTDIR, and
// the pager and editor ones git and less start).
var codeNameRe = regexp.MustCompile(`^(PS[0-4]|BASH_ENV|ENV|ZDOTDIR|PATH|HOME|IFS|CDPATH|GLOBIGNORE|SHELLOPTS|BASHOPTS|` +
	`PROMPT_COMMAND|[A-Z_]*PAGER|EDITOR|VISUAL|LESSOPEN|LESSCLOSE|(GIT|BASH|LD|DYLD|XDG)_[A-Za-z0-9_]*)$`)

// aroundCmds are the commands that may sit around a script run: they print or read, and none has an option that
// writes a file or runs a command (awk, sort -o, tee and find do; git is read by safeGit).
var aroundCmds = map[string]bool{":": true, "true": true, "false": true, "echo": true, "printf": true, "test": true,
	"[": true, "date": true, "cat": true, "head": true, "tail": true, "wc": true, "grep": true, "ls": true, "tr": true,
	"cut": true, "basename": true, "dirname": true, "sleep": true, "cd": true}

// aroundCall says whether one command around a script run may pass: a shell running a script (that script is judged
// on its own), git that safeGit passes, or one of aroundCmds, bare or in a system folder; printf takes no option (-v
// sets a variable).
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
	case name == "git":
		return safeGit(words[1:]) // a read, as in a script: no --output, no computed option (review of 73f3942)
	}
	return aroundCmds[name] && (name != "cd" || !strings.Contains(a.text, "/"))
}

// safeWrite says whether a redirect writes nowhere a script run or bus-post is read from: input and copies of a numbered
// descriptor pass; a file must be a path the guard reads, absolute or from the command's own folder, with no .. part,
// no link, and not in the script's folder or bus-post's, judged on the folders the disk will use.
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
		p = c.cwd + "/" + p
	}
	// the disk goes up from a link's target at a .., the text from the link (review of fd71df9: gen/../../.crius/bin);
	// with none, cleaning the text keeps every folder the disk passes
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return false
		}
	}
	p = filepath.Clean(p)
	if devFiles[p] {
		return true
	}
	if fi, err := os.Lstat(p); err == nil {
		// a link, or a file with a second name, may be the script or bus-post (review of 1a40027: other/hl is lead/say.sh)
		if st, ok := fi.Sys().(*syscall.Stat_t); fi.Mode()&os.ModeSymlink != 0 || !ok || st.Nlink > 1 && !fi.IsDir() {
			return false
		}
	}
	// the folders the disk uses: the nearest one that is there with its links followed, then the parts not made yet,
	// which hold no link (review of fd71df9: other/ln -> lead/sub, > other/ln/x is in lead)
	e, rest := p, ""
	for {
		if _, err := os.Lstat(e); err == nil || e == filepath.Dir(e) {
			break
		}
		e, rest = filepath.Dir(e), filepath.Join(filepath.Base(e), rest)
	}
	r, err := filepath.EvalSymlinks(e)
	if err != nil {
		return false
	}
	p = filepath.Join(r, rest)
	// by file identity, not by text: the disk may fold case (review of 3d38316: > dir/LEAD/say.sh is lead/say.sh)
	for d := filepath.Dir(p); ; d = filepath.Dir(d) {
		if fi, err := os.Stat(d); err == nil {
			for _, k := range keep {
				if ki, err := os.Stat(k); err == nil && os.SameFile(fi, ki) {
					return false
				}
			}
		}
		if d == filepath.Dir(d) {
			return true
		}
	}
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
		case len(a.text) > 1 && a.text[0] == '-' && a.text[1] != '-' && strings.Contains(a.text, "O") || runsOption(a.text):
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
// words that name a script a shell runs, and whether it changes folder; and the variables only one for loop over literal
// words sets, with their values inside its body.
func (c ctx) scan(f *syntax.File, src string) ctx {
	c.vars = map[string]setVar{}
	c.loops = map[string]loopVar{}
	c.evals = nil
	if c.movedAt > 0 {
		c.moved = true // the text of a call after the parent's eval or source
	}
	c.cd = nil
	c.movedAt = 0
	moved, cds, loops := c.moved, 0, false
	funcs := false
	r := runs{file: f, src: src, scripts: map[*syntax.Word]bool{}}
	count := map[string]int{}
	fors := map[string][]*syntax.ForClause{}
	lits := map[string]string{} // a literal value each name is set to
	var dyn []*syntax.Word      // command names from a variable
	all := false                // eval or source may set any variable
	syntax.Walk(f, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.Assign:
			if n.Name != nil {
				count[n.Name.Value]++
				if n.Value != nil {
					if l := literal(n.Value); l.ok {
						lits[n.Name.Value] = l.text
					}
				}
			}
		case *syntax.WhileClause:
			loops = true
		case *syntax.FuncDecl:
			funcs = true
			if n.Name.Value == "cd" || n.Name.Value == "exit" {
				cds += 2 // cd, or cd D || exit, is the function then (review r1)
			}
		case *syntax.ForClause:
			loops = true
			if wi, ok := n.Loop.(*syntax.WordIter); ok {
				count[wi.Name.Value] += 2
				fors[wi.Name.Value] = append(fors[wi.Name.Value], n)
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
			if !args[k].ok {
				// path.Base("") is ".": a command name from a variable is no source unless its value is (Crius 11-05: $B, $R)
				dyn = append(dyn, n.Args[k])
				c.evals = append(c.evals, n.End().Offset())
				break
			}
			switch name := path.Base(args[k].text); {
			case name == "cd" || name == "pushd" || name == "popd":
				c.moved = true
				cds++
			case name == "eval" || name == "source" || name == "." || name == "trap" && len(args) > k+1 &&
				args[k+1].text != "-l" && args[k+1].text != "-p":
				all = true
				c.evals = append(c.evals, n.End().Offset())
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
	// a command name from a variable is its literal value; a builtin that changes this shell's state through it counts
	// as the builtin (review r1: X=cd; $X /d and X=declare; $X S=/d were missed), any other value as eval
	for _, w := range dyn {
		pe, ok := w.Parts[0].(*syntax.ParamExp)
		if len(w.Parts) != 1 || !ok || pe.Param == nil || pe.Exp != nil || pe.Index != nil || pe.Slice != nil ||
			pe.Repl != nil || pe.Excl || pe.Length || count[pe.Param.Value] != 1 {
			all = true
			continue
		}
		v, ok := lits[pe.Param.Value]
		switch b := path.Base(v); {
		case !ok:
			all = true
		case b == "cd" || b == "pushd" || b == "popd":
			c.moved = true
			cds++
		case stateBuiltins[b]:
			all = true
		}
	}
	// eval, source, a trap or a command name it cannot read may also change folder (review r1), for what runs after it:
	// with no loop or function, what comes after it in the text
	if all && (funcs || loops) {
		c.moved = true
	} else if all && len(c.evals) > 0 {
		c.movedAt = slices.Min(c.evals)
	}
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
	for name, fs := range fors {
		wi := fs[0].Loop.(*syntax.WordIter)
		// eval or source may set the name too: texts reads it only where none ran since the loop's body began; a function
		// may hold one, so none with both
		if len(fs) != 1 || count[name] != 2 || all && funcs || len(wi.Items) == 0 || len(wi.Items) > maxTexts {
			continue
		}
		lv := loopVar{from: fs[0].DoPos.Offset(), to: fs[0].DonePos.Offset()}
		for _, it := range wi.Items {
			if l := literal(it); l.ok && !strings.ContainsAny(it.Lit()+l.src, "*?[{~") {
				lv.vals = append(lv.vals, l.text)
			}
		}
		if len(lv.vals) == len(wi.Items) {
			c.loops[name] = lv
		}
	}
	if cds == 1 && !moved && !all {
		c.cd = c.followCd(f, src)
	}
	r.vars = c.vars
	c.files = append(slices.Clone(c.files), r)
	return c
}

// followCd gives the command's one cd when it is a step of an && chain that is a statement at its top level or in a
// ( … ) there (cd D, a && cd D && b, or cd D || exit as a whole statement), to one word that names a folder that is
// there by its full path (a relative one may go by CDPATH). The steps after it in its chain, and everything after
// cd D || exit up to the end of its ( … ), run only when it went to D.
func (c ctx) followCd(f *syntax.File, src string) *cdTo {
	isCd := func(st *syntax.Stmt) (*syntax.CallExpr, bool) {
		ce, ok := st.Cmd.(*syntax.CallExpr)
		return ce, ok && len(ce.Assigns) == 0 && len(ce.Args) == 2 && literal(ce.Args[0]).src == "cd"
	}
	type list struct {
		stmts []*syntax.Stmt
		to    uint
	}
	lists := []list{{f.Stmts, uint(len(src))}}
	for _, st := range f.Stmts {
		for _, step := range andChain(st) {
			if sub, ok := step.Cmd.(*syntax.Subshell); ok {
				lists = append(lists, list{sub.Stmts, sub.End().Offset()})
			}
		}
	}
	for _, l := range lists {
		for _, st := range l.stmts {
			chain := andChain(st)
			for k, step := range chain {
				ce, ok := isCd(step)
				sure := chain[len(chain)-1].End().Offset()
				if b, isOr := step.Cmd.(*syntax.BinaryCmd); !ok && isOr && b.Op == syntax.OrStmt && len(chain) == 1 && !st.Negated {
					if ce, ok = isCd(b.X); ok {
						ex, isCall := b.Y.Cmd.(*syntax.CallExpr)
						// return at the top level is an error bash goes on after (review r1)
						if !isCall || len(ex.Args) == 0 || ex.Args[0].Lit() != "exit" {
							return nil
						}
						sure = l.to
					}
				} else if ok && k == len(chain)-1 {
					sure = 0
				}
				if !ok {
					continue
				}
				dir, known := c.resolve(ce.Args[1])
				if fi, err := os.Stat(dir); !known || !filepath.IsAbs(dir) || err != nil || !fi.IsDir() {
					return nil
				}
				return &cdTo{dir: dir, at: ce.End().Offset(), sure: sure, to: l.to}
			}
		}
	}
	return nil
}

// stateBuiltins change this shell's variables, options or the code it runs, beside cd.
var stateBuiltins = map[string]bool{"eval": true, "source": true, ".": true, "builtin": true, "command": true,
	"exec": true, "declare": true, "typeset": true, "local": true, "export": true, "readonly": true, "read": true,
	"readarray": true, "mapfile": true, "printf": true, "unset": true, "getopts": true, "let": true, "set": true,
	"shopt": true, "trap": true, "alias": true, "enable": true, "hash": true}

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
		if !ok && pe.Param.Value == "HOME" {
			// "$HOME/.crius/bin/bus-post" is how leads call bus-post (review r3 of the ctx tools check)
			home, err := os.UserHomeDir()
			b.WriteString(home)
			return err == nil
		}
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

// azOpts is an az command line: its command words and the values of each option, by option name; whether a command word
// is one the guard cannot read (az $X, az repos pr "$1"), and whether a word it cannot read may be an option or more than
// one word (az repos pr update $ARGS, "$@", "${A[@]}", a value-less spot).
type azOpts struct {
	cmd       string
	opts      map[string][]word
	unreadCmd bool
	unreadOpt bool
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
		if !a.ok && !strings.HasPrefix(a.text, "-") {
			o.unreadCmd = true
		}
		if !a.ok || strings.HasPrefix(a.text, "-") {
			break
		}
		words = append(words, strings.ToLower(a.text))
	}
	o.cmd = strings.Join(words, " ")
	var cur []string
	wants := false // the word before is an option name with no value attached
	for ; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a.text, "-") || len(a.text) < 2 || (!a.ok && !strings.Contains(a.text, "=") && len(a.text) > 2) {
			// one word that starts with a literal such as commitId= is never an option, whatever it expands to
			if !a.ok && (!oneWord(a.node) || !wants && !fixedStart(a.node)) {
				o.unreadOpt = true
			}
			for _, k := range cur {
				o.opts[k] = append(o.opts[k], azValue(a))
			}
			wants = false
			continue
		}
		wants = a.ok && azValueOpts[strings.ToLower(a.text)]
		if !a.ok && !oneWord(a.node) {
			o.unreadOpt = true // review r1: --title=$1 and -t$1 may split into more options
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

// azValueOpts are the options of az's text writes that always take one value, so a quoted word the guard cannot read after
// one is that value; after any other option it may be an option itself: a flag, or one whose value is optional (--draft,
// --auto-complete, --squash, --delete-source-branch, --transition-work-items: az reads --description=x there as an
// option, review r1), or one that takes any number (--reviewers, --work-items, --labels, --route-parameters,
// --query-parameters, --headers, review r2).
var azValueOpts = map[string]bool{}

func init() {
	for _, o := range strings.Fields(`--id --title -t --org --organization --project -p --repository -r --source-branch -s
		--target-branch --status --area --resource --api-version --in-file --http-method --url --uri --method -m
		--resource-group -g --subscription --query -o --output --type --state --assigned-to --iteration --path --wiki
		--version --merge-commit-message --bypass-policy-reason --reason --parent`) {
		azValueOpts[o] = true
	}
}

// readVerb says whether an az command word ends a command that only reads (show, list, query, get, get-access-token,
// show-tags, list-*): no az group has such a name, so words after it cannot make a write.
func readVerb(v string) bool {
	switch v {
	case "show", "list", "query", "get", "get-access-token", "wait":
		return true
	}
	return strings.HasPrefix(v, "show-") || strings.HasPrefix(v, "list-") || strings.HasPrefix(v, "get-")
}

// oneWord says whether a word bash may expand stays one word that cannot start an option list of its own: every
// expansion in double quotes and none of "$@", "$*" or "${a[@]}". A word with no parse (xargs's) is not one.
func oneWord(w *syntax.Word) bool {
	if w == nil {
		return false
	}
	for _, p := range w.Parts {
		switch p := p.(type) {
		case *syntax.Lit, *syntax.SglQuoted:
		case *syntax.DblQuoted:
			for _, q := range p.Parts {
				if pe, ok := q.(*syntax.ParamExp); ok && pe.Param != nil &&
					(pe.Param.Value == "@" || pe.Param.Value == "*" || pe.Index != nil || pe.Names != 0) {
					return false
				}
			}
		default:
			return false
		}
	}
	return true
}

// fixedStart says whether a word starts with literal text that is not an option's dash (commitId="$3").
func fixedStart(w *syntax.Word) bool {
	if w == nil || len(w.Parts) == 0 {
		return false
	}
	var first string
	switch p := w.Parts[0].(type) {
	case *syntax.Lit:
		first = p.Value
	case *syntax.SglQuoted:
		first = p.Value
	case *syntax.DblQuoted:
		if len(p.Parts) > 0 {
			if l, ok := p.Parts[0].(*syntax.Lit); ok {
				first = l.Value
			}
		}
	}
	return first != "" && first[0] != '-'
}

// azArgs puts in the value of each word whose only expansions are variables the command set to a literal, split into
// words as bash splits an unquoted one (O="--org https://dev.azure.com/x"; az … $O).
func (c ctx) azArgs(args []word) []word {
	var out []word
	for _, a := range args {
		if a.ok || a.node == nil {
			out = append(out, a)
			continue
		}
		s, ok := c.resolve(a.node)
		switch {
		case !ok:
			out = append(out, a)
		case oneWord(a.node):
			out = append(out, word{text: s, src: s, ok: true, node: a.node})
		default:
			for _, f := range strings.Fields(s) {
				out = append(out, word{text: f, src: f, ok: true})
			}
		}
	}
	return out
}

func checkAz(args []word, c ctx) string {
	o := parseAz(c.azArgs(args))
	words := strings.Fields(o.cmd)
	verb := ""
	if len(words) > 0 {
		verb = words[len(words)-1]
	}
	if readVerb(verb) {
		return ""
	}
	switch o.cmd {
	case "devops invoke", "rest", "boards work-item update", "boards work-item create", "repos pr create", "repos pr update",
		"devops wiki page create", "devops wiki page update":
		// a whole command: words after it are its options ("${A[@]}" --description …)
		o.unreadOpt = o.unreadOpt || o.unreadCmd
	default:
		if o.unreadCmd {
			// 11-05: az $CMD, az repos pr "$v", az "$@" may be any command, a text write too
			return asked("az command words the guard cannot read")
		}
	}
	// a text write with a word that may be an option the guard cannot read: --description, --discussion, --http-method
	unread := func(what string) string {
		if o.unreadOpt {
			return asked(what + " with words the guard cannot read")
		}
		return ""
	}
	switch o.cmd {
	case "devops invoke":
		if why := checkInvoke(o, c); why != "" {
			return why
		}
		return unread("az devops invoke")
	case "rest":
		if why := unread("az rest"); why != "" {
			return why
		}
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
		if why := unread("az " + o.cmd); why != "" {
			return why
		}
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
		return unread("az " + o.cmd)
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
