package main

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"mvdan.cc/sh/v3/syntax"
)

// Two more Quotes calls that asked many times a day (Crius 11-06: 41 pr-build.sh and 82 az repos pr clicks of 272) pass
// as one exact whole command each, with `; echo rc=$?` (or ado_rc, quoted or not) after it allowed:
//
//	bash ~/.crius/state/quotes/build/bin/pr-build.sh <digits>
//	az repos pr set-vote --id <digits> --vote approve --org https://dev.azure.com/Appxite -o none
//
// pr-build.sh only queues a PR merge build and posts no text; it passes while it has a SHA-256 the pin file lists (it refuses
// a PR whose azure-pipelines.yml is not the team's) and the hook's env has none of BASH_ENV, ENV, SHELLOPTS, BASHOPTS,
// PS4 or an exported bash function (BASH_FUNC_*), which bash would run or obey. The approve vote passes only when the PR,
// read at hook time with one `az repos pr show` (a GET, its process group killed after 10 s), is active, targets develop
// or integration/96365-bN and has no auto-complete, and qa/*-<head8>/verdict.md (in the real Quotes build qa/ folder,
// folder and file no symlink) has a first line naming PR <id> and holding "@ <head8> — PASS", PASS then the end, a space
// or "(", where head8 is the first 8 characters of the PR's lastMergeSourceCommit. A PR the guard cannot read asks. PR
// create, PR update (a merge), any other vote, flag order or text keep the click through the usual reading.
// Known gaps, besides those in adoexact.go (zsh snapshot, PATH for bash and az, files changed after the hook):
//   - Anyone who can write the qa/ folder can write a PASS verdict: the guard checks that one exists, not who wrote it.
//   - A push to the PR between the hook's head read and the vote is not seen.
//   - The verdict names the head by its first 8 hex characters: another commit with the same 8 would match.

var (
	// prState reads the PR at hook time (readPR; tests swap it).
	prState = readPR
	// bashStartupVars are the env names bash reads code or options from.
	bashStartupVars = []string{"BASH_ENV", "ENV", "SHELLOPTS", "BASHOPTS", "PS4"}
)

// prInfo is what the vote rule reads of a PR.
type prInfo struct{ Head, Status, Target, Auto, Repo string }

// readPR reads the PR's source head (the commit QA judged), status, target and auto-complete with one GET. az is a bash
// wrapper whose python keeps stdout open, so a timeout kills the whole process group and stops waiting for the pipe.
// prRead keeps each PR read for this hook call: the allow list reads the call a second time, and a hung az must cost its
// 10 s once.
var prRead = map[string]struct {
	p   prInfo
	err error
}{}

func readPR(id string) (prInfo, error) {
	if r, seen := prRead[id]; seen {
		return r.p, r.err
	}
	p, err := readPROnce(id)
	prRead[id] = struct {
		p   prInfo
		err error
	}{p, err}
	return p, err
}

func readPROnce(id string) (prInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "az", "repos", "pr", "show", "--id", id, "--org", adoOrg, "--query",
		"{head: lastMergeSourceCommit.commitId, status: status, target: targetRefName, auto: autoCompleteSetBy.id, repo: repository.name}", "-o", "json")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		return prInfo{}, err
	}
	var v struct {
		Head, Status, Target, Repo string
		Auto                       *string
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return prInfo{}, err
	}
	p := prInfo{Head: v.Head, Status: v.Status, Target: v.Target, Repo: v.Repo}
	if v.Auto != nil {
		p.Auto = *v.Auto
		if p.Auto == "" {
			p.Auto = "(set, no id)"
		}
	}
	return p, nil
}

const adoOrg = "https://dev.azure.com/Appxite"

var (
	quotesTextRe = regexp.MustCompile(`^[A-Za-z0-9._/:=~ ;$?"-]+$`)
	quotesWordRe = regexp.MustCompile(`^[A-Za-z0-9._/:=-]+$`)
	prIDRe       = regexp.MustCompile(`^[0-9]{1,9}$`)
	sha40Re      = regexp.MustCompile(`^[0-9a-f]{40}$`)
	prTargetRe   = regexp.MustCompile(`^refs/heads/(develop|integration/96365-b[0-9]+)$`)
)

// quotesExact is adoExact's twin for pr-build.sh and the approve vote: ok when command is one of the shapes; why is ""
// when it passes, or the ask when a hook-time check fails.
func quotesExact(command string) (ok bool, why string) {
	if !quotesTextRe.MatchString(command) || strings.Count(command, "$") != strings.Count(command, "$?") {
		return false, ""
	}
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil || len(f.Stmts) < 1 || len(f.Stmts) > 2 || len(f.Stmts) == 2 && !echoRc(f.Stmts[1]) {
		return false, ""
	}
	st := f.Stmts[0]
	ce, isCall := st.Cmd.(*syntax.CallExpr)
	if !isCall || !plainStmt(st) || len(st.Redirs) > 0 || len(ce.Assigns) > 0 {
		return false, ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false, ""
	}
	args := make([]string, len(ce.Args))
	for i, w := range ce.Args {
		if len(w.Parts) != 1 {
			return false, ""
		}
		l, isLit := w.Parts[0].(*syntax.Lit)
		if !isLit {
			return false, ""
		}
		args[i] = l.Value
		if strings.HasPrefix(args[i], "~/") {
			args[i] = home + args[i][1:]
		}
		if !quotesWordRe.MatchString(args[i]) {
			return false, ""
		}
	}
	build := filepath.Join(home, ".crius/state/quotes/build")
	script := filepath.Join(build, "bin/pr-build.sh")
	switch {
	case len(args) == 3 && args[0] == "bash" && args[1] == script && prIDRe.MatchString(args[2]):
		if why := pinned(script, "pr-build.sh"); why != "" {
			return true, why
		}
		return true, bashEnv("pr-build.sh")
	case len(args) == 12 && strings.Join(args[:5], " ") == "az repos pr set-vote --id" && prIDRe.MatchString(args[5]) &&
		strings.Join(args[6:], " ") == "--vote approve --org "+adoOrg+" -o none":
		return true, qaPassed(args[5], filepath.Join(build, "qa"))
	}
	return false, ""
}

// bashEnv asks when the hook's env has one of bashStartupVars or an exported bash function (BASH_FUNC_*), which bash
// would run or obey for the script who.
func bashEnv(who string) string {
	for _, n := range bashStartupVars {
		if v := os.Getenv(n); v != "" {
			return asked(who + " with " + n + " set in the session (" + v + "): bash reads code or options from it")
		}
	}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "BASH_FUNC_") {
			name, _, _ := strings.Cut(kv, "=")
			return asked(who + " with an exported bash function in the session (" + name + "): bash runs it in place of a command")
		}
	}
	return ""
}

// qaPassed asks unless the PR is active on develop or integration/96365-bN without auto-complete, and
// qa/*-<head8>/verdict.md's first line names PR <id> and holds "@ <head8> — PASS" for the PR's current head.
func qaPassed(id, qa string) string {
	pi, err := prState(id)
	head := pi.Head
	if err != nil || !sha40Re.MatchString(head) {
		return asked("approving PR " + id + ": the guard cannot read the head (lastMergeSourceCommit) to match a QA verdict")
	}
	switch {
	case pi.Status != "active":
		return asked("approving PR " + id + ": its status " + pi.Status + " is not active")
	case !prTargetRe.MatchString(pi.Target):
		return asked("approving PR " + id + ": it targets " + pi.Target + ", not develop or integration/96365-bN")
	case pi.Auto != "":
		return asked("approving PR " + id + ": auto-complete is set (" + pi.Auto + "), so an approve could complete it")
	}
	if real, err := filepath.EvalSymlinks(qa); err != nil || real != qa {
		return asked("approving PR " + id + ": " + qa + " is not a real folder")
	}
	idRe := regexp.MustCompile(`(^|[^0-9A-Za-z])PR ` + id + `([^0-9]|$)`)
	// PASS is the first word after the dash after the head, then the end, a space or "(" (review 11-06 P2-1).
	passRe := regexp.MustCompile(`@ ` + head[:8] + ` — PASS($|[ (])`)
	found, _ := filepath.Glob(filepath.Join(qa, "*-"+head[:8], "verdict.md"))
	for _, v := range found {
		if d, err := os.Lstat(filepath.Dir(v)); err != nil || !d.IsDir() {
			continue
		}
		if fi, err := os.Lstat(v); err != nil || !fi.Mode().IsRegular() {
			continue
		}
		fh, err := os.Open(v)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(fh)
		first := ""
		if sc.Scan() {
			first = sc.Text()
		}
		fh.Close()
		if idRe.MatchString(first) && passRe.MatchString(first) {
			return ""
		}
	}
	return asked("approving PR " + id + ": no QA verdict at " + qa + "/*-" + head[:8] + "/verdict.md whose first line names PR " + id + " and holds \"@ " + head[:8] + " — PASS\"")
}
