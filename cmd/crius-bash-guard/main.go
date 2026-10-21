// Command crius-bash-guard is a Claude Code PreToolUse hook for Bash. It refuses two commands that broke the team:
// team-wait.sh run detached with `&` (a detached waiter cannot wake the session; 10-21 22:44), and a VERIFIED line
// written straight onto a bus log instead of through bus-post (10-20 and 10-21: UTC time slip, VERIFIED before the
// run). Exit 2 refuses, with the reason on stderr; anything else passes.
package main

import (
	"encoding/json"
	"fmt"
	"os"
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

// check gives the reason to refuse command, or "".
func check(command string) string {
	f, err := syntax.NewParser().Parse(strings.NewReader(command), "")
	if err != nil {
		// Not shell this parser reads: judge the text, so an unparsable command cannot slip past.
		if strings.Contains(command, "team-wait.sh") && strings.HasSuffix(strings.TrimSpace(command), "&") {
			return detached
		}
		if strings.Contains(command, "VERIFIED") && strings.Contains(command, ".crius/bus/") && strings.Contains(command, ">") {
			return rawVerified
		}
		return ""
	}
	for _, top := range f.Stmts {
		text := print(top)
		if strings.Contains(text, "VERIFIED") && writesBus(top) {
			return rawVerified
		}
		bad := false
		syntax.Walk(top, func(n syntax.Node) bool {
			if s, ok := n.(*syntax.Stmt); ok && s.Background && strings.Contains(print(s.Cmd), "team-wait.sh") {
				bad = true
			}
			return !bad
		})
		if bad {
			return detached
		}
	}
	return ""
}

const (
	detached = "team-wait.sh must not run detached with '&': a detached waiter cannot wake this session. " +
		"Run it with the Bash tool's run_in_background: true instead."
	rawVerified = "a VERIFIED line goes onto the bus only through bus-post " +
		"(~/.crius/bin/bus-post <log> VERIFIED <from> <to> <text>): it stamps the time and checks the run log."
)

// writesBus reports whether stmt redirects into, or tees into, a file under .crius/bus/.
func writesBus(stmt *syntax.Stmt) bool {
	found := false
	syntax.Walk(stmt, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.Redirect:
			switch n.Op {
			case syntax.RdrOut, syntax.AppOut, syntax.ClbOut, syntax.RdrAll, syntax.AppAll:
				found = found || strings.Contains(print(n.Word), ".crius/bus/")
			}
		case *syntax.CallExpr:
			if len(n.Args) > 0 && print(n.Args[0]) == "tee" {
				for _, a := range n.Args[1:] {
					found = found || strings.Contains(print(a), ".crius/bus/")
				}
			}
		}
		return !found
	})
	return found
}

func print(n syntax.Node) string {
	var b strings.Builder
	syntax.NewPrinter().Print(&b, n)
	return b.String()
}
