// Command crius-bash-guard is a Claude Code PreToolUse hook for Bash. It refuses two commands that broke the team:
// team-wait.sh run detached with `&` (a detached waiter cannot wake the session; 10-21 22:44), and a VERIFIED line
// written straight onto a bus log instead of through bus-post (10-20 and 10-21: UTC time slip, VERIFIED before the
// run). Exit 2 refuses, with the reason on stderr; anything else passes.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

func main() {
	var in struct {
		ToolName  string `json:"tool_name"`
		ToolInput struct {
			Command string `json:"command"`
		} `json:"tool_input"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&in); err != nil {
		fmt.Fprintln(os.Stderr, "crius-bash-guard: unreadable hook input:", err)
		os.Exit(1) // a hook error, not a refusal: Claude Code shows it and runs the command
	}
	if in.ToolName != "Bash" {
		return
	}
	if why := check(in.ToolInput.Command); why != "" {
		fmt.Fprintln(os.Stderr, "Refused by crius-bash-guard:", why)
		os.Exit(2)
	}
}

// verifiedRe is a VERIFIED record (type, sender, arrow), not the word in passing ("waiting for VERIFIED").
var (
	verifiedRe = regexp.MustCompile(`VERIFIED\s+\S+:\S+\s+->`)
	loneAmpRe  = regexp.MustCompile(`(^|[^&>])&($|[^&>])`)
)

// check gives the reason to refuse command, or "". It reads the whole command: a VERIFIED record anywhere (even in
// a variable set earlier) with a write to the bus anywhere is refused.
func check(command string) string {
	f, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		// Bash runs the lines before a syntax error, so an unparsable command is judged on its text, strictly.
		if strings.Contains(command, "team-wait.sh") && loneAmpRe.MatchString(command) {
			return detached
		}
		if verifiedRe.MatchString(command) && strings.Contains(command, ".crius/bus") {
			return rawVerified
		}
		return ""
	}
	inBus, writes, why := false, false, ""
	syntax.Walk(f, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.Stmt:
			if n.Background && runsWaiter(n) {
				why = detached
			}
		case *syntax.Redirect:
			switch n.Op {
			case syntax.RdrOut, syntax.AppOut, syntax.ClbOut, syntax.RdrAll, syntax.AppAll:
				writes = writes || strings.Contains(print(n.Word), ".crius/bus")
			}
		case *syntax.CallExpr:
			args := unwrap(n.Args)
			if len(args) == 0 {
				break
			}
			switch base(args[0]) {
			case "tee":
				for _, a := range args[1:] {
					writes = writes || strings.Contains(print(a), ".crius/bus")
				}
			case "cd", "pushd":
				for _, a := range args[1:] {
					inBus = inBus || strings.Contains(print(a), ".crius/bus")
				}
			case "bash", "sh", "zsh":
				for i, a := range args[:len(args)-1] {
					if script, ok := literal(args[i+1]); ok && print(a) == "-c" && why == "" {
						why = check(script)
					}
				}
			}
		}
		return why == ""
	})
	if why != "" {
		return why
	}
	if inBus { // after a cd into the bus folder every write may land on a bus log
		syntax.Walk(f, func(n syntax.Node) bool {
			if r, ok := n.(*syntax.Redirect); ok && (r.Op == syntax.RdrOut || r.Op == syntax.AppOut) {
				writes = true
			}
			if c, ok := n.(*syntax.CallExpr); ok && len(unwrap(c.Args)) > 0 && base(unwrap(c.Args)[0]) == "tee" {
				writes = true
			}
			return true
		})
	}
	if writes && verifiedRe.MatchString(command) {
		return rawVerified
	}
	return ""
}

const (
	detached = "team-wait.sh must not run detached with '&': a detached waiter cannot wake this session. " +
		"Run it with the Bash tool's run_in_background: true instead."
	rawVerified = "a VERIFIED line goes onto the bus only through bus-post " +
		"(~/.crius/bin/bus-post <log> VERIFIED <from> <to> <text>): it stamps the time and checks the run log."
)

// runsWaiter reports whether stmt runs team-wait.sh itself, or a shell on it (not a grep that names it).
func runsWaiter(stmt *syntax.Stmt) bool {
	found := false
	syntax.Walk(stmt, func(n syntax.Node) bool {
		if c, ok := n.(*syntax.CallExpr); ok {
			args := unwrap(c.Args)
			if len(args) > 0 && base(args[0]) == "team-wait.sh" {
				found = true
			}
			if len(args) > 1 && (base(args[0]) == "bash" || base(args[0]) == "sh" || base(args[0]) == "zsh") &&
				base(args[1]) == "team-wait.sh" {
				found = true
			}
		}
		return !found
	})
	return found
}

// unwrap drops leading words that run the next word as the command: command, env (and its NAME=value), nohup, ...
func unwrap(args []*syntax.Word) []*syntax.Word {
	for len(args) > 0 {
		switch w, _ := literal(args[0]); {
		case w == "command" || w == "env" || w == "nohup" || w == "exec" || w == "sudo" || w == "time":
			args = args[1:]
		case strings.Contains(w, "=") && !strings.HasPrefix(w, "-"):
			args = args[1:]
		default:
			return args
		}
	}
	return args
}

// base is the last path part of a word's literal value ("/usr/bin/tee" and "\"tee\"" are both tee).
func base(w *syntax.Word) string {
	s, ok := literal(w)
	if !ok {
		return ""
	}
	return path.Base(s)
}

// literal gives a word's value when it holds no expansion: plain, 'single' or "double" quoted parts.
func literal(w *syntax.Word) (string, bool) {
	var b strings.Builder
	for _, p := range w.Parts {
		switch p := p.(type) {
		case *syntax.Lit:
			b.WriteString(p.Value)
		case *syntax.SglQuoted:
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			for _, q := range p.Parts {
				l, ok := q.(*syntax.Lit)
				if !ok {
					return "", false
				}
				b.WriteString(l.Value)
			}
		default:
			return "", false
		}
	}
	return b.String(), true
}

func print(n syntax.Node) string {
	var b strings.Builder
	syntax.NewPrinter().Print(&b, n)
	return b.String()
}
