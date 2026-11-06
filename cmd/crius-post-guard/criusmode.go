package main

import (
	"errors"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

// Crius mode (Crius 11-06, owner: the Crius session kept asking him for clicks, so he could not leave it alone). In the
// Crius session and its subagents, known by the hook input's session_id being the one Crius writes to
// $CRIUS_HOME/state/crius/session-id (review of 2a882aa, P2-7: a project folder is not a session), the guard never
// answers "ask":
//   - every post the reading finds is recorded instead of returned, so the reading goes on through the whole command and
//     every script it reads; when all of them pass the hard checks the call goes on (the guard writes nothing, so the
//     owner's own permission rules still apply);
//   - a hard check that fails denies with its reason: text with AI credit (aiCreditRe: the real forms, not a bare
//     "generated"), an az repos pr update --status completed into develop or main (the target is read with one GET in
//     the command's own org, which must be Appxite's), a completion through REST (by name or as the number 3), any
//     --auto-complete or a prefix of it, a production URL or resource (prodRe, prodHostRe);
//   - anything the guard cannot read denies with the reason and "write it so the guard can read it": the agent rewrites
//     the call and nobody waits. That covers a post with a word that is not a literal (its text could be anything), and
//     every source of text the post's own parser finds (bodySources) that the guard did not read: a file that cannot be
//     read, stdin, a curl or wget config file, a file the command writes before it in a way the guard does not read, a
//     relative file after a folder change (a cd, find -execdir, and find -exec and xargs, which fill in words).
// The exact shapes (adoexact.go, quotesexact.go, passpost.go) still pass on their own; when their hook-time checks fail,
// the call gets the reading like any other. Allows go to post-guard-allows.log, not the ask log.
// Known gaps: hook input the guard cannot parse names no session, so it still asks; a git push to develop or main
// passes here as everywhere (not a post to this guard); text an az call reads from somewhere other than its words and
// the files they name (a config default) is not seen; a file a script the command runs writes under a name the script
// computes is not seen; any session of this user can write the session-id file.

// criusSession says whether sid is the Crius session: the one line of $CRIUS_HOME/state/crius/session-id, a plain file
// of this user's that no one else may write.
func criusSession(sid string) bool {
	if sid == "" {
		return false
	}
	b, why := ownFile(filepath.Join(crHome(), "state", "crius", "session-id"))
	if why != "" {
		return false
	}
	id, rest, _ := strings.Cut(string(b), "\n")
	return strings.TrimSpace(rest) == "" && strings.TrimSpace(id) == sid
}

// crHome is $CRIUS_HOME, else ~/.crius.
func crHome() string {
	if h := os.Getenv("CRIUS_HOME"); h != "" {
		return h
	}
	u, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(u, ".crius")
}

// ownFile reads p when it is a plain file of this user's that neither group nor others may write; why says why not.
func ownFile(p string) ([]byte, string) {
	fi, err := os.Lstat(p)
	switch {
	case err != nil:
		return nil, p + " cannot be read (" + err.Error() + ")"
	case !fi.Mode().IsRegular():
		return nil, p + " is not a plain file"
	case fi.Mode().Perm()&0o022 != 0:
		return nil, p + " is group- or world-writable"
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Getuid() {
		return nil, p + " is not this user's"
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, p + " cannot be read (" + err.Error() + ")"
	}
	return b, ""
}

// postCall is one post the reading found: the tool, its words after the tool's name (variables set to a literal put in),
// the reading's context where it was found, and every source of text its parser found beyond the words.
type postCall struct {
	tool  string
	args  []word
	c     ctx
	moved bool // the command may have changed folder before it, so a relative file name is not known
	src   []source
	why   string
}

// criusLog collects the posts of one hook call in Crius mode.
type criusLog struct {
	posts   []postCall
	lead    bool     // the lead allow list's reading: pinned team scripts pass unread (allowlist.go)
	pinMiss []string // why team scripts the command runs did not pass on their pin
}

// post records a post the reading found and gives "" in Crius mode, so the reading goes on; elsewhere it gives why.
// stdin: the call reads a pipe, a file or a heredoc on its standard input.
func (c ctx) post(tool string, args []word, stdin bool, why string) string {
	if c.crius == nil || why == "" || unsureWhy(why) {
		return why
	}
	a := c.azArgs(args)
	c.crius.posts = append(c.crius.posts, postCall{tool: tool, args: a, c: c, moved: c.moved || c.cd != nil || c.fed,
		src: bodySources(tool, a, stdin, c), why: why})
	return ""
}

// bodySources gives every source of text the tool's own parser finds in its words (the parser that found the post).
func bodySources(tool string, args []word, stdin bool, c ctx) []source {
	switch tool {
	case "az":
		return parseAz(args).src
	case "curl":
		return readCurl(args).src
	case "wget":
		return readWget(args, c).src
	}
	return readHTTPie(args, stdin, c).src
}

var (
	// aiCreditRe is AI credit in its real forms (Crius 11-06: "Migration regenerated" in a develop commit matched a bare
	// "generated" twice).
	aiCreditRe = regexp.MustCompile(`(?i)co-authored-by\s*[:=][^\n]*\b(claude|anthropic)\b|\bgenerated with \[?claude|🤖|\bclaude\.(ai|com)\b`)
	// prodRe is a production host or resource: -prod-, p1-prod, prod.<host>, <name>-prod.<host> (permissions.ask holds
	// the same marks).
	prodRe = regexp.MustCompile(`(?i)[a-z0-9]-prod\b|\bprod-[a-z0-9]|\bprod\.[a-z0-9]|\.prod\.`)
	// prodHostRe is a URL whose host has the word prod or production (quotes.production.appxite.com, review P3-10).
	prodHostRe = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^/\s"'<>]*\bprod(uction)?\b`)
	// restCompleteRe is a PR completion in a REST body or query: status completed, or its number 3 (PullRequestStatus).
	restCompleteRe = regexp.MustCompile(`(?i)\bstatus["']?\s*[:=]\s*(["']?completed\b|3\b)|autoCompleteSetBy`)
	protectedRe    = regexp.MustCompile(`^refs/heads/(develop|main|master)$`)
)

const rewrite = "; write it so the guard can read it (literal words; post text in a file passed with @file or --in-file)"

// criusRun is run for a Crius-mode call: nothing when it may go on, else a deny with the reason.
func criusRun(in input, w io.Writer) error {
	col := &criusLog{}
	if why := checkIn(in, col); why != "" {
		// posts are recorded, so what is left is what the guard cannot read (a command, a script, code of another language)
		why = "Crius mode: refused, not asked: " + strings.TrimPrefix(why, reason+" ") + rewrite
		logAsk(in, why)
		return decide(w, "deny", why)
	}
	for _, p := range col.posts {
		if why := hardCheck(p); why != "" {
			why = "Crius mode: refused, not asked: " + why
			logAsk(in, why)
			return decide(w, "deny", why)
		}
	}
	if len(col.posts) > 0 {
		logTo(in, "Crius mode: allowed "+strings.TrimPrefix(col.posts[0].why, reason+" "), "post-guard-allows.log")
	}
	return nil
}

// hardCheck gives why one post may not go on, or "".
func hardCheck(p postCall) string {
	what := p.tool
	var text []string
	for _, a := range p.args {
		if !a.ok {
			return what + " has a word the guard cannot read (" + a.src + ")" + rewrite
		}
		text = append(text, a.text)
	}
	for _, s := range p.src {
		if s.unread != "" {
			return what + ": " + s.unread + ", which the guard does not read" + rewrite
		}
		body, why := p.readBody(s.file)
		if why != "" {
			return what + ": " + why + rewrite
		}
		text = append(text, body)
	}
	all := strings.Join(text, "\n")
	if m := aiCreditRe.FindString(all); m != "" {
		return what + ": the text carries AI credit (" + m + ")"
	}
	if m := prodRe.FindString(all) + prodHostRe.FindString(all); m != "" {
		return what + ": it names a production URL or resource (" + m + ")"
	}
	if p.tool == "az" {
		o := parseAz(p.args)
		what = "az " + o.cmd
		if _, set := o.opts["--auto-complete"]; set {
			return what + " --auto-complete: auto-complete stays the owner's"
		}
		if o.cmd == "repos pr update" && completes(o) {
			id, _ := o.last("--id")
			if !prIDRe.MatchString(id.text) {
				return what + " --status completed with a PR id the guard cannot read" + rewrite
			}
			// the guard reads the target in Appxite's org; the command's own org must be that one (review P3-11)
			switch org, set := o.last("--org", "--organization"); {
			case !set:
				return what + " --status completed names no --org, so the guard cannot tell which PR " + id.text + " is" + rewrite
			case !strings.EqualFold(strings.TrimRight(org.text, "/"), adoOrg):
				return what + " --status completed in " + org.text + ": the guard reads PR targets only in " + adoOrg + rewrite
			}
			pi, err := prState(id.text)
			if err != nil || pi.Target == "" {
				return what + " --status completed: the guard cannot read the target of PR " + id.text + rewrite
			}
			if protectedRe.MatchString(pi.Target) {
				return what + " --status completed: PR " + id.text + " merges into " + pi.Target + ", which stays the owner's"
			}
			return ""
		}
	}
	if m := restCompleteRe.FindString(all); m != "" {
		return what + ": it completes a PR or sets auto-complete through REST (" + m + "), where the guard cannot read the target; use az repos pr update --id <n> --status completed"
	}
	return ""
}

// completes says whether an az repos pr update sets --status completed.
func completes(o azOpts) bool {
	for _, v := range o.opts["--status"] {
		if strings.EqualFold(v.text, "completed") {
			return true
		}
	}
	return false
}

// readBody reads a file a post sends (at most 1 MiB, a regular file, plus the text a heredoc in the command writes into
// it); why says what kept the guard from reading what the tool will send.
func (p postCall) readBody(name string) (string, string) {
	if name == "" || name == "-" || strings.HasPrefix(name, "/dev/") {
		return "", "it sends text from stdin or a device (" + name + ")"
	}
	if strings.HasPrefix(name, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			name = home + name[1:]
		}
	}
	if !filepath.IsAbs(name) {
		if p.moved || p.c.cwd == "" {
			return "", "it sends " + name + ", a relative file after a folder change the guard does not follow"
		}
		name = filepath.Join(p.c.cwd, name)
	}
	docs, written, open := p.c.heredocs(name)
	fine := maps.Clone(written)
	for _, a := range p.args {
		if a.node != nil {
			fine[a.node] = true // the post's own words read the file
		}
	}
	if p.c.changes(name, fine) || p.c.madeBy(name) != "" {
		return "", "the command writes " + name + " before it sends it, in a way the guard does not read (write the file in a call of its own first)"
	}
	if open {
		return "", "a heredoc writes " + name + " with text the guard does not put in ($(…) or a variable)"
	}
	fi, err := os.Stat(name)
	switch {
	case err == nil && fi.Mode().IsRegular() && fi.Size() <= 1<<20:
		b, err := os.ReadFile(name)
		if err != nil {
			return "", "the guard cannot read the file it sends (" + name + ")"
		}
		docs = append(docs, string(b))
	case errors.Is(err, fs.ErrNotExist) && len(docs) > 0:
		// the heredoc makes it
	default:
		return "", "the guard cannot read the file it sends (" + name + ")"
	}
	return strings.Join(docs, "\n"), ""
}
