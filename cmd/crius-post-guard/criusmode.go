package main

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Crius mode (Crius 11-06, owner: the Crius session kept asking him for clicks, so he could not leave it alone). In the
// Crius session and its subagents, known by the hook input's transcript_path lying under the Claude project folder
// criusProject, the guard never answers "ask":
//   - every post the reading finds is recorded instead of returned, so the reading goes on through the whole command and
//     every script it reads; when all of them pass the hard checks the call goes on (the guard writes nothing, so the
//     owner's own permission rules still apply);
//   - a hard check that fails denies with its reason: text with AI credit (aiCreditRe: the real forms, not a bare
//     "generated"), an az repos pr update --status completed into develop or main (the target is read with one GET), a
//     completion through REST, any --auto-complete, a production URL or resource (prodRe);
//   - anything the guard cannot read denies with the reason and "write it so the guard can read it": the agent rewrites
//     the call and nobody waits. That covers a post with a word that is not a literal (its text could be anything) and
//     a file it names that cannot be read.
// The exact shapes (adoexact.go, quotesexact.go, passpost.go) still pass on their own; when their hook-time checks fail,
// the call gets the reading like any other.
// Known gaps: hook input the guard cannot parse names no transcript, so it still asks; a git push to develop or main
// passes here as everywhere (not a post to this guard); text an az call reads from somewhere other than an @file or
// --in-file it names (a config default) is not seen.

// criusProject is the Claude project folder of the Crius session: the folder Claude Code keeps its transcripts in, and
// its subagents' below them.
const criusProject = "-Users-orkhanrzazade-Projects-scifi-crius"

// criusSession says whether transcript lies under <config>/projects/<criusProject>/.
func criusSession(transcript string) bool {
	if !filepath.IsAbs(transcript) {
		return false
	}
	parts := strings.Split(filepath.Clean(transcript), string(filepath.Separator))
	for i := 0; i+2 < len(parts); i++ {
		if parts[i] == "projects" && parts[i+1] == criusProject {
			return true
		}
	}
	return false
}

// postCall is one post the reading found: the tool, its words after the tool's name (variables set to a literal put in),
// and the folder the call runs in.
type postCall struct {
	tool  string
	args  []word
	cwd   string
	moved bool // the command may have changed folder before it, so a relative file name is not known
	why   string
}

// criusLog collects the posts of one hook call in Crius mode.
type criusLog struct {
	posts   []postCall
	lead    bool     // the lead allow list's reading: pinned team scripts pass unread (allowlist.go)
	pinMiss []string // why team scripts the command runs did not pass on their pin
}

// post records a post the reading found and gives "" in Crius mode, so the reading goes on; elsewhere it gives why.
func (c ctx) post(tool string, args []word, why string) string {
	if c.crius == nil || why == "" || unsureWhy(why) {
		return why
	}
	c.crius.posts = append(c.crius.posts, postCall{tool: tool, args: c.azArgs(args), cwd: c.cwd, moved: c.moved || c.cd != nil, why: why})
	return ""
}

var (
	// aiCreditRe is AI credit in its real forms (Crius 11-06: "Migration regenerated" in a develop commit matched a bare
	// "generated" twice).
	aiCreditRe = regexp.MustCompile(`(?i)co-authored-by\s*[:=][^\n]*\b(claude|anthropic)\b|\bgenerated with \[?claude|🤖|\bclaude\.(ai|com)\b`)
	// prodRe is a production host or resource: -prod-, p1-prod, prod.<host>, <name>-prod.<host> (permissions.ask holds
	// the same marks).
	prodRe = regexp.MustCompile(`(?i)[a-z0-9]-prod\b|\bprod-[a-z0-9]|\bprod\.[a-z0-9]|\.prod\.`)
	// restCompleteRe is a PR completion in a REST body or query.
	restCompleteRe = regexp.MustCompile(`(?i)\bstatus["']?\s*[:=]\s*["']?completed\b|autoCompleteSetBy`)
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
		logAsk(in, "Crius mode: allowed "+strings.TrimPrefix(col.posts[0].why, reason+" "))
	}
	return nil
}

// hardCheck gives why one post may not go on, or "".
func hardCheck(p postCall) string {
	what := p.tool
	var text []string
	for i, a := range p.args {
		if !a.ok {
			return what + " has a word the guard cannot read (" + a.src + ")" + rewrite
		}
		text = append(text, a.text)
		for _, f := range namedFiles(p.tool, p.args, i) {
			body, why := readPostFile(f, p)
			if why != "" {
				return what + ": " + why + rewrite
			}
			text = append(text, body)
		}
	}
	all := strings.Join(text, "\n")
	if m := aiCreditRe.FindString(all); m != "" {
		return what + ": the text carries AI credit (" + m + ")"
	}
	if m := prodRe.FindString(all); m != "" {
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

// fileOpts are the options whose next word names a file the tool sends.
var fileOpts = map[string]bool{"--in-file": true, "-T": true, "--upload-file": true, "--post-file": true, "--body-file": true}

// namedFiles gives the files word i makes the tool send: @file (az reads any value so), name=@file, name:=@file and
// name=<file (curl -F, httpie), -d@file, and the value of a file option.
func namedFiles(tool string, args []word, i int) []string {
	a := args[i].text
	var out []string
	switch {
	case strings.HasPrefix(a, "@"):
		out = append(out, a[1:])
	case tool != "az" && strings.HasPrefix(a, "-") && strings.Contains(a, "@"):
		out = append(out, a[strings.Index(a, "@")+1:])
	case tool != "az" && (strings.Contains(a, "=@") || strings.Contains(a, "=<")):
		k := strings.Index(a, "=@")
		if k < 0 {
			k = strings.Index(a, "=<")
		}
		out = append(out, a[k+2:])
	}
	name, val, eq := strings.Cut(a, "=")
	if fileOpts[name] && eq {
		out = append(out, val)
	}
	if i > 0 && fileOpts[args[i-1].text] {
		out = append(out, a)
	}
	return out
}

// readPostFile reads a file a post sends (at most 1 MiB, a regular file); why says what kept it from being read.
func readPostFile(name string, p postCall) (string, string) {
	if name == "" || name == "-" || strings.HasPrefix(name, "/dev/") {
		return "", "it sends text from stdin or a device (" + name + ")"
	}
	if strings.HasPrefix(name, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			name = home + name[1:]
		}
	}
	if !filepath.IsAbs(name) {
		if p.moved || p.cwd == "" {
			return "", "it sends " + name + ", a relative file after a folder change the guard does not follow"
		}
		name = filepath.Join(p.cwd, name)
	}
	fi, err := os.Stat(name)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > 1<<20 {
		return "", "the guard cannot read the file it sends (" + name + ")"
	}
	b, err := os.ReadFile(name)
	if err != nil {
		return "", "the guard cannot read the file it sends (" + name + ")"
	}
	return string(b), ""
}
