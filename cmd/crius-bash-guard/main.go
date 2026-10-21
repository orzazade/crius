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
	verifiedRe = regexp.MustCompile(`VERIFIED\s+\S+\s+->`)
	loneAmpRe  = regexp.MustCompile(`(^|[^&>|])&($|[^&>])`)
	writeRe    = regexp.MustCompile(`(^|[^-])>|\btee\b`)
)

// check gives the reason to refuse command, or "".
func check(command string) string {
	f, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		return checkText(command)
	}
	why, inBus, recordVar := "", false, false
	syntax.Walk(f, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.Stmt:
			if n.Background && runsWaiter(n) {
				why = detached
			}
		case *syntax.Assign:
			recordVar = recordVar || verifiedRe.MatchString(print(n))
		case *syntax.CallExpr:
			args := unwrap(n.Args)
			if len(args) == 0 {
				break
			}
			switch base(args[0]) {
			case "cd", "pushd":
				for _, a := range args[1:] {
					inBus = inBus || strings.Contains(print(a), ".crius/bus")
				}
			case "bash", "sh", "zsh":
				if script := shellScript(args); script != nil && why == "" {
					if text, ok := literal(script); ok {
						why = check(text)
					} else {
						why = checkText(print(script))
					}
				}
			}
		}
		return why == ""
	})
	if why != "" {
		return why
	}
	namesBus := strings.Contains(command, ".crius/bus")
	for _, top := range f.Stmts {
		for _, u := range units(top) {
			if writesBus(u, inBus, namesBus) && (recordVar || verifiedRe.MatchString(print(u))) {
				return rawVerified
			}
		}
	}
	return ""
}

// checkText judges a command the parser cannot read. Bash runs the lines before a syntax error, so it is strict.
func checkText(text string) string {
	if strings.Contains(text, "team-wait.sh") && loneAmpRe.MatchString(text) {
		return detached
	}
	if verifiedRe.MatchString(text) && strings.Contains(text, ".crius/bus") && writeRe.MatchString(text) {
		return rawVerified
	}
	return ""
}

// units splits a statement at && and || into the parts that write on their own; a pipeline stays one unit, since
// its first command makes the text the last one writes.
func units(stmt *syntax.Stmt) []*syntax.Stmt {
	if b, ok := stmt.Cmd.(*syntax.BinaryCmd); ok && (b.Op == syntax.AndStmt || b.Op == syntax.OrStmt) {
		return append(units(b.X), units(b.Y)...)
	}
	return []*syntax.Stmt{stmt}
}

// writesBus reports whether unit writes to a bus log: a redirect or tee into .crius/bus, a relative target after a
// cd into the bus folder, or a target held in a variable when the command names the bus.
func writesBus(unit *syntax.Stmt, inBus, namesBus bool) bool {
	target := func(w *syntax.Word) bool {
		if strings.Contains(print(w), ".crius/bus") {
			return true
		}
		s, ok := literal(w)
		if !ok {
			return namesBus
		}
		return inBus && s != "/dev/null" && !strings.HasPrefix(s, "/") && !strings.HasPrefix(s, "~")
	}
	found := false
	syntax.Walk(unit, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.Redirect:
			switch n.Op {
			case syntax.RdrOut, syntax.AppOut, syntax.ClbOut, syntax.RdrAll, syntax.AppAll:
				found = found || target(n.Word)
			}
		case *syntax.CallExpr:
			if args := unwrap(n.Args); len(args) > 0 && base(args[0]) == "tee" {
				for _, a := range args[1:] {
					found = found || (!strings.HasPrefix(print(a), "-") && target(a))
				}
			}
		}
		return !found
	})
	return found
}

// shellScript gives the script word of `sh -c script` (also -lc, -ec, ...), or nil.
func shellScript(args []*syntax.Word) *syntax.Word {
	if len(args) < 3 {
		return nil
	}
	for i, a := range args[1 : len(args)-1] {
		if w, _ := literal(a); strings.HasPrefix(w, "-") && w != "--" && strings.Contains(w, "c") {
			return args[i+2]
		}
	}
	return nil
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
			if len(args) > 1 && (base(args[0]) == "bash" || base(args[0]) == "sh" || base(args[0]) == "zsh") {
				script := shellScript(args)
				found = found || base(args[1]) == "team-wait.sh" || (script != nil && strings.Contains(print(script), "team-wait.sh"))
			}
		}
		return !found
	})
	return found
}

var (
	wrappers   = map[string]bool{"command": true, "env": true, "nohup": true, "exec": true, "sudo": true, "timeout": true, "caffeinate": true, "nice": true, "setsid": true}
	durationRe = regexp.MustCompile(`^\d+(\.\d+)?[smhd]?$`)
)

// unwrap drops leading words that run the next word as the command: command, env (and its NAME=value), nohup, ...
func unwrap(args []*syntax.Word) []*syntax.Word {
	for len(args) > 0 {
		switch w, _ := literal(args[0]); {
		case wrappers[w]:
			args = args[1:]
			for len(args) > 0 { // the wrapper's options and numbers: timeout 900, nice -n 5
				o, _ := literal(args[0])
				if !strings.HasPrefix(o, "-") && !durationRe.MatchString(o) {
					break
				}
				args = args[1:]
			}
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
