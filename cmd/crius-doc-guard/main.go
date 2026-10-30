// Command crius-doc-guard is a Claude Code PreToolUse hook for Write and Bash, in both personas and their subagents.
// It refuses a second document on a topic that already has one (~/.agents/AGENTS.md, "One document per topic"; the
// owner approved the hook on 10-30). Agents kept writing a new doc for each new angle instead of rewriting the old
// one: crius docs/notes holds COST-STUDY-2026-10-18, COST-STUDY-2026-10-25-A and -B, COST-LEVERS-STATUS and
// COST-LEVER3-FRESH.
//
// A call is refused when it creates a .md or .txt file that does not exist yet and the same folder already holds a
// .md or .txt doc on the same topic. The topic stem is the name in lower case, less dates (2026-10-30, 20261030) with
// a time or copy letter after them (2026-10-25-A), version words (v2) and the words fresh, new, final, draft and
// updated. A last word that counts (-2, -B, 1030) marks a copy only beside the doc without it (BRIEF-2 beside BRIEF);
// beside each other they are a numbered series (sprint-10, sprint-11). A name that is only a date has no topic and
// passes. Folders that hold series on purpose (tmp, scratch, script output, evidence, vendor; see seriesDirs) pass.
// On the 10-30 scan of 67,221 docs the guard would have refused 31, each a copy of one doc by name.
//
// It sees the Write tool and, in Bash, output redirections (> >> &> >| >& <>, not 2>), tee and cp (to a file, a
// folder or -t DIR) to a path it can read without running anything. It answers "deny", never "ask": this steers agents and must not interrupt the owner. It is not a
// safety boundary, so input it cannot read lets the call go on, said on stderr.
//
// Known gaps: a doc in another folder of the same repo; a topic renamed in other words (COST-STUDY vs SPEND-REVIEW);
// a shared first word (COST-STUDY vs COST-LEVERS) is not a match: on the scan it would refuse 5,770 docs, mostly
// distinct topics (BRIEF-owner-queue beside BRIEF-build); copy, old, latest and revised are not copy words; two
// dashed dates in one name (x-2026-10-30-2026-10-31) stay in the stem; letters outside a-z split a name into
// fragments; Bash paths from variables or command output, relative paths after a cd that may not run (see
// checkBash), wrappers (sudo, env, xargs, bash -c), mv, install, dd, sed -i, script files and inline code in other
// languages; MultiEdit, NotebookEdit and MCP tools that write files; an appended "update" section in an existing doc
// (an edit, out of scope).
// Output: nothing (the call goes on) or one JSON line with permissionDecision "deny".
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

type input struct {
	ToolName  string `json:"tool_name"`
	Cwd       string `json:"cwd"`
	ToolInput struct {
		FilePath string `json:"file_path"`
		Command  string `json:"command"`
	} `json:"tool_input"`
}

func main() {
	// A panic exits 2, which blocks the call; this guard lets the call go on when it fails.
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintln(os.Stderr, "crius-doc-guard: failed, the call goes on:", r)
			os.Exit(0)
		}
	}()
	if err := run(os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "crius-doc-guard:", err)
	}
}

// logw takes the guard's notes on what it could not check; run points it at its error writer.
var logw io.Writer = os.Stderr

// run reads the hook input and writes a deny when the call makes a second doc on a topic; it writes nothing otherwise.
func run(r io.Reader, w, errw io.Writer) error {
	logw = errw
	var in input
	if err := json.NewDecoder(r).Decode(&in); err != nil {
		fmt.Fprintln(errw, "crius-doc-guard: unreadable hook input, the call goes on:", err)
		return nil
	}
	why := check(in)
	if why == "" {
		return nil
	}
	var out struct {
		HookSpecificOutput struct {
			HookEventName            string `json:"hookEventName"`
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	out.HookSpecificOutput.HookEventName = "PreToolUse"
	out.HookSpecificOutput.PermissionDecision = "deny"
	out.HookSpecificOutput.PermissionDecisionReason = why
	return json.NewEncoder(w).Encode(out)
}

func check(in input) string {
	switch in.ToolName {
	case "Write":
		return checkPath(in.ToolInput.FilePath)
	case "Bash":
		return checkBash(in.ToolInput.Command, in.Cwd)
	}
	return ""
}

func isDoc(p string) bool {
	ext := strings.ToLower(filepath.Ext(p))
	return ext == ".md" || ext == ".txt"
}

// checkPath says why creating p would make a second doc on a topic, or "" when it would not.
func checkPath(p string) string {
	if !isDoc(p) || !filepath.IsAbs(p) {
		return ""
	}
	p = filepath.Clean(p)
	if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
		return "" // it exists (a rewrite in place), or the guard cannot tell
	}
	dir := filepath.Dir(p)
	if seriesDir(dir) {
		return ""
	}
	stem := topicStem(filepath.Base(p))
	if stem == "" {
		return ""
	}
	if same := sameTopic(dir, filepath.Base(p), stem); same != "" {
		return "A doc on this topic exists: " + same + ". Rewrite it in place (rule: one document per topic). " +
			"If this really is a new topic, pick a name that says what is different."
	}
	return ""
}

// sameTopic gives the first doc in dir, other than name, whose topic stem is stem.
func sameTopic(dir, name, stem string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintln(logw, "crius-doc-guard: cannot list", dir+", the call goes on:", err)
		}
		return ""
	}
	for _, e := range entries {
		if e.IsDir() || e.Name() == name || !isDoc(e.Name()) {
			continue
		}
		if other := topicStem(e.Name()); other != "" && sameStem(stem, other) {
			return filepath.Join(dir, e.Name())
		}
	}
	return ""
}

var (
	// dateRe finds a date (2026-10-30, 2026_10_30, 20261030), a time right after it (-1530, T153012) and a copy
	// letter or number right after that (2026-10-25-A, 2026-10-21b, 2026-10-25-2).
	dateRe = regexp.MustCompile(`(?:19|20)\d\d([-_.]?)(?:0[1-9]|1[0-2])([-_.]?)(?:0[1-9]|[12]\d|3[01])` +
		`(?:[-_t]?\d{4}(?:\d\d)?)?(?:[-_]?(?:[a-z]|\d{1,2})(?:[^a-z0-9]|$))?`)
	// copyRe is a word that says which copy, not what: v2 and the copy words.
	copyRe = regexp.MustCompile(`^(?:v\d+|fresh|new|final|draft|updated)$`)
	// counterRe is a last word that may count copies: one letter (-A), a small number (-2) or a day (1030). It marks a
	// copy only beside a doc without it (BRIEF-2 beside BRIEF); beside each other they are a numbered series (sprint-10
	// and sprint-11, CLIENT-A and CLIENT-B, OWNER-CARD-1 and -3), as the 10-30 scan of the real tree showed. A number
	// inside a name is an id (F-236-PLAN, 27-02-PLAN, proof-0079) and always stays.
	counterRe = regexp.MustCompile(`^(?:[a-z]|\d{1,2}|(?:0[1-9]|1[0-2])(?:0[1-9]|[12]\d|3[01]))$`)
	splitRe   = regexp.MustCompile(`[^a-z0-9]+`)
)

// topicStem is what a doc's name says it is about: lower case, with dates, versions and copy words taken out. ""
// when nothing but those and counters is left.
func topicStem(name string) string {
	s := strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name)))
	var b strings.Builder
	last := 0
	for _, m := range dateRe.FindAllStringSubmatchIndex(s, -1) {
		// A date is its own word: not part of a longer number, and its two separators are the same.
		if m[0] > 0 && isDigit(s[m[0]-1]) || m[1] < len(s) && isDigit(s[m[1]]) || s[m[2]:m[3]] != s[m[4]:m[5]] {
			continue
		}
		b.WriteString(s[last:m[0]])
		b.WriteString("-")
		last = m[1]
	}
	b.WriteString(s[last:])
	var words []string
	for _, w := range splitRe.Split(b.String(), -1) {
		if w != "" && !copyRe.MatchString(w) {
			words = append(words, w)
		}
	}
	if base(words) == nil {
		return ""
	}
	return strings.Join(words, "-")
}

// base drops the trailing counters of a stem's words.
func base(words []string) []string {
	for len(words) > 0 && counterRe.MatchString(words[len(words)-1]) {
		words = words[:len(words)-1]
	}
	if len(words) == 0 {
		return nil
	}
	return words
}

// sameStem says whether two stems name one topic: they are equal, or one is the other with copy counters added.
func sameStem(a, b string) bool {
	if a == b {
		return true
	}
	ba, bb := strings.Join(base(strings.Split(a, "-")), "-"), strings.Join(base(strings.Split(b, "-")), "-")
	return ba == b || bb == a
}

func isDigit(c byte) bool { return '0' <= c && c <= '9' }

// seriesDirs are folder names (globs, lower case) whose docs are a series on purpose: a doc anywhere under one passes.
// Each is here because the 10-30 scan of ~/Projects, ~/.crius and ~/.claude* found docs of one stem
// there that are not copies; "hits" counts docs the guard would have refused without the entry, first under a stem
// rule that dropped every number, then under the rule in topicStem.
var seriesDirs = []string{
	"tmp",       // 1627 / 0 hits: ~/.claude/jobs/*/tmp/*_logs run logs (run_1836_data.txt); the scratchpad
	"*scratch*", // 6 / 1: .qa-scratch/t8 (real.txt, real-fresh.txt), tools/agent-cost/review-1016/scratch/cost
	"out",       // 71 / 66: script output, a poc verify/out (offers-<time>.md), freqtrade research/scripts/out
	"evidence",  // 565 / 1: captured proof, staffy docs/build/qa/evidence, poc/*/evidence (mutants-12.txt)
	"vendor",    // 2 / 2: third-party copies, a workflow repo vendor/gstack (PLAN_TUNING_V0.md, gpt-5.4.md)
}

func seriesDir(dir string) bool {
	for _, part := range strings.Split(strings.ToLower(filepath.ToSlash(dir)), "/") {
		if slices.ContainsFunc(seriesDirs, func(g string) bool { ok, _ := path.Match(g, part); return ok }) {
			return true
		}
	}
	return false
}

// checkBash finds the files a command creates by redirection, tee or cp, and checks each it can name without
// running anything. Only a cd that surely runs in the main shell moves the start folder: the first command of a
// top-level statement (cd x; ... or cd x && ...). Any other cd (in a subshell, a pipe, $(...), a function, after
// && or ||) makes the folder unknown from there on, so later relative paths pass instead of being checked in the
// wrong folder.
func checkBash(command, cwd string) string {
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil {
		return ""
	}
	sure := map[*syntax.CallExpr]bool{}
	for _, st := range f.Stmts {
		for !st.Background && !st.Negated {
			if b, ok := st.Cmd.(*syntax.BinaryCmd); ok && b.Op == syntax.AndStmt {
				st = b.X
				continue
			}
			if ce, ok := st.Cmd.(*syntax.CallExpr); ok {
				sure[ce] = true
			}
			break
		}
	}
	why := ""
	try := func(p string) {
		if why == "" {
			if p = resolve(cwd, p); p != "" {
				why = checkPath(p)
			}
		}
	}
	syntax.Walk(f, func(n syntax.Node) bool {
		if why != "" {
			return false
		}
		switch n := n.(type) {
		case *syntax.Stmt:
			for _, r := range n.Redirs {
				if r.N != nil && r.N.Value != "1" {
					continue // 2> and other numbered streams write logs, not docs
				}
				switch r.Op {
				case syntax.RdrOut, syntax.AppOut, syntax.RdrAll, syntax.AppAll, syntax.ClbOut, syntax.RdrInOut:
					try(lit(r.Word))
				case syntax.DplOut: // >& file; >&2 and >&- name a stream
					if p := lit(r.Word); p != "-" && strings.Trim(p, "0123456789") != "" {
						try(p)
					}
				}
			}
		case *syntax.CallExpr:
			if len(n.Args) == 0 {
				return true
			}
			args := make([]string, len(n.Args))
			for i, a := range n.Args {
				args[i] = lit(a)
			}
			switch filepath.Base(args[0]) {
			case "cd":
				next := ""
				if sure[n] && len(args) == 2 && !strings.HasPrefix(args[1], "-") {
					next = resolve(cwd, args[1])
				}
				cwd = next
			case "tee":
				for _, a := range args[1:] {
					if !strings.HasPrefix(a, "-") {
						try(a)
					}
				}
			case "cp":
				for _, p := range cpTargets(cwd, args[1:]) {
					try(p)
				}
			}
		}
		return true
	})
	return why
}

// cpTargets gives the files a cp makes: the last operand, or each source's name in the target folder (-t DIR, or a
// last operand that ends in / or is a folder). "" for a path it cannot read.
func cpTargets(cwd string, args []string) []string {
	var ops []string
	dir := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-t" || a == "--target-directory":
			if i+1 < len(args) {
				dir = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "--target-directory="):
			dir = strings.TrimPrefix(a, "--target-directory=")
		case strings.HasPrefix(a, "-") && a != "-":
		default:
			ops = append(ops, a)
		}
	}
	if dir == "" {
		if len(ops) < 2 {
			return nil
		}
		last := ops[len(ops)-1]
		ops = ops[:len(ops)-1]
		info, err := os.Stat(resolve(cwd, last))
		if !strings.HasSuffix(last, "/") && (err != nil || !info.IsDir()) {
			return []string{last}
		}
		dir = last
	}
	var out []string
	for _, src := range ops {
		if src != "" && dir != "" {
			out = append(out, filepath.Join(dir, filepath.Base(src)))
		}
	}
	return out
}

// lit gives a word's text as the shell reads it when it is plain text, quotes and escapes only; "" when it holds a
// variable, a command, a $'...' string or a glob.
func lit(w *syntax.Word) string {
	var b strings.Builder
	for _, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			b.WriteString(unescape(p.Value, ""))
		case *syntax.SglQuoted:
			if p.Dollar {
				return ""
			}
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			if p.Dollar {
				return ""
			}
			for _, q := range p.Parts {
				l, ok := q.(*syntax.Lit)
				if !ok {
					return ""
				}
				b.WriteString(unescape(l.Value, "$`\"\\\n"))
			}
		default:
			return ""
		}
	}
	if strings.ContainsAny(b.String(), "*?[") {
		return ""
	}
	return b.String()
}

// unescape drops the backslash of each escape: before any character outside quotes (only: ""), or before one of only
// inside double quotes. A backslash and newline go away together.
func unescape(s, only string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && (only == "" || strings.IndexByte(only, s[i+1]) >= 0) {
			i++
			if s[i] == '\n' {
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// resolve makes p absolute from cwd, with a leading ~/ as the home folder; "" when it cannot.
func resolve(cwd, p string) string {
	if p == "" {
		return ""
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		p = filepath.Join(home, p[1:])
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	if !filepath.IsAbs(cwd) {
		return ""
	}
	return filepath.Join(cwd, p)
}
