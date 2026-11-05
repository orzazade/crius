package main

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"mvdan.cc/sh/v3/syntax"
)

// Scripts and code files a command runs (owner 11-05: 14 asks in half an hour, 3 of them posts; the rest were scripts that
// only read and were reported as "a shell script the guard cannot read"). The guard reads the file a command runs (bash
// x.sh, sh, zsh, source, ./x.sh or any path, bun x.ts, node, python3 x.py), and a heredoc the same command writes into
// it, and applies the command check to what it holds, down 4 levels: it asks only when that posts, and names the file and
// line. A file it cannot read (missing, unreadable, binary for a shell, a path from text it cannot work out) asks and says
// why when the command or a script around it names az or Azure DevOps, as an unread script did before; a name it cannot
// place for sure is first looked for in the start folder and the cd's. Past 20 s it asks. A script the command (or the script itself) may change before it runs, or
// that gets code-running variables (PATH, PS4, BASH_ENV, …) or computed text that names az, asks when it or the command
// names az or Azure DevOps.

// runKind says how a file is started.
type runKind int

const (
	byShell  runKind = iota // bash x.sh, sh, zsh
	bySource                // source x.sh, . x.sh: a name the guard cannot place asks only when the command names az
	byPath                  // ./x.sh, ~/bin/x: a binary there is a program, not read
	byCode                  // bun x.ts, node x.js, python3 x.py
)

// scriptText is one text a file may hold when it runs, and where it came from for the ask.
type scriptText struct {
	body, where string
	code        string // the language of code that is not shell, or ""
}

// cannotRead asks for a file the guard could not read, with the reason, when the command or a script that runs it names
// az or Azure DevOps, as before 11-05 (a missing ./build/app or bun x.ts in other work is no post).
func cannotRead(c ctx, name, why string) string {
	if !namesAz(c) {
		return ""
	}
	return asked("the guard cannot read " + name + ": " + why)
}

// namesAz: the command, or a script around this one, names az or Azure DevOps.
func namesAz(c ctx) bool {
	return namesAzOrADO(c.whole)
}

// cannotPlace asks, like cannotRead, for a file whose path the guard cannot work out; source keeps its own text (the
// start-up files of tools: . "$NVM_DIR/nvm.sh").
func cannotPlace(a word, c ctx, kind runKind, why string) string {
	what := "the shell script "
	if kind == byCode {
		what = "the code file "
	}
	if kind == bySource {
		return unsure(c, "a sourced file name")
	}
	return cannotRead(c, what+a.src, why)
}

// atLine adds where an ask came from: "(az repos pr create --description; /x/pr.sh line 3)".
func atLine(why, where string, line uint) string {
	if !strings.HasSuffix(why, ")") {
		return why
	}
	return strings.TrimSuffix(why, ")") + "; " + where + " line " + strconv.FormatUint(uint64(line), 10) + ")"
}

// checkScript finds the file a command runs and judges it (scriptAt). A relative name follows one cd to a full path,
// and also means the start folder's file where that cd may have failed.
func checkScript(a word, c ctx, kind runKind, plain bool) string {
	name, ok := a.text, a.ok
	if !ok && a.node != nil {
		name, ok = c.resolve(a.node)
	}
	if !ok {
		return cannotPlace(a, c, kind, "its path is built from text the guard cannot work out")
	}
	changed := "the command changes folder in a way the guard does not follow"
	switch {
	case filepath.IsAbs(name):
		return scriptAt(name, c, kind, plain, false, c.cwd)
	case c.movedAt > 0 && (a.node == nil || a.node.End().Offset() > c.movedAt):
		return unplaced(a, name, c, kind, plain, changed)
	case c.cwd != "" && (!c.moved || c.cd != nil && a.node != nil && a.node.Pos().Offset() >= c.cd.to):
		return scriptAt(filepath.Join(c.cwd, name), c, kind, plain, false, c.cwd)
	case c.cd == nil || a.node == nil || a.node.Pos().Offset() < c.cd.at || a.node.End().Offset() > c.cd.to:
		return unplaced(a, name, c, kind, plain, changed)
	}
	// after cd D the file is D's; where the cd may have failed, it is the start folder's too, and one that is not there
	// runs nothing
	if why := scriptAt(filepath.Join(c.cd.dir, name), c, kind, plain, false, c.cd.dir); why != "" || a.node.End().Offset() <= c.cd.sure {
		return why
	}
	if c.cwd == "" {
		return cannotPlace(a, c, kind, changed)
	}
	c.alt = filepath.Join(c.cd.dir, name)
	return scriptAt(filepath.Join(c.cwd, name), c, kind, plain, true, c.cwd)
}

// readText is a script the guard read for this hook call, and its text.
type readText struct{ name, body string }

// madeBy gives the text of a script read before for this hook call that names name's file and is neither name itself
// nor a script that runs it (whose writes changes reads): it ran before and may have written name.
func (c ctx) madeBy(name string) string {
	if c.read == nil {
		return ""
	}
next:
	for _, t := range *c.read {
		// the same word's file at its other place (after a cd that may fail the word names a file in either folder:
		// kq.sh's usage line names kq.sh, today's replay) never ran there, so it made nothing; another file by the same
		// name did (review r4: cd sub && ./x.sh .. && bash ../x.sh)
		if samePath(t.name, name) || c.alt != "" && samePath(t.name, c.alt) || !strings.Contains(t.body, filepath.Base(name)) {
			continue
		}
		for _, r := range c.files {
			if r.src == t.body {
				continue next
			}
		}
		return t.body
	}
	return ""
}

// deadline is when the guard stops reading scripts and asks (review of the 11-05 change: a 1 MB script that runs itself
// took 43 s, and a hook past Claude Code's 60 s limit lets the call through); zero in tests: no limit.
var deadline time.Time

func late() bool {
	return !deadline.IsZero() && time.Now().After(deadline)
}

// unplaced judges a relative name the guard cannot place for sure (after source, eval, $X or a cd it does not follow):
// the start folder's file and the cd's are read, and one that posts asks (review of the 11-05 change: source env.sh &&
// bash sub/post.sh passed); otherwise it asks as a file the guard cannot read.
func unplaced(a word, name string, c ctx, kind runKind, plain bool, why string) string {
	dirs := []string{c.cwd}
	if c.cd != nil {
		dirs = append(dirs, c.cd.dir)
	}
	for i, d := range dirs {
		if d == "" {
			continue
		}
		if i > 0 {
			c.alt = filepath.Join(dirs[0], name)
		}
		if w := scriptAt(filepath.Join(d, name), c, kind, plain, true, d); w != "" {
			return w
		}
	}
	return cannotPlace(a, c, kind, why)
}

// shebangCode gives the language a #! line runs other than a shell, or "".
var shebangCode = regexp.MustCompile(`^#!.*\b(node|bun|deno|tsx|ts-node|python[0-9.]*|ruby|perl|pwsh)\b`)

// scriptAt judges the file at name; orNone: when no file is there and nothing in the command writes one, nothing runs.
// plain: the shell reads no start-up file of its own (zsh, bash -i or -l do). dir is the folder it runs in.
func scriptAt(name string, c ctx, kind runKind, plain, orNone bool, dir string) string {
	if late() {
		return asked("the guard ran out of time reading the scripts the command runs")
	}
	if c.reads != nil {
		if *c.reads++; *c.reads > 16 {
			return cannotRead(c, name, "the command runs more than 16 scripts, the most the guard reads")
		}
	}
	if c.depth >= 4 {
		return cannotRead(c, name, "it is run 4 scripts down, deeper than the guard reads")
	}
	heredocs, written := c.heredocs(name)
	var texts []scriptText
	fi, err := os.Stat(name)
	switch {
	case err == nil && fi.IsDir():
		return cannotRead(c, name, "it is a folder")
	case err == nil && !fi.Mode().IsRegular():
		return cannotRead(c, name, "it is not a regular file")
	case err == nil:
		body, rerr := readHead(name, 1<<20)
		switch {
		case errors.Is(rerr, fs.ErrPermission):
			return cannotRead(c, name, "permission denied")
		case rerr != nil:
			return cannotRead(c, name, rerr.Error())
		case bytes.IndexByte(body, 0) < 0 && fi.Size() > 1<<20:
			return cannotRead(c, name, "it is over 1 MiB")
		case bytes.IndexByte(body, 0) >= 0 && kind == byPath:
			// a compiled program (bus-post, crius-rec): not read; what the command writes into its folder still counts
			if c.changes(name, written) {
				return unsure(c, "a program the command may change before it runs ("+name+"):")
			}
			return ""
		case bytes.IndexByte(body, 0) >= 0:
			return cannotRead(c, name, "it is not text")
		}
		t := scriptText{body: string(body), where: name}
		if kind == byCode {
			t.code = codeLang(name)
		} else if m := shebangCode.FindStringSubmatch(firstLine(t.body)); m != nil && kind == byPath {
			t.code = m[1]
		}
		texts = append(texts, t)
	case errors.Is(err, fs.ErrNotExist):
		if len(heredocs) == 0 {
			switch made := c.madeBy(name); {
			case made != "":
				// a script the command ran before names this one: it may write it (gen.sh && bash out.sh, review r3b)
				return unsure(ctx{whole: made + "\n" + c.whole}, "a script or program another script the command runs may make:")
			case c.changes(name, written):
				// made by the command in a way the guard does not read (go build -o x, cp a.sh x.sh)
				return unsure(c, "a script or program the command makes before it runs ("+name+"):")
			case orNone:
				return ""
			}
			return cannotRead(c, name, "it does not exist")
		}
	case errors.Is(err, fs.ErrPermission):
		return cannotRead(c, name, "permission denied")
	default:
		return cannotRead(c, name, err.Error())
	}
	for _, h := range heredocs {
		t := scriptText{body: h, where: "the heredoc that writes " + name}
		if kind == byCode {
			t.code = codeLang(name)
		}
		texts = append(texts, t)
	}
	all := c.whole
	for _, t := range texts {
		all = t.body + "\n" + all
	}
	made := c.madeBy(name)
	if c.read != nil {
		for _, t := range texts {
			*c.read = append(*c.read, readText{name: name, body: t.body})
		}
	}
	for _, t := range texts {
		if t.code != "" {
			if why, line := codePosts(t.code, t.body); why != "" {
				return atLine(why, t.where, line)
			}
			continue
		}
		sc := ctx{cwd: dir, whole: t.body + "\n" + c.whole, depth: c.depth + 1, reads: c.reads, read: c.read, files: c.files,
			script: name}
		if why, line := checkBashAt(t.body, sc); why != "" {
			return atLine(why, t.where, line)
		}
	}
	if !plain || c.changes(name, written) || selfChanges(texts, name, dir) || made != "" {
		return unsure(ctx{whole: made + "\n" + all}, "a script the command or the script itself may change before it runs ("+name+"):")
	}
	if late() {
		return asked("the guard ran out of time reading the scripts the command runs")
	}
	return ""
}

// readHead reads at most n bytes of a file.
func readHead(name string, n int64) ([]byte, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, n))
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}

// codeLang names the language of a code file by its extension.
func codeLang(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".py":
		return "python"
	case ".rb":
		return "ruby"
	case ".pl":
		return "perl"
	case ".ps1":
		return "pwsh"
	}
	return "javascript"
}

// codeFileRe is a file name an interpreter runs as code (bun x.ts, node dist/x.js, python3 x.py).
var codeFileRe = regexp.MustCompile(`(?i)\.(ts|tsx|mts|cts|js|mjs|cjs|jsx|py|rb|pl|ps1)$`)

// codeFile gives the word of the file an interpreter runs (bun [run] x.ts, node [opts] x.js, python3 [opts] x.py), or
// -1 when it runs none (a module, a package script, a subcommand, inline code, stdin).
func codeFile(name string, args []word) int {
	for i := 1; i < len(args); i++ {
		a := args[i]
		if i > 1 && args[i-1].ok && codeValueOpts[args[i-1].text] {
			continue // the option's value: node -r ./pre.js x.js, --env-file .env, python3 -W ignore
		}
		if !a.ok {
			// review of the 11-05 change: node "$S" runs the file $S names; checkScript works it out or says it cannot
			return i
		}
		switch {
		case a.text == "-m" || a.text == "-":
			return -1
		case (name == "bun" || name == "deno") && i == 1 && a.text == "run":
			continue
		case strings.HasPrefix(a.text, "-"):
			continue
		case codeFileRe.MatchString(a.text):
			return i
		}
		return -1
	}
	return -1
}

// codeValueOpts are interpreter options that take the next word as their value.
var codeValueOpts = map[string]bool{"-r": true, "--require": true, "--import": true, "--loader": true,
	"--experimental-loader": true, "--env-file": true, "-C": true, "--conditions": true, "--title": true, "--cwd": true,
	"--preload": true, "--config": true, "--tsconfig-override": true, "-W": true, "-X": true}

// azLineRe finds an az command in code: a string that starts the CLI (az or a path to it), up to the quote that ends the
// string or the line's end. Its words may start with a placeholder (`az ${args}`) or be joined on after the quote
// ('az ' + open('cmd.txt').read(), review r5).
var azLineRe = regexp.MustCompile("(?:^|[\"'`(\\s/])(az\\s+(?:[a-z$#{%][^\"'`\\n]*)?)")

// placeholderRe is a value code puts into a string: {url} (f-strings, .format), ${url}, #{url}, %s.
var placeholderRe = regexp.MustCompile(`[$#]?\{[^{}\n]*\}|%(\([A-Za-z_]+\))?[sdrf]`)

// runners are the calls that run a program from a list of words: 'az' as their first word runs az.
var runners = map[string]bool{"spawn": true, "spawnSync": true, "execFile": true, "execFileSync": true, "execa": true,
	"execaSync": true, "run": true, "Popen": true, "call": true, "check_call": true, "check_output": true, "Command": true,
	"spawn_blocking": true, "system": true, "exec": true, "capture2": true, "capture3": true, "popen": true, "$": true}

// runsList says whether code that comes before 'az', in a list of words ends a list's start ([ 'az', …]) or a runner's
// call (spawnSync('az', …), subprocess.run(['az', …])).
func runsList(before string) bool {
	b := strings.TrimRight(before, " \t\n")
	if strings.HasSuffix(b, "[") {
		return true
	}
	if !strings.HasSuffix(b, "(") {
		return false
	}
	b = strings.TrimRight(b[:len(b)-1], " \t\n")
	k := strings.LastIndexFunc(b, func(r rune) bool { return !(r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r)) })
	return runners[b[k+1:]]
}

// azArrayHeadRe starts az run with its words as a list; azArrayRe takes the words, which may run over several lines.
var azArrayHeadRe = regexp.MustCompile(`['"]az['"]\s*,`)

// azJoinRe is a list that holds only az, joined to more words (['az'] + args).
var azJoinRe = regexp.MustCompile(`\[\s*['"]az['"]\s*\]\s*\+`)
var azArrayRe = regexp.MustCompile(`['"]az['"]\s*,\s*\[?((?:\s*(?:['"][^'"\n]*['"]|[^'"\s,\]\)]+)\s*,?)*)`)

// codeWriteRe is an HTTP write in code: a method set to POST, PUT, PATCH or DELETE, a client's post or put call, or an
// Azure DevOps SDK call that creates or updates a thread, comment, pull request or work item.
var codeWriteRe = regexp.MustCompile(`(?i)(method\s*[:=]\s*['"](post|put|patch|delete)['"]|\b(requests|axios|httpx|session|client|http|got|ky)\.(post|put|patch|delete)\s*\(|\brequest\s*\(\s*['"](post|put|patch|delete)['"]|\bRequest\s*\([^)]*\bdata\s*=|-X\s*['"]?(post|put|patch|delete)\b|--request\s+(post|put|patch|delete)\b|--http-method\s+['"]?(post|put|patch|delete)\b|--method\s+['"]?(post|put|patch|delete)\b|\b(create|update)_?(thread|comment|pull_?request|work_?item)\w*\s*\()`)

// codePosts judges code of another language: each az command it holds gets the command check, and an HTTP write in code
// that names Azure DevOps asks; it gives the line. A string cut at a quote or joined to more text ("… --url " + url), a
// placeholder ({url}, %s) and a list of words from a variable count as words the guard cannot read: in a read they pass,
// in a write they ask.
func codePosts(lang, body string) (string, uint) {
	lines := strings.Split(body, "\n")
	ado := adoMarkRe.MatchString(body)
	c := ctx{whole: body}
	for i := range lines {
		n := uint(i + 1)
		l := lines[i]
		// a line that ends in \ goes on in the next (a shell command in a Python """ string)
		for j := i; j+1 < len(lines) && j < i+20 && strings.HasSuffix(strings.TrimRight(lines[j], " \t"), "\\"); j++ {
			l = strings.TrimSuffix(strings.TrimRight(l, " \t"), "\\") + " " + strings.TrimSpace(lines[j+1])
		}
		for _, m := range azLineRe.FindAllStringSubmatchIndex(l, -1) {
			text := placeholderRe.ReplaceAllString(l[m[2]:m[3]], `"$$__x"`)
			cut := false
			open := byte(0) // the quote the az string starts with, if any
			if m[0] < m[2] {
				open = l[m[0]]
			}
			if m[3] < len(l) {
				rest := strings.TrimLeft(l[m[3]+1:], " \t")
				cut = l[m[3]] != open || rest != "" && strings.ContainsRune("+.%", rune(rest[0]))
			}
			why := ""
			if _, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(text), ""); err != nil || cut {
				// text no shell runs as it stands ('no az token (az login?)') or that goes on past the quote: its words
				// get the az check, and what follows the quote is a word the guard cannot read
				var args []word
				for _, f := range strings.Fields(text)[1:] {
					// a placeholder ($__x) or one the quote cut ( `az ${require(` ); a bare $X in code is text
					args = append(args, word{text: f, src: f, ok: !strings.Contains(f, "$__x") && !strings.Contains(f, "${")})
				}
				if strings.ContainsRune("\"'`", rune(open)) && (len(args) == 0 || !args[0].ok) {
					// review r5: 'az ' + open('cmd.txt').read(), `az ${args}`: the command words come from elsewhere
					return asked(lang + " code that runs az with words the guard cannot read"), n
				}
				if cut {
					args = append(args, word{text: "--", src: "…"})
				}
				why = checkAz(args, c)
			} else {
				why = checkBash(text, ctx{whole: body, reads: new(int), depth: 4, code: true})
			}
			if why != "" {
				return why, n
			}
		}
		if m := azJoinRe.FindStringIndex(lines[i]); m != nil && runsList(strings.Join(lines[:i], "\n")+"\n"+lines[i][:m[0]]) {
			// review r5b: ['az'] + open('a').read().split(): the words come from elsewhere
			return asked(lang + " code that runs az with words the guard cannot read"), n
		}
		if azArrayHeadRe.MatchString(lines[i]) {
			more := strings.Join(lines[i:min(i+20, len(lines))], " ")
			for _, m := range azArrayRe.FindAllStringSubmatchIndex(more, -1) {
				if m[0] >= len(lines[i]) {
					break
				}
				if !runsList(strings.Join(lines[:i], "\n") + "\n" + lines[i][:m[0]]) {
					continue // console.log('az', …): a language code, no az run (today's replay)
				}
				var args []word
				for _, w := range regexp.MustCompile(`['"]([^'"\n]*)['"]|([^'"\s,\]\)]+)`).FindAllStringSubmatch(more[m[2]:m[3]], -1) {
					if w[2] != "" {
						// a word from a variable is one argument the guard cannot read: in the command words or a text
						// write's options it asks, after them in a read it does not (['repos', 'pr', 'show', '--id', pr])
						args = append(args, word{text: w[2], src: w[2]})
						continue
					}
					args = append(args, word{text: w[1], src: w[1], ok: true})
				}
				if len(args) == 0 {
					return asked(lang + " code that runs az with words the guard cannot read"), n
				}
				if why := checkAz(args, c); why != "" {
					return why, n
				}
			}
		}
		if ado && codeWriteRe.MatchString(l) {
			return asked(lang + " code that sends a write to Azure DevOps"), n
		}
	}
	return "", 0
}

// heredocs gives the texts a heredoc in the command writes into the script (cat > x.sh <<'EOF', cat >> x.sh, tee [-a]
// x.sh), and the words that name the script there. Only a cat or tee that is no function, reads only the heredoc and
// writes the script by a path the guard reads counts; any other write to it is a change (changes).
func (c ctx) heredocs(script string) ([]string, map[*syntax.Word]bool) {
	var texts []string
	words := map[*syntax.Word]bool{}
	for i := range c.files {
		r := &c.files[i]
		rc := ctx{cwd: r.cwd, vars: r.vars, moved: r.moved, movedAt: r.movedAt, script: c.script, cd: r.cd}
		funcs := map[string]bool{}
		syntax.Walk(r.file, func(n syntax.Node) bool {
			if fd, ok := n.(*syntax.FuncDecl); ok && fd.Name != nil {
				funcs[fd.Name.Value] = true
			}
			return true
		})
		syntax.Walk(r.file, func(n syntax.Node) bool {
			st, ok := n.(*syntax.Stmt)
			if !ok {
				return true
			}
			ce, ok := st.Cmd.(*syntax.CallExpr)
			if !ok || len(ce.Args) == 0 {
				return true
			}
			name, known := cmdName(ce.Args[0])
			if !known || funcs[name] {
				return true
			}
			var doc *syntax.Redirect
			ins := 0
			var outs []*syntax.Word
			for _, rd := range st.Redirs {
				switch rd.Op {
				case syntax.Hdoc, syntax.DashHdoc:
					doc = rd
					ins++
				case syntax.RdrIn, syntax.WordHdoc, syntax.RdrInOut:
					ins++
				case syntax.RdrOut, syntax.AppOut, syntax.ClbOut, syntax.RdrAll, syntax.AppAll:
					outs = append(outs, rd.Word)
				}
			}
			if doc == nil || ins != 1 {
				return true
			}
			var target *syntax.Word
			switch {
			case name == "cat" && len(ce.Args) == 1 && len(outs) == 1:
				target = outs[0]
			case name == "tee" && len(ce.Args) == 2:
				target = ce.Args[1]
			case name == "tee" && len(ce.Args) == 3 && literal(ce.Args[1]).text == "-a":
				target = ce.Args[2]
			default:
				return true
			}
			if p, ok := rc.place(target); ok && samePath(p, script) {
				texts = append(texts, heredocBody(doc, rc))
				words[target] = true
			}
			return true
		})
	}
	return texts, words
}

// heredocBody gives the text a heredoc writes: as written when its end word is quoted; else with \$, \` and \\ undone
// and each $X the command set to a literal put in (other expansions stay as written, so the check reads them as such).
func heredocBody(rd *syntax.Redirect, c ctx) string {
	if rd.Hdoc == nil {
		return ""
	}
	quoted := false
	for _, p := range rd.Word.Parts {
		switch p := p.(type) {
		case *syntax.SglQuoted, *syntax.DblQuoted:
			quoted = true
		case *syntax.Lit:
			quoted = quoted || strings.Contains(p.Value, `\`)
		}
	}
	var b strings.Builder
	for _, p := range rd.Hdoc.Parts {
		switch p := p.(type) {
		case *syntax.Lit:
			if quoted {
				b.WriteString(p.Value)
			} else {
				b.WriteString(strings.ReplaceAll(unescape(p.Value, "$`\\"), "\\\n", ""))
			}
		case *syntax.ParamExp:
			if s, ok := c.resolve(&syntax.Word{Parts: []syntax.WordPart{p}}); ok {
				b.WriteString(s)
				continue
			}
			syntax.NewPrinter().Print(&b, p)
		default:
			syntax.NewPrinter().Print(&b, p)
		}
	}
	return b.String()
}

// place gives the path a word names, from the folder the command started in when it is relative and the command did
// not change folder.
// places gives each path a word may name: after cd D; (which may fail) a relative word is D's or the start folder's
// (today's replay: cd …/watch; bash kq.sh q | tee ../x.json). nil: the guard cannot tell.
func (c ctx) places(w *syntax.Word) []string {
	if p, ok := c.place(w); ok {
		return []string{p}
	}
	s, ok := c.resolve(w)
	if !ok || s == "" || filepath.IsAbs(s) || strings.ContainsAny(literal(w).src, "*?[") || c.cwd == "" ||
		c.movedAt > 0 && w.End().Offset() > c.movedAt || c.cd == nil || w.Pos().Offset() < c.cd.at || w.End().Offset() > c.cd.to {
		return nil
	}
	return []string{filepath.Join(c.cd.dir, s), filepath.Join(c.cwd, s)}
}

func (c ctx) place(w *syntax.Word) (string, bool) {
	s, ok := c.resolve(w)
	if !ok || s == "" || strings.ContainsAny(literal(w).src, "*?[") {
		return "", false
	}
	if !filepath.IsAbs(s) {
		dir := c.cwd
		if c.movedAt > 0 && w.End().Offset() > c.movedAt {
			return "", false
		}
		if c.moved {
			switch {
			case c.cd != nil && c.cd.before && w.End().Offset() <= c.cd.from:
				// today's replay: a word before the cd runs in the start folder (review-pass.sh $R/x; cd $S; …)
			case c.cd == nil || w.Pos().Offset() < c.cd.at || w.End().Offset() > c.cd.sure:
				return "", false
			default:
				// 11-05 18:39: after cd D && …, a relative word there is D's (cd D && x.sh 1)
				dir = c.cd.dir
			}
		}
		if dir == "" {
			return "", false
		}
		s = filepath.Join(dir, s)
	}
	return filepath.Clean(s), true
}

// samePath says whether two paths name one file: the same text (case folded, as macOS's disk does), the same folder
// through links, or one file.
func samePath(a, b string) bool {
	if strings.EqualFold(filepath.Clean(a), filepath.Clean(b)) ||
		strings.EqualFold(filepath.Join(realDir(filepath.Dir(a)), filepath.Base(a)), filepath.Join(realDir(filepath.Dir(b)), filepath.Base(b))) {
		return true
	}
	x, aerr := os.Stat(a)
	y, berr := os.Stat(b)
	return aerr == nil && berr == nil && os.SameFile(x, y)
}

// within says whether path p is folder dir or inside it, links followed, case folded.
func within(p, dir string) bool {
	p, dir = strings.ToLower(realDir(p)), strings.ToLower(realDir(dir))
	return p == dir || strings.HasPrefix(p, strings.TrimSuffix(dir, "/")+"/")
}

// fileWriters write or replace files their words name (or, for tar and unzip with no -C, the folder they run in).
var fileWriters = map[string]bool{"cp": true, "mv": true, "ln": true, "install": true, "rsync": true, "ditto": true,
	"tar": true, "unzip": true, "tee": true, "dd": true, "patch": true, "truncate": true, "rm": true, "sponge": true}

// oneFile are the writers that write only the files they name (sed -i, perl -i too).
var oneFile = map[string]bool{"tee": true, "truncate": true, "sponge": true, "sed": true, "perl": true, "ruby": true}

// gitWrites are the git subcommands that may rewrite files in a work tree.
var gitWrites = map[string]bool{"checkout": true, "restore": true, "apply": true, "reset": true, "pull": true,
	"merge": true, "stash": true, "switch": true, "am": true, "cherry-pick": true, "rebase": true, "mv": true,
	"clone": true, "worktree": true}

// gitNoFile are git subcommands with no option that writes a work-tree file (as log --output and diff --output do), so
// a word the guard cannot read after one writes nothing.
var gitNoFile = map[string]bool{"cat-file": true, "rev-parse": true, "status": true, "ls-files": true, "ls-tree": true,
	"merge-base": true, "rev-list": true, "show-ref": true, "for-each-ref": true, "symbolic-ref": true, "name-rev": true,
	"describe": true, "fetch": true}

// readsOnly are commands whose words may name the script and leave it as it is.
var readsOnly = map[string]bool{"chmod": true, "shellcheck": true, "bat": true, "less": true, "file": true}

// changes says whether the commands around a script run (c.files) may make it other than the guard read it: a code
// variable set for it (PATH, PS4, BASH_ENV, an exported function), computed text that names az handed to it, a write to
// it or into its folder (a redirect, cp, tar, git checkout, a program the guard cannot name) other than the heredoc it
// read (fine), or a word naming it in any command not known to leave it as is.
func (c ctx) changes(script string, fine map[*syntax.Word]bool) bool {
	for i := range c.files {
		r := c.files[i]
		// each text in the folder state of its own (review of the 11-05 change: a script's folder was used for the
		// command's relative words)
		rc := ctx{cwd: r.cwd, vars: r.vars, moved: r.moved, movedAt: r.movedAt, script: r.script, cd: r.cd, alt: c.alt}
		// a script's own text is held to what it writes by name (selfChanges); the command the agent wrote also to
		// writes the guard cannot place
		if handsCode(r) || changesIn(r, script, rc, r.script != "", r.script == script, fine) {
			return true
		}
	}
	return false
}

// selfChanges says whether a script's own text may change it before bash reads the rest: a write to $0 or to its own
// path, or a code variable it sets.
func selfChanges(texts []scriptText, script, dir string) bool {
	for _, t := range texts {
		if t.code != "" {
			continue
		}
		f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(t.body), "")
		if err != nil {
			return true
		}
		sc := (ctx{cwd: dir, script: script}).scan(f, t.body)
		if changesIn(sc.files[len(sc.files)-1], script, ctx{cwd: dir, vars: sc.vars, moved: sc.moved, script: script}, true, true, nil) {
			return true
		}
	}
	return false
}

// namesSelf says whether a word expands $0 or BASH_SOURCE, the running script's path.
func namesSelf(w *syntax.Word) bool {
	found := false
	syntax.Walk(w, func(n syntax.Node) bool {
		if pe, ok := n.(*syntax.ParamExp); ok && pe.Param != nil && (pe.Param.Value == "0" || pe.Param.Value == "BASH_SOURCE") {
			found = true
		}
		return !found
	})
	return found
}

// changesIn is changes for one text; self: the text is a script's, whose writes count only where they name the script;
// own: it is the text of the script judged, where $0 names it.
func changesIn(r runs, script string, rc ctx, self, own bool, fine map[*syntax.Word]bool) bool {
	if codeVarSet(r.file, r.src, afterRuns(r)) {
		return true
	}
	base := strings.ToLower(filepath.Base(script))
	dir := filepath.Dir(script)
	names := func(w *syntax.Word) bool {
		l := literal(w)
		if strings.Contains(strings.ToLower(l.src), base) || strings.Contains(strings.ToLower(l.text), base) ||
			own && namesSelf(w) {
			return true
		}
		return slices.ContainsFunc(rc.places(w), func(p string) bool { return samePath(p, script) })
	}
	// into: a write to w may land in the script's folder (or one above it); one the guard cannot place may too
	into := func(w *syntax.Word) bool {
		if names(w) {
			return true
		}
		ps := rc.places(w)
		if ps == nil {
			return !self
		}
		return !self && slices.ContainsFunc(ps, func(p string) bool { return within(dir, p) || within(filepath.Dir(p), dir) })
	}
	ok := map[*syntax.Word]bool{} // words that may name the script and leave it as it is
	changed := false
	syntax.Walk(r.file, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.Assign:
			if n.Value != nil {
				ok[n.Value] = true
			}
		case *syntax.TestClause:
			syntax.Walk(n, func(m syntax.Node) bool {
				if w, isWord := m.(*syntax.Word); isWord {
					ok[w] = true
				}
				return true
			})
		case *syntax.Stmt:
			if ce, isCall := n.Cmd.(*syntax.CallExpr); isCall && len(ce.Args) > 0 {
				// a heredoc is text a command reads (cat > x.sh <<'EOF' … # x.sh usage …), unless code runs it
				if name, _ := cmdName(ce.Args[0]); !shells[name] && !interpreters[name] {
					for _, rd := range n.Redirs {
						if rd.Hdoc != nil {
							ok[rd.Hdoc] = true
						}
					}
				}
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
				ok[rd.Word] = true
				if fine[rd.Word] {
					continue
				}
				if l := literal(rd.Word); l.ok && devFiles[l.text] {
					continue
				}
				// a redirect writes the one file it names: the script when it is that file (by name, path or link)
				if _, known := rc.resolve(rd.Word); names(rd.Word) || !self && !known {
					changed = true
				}
			}
			ce, isCall := n.Cmd.(*syntax.CallExpr)
			if !isCall || len(ce.Args) == 0 {
				return true
			}
			name, known := cmdName(ce.Args[0])
			args := literalWords(ce.Args[1:])
			if k, sawC, _ := shellOpts(literalWords(ce.Args)); shells[name] && sawC && k < len(ce.Args) {
				t, known := rc.resolve(ce.Args[k])
				f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(t), "")
				if !known || err != nil {
					changed = !self
				} else {
					sub := (ctx{cwd: rc.cwd, moved: rc.moved, script: rc.script}).scan(f, t)
					changed = changesIn(sub.files[len(sub.files)-1], script,
						ctx{cwd: rc.cwd, vars: sub.vars, moved: sub.moved || rc.moved, script: rc.script}, self, own, fine)
				}
				if changed {
					return false
				}
			}
			if names(ce.Args[0]) {
				ok[ce.Args[0]] = true // the script run by its path
			}
			// after a cd that may fail the word is in either folder: it runs the script only when each place is the
			// script or holds no file (review r4: ./cpx.sh may be sub/cpx.sh, another program), or when its places are
			// the script and the other place of the word being judged (alt): then only one of the two ever runs
			ps := rc.places(ce.Args[0])
			isScript := func(p string) bool { return samePath(p, script) }
			runsIt := len(ps) > 0 && !slices.ContainsFunc(ps, func(p string) bool {
				_, err := os.Stat(p)
				return !isScript(p) && err == nil
			}) || rc.alt != "" && slices.ContainsFunc(ps, isScript) && slices.ContainsFunc(ps, func(p string) bool { return samePath(p, rc.alt) })
			// a program the guard does not know may write any file it names; the script run by its path is read on its
			// own, so its arguments are only its input (./rd.sh 7774, review r3)
			writes := !known && !runsIt || fileWriters[name] || name == "wget" || name == "curl" && curlWrites(args) || name == "sed" && !shownNoWrite(name, args) ||
				name == "find" && !shownNoWrite(name, args) || (name == "perl" || name == "ruby") && slices1(args, "-i") ||
				name == "git" && gitWritesIn(args) || name == "az" && azFileOpt(parseAz(args)) // az … download --path art
			switch {
			case writes:
				for _, w := range ce.Args[1:] {
					ok[w] = true
					at := w
					t, known := rc.resolve(w)
					if known && t == "" {
						continue // sed -i '' names no file
					}
					if known && strings.HasPrefix(t, "-") {
						_, v, eq := strings.Cut(t, "=")
						if !eq {
							continue
						}
						// --path=art writes art (review r3b)
						at = &syntax.Word{Parts: []syntax.WordPart{&syntax.Lit{ValuePos: w.Pos(), ValueEnd: w.End(), Value: v}}}
					}
					// tee, truncate, sponge and sed -i write the files they name, never a folder: only those files count
					if oneFile[name] && (names(at) || rc.places(at) == nil && !self) || !oneFile[name] && into(at) {
						changed = true
					}
				}
				if (name == "tar" || name == "unzip") && !slices1(args, "-C") && !self && (rc.moved || within(dir, rc.cwd)) {
					changed = true
				}
			case readCmds[name] || noFileCmds[name] || keepCmds[name] || readsOnly[name] || shells[name] || name == "az" ||
				name == "git" || name == "sed" || name == "find" || name == "sort" && shownNoWrite(name, args) ||
				self && !interpreters[name]:
				// in a script's own text a named command that writes no file it names leaves the script as it is
				// (lockf … bash -c '"$HOME/.crius/bin/bus-post" …' runs it); code of another language may write it
				for _, w := range ce.Args[1:] {
					ok[w] = true
				}
			}
		}
		return !changed
	})
	if changed {
		return true
	}
	syntax.Walk(r.file, func(n syntax.Node) bool {
		if w, isWord := n.(*syntax.Word); isWord && !changed && !ok[w] && !fine[w] && !r.scripts[w] && names(w) {
			changed = true
		}
		return !changed
	})
	return changed
}

// curlWrites says whether a curl call writes a file: -o, -O, --output, --remote-name, -D, --dump-header, -c,
// --cookie-jar, --trace, or a word it cannot read.
func curlWrites(args []word) bool {
	for _, a := range args {
		t := a.text
		if !a.ok || strings.HasPrefix(t, "--") && (strings.HasPrefix(t, "--output") || strings.HasPrefix(t, "--remote-name") ||
			strings.HasPrefix(t, "--dump-header") || strings.HasPrefix(t, "--cookie-jar") || strings.HasPrefix(t, "--trace") ||
			strings.HasPrefix(t, "--config")) ||
			len(t) > 1 && t[0] == '-' && t[1] != '-' && strings.ContainsAny(t, "oODcK") {
			return true
		}
	}
	return false
}

func slices1(args []word, prefix string) bool {
	for _, a := range args {
		if a.ok && strings.HasPrefix(a.text, prefix) {
			return true
		}
	}
	return false
}

// gitWritesIn says whether a git call may rewrite work-tree files: a subcommand in gitWrites, or one it cannot read.
func gitWritesIn(args []word) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !a.ok {
			return true
		}
		if strings.HasPrefix(a.text, "-") {
			if a.text == "-C" || a.text == "-c" || a.text == "--git-dir" || a.text == "--work-tree" {
				i++
			}
			continue
		}
		if gitWrites[a.text] {
			return true
		}
		if gitNoFile[a.text] {
			return false // 11-05: git cat-file -e $h has no option that writes a file
		}
		// log --output=x, diff --out x
		return slices.ContainsFunc(args[i+1:], func(w word) bool {
			return !w.ok || runsOption(w.text) && strings.HasPrefix("--output", strings.SplitN(w.text, "=", 2)[0])
		})
	}
	return false
}

// codeVarSet says whether text sets a variable that runs code or finds commands for what it starts (PATH, PS4, BASH_ENV,
// HOME, GIT_*, …: codeNameRe) in any way bash sets one (NAME=, export, declare, read, printf -v, mapfile, getopts, a for
// loop, ${NAME:=}, (( NAME=1 )), {NAME}>), sets a name it computes there, or exports a function or all names (set -a).
// The name inside quoted text (an echo's message) is no setting.
func codeVarSet(f *syntax.File, src string, late func(syntax.Node) bool) bool {
	if exportFnRe.MatchString(src) {
		return true
	}
	setters := map[string]bool{"read": true, "mapfile": true, "readarray": true, "getopts": true, "printf": true,
		"declare": true, "typeset": true, "local": true, "export": true, "readonly": true, "unset": false}
	found := false
	ifs := map[*syntax.Assign]bool{} // IFS= in front of one command changes only how that command splits words
	syntax.Walk(f, func(n syntax.Node) bool {
		if ce, ok := n.(*syntax.CallExpr); ok && len(ce.Args) > 0 {
			for _, a := range ce.Assigns {
				if a.Name != nil && a.Name.Value == "IFS" {
					ifs[a] = true
				}
			}
		}
		return true
	})
	syntax.Walk(f, func(n syntax.Node) bool {
		if n != nil && late(n) {
			return false
		}
		switch n := n.(type) {
		case *syntax.Assign:
			found = found || n.Name != nil && codeNameRe.MatchString(n.Name.Value) && !ifs[n]
		case *syntax.DeclClause:
			// declare "$1", export "$n=x": a name the guard cannot read
			for _, a := range n.Args {
				if a.Naked && a.Value != nil {
					l := literal(a.Value)
					v, _, _ := strings.Cut(l.text, "=")
					found = found || !l.ok && !fixedStart(a.Value) || l.ok && !strings.HasPrefix(l.text, "-") && codeNameRe.MatchString(v)
				}
			}
		case *syntax.ForClause:
			if wi, ok := n.Loop.(*syntax.WordIter); ok {
				found = found || codeNameRe.MatchString(wi.Name.Value)
			}
		case *syntax.ParamExp:
			found = found || n.Param != nil && codeNameRe.MatchString(n.Param.Value) && n.Exp != nil &&
				(n.Exp.Op == syntax.AssignUnset || n.Exp.Op == syntax.AssignUnsetOrNull)
		case *syntax.ArithmCmd, *syntax.ArithmExp:
			var b bytes.Buffer
			syntax.NewPrinter().Print(&b, n)
			found = found || codeNameWordRe.MatchString(b.String()) && strings.Contains(b.String(), "=")
		case *syntax.Redirect:
			found = found || n.N != nil && strings.HasPrefix(n.N.Value, "{") &&
				codeNameRe.MatchString(strings.Trim(n.N.Value, "{}"))
		case *syntax.CallExpr:
			if len(n.Args) == 0 {
				break
			}
			name, _ := cmdName(n.Args[0])
			if !setters[name] {
				break
			}
			found = setsCodeName(name, n.Args[1:])
		}
		return !found
	})
	return found
}

// valueOpts are the options of read, mapfile and printf that take a value that is no variable name.
var valueOpts = map[string]string{"read": "dinNptu", "mapfile": "dnOsuCc", "readarray": "dnOsuCc", "printf": ""}

// setsCodeName says whether a builtin call sets a codeNameRe variable or one whose name the guard cannot read: read and
// mapfile set their non-option words (and read -a's value), printf only -v's value, getopts its second word, declare,
// export and the like each NAME=.
func setsCodeName(name string, args []*syntax.Word) bool {
	bad := func(w *syntax.Word, text string) bool {
		l := literal(w)
		v, _, _ := strings.Cut(text, "=")
		return !l.ok && !fixedStart(w) || codeNameRe.MatchString(v)
	}
	if name == "getopts" {
		return len(args) > 1 && bad(args[1], literal(args[1]).text)
	}
	opts, takesValues := valueOpts[name]
	for i := 0; i < len(args); i++ {
		w := args[i]
		l := literal(w)
		if !takesValues {
			if bad(w, l.text) {
				return true
			}
			continue
		}
		if !l.ok && i > 0 && strings.HasPrefix(literal(args[i-1]).text, "-") && !strings.HasPrefix(literal(args[i-1]).text, "-v") {
			continue // a computed option value (read -p "$msg")
		}
		if l.ok && strings.HasPrefix(l.text, "-") && len(l.text) > 1 {
			flags := l.text[1:]
			switch {
			case name == "printf" && flags == "v":
				if i+1 < len(args) && bad(args[i+1], literal(args[i+1]).text) {
					return true
				}
				i++
			case name == "printf" && strings.HasPrefix(flags, "v"):
				if codeNameRe.MatchString(flags[1:]) {
					return true
				}
			case name == "read" && strings.HasSuffix(flags, "a"):
				if i+1 < len(args) && bad(args[i+1], literal(args[i+1]).text) {
					return true
				}
				i++
			case strings.ContainsAny(flags[len(flags)-1:], opts) && opts != "":
				i++
			}
			continue
		}
		if name == "printf" {
			return false // the format and its arguments
		}
		if bad(w, l.text) {
			return true
		}
	}
	return false
}

// handsCode says whether a command hands a script computed text that names az or Azure DevOps (in an argument or an
// assignment): a $(...) other than date or an az or HTTP read, a <(...), a `...`, a ${…} beyond $X, or quoted text with
// $, ` or \ (bash runs a $(...) in text a script reads as a number: (( n > 0 )) on its $1).
func handsCode(r runs) bool {
	var words []*syntax.Word
	late := afterRuns(r)
	syntax.Walk(r.file, func(n syntax.Node) bool {
		if n != nil && late(n) {
			return false
		}
		switch n := n.(type) {
		case *syntax.Assign:
			if n.Value != nil {
				words = append(words, n.Value)
			}
		case *syntax.CallExpr:
			for i, w := range n.Args {
				if r.scripts[w] || i > 0 && r.scripts[n.Args[i-1]] {
					// the words after a script run are its arguments
					words = append(words, n.Args[i:]...)
					break
				}
			}
		}
		return true
	})
	for _, w := range words {
		l := literal(w)
		if !adoRe.MatchString(l.src) && !adoRe.MatchString(l.text) {
			continue
		}
		bad := strings.ContainsAny(l.text, "$`\\")
		syntax.Walk(w, func(n syntax.Node) bool {
			switch n := n.(type) {
			case *syntax.CmdSubst:
				if !dateOnly(n) && !readsOnlySubst(n) {
					bad = true
				}
				return false
			case *syntax.ProcSubst, *syntax.ArithmExp:
				bad = true
			case *syntax.ParamExp:
				bad = bad || n.Exp != nil || n.Slice != nil || n.Repl != nil || n.Index != nil || n.Excl || n.Length || n.Names != 0
			case *syntax.SglQuoted:
				bad = bad || n.Dollar
			}
			return !bad
		})
		if bad {
			return true
		}
	}
	return false
}

// afterRuns gives a test for text that runs only after every program this text may start has started: it comes after
// the last word that runs a script and the last call that may run one (a shell, an interpreter, a wrapper, eval or
// source, a command name the guard cannot read), outside any loop, in a text with no function. Nothing set there reaches
// a program already running (live 11-05 21:56: pass-post.sh sets b=$(az … | tr …) and IFS in a $(…) after bun
// verdict-check.ts ran).
func afterRuns(r runs) func(syntax.Node) bool {
	var runs []syntax.Node
	for w := range r.scripts {
		runs = append(runs, w)
	}
	var loops, funcs [][2]uint
	syntax.Walk(r.file, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.ForClause, *syntax.WhileClause:
			loops = append(loops, [2]uint{n.Pos().Offset(), n.End().Offset()})
		case *syntax.FuncDecl:
			funcs = append(funcs, [2]uint{n.Pos().Offset(), n.End().Offset()})
		case *syntax.CallExpr:
			if len(n.Args) == 0 {
				break
			}
			name, ok := cmdName(n.Args[0])
			if !ok || shells[name] || interpreters[name] || wrappers[name] && !findReadsOnly(name, n.Args) || runBuiltins[name] {
				runs = append(runs, n)
			}
		}
		return true
	})
	in := func(at uint, spans [][2]uint) bool {
		return slices.ContainsFunc(spans, func(s [2]uint) bool { return at >= s[0] && at < s[1] })
	}
	var last uint
	inFunc := false        // a function that runs a program may run it after any later line (no() { echo; exit; } runs none)
	var runLoops [][2]uint // a loop that runs a program runs it again after a later line in it
	for _, n := range runs {
		at := n.Pos().Offset()
		last = max(last, n.End().Offset())
		inFunc = inFunc || in(at, funcs)
		for _, l := range loops {
			if in(at, [][2]uint{l}) {
				runLoops = append(runLoops, l)
			}
		}
	}
	return func(n syntax.Node) bool {
		at := n.Pos().Offset()
		return last > 0 && !inFunc && at > last && !in(at, runLoops)
	}
}

// runBuiltins run text or a file in this shell, or replace it.
var runBuiltins = map[string]bool{"eval": true, "source": true, ".": true, "builtin": true, "command": true, "exec": true,
	"trap": true, "enable": true}

// findReadsOnly says whether a find call runs only commands that read (find … -exec grep … {} +).
func findReadsOnly(name string, args []*syntax.Word) bool {
	if name != "find" {
		return false
	}
	for i, w := range args {
		if t := literal(w).text; t == "-exec" || t == "-execdir" || t == "-ok" || t == "-okdir" {
			if i+1 >= len(args) {
				return false
			}
			if n, ok := cmdName(args[i+1]); !ok || !readCmds[n] {
				return false
			}
		}
	}
	return true
}

// readsOnlySubst says whether every command in a $(...) is an az or HTTP call that only reads (a sha or an id that goes
// to a script: bash b13-parents.sh 7740 $(az repos pr show … --query lastMergeSourceCommit.commitId -o tsv)).
func readsOnlySubst(cs *syntax.CmdSubst) bool {
	return len(cs.Stmts) > 0 && readsOnlyIn(cs)
}

func readsOnlyIn(node syntax.Node) bool {
	only := true
	syntax.Walk(node, func(n syntax.Node) bool {
		if ce, ok := n.(*syntax.CallExpr); ok && len(ce.Args) > 0 {
			name, _ := cmdName(ce.Args[0])
			args := literalWords(ce.Args[1:])
			if shells[name] && len(args) == 2 && args[0].text == "-c" {
				// bash -c "az pipelines build list … $n …": its $n reads as it is in the text
				text := args[1].text
				if dq, isDq := ce.Args[2].Parts[0].(*syntax.DblQuoted); !args[1].ok && len(ce.Args[2].Parts) == 1 && isDq {
					text = strings.TrimSuffix(strings.TrimPrefix(literal(&syntax.Word{Parts: []syntax.WordPart{dq}}).src, "\""), "\"")
				} else if !args[1].ok {
					only = false
					return false
				}
				f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(text), "")
				only = only && err == nil && readsOnlyIn(f)
				return false
			}
			// tr, printf, echo -e, cut and sed can make a $ from text with none (review of 260216b)
			only = only && (readCall(ce.Args) || name == "head" || name == "tail" || name == "wc" || name == "sort" ||
				name == "uniq" || name == "jq" || name == "tr" && len(args) > 0 && args[0].text == "-d")
		}
		return only
	})
	return only
}

// scriptDirRe is a $(...) that gives the folder of the running script: $(dirname "$0"), $(cd "$(dirname "$0")" && pwd),
// also of $(realpath "$0") or $(readlink -f "$0") (review r5: source "$(dirname "$(realpath "$0")")/lib/ado.sh").
var scriptDirRe = func() *regexp.Regexp {
	self := `\$(\{?0\}?|\{BASH_SOURCE(\[0\])?\})`
	arg := `"?(` + self + `|\$\((g?realpath|readlink\s+-f)\s+"?` + self + `"?\))"?`
	dir := `dirname\s+` + arg
	return regexp.MustCompile(`^\$\((cd\s+"?\$\(` + dir + `\)"?\s*(&&|;)\s*pwd(\s+-P)?|` + dir + `)\)$`)
}()

// scriptDirOf gives the running script's folder for a $(...) that scriptDirRe matches.
func (c ctx) scriptDirOf(cs *syntax.CmdSubst) (string, bool) {
	if c.script == "" {
		return "", false
	}
	var b bytes.Buffer
	syntax.NewPrinter().Print(&b, cs)
	t := strings.TrimSpace(b.String())
	if !scriptDirRe.MatchString(t) {
		return "", false
	}
	if strings.Contains(t, "realpath") || strings.Contains(t, "readlink") {
		if r, err := filepath.EvalSymlinks(c.script); err == nil {
			return path.Dir(r), true
		}
	}
	return path.Dir(c.script), true
}
