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
	// teamRules: a PR description from a team's folders (under the home folder) or for its repository names the team's
	// work item and no other (Quotes: TEAM.md "one work item only, AB#96365", pr-text-check.sh rc 5).
	teamRules = []struct {
		dirs       []string
		repo, item string
	}{
		{[]string{"Projects/Appxite/svc_quote", ".crius/state/quotes"}, "svc_quote", "AB#96365"},
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
	if !f.ok || f.text == "" || strings.HasPrefix(f.text, "-") {
		return "", "", false
	}
	return prefix, f.text, true
}

// resolve gives the post's words with each "$(cat FILE)" word read as FILE's text; why says why a file cannot be read.
// Other words the guard cannot read stay as they are (hardCheck refuses them).
func (p postCall) resolve() ([]word, string) {
	out := make([]word, 0, len(p.args))
	for _, a := range p.args {
		prefix, file, ok := "", "", false
		if !a.ok {
			prefix, file, ok = catWord(a.node)
		}
		if !ok {
			out = append(out, a)
			continue
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

// pathLike says whether a word is a path, a branch or a URL rather than text people read: one word with a slash.
func pathLike(s string) bool {
	return strings.Contains(s, "/") && !strings.ContainsAny(s, " \t\r\n")
}

// contentCheck gives why the text of one post (its words resolved, hardCheck passed) may not go on, or "".
func contentCheck(p postCall, in input) string {
	what := p.tool
	var read []string // the text people read: words that are not options or paths, and the files the post sends
	for _, a := range p.args {
		t := a.text
		if strings.HasPrefix(t, "-") {
			_, v, eq := strings.Cut(t, "=")
			if !eq {
				continue
			}
			t = v
		}
		if !pathLike(t) {
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
	if item, team := teamItem(o, p.c.cwd, in.Cwd); team != "" {
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
// (--repository) or the call runs in one of the team's folders; else "", "".
func teamItem(o azOpts, dirs ...string) (item, team string) {
	home, _ := os.UserHomeDir()
	repo, rset := o.last("--repository", "-r")
	for _, r := range teamRules {
		if rset && strings.EqualFold(repo.text, r.repo) {
			return r.item, r.repo
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
