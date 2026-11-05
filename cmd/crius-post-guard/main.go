// Command crius-post-guard is a Claude Code PreToolUse hook for Bash and context-mode's ctx_execute, ctx_execute_file and
// ctx_batch_execute, in both personas and their subagents. It makes
// every Azure DevOps write wait for the owner's click: it answers "ask", which Claude Code keeps even in bypass mode (a
// headless probe on 10-30 refused the call). 10-30: a lead session posted a QA-notes comment and a reply to a colleague
// on AB#94205 under the owner's name, because its brief said to; the owner had both deleted. No brief can grant this now.
//
// It keys on the property (an az or REST write to Azure DevOps), not on one spelling. Owner 11-05, a post is:
//   - az repos pr create or update (any option), and every other az repos pr command that is not a read (set-vote,
//     reviewer add, work-item add); az repos pr checkout and az repos pr policy queue are not posts;
//   - az boards work-item create or update, and every other az boards command that is not a read;
//   - az pipelines commands that are not a read (runs, build queue, runs tag add, ...);
//   - az devops invoke with a write method (--http-method POST, PUT, PATCH or DELETE, any case, = form) or an
//     --in-file, except a POST to a query resource (wit wiql, wit workitemsbatch, git commitsbatch, git itemsbatch);
//   - az devops wiki page create or update; any az command whose words name a comment, thread or discussion and end in a
//     write verb (create, add, update, reply, ...);
//   - a REST call to Azure DevOps (dev.azure.com, *.visualstudio.com, an /_apis/ path, or a URL from a variable in a
//     command that names an ADO host) with a write method through az rest, curl, wget, httpie or xh. A POST to the
//     read endpoints (wit/wiql, wit/workitemsbatch, search/) is a read.
//
// It looks through env prefixes (GH_TOKEN=... az), wrappers (env, env -S, timeout, sudo, xargs, find -exec, watch,
// op run, ...), bash -c, eval, a shell reading its script from stdin, ; && | chains, $(...), line continuations, quotes
// and $'...'. A script or code file the command runs (bash x.sh, source, ./x.sh, bun x.ts, node, python3) is read and
// checked like a command, down 4 levels, with a heredoc the same command writes into it; see scripts.go. It asks only
// when what the file runs posts, and names the file and line. A file it cannot read (missing, unreadable, binary for a
// shell, a path from text it cannot work out or a cd it does not follow) asks and says why when the command or a
// script around it names az or Azure DevOps, as an unread script did before (a missing ./build/app in other work is no
// post). A script the command or the script itself may change before it runs, or that gets code-running variables (PS4,
// BASH_ENV, PATH), asks on the same condition. A relative script name follows one cd (a relative one from the start
// folder when CDPATH is unset); two cds, pushd, or a cd in bash -c or eval text are not followed. A wrapper (exec,
// timeout) with a word it cannot read before its command asks like a command name.
// In az text writes (devops invoke, rest, work-item create/update, pr create/update, wiki page create/update), a
// word the guard cannot read asks when it may split into several words or does not follow an option that takes a
// value; variables set to a literal are put in first, and a for loop's literal words are read in bash -c and eval text.
// az takes the last copy of an option and reads a value from @file; the guard does the same. When it
// cannot tell (a method, resource or fields value from a variable or @file, eval or bash -c text it cannot read, a
// command the bash parser cannot read that names az or an ADO host) it asks. Unreadable hook input also asks: a guard
// on an outward-facing write never lets one through silently.
//
// Known gaps: git push to an Azure DevOps remote passes (not yet a post here); a command name from a variable or a
// function ($AZ, f() { az "$@"; }) when the command names neither az nor Azure DevOps; a command that makes bash read
// quoted code as a number or a name ((( 'a[$(az …)]' )), printf -v $'a[\x24(az …)]' x, (( $(printf …) ))); a script
// that hides az (a'z', a PS4 prompt) when neither it nor the command names az, as for a command itself (and
// PS4='$(az ...)' bash -xc : with no script); a script past the 16th one a call reads or 4 levels down; a command that
// rewrites a script without naming it (tar, rsync, git checkout, a glob), and python, node or other code (-c text or a
// file) that writes a script, which is not read for writes; a relative redirect after a cd through a symlink of
// another name; a compiled program run by its path (it is not read); a script copied, moved or linked into place in the
// same command (cp a.sh run.sh && bash run.sh: the source is not read); a script run from a glob, a loop's file, find
// -exec, stdin or a pipe (bash < x.sh, cat x.sh | bash, bash <(cat x.sh)); a cd inside a script before a relative run;
// a heredoc appended (cat >> x.sh) is read apart from the file it extends; python -m azure.cli; tsx and npx; ssh or
// kubectl exec to another machine; code files are read by line for az calls and write fetches, not run: an import is
// not followed, and an az call built from parts in other ways passes; a curl config file (-K); an in-file written by a
// script; Azure DevOps MCP tools (they are not Bash calls); in code strings az with a global option before its group
// (az -o none repos pr create), interpreter flags the guard does not know (python3 -uc, perl -E, deno eval), bun --cwd,
// uv run, and a code file with no extension; a script that writes a later script of the command without naming its
// file (dl.sh art; bash art/run.sh, where dl.sh downloads into "$1"), or -c inline code flags written attached
// (python3 -c'…', node --eval='…').
// ctx tools: code of another language that runs the file it is handed by a spelling runnerRe lacks (require(path),
// runpy) or runs a shell command that reads a script (execSync('bash post.sh')); the plugin's project folder is taken
// to be the session's folder.
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
	"unsafe"

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
	deadline = time.Now().Add(20 * time.Second)
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
	vars       map[string]setVar       // variables the command sets to a literal value at its top level, before a script runs
	loops      map[string]loopVar      // variables only a for loop over literal words sets
	evals      []uint                  // where each call that may set any variable (eval, source, $X) ends
	files      []runs                  // this command's parse and those of the commands around it (bash -c text, a script's caller)
	moved      bool                    // the command changes folder, so a relative script name is not known
	cd         *cdTo                   // the one cd of this command's text the guard follows for a script's name
	movedAt    uint                    // >0: an eval, source, trap or $X ending here may change folder for what follows
	depth      int                     // scripts read to get here (a script that runs itself stops at 4)
	reads      *int                    // scripts read for this hook call, at most 16 (15 lines of bash self.sh took 19 s)
	read       *[]readText             // the scripts read so far for this hook call, and their text
	script     string                  // the script whose text this is ($0), or ""
	own        map[string]*syntax.Word // in a script's text: the variables it sets once, and to what
	code       bool                    // an az line found in code of another language: its own text, not a command's
	alt        string                  // the other place the same script word may name (after a cd that may fail)
}

// cdTo is a cd that runs before what follows it in the command's own text, up to to (the end of the text, or of the
// ( … ) it is in): a script named in there runs in dir, and up to sure only when the cd went there (… && cd D && …,
// cd D || exit; …), elsewhere maybe still in the start folder. From to on, the start folder is the folder again. With
// before set (the command defines no function, which could run an earlier word after the cd), a word that ends by from
// (where the cd starts) runs in the start folder.
type cdTo struct {
	dir                string
	from, at, sure, to uint
	before             bool
}

// setVar is a variable's literal value and where its assignment ends; lost: also set elsewhere, computed, read, a loop's
// or under eval or source, so its value when a script runs is not known.
type setVar struct {
	text string
	end  uint
	lost bool
	in   *syntax.Stmt // set after a command in a && chain: known only inside that chain (cd /tmp && S=x && bash $S/y)
	// until: in a text with eval or source, the value holds only up to the first of them (review r5: HERE=$(cd …
	// && pwd); source "$HERE/lib/ado.sh" lost HERE); 0: no such limit
	until uint
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
	script  string                // the script this is the text of, or "" for the command the agent wrote
	cd      *cdTo                 // the one cd this text makes, when the guard follows it
	cwd     string                // the folder this text starts in
	moved   bool                  // it may change folder
	movedAt uint                  // >0: from here on it may have changed folder (eval, source, $X)
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
		return checkBash(t.Command, ctx{cwd: in.Cwd, whole: t.Command, reads: new(int), read: new([]readText)})
	case strings.HasSuffix(name, "__ctx_batch_execute"):
		for _, command := range t.Commands {
			if why := checkBash(command, ctx{cwd: dir, whole: command, reads: new(int), read: new([]readText)}); why != "" {
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

// azWordRe finds az as a word in code of another language: 'az' in a list, or a string that starts the CLI. az before a
// "/" is a folder or a language code (az/en/ru, /az/language), never the CLI (today's replay).
var azWordRe = regexp.MustCompile(`(?i)(^|[^\w.-])az([^\w./-]|$)`)

// azCallRe finds az as the CLI is written in a command: lower case, then a word (az login, az repos).
var azCallRe = regexp.MustCompile(`(^|[^\w.-])az[ \t]+[a-z]`)

// azGroupRe finds an az command group that can write to Azure DevOps. Every such az call names one, so a bare az word
// ('az' in quotes, az) and none of them in the command is a word like the language code 'az' (today's replay: staffy's
// copy scripts: loadMessages('az'), {'az': ...}), not the CLI.
// rest counts only with an option after it (az rest --method, 'rest', '--url'): rest=$caller_path in node_modules/.bin shims
// is a variable.
var azGroupRe = regexp.MustCompile(`(?i)\b(repos|boards|pipelines|devops|artifacts)\b|\brest\b["']?[\s,]+["']?-`)

// azListRe finds az as the first word of a call in code: 'az', 'account' / ["az", "rest"] (review r4: an ADO token from
// az account get-access-token in python, execFileSync('az', ['rest'] …)).
var azListRe = regexp.MustCompile(`(?i)(^|[^\w.-])az['"]?[\s,\[(]+['"](account|repos|boards|pipelines|devops|artifacts|rest|login)\b`)

// azLookRe finds a look-up of the az program (which az, command -v az): a script that checks for az runs it (review r4).
var azLookRe = regexp.MustCompile(`(?i)\b(which|whence|where|type|hash|command\s+-[vV])\s+az\b`)

// namesAzCLI: the text names the az CLI: az then a word, az first in a call's word list, a look-up of az, or an az word
// next to a command group that can write to ADO.
func namesAzCLI(s string) bool {
	return azCallRe.MatchString(s) || azListRe.MatchString(s) || azLookRe.MatchString(s) ||
		azWordRe.MatchString(s) && azGroupRe.MatchString(s)
}

// runnerRe finds code of another language that starts a command or runs text.
var runnerRe = regexp.MustCompile(`\b(exec\w*|spawn\w*|system|popen|Popen|subprocess|child_process|eval|shell_exec|passthru|Deno\.Command)\b`)

// unreadFile stands for a file ctx_execute_file names that the guard cannot read: it may hold an az command.
const unreadFile = "az call (a file the guard cannot read)"

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
		return checkBash(code, ctx{cwd: cwd, whole: code + "\n" + text, reads: new(int), read: new([]readText)})
	}
	switch {
	case namesAzCLI(code) || adoMarkRe.MatchString(code):
		// review r3: a write needs no method word (urllib data=, the Azure DevOps SDKs)
		return asked(lang + " code that runs az or calls Azure DevOps")
	case hasFile && runnerRe.MatchString(code) && (namesAzCLI(text) || adoMarkRe.MatchString(text)):
		return asked(lang + " code that starts a command and a file that names az or Azure DevOps")
	}
	// review r5: az run with its words from a file or a variable names no command group (spawnSync('az', argv)); the
	// check a code file run by Bash gets judges it
	why, _ := codePosts(lang, code)
	return why
}

// adoMarkRe finds a sign of Azure DevOps in a command: a host, an API path, or a variable that only an ADO call uses.
// 499b84ac-… is Azure DevOps' resource id: a token for it is a token for ADO (review r4).
var adoMarkRe = regexp.MustCompile(`(?i)dev\.azure\.com|\.visualstudio\.com|/_apis/|AZURE_DEVOPS|SYSTEM_ACCESSTOKEN|SYSTEM_COLLECTIONURI|499b84ac-1321-427f-aa17-267ca6975798`)

// adoRe finds an az call or a sign of Azure DevOps in text the guard cannot read.
var adoRe = regexp.MustCompile(`(?i)(^|[^\w.-])az(\s|$)|` + adoMarkRe.String()[4:])

func asked(what string) string { return reason + " (" + what + ")" }

// namesAzOrADO says whether a text names the az CLI or Azure DevOps. The answer is kept per text: each word the guard
// cannot read asks it of the same whole command (review r5: 2000 evals took 11 s, the regexes ran 2000 times over it).
// The key holds the text's bytes, so they stay alive and no other text gets their address.
func namesAzOrADO(s string) bool {
	k := textKey{unsafe.StringData(s), len(s)}
	v, seen := azTexts[k]
	if !seen {
		v = namesAzCLI(s) || adoMarkRe.MatchString(s)
		azTexts[k] = v
	}
	return v
}

type textKey struct {
	p *byte
	n int
}

var azTexts = map[textKey]bool{}

// unsure asks when text the guard cannot read sits in a command that names az or Azure DevOps.
func unsure(c ctx, what string) string {
	// review r4: az next to ), `, ] or " counts too ($(which az), ${commands[az]})
	if namesAzOrADO(c.whole) {
		return asked(what + " the guard cannot read")
	}
	return ""
}

// unsureName asks for a command name the guard cannot read when the text names az, or when a word of the shell's own is
// az or a path to it (review r5: x=(az); "${x[@]}" $(cat a), for c in az; do $c …, p=$(brew --prefix)/bin/az; "$p" …).
// az inside a longer quoted word (loadMessages('az')) is no such word.
func unsureName(c ctx) string {
	if why := unsure(c, "a command name"); why != "" {
		return why
	}
	if azShellWord(c.files) {
		return asked("a command name the guard cannot read, and az as a word of the command")
	}
	return ""
}

// azShellWord says whether a word in these parses is az, or ends in a path to az, in any case. The answer is kept per
// parse list: each command name the guard cannot read asks it again (review r5b: 1000 lines took 9 s).
func azShellWord(fs []runs) bool {
	if len(fs) == 0 {
		return false
	}
	k := shellKey{&fs[0], len(fs)}
	if v, seen := azWords[k]; seen {
		return v
	}
	v := azShellWordIn(fs)
	azWords[k] = v
	return v
}

type shellKey struct {
	first *runs
	n     int
}

var azWords = map[shellKey]bool{}

func azShellWordIn(fs []runs) bool {
	found := false
	for _, r := range fs {
		if r.file == nil {
			continue
		}
		syntax.Walk(r.file, func(n syntax.Node) bool {
			if w, ok := n.(*syntax.Word); ok && !found {
				if l := literal(w); l.ok {
					found = strings.EqualFold(path.Base(l.text), "az")
				} else if t := tailLit(w); strings.Contains(t, "/") {
					found = strings.EqualFold(path.Base(t), "az")
				}
			}
			return !found
		})
	}
	return found
}

// tailLit gives the literal text at the end of a word ($(brew --prefix)/bin/az: /bin/az), or "".
func tailLit(w *syntax.Word) string {
	if len(w.Parts) == 0 {
		return ""
	}
	switch p := w.Parts[len(w.Parts)-1].(type) {
	case *syntax.Lit:
		return p.Value
	case *syntax.SglQuoted:
		return p.Value
	case *syntax.DblQuoted:
		if len(p.Parts) > 0 {
			if l, ok := p.Parts[len(p.Parts)-1].(*syntax.Lit); ok {
				return l.Value
			}
		}
	}
	return ""
}

func checkBash(command string, c ctx) string {
	why, _ := checkBashAt(command, c)
	return why
}

// checkBashAt is checkBash that also gives the line of the command's text where it found what asks.
func checkBashAt(command string, c ctx) (string, uint) {
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil {
		// c.whole holds the file ctx_execute_file hands the code (review r3: zsh code that reads it does not parse)
		if adoRe.MatchString(command + "\n" + c.whole) {
			return asked("the guard cannot parse this command, so it cannot tell whether it writes"), 1
		}
		return "", 0
	}
	c = c.scan(f, command)
	piped := map[*syntax.CallExpr]bool{}
	why, line := "", uint(0)
	soft, softLine := "", uint(0) // the first ask for a step the guard cannot read
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
			if late() {
				// review r5: one long command (5000 evals) held the guard past the hook's limit
				why, line = asked("the guard ran out of time reading the command"), n.Pos().Line()
				return false
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
			if w := checkCall(args, stdin, c); unsureWhy(w) && soft == "" {
				soft, softLine = w, n.Pos().Line()
			} else if w != "" && !unsureWhy(w) {
				why, line = w, n.Pos().Line()
			}
		}
		return true
	})
	if why == "" {
		why, line = soft, softLine
	}
	return why, line
}

// unsureWhy says whether an ask is for something the guard cannot read rather than a post it found. checkBashAt reads
// on past such a step, so the ask names the line that posts when there is one (11-05 replay: merge.sh asked on tsc after
// a cd in merge-ok.sh, not on its az repos pr update --status completed).
func unsureWhy(w string) bool {
	return strings.Contains(w, "the guard cannot")
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

// spreads says whether a word the guard cannot read may become several words, any of which may be an option ("$@",
// $*, ${a[@]}, or a $X or $(...) outside quotes): curl "$@" in a script gets its method from the caller (review r3).
func spreads(a word) bool {
	if a.ok || a.node == nil {
		return false
	}
	for _, p := range a.node.Parts {
		switch p := p.(type) {
		case *syntax.ParamExp, *syntax.CmdSubst, *syntax.ArithmExp, *syntax.ProcSubst:
			return true
		case *syntax.DblQuoted:
			for _, q := range p.Parts {
				pe, ok := q.(*syntax.ParamExp)
				if !ok {
					continue
				}
				if pe.Param != nil && (pe.Param.Value == "@" || pe.Param.Value == "*") {
					return true
				}
				if pe.Index != nil {
					var b bytes.Buffer
					syntax.NewPrinter().Print(&b, pe.Index)
					if i := strings.TrimSpace(b.String()); i == "@" || i == "*" {
						return true
					}
				}
			}
		}
	}
	return false
}

// leadText gives the literal text a word starts with, up to its first expansion ("http.extraHeader=Bearer $tok" gives
// http.extraHeader=Bearer ), so a git -c key in quotes is still read (today's replay, 11-05).
func leadText(a word) string {
	if a.node == nil {
		return ""
	}
	var b strings.Builder
	for _, p := range a.node.Parts {
		switch p := p.(type) {
		case *syntax.Lit:
			b.WriteString(unescape(p.Value, ""))
		case *syntax.SglQuoted:
			if p.Dollar {
				return b.String()
			}
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			for _, q := range p.Parts {
				l, ok := q.(*syntax.Lit)
				if !ok {
					return b.String()
				}
				b.WriteString(unescape(l.Value, "\"\\$`"))
			}
		default:
			return b.String()
		}
	}
	return b.String()
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

// plainWrappers take no option with a value, so their first word that is no option is the command they run and the
// rest are its words (today's replay: command -p cygpath -w "$basedir" in node_modules/.bin shims).
var plainWrappers = map[string]bool{"command": true, "builtin": true, "nohup": true}

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
			// a command name from text the guard cannot read ($FILE_CONTENT, X=$(cat f); $X), review r2 of the ctx tools check;
			// in a script, one the script makes from its own text alone asks only when that text names az
			if args[0].node != nil && c.inner(args[0].node, 0) {
				return unsureName(ctx{whole: c.files[len(c.files)-1].src, files: c.files[len(c.files)-1:]})
			}
			return unsureName(c)
		}
	}
	// the Mac's disk ignores case: AZ, CURL and BASH run az, curl and bash (review r5)
	name := strings.ToLower(path.Base(args[0].text))
	switch {
	case (name == "source" || name == ".") && len(args) > 1:
		if args[1].text == "--" && len(args) > 2 {
			return checkScript(args[2], c, bySource, true)
		}
		return checkScript(args[1], c, bySource, true)
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
				// review of the 11-05 change: node -e "execSync('az repos pr create …')" ran az unread
				texts := []string{code.text}
				if !code.ok {
					// code the guard cannot read ("$1" in a script) is the caller's: ask as for any word it cannot read
					var ok bool
					if texts, ok = c.texts(code.node); !ok {
						return unsure(c, name+" inline code")
					}
				}
				for _, text := range texts {
					if why, _ := codePosts(name, text); why != "" {
						return why
					}
				}
				return ""
			}
		}
		if k := codeFile(name, args); k > 0 {
			return checkScript(args[k], c, byCode, true)
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
		// in a script, c.whole is the whole script: its code is judged by codePosts below, not by any word in it
		if stdin && c.script == "" && adoMarkRe.MatchString(c.whole) && writeWordRe.MatchString(c.whole) {
			return asked(name + " code on stdin that may write to Azure DevOps")
		}
		if stdin && !c.code {
			// the code is in the command's text (python3 - <<'EOF' … subprocess.run(["az", "repos", "pr", "create"])); in an
			// az line of code that text is the line itself, which would be read again with no end (… | python3 -)
			why, _ := codePosts(name, c.whole)
			return why
		}
		return ""
	case name == "command" && looksUp(args[1:]):
		return ""
	case wrappers[name]:
		runsNext := name != "find" // find runs a command only after -exec, -execdir, -ok or -okdir
		first := runsNext          // the next word that is no option, setting or number is the command it runs
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
				if first && a.node != nil && c.inner(a.node, 0) {
					return unsureName(ctx{whole: c.files[len(c.files)-1].src, files: c.files[len(c.files)-1:]}) // exec "$bin" "$@"
				}
				return unsureName(c)
			}
			if name == "find" && (a.text == "-exec" || a.text == "-execdir" || a.text == "-ok" || a.text == "-okdir") {
				runsNext, first = true, true
				continue
			}
			if first && !strings.HasPrefix(a.text, "-") && !envSetRe.MatchString(a.text) && strings.Trim(a.text, "0123456789.smhd") != "" {
				first = false
				// a program by its path, or a command that runs nothing (command printf %s "$*" in nvm.sh, today's replay):
				// the rest is its own words, judged as its call (exec "$HOME/.crius/bin/bus-post" …, timeout 60 ./x.sh)
				b := a.text
				if sysDirs[path.Dir(b)] {
					b = path.Base(b) // find -exec /usr/bin/grep … "${head:0:7}" {} + (review-check.sh, 11-05 23:02)
				}
				if strings.Contains(a.text, "/") && !sysDirs[path.Dir(a.text)] || (noFileCmds[b] || readCmds[b]) && !feeders[name] ||
					plainWrappers[name] {
					return checkCall(args[i:], stdin, c)
				}
			}
			if n := strings.ToLower(path.Base(a.text)); n == "az" || n == "eval" || (n == "source" || n == ".") && (name == "builtin" || name == "command") || shells[n] || httpTools[n] || interpreters[n] {
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
	case name == "alias":
		// an alias body runs where its name is used later (review r5: alias x="az repos pr create …"; x)
		for _, a := range args[1:] {
			if !a.ok {
				return unsure(c, "alias text")
			}
			if _, body, set := strings.Cut(a.text, "="); set && !strings.HasPrefix(a.text, "-") {
				if why := checkBash(body, c); why != "" {
					return why
				}
				if f := strings.Fields(body); len(f) > 0 && strings.EqualFold(path.Base(f[0]), "az") {
					// review r5b: alias x=az; x repos pr create: the words come where the alias is used
					return asked("an alias for az: the guard does not read the words it gets where it is used")
				}
			}
		}
	case name == "curl":
		return checkCurl(args[1:], c)
	case name == "wget":
		return checkWget(args[1:], c)
	case httpTools[name]:
		return checkHTTPie(args[1:], stdin, c)
	case name == "trap" && len(args) > 2 && !strings.HasPrefix(args[1].text, "-") || name == "trap" && len(args) > 3 && args[1].text == "--":
		// the text a trap runs later
		k := 1
		if args[1].text == "--" {
			k = 2
		}
		return checkText(args[k], c, "trap text")
	case name == "awk" || name == "gawk" || name == "mawk" || name == "nawk":
		if !safeAwk(args[1:]) {
			return unsure(c, "awk code that may run a command")
		}
	case name == "git":
		if gitRunsCode(args[1:]) {
			return unsure(c, "a git call that may run a program")
		}
	case strings.Contains(args[0].text, "/") && !sysDirs[path.Dir(args[0].text)]:
		// a script or program run by its path (./x.sh, $S/x.sh)
		return checkScript(args[0], c, byPath, true)
	}
	return ""
}

// checkText checks a word that bash runs as a command line (bash -c, trap): each text it may hold, or it is unsure.
func checkText(a word, c ctx, what string) string {
	if a.ok {
		return checkBash(a.text, c)
	}
	texts, ok := c.texts(a.node)
	if !ok {
		return unsure(c, what)
	}
	for _, t := range texts {
		if why := checkBash(t, c); why != "" {
			return why
		}
	}
	return ""
}

// gitSafeKeys are the git -c settings that run no program: an HTTP header (the az token), who commits, colours.
var gitSafeKeys = regexp.MustCompile("(?i)^(http\\.[a-z.]+|user\\.(name|email)|color\\.[a-z.]+|advice\\.[a-z.]+|init\\.defaultbranch|safe\\.directory|core\\.(quotepath|autocrlf|longpaths)|protocol\\.version|gc\\.auto|maintenance\\.auto|push\\.default|pull\\.rebase|fetch\\.prune)=")

// gitPagerRe is a pager set to a plain viewer (git -c core.pager=cat log): it runs no program the command hands it.
var gitPagerRe = regexp.MustCompile(`(?i)^(core\.pager|pager\.[a-z-]+)=(cat|less|more)$`)

// gitRunsCode says whether a git call may run a program it is handed: a -c setting not on gitSafeKeys (alias.x=!cmd,
// core.pager), --config-env or --exec-path, a computed subcommand, a git grep option that opens files in a program (-O,
// --open-files-in-pager, or a computed word before --), or diff --ext-diff.
func gitRunsCode(args []word) bool {
	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		if lead := leadText(a); strings.HasPrefix(lead, "--work-tree=") || strings.HasPrefix(lead, "--git-dir=") ||
			strings.HasPrefix(lead, "--namespace=") {
			continue // a folder from a computed value runs no program (git --work-tree=$NS add, today's replay)
		}
		if !a.ok {
			return true
		}
		if !strings.HasPrefix(a.text, "-") {
			break
		}
		switch {
		case a.text == "-c":
			// a value with a computed part passes when its key is safe (http.extraHeader="Authorization: Bearer $tok")
			if i++; i >= len(args) || args[i].ok && !gitSafeKeys.MatchString(args[i].text) && !gitPagerRe.MatchString(args[i].text) &&
				!strings.EqualFold(args[i].text, "credential.helper=") || !args[i].ok && !gitSafeKeys.MatchString(leadText(args[i])) {
				return true
			}
		case strings.HasPrefix(a.text, "-c") || strings.HasPrefix(a.text, "--config-env") || strings.HasPrefix(a.text, "--exec-path"):
			return true
		case a.text == "-C" || a.text == "--git-dir" || a.text == "--work-tree" || a.text == "--namespace":
			i++
		}
	}
	if i >= len(args) {
		return false
	}
	sub := args[i].text
	ends := false
	for j, a := range args[i+1:] {
		switch {
		case !a.ok:
			if sub == "grep" && !ends && (j == 0 || !args[i+j].ok || args[i+j].text != "-e") {
				return true
			}
		case ends:
		case a.text == "--":
			ends = true
		case sub == "grep" && len(a.text) > 1 && a.text[0] == '-' && a.text[1] != '-' && strings.Contains(a.text, "O"):
			return true
		case runsOption(a.text) && !strings.HasPrefix("--output", strings.SplitN(a.text, "=", 2)[0]):
			return true
		}
	}
	return false
}

// checkShell reads bash [options] [-c text | -s | file] [args]: -c runs the first word after the options, -s or no file
// reads the script from stdin; -o and -O take a value; - and -- end the options.
func checkShell(args []word, stdin bool, c ctx) string {
	i, sawC, sawS := shellOpts(args)
	switch {
	case rcFile(args[1:min(i, len(args))]) != nil:
		// bash runs the file before what it was given
		if why := checkScript(*rcFile(args[1:min(i, len(args))]), c, byShell, true); why != "" {
			return why
		}
		return checkShell(append([]word{args[0]}, withoutRc(args[1:])...), stdin, c)
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
		return checkScript(args[i], c, byShell, plain)
	case stdin:
		return unsure(c, "a script on the shell's stdin")
	}
	return ""
}

// rcFile gives the file a shell's --rcfile or --init-file names, or nil.
func rcFile(opts []word) *word {
	for j := 0; j+1 < len(opts); j++ {
		if opts[j].text == "--rcfile" || opts[j].text == "--init-file" {
			return &opts[j+1]
		}
	}
	return nil
}

// withoutRc drops --rcfile and --init-file and their values from a shell's words.
func withoutRc(args []word) []word {
	var out []word
	for j := 0; j < len(args); j++ {
		if args[j].text == "--rcfile" || args[j].text == "--init-file" {
			j++
			continue
		}
		out = append(out, args[j])
	}
	return out
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

// shownNoWrite says whether a find, sed or sort call is shown to write no file: every word readable, and no option or
// program part that writes or runs (find -delete, -fprint, -fls, -exec, -ok; sed -i, -f, a long option, or a w, W or e
// in its program; sort -o).
func shownNoWrite(name string, args []word) bool {
	if name != "find" && name != "sed" && name != "sort" {
		return false
	}
	// sed's program is the word after -e, else its first word that is no option; the words after it are files it
	// reads (11-05 23:02: sed -n 1,40p review-check.sh; bash review-check.sh asked: the path has a w and an e)
	sawE, sawProg, progNext := false, false, false
	for _, a := range args {
		t := a.text
		short := strings.HasPrefix(t, "-") && !strings.HasPrefix(t, "--")
		if name == "sed" && a.ok {
			isProg := progNext || !sawE && !sawProg && !strings.HasPrefix(t, "-")
			progNext = short && strings.Contains(t, "e")
			sawE = sawE || progNext
			if !isProg && !strings.HasPrefix(t, "-") {
				continue // a file it reads
			}
			sawProg = sawProg || isProg
		}
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

// cmdName gives a command's name when it is a literal bare name or a path in a system folder (/usr/bin/grep), so the
// command is the one its name says (review r2: ./cat is any program).
func cmdName(w *syntax.Word) (string, bool) {
	l := literal(w)
	if !l.ok || strings.Contains(l.text, "/") && !sysDirs[path.Dir(l.text)] {
		return "", false
	}
	return strings.ToLower(path.Base(l.text)), true
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
		if k == "-p" {
			continue // --project (review r3b: "p" starts "path")
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
	"[": true, "shellcheck": true, "file": true, "stat": true, "cmp": true, "diff": true, "shasum": true, "md5": true,
	"readlink": true, "realpath": true, "dirname": true, "basename": true} // today's replay: command -p readlink in .bin shims

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

// exportFnRe finds an exported function (export -f, declare -fx) or set -a.
var exportFnRe = regexp.MustCompile(`\b((export|declare|typeset)\s+-[a-zA-Z]*f|set\s+-[a-zA-Z]*a)`)

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

// realDir gives a folder with its links followed, or cleaned when it does not exist.
func realDir(d string) string {
	if r, err := filepath.EvalSymlinks(d); err == nil {
		return r
	}
	return filepath.Clean(d)
}

// sysDirs are the folders a safeCmds name may be written with (/usr/bin/grep).
var sysDirs = map[string]bool{"/bin": true, "/usr/bin": true, "/sbin": true, "/usr/sbin": true}

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
	r := runs{file: f, src: src, scripts: map[*syntax.Word]bool{}, script: c.script}
	count := map[string]int{}
	fors := map[string][]*syntax.ForClause{}
	lits := map[string]string{} // a literal value each name is set to
	own := map[string]*syntax.Word{}
	var dyn []*syntax.Word // command names from a variable
	all := false           // eval or source may set any variable
	syntax.Walk(f, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.Assign:
			if n.Name != nil {
				count[n.Name.Value]++
				own[n.Name.Value] = n.Value
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
				r.scripts[n.Args[k]] = true // $S/x.sh: the run itself
				if hasSlash(n.Args[k]) {
					// a name with a / in its own text runs a file, never cd, eval or source (today's replay:
					// $D/../../frontend-bin/review-pass.sh); the file is read on its own
					break
				}
				dyn = append(dyn, n.Args[k])
				c.evals = append(c.evals, n.End().Offset())
				break
			}
			switch name := path.Base(args[k].text); {
			case name == "cd" || name == "pushd" || name == "popd":
				c.moved = true
				cds++
			case name == "eval" || name == "source" || name == "." || name == "trap" && len(args) > k+1 &&
				args[k+1].text != "-l" && args[k+1].text != "-p" && !onExit(args[k+1:]):
				all = true
				c.evals = append(c.evals, n.End().Offset())
			case name == "read" || name == "mapfile" || name == "readarray" || name == "unset" || name == "getopts" ||
				name == "printf" || name == "declare" || name == "typeset" || name == "local" || name == "export":
				for _, a := range args[k+1:] {
					if v, _, _ := strings.Cut(a.text, "="); a.ok {
						count[v] += 2
					}
				}
			}
			markRun(n, args, k, r.scripts)
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
	// with eval or source (and no loop that may run one again, nor a function whose body runs after them: review r5b),
	// a value set before the first of them holds up to it
	first := uint(0)
	if all && !loops && !funcs && len(c.evals) > 0 {
		first = slices.Min(c.evals)
	}
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
				if s, known := c.resolve(a.Value); !v.ok && known {
					v = word{text: s, ok: true} // D=$(dirname "$0"), S=$HOME/x
				}
			}
			if v.ok && count[a.Name.Value] == 1 && (!all || first > 0 && a.End().Offset() <= first) {
				c.vars[a.Name.Value] = setVar{text: v.text, end: a.End().Offset(), in: t.in, until: first}
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
		if c.cd != nil {
			c.cd.before = !funcs
		}
	}
	r.vars = c.vars
	r.cd, r.cwd, r.moved, r.movedAt = c.cd, c.cwd, c.moved, c.movedAt
	c.files = append(slices.Clone(c.files), r)
	c.own = nil
	if c.script != "" {
		c.own = map[string]*syntax.Word{}
		for name, w := range own {
			if count[name] == 1 && w != nil {
				c.own[name] = w
			}
		}
	}
	return c
}

// inner says whether a word in a script's text takes its value from the script alone: each variable in it is one the
// script sets once from text that, through such variables, reads no argument ($1, $@) and no variable it does not set
// ($HOME, $0 and its own folder aside). bin="$HOME/…/$(printf %s "$repo" | shasum)/crius-rec"; exec "$bin" "$@" runs a
// program whose name the command around the script cannot choose.
func (c ctx) inner(w *syntax.Word, depth int) bool {
	if c.own == nil || depth > 8 {
		return false
	}
	// a $(...) gives text from outside the script ($(cat cmd.txt)) unless it is the script's folder or a literal folder
	// and name follow it in the same quotes ("$HOME/…/$(… | shasum)/crius-rec"), review r3
	fixed := map[syntax.Node]bool{}
	for i, p := range w.Parts {
		if cs, isCs := p.(*syntax.CmdSubst); isCs {
			_, fixed[cs] = c.scriptDirOf(cs)
		}
		dq, isDq := p.(*syntax.DblQuoted)
		if !isDq {
			continue
		}
		for j, q := range dq.Parts {
			switch q := q.(type) {
			case *syntax.CmdSubst:
				_, fixed[q] = c.scriptDirOf(q)
				fixed[q] = fixed[q] || slashAfter(dq.Parts[j+1:]) || j == len(dq.Parts)-1 && slashAfter(w.Parts[i+1:])
			}
		}
	}
	ok := true
	syntax.Walk(w, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.CmdSubst:
			// what a fixed one reads only picks the folder (the hash of $repo above): it is not looked into
			ok = ok && fixed[n]
			return false
		case *syntax.ProcSubst:
			ok = false
		}
		pe, isPe := n.(*syntax.ParamExp)
		if !isPe || pe.Param == nil {
			return ok
		}
		switch v, set := c.own[pe.Param.Value]; {
		case pe.Param.Value == "HOME" || pe.Param.Value == "0" || pe.Param.Value == "BASH_SOURCE":
		case !set || !c.inner(v, depth+1):
			ok = false
		}
		return ok
	})
	return ok
}

// slashAfter says whether a literal with a / comes in these parts of a word.
func slashAfter(parts []syntax.WordPart) bool {
	for _, p := range parts {
		if l, ok := p.(*syntax.Lit); ok && strings.Contains(l.Value, "/") {
			return true
		}
	}
	return false
}

// markRun marks the word that names the file a call runs (bash x.sh, source x.sh, ./x.sh, bun x.ts), also behind a
// wrapper (timeout 60 bash x.sh).
// looksUp says whether command's options make it print what a name is (command -v, -V, -pv) instead of running it.
func looksUp(args []word) bool {
	for _, a := range args {
		if !a.ok || !strings.HasPrefix(a.text, "-") || a.text == "--" {
			return false
		}
		if strings.ContainsAny(a.text[1:], "vV") {
			return true
		}
	}
	return false
}

func markRun(n *syntax.CallExpr, args []word, k int, scripts map[*syntax.Word]bool) {
	for j := k; j < len(args); j++ {
		a := args[j]
		if !a.ok {
			return
		}
		name := path.Base(a.text)
		switch {
		case shells[name]:
			if i, sawC, sawS := shellOpts(args[j:]); !sawC && !sawS && j+i < len(args) {
				scripts[n.Args[j+i]] = true
			}
		case interpreters[name]:
			if i := codeFile(name, args[j:]); i > 0 {
				scripts[n.Args[j+i]] = true
			}
		case j == k && (name == "source" || name == "."):
			if j+1 < len(args) {
				scripts[n.Args[j+1]] = true
			}
		case strings.Contains(a.text, "/") && !sysDirs[path.Dir(a.text)]:
			scripts[n.Args[j]] = true
		case name == "command" && looksUp(args[j+1:]):
			return
		case wrappers[name]:
			// the command a wrapper runs is its first word that is no option, setting or number (timeout 60, nice -n 10)
			for j+1 < len(args) && args[j+1].ok && (strings.HasPrefix(args[j+1].text, "-") || envSetRe.MatchString(args[j+1].text) ||
				strings.Trim(args[j+1].text, "0123456789.smhd") == "") {
				j++
			}
			continue
		}
		return
	}
}

// followCd gives the command's one cd when it is a step of an && chain that is a statement at its top level or in a
// ( … ) there (cd D, a && cd D && b, or cd D || exit as a whole statement), to one word that names a folder that is
// there by its full path, or by one from the start folder when CDPATH is not set. The steps after it in its chain, and
// everything after cd D || exit up to the end of its ( … ), run only when it went to D.
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
				// a relative one is the start folder's when no CDPATH can send it elsewhere (cd sub && bash post.sh, review r3)
				if known && !filepath.IsAbs(dir) && filepath.IsAbs(c.cwd) && os.Getenv("CDPATH") == "" && dir != "-" {
					dir = filepath.Join(c.cwd, dir)
				}
				if fi, err := os.Stat(dir); !known || !filepath.IsAbs(dir) || err != nil || !fi.IsDir() {
					return nil
				}
				return &cdTo{dir: dir, from: ce.Pos().Offset(), at: ce.End().Offset(), sure: sure, to: l.to}
			}
		}
	}
	return nil
}

// hasSlash says whether a word's own text, outside what it expands, holds a /: as a command name it names a file.
func hasSlash(w *syntax.Word) bool {
	for _, p := range w.Parts {
		switch p := p.(type) {
		case *syntax.Lit:
			if strings.Contains(p.Value, "/") {
				return true
			}
		case *syntax.SglQuoted:
			if strings.Contains(p.Value, "/") {
				return true
			}
		case *syntax.DblQuoted:
			for _, q := range p.Parts {
				if l, ok := q.(*syntax.Lit); ok && strings.Contains(l.Value, "/") {
					return true
				}
			}
		}
	}
	return false
}

// onExit says whether a trap's text runs only when the shell exits (trap '…' EXIT): it changes no state the rest of
// the text sees (today's replay: make-newtag.sh lost every variable after its cleanup trap).
func onExit(args []word) bool {
	if len(args) > 0 && args[0].text == "--" {
		args = args[1:]
	}
	if len(args) < 2 {
		return false
	}
	for _, a := range args[1:] {
		if !a.ok || a.text != "EXIT" && a.text != "0" && a.text != "SIGEXIT" {
			return false
		}
	}
	return true
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
		if pe.Param.Value == "0" && c.script != "" {
			b.WriteString(c.script)
			return true
		}
		if !ok && pe.Param.Value == "HOME" {
			// "$HOME/.crius/bin/bus-post" is how leads call bus-post (review r3 of the ctx tools check)
			home, err := os.UserHomeDir()
			b.WriteString(home)
			return err == nil
		}
		if !ok || v.lost || v.end > w.Pos().Offset() || v.in != nil && w.End().Offset() > v.in.End().Offset() ||
			v.until > 0 && w.End().Offset() > v.until {
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
		case *syntax.CmdSubst:
			d, ok := c.scriptDirOf(p)
			if !ok {
				return "", false
			}
			b.WriteString(d)
		case *syntax.DblQuoted:
			for _, q := range p.Parts {
				switch q := q.(type) {
				case *syntax.Lit:
					b.WriteString(unescape(q.Value, "\"\\$`"))
				case *syntax.ParamExp:
					if !param(q) {
						return "", false
					}
				case *syntax.CmdSubst:
					d, ok := c.scriptDirOf(q)
					if !ok {
						return "", false
					}
					b.WriteString(d)
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
		strings.Contains(s, "/_apis/") || !w.ok && (adoMarkRe.MatchString(c.whole) || namesAzCLI(c.whole))
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
	case "show", "list", "query", "get", "get-access-token", "wait", "download":
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
		return asked("az " + o.cmd) // owner 11-05: a work item change is a post
	case "repos pr create", "repos pr update":
		if _, ok := o.last("--description", "-d"); ok {
			return asked("az " + o.cmd + " --description")
		}
		if why := unread("az " + o.cmd); why != "" {
			return why
		}
		return asked("az " + o.cmd) // owner 11-05: a title, a status (completed, abandoned) or a draft flag is a post too
	case "devops wiki page create", "devops wiki page update":
		return asked("az " + o.cmd)
	case "repos pr checkout", "repos pr policy queue":
		// a local branch; a policy run queued again (owner 11-05: not a post)
		return ""
	}
	// owner 11-05: every other change to a pull request, a work item or a pipeline posts (a vote, a reviewer, a linked
	// work item, a run queued)
	if len(words) >= 2 && (words[0] == "repos" && words[1] == "pr" || words[0] == "boards" || words[0] == "pipelines") {
		return asked("az " + o.cmd)
	}
	if writeVerbs[verb] && textWordRe.MatchString(o.cmd) {
		return asked("az " + o.cmd)
	}
	return ""
}

// owner 11-05: az devops invoke with POST, PUT, PATCH or DELETE, or with an --in-file, posts; a POST to a query resource
// (wiql, workitemsbatch, commitsbatch, itemsbatch) only reads.
func checkInvoke(o azOpts, c ctx) string {
	w, set := o.last("--http-method")
	_, in := o.last("--in-file")
	m := method{word: word{text: "GET", ok: true}}
	if set && !(w.ok && w.text == "") {
		m = methodOf(w)
	}
	if !writesTo(m, nil) && !in {
		return ""
	}
	r, _ := o.last("--resource")
	a, _ := o.last("--area")
	what := "az devops invoke " + methodName(m)
	if in {
		what += " --in-file"
	}
	switch res := strings.ToLower(r.text); {
	case !r.ok:
		return asked(what + " to a resource the guard cannot read")
	case a.ok && w.ok && strings.EqualFold(w.text, "POST") && readResources[strings.ToLower(a.text)+" "+res]:
		return ""
	}
	return asked(what + " --resource " + r.text)
}

// readResources are the Azure DevOps resources (area and resource) where a POST is a query.
var readResources = map[string]bool{"wit wiql": true, "wit workitemsbatch": true, "git commitsbatch": true,
	"git itemsbatch": true}

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
	spread := slices.ContainsFunc(args, spreads) // an option's value too (curl --url "$@", review r3b)
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
	if spread {
		return asked("curl with words the guard cannot read, to Azure DevOps")
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
	spread := slices.ContainsFunc(args, spreads)
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
	if len(ado) > 0 && spread {
		return asked("wget with words the guard cannot read, to Azure DevOps")
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
	spread := slices.ContainsFunc(args, spreads)
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
	if spread {
		return asked("HTTP request with words the guard cannot read, to Azure DevOps")
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
