package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 11-05 18:29-18:58: 14 asks and 3 real posts. The false asks were local scripts that only read Azure DevOps
// (review-gate.sh, requeue-all.sh, prwatch.sh, a heredoc-written b13check.sh), each reported as "a shell script the guard
// cannot read" though the guard could read it. Owner 11-05: read the script and ask only when what it runs posts; a script
// the guard truly cannot read still asks and says why. testdata/review-gate.sh and requeue-all.sh are the quotes lead's
// scripts with the org, project, repository and paths replaced.

func contentsDir(t *testing.T) string {
	t.Helper()
	files := map[string]string{
		"prwatch.sh": "#!/bin/bash\nfor a in \"$@\"; do pr=${a%%:*}; az pipelines runs list --org https://dev.azure.com/example " +
			"--project \"Example Project\" --branch refs/pull/$pr/merge --top 1 --query \"[0].[id,status,result]\" -o tsv; done\n",
		"pr-text-check.sh": "#!/bin/bash\n/usr/bin/grep -n -E 'TODO|FIXME' \"$1\" && exit 1; echo ok\n",
		"pr.sh":            "#!/bin/bash\nset -e\naz repos pr create --title t --description \"$(cat d.md)\"\n",
		"outer.sh":         "#!/bin/bash\necho start\nbash \"$(dirname \"$0\")/pr.sh\"\n",
		"outer2.sh":        "#!/bin/bash\necho start\nbash DIR/pr.sh\n",
		"post.sh":          "#!/bin/bash\necho hi\naz boards work-item update --id 1 --discussion \"$1\"\n",
		"read.ts": "const r = Bun.spawnSync(['az', 'repos', 'pr', 'show', '--id', '7', '-o', 'json']);\nconsole.log(r.stdout.toString());\n" +
			// 11-05 verdict-check.ts line 12 and ado-suite.ts line 25: a variable after the options of a read; az in a message
			"const pr = process.argv[2];\nspawnSync('az', ['repos', 'pr', 'show', '--id', pr, '--query', 'description', '-o', 'tsv']);\n" +
			"if (!r.stdout) die(2, 'no az token (az login?)');\n",
		"vote.ts": "const pr = process.argv[2];\nspawnSync('az', ['repos', 'pr', 'set-vote', '--id', pr, '--vote', 'approve']);\n",
		"desc.ts": "const d = process.argv[2];\n// no az token (az login?)\nspawnSync('az', ['repos', 'pr', 'update', '--id', '7', d]);\n",
		"post.ts": "const ORG = 'https://dev.azure.com/example';\nconst t = process.env.T;\n" +
			"await fetch(`${ORG}/_apis/pipelines/1/runs?api-version=7.1`, {\n  method: 'POST',\n  body: '{}',\n});\n",
		"noread.sh": "#!/bin/bash\necho hi\n",
		"bin.sh":    "\x7fELF\x02\x01\x01\x00\x00\x00",
	}
	dir := scriptDir(t, files)
	for _, name := range []string{"review-gate.sh", "requeue-all.sh"} {
		b, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	o2 := filepath.Join(dir, "outer2.sh")
	b, _ := os.ReadFile(o2)
	if err := os.WriteFile(o2, []byte(strings.ReplaceAll(string(b), "DIR", dir)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "noread.sh"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(dir, "noread.sh"), 0o644) })
	return dir
}

func TestScriptsThatOnlyReadAzureDevOpsPass(t *testing.T) {
	dir := contentsDir(t)
	s := `S=` + dir + `; `
	for _, cmd := range []string{
		// the logged shapes (18:35, 18:39, 18:57, 18:59)
		`bash ` + dir + `/review-gate.sh 7773; echo rc=$?; cat ` + dir + `/x.log`,
		s + `bash $S/review-gate.sh 7779; echo gate=$?`,
		s + `O=https://dev.azure.com/example; for p in 7773 7774; do echo "== $p"; bash $S/review-gate.sh $p; done`,
		`timeout 60 bash ` + dir + `/prwatch.sh 7773:999999999 7774:126800; echo rc=$?`,
		`bash ` + dir + `/requeue-all.sh; echo rc=$?`,
		// a script the same command writes with a heredoc, then runs (18:41 b13check.sh)
		`cat > ` + dir + `/b13check.sh <<'EOF'` + "\n#!/bin/bash\n# b13check.sh <pr> <build>\nh=$(az repos pr show --id \"$1\" --query lastMergeSourceCommit.commitId -o tsv)\naz pipelines runs show --id \"$2\" --query result -o tsv\necho \"$h\"\nEOF\nbash " + dir + `/b13check.sh 7773 126800`,
		// a script run by its path (18:29)
		dir + `/pr-text-check.sh ` + dir + `/PR.md && echo ok`,
		// the description is only read
		`az repos pr show --id 7774 --org https://dev.azure.com/example --query description -o tsv`,
		`az repos pr show --org https://dev.azure.com/example --id 7 --query "join(' ', [title, description || ''])" -o tsv`,
		// TypeScript that only reads
		`bun ` + dir + `/read.ts`,
	} {
		if why := check(bash(cmd, dir)); why != "" {
			t.Errorf("%q asked: %s", cmd, why)
		}
	}
}

func TestScriptsThatPostAskAndNameTheLine(t *testing.T) {
	dir := contentsDir(t)
	for cmd, want := range map[string][]string{
		`bash ` + dir + `/pr.sh`: {"(az repos pr create --description; " + dir + "/pr.sh line 3)"},
		// a heredoc written in the same command is what runs
		`cat > ` + dir + `/new.sh <<'EOF'` + "\n#!/bin/bash\naz repos pr update --id 7 --description x\nEOF\nbash " + dir + `/new.sh`: {
			"(az repos pr update --description; the heredoc that writes " + dir + "/new.sh line 2)"},
		// a script that runs one that posts names both lines
		`bash ` + dir + `/outer2.sh`: {"(az repos pr create --description; " + dir + "/pr.sh line 3; " + dir + "/outer2.sh line 3)"},
		// run by its path
		`./post.sh hi`: {"(az boards work-item update", "; " + dir + "/post.sh line 3)"},
		// $(dirname "$0") is the script's folder
		`bash ` + dir + `/outer.sh`: {"(az repos pr create --description; " + dir + "/pr.sh line 3; " + dir + "/outer.sh line 3)"},
		`bun ` + dir + `/post.ts`:   {"Azure DevOps", dir + "/post.ts line 4"},
		`bun ` + dir + `/vote.ts`:   {"(az repos pr set-vote; " + dir + "/vote.ts line 2)"},
		`bun ` + dir + `/desc.ts`:   {"(az repos pr update with words the guard cannot read; " + dir + "/desc.ts line 3)"},
	} {
		why := check(bash(cmd, dir))
		for _, w := range want {
			if !strings.Contains(why, w) {
				t.Errorf("%q: want %q in %q", cmd, w, why)
			}
		}
	}
}

// A file the guard cannot open asks and says why when the command names az or Azure DevOps, as an unread script did
// before 11-05 (here "7773 https://dev.azure.com/example"); in other work it passes: a missing ./build/app is no post.
func TestScriptsTheGuardCannotOpenAskAndSayWhy(t *testing.T) {
	dir := contentsDir(t)
	const ado = " 7773 https://dev.azure.com/example"
	for cmd, want := range map[string]string{
		`cd /tmp; cd /usr; bun x.ts`:  "cannot read the code file x.ts: the command changes folder in a way the guard does not follow",
		`bash ` + dir + `/nothere.sh`: "cannot read " + dir + "/nothere.sh: it does not exist",
		`bash ` + dir + `/noread.sh`:  "cannot read " + dir + "/noread.sh: permission denied",
		`bash ` + dir + `/bin.sh`:     "cannot read " + dir + "/bin.sh: it is not text",
		`bash $(mktemp -d)/x.sh`:      "cannot read the shell script $(mktemp -d)/x.sh: its path is built from text the guard cannot work out",
		`bun ` + dir + `/nothere.ts`:  "cannot read " + dir + "/nothere.ts: it does not exist",
	} {
		if why := check(bash(cmd+ado, dir)); !strings.HasPrefix(why, reason) || !strings.Contains(why, want) {
			t.Errorf("%q: want %q in %q", cmd+ado, want, why)
		}
		if why := check(bash(cmd, dir)); why != "" {
			t.Errorf("%q (no az) asked: %s", cmd, why)
		}
	}
}

// Review of the 11-05 change (Opus fallback for Codex): posts that passed. Each command runs a file that posts and names
// neither az nor Azure DevOps itself.
func TestReviewOfTheScriptReadingPostsAsk(t *testing.T) {
	dir := scriptDir(t, map[string]string{
		"sub/post.sh": "#!/bin/bash\naz repos pr create --title x\n",
		"sub/read.sh": "#!/bin/bash\naz repos pr show --id 1\n",
		"env.sh":      "export X=1\n",
		"pos.js":      "require('child_process').execSync(\"az repos pr create --title x\");\n",
		"pos.py":      "import subprocess\nsubprocess.run(\"az repos pr create --title x\", shell=True)\n",
		// a string cut at an inner quote, joined to more text, an f-string, a list over several lines, a """ string
		// with \ lines, az by its path
		"rest.js":  "execSync(\"az rest --method patch --url 'https://dev.azure.com/org/_apis/wit/workitems/1?api-version=7.0' --body @b.json\");\n",
		"inv.js":   "execSync(\"az devops invoke --area git --resource threads --route-parameters 'project=P' --http-method POST --in-file t.json\");\n",
		"cat.js":   "const url = process.argv[2];\nexecSync(\"az rest --method post --url \" + url + \" --body @b.json\");\n",
		"fstr.py":  "import os, sys\nurl = sys.argv[1]\nos.system(f\"az rest --method post --url {url} --body @b.json\")\n",
		"multi.js": "spawnSync(\n  \"az\",\n  [\n    \"repos\",\n    \"pr\",\n    \"create\",\n    \"--title\",\n    \"x\",\n  ],\n);\n",
		"tri.py":   "import subprocess\nsubprocess.run(\"\"\"az devops invoke \\\n  --area git --resource threads \\\n  --http-method POST --in-file body.json\"\"\", shell=True)\n",
		"abs.js":   "execSync(\"/opt/homebrew/bin/az repos pr create --title x\");\n",
	})
	for cmd, want := range map[string]string{
		`source env.sh && bash sub/post.sh`:                                                       "(az repos pr create",
		`eval "$(true)"; bash sub/post.sh`:                                                        "(az repos pr create",
		`trap "echo hi" EXIT; bash sub/post.sh`:                                                   "(az repos pr create",
		`cd ` + dir + ` && cat > x.sh <<'EOF'` + "\nbash " + dir + "/sub/post.sh\nEOF\nbash x.sh": "(az repos pr create",
		`S=pos.js; node "$S"`:                                                                     "(az repos pr create",
		`S=pos.py; python3 "$S"`:                                                                  "(az repos pr create",
		`node --env-file .env pos.js`:                                                             "(az repos pr create",
		`node -r ./env.js pos.js`:                                                                 "(az repos pr create",
		`node rest.js`:                                                                            "(az rest",
		`node inv.js`:                                                                             "(az devops invoke",
		`node cat.js https://dev.azure.com/o/_apis/x`:                                             "(az rest",
		`python3 fstr.py https://dev.azure.com/o/_apis/x`:                                         "(az rest",
		`node multi.js`:                                                                           "(az repos pr create",
		`python3 tri.py`:                                                                          "(az devops invoke POST --in-file --resource threads",
		`node abs.js`:                                                                             "(az repos pr create",
		`node -e "require('child_process').execSync('az repos pr create --title x')"`:                                       "(az repos pr create",
		`python3 -c 'import subprocess; subprocess.run(["az","repos","pr","create"])'`:                                      "(az repos pr create",
		"python3 - <<'EOF'\nimport subprocess\nsubprocess.run([\"az\",\"repos\",\"pr\",\"create\",\"--title\",\"x\"])\nEOF": "(az repos pr create",
	} {
		if why := check(bash(cmd, dir)); !strings.HasPrefix(why, reason) || !strings.Contains(why, want) {
			t.Errorf("%q: want %q in %q", cmd, want, why)
		}
	}
	// reads still pass
	for _, cmd := range []string{
		`source env.sh && bash sub/read.sh`,
		`az repos pr show --id 1 -o none && git -c core.pager=cat log -1`,
		`node -e "console.log(1)"`,
		"python3 - <<'EOF'\nprint(1)\nEOF",
	} {
		if why := check(bash(cmd, dir)); why != "" {
			t.Errorf("%q asked: %s", cmd, why)
		}
	}
	// past its time the guard asks instead of letting Claude Code's hook limit pass the call
	deadline = time.Now().Add(-time.Second)
	defer func() { deadline = time.Time{} }()
	if why := check(bash(`bash sub/read.sh`, dir)); !strings.Contains(why, "ran out of time") {
		t.Errorf("late: %q", why)
	}
}

// Round 3 of the script-reading review: a read piped into code on stdin crashed the guard; scripts that pass their arguments to curl
// or to inline code, a command named by $(...), and a script after a relative cd passed; a read-only script run by
// its path with an argument asked.
func TestScriptReadingReviewRound3(t *testing.T) {
	dir := scriptDir(t, map[string]string{
		"cu.sh":       "#!/bin/bash\ncurl -s \"$@\"\n",
		"py.sh":       "#!/bin/bash\npython3 -c \"$1\"\n",
		"ex2.sh":      "#!/bin/bash\nbin=$(cat cmd.txt)\n\"$bin\" repos pr create --title x\n",
		"sub/post.sh": "#!/bin/bash\naz repos pr create --title x\n",
		"rd.sh":       "#!/bin/bash\naz repos pr show --id \"$1\"\n",
		"art/run.sh":  "echo hi\n",
		"sub/rd.sh":   "#!/bin/bash\naz repos pr show --id \"$1\"\n",
		"tag.sh":      "#!/bin/bash\n# writes a format-patch for https://dev.azure.com/o/p/_git/r; does not push\npython3 - \"$1\" <<'PY'\nimport sys\nprint(sys.argv[1])\nPY\n",
		"echo.sh":     "nvm_echo() {\n  command printf %s\\\\n \"$*\" 2>/dev/null\n}\n",
		"trap.sh":     "#!/bin/bash\nD=$(dirname \"$0\")\ntrap 'rm -f /tmp/x; echo done' EXIT\nbash \"$D/sub/post.sh\"\n",
		"run.sh":      "#!/bin/bash\n\"$@\"\n",
		"shim.sh":     "#!/bin/sh\nlink=$1\ntarget=$(command -p readlink \"$link\")\nd=$(command dirname \"$target\")\n",
		"shim2.sh":    "#!/bin/sh\nb=$1\nif c=$(command -p cygpath -w \"$b\" 2>/dev/null); then echo \"$c\"; fi\nnohup sleep \"$b\"\n",
		"shim3.sh":    "#!/bin/sh\nrest=$1\n\"$rest\" x\n",
		"sub/x.sh":    "#!/bin/bash\n# writes x.sh\nn=x\nprintf '%s\\n' 'az repos pr create --title t' > \"$1/$n.sh\"\n",
		"sub/cpx.sh":  "#!/bin/bash\nn=cpx\ncp p.txt \"$1/$n.sh\"\n",
		"cpx.sh":      "#!/bin/bash\necho hi\n",
		"sub/p.txt":   "az repos pr create --title t\n",
		"c.txt":       "az repos pr create --title t\n",
		"vc.ts":       "const t = require('child_process').spawnSync('az', ['repos', 'pr', 'show', '--id', '1']).stdout;\nconsole.log(String(t));\n",
		"pp.sh":       "#!/bin/bash\nB=$(dirname \"$0\")\nbun \"$B/vc.ts\" --pr \"$1\" || exit 6\nb=$(az pipelines runs show --id 1 -o tsv | tr '\\n' ' ')\nprintf '%s\\n' \"$(IFS=,; echo \"$b\")\" >> \"$B/log.md\"\n",
		"pp3.sh":      "#!/bin/bash\nno() { echo \"refused $1\"; exit 2; }\nB=$(dirname \"$0\")\nbun \"$B/vc.ts\" || no 1\nb=$(az pipelines runs show --id 1 -o tsv | tr '\\n' ' ') || no 2\n",
		"pp4.sh":      "#!/bin/bash\nr() { bun \"$(dirname \"$0\")/vc.ts\"; }\nIFS=,\nr\n",
		"pp6.sh":      "#!/bin/bash\nbun \"$(dirname \"$0\")/vc.ts\"\nfor i in 1 2; do s=$(az repos pr show --id 1 | tr '\\n' ' '); done\n",
		"pp2.sh":      "#!/bin/bash\nB=$(dirname \"$0\")\nfor i in 1 2; do bun \"$B/vc.ts\"; IFS=,; done\n",
		"sub/self.sh": "#!/bin/bash\n# self.sh <arg>: prints it\necho \"$1\"\n",
		"gen.sh":      "#!/bin/bash\ncat > out.sh <<'EOF'\naz repos pr create --title x\nEOF\n",
		"cuu.sh":      "#!/bin/bash\ncurl -s --url \"$@\"\n",
		"cuh.sh":      "#!/bin/bash\ncurl -s -H \"$@\" https://dev.azure.com/o/_apis/wit/workItems/1/comments\n",
	})
	for cmd, want := range map[string]string{
		`bash cu.sh -X POST -d '{"text":"hi"}' https://dev.azure.com/o/_apis/wit/workItems/1/comments`: "curl",
		`bash py.sh 'import os; os.system("az repos pr create --title x")'`:                            "inline code",
		`echo az > cmd.txt; bash ex2.sh`:                                                               "a command name",
		`cd sub && bash post.sh`:                                                                       "(az repos pr create",
		`cd sub && ./post.sh`:                                                                          "(az repos pr create",
		`az pipelines runs artifact download --run-id 1 --artifact-name a --path art; bash art/run.sh`: "may change before it runs",
		`az pipelines runs artifact download --run-id 1 --artifact-name a --path=art; bash art/run.sh`: "may change before it runs",
		`bash cuu.sh https://dev.azure.com/o/_apis/wit/workItems/1/comments -X POST -d hi`:             "curl",
		`bash cuh.sh 'A: b' -X POST -d hi`:                                                             "curl",
		`TOK=$(az account get-access-token -o tsv); git -c credential.helper="$TOK" fetch`:             "a git call",
		`TOK=$(az account get-access-token -o tsv); git -c "core.sshCommand=$TOK" fetch`:               "a git call",
		`echo x | tee sub/rd.sh; bash sub/rd.sh 1`:                                                     "may change before it runs",
		`bash trap.sh`: "(az repos pr create",
		`bash run.sh az repos pr create --title x`:                       "a command name",
		`A=$(which az); bash run.sh "$A" repos pr create`:                "a command name",
		`A=$(which az); bash run.sh "$A" rest --method patch --url "$U"`: "a command name",
		`X=az; command -p "$X" repos pr create --title x`:                "(az repos pr create",
		`echo az > x; command -p "$(cat x)" repos pr create --title x`:   "a command name",
		`bash gen.sh && bash out.sh`:                                     "another script the command runs may make",
		// an IFS set later in a loop reaches the script's next run
		`bash pp2.sh; echo "az login"`: "may change before it runs",
		// a function that runs the script after a later setting
		`bash pp4.sh; echo "az login"`: "may change before it runs",
		// review r4 (Opus): a script by the same name in another folder is another script (madeBy keys on the place)
		`cd sub && ./x.sh .. && bash ../x.sh`: "another script the command runs may make",

		// review r4: az named only by which or command -v, the call in text the guard cannot read
		`which az >/dev/null && eval "$(cat c.txt)"`:             "eval",
		`command -v az >/dev/null || exit 1; C=$(cat c.txt); $C`: "a command name",
	} {
		if why := check(bash(cmd, dir)); !strings.HasPrefix(why, reason) || !strings.Contains(why, want) {
			t.Errorf("%q: want %q in %q", cmd, want, why)
		}
	}
	for _, cmd := range []string{
		`az repos pr show --id 1 -o json | python3 -`,
		`az repos pr list | node`,
		`python3 -c 'import os; os.system("az repos pr list | python3")'`,
		`./rd.sh 7774`,
		`./rd.sh -o json`,
		`for p in 1 2; do ./rd.sh $p; done`,
		`az repos pr show --id 1 -p Proj -o tsv; bash rd.sh 1`,
		// today's replay: tee into the folder above writes one file there, not the script (bash kq.sh … | tee ../x.json)
		`cd sub && bash rd.sh 1 | tee ../out.json`,
		`cd sub; bash rd.sh 1 | tee ../out.json`,
		`sed -i '' 's/a/b/' notes.txt; bash sub/rd.sh 1`,
		// today's replay: a script that names Azure DevOps and the word patch, and feeds python code that only prints
		`bash tag.sh x`,
		// today's replay: a folder option with a computed value runs no program
		`T=$(az account get-access-token -o tsv); NS=/tmp/ns; git --work-tree=$NS --git-dir="$NS/.git" status`,
		// today's replay: words after the command a wrapper runs are that command's (nvm.sh: command printf %s "$*")
		`. ./echo.sh; az repos pr show --id 1`,
		`f() { GREP_OPTIONS='' command grep "$@"; }; f x y; az repos pr show --id 1`,
		`command printf '%s' "$X"; timeout 5 echo "$Y"; az repos pr show --id 1`,
		// today's replay: az as a folder or language code (az/en/ru, /az/language) names no az CLI
		`bash run.sh ls; echo "toolbar az/en/ru; /az/language"`,
		// today's replay: after a cd that may fail, the script's own text (its usage line names it) is no other script
		// that may make it in the start folder (kq.sh)
		`cd sub; ./self.sh x; echo "az login"`,
		// today's replay: a word before the one cd is in the start folder (review-pass.sh $R/x; cd $S; pr-text-check.sh)
		`./art/run.sh art/a; cd art; bash ../sub/rd.sh 1`,
		// live 21:56: pass-post.sh sets b=$(az … | tr …) and IFS in a $(…) after bun verdict-check.ts ran: neither reaches it
		`bash pp.sh 7770`,
		// live 21:56: merge.sh has no() { echo; exit; }, a function that runs nothing, then b=$(az … | tr …) after its runs
		`bash pp3.sh 7798`,
		// live 21:56: merge.sh's wait loop after its runs (s=$(az repos pr show … | tr …)) runs no program
		`bash pp6.sh`,
		// today's replay: command -p readlink "$link" in node_modules/.bin shims reads a link; it runs nothing
		`bash shim.sh x; echo "az login"`,
		// today's replay: command -p cygpath -w "$basedir" (line 52 of the shims): its words are cygpath's, not a command
		`bash shim2.sh x; echo "az login"`,
		// today's replay: 'az' as a quoted language code with no az command group in the command
		`bash run.sh ls; echo "loadMessages('az'); copy={'az': 1}"`,
		// today's replay: rest as a shell variable (rest=$caller_path in the .bin shims) is no az rest call
		`bash shim3.sh ls; echo "loadMessages('az')"`,
		// today's replay: 'az' as a language code, logged, is no az run
		`for l in az en ru; do node -e "const m=require('./m/$l.json'); console.log('$l', JSON.stringify(m.a))"; done`,
		// today's replay: a token header in quotes, and an empty credential helper, run no program
		`TOK=$(az account get-access-token -o tsv); git -c "http.extraheader=AUTHORIZATION: bearer $TOK" fetch -q origin`,
		`TOK=$(az account get-access-token -o tsv); git -c credential.helper= -c http.extraheader="A: b $TOK" ls-remote origin`,
	} {
		if why := check(bash(cmd, dir)); why != "" {
			t.Errorf("%q asked: %s", cmd, why)
		}
	}
	// today's replay: a program named by a path from a variable ($D/../../frontend-bin/review-pass.sh) is a file run,
	// never cd or eval, so the words after it keep their folder
	cmd := "D=" + dir + "/art; $D/run.sh x\ncd /tmp && cp a b && " + dir + "/sub/rd.sh 1"
	if why := check(bash(cmd, dir)); why != "" {
		t.Errorf("%q asked: %s", cmd, why)
	}
	// review r4 (Opus): after a cd that may fail, ./cpx.sh may be sub/cpx.sh, a different program that writes cpx.sh
	cmd = "cd sub; ./cpx.sh ..; bash " + dir + "/cpx.sh; az repos pr list -o table"
	if why := check(bash(cmd, dir)); !strings.Contains(why, "may change before it runs") {
		t.Errorf("%q: want a may-change ask, got %q", cmd, why)
	}
}

// Review r5 (Opus, 11-05): az run with its words from a file passed (the az gate needed a command group in the text);
// a sourced helper library placed by HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd) passed; upper-case AZ runs az on
// the Mac's case-blind disk; an alias body was never read; a long eval chain held the guard past the hook's limit.
func TestScriptReadingReviewRound5(t *testing.T) {
	dir := scriptDir(t, map[string]string{
		"lib/ado.sh":  "post_comment() { az repos pr update --id \"$1\" --description \"$2\"; }\n",
		"notes2.sh":   "HERE=\"$(cd \"$(dirname \"${BASH_SOURCE[0]}\")\" && pwd)\"\nsource \"$HERE/lib/ado.sh\"\npost_comment 12 \"QA notes\"\n",
		"notes3.sh":   "source \"$(dirname \"$(realpath \"$0\")\")/lib/ado.sh\"\npost_comment 12 \"QA notes\"\n",
		"a":           "repos pr create --title x\n",
		"list.sh":     "#!/bin/bash\necho hi\n",
		"safe/lib.sh": "echo hi\n",
		"evil/lib.sh": "az repos pr create --title x\n",
		"mrg.sh":      "cd \"$1\" && ./node_modules/.bin/tsc -b\naz repos pr update --id 1 --status completed\n",
		"lib/c.sh":    "X=1\n",
		"c1.sh":       "DIR=$(dirname \"$0\")\nsource \"$DIR/lib/c.sh\"\naz repos pr list -o table\n",
	})
	for cmd, want := range map[string]string{
		`x=(az); "${x[@]}" $(cat a)`:                     "a command name",
		`set -- az $(cat a); "$@"`:                       "a command name",
		`for c in az; do $c $(cat a); done`:              "az",
		`p=$(brew --prefix)/bin/az; "$p" $(< a)`:         "a command name",
		`bash notes2.sh`:                                 "lib/ado.sh",
		`bash notes3.sh`:                                 "lib/ado.sh",
		`AZ repos pr create --title x`:                   "(az repos pr create",
		`/opt/homebrew/bin/AZ repos pr create --title x`: "(az repos pr create",
		"shopt -s expand_aliases\nalias x=\"az repos pr create --title y\"\nx":   "(az repos pr create",
		"alias x='echo hi; az boards work-item update --id 1 --discussion y'\nx": "(az boards work-item update",
		// 11-05 replay: merge.sh asked on tsc after a cd (merge-ok.sh line 75); the ask names the line that posts
		`bash mrg.sh web`: "(az repos pr update; ",
		// a sed program that writes (w) still changes the script
		"sed -n -e 'w review-check.sh' notes.txt; bash review-check.sh 1; az repos pr list": "before it runs",
		"sed -n 'w review-check.sh' notes.txt; bash review-check.sh 1; az repos pr list":    "before it runs",
		// review r5b: a part of an az command in an alias body (bash expands x repos pr create)
		"shopt -s expand_aliases\nalias x=az\nx repos pr create --title t": "alias",
		"alias x='az repos'\nx pr create --title t":                        "alias",
	} {
		if why := check(bash(cmd, dir)); !strings.HasPrefix(why, reason) || !strings.Contains(why, want) {
			t.Errorf("%q: want %q in %q", cmd, want, why)
		}
	}
	execute := "mcp__plugin_context-mode_context-mode__ctx_execute"
	for lang, codes := range map[string][]string{
		"javascript": {
			"const {spawnSync} = require('child_process'); spawnSync('az', process.argv.slice(2))",
			"require('child_process').execFileSync('az', JSON.parse(require('fs').readFileSync('plan.json')))",
			"require('child_process').execSync(`az ${require('fs').readFileSync('cmd.txt')}`)",
		},
		"python": {
			"import subprocess, json; subprocess.run(['az', *json.load(open('plan.json'))])",
			"import os; os.system('az ' + open('cmd.txt').read())",
			"import subprocess; subprocess.run(['az'] + open('a').read().split())",
		},
	} {
		for _, code := range codes {
			in := hookInput(t, execute, map[string]any{"language": lang, "code": code})
			in.Cwd = dir
			if why := check(in); !strings.HasPrefix(why, reason) {
				t.Errorf("ctx_execute %s %q: not asked: %q", lang, code, why)
			}
		}
	}
	for _, cmd := range []string{
		// a language code next to a command name from a variable is no az word of the shell's own
		`bash -c '"$@"' x ls; echo "loadMessages('az')"`,
		`alias ll='ls -l'; ll`,
		// review r5 (P3): command -v only prints where a program is; it runs nothing
		`command -v ./list.sh; az repos pr list -o table`,
		// review r5 (P2): a library placed by a variable set before the source, then an az read
		`bash c1.sh`,
	} {
		if why := check(bash(cmd, dir)); why != "" {
			t.Errorf("%q asked: %s", cmd, why)
		}
	}
	// review r5b (P1): a function body runs after the source or eval that comes later in the text, so a value set before
	// them does not reach it
	cfg := filepath.Join(dir, "cfg")
	if err := os.WriteFile(cfg, []byte("HERE="+dir+"/evil\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{
		"HERE=" + dir + "/safe; load() { bash \"$HERE/lib.sh\"; }; source " + cfg + "; load; echo az version",
		"HERE=" + dir + "/safe; load() { bash \"$HERE/lib.sh\"; }; eval \"HERE=" + dir + "/evil\"; load; echo az version",
		"HERE=" + dir + "/safe; load() { source \"$HERE/lib.sh\"; }; source " + cfg + "; load; echo az version",
	} {
		if why := check(bash(cmd, dir)); !strings.HasPrefix(why, reason) {
			t.Errorf("%q: not asked: %q", cmd, why)
		}
	}
	// 11-05 23:02, ten Quotes tabs: review-check.sh reads one PR and greps local notes with find -exec /usr/bin/grep
	// "…${head:0:7}" {} +; a read command by its system path takes the words after it as its own
	b, err := os.ReadFile(filepath.Join("testdata", "review-check.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "review-check.sh"), b, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{`bash review-check.sh 7815`, `./review-check.sh 7815; echo rc=$?`,
		// 11-05 23:0x: printing the script first changes nothing; the words after sed's program are files it reads
		`sed -n 1,40p review-check.sh; bash review-check.sh 7809; echo rc=$?`} {
		if why := check(bash(cmd, dir)); why != "" {
			t.Errorf("%q asked: %s", cmd, why)
		}
	}
	// the time limit holds inside one long command, not only between the scripts it reads
	deadline = time.Now().Add(-time.Second)
	defer func() { deadline = time.Time{} }()
	if why := check(bash(`eval "echo $i"; echo done`, dir)); !strings.Contains(why, "ran out of time") {
		t.Errorf("late in one command: %q", why)
	}
}
