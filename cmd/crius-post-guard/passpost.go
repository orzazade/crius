package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// The Quotes QA's pass-post.sh (Crius 11-06) checks a PASS verdict and appends one DONE line to BUILD-LOG.md; its ADO
// calls are GETs (az pipelines runs show, az repos pr show, and verdict-check.ts's az repos pr show), but it runs
// verdict-check.ts with bun, so every call asked as a script that may change before it runs. It passes as this one line:
//
//	cd ~/.crius/state/quotes/build/qa/tools && SENDER=<qa-*> TO='<names>' ./pass-post.sh [--check] <digits> <path> <path>
//
// optionally with `2>&1 | tail -N` and then `&& echo <text> >> ../../notes/<name>.md`. The echo text is plain words and
// quoted text; the whole line has no $, backtick, backslash, line break or # outside quotes, so bash's parser here and
// the zsh that runs it read the same line. At hook time pass-post.sh and verdict-check.ts must have a SHA-256 the pin
// file lists; qa/tools must hold no bunfig.toml or .env file (bun reads them in its folder: a .env PATH would swap the az
// that verdict-check.ts runs); no global bunfig, BUN_OPTIONS, BASH_ENV or exported bash function; the notes file is
// one safeTarget accepts. Known gaps: those of adoexact.go (zsh snapshot, PATH for bash, bun and az, files changed
// after the hook).

var (
	ppSenderRe = regexp.MustCompile(`^qa-[a-z0-9-]+$`)
	ppToRe     = regexp.MustCompile(`^[A-Za-z0-9_-]+(, ?[A-Za-z0-9_-]+)+$`)
	ppEchoRe   = regexp.MustCompile(`^[A-Za-z0-9._/:,+@-]+$`)
	ppNoteRe   = regexp.MustCompile(`^[A-Za-z0-9._-]+\.md$`)
	ppTailRe   = regexp.MustCompile(`^-[0-9]{1,6}$`)
)

// passPostExact is adoExact's twin for pass-post.sh: ok when command is the shape; why is "" when it passes, or the
// ask when a hook-time check fails.
func passPostExact(command, sid string) (ok bool, why string) {
	if strings.ContainsAny(command, "$`\\\n\r") {
		return false, ""
	}
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil || len(f.Stmts) != 1 {
		return false, ""
	}
	parts, good := exactAndChain(f.Stmts[0])
	if !good || len(parts) < 2 || len(parts) > 3 {
		return false, ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false, ""
	}
	build := filepath.Join(home, ".crius/state/quotes/build")
	tools := filepath.Join(build, "qa/tools")
	quotedHashes := 0
	// cd <qa/tools>
	if args, good := plainCall(parts[0], home); !good || len(args) != 2 || args[0] != "cd" || args[1] != tools {
		return false, ""
	}
	// SENDER=<qa-*> TO='<names>' ./pass-post.sh [--check] <digits> <path> <path> [2>&1] [| tail -N]
	calls, good := pipeline(parts[1])
	if !good || len(calls) > 2 || len(calls) == 2 && !tailN(calls[1]) {
		return false, ""
	}
	ce := calls[0].Cmd.(*syntax.CallExpr)
	if target, good := redirects(calls[0], home); !good || target != "" || len(ce.Assigns) != 2 {
		return false, ""
	}
	seen := map[string]bool{}
	for _, a := range ce.Assigns {
		if a.Append || a.Naked || a.Index != nil || a.Array != nil || seen[a.Name.Value] {
			return false, ""
		}
		seen[a.Name.Value] = true
		switch a.Name.Value {
		case "SENDER":
			if v, good := plainWord(a.Value, ""); !good || !ppSenderRe.MatchString(v) {
				return false, ""
			}
		case "TO":
			v, n, good := quotedText(a.Value)
			if !good || !ppToRe.MatchString(v) {
				return false, ""
			}
			quotedHashes += n
		default:
			return false, ""
		}
	}
	args := make([]string, len(ce.Args))
	for i, w := range ce.Args {
		if args[i], good = plainWord(w, home); !good {
			return false, ""
		}
	}
	// ./pass-post.sh [--check] <pr> <verdict> <message>
	if len(args) == 5 && args[1] == "--check" {
		args = append(args[:1], args[2:]...)
	}
	if len(args) != 4 || args[0] != "./pass-post.sh" || !prIDRe.MatchString(args[1]) ||
		strings.HasPrefix(args[2], "-") || strings.HasPrefix(args[3], "-") {
		return false, ""
	}
	// echo <text> >> ../../notes/<name>.md
	note := ""
	if len(parts) == 3 {
		st := parts[2]
		ce, isCall := st.Cmd.(*syntax.CallExpr)
		if !isCall || len(ce.Assigns) > 0 || len(ce.Args) < 1 || len(st.Redirs) != 1 {
			return false, ""
		}
		if n, good := plainWord(ce.Args[0], ""); !good || n != "echo" {
			return false, ""
		}
		for _, w := range ce.Args[1:] {
			n, good := echoWord(w)
			if !good {
				return false, ""
			}
			quotedHashes += n
		}
		r := st.Redirs[0]
		w, good := plainWord(r.Word, home)
		if !good || r.Op != syntax.AppOut || r.N != nil && r.N.Value != "1" {
			return false, ""
		}
		if !filepath.IsAbs(w) {
			w = filepath.Join(tools, w)
		}
		if filepath.Dir(w) != filepath.Join(build, "notes") || !ppNoteRe.MatchString(filepath.Base(w)) {
			return false, ""
		}
		note = w
	}
	// a # outside quotes may start a comment in one shell and not the other
	if strings.Count(command, "#") != quotedHashes {
		return false, ""
	}
	// The shape is exact; now what it relies on must hold at hook time.
	for _, p := range [][2]string{{filepath.Join(tools, "pass-post.sh"), "pass-post.sh"}, {filepath.Join(tools, "verdict-check.ts"), "verdict-check.ts"}} {
		if why := pinned(p[0], p[1]); why != "" {
			return true, why
		}
	}
	ents, err := os.ReadDir(tools)
	if err != nil {
		return true, asked("pass-post.sh: the guard cannot list " + tools)
	}
	for _, e := range ents {
		if n := strings.ToLower(e.Name()); n == "bunfig.toml" || strings.HasPrefix(n, ".env") {
			return true, asked("pass-post.sh: qa/tools holds " + e.Name() + ", which bun reads when verdict-check.ts runs there")
		}
	}
	if why := bunEnv(home, "pass-post.sh"); why != "" {
		return true, why
	}
	if why := bashEnv("pass-post.sh"); why != "" {
		return true, why
	}
	if note != "" {
		if why := safeTarget(note, home, sid); why != "" {
			return true, why
		}
	}
	return true, ""
}

// exactAndChain gives the statements of a chain joined by && (a single statement is a chain of one); none may redirect as
// a whole or be negated or in the background.
func exactAndChain(st *syntax.Stmt) ([]*syntax.Stmt, bool) {
	if !plainStmt(st) {
		return nil, false
	}
	b, isBin := st.Cmd.(*syntax.BinaryCmd)
	if !isBin || b.Op != syntax.AndStmt {
		return []*syntax.Stmt{st}, true
	}
	if len(st.Redirs) > 0 {
		return nil, false
	}
	l, ok := exactAndChain(b.X)
	if !ok {
		return nil, false
	}
	r, ok := exactAndChain(b.Y)
	return append(l, r...), ok
}

// plainCall gives the words of a call with no assignment or redirect, each a plain word.
func plainCall(st *syntax.Stmt, home string) ([]string, bool) {
	ce, isCall := st.Cmd.(*syntax.CallExpr)
	if !isCall || len(st.Redirs) > 0 || len(ce.Assigns) > 0 {
		return nil, false
	}
	args := make([]string, len(ce.Args))
	for i, w := range ce.Args {
		var good bool
		if args[i], good = plainWord(w, home); !good {
			return nil, false
		}
	}
	return args, true
}

// tailN is `tail -N` with nothing else.
func tailN(st *syntax.Stmt) bool {
	args, good := plainCall(st, "")
	return good && len(args) == 2 && args[0] == "tail" && ppTailRe.MatchString(args[1])
}

// quotedText is a word of one part: a plain literal, '…', or "…" with no expansion; it gives the text and its # count.
func quotedText(w *syntax.Word) (string, int, bool) {
	if w == nil || len(w.Parts) != 1 {
		return "", 0, false
	}
	switch p := w.Parts[0].(type) {
	case *syntax.Lit:
		return p.Value, 0, exactWordRe.MatchString(p.Value)
	case *syntax.SglQuoted:
		return p.Value, strings.Count(p.Value, "#"), !p.Dollar
	case *syntax.DblQuoted:
		if p.Dollar || len(p.Parts) > 1 {
			return "", 0, false
		}
		if len(p.Parts) == 0 {
			return "", 0, true
		}
		l, isLit := p.Parts[0].(*syntax.Lit)
		if !isLit {
			return "", 0, false
		}
		return l.Value, strings.Count(l.Value, "#"), true
	}
	return "", 0, false
}

// echoWord is an echo argument made of plain literals and quoted text with no expansion; it gives its # count.
func echoWord(w *syntax.Word) (int, bool) {
	n := 0
	for _, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			if !ppEchoRe.MatchString(p.Value) {
				return 0, false
			}
		case *syntax.SglQuoted, *syntax.DblQuoted:
			_, k, good := quotedText(&syntax.Word{Parts: []syntax.WordPart{p}})
			if !good {
				return 0, false
			}
			n += k
		default:
			return 0, false
		}
	}
	return n, len(w.Parts) > 0
}
