package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"mvdan.cc/sh/v3/syntax"
)

// The content checks (owner 11-08): a post goes on when its text passes them, and is refused with the reason when one
// fails, so the session fixes the text and tries again.

var (
	// modelRe is a model or tool name in text people read (the Quotes team's pr-text-check.sh list, as words).
	modelRe = regexp.MustCompile(`(?i)\b(opus|sonnet|codex|claude|anthropic|openai)\b|\b(chat)?gpt|\busage limit`)
	// workItemRe is a work item mention in PR text.
	workItemRe = regexp.MustCompile(`AB#[0-9]+`)
	// textOpts are the az options whose value is text people read.
	textOpts = []string{"--description", "-d", "--title", "--discussion", "--comment", "--content", "--text", "--message"}
	// teamRules: a PR description from a team's folders (under the home folder) or for its repository (by name or id)
	// names the team's work item and no other, and the PR links no other (Quotes: TEAM.md "one work item only,
	// AB#96365", pr-text-check.sh rc 5).
	teamRules = []struct {
		dirs               []string
		repo, repoID, item string
	}{
		{[]string{"Projects/Appxite/svc_quote", ".crius/state/quotes"}, "svc_quote", "d88331ee-60dc-4f15-b236-b1790c6fdeaa", "AB#96365"},
	}
	// pathOpts are options whose value is a path, branch, URL, id or query rather than text people read.
	pathOpts = map[string][]string{
		"az": {"--org", "--organization", "--url", "--uri", "-u", "--in-file", "--file", "--file-path", "--source-branch", "-s",
			"--target-branch", "-t", "--branch", "--path", "--repository", "-r", "--project", "-p", "-o", "--output", "--query",
			"--id", "--ids", "--pipeline-ids", "--route-parameters", "--query-parameters"},
		"curl": {"-o", "--output", "-K", "--config", "--cacert", "--cert", "--key", "-T", "--upload-file", "-u", "--user"},
		"wget": {"-O", "--output-document", "-o", "--output-file", "--ca-certificate", "--certificate", "--private-key"},
	}
)

// maxPRDescription is the most characters Azure DevOps keeps in a PR description.
const maxPRDescription = 4000

// catWord gives FILE for a word that is "$(cat FILE)" in double quotes, alone or after a literal --option=, and the
// literal before it: the shell gives az the file's text as one word, less its last newlines. Unquoted, the text would
// split into several words (and could hold options), so it does not count.
func catWord(w *syntax.Word) (prefix, file string, ok bool) {
	if w == nil {
		return "", "", false
	}
	parts := w.Parts
	if len(parts) == 2 {
		lit, isLit := parts[0].(*syntax.Lit)
		if !isLit || !strings.HasPrefix(lit.Value, "-") || !strings.HasSuffix(lit.Value, "=") {
			return "", "", false
		}
		prefix, parts = lit.Value, parts[1:]
	}
	if len(parts) != 1 {
		return "", "", false
	}
	dq, isDq := parts[0].(*syntax.DblQuoted)
	if !isDq || len(dq.Parts) != 1 {
		return "", "", false
	}
	cs, isCs := dq.Parts[0].(*syntax.CmdSubst)
	if !isCs || len(cs.Stmts) != 1 {
		return "", "", false
	}
	st := cs.Stmts[0]
	ce, isCe := st.Cmd.(*syntax.CallExpr)
	if !isCe || len(st.Redirs) != 0 || st.Background || st.Coprocess || st.Negated || len(ce.Assigns) != 0 || len(ce.Args) != 2 {
		return "", "", false
	}
	if c := literal(ce.Args[0]); !c.ok || c.text != "cat" {
		return "", "", false
	}
	f := literal(ce.Args[1])
	// the shell globs, brace-expands, expands ~user and (zsh) =cmd to a program's path in the name; the guard reads it as
	// written, so such a name is not read (reviews of 07f23bd P1-3 and 7bb778c P1). Only a quoted or plain name passes.
	if !f.ok || f.text == "" || strings.HasPrefix(f.text, "-") || !asWritten(ce.Args[1]) || strings.ContainsAny(f.text, "{") ||
		strings.HasPrefix(f.text, "~") && !strings.HasPrefix(f.text, "~/") || strings.HasPrefix(f.text, "=") && isLitStart(ce.Args[1]) {
		return "", "", false
	}
	return prefix, f.text, true
}

// isLitStart says whether a word starts with unquoted text (where zsh expands a leading = or ~).
func isLitStart(w *syntax.Word) bool {
	if len(w.Parts) == 0 {
		return false
	}
	_, lit := w.Parts[0].(*syntax.Lit)
	return lit
}

// resolve gives the post's words with each "$(cat FILE)" word read as FILE's text; why says why a file cannot be read.
// Other words the guard cannot read stay as they are (hardCheck refuses them).
func (p postCall) resolve() ([]word, string) {
	out := make([]word, 0, len(p.args))
	alone := catShape(p.c.whole)
	for _, a := range p.args {
		prefix, file, ok := "", "", false
		if !a.ok {
			prefix, file, ok = catWord(a.node)
		}
		if !ok {
			out = append(out, a)
			continue
		}
		if !alone {
			return nil, p.tool + ": the guard reads \"$(cat FILE)\" (" + a.src + ") only in a command that is the post " +
				"alone, or cd DIR && the post, with plain words; pass @FILE instead (az reads it itself)"
		}
		body, why := p.readBody(file)
		if why != "" {
			return nil, p.tool + ": " + why + rewrite
		}
		if strings.TrimSpace(body) == "" {
			return nil, p.tool + ": " + file + " is empty (" + a.src + ")"
		}
		out = append(out, word{text: prefix + strings.TrimRight(body, "\n"), src: a.src, ok: true, node: a.node})
	}
	return out, ""
}

// catShape says whether a command text is one post alone, or cd DIR && the post, whose words are all plain or
// "$(cat FILE)": only then does "$(cat FILE)" give FILE's text as the guard reads it. Anything else before or around the
// post (zsh's path=(…), functions[cat]=…, hash cat=/x, a glob qualifier *(e:…:) that runs code, a script around it) may
// make cat another program, and a list of such forms is never complete (reviews of 1a2069f and 3a9092d: the same cause
// twice), so the shape is what is checked.
func catShape(text string) bool {
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(text), "")
	if err != nil || len(f.Stmts) != 1 {
		return false
	}
	plainStmt := func(st *syntax.Stmt) (*syntax.CallExpr, bool) {
		if st == nil || st.Background || st.Coprocess || st.Negated {
			return nil, false
		}
		for _, r := range st.Redirs {
			if r.Word != nil && !asWritten(r.Word) || r.Hdoc != nil {
				return nil, false
			}
		}
		ce, ok := st.Cmd.(*syntax.CallExpr)
		if !ok || len(ce.Assigns) != 0 || len(ce.Args) == 0 {
			return nil, false
		}
		return ce, true
	}
	st := f.Stmts[0]
	if b, ok := st.Cmd.(*syntax.BinaryCmd); ok && b.Op == syntax.AndStmt && !st.Background && !st.Negated && len(st.Redirs) == 0 {
		cd, ok := plainStmt(b.X)
		if !ok || len(cd.Args) != 2 || !asWritten(cd.Args[0]) || !asWritten(cd.Args[1]) || literal(cd.Args[0]).text != "cd" {
			return false
		}
		st = b.Y
	}
	ce, ok := plainStmt(st)
	if !ok {
		return false
	}
	for _, w := range ce.Args {
		if _, _, cat := catWord(w); !cat && !asWritten(w) { // catWord takes only a file name with no glob or ~user
			return false
		}
	}
	return true
}

// asWritten says whether a word is text the shell passes as written: literal parts with no glob or zsh qualifier, and
// quotes that hold only literal text.
func asWritten(w *syntax.Word) bool {
	for _, p := range w.Parts {
		switch p := p.(type) {
		case *syntax.Lit:
			if strings.ContainsAny(p.Value, "*?[]()") {
				return false
			}
		case *syntax.SglQuoted:
			if p.Dollar {
				return false
			}
		case *syntax.DblQuoted:
			for _, q := range p.Parts {
				if _, lit := q.(*syntax.Lit); !lit {
					return false
				}
			}
		default:
			return false
		}
	}
	return true
}

// notText says whether a word of the tool's is not text people read: an @file (its text is read as a file), a URL, or
// the value of an option that takes a path, branch, URL, id or query (after opt, the option word before it, if any).
// A wiki page's --path is its title, so it is text (review of 1a2069f P2).
func notText(tool, opt, s string, wiki bool) bool {
	if strings.HasPrefix(s, "@") || strings.Contains(s, "://") && !strings.ContainsAny(s, " \t\r\n") {
		return true
	}
	o := opt
	if strings.HasPrefix(o, "--") {
		o = strings.ToLower(o) // short options keep their case: wget -O is a path, -o a log file
	}
	if wiki && (o == "--path" || o == "-p") {
		return false
	}
	return o != "" && slices.Contains(pathOpts[tool], o)
}

// contentCheck gives why the text of one post (its words resolved, hardCheck passed) may not go on, or "".
func contentCheck(p postCall, in input) string {
	what := p.tool
	var read []string // the text people read: every word but options and paths, and the files the post sends
	wiki := p.tool == "az" && strings.HasPrefix(parseAz(p.args).cmd, "devops wiki")
	prev := ""
	for _, a := range p.args {
		t, opt := a.text, prev
		prev = ""
		if strings.HasPrefix(t, "-") && strings.ContainsAny(t, " \t\r\n") {
			read = append(read, t) // az's argparse takes "-Opus says" or "--Claude says" as a value (review of 7bb778c P2)
		}
		if strings.HasPrefix(t, "-") {
			o, v, eq := strings.Cut(t, "=")
			switch {
			case !strings.HasPrefix(t, "--") && len(t) > 2:
				// a value glued to a short option (-d"Opus: LGTM (score=9)", curl -d'{…}'), or behind joined flags
				// (curl -sdOpus): each tail after the first letters may be the value (reviews of 1a2069f and 3a9092d)
				for i := 3; i < len(t) && !notText(p.tool, t[:2], t, wiki); i++ {
					read = append(read, t[i:]) // no cap (-vvvvvvvvdClaude); not for a path option (-sfeature/codex-fix)
				}
				t, opt = t[2:], t[:2]
			case eq:
				t, opt = v, o
			default:
				prev = t
				continue
			}
		}
		if !notText(p.tool, opt, t, wiki) {
			read = append(read, t)
		}
	}
	for _, s := range p.src {
		body, why := p.readBody(s.file)
		if why != "" {
			return what + ": " + why + rewrite
		}
		if strings.TrimSpace(body) == "" {
			return what + ": " + s.file + " is empty"
		}
		read = append(read, body)
	}
	all := strings.Join(read, "\n")
	if m := modelRe.FindString(all + "\n" + jsonDecoded(all)); m != "" {
		return what + ": the text names a model or tool (" + m + "); write \"Deep review: <findings>\" and leave the name out"
	}
	if p.tool != "az" {
		return ""
	}
	o := parseAz(p.args)
	what = "az " + o.cmd
	texts := map[string]string{}
	for _, k := range textOpts {
		vals, set := o.opts[k]
		if !set {
			continue
		}
		var parts []string
		for _, v := range vals {
			if f, at := azAtFile(v.text); at {
				body, why := p.readBody(f)
				if why != "" {
					return what + " " + k + ": " + why + rewrite
				}
				parts = append(parts, body)
				continue
			}
			parts = append(parts, v.text)
		}
		texts[k] = strings.Join(parts, "\n")
		if strings.TrimSpace(texts[k]) == "" {
			return what + " " + k + " is empty: write the text, or leave the option out"
		}
	}
	repo := ""
	if id, set := o.last("--id"); set && (o.cmd == "repos pr update" || o.cmd == "repos pr work-item add") {
		if pr, err := prState(id.text); err == nil {
			repo = pr.Repo
		}
	}
	item, team := teamItem(o, repo, p.c.cwd, in.Cwd)
	if team != "" && (o.cmd == "repos pr create" || o.cmd == "repos pr work-item add") {
		for _, v := range o.opts["--work-items"] {
			for _, id := range strings.Fields(v.text) {
				if "AB#"+strings.TrimPrefix(id, "#") != item {
					return what + ": a " + team + " PR links its work item " + strings.TrimPrefix(item, "AB#") +
						" and no other (--work-items " + v.text + ")"
				}
			}
		}
	}
	if o.cmd != "repos pr create" && o.cmd != "repos pr update" {
		return ""
	}
	desc, set := texts["--description"]
	if d, dset := texts["-d"]; dset {
		desc, set = desc+d, true
	}
	if !set {
		return ""
	}
	if n := utf8.RuneCountInString(desc); n > maxPRDescription {
		return what + ": the description is " + strconv.Itoa(n) + " characters; Azure DevOps keeps " +
			strconv.Itoa(maxPRDescription) + ": trim it"
	}
	if team != "" {
		var ids []string
		for _, id := range workItemRe.FindAllString(desc, -1) {
			if !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
		if len(ids) != 1 || ids[0] != item {
			return what + ": a " + team + " PR description names its work item " + item + " and no other (it names " +
				strconv.Quote(strings.Join(ids, " ")) + ")"
		}
	}
	return ""
}

// teamItem gives the work item a PR's team requires and the team's repository, when the PR is for a team's repository
// (--repository, or the repository of the PR it changes, by name or id) or the call runs in one of the team's folders;
// else "", "".
func teamItem(o azOpts, prRepo string, dirs ...string) (item, team string) {
	home, _ := os.UserHomeDir()
	repos := []string{prRepo}
	if r, set := o.last("--repository", "-r"); set {
		repos = append(repos, r.text)
	}
	for _, r := range teamRules {
		for _, name := range repos {
			if name != "" && (strings.EqualFold(name, r.repo) || strings.EqualFold(name, r.repoID)) {
				return r.item, r.repo
			}
		}
		for _, d := range dirs {
			for _, td := range r.dirs {
				if full := filepath.Join(home, td); home != "" && d != "" && (d == full || strings.HasPrefix(d, full+"/")) {
					return r.item, r.repo
				}
			}
		}
	}
	return "", ""
}
