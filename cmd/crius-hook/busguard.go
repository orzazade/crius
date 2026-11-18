package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/orzazade/crius/internal/store"
	"mvdan.cc/sh/v3/syntax"
)

// busDenyReason goes back to the agent whose tool call would write into the bus folder itself. A line written there
// directly skips bus-post: on 11-18 axi:api-split stamped one with `date -u` (UTC, 4 h behind the local-time bus) and
// cortex:lead did the same on 11-16. Notes are files agents write on purpose, so the notes subfolder stays open.
const busDenyReason = "refused: write bus lines with ~/.crius/bin/bus-post (it stamps local time, checks length, " +
	"wakes the addressee, refuses mirror copies); direct writes to ~/.crius/bus/ skip that. Long text: write a note " +
	"in ~/.crius/bus/notes/ and post its path with bus-post."

// unknown stands for a part of a shell word the hook cannot know without running the shell (a command substitution,
// a variable set elsewhere).
const unknown = "\x00"

// denyBusWrite prints Claude Code's PreToolUse deny when the call would write a file in the bus folder itself, and
// returns whether it did. Other tools are not read at all. A Bash command the bash parser cannot read (zsh-only syntax
// such as `*.log(om[1,3])`) is let through, and logged only when it names the bus and could write.
func denyBusWrite(raw []byte, tool, cwd string) (bool, error) {
	if tool != "Bash" && tool != "Write" && tool != "Edit" && tool != "MultiEdit" {
		return false, nil
	}
	var in struct {
		Input struct {
			Command  string `json:"command"`
			FilePath string `json:"file_path"`
		} `json:"tool_input"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return false, fmt.Errorf("PreToolUse %s tool_input: %w", tool, err)
	}
	crius := store.Home() // panics with no home at all; main recovers it
	home, _ := os.UserHomeDir()
	buses := []string{filepath.Join(crius, "bus"), filepath.Join(home, ".crius", "bus")}
	targets := []string{in.Input.FilePath}
	if tool == "Bash" {
		var err error
		if targets, err = writeTargets(in.Input.Command, home, crius); err != nil {
			c := in.Input.Command
			if strings.Contains(c, ".crius/bus") && (strings.Contains(c, ">") || strings.Contains(c, "tee")) {
				return false, fmt.Errorf("PreToolUse Bash command naming the bus not parsed, let through: %w", err)
			}
			return false, nil
		}
	}
	for _, t := range targets {
		dir := t
		if i := strings.LastIndex(t, "/"); i >= 0 {
			dir = t[:i]
		}
		if t == "" || strings.Contains(dir, unknown) {
			continue // no file, or its folder is not known
		}
		if !filepath.IsAbs(t) {
			t = filepath.Join(cwd, t)
		}
		if slices.Contains(buses, filepath.Dir(filepath.Clean(t))) {
			out, err := json.Marshal(map[string]any{"hookSpecificOutput": map[string]string{
				"hookEventName": "PreToolUse", "permissionDecision": "deny", "permissionDecisionReason": busDenyReason}})
			if err != nil {
				return false, err
			}
			_, err = os.Stdout.Write(out)
			return true, err
		}
	}
	return false, nil
}

// writeTargets lists the files a shell command writes by redirection (>, >>, >|, &>, &>>, <>, >&file) or tee, with ~,
// $HOME, $CRIUS_HOME and variables the command itself set to a plain value expanded, and every other expansion as
// unknown. Not seen: a `cd` earlier in the command, and commands run through `bash -c` or `eval`.
func writeTargets(cmd, home, crius string) ([]string, error) {
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(cmd), "")
	if err != nil {
		return nil, err
	}
	vars := map[string]string{"HOME": home, "CRIUS_HOME": crius}
	assign := func(as []*syntax.Assign) {
		for _, a := range as {
			if a.Name != nil && !a.Naked && !a.Append && a.Array == nil && a.Index == nil {
				vars[a.Name.Value] = expand(a.Value, vars, home)
			}
		}
	}
	var targets []string
	syntax.Walk(f, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.Redirect:
			switch n.Op {
			case syntax.RdrOut, syntax.AppOut, syntax.ClbOut, syntax.RdrAll, syntax.AppAll, syntax.RdrInOut:
				targets = append(targets, expand(n.Word, vars, home))
			case syntax.DplOut: // >&2 and >&- name a descriptor; >&file writes the file
				if w := expand(n.Word, vars, home); w != "-" && strings.Trim(w, "0123456789") != "" {
					targets = append(targets, w)
				}
			}
		case *syntax.DeclClause: // export, local, declare, typeset, readonly
			assign(n.Args)
		case *syntax.CallExpr:
			if len(n.Args) == 0 {
				assign(n.Assigns)
				return true
			}
			args := n.Args
			for len(args) > 1 && slices.Contains([]string{"sudo", "command", "nohup"}, filepath.Base(expand(args[0], vars, home))) {
				args = args[1:]
			}
			if filepath.Base(expand(args[0], vars, home)) == "tee" {
				for _, a := range args[1:] {
					if s := expand(a, vars, home); !strings.HasPrefix(s, "-") {
						targets = append(targets, s)
					}
				}
			}
		}
		return true
	})
	return targets, nil
}

// expand gives a word's value as the shell would, with parts it cannot know replaced by unknown.
func expand(w *syntax.Word, vars map[string]string, home string) string {
	if w == nil {
		return ""
	}
	var b strings.Builder
	var part func(p syntax.WordPart, first bool)
	part = func(p syntax.WordPart, first bool) {
		switch p := p.(type) {
		case *syntax.Lit:
			v := p.Value
			if first && (v == "~" || strings.HasPrefix(v, "~/")) {
				v = home + v[1:]
			}
			b.WriteString(v)
		case *syntax.SglQuoted:
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			for _, q := range p.Parts {
				part(q, false)
			}
		case *syntax.ParamExp:
			v, ok := "", false
			if p.Param != nil {
				v, ok = vars[p.Param.Value]
			}
			if !ok || p.Excl || p.Length || p.Width || p.Index != nil || p.Slice != nil || p.Repl != nil || p.Exp != nil {
				v = unknown
			}
			b.WriteString(v)
		default:
			b.WriteString(unknown)
		}
	}
	for i, p := range w.Parts {
		part(p, i == 0)
	}
	return b.String()
}
