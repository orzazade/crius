package main

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"mvdan.cc/sh/v3/syntax"
)

// Two more Quotes calls that asked many times a day (Crius 11-06: 41 pr-build.sh and 82 az repos pr clicks of 272) pass
// as one exact whole command each, with `; echo rc=$?` (or ado_rc, quoted or not) after it allowed:
//
//	bash ~/.crius/state/quotes/build/bin/pr-build.sh <digits>
//	az repos pr set-vote --id <digits> --vote approve --org https://dev.azure.com/Appxite -o none
//
// pr-build.sh only queues a PR merge build and posts no text; it passes while it has the SHA-256 pinned here and the
// hook's env has no BASH_ENV and no exported bash function (BASH_FUNC_*), which bash would run first. The approve vote
// passes only when qa/*-<head8>/verdict.md (in the real Quotes build qa/ folder, folder and file no symlink) has a first
// line naming PR <id> and PASS and no FAIL or BLOCK, where head8 is the first 8 characters of the PR's current
// lastMergeSourceCommit, read at hook time with `az repos pr show` (a GET); a head the guard cannot read asks. PR create,
// PR update (a merge), any other vote, flag order or text keep the click through the usual reading.
// Known gaps, besides those in adoexact.go (zsh snapshot, PATH for bash and az, files changed after the hook):
//   - Anyone who can write the qa/ folder can write a PASS verdict: the guard checks that one exists, not who wrote it.
//   - A push to the PR between the hook's head read and the vote is not seen.

var (
	// prBuildSHA is the pinned SHA-256 of pr-build.sh (testdata/pr-build/pr-build.sh); read the change before pinning.
	prBuildSHA = "ce1d5d0b2babceab978e313cf9005f4a9ef968645e84e39c80cceeed45e354bd"
	// prHead gives the PR's current source head, the commit QA judged.
	prHead = func(id string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "az", "repos", "pr", "show", "--id", id, "--org", adoOrg,
			"--query", "lastMergeSourceCommit.commitId", "-o", "tsv").Output()
		return strings.TrimSpace(string(out)), err
	}
)

const adoOrg = "https://dev.azure.com/Appxite"

var (
	quotesTextRe = regexp.MustCompile(`^[A-Za-z0-9._/:=~ ;$?"-]+$`)
	quotesWordRe = regexp.MustCompile(`^[A-Za-z0-9._/:=-]+$`)
	prIDRe       = regexp.MustCompile(`^[0-9]{1,9}$`)
	sha40Re      = regexp.MustCompile(`^[0-9a-f]{40}$`)
	passRe       = regexp.MustCompile(`\bPASS\b`)
	failRe       = regexp.MustCompile(`\b(FAIL|FAILED|BLOCK|BLOCKED)\b`)
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
		if why := pinned(script, prBuildSHA); why != "" {
			return true, why
		}
		if v := os.Getenv("BASH_ENV"); v != "" {
			return true, asked("pr-build.sh with BASH_ENV set in the session (" + v + "): bash runs it first")
		}
		for _, kv := range os.Environ() {
			if strings.HasPrefix(kv, "BASH_FUNC_") {
				name, _, _ := strings.Cut(kv, "=")
				return true, asked("pr-build.sh with an exported bash function in the session (" + name + "): bash runs it in place of a command")
			}
		}
		return true, ""
	case len(args) == 12 && strings.Join(args[:5], " ") == "az repos pr set-vote --id" && prIDRe.MatchString(args[5]) &&
		strings.Join(args[6:], " ") == "--vote approve --org "+adoOrg+" -o none":
		return true, qaPassed(args[5], filepath.Join(build, "qa"))
	}
	return false, ""
}

// qaPassed asks unless qa/*-<head8>/verdict.md says on its first line PR <id> and PASS for the PR's current head.
func qaPassed(id, qa string) string {
	head, err := prHead(id)
	if err != nil || !sha40Re.MatchString(head) {
		return asked("approving PR " + id + ": the guard cannot read the head (lastMergeSourceCommit) to match a QA verdict")
	}
	if real, err := filepath.EvalSymlinks(qa); err != nil || real != qa {
		return asked("approving PR " + id + ": " + qa + " is not a real folder")
	}
	idRe := regexp.MustCompile(`(^|[^0-9A-Za-z])PR ` + id + `([^0-9]|$)`)
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
		if idRe.MatchString(first) && passRe.MatchString(first) && !failRe.MatchString(first) {
			return ""
		}
	}
	return asked("approving PR " + id + ": no QA verdict at " + qa + "/*-" + head[:8] + "/verdict.md whose first line says PR " + id + " and PASS")
}
