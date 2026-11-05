package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 11-05: 113 of 148 asks in a day were local scripts that only read, test, print or post to the bus
// (~/.crius/state/crius/post-guard-ask-causes-1105.md). The command shapes below are the logged ones, with the scratchpad
// folders replaced by a test folder.

func scriptDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, text := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// buildwait.sh, kq.sh, review-gate.sh and blog.sh as the quotes and AXI leads wrote them, cut to their shape.
var readScripts = map[string]string{
	"buildwait.sh": `#!/bin/bash
E=$(dirname "$0")
for i in $(seq 1 60); do
  r=$(az pipelines runs show --org https://dev.azure.com/Appxite --project "AppXite Platform" --id 125805 --query "[status,result]" -o tsv)
  case "$r" in *completed*) echo "$r" > "$E/build.txt"; exit 0;; esac
  sleep 60
done
echo LOUD timeout
`,
	"watch/kq.sh": `#!/bin/bash
W=$(az monitor log-analytics workspace show -g rg -n law --query customerId -o tsv)
az monitor log-analytics query -w "$W" --analytics-query "$1" -o json
`,
	"lead/review-gate.sh": `#!/bin/bash
ORG=https://dev.azure.com/Appxite
pr=$1
while IFS= read -r line; do echo "$line"; done < <(az repos pr show --id "$pr" --org $ORG --query "reviewers[].vote" -o tsv)
set -- $(az pipelines runs list --org $ORG --top 1 --query "[0].[result,sourceBranch,sourceVersion]" -o tsv)
p2=$(az devops invoke --org https://dev.azure.com/Appxite --area git --resource commits --route-parameters project="AppXite Platform" repositoryId=d88331ee commitId="$3" --api-version 7.1 --query "parents[1]" -o tsv)
az repos pr list --org $ORG --status active --query "[].pullRequestId" -o tsv
find . -name '*.md' -newer x | sed -n 1p
`,
	"lead/blog.sh": `#!/bin/bash
echo "$(date +%H:%M) $1 $2 -> $3: $4" >> ~/.crius/state/quotes/build/BUILD-LOG.md
printf '%s\n' "$4" >> ~/.crius/state/quotes/lead-state.md
`,
	"proof.sh": `#!/bin/bash
check() { /usr/bin/grep -q -- "$2" "$1" && echo "ok $2" || echo "FAIL $2"; }
check out.txt '> az: SIL is the word'
`,
	"watch/watch-0152.sh": `#!/bin/bash
W=WATCHDIR
OUT=OUTFILE
: > $OUT
Q="KubePodInventory | where Namespace=='env004' | project Name, PodStatus"
for i in $(seq 1 1); do
  bash $W/kq.sh "$Q" | python3 -c '
import json,sys
rows=json.load(sys.stdin)
print("SUMMARY", len(rows))' >> $OUT
  sleep 60
done
`,
}

func TestScriptsThatOnlyReadPass(t *testing.T) {
	dir := scriptDir(t, readScripts)
	w := filepath.Join(dir, "watch/watch-0152.sh")
	body, _ := os.ReadFile(w)
	text := strings.ReplaceAll(strings.ReplaceAll(string(body), "WATCHDIR", dir+"/watch"), "OUTFILE", dir+"/watch-0152.out")
	// the logged watch less its python, which the guard cannot read (TestScriptsTheGuardCannotReadAsk)
	py := "python3 -c '\nimport json,sys\nrows=json.load(sys.stdin)\nprint(\"SUMMARY\", len(rows))'"
	if !strings.Contains(text, py) {
		t.Fatal("watch-0152.sh has no python3 -c")
	}
	if err := os.WriteFile(w, []byte(strings.Replace(text, py, "tail -1", 1)), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{
		// A: the script reads Azure DevOps through az; the command around reads too
		`bash ` + dir + `/watch/kq.sh "KubePodInventory | take 5"`,
		`L=` + dir + `/lead; bash $L/review-gate.sh 7724; echo control_rc=$?`,
		`az pipelines runs list --org https://dev.azure.com/Appxite --project "AppXite Platform" --branch refs/pull/7711/merge --top 2 --query "[].[id,status,result]" -o tsv; L=` + dir + `/lead; bash $L/blog.sh build NEED "backend-core" "No line since 17:19. STATUS please: b1 fixes and when b1 goes to QA."; echo rc=$?`,
		// H: a script that runs one of those through a variable it sets itself, and writes a file it names
		`bash ` + w + `; echo rc=$?; tail -20 ` + dir + `/watch-0152.out | cut -c1-200`,
		// C: bash -c and eval text that holds a loop variable over literal words
		`for i in $(seq 1 90); do out=""; for n in 7730 7736; do r=$(bash -c "az pipelines build list --org https://dev.azure.com/Appxite --project 'AppXite Platform' --branch refs/pull/$n/merge --top 1 --query '[0].[id,status,result,queueTime]' -o tsv" 2>&1); case "$r" in *completed*) out="$out $n:$r";; esac; done; sleep 60; done`,
		`O=https://dev.azure.com/Appxite; for i in $(seq 1 60); do for n in 7740 7741; do r=$(az pipelines build list --org $O --project 'AppXite Platform' --branch refs/pull/$n/merge --top 1 -o tsv | tr '\t' ' '); eval "s$n=\"\$r\""; done; case "$s7740 $s7741" in *completed*completed*) exit 0;; esac; sleep 60; done`,
		// bash -n reads a script's syntax and runs nothing
		`bash -n ` + dir + `/missing.sh && echo 'az: syntax ok'`,
		// D: a command name from a variable set to a literal path
		`R=~/Projects/scifi/crius/bin/crius-rec` + "\n" + `$R record bus --project cortex --role lead --type DONE --text "az login works"`,
		// az reads in bash -c text beside a script (quotes lead 03:27)
		`L=` + dir + `/lead; n=7742; b=$(bash -c "az pipelines build list --org https://dev.azure.com/Appxite --project 'AppXite Platform' --branch refs/pull/$n/merge --top 1 --query '[0].id' -o tsv"); bash $L/review-gate.sh $n | tail -1`,
		// a script run inside bash -c text (quotes lead 23:56)
		"bash -c '\nL=" + dir + "/lead\nbash $L/review-gate.sh 7724; echo control_rc=$?\nO=\"--org https://dev.azure.com/Appxite\"\naz repos pr policy list --id 7732 $O --query \"[].status\" -o tsv\n'",
		// B: a relative script after one cd to a folder that is there (AXI lead 07:27, 18:11)
		`cd ` + dir + `/lead; tail -1 x.txt; bash review-gate.sh 7724 2>&1 | tail -4`,
		`cd ` + dir + ` && B=$(az repos ref list --repository gitops --org https://dev.azure.com/appxite --query '[0].objectId' -o tsv); echo base=$B; bash lead/review-gate.sh 7724`,
		`cd ` + dir + `/lead || exit 1; bash review-gate.sh 7724`,
		// a cd later in an && chain, and one in ( … ) (staffy 06:42, AXI 20:47)
		`az repos pr show --id 7 -o json && cd ` + dir + `/lead && bash review-gate.sh 7724`,
		`az repos pr show --id 7 -o json; bash -n ` + dir + `/lead/review-gate.sh && (cd ` + dir + `/lead && bash review-gate.sh 7724 > out 2>&1; echo rc=$?)`,
		// the Azerbaijani locale az in text a script gets or holds: az/en/ru, "az:" and "az," are no az command
		`bash ` + dir + `/lead/blog.sh payments-dev "mask e-mails in CV free text, red first on az/en/ru samples" 2>&1 | tail -2`,
		`bash ` + dir + `/lead/blog.sh LEADER CLEAR "Copy in az, en and ru matches"`,
		`bash ` + dir + `/proof.sh > ` + dir + `/proof.log 2>&1`,
	} {
		if why := check(bash(cmd, dir)); why != "" {
			t.Errorf("%q asked: %s", cmd, why)
		}
	}
}

// Review r2 asked for these (buildwait.sh writes "$E/build.txt" beside itself, from $0; watch-0152.sh runs python3 -c);
// owner 11-05: they only read Azure DevOps, so they pass. $(dirname "$0") is the script's folder, and a redirect writes
// the one file it names.
func TestScriptsThatWriteBesideThemselvesOrRunPythonPass(t *testing.T) {
	dir := scriptDir(t, readScripts)
	w := filepath.Join(dir, "watch/watch-0152.sh")
	body, _ := os.ReadFile(w)
	text := strings.ReplaceAll(strings.ReplaceAll(string(body), "WATCHDIR", dir+"/watch"), "OUTFILE", dir+"/watch-0152.out")
	if err := os.WriteFile(w, []byte(text), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{
		`E=` + dir + `; bash $E/buildwait.sh; cat $E/build.txt`,
		`bash ` + w + `; echo rc=$?; tail -20 ` + dir + `/watch-0152.out | cut -c1-200`,
	} {
		if why := check(bash(cmd, dir)); why != "" {
			t.Errorf("%q asked: %s", cmd, why)
		}
	}
}

// The logged pr-text-check command asks for its PR description, not because $B is a command name it cannot read.
func TestACommandNameFromALiteralVariableIsRead(t *testing.T) {
	cmd := `S=/tmp/s; B=~/.crius/state/quotes/build/bin/pr-text-check.sh; A=(--org https://dev.azure.com/Appxite --project "AppXite Platform" --repository svc_quote -o tsv); $B $S/lc/PR-linux.md && az repos pr create "${A[@]}" --source-branch feature/96365-linux-tests --title "build(tests): run the suite on Linux" --description "$(cat $S/lc/PR-linux.md)"; echo rc=$?`
	if why := check(bash(cmd, "/tmp")); !strings.Contains(why, "(az repos pr create --description)") {
		t.Errorf("%q: %q", cmd, why)
	}
}

// The hard line (owner 11-05): an az or Azure DevOps REST write with text still asks when a script, a variable, a loop
// or a function hides it.
func TestTextWritesHiddenInScriptsVariablesLoopsAndFunctionsAsk(t *testing.T) {
	dir := scriptDir(t, map[string]string{
		"pr.sh":     "#!/bin/bash\naz repos pr create --org https://dev.azure.com/Appxite \"$@\"\n",
		"anyaz.sh":  "#!/bin/bash\naz \"$@\"\n",
		"verb.sh":   "#!/bin/bash\naz repos pr \"$1\" --id 7 --description \"$(cat d.md)\"\n",
		"fn.sh":     "#!/bin/bash\npost() { az repos pr update --id \"$1\" --description \"$2\"; }\npost 7 \"$(cat d.md)\"\n",
		"inv.sh":    "#!/bin/bash\naz devops invoke --area wit --resource comments $ARGS\n",
		"rest.sh":   "#!/bin/bash\ncurl -sS -X POST -H 'Content-Type: application/json' -d @c.json \"$1\"\n",
		"wi.sh":     "#!/bin/bash\naz boards work-item update --id 94205 $EXTRA\n",
		"read.sh":   "#!/bin/bash\naz repos pr show --id 7 -o json\n",
		"trap.sh":   "#!/bin/bash\ntrap 'az boards work-item update --id 1 --discussion done' EXIT\necho hi\n",
		"loop.sh":   "#!/bin/bash\nfor v in create update; do az repos pr $v --id 7 -d \"x\"; done\n",
		"thr.sh":    "#!/bin/bash\npr=$1\naz devops invoke --area git --resource pullRequestThreads --route-parameters project=P pullRequestId=$pr -o json\n",
		"rd.sh":     "#!/bin/bash\naz repos pr update --id 7 --description x\n",
		"w/rd.sh":   "#!/bin/bash\naz repos pr show --id 7 -o json\n",
		"w/read.sh": "#!/bin/bash\naz repos pr show --id 7 -o json\n",
		"w/pr.sh":   "#!/bin/bash\naz repos pr create --org https://dev.azure.com/Appxite \"$@\"\n",
		"echoaz.sh": "#!/bin/bash\nwhile read -r l; do $l; done <<'EOF'\naz boards work-item update --id 1 --discussion x\nEOF\n",
	})
	s := `S=` + dir + `; `
	for _, cmd := range []string{
		s + `bash $S/pr.sh --title T --description "$(cat d.md)"`,
		s + `bash $S/pr.sh --title T`, // what "$@" holds is not read
		s + `bash $S/anyaz.sh repos pr update --id 7 -d x`,
		s + `bash $S/anyaz.sh`,
		s + `bash $S/verb.sh update`,
		s + `bash $S/fn.sh`,
		s + `bash $S/inv.sh`,
		s + `bash $S/rest.sh "https://dev.azure.com/appxite/_apis/wit/workItems/1/comments"`,
		s + `bash $S/wi.sh`,
		s + `bash $S/trap.sh`,
		s + `bash $S/loop.sh`,
		s + `bash $S/echoaz.sh`,
		s + `bash $S/thr.sh 7724`, // $pr unquoted may split into --http-method POST
		// the command rewrites a script that only reads, then runs it
		s + `sed -i '' 's/show --id 7 -o json/update --id 7 --description x/' $S/read.sh; bash $S/read.sh`,
		s + `cat > $S/read.sh <<'EOF'` + "\naz repos pr update --id 7 --description \"$(cat d.md)\"\nEOF\nbash $S/read.sh",
		s + `cat > $S/new.sh <<EOF` + "\naz repos pr update --id 7 --description \"\\$(cat $S/d.md)\"\nEOF\nbash -n $S/new.sh && bash $S/new.sh",
		s + `cat >> $S/read.sh <<'EOF'` + "\naz repos pr update --id 7 -d x\nEOF\nbash $S/read.sh",
		s + `python3 - <<'EOF'` + "\nopen('read.sh','w').write('az repos pr update --id 7 -d x')\nEOF\nbash $S/read.sh",
		s + `PATH=/tmp/fake:$PATH bash $S/read.sh`, // a fake az first on PATH
		// after a cd the relative name is the cd's folder's file, and the start folder's too when the cd may fail; a cd the
		// guard does not follow leaves the name unknown, which asks when the command names az ("az x")
		`cd ` + dir + `/w; bash pr.sh --title T`,
		`cd ` + dir + `/w; bash rd.sh`,
		`cd ` + dir + `/nothere; bash pr.sh --title "az x"`,
		`cd ` + dir + `/w; cd ..; bash read.sh --title "az x"`,
		`(cd ` + dir + `/w); bash pr.sh --title "az x"`,
		`f() { bash pr.sh --title "az x"; }; cd ` + dir + `/w && f`,
		`cd $D; bash pr.sh --title "az x"`,
		`eval "cd /tmp"; cd ` + dir + `/w; bash read.sh --title "az x"`,
		`cd ` + dir + `/w & bash read.sh --title "az x"`,
		// a function named cd goes where it likes: here nowhere, so rd.sh is the start folder's, which writes
		`az repos pr show --id 7 -o json; cd() { :; }; cd ` + dir + `/w && bash rd.sh`,
		// after a cd later in a chain, a step after the chain may run in the start folder; after ( cd … ) it does
		`az repos pr show --id 7 -o json && cd ` + dir + `/w; bash rd.sh`,
		`az repos pr show --id 7 -o json; (cd ` + dir + `/w && bash rd.sh); bash rd.sh`,
		`az repos pr show --id 7 -o json; (cd ` + dir + `/w || exit; bash read.sh); bash rd.sh`,
		s + `bash -c "sed -i '' 's/show --id 7 -o json/update --id 7 -d x/' $S/read.sh"; bash $S/read.sh`,
		s + `bash -c "sed -i '' 's/show --id 7 -o json/update --id 7 -d x/' $S/read.sh; bash $S/read.sh"`,
		// variables, arrays, loops and functions in a command
		`D=--description; az repos pr update --id 7 $D "text"`,
		`A=(--description "text"); az repos pr update --id 7 "${A[@]}"`,
		`az repos pr update --id 7 $ARGS`,
		`az $CMD --id 7`,
		`az repos pr "$V" --id 7 -d x`,
		`for v in create update; do az repos pr $v --id 7 -d x; done`,
		`for v in create; do bash -c "az repos pr $v --title t -d x"; done`,
		`for m in POST; do eval "az devops invoke --area wit --resource comments --http-method $m --in-file x.json"; done`,
		`az devops invoke --area wit --resource comments $ARGS`,
		`az boards work-item update --id 1 $EXTRA`,
		`az rest --method post --url "https://dev.azure.com/appxite/_apis/wit/workItems/1/comments" $BODY`,
		`f() { az repos pr update --id 7 "$@"; }; f --description x`,
		`O="--org https://dev.azure.com/Appxite -d text"; az repos pr update --id 7 $O`,
		// bash -c's own arguments are no -c text: the script's path there is not known
		s + `bash -c 'bash $1/read.sh "$2"' _ $S "az repos pr update --id 7 -d x"`,
		// owner 11-05: a change of status is a post
		`O="--org https://dev.azure.com/Appxite"; az repos pr update --id 7 $O --status completed`,
		`az repos pr update --id "$ID" --status completed --org https://dev.azure.com/Appxite`,
	} {
		if why := check(bash(cmd, dir)); !strings.Contains(why, "owner's click") {
			t.Errorf("%q: %q", cmd, why)
		}
	}
	for _, cmd := range []string{
		s + `bash $S/read.sh`,
		`cd ` + dir + `/w && bash read.sh`,
		`cd ` + dir + `/w && bash rd.sh`,
		`cd ` + dir + `/w || exit; bash rd.sh`,
		`az repos pr show --id 7 -o json; (cd ` + dir + `/w || exit; bash rd.sh); bash read.sh`,
		// owner 11-05: az text a script gets that it only prints or reads is no command (read.sh runs none of its words)
		s + `bash $S/read.sh "az repos pr update --id 7 -d x"`,
		s + `bash $S/read.sh "x" '/usr/local/bin/az boards work-item update --id 1 --discussion x'`,
		`O=https://dev.azure.com/Appxite; az repos pr policy list --id 7732 --org $O --query "[].status" -o tsv`,
		`az pipelines runs show --id $RUN $OPTS`,
	} {
		if why := check(bash(cmd, dir)); why != "" {
			t.Errorf("%q asked: %s", cmd, why)
		}
	}
}

// Review r1 of the script pass (11-05): what the guard cannot read stays an ask, and text a command writes into a
// script is never taken as the script's.
func TestScriptPassFailsClosed(t *testing.T) {
	dir := scriptDir(t, map[string]string{
		"s.sh":      "#!/bin/bash\naz account show -o none\n",
		"b/s.sh":    "#!/bin/bash\naz repos pr create --title t -d hi\n",
		"c.sh":      "#!/bin/bash\naz account show -o none\nX=$(printf \"\\141\\172\")\nexec $X repos pr create --title t -d hi\n",
		"c2.sh":     "#!/bin/bash\naz account show -o none\nX=$(printf \"\\141\\172\")\ntimeout 60 $X repos pr create --title t -d hi\n",
		"d.sh":      "#!/bin/bash\naz repos pr update --id 1 --title=$1\n",
		"d2.sh":     "#!/bin/bash\naz repos pr update --id 1 -t$1\n",
		"e.sh":      "#!/bin/bash\naz repos pr update --id 1 --draft \"$1\"\n",
		"f.sh":      "#!/bin/bash\nprintf '[{\"op\":\"add\",\"path\":\"/fields/System.History\",\"value\":\"%s\"}]' \"$1\" > " + "INFILE\"\".json\naz devops invoke --area wit --resource workitems --http-method PATCH --in-file INFILE.json --route-parameters id=1\n",
		"in.json":   "[]\n",
		"t.sh":      "#!/bin/bash\ntrap 'az repos pr create --title t -d hi' EXIT\n",
		"d3/s.sh":   "#!/bin/bash\naz account show -o none\n",
		"d3/x/s.sh": "#!/bin/bash\naz account show -o none\n",
	})
	f := filepath.Join(dir, "f.sh")
	body, _ := os.ReadFile(f)
	if err := os.WriteFile(f, []byte(strings.ReplaceAll(string(body), "INFILE", dir+"/in")), 0o755); err != nil {
		t.Fatal(err)
	}
	s := `S=` + dir + `; `
	enc := `printf '\141\172 repos pr create --title t -d hi\n'`
	for name, cmd := range map[string]string{
		"P1.1 a write to the script through a name the guard cannot read": s + enc + ` > $S/e; T=$S/s.sh; cp $S/e ${T%.x}; bash $S/s.sh`,
		"P1.2a cat's stdin is another redirect, not the heredoc":          `cat > ` + dir + `/n.sh <<'E' <<< "$(` + enc + `)"` + "\naz account show\nE\nbash " + dir + "/n.sh",
		"P1.2b cat is a function":                                         `cat() { ` + enc + `; }; cat > ` + dir + `/n.sh <<'E'` + "\naz account show\nE\nbash " + dir + "/n.sh",
		"P1.2c a heredoc into the script that posts":                      `cat > ` + dir + `/n.sh <<'E'` + "\naz repos pr create --title t -d hi\nE\nbash " + dir + "/n.sh",
		"P1.3a exec runs a command name it cannot read":                   s + `bash $S/c.sh`,
		"P1.3b timeout runs a command name it cannot read":                s + `bash $S/c2.sh`,
		"P1.4a --title=$1 may split":                                      s + `bash $S/d.sh 'x --description hi'`,
		"P1.4b -t$1 may split":                                            s + `bash $S/d2.sh 'x --description hi'`,
		"P1.5 --draft takes no value for sure":                            s + `bash $S/e.sh --description=hi`,
		"P1.6a eval may cd":                                               `az account show -o none; eval "cd ` + dir + `/b"; bash s.sh`,
		"P1.6b a cd through a variable":                                   `az account show -o none; X=cd; $X ` + dir + `/b; bash s.sh`,
		"P1.6c a trap may cd":                                             `az account show -o none; trap "cd ` + dir + `/b" DEBUG; bash s.sh`,
		"P1.6d declare through a variable":                                `S=` + dir + `; X=declare; $X S=` + dir + `/b; az account show -o none; bash $S/s.sh`,
		"P1.7 an --in-file a script writes":                               s + `bash $S/f.sh hi`,
		"P2 cd || return goes on":                                         `az account show -o none; cd ` + dir + `/d3/x || return; bash b/s.sh`,
		"P2 a function named exit":                                        `az account show -o none; exit() { :; }; cd ` + dir + `/d3/x || exit; bash ../b/s.sh`,
		"P2 trap text":                                                    s + `bash $S/t.sh`,
	} {
		if why := check(bash(cmd, dir)); !strings.Contains(why, "owner's click") {
			t.Errorf("%s: %q passed", name, cmd)
		}
	}
}

// Review r2 (11-05): a call counts as a read only when the guard shows it reads, and a script's own writes are held to
// the rules of the commands around it.
func TestScriptPassNeedsAShownRead(t *testing.T) {
	dir := scriptDir(t, map[string]string{
		"tok.sh":  "#!/bin/bash\nT=$(az account get-access-token --query accessToken -o tsv)\ncurl -sS -X POST -H \"Authorization: Bearer $T\" \"$URL\" -d '{\"text\":\"hi\"}'\n",
		"rev.sh":  "#!/bin/bash\naz repos pr update --id 1 --reviewers \"$1\"\n",
		"self.sh": "#!/bin/bash\naz account show -o none\nprintf 'echo x\\n' >> \"$0\"\n",
		"cpy.sh":  "#!/bin/bash\naz account show -o none\ncp \"$1\" \"$0\"\n",
		"inv.sh":  "#!/bin/bash\naz devops invoke --area wit --resource workitems --http-method PATCH --in " + "IN --route-parameters id=1\n",
		"s.sh":    "#!/bin/bash\naz account show -o none\n",
		"spl.sh":  "#!/bin/bash\nX=$(cat args)\naz devops invoke --area git --resource commits $X -o json\n",
		"cat":     "#!/bin/bash\ncp /dev/null \"$2\"\n",
		"in.json": "[]\n",
	})
	inv := filepath.Join(dir, "inv.sh")
	body, _ := os.ReadFile(inv)
	if err := os.WriteFile(inv, []byte(strings.ReplaceAll(string(body), "IN", dir+"/in.json")), 0o755); err != nil {
		t.Fatal(err)
	}
	s := `S=` + dir + `; `
	for name, cmd := range map[string]string{
		"P1 an HTTP write to a URL the guard cannot read": s + `bash $S/tok.sh`,
		"P1 an option that takes many values":             s + `bash $S/rev.sh --description=hi`,
		"P1 a script that writes to itself":               s + `bash $S/self.sh`,
		"P1 a script that copies over itself":             s + `bash $S/cpy.sh x`,
		"P2 an abbreviated --in-file":                     s + `bash $S/inv.sh`,
		"P2 a command that writes files it does not name": `cp -R ` + dir + `/new/. ` + dir + `/; bash ` + dir + `/s.sh`,
		"P2 a word that may split into --out-file":        s + `bash $S/spl.sh`,
		"P3 a read command by a path of its own":          `az account show -o none; ` + dir + `/cat x ${S%.x}; bash ` + dir + `/s.sh`,
	} {
		if why := check(bash(cmd, dir)); !strings.Contains(why, "owner's click") {
			t.Errorf("%s: %q passed", name, cmd)
		}
	}
}
