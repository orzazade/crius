package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/orzazade/crius/internal/testutil"
)

// hookInput is the hook's JSON for a tool call, as Claude Code sends it.
func hookInput(t *testing.T, tool string, toolInput map[string]any) input {
	b, err := json.Marshal(map[string]any{"tool_name": tool, "cwd": t.TempDir(), "tool_input": toolInput})
	if err != nil {
		t.Fatal(err)
	}
	var in input
	if err := json.NewDecoder(bytes.NewReader(b)).Decode(&in); err != nil {
		t.Fatal(err)
	}
	return in
}

// Crius 11-01 19:39: the quotes lead ran an az write to Azure DevOps (az repos ref delete) as shell code in context-mode's
// ctx_execute, and the guard read only Bash. Shell code in ctx_execute, ctx_execute_file and each ctx_batch_execute
// command now gets the Bash check; code in another language gets the check of inline interpreter code (python -c).
// The tool's name is read by its ending, so the plugin's server name does not matter.
func TestCodeInContextModeToolsIsCheckedLikeBash(t *testing.T) {
	const refDelete = `az repos ref delete --name heads/feature/1-x --object-id 0000000000000000000000000000000000000000 --repository svc_demo --org https://dev.azure.com/example-org --project "Example Platform"`
	post := `node -e "fetch('https://dev.azure.com/o/p/_apis/wit/workItems/1/comments?api-version=7.1-preview', {method: 'POST', body: '{}'})"`
	for name, c := range map[string]struct {
		tool string
		in   map[string]any
	}{
		"ctx_execute shell":         {"mcp__plugin_context-mode_context-mode__ctx_execute", map[string]any{"language": "shell", "code": lead94205}},
		"shell, no host or method":  {"mcp__plugin_context-mode_context-mode__ctx_execute", map[string]any{"language": "shell", "code": "az boards work-item update --id 1 --discussion hi"}},
		"another server name":       {"mcp__context-mode__ctx_execute", map[string]any{"language": "shell", "code": "cd /tmp\n" + lead94205}},
		"ctx_execute_file shell":    {"mcp__plugin_context-mode_context-mode__ctx_execute_file", map[string]any{"path": "x.log", "language": "shell", "code": lead94205}},
		"ctx_batch_execute command": {"mcp__plugin_context-mode_context-mode__ctx_batch_execute", map[string]any{"commands": []map[string]string{{"label": "ls", "command": "ls"}, {"label": "post", "command": lead94205}}, "queries": []string{"x"}}},
		"javascript that posts":     {"mcp__plugin_context-mode_context-mode__ctx_execute", map[string]any{"language": "javascript", "code": "await " + strings.TrimPrefix(post, `node -e "`)}},
	} {
		if why := check(hookInput(t, c.tool, c.in)); !strings.HasPrefix(why, reason) {
			t.Errorf("%s: not asked: %q", name, why)
		}
	}
	// review of the ctx tools check: code that starts the az CLI, the tool's own folder, the file ctx_execute_file hands
	// the code, and commands given as plain strings or as JSON text (the plugin takes all three)
	dir, other := t.TempDir(), t.TempDir()
	for _, d := range []string{dir, other} {
		testutil.Write(t, filepath.Join(d, "post.sh"), "#!/bin/bash\necho hi\n")
	}
	testutil.Write(t, filepath.Join(other, "post.sh"), "#!/bin/bash\n"+lead94205+"\n")
	testutil.Write(t, filepath.Join(other, "cmd.txt"), lead94205+"\n")
	execFile := "mcp__plugin_context-mode_context-mode__ctx_execute_file"
	for name, c := range map[string]struct {
		tool string
		in   map[string]any
	}{
		"javascript execSync":        {"mcp__plugin_context-mode_context-mode__ctx_execute", map[string]any{"language": "javascript", "code": "require('child_process').execSync('az boards work-item update --id 1 --discussion hi')"}},
		"python subprocess":          {"mcp__plugin_context-mode_context-mode__ctx_execute", map[string]any{"language": "python", "code": "subprocess.run(['az','repos','pr','create','-d','hi'])"}},
		"a script in the tool's cwd": {"mcp__plugin_context-mode_context-mode__ctx_execute", map[string]any{"language": "shell", "code": "bash post.sh", "cwd": other}},
		"the file run by its path":   {execFile, map[string]any{"path": filepath.Join(other, "post.sh"), "language": "shell", "code": `bash "$FILE_CONTENT_PATH"`}},
		"the file run by eval":       {execFile, map[string]any{"path": filepath.Join(other, "cmd.txt"), "language": "shell", "code": `eval "$FILE_CONTENT"`}},
		"the file run by python":     {execFile, map[string]any{"path": filepath.Join(other, "cmd.txt"), "language": "python", "code": "subprocess.run(FILE_CONTENT, shell=True)"}},
		"a file it cannot read":      {execFile, map[string]any{"path": "cmd.txt", "language": "shell", "code": `eval "$FILE_CONTENT"`}},
		"commands as strings":        {"mcp__plugin_context-mode_context-mode__ctx_batch_execute", map[string]any{"commands": []string{"ls", lead94205}}},
		"commands as JSON text":      {"mcp__plugin_context-mode_context-mode__ctx_batch_execute", map[string]any{"commands": `["ls", ` + strconv.Quote(lead94205) + `]`}},
	} {
		in := hookInput(t, c.tool, c.in)
		in.Cwd = dir // the session's folder: its post.sh is harmless
		if why := check(in); !strings.HasPrefix(why, reason) {
			t.Errorf("%s: not asked: %q", name, why)
		}
	}
	for name, c := range map[string]struct {
		tool string
		in   map[string]any
	}{
		"a log read by its path":   {execFile, map[string]any{"path": filepath.Join(other, "cmd.txt"), "language": "shell", "code": `grep -c invoke "$FILE_CONTENT_PATH"`}},
		"a log read in javascript": {execFile, map[string]any{"path": filepath.Join(other, "cmd.txt"), "language": "javascript", "code": "console.log(FILE_CONTENT.split('\\n').length)"}},
		"commands as strings":      {"mcp__plugin_context-mode_context-mode__ctx_batch_execute", map[string]any{"commands": []string{"ls", "git status --short"}}},
	} {
		if why := check(hookInput(t, c.tool, c.in)); why != "" {
			t.Errorf("%s: asked: %s", name, why)
		}
	}
	// the same verdict as Bash for the case Crius saw: a ref delete posts no text people read, in Bash or here
	if got, want := check(hookInput(t, "mcp__plugin_context-mode_context-mode__ctx_execute", map[string]any{"language": "shell", "code": refDelete})), check(bash(refDelete, "")); got != want {
		t.Errorf("ref delete: ctx_execute %q, Bash %q", got, want)
	}
	for name, c := range map[string]struct {
		tool string
		in   map[string]any
	}{
		"shell that reads":      {"mcp__plugin_context-mode_context-mode__ctx_execute", map[string]any{"language": "shell", "code": "git log --oneline -3; az pipelines list -o tsv"}},
		"batch that reads":      {"mcp__plugin_context-mode_context-mode__ctx_batch_execute", map[string]any{"commands": []map[string]string{{"label": "st", "command": "git status --short"}}}},
		"javascript that reads": {"mcp__plugin_context-mode_context-mode__ctx_execute", map[string]any{"language": "javascript", "code": "console.log(1)"}},
		"another tool":          {"mcp__plugin_context-mode_context-mode__ctx_search", map[string]any{"queries": []string{lead94205}, "command": lead94205, "language": "shell", "code": lead94205}},
	} {
		if why := check(hookInput(t, c.tool, c.in)); why != "" {
			t.Errorf("%s: asked: %s", name, why)
		}
	}
}

// Review r2 of the ctx tools check: with no cwd the plugin runs shell code in its project folder (the session's folder
// here), ctx_execute_file reads its path from there and takes no cwd, the file reaches the code by the plugin's own
// lines (so source and . read it too), a file that is not a small regular one is not read, and code of another language
// that runs the file asks on az as a word in it.
func TestContextModeToolsRunWhereThePluginRunsThem(t *testing.T) {
	proj, stray := t.TempDir(), t.TempDir()
	testutil.Write(t, filepath.Join(proj, "post.sh"), "#!/bin/bash\n"+lead94205+"\n")
	testutil.Write(t, filepath.Join(proj, "cmd2.txt"), lead94205+"\n")
	testutil.Write(t, filepath.Join(proj, "run.sh"), "#!/bin/bash\nbash "+filepath.Join(proj, "post.sh")+"\n")
	testutil.Write(t, filepath.Join(stray, "post.sh"), "#!/bin/bash\necho hi\n")
	testutil.Write(t, filepath.Join(stray, "cmd2.txt"), "echo hi\n")
	testutil.Write(t, filepath.Join(proj, "run.py"), "subprocess.run(['az','repos','pr','create','-d','hi'])\n")
	testutil.Write(t, filepath.Join(proj, "big.txt"), strings.Repeat("echo hi\n", 1<<18))
	fifo := filepath.Join(proj, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	execute, execFile := "mcp__plugin_context-mode_context-mode__ctx_execute", "mcp__plugin_context-mode_context-mode__ctx_execute_file"
	batch := "mcp__plugin_context-mode_context-mode__ctx_batch_execute"
	for name, c := range map[string]struct {
		tool string
		in   map[string]any
	}{
		"shell with no cwd":            {execute, map[string]any{"language": "shell", "code": "bash post.sh"}},
		"batch with no cwd":            {batch, map[string]any{"commands": []string{"bash post.sh"}}},
		"a relative cwd":               {execute, map[string]any{"language": "shell", "code": "bash ../post.sh", "cwd": "sub"}},
		"a file beside a stray cwd":    {execFile, map[string]any{"path": "cmd2.txt", "cwd": stray, "language": "shell", "code": `eval "$FILE_CONTENT"`}},
		"the file sourced":             {execFile, map[string]any{"path": "post.sh", "language": "shell", "code": `source "$FILE_CONTENT_PATH"`}},
		"the file runs a script":       {execFile, map[string]any{"path": "run.sh", "language": "shell", "code": `bash "$FILE_CONTENT_PATH"`}},
		"the file run by .":            {execFile, map[string]any{"path": "post.sh", "language": "shell", "code": `. "$file_path"`}},
		"the file's text as a command": {execFile, map[string]any{"path": "cmd2.txt", "language": "shell", "code": `$FILE_CONTENT`}},
		"python runs az in a list":     {execFile, map[string]any{"path": "run.py", "language": "python", "code": "exec(FILE_CONTENT)"}},
		"a file over 1 MiB":            {execFile, map[string]any{"path": "big.txt", "language": "shell", "code": `eval "$FILE_CONTENT"`}},
		"a FIFO":                       {execFile, map[string]any{"path": "fifo", "language": "shell", "code": `eval "$FILE_CONTENT"`}},
		"one command as a string":      {batch, map[string]any{"commands": lead94205}},
	} {
		in := hookInput(t, c.tool, c.in)
		in.Cwd = proj
		if why := check(in); !strings.HasPrefix(why, reason) {
			t.Errorf("%s: not asked: %q", name, why)
		}
	}
	for name, c := range map[string]struct {
		tool string
		in   map[string]any
	}{
		"a stray cwd's own script": {execute, map[string]any{"language": "shell", "code": "bash post.sh", "cwd": stray}},
		"a log read by its name":   {execFile, map[string]any{"path": "cmd2.txt", "language": "shell", "code": `grep -c invoke "$FILE_CONTENT_PATH"`}},
		"one read as a string":     {batch, map[string]any{"commands": "ls -la"}},
	} {
		in := hookInput(t, c.tool, c.in)
		in.Cwd = proj
		if why := check(in); why != "" {
			t.Errorf("%s: asked: %s", name, why)
		}
	}
}

// The same review: source and . run a script as bash does, in Bash too.
func TestSourceReadsTheScriptInBash(t *testing.T) {
	dir := t.TempDir()
	testutil.Write(t, filepath.Join(dir, "post.sh"), "#!/bin/bash\n"+lead94205+"\n")
	testutil.Write(t, filepath.Join(dir, "env.sh"), "export X=1\n")
	for _, cmd := range []string{"source post.sh", ". " + filepath.Join(dir, "post.sh"), "builtin source post.sh"} {
		if why := check(bash(cmd, dir)); !strings.HasPrefix(why, reason) {
			t.Errorf("%s: not asked: %q", cmd, why)
		}
	}
	if why := check(bash("source env.sh && echo $X", dir)); why != "" {
		t.Errorf("source env.sh: asked: %s", why)
	}
}

// Review r3 of the ctx tools check: zsh code (the plugin runs $SHELL) that reads the file does not parse as bash, a write
// from code of another language needs no method word, source takes --, and "$HOME/…" names a known command.
func TestContextModeToolsReviewR3(t *testing.T) {
	proj := t.TempDir()
	testutil.Write(t, filepath.Join(proj, "cmds.txt"), lead94205+"\n")
	testutil.Write(t, filepath.Join(proj, "post.sh"), "#!/bin/bash\n"+lead94205+"\n")
	execute, execFile := "mcp__plugin_context-mode_context-mode__ctx_execute", "mcp__plugin_context-mode_context-mode__ctx_execute_file"
	for name, c := range map[string]struct {
		tool string
		in   map[string]any
	}{
		"zsh reads the file":  {execFile, map[string]any{"path": "cmds.txt", "language": "shell", "code": "for l in ${(f)FILE_CONTENT}; do eval $l; done"}},
		"python urllib data=": {execute, map[string]any{"language": "python", "code": "urllib.request.urlopen(urllib.request.Request('https://dev.azure.com/o/p/_apis/wit/workItems/1/comments?api-version=7.1-preview', data=b'{}'))"}},
		"node SDK":            {execute, map[string]any{"language": "javascript", "code": "const c = new WebApi('https://dev.azure.com/o', h); (await c.getGitApi()).createThread({comments: [{content: 'hi'}]}, 'repo', 7, 'p')"}},
		"source --":           {execute, map[string]any{"language": "shell", "code": "source -- post.sh"}},
	} {
		in := hookInput(t, c.tool, c.in)
		in.Cwd = proj
		if why := check(in); !strings.HasPrefix(why, reason) {
			t.Errorf("%s: not asked: %q", name, why)
		}
	}
	for _, cmd := range []string{". -- post.sh", `. -- "$HOME/../..` + filepath.Join(proj, "post.sh") + `"`} {
		if why := check(bash(cmd, proj)); !strings.HasPrefix(why, reason) {
			t.Errorf("%s: not asked: %q", cmd, why)
		}
	}
	if why := check(bash(`"$HOME/.crius/bin/bus-post" ~/.crius/bus/x.log INFO x:lead crius:lead "ran az boards query, 3 rows"`, proj)); why != "" {
		t.Errorf("bus-post by $HOME: asked: %s", why)
	}
}

// Review r4: a command name the guard cannot read asks when az sits next to a ), `, ], ; or " (not only a space).
func TestACommandNameFromAVariableOrSubshellAsks(t *testing.T) {
	proj := t.TempDir()
	const w = " boards work-item update --id 1 --discussion hi"
	for _, cmd := range []string{
		"X=az; $X" + w,
		"$(which az)" + w,
		`"$(command -v az)"` + w,
		"`which az`" + w,
		"${commands[az]}" + w,
		`zsh -c "$(printf %s az)` + w + `"`,
	} {
		if why := check(bash(cmd, proj)); !strings.HasPrefix(why, reason) {
			t.Errorf("Bash %s: not asked: %q", cmd, why)
		}
		in := hookInput(t, "mcp__plugin_context-mode_context-mode__ctx_execute", map[string]any{"language": "shell", "code": cmd})
		in.Cwd = proj
		if why := check(in); !strings.HasPrefix(why, reason) {
			t.Errorf("ctx_execute %s: not asked: %q", cmd, why)
		}
	}
}
