package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func bash(cmd, cwd string) input {
	in := input{ToolName: "Bash", Cwd: cwd}
	in.ToolInput.Command = cmd
	return in
}

// 10-30: a lead session posted a QA-notes comment and a reply to a colleague on AB#94205 under the owner's name, with
// this exact command shape, and the owner had both deleted. These must wait for the owner's click.
const lead94205 = `az devops invoke --area wit --resource comments --route-parameters project="AppXite Platform" workItemId=94205 --http-method POST --in-file x.json --api-version 7.1-preview --org https://dev.azure.com/appxite`

func TestAzureDevOpsTextWritesAsk(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "history.json"), []byte(`[{"op":"add","path":"/fields/System.History","value":"hi"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{
		lead94205,
		// wrappers, chains and spellings
		`GH_TOKEN=x AZURE_DEVOPS_EXT_PAT=y ` + lead94205,
		`env AZURE_DEVOPS_EXT_PAT=y ` + lead94205,
		`cd /tmp && cat > x.json <<'EOF'
{"text":"QA notes"}
EOF
` + lead94205,
		`true; ` + lead94205 + ` | jq .id`,
		`bash -c "` + strings.ReplaceAll(lead94205, `"`, `\"`) + `"`,
		`bash -lc '` + lead94205 + `'`,
		`timeout 60 az devops invoke --area wit --resource comments --http-method=post --in-file x.json`,
		"az devops invoke \\\n  --area wit \\\n  --resource comments \\\n  --http-method Post \\\n  --in-file x.json",
		`az devops invoke --area=wit --resource=Comments --http-method=PATCH --in-file x.json`,
		`az devops invoke --area wit --resource commentsReactions --http-method PUT`,
		`/opt/homebrew/bin/az devops invoke --area wit --resource comments --http-method POST --in-file x.json`,
		`out=$(az devops invoke --area wit --resource comments --http-method POST --in-file x.json)`,
		`az devops invoke --area wit --resource comments --http-method "$M" --in-file x.json`, // method unknown
		`az devops invoke --area wit --resource "$R" --http-method POST --in-file x.json`,     // resource unknown
		`az devops invoke --area wit --resource workitems --http-method PATCH --in-file history.json`,
		`az devops invoke --area wit --resource workitems --http-method PATCH --in-file missing.json`, // cannot tell
		// PR threads and comments
		`az devops invoke --area git --resource threads --route-parameters project=P repositoryId=R pullRequestId=7 --http-method POST --in-file t.json`,
		`az devops invoke --area git --resource pullRequestThreads --http-method POST --in-file t.json`,
		`az devops invoke --area git --resource pullRequestThreadComments --http-method POST --in-file t.json`,
		`az devops invoke --area wiki --resource pages --http-method PUT --in-file p.json`,
		// boards
		`az boards work-item update --id 94205 --discussion "QA notes: all green"`,
		`az boards work-item update --id 94205 --discussion="QA notes"`,
		`az boards work-item create --title T --type Bug --discussion "first note"`,
		`az boards work-item update --id 94205 --fields "System.History=QA notes"`,
		`az boards work-item update --id 94205 -f System.State=Done "System.History=done"`,
		`az boards work-item update --id 94205 --fields "$F"`, // cannot tell
		// repos
		`az repos pr create --title T --description "Fixes the bug" --source-branch b`,
		`az repos pr create --title T -d "Fixes the bug"`,
		`az repos pr update --id 7 --description "new text"`,
		`az repos pr comment create --id 7 --text hi`, // a comment-like subcommand
		`az devops wiki page update --path /a --wiki W --content hi --version v`,
		// REST through an HTTP tool or az rest
		`curl -X POST -H "Content-Type: application/json" -d @c.json "https://dev.azure.com/appxite/AppXite%20Platform/_apis/wit/workItems/94205/comments?api-version=7.1-preview"`,
		`curl -sS -d '{"text":"hi"}' https://dev.azure.com/appxite/_apis/wit/workItems/1/comments`,
		`curl --request=PATCH https://appxite.visualstudio.com/_apis/wit/workItems/1`,
		`curl -XPOST https://dev.azure.com/appxite/_apis/x`,
		`curl -sSdX https://dev.azure.com/appxite/_apis/x`, // -d takes "X" as its data, not -X
		`curl -sSXPATCH -d @c.json https://dev.azure.com/appxite/_apis/x`,
		`curl --json '{"a":1}' https://dev.azure.com/appxite/_apis/x`,
		`curl -X POST "https://dev.azure.com/$ORG/_apis/wit/workItems/1/comments"`,
		`wget --method=POST --body-data='{"a":1}' https://dev.azure.com/appxite/_apis/x`,
		`wget --post-data='a=1' https://dev.azure.com/appxite/_apis/x`,
		`http POST https://dev.azure.com/appxite/_apis/x text=hi`,
		`xh https://dev.azure.com/appxite/_apis/x text=hi`,
		`az rest --method post --url "https://dev.azure.com/appxite/_apis/wit/workItems/1/comments?api-version=7.1" --body @c.json`,
		`az rest -m PATCH -u https://dev.azure.com/appxite/_apis/x --body '{}'`,
	} {
		if why := check(bash(cmd, dir)); !strings.HasPrefix(why, reason) {
			t.Errorf("not asked: %q: %q", cmd, why)
		}
	}
}

func TestReadsAndOtherWritesPass(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(`[{"op":"add","path":"/fields/System.State","value":"Done"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{
		`az devops invoke --area wit --resource comments --route-parameters project="AppXite Platform" workItemId=94205 --api-version 7.1-preview --org https://dev.azure.com/appxite`,
		`az devops invoke --area wit --resource comments --http-method GET --route-parameters workItemId=94205`,
		`az devops invoke --area git --resource pullRequestThreads --http-method get`,
		`az boards work-item show --id 94205 --org https://dev.azure.com/appxite`,
		`az boards query --wiql "SELECT [System.Id] FROM workitems"`,
		`az repos pr list --status active`,
		`az repos pr show --id 7`,
		`az account show`,
		`az devops invoke --area wit --resource workitems --http-method PATCH --in-file state.json`, // a field-only update (known gap)
		`az boards work-item update --id 94205 --state Done`,                                        // field-only (known gap)
		`az boards work-item update --id 94205 --title T --description D`,                           // field-only (known gap)
		`az boards work-item update --id 94205 --fields System.State=Done`,
		`az repos pr update --id 7 --status completed`,
		`curl -s https://dev.azure.com/appxite/_apis/wit/workItems/94205/comments`,
		`curl -G -d api-version=7.1 https://dev.azure.com/appxite/_apis/x`,
		`curl -X POST https://example.com/api -d x=1`,
		`wget https://dev.azure.com/appxite/_apis/x`,
		`http GET https://dev.azure.com/appxite/_apis/x`,
		`az rest --url https://dev.azure.com/appxite/_apis/x`,
		`echo az devops invoke --area wit --resource comments --http-method POST`,
		`git commit -m "az boards work-item update --discussion x"`,
		`grep -rn "http-method POST" docs/`,
		`gh pr create --title T --body B`,
	} {
		if why := check(bash(cmd, dir)); why != "" {
			t.Errorf("asked: %q: %s", cmd, why)
		}
	}
}

// Review round 1 (Opus, Codex out of quota): shapes that slipped through, each now asked.
func TestReviewRound1BypassesAsk(t *testing.T) {
	dir := t.TempDir()
	for f, body := range map[string]string{
		"state.json":     `[{"op":"add","path":"/fields/System.State","value":"Done"}]`, // field-only on disk
		"sub/state.json": `[{"op":"add","path":"/fields/System.State","value":"Done"}]`,
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	post := `az devops invoke --area wit --resource comments --http-method POST --in-file x.json`
	for _, cmd := range []string{
		// an in-file that is not a regular file, written by the same command, or moved by a cd
		"az devops invoke --area wit --resource workitems --http-method PATCH --in-file /dev/stdin <<'X'\n[{\"op\":\"add\",\"path\":\"/fields/System.History\",\"value\":\"QA\"}]\nX",
		"cat > state.json <<'X'\n[{\"op\":\"add\",\"path\":\"/fields/System.History\",\"value\":\"QA\"}]\nX\naz devops invoke --area wit --resource workitems --http-method PATCH --in-file state.json",
		`cd sub && az devops invoke --area wit --resource workitems --http-method PATCH --in-file state.json`,
		`az devops invoke --area wit --resource workitems --http-method PATCH --in-file sub`, // a folder
		// the host in a variable
		`ORG=https://dev.azure.com/appxite; curl -u ` + `":$PAT" -X POST -d @c.json "$ORG/P/_apis/wit/workItems/94205/comments?api-version=7.1-preview"`,
		`curl -X POST -d @c.json "$SYSTEM_COLLECTIONURI/P/_apis/wit/workItems/1/comments"`,
		`az rest -m post --resource 499b84ac-1321-427f-aa17-267ca6975798 --url "$ADO/x" --body @c.json`,
		// az takes the last copy of an option, and reads a value from @file
		`az devops invoke --area wit --resource comments --http-method GET --http-method POST --in-file x.json`,
		`az devops invoke --area wit --resource workitems --http-method POST --resource comments --in-file x.json`,
		`az boards work-item update --id 1 --fields @f.txt`,
		`az devops invoke --area wit --resource @r.txt --http-method POST --in-file x.json`,
		// a shell reading its script from stdin, text in a variable, eval of unreadable text
		"bash <<'X'\n" + post + "\nX",
		`echo '` + post + `' | bash`,
		`cmd='` + post + `'; eval "$cmd"`,
		`c='` + post + `'; bash -c "$c"`,
		`eval az devops invoke --area wit --resource comments --http-method "$M" --in-file x.json`,
		`bash -c $'cd /tmp\n` + post + `'`,
		// wrappers
		`gtimeout 60 ` + post,
		`op run -- ` + post,
		`watch -n5 '` + post + `'`,
		`env -S '` + post + `'`,
		`sudo -u me ` + post,
		`ls *.json | xargs -I{} ` + post,
		// httpie and xh
		`http https://dev.azure.com/o/_apis/wit/workItems/1/comments text='see https://dev.azure.com/o/p/_git/r/pullrequest/7'`,
		`http https://dev.azure.com/o/_apis/wit/workItems/1/comments < c.json`,
		`cat c.json | xh https://dev.azure.com/o/_apis/wit/workItems/1/comments`,
		// parsing
		`az boards work-item update --id 1 --fi "System.History=x"`,
		`az boards work-item update --id 1 --fields "History=QA notes"`,
		`az repos pr create --title T -d"Fixes the bug"`,
		`'az' 'devops' 'invoke' '--area' 'wit' '--resource' 'comments' '--http-method' 'POST'`,
		`curl -X"$M" -d x https://dev.azure.com/appxite/_apis/x`,
		`curl --request="$M" https://dev.azure.com/appxite/_apis/x`,
		`curl --expand-data '{{x}}' https://dev.azure.com/appxite/_apis/x`,
		`wget --method="$M" --body-data=x https://dev.azure.com/appxite/_apis/x`,
	} {
		if why := check(bash(cmd, dir)); !strings.HasPrefix(why, reason) {
			t.Errorf("not asked: %q: %q", cmd, why)
		}
	}
}

// Review round 1: reads that asked.
func TestReviewRound1ReadsPass(t *testing.T) {
	for _, cmd := range []string{
		`az rest --method post --url "https://dev.azure.com/appxite/P/_apis/wit/wiql?api-version=7.1" --body '{"query":"SELECT"}'`,
		`curl -d '{"ids":[1,2]}' "https://dev.azure.com/appxite/_apis/wit/workitemsbatch?api-version=7.1"`,
		`curl -sS -H "Authorization: Basic $T" -d '{"searchText":"foo"}' https://almsearch.dev.azure.com/appxite/_apis/search/codesearchresults`,
		`http https://dev.azure.com/appxite/_apis/x "Authorization:Basic OjEyMzQ="`,
		`http -a me@x.com:pat https://dev.azure.com/appxite/_apis/x`,
		`xh https://dev.azure.com/appxite/_apis/x api-version==7.1`,
		`curl -u ` + `":$PAT" https://dev.azure.com/appxite/_apis/x`, // split: the repo holds no token shape
		`wget -qO- https://dev.azure.com/appxite/_apis/x`,
		`curl -s "$ORG/_apis/wit/workItems/1/comments"`,
		`echo '{"a":1}' | az devops invoke --area wit --resource comments --route-parameters workItemId=1`,
	} {
		if why := check(bash(cmd, t.TempDir())); why != "" {
			t.Errorf("asked: %q: %s", cmd, why)
		}
	}
}

// Review round 2.
func TestReviewRound2(t *testing.T) {
	post := `az devops invoke --area wit --resource comments --http-method POST --in-file x.json`
	for _, cmd := range []string{
		`bash -euo pipefail -c '` + post + `'`,
		`bash -euxo pipefail -c '` + post + `'`,
		`sh -eo pipefail -c '` + post + `'`,
		`bash +o posix -c '` + post + `'`,
		`bash -O extglob -c '` + post + `'`,
		`echo '` + post + `' | bash -`,
		"bash - <<'X'\n" + post + "\nX",
		`echo '` + post + `' | bash -s -- a`,
		`bash -c -- '` + post + `'`,
		`U="$ORG/P/_apis/wit/workItems/1/comments?api-version=7.1-preview"; curl -X POST -d @c.json "$U"`,
		`curl -X POST -H "Authorization: Bearer $AZURE_DEVOPS_EXT_PAT" -d @c.json "$URL"`,
		`direnv exec . ` + post,
		`mise exec -- ` + post,
		`arch -arm64 ` + post,
		`setsid ` + post,
		`su me -c '` + post + `'`,
		`curl -X POST -d @c.json 'https://dev.azure.com/o/_apis/wit/workItems/1/comments?x=/_apis/wit/wiql'`,
		`xh --bearer abc POST https://dev.azure.com/o/_apis/wit/workItems/1/comments text=hi`,
		`echo POST | xargs az devops invoke --area wit --resource comments --in-file x.json --http-method`,
		`echo https://dev.azure.com/o/_apis/wit/workItems/1/comments | xargs curl -X POST -d @c.json`,
		`az devops invoke --area git --resource statuses --http-method POST --in-file s.json`,
		`az -o json devops invoke --area wit --resource comments --http-method POST --in-file x.json`,
		`python3 -c 'import requests; requests.post("https://dev.azure.com/o/_apis/wit/workItems/1/comments", json={})'`,
		`bun -e 'await fetch("https://dev.azure.com/o/_apis/x", {method: "PATCH"})'`,
	} {
		if why := check(bash(cmd, t.TempDir())); !strings.HasPrefix(why, reason) {
			t.Errorf("not asked: %q: %q", cmd, why)
		}
	}
	for _, cmd := range []string{
		`timeout 30 rg 'az boards work-item update --discussion' docs/`,
		`find . -name '*.md' -exec grep -l 'az devops invoke --http-method POST' {} +`,
		`xargs -0 grep -n 'curl -X POST https://dev.azure.com/o/_apis/x'`,
		`curl -X POST -d @q.json https://dev.azure.com/o/P/_apis/git/repositories/R/commitsbatch`,
		`bash -euo pipefail -c 'az boards work-item show --id 1'`,
		`python3 -c 'print("dev.azure.com")'`,
		`ls | xargs -I{} az boards work-item show --id {}`,
	} {
		if why := check(bash(cmd, t.TempDir())); why != "" {
			t.Errorf("asked: %q: %s", cmd, why)
		}
	}
}

// Review round 3 (P1 = 0); the cheap P2s.
func TestReviewRound3(t *testing.T) {
	post := `az devops invoke --area wit --resource comments --http-method POST --in-file x.json`
	for _, cmd := range []string{
		`uv run ` + post,
		`poetry run ` + post,
		`doppler run -- ` + post,
		`dotenvx run -- curl -X POST -d @c.json https://dev.azure.com/o/_apis/wit/workItems/1/comments`,
		`xh --http-version 1.1 POST https://dev.azure.com/o/_apis/wit/workItems/1/comments text=hi`,
		`http --response-charset utf8 POST https://dev.azure.com/o/_apis/wit/workItems/1/comments text=hi`,
		"python3 - <<'X'\nimport requests; requests.post('https://dev.azure.com/o/_apis/x')\nX",
	} {
		if why := check(bash(cmd, t.TempDir())); !strings.HasPrefix(why, reason) {
			t.Errorf("not asked: %q: %q", cmd, why)
		}
	}
	for _, cmd := range []string{
		`curl -s -X POST -d '{"query":"SELECT [System.Id] FROM workitems"}' "https://dev.azure.com/o/P/_apis/wit/wiql?api-version=7.1" | python3 -m json.tool`,
		`az rest --method post --url "https://dev.azure.com/o/P/_apis/wit/wiql?api-version=7.1" --body @q.json | python3 -m json.tool`,
		`curl -s -X POST -d @q.json "https://dev.azure.com/o/P/_apis/wit/wiql" | python3 summarize.py`,
	} {
		if why := check(bash(cmd, t.TempDir())); why != "" {
			t.Errorf("asked: %q: %s", cmd, why)
		}
	}
}

func TestOtherToolsPass(t *testing.T) {
	in := input{ToolName: "Grep"}
	in.ToolInput.Command = lead94205
	if why := check(in); why != "" {
		t.Errorf("a Grep call was asked: %s", why)
	}
}

// decision runs the hook as Claude Code does and gives its permissionDecision ("" when it prints nothing).
func decision(t *testing.T, stdin string) (string, string) {
	t.Helper()
	var out bytes.Buffer
	if err := run(strings.NewReader(stdin), &out); err != nil {
		t.Fatal(err)
	}
	if out.Len() == 0 {
		return "", ""
	}
	var got struct {
		HookSpecificOutput struct {
			HookEventName            string `json:"hookEventName"`
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output is not JSON: %q", out.String())
	}
	if got.HookSpecificOutput.HookEventName != "PreToolUse" {
		t.Fatalf("hookEventName: %q", got.HookSpecificOutput.HookEventName)
	}
	return got.HookSpecificOutput.PermissionDecision, got.HookSpecificOutput.PermissionDecisionReason
}

func TestTheHookPrintsAskForAWriteAndNothingForARead(t *testing.T) {
	stdin, _ := json.Marshal(map[string]any{"tool_name": "Bash", "cwd": t.TempDir(), "tool_input": map[string]string{"command": lead94205}})
	if d, why := decision(t, string(stdin)); d != "ask" || !strings.HasPrefix(why, reason) {
		t.Errorf("write: %q %q", d, why)
	}
	stdin, _ = json.Marshal(map[string]any{"tool_name": "Bash", "tool_input": map[string]string{"command": "az boards work-item show --id 94205"}})
	if d, why := decision(t, string(stdin)); d != "" {
		t.Errorf("read: %q %q", d, why)
	}
}

// Unreadable input never lets a write through silently: the owner is asked.
func TestUnreadableInputAsks(t *testing.T) {
	if d, why := decision(t, `{"tool_name":"Bash","tool_input":`); d != "ask" || !strings.Contains(why, "could not read") {
		t.Errorf("%q %q", d, why)
	}
}

// A command the shell parser cannot read asks when it names Azure DevOps, and passes otherwise.
func TestUnparsableCommands(t *testing.T) {
	if why := check(bash(`az devops invoke --area wit --resource comments --http-method POST "unclosed`, "")); !strings.HasPrefix(why, reason) {
		t.Errorf("unparsable ADO command not asked: %q", why)
	}
	if why := check(bash(`echo "unclosed; az`, "")); !strings.HasPrefix(why, reason) {
		t.Errorf("unparsable command ending in az not asked: %q", why)
	}
	if why := check(bash(`echo "unclosed`, "")); why != "" {
		t.Errorf("unparsable other command asked: %q", why)
	}
}

// Crius 10-30 20:27: the quotes lead's bus post asked the owner as an Azure DevOps write. The script's name came from
// $S, so the guard could not read it, and the bus text named az ("az blob list"). S is set to a literal earlier in the
// same command: the guard reads the script and checks its own commands.
func TestAScriptNamedThroughAVariableSetInTheCommandIsRead(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "lead"), 0o755); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile("testdata/say.sh")
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, text string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("lead/say.sh", string(body))
	if err := os.Symlink(filepath.Join(dir, "lead/say.sh"), filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	rig, err := os.ReadFile("testdata/rig-say.sh")
	if err != nil {
		t.Fatal(err)
	}
	write("rig.sh", string(rig))
	write("post.sh", "#!/bin/bash\n"+lead94205+"\n")
	write("relay.sh", "#!/bin/bash\naz devops invoke --area wit --resource comments --http-method \"$1\" --in-file x.json\n")
	write("chain.sh", "#!/bin/bash\nbash \""+dir+"/post.sh\"\n")
	write("safe.sh", "#!/bin/bash\necho hi\n")
	write("src.sh", "#!/bin/bash\nsource ./lib.sh\n")
	write("fn.sh", "#!/bin/bash\nrun() { \"$@\"; }\nrun \"$@\"\n")
	write("args.sh", "#!/bin/bash\n\"$1\" \"${@:2}\"\n")
	write("wrap.sh", "#!/bin/bash\ntimeout 60 \"$@\"\n")
	write("py.sh", "#!/bin/bash\npython3 post.py\n")
	write("z2.sh", "#!/bin/zsh\nprint ${(j:,:)arr}\n")
	// review of ace0c14: az quoted, and a script that runs its arguments one step removed
	write("q.sh", "#!/bin/bash\n\"az\" boards work-item update --id 1 --discussion \"$1\"\n")
	write("ind1.sh", "#!/bin/bash\nc=$1; shift; \"$c\" \"$@\"\n")
	write("ind2.sh", "#!/bin/bash\ncmd=(\"$@\"); \"${cmd[@]}\"\n")
	write("ind3.sh", "#!/bin/bash\n$SHELL -c \"$1\"\n")
	write("ind4.sh", "#!/bin/bash\ntrap \"$1\" EXIT\n")
	write("ind5.sh", "#!/bin/bash\nawk \"BEGIN{system(ARGV[1])}\" \"$1\"\n")
	write("ind6.sh", "#!/bin/bash\nPATH=$1:$PATH; grep x y\n")
	write("ind7.sh", "#!/bin/bash\ngit -c \"alias.x=!$1\" x\n")
	write("quiet.sh", "#!/bin/bash\nexec 2>/dev/null\necho usage: bash x.sh \"$1\" >&2\n")
	// review of 377cc0a: code run through a shell or git setting, a file the script writes, a shell's start-up file
	write("ps4.sh", "#!/bin/bash\nPS4='$(a'z' boards work-item update --id 1 --discussion \"$1\")'\nset -x\necho \"$1\"\n")
	write("benv.sh", "#!/bin/bash\nBASH_ENV=/tmp/p.sh bash -c :\n")
	write("rc.sh", "#!/bin/bash\nbash --rcfile /tmp/p.sh -ic :\n")
	write("gitenv.sh", "#!/bin/bash\nGIT_EXTERNAL_DIFF=/tmp/p.sh git diff\n")
	write("decl.sh", "#!/bin/bash\ndeclare \"$1\"; grep x y\n")
	write("over.sh", "#!/bin/bash\nprintf x > \"$HOME/.crius/bin/bus-post\"; \"$HOME/.crius/bin/bus-post\"\n")
	write("home.sh", "#!/bin/bash\nHOME=/tmp/evil; \"$HOME/.crius/bin/bus-post\"\n")
	write("log.sh", "#!/bin/bash\necho \"$1\" >> /tmp/x.log\n")
	write("co.sh", "#!/bin/bash\nbash -co\n")
	// review of 26148a0: a computed git word that becomes an option, a shortened option that writes, git's config folder
	write("g1.sh", "#!/bin/bash\ngit -C /tmp grep \"-O$1\" -e hello -- n.txt\n")
	write("g2.sh", "#!/bin/bash\ngit -C /tmp grep \"$1\" -- n.txt\n")
	write("g3.sh", "#!/bin/bash\ngit -C /tmp grep --open-files=less -e hi\n")
	write("g4.sh", "#!/bin/bash\ngit -C /tmp log -1 --out=/tmp/x\n")
	write("g5.sh", "#!/bin/bash\nXDG_CONFIG_HOME=/tmp/y git -C /tmp show HEAD\n")
	write("g6.sh", "#!/bin/bash\ngit -C /tmp grep -q -e \"$1\" -- n.txt; git -C /tmp log -1 --oneline -- \"$2\"\n")
	self := "#!/bin/bash\n" + strings.Repeat("bash "+filepath.Join(dir, "self.sh")+"\n", 15)
	write("self.sh", self)
	s := `S=` + dir + `; `
	run := ` az boards work-item update --id 1 --discussion hi`
	say2027 := s + `cd /tmp && echo "- 10-30 $(date +%H:%M) lead: rig-scan-tag died 20:25 LOUD (az blob list to Azurite :10000 read timed out at 60 s; machine load 197)." >> /tmp/TRIAGE.md && bash $S/lead/say.sh INFO "crius:lead,quotes:qa1" "Lead $(date +%H:%M): rig-scan-tag died 20:25 (Azurite read timeout, machine load 197); restarted pid 9819."; echo rc=$?`
	for _, cmd := range []string{
		say2027,
		s + `bash "$S/lead/say.sh" INFO crius:lead "az login works again"`,
		s + `bash ${S}/lead/say.sh INFO crius:lead "az login works"; bash $S/lead/say.sh INFO crius:lead "az again"`,
		`export S=` + dir + `; bash $S/lead/say.sh INFO crius:lead "az ok"`,
		`bash ` + dir + `/lead/say.sh INFO crius:lead "az ok"`,
		s + `cd /tmp && bash $S/lead/say.sh INFO crius:lead "az login ok"`, s + `bash $S/safe.sh 'az login'`,
		// review of ace0c14: S set in an && chain, a shell after command, a wrapper body that only logs and prints
		`S=` + dir + ` && bash $S/lead/say.sh INFO x 'az login'`, s + `command bash $S/lead/say.sh INFO x 'az login'`,
		s + `bash $S/quiet.sh 'az login'`, s + `bash $S/rig.sh INFO crius:lead 'az blob list timed out'`,
		// review of 377cc0a: a glob or a folder of the same name elsewhere in the command is no copy of the script
		s + `tail -3 ~/.crius/runs/* ; bash $S/lead/say.sh INFO x 'az blob list ok'`,
		s + `ls /tmp/*; bash $S/lead/say.sh INFO x 'az blob list ok'`,
		s + `tail -3 ~/Projects/other/lead/STATUS.md; bash $S/lead/say.sh INFO x 'az blob list ok'`,
		s + `bash -e $S/lead/say.sh INFO x 'az blob list ok'`,
		// review of a4106c1: S set after a cd in an && chain, HOME= in the bus text, -- before the script
		`cd /tmp && S=` + dir + ` && bash $S/lead/say.sh INFO x 'az blob list ok'`,
		s + `bash $S/lead/say.sh INFO x 'HOME=/tmp az blob list ok'`,
		s + `bash -x -- $S/lead/say.sh INFO x 'az blob list ok'`,
		// review of 516ec50: a dot after cd, a code variable's name in notes text, a bare glob
		s + `echo '- 10-30 lead: fixed PATH in rig' >> /tmp/TRIAGE.md && bash $S/lead/say.sh INFO crius:lead 'az blob list ok'`,
		s + `cd ~/Projects && ls *; grep -rn foo . ; bash $S/lead/say.sh INFO x 'az blob list ok'`,
		// review of 26148a0: a computed git word as -e's pattern or after --
		s + `bash $S/g6.sh 'az blob list' 'az login'`,
	} {
		if why := check(bash(cmd, dir)); why != "" {
			t.Errorf("%q asked: %s", cmd, why)
		}
	}
	for _, cmd := range []string{
		s + `bash $S/post.sh`,       // the script itself posts a comment
		s + `bash $S/relay.sh POST`, // a method from its arguments
		`bash ` + dir + `/post.sh`,  // a literal name is read too
		s + `cat > $S/lead/say.sh <<'EOF'` + "\n" + lead94205 + "\nEOF\nbash $S/lead/say.sh", // written by this command
		s + `S=/elsewhere; bash $S/lead/say.sh INFO x "az login"`,                            // set twice: which one runs is not read
		`bash $S/lead/say.sh INFO x "az login"`,                                              // S not set in the command
		`bash $S/lead/say.sh INFO x "az login"; S=` + dir,                                    // set after the run
		s + `S=$(pwd) bash $S/lead/say.sh INFO x "az login"`,                                 // computed
		`for S in ` + dir + `; do bash $S/lead/say.sh INFO x "az login"; done`,
		s + `bash $S/missing.sh INFO x "az login"`,
		// review of 6cbb7b7: a script this command rewrites, writes the guard cannot see, code the script runs
		`F=` + dir + `/safe.sh; cat > "$F" <<'EOF'` + "\n" + lead94205 + "\nEOF\nbash \"$F\"",
		`F=` + dir + `/safe.sh; printf '%s' x | tee "$F" >/dev/null; bash $F 'az login'`,
		`T=` + dir + `/safe.sh; cp post.sh "$T"; bash $T 'az login'`,
		s + `find . -name post.sh -exec cp {} ` + dir + `/safe.sh \; ; bash $S/safe.sh 'az login'`,
		s + `sudo tee ` + dir + `/safe.sh < post.sh; bash $S/safe.sh 'az login'`,
		s + `bash -c 'cp post.sh safe.sh'; bash $S/safe.sh 'az login'`,
		s + `python3 -c 'import shutil; shutil.copy("post.sh", "safe.sh")'; bash $S/safe.sh 'az login'`,
		s + `git -C $S pull -q && bash $S/safe.sh 'az login'`,
		s + `bash $S/src.sh 1 'see https://dev.azure.com/o/p'`, s + `bash $S/fn.sh az boards work-item update --discussion x`,
		s + `bash $S/args.sh az boards x`, s + `bash $S/wrap.sh az boards x`, s + `bash $S/py.sh 'az login'`,
		s + `zsh $S/z2.sh POST https://dev.azure.com/o/p/_apis/wit/workItems/1/comments`,
		s + `eval 'S=/elsewhere'; bash $S/safe.sh 'az login'`,
		s + `f() { local S; bash $S/safe.sh 'az login'; }; f`, s + `if true; then S=/x; fi; bash $S/safe.sh 'az login'`,
		`cd /tmp && bash safe.sh 'az login'`, // a relative name after cd is not the file the guard can read
		// review of ace0c14
		s + `bash $S/chain.sh`, // a script that runs one that posts
		s + `bash $S/q.sh hello`, s + `bash $S/ind1.sh` + run, s + `bash $S/ind2.sh` + run, s + `bash $S/ind3.sh '` + run + `'`,
		s + `bash $S/ind4.sh '` + run + `'`, s + `bash $S/ind5.sh '` + run + `'`, s + `bash $S/ind6.sh /tmp/az-bin 'az login'`,
		s + `bash $S/ind7.sh '` + run + `'`,
		s + `cp q.sh ` + dir + `/saf[e].sh; bash $S/safe.sh 'az login'`,
		s + `cp q.sh ` + dir + `/saf""e.sh; bash $S/safe.sh 'az login'`,
		s + `ln -sfn ` + dir + `/evil ` + dir + `/lead; bash $S/lead/say.sh INFO x 'az login'`,
		// review of 377cc0a
		`bash ` + dir + `/ps4.sh 'az ok'`, s + `bash $S/ps4.sh 'az ok'`, s + `bash $S/benv.sh 'az login'`,
		s + `bash $S/rc.sh 'az login'`, s + `bash $S/gitenv.sh 'az login'`, s + `bash $S/decl.sh PA""TH=/tmp/az-bin 'az login'`,
		s + `bash $S/over.sh 'az login'`, s + `bash $S/home.sh 'az login'`, s + `bash $S/log.sh 'az login'`,
		s + `bash $S/self.sh 'az login'`,
		s + `PS4='$(az boards work-item update --id 1)' bash -x $S/lead/say.sh INFO x hi`,
		s + `BASH_ENV=/tmp/p.sh bash $S/lead/say.sh INFO x 'az login'`,
		s + `export BASH_ENV=/tmp/p.sh; bash $S/lead/say.sh INFO x 'az login'`,
		s + `bash --rcfile /tmp/p.sh -i $S/lead/say.sh INFO x 'az login'`,
		`false && S=` + dir + `; bash $S/safe.sh 'az login'`, // S is not set when false fails
		// review of a4106c1: a code variable set without NAME=, a function exported, a copy into a folder above the script
		s + `read PS4 <<< '$(az boards work-item update --id 1)'; bash -x $S/lead/say.sh INFO x hi`,
		s + `printf -v PS4 '%s' '$(az boards work-item update --id 1)'; bash -x $S/lead/say.sh INFO x hi`,
		s + `declare -x "P"S4='$(az boards work-item update --id 1)'; bash -x $S/lead/say.sh INFO x hi`,
		s + `set -a; read PS4 <<< '$(az boards work-item update --id 1)'; bash -x $S/lead/say.sh INFO x hi`,
		s + `echo 'az boards work-item update --id 1' > /tmp/e.sh; read BASH_ENV <<< /tmp/e.sh; bash $S/lead/say.sh INFO x hi`,
		s + `v=P; read "${v}S4" <<< '$(az boards work-item update --id 1)'; bash -x $S/lead/say.sh INFO x hi`,
		s + `echo() { az boards work-item update --id 1; }; export -f echo; bash $S/lead/say.sh INFO x hi`,
		s + `ZDOTDIR=/tmp/z zsh $S/lead/say.sh INFO x 'az login'`,
		s + `echo 'az boards work-item update --id 1' >> /tmp/z/.zshenv; zsh $S/lead/say.sh INFO x hi`,
		s + `cp /tmp/x/* ` + dir + `/lead/.; bash $S/lead/say.sh INFO x 'az login'`,
		s + `cp /tmp/x/* ` + dir + `/lea[d]/; bash $S/lead/say.sh INFO x 'az login'`,
		s + `tar -C ` + dir + `/. -xf /tmp/x.tar; bash $S/lead/say.sh INFO x 'az login'`,
		s + `cp -R /tmp/x/. ` + dir + `/lead/./; bash $S/lead/say.sh INFO x 'az login'`,
		s + `cp /tmp/evil ~/.crius/bin/; bash $S/lead/say.sh INFO x 'az login'`,
		s + `bash $S/co.sh 'az login'`, // bash -co with no text after it (it crashed the guard)
		// review of 516ec50: a code variable set by ${X:=}, a loop or -vNAME with set -a; a write at or above home or into
		// the command's own folder; a redirect into bus-post's folder, through a link, or to a {fd} named PATH
		s + `echo 'az boards work-item update --id 1' > /tmp/e.sh; set -a; : ${BASH_ENV:=/tmp/e.sh}; bash $S/lead/say.sh INFO x hi`,
		s + `echo 'az boards work-item update --id 1' > /tmp/e.sh; bash $S/lead/say.sh INFO x "${BASH_ENV:=/tmp/e.sh}"`,
		s + `set -a; for PS4 in '$(az boards work-item update --id 1)'; do :; done; bash -x $S/lead/say.sh INFO x hi`,
		s + `set -a; printf -vPS4 '%s' '$(az boards work-item update --id 1)'; bash -x $S/lead/say.sh INFO x hi`,
		s + `tar -C ~ -xf /tmp/evil.tar; bash $S/lead/say.sh INFO x 'az login'`,
		s + `rsync -a /tmp/evil/ $HOME/; bash $S/lead/say.sh INFO x 'az login'`,
		s + `tar -xf /tmp/evil.tar; bash $S/lead/say.sh INFO x 'az login'`,
		s + `echo x > ~/.crius/bin/bus-post; bash $S/lead/say.sh INFO x 'az login'`,
		s + `echo x >> ` + dir + `/lead/say.sh; bash $S/lead/say.sh INFO x 'az login'`,
		s + `echo x > ` + dir + `/link.txt; bash $S/lead/say.sh INFO x 'az login'`,
		s + `exec {PATH}>/tmp/x; bash $S/lead/say.sh INFO x 'az login'`,
		s + `(( PATH=1 )); bash $S/lead/say.sh INFO x 'az login'`,
		s + `for PATH in /tmp/az-bin; do :; done; bash $S/lead/say.sh INFO x 'az login'`, // PATH is exported already
		s + `cd ` + dir + `/lead && echo x > say.sh; bash $S/lead/say.sh INFO x 'az login'`,
		// review of 26148a0: commands that write on their own (awk >, git --output), a target bash expands and the guard
		// does not (a glob, ~user, ~+), git's config folder, a computed git word or a shortened option, and a $(...) in a
		// value bash reads as a number or a prompt; git around the script asks with them
		s + `awk 'BEGIN{print "x" > "` + dir + `/lead/say.sh"}'; bash $S/lead/say.sh INFO x 'az login'`,
		s + `git -C /tmp log -1 --format=x --output=$S/lead/say.sh; bash $S/lead/say.sh INFO x 'az login'`,
		s + `printf x > ` + dir + `/lea[d]/say.sh; bash $S/lead/say.sh INFO x 'az login'`,
		s + `printf x > ` + dir + `/l*/say.sh; bash $S/lead/say.sh INFO x 'az login'`,
		s + `printf x > ~` + os.Getenv("USER") + `/.crius/bin/bus-post; bash $S/lead/say.sh INFO x 'az login'`,
		s + `cd ~ && printf x > ~+/.crius/bin/bus-post; bash $S/lead/say.sh INFO x 'az login'`,
		s + `XDG_CONFIG_HOME=/tmp/y bash $S/lead/say.sh INFO x 'az login'`,
		s + `XDG_CONFIG_HOME=/tmp/y; bash $S/lead/say.sh INFO x 'az login'`,
		s + `bash $S/g1.sh '` + run + `;:'`, s + `bash $S/g2.sh '-O` + run + `;:'`, s + `bash $S/g3.sh 'az login'`,
		s + `bash $S/g4.sh 'az login'`, s + `bash $S/g5.sh 'az login'`,
		s + `X='a[$(az boards work-item update --id 1)]'; for ((X;0;)); do :; done; bash $S/lead/say.sh INFO x hi`,
		s + `X='a[$(az boards work-item update --id 1)]'; bash $S/lead/say.sh INFO x "${S:X}"`,
		s + `X='$(az boards work-item update --id 1)'; bash $S/lead/say.sh INFO x "${X@P}"`,
		s + `cd ~/Projects && git status --short . && bash $S/lead/say.sh INFO crius:lead 'az blob list ok'`,
		s + `export PAGER='az boards work-item update --id 1'; bash $S/lead/say.sh INFO x hi`, // a name no script word uses
		`HOME=` + dir + `; bash $HOME/lead/say.sh INFO x 'az login'`,                          // a code variable the path uses
		s + `bash $S/lead/say.sh INFO x "${S:X} az login"`,                                    // X from the environment
		s + `: {PATH}>/tmp/x; bash $S/lead/say.sh INFO x 'az login'`,
		`XDG_CONFIG_HOME=` + dir + `; bash $XDG_CONFIG_HOME/lead/say.sh INFO x 'az login'`,
	} {
		if why := check(bash(cmd, dir)); !strings.Contains(why, "owner's click") {
			t.Errorf("%q: %q", cmd, why)
		}
	}
}
