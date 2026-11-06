package main

import (
	"regexp"
	"strings"
)

// The lead allow list (Crius 11-06 12:16, owner: "we have too much guard"; a click only when a check fails). When the
// usual reading asks in a lead session, the guard reads the call again as Crius mode does, recording each post and
// letting a pinned team script's own posts pass (pins.go: merge.sh, merge-ok.sh, review-gate.sh, pr-build.sh, pass-post.sh,
// ado-suite.ts report); the scripts and code a pinned shell script runs are read as usual or pass on their own pins
// (review r2 P1-6: today's merge-ok.sh runs scripts/heavy.ts from a repo checkout, so merge.sh asks). The call passes when the second reading finds nothing it cannot read and every post is one of:
//   - az pipelines run with only --id <digits>, --branch of a feature/96365-*, integration/96365-bN or develop branch,
//     and --org, --project, --query, --output: no free text. A PR merge build (refs/pull/<n>/merge) is not on the list:
//     it goes through pr-build.sh, which skips a run that exists (today's 3 declined clicks were such duplicates).
//     --variables asks until its names are listed in knownRunVars;
//   - az repos pr create with --source-branch feature/96365-* and --target-branch integration/96365-bN, both given;
// and passes the hard checks of Crius mode (hardCheck: no AI credit in the words or the files they send, no
// --auto-complete, no production URL). Everything else asks as before, with the reason the list refused it. The approve
// vote keeps its own rule (quotesexact.go); reads (pr show/list, runs show/list, ref list, devops invoke GET, account
// get-access-token) never asked and still do not.

// knownRunVars are the pipeline variables az pipelines run may set here; none is named yet.
var knownRunVars = map[string]bool{}

var (
	runOpts        = map[string]bool{"--id": true, "--branch": true, "--variables": true, "--org": true, "--organization": true, "--project": true, "-p": true, "--query": true, "-o": true, "--output": true, "--only-show-errors": true}
	runBranchRe    = regexp.MustCompile(`^(refs/heads/)?(feature/96365-[A-Za-z0-9._/-]+|integration/96365-b[0-9]+|develop)$`)
	prSourceRe     = regexp.MustCompile(`^(refs/heads/)?feature/96365-[A-Za-z0-9._/-]+$`)
	prTargetListRe = regexp.MustCompile(`^(refs/heads/)?integration/96365-b[0-9]+$`)
)

// allowList gives "" when the call that asked with why passes the lead allow list, else the ask.
func allowList(in input, why string) string {
	col := &criusLog{lead: true}
	if w := checkIn(in, col); w != "" {
		if len(col.pinMiss) > 0 {
			return why + " Not a pinned team script: " + col.pinMiss[0]
		}
		return why
	}
	for _, p := range col.posts {
		if r := leadAllowed(p); r != "" {
			return why + " Not on the allow list: " + r
		}
	}
	return ""
}

// leadAllowed gives "" when one post is on the allow list and passes the hard checks, else why not.
func leadAllowed(p postCall) string {
	if p.tool != "az" {
		return p.tool + " is not on the allow list"
	}
	for _, a := range p.args {
		if !a.ok {
			return "az has a word the guard cannot read (" + a.src + ")"
		}
	}
	o := parseAz(p.args)
	switch o.cmd {
	case "pipelines run":
		for k, vs := range o.opts {
			if !runOpts[k] {
				return "az pipelines run " + k + ": only --id, --branch, --org, --project, --query and --output pass"
			}
			for _, v := range vs {
				switch {
				case k == "--id" && !prIDRe.MatchString(v.text):
					return "az pipelines run --id " + v.text + " is not a number"
				case k == "--branch" && !runBranchRe.MatchString(v.text):
					return "az pipelines run --branch " + v.text + ": a PR merge build goes through pr-build.sh; others only on feature/96365-*, integration/96365-bN or develop"
				case k == "--variables":
					if name, _, _ := strings.Cut(v.text, "="); !knownRunVars[name] {
						return "az pipelines run --variables " + name + " is not a known variable"
					}
				}
			}
		}
		if _, set := o.opts["--branch"]; !set {
			return "az pipelines run without --branch runs the pipeline's default branch"
		}
	case "repos pr create":
		src, sset := o.last("--source-branch", "-s")
		tgt, tset := o.last("--target-branch", "-t")
		switch {
		case !sset || !prSourceRe.MatchString(src.text):
			return "az repos pr create needs --source-branch feature/96365-*"
		case !tset || !prTargetListRe.MatchString(tgt.text):
			return "az repos pr create into " + tgt.text + ": only integration/96365-bN passes; develop and main keep the click"
		}
	default:
		return "az " + o.cmd + " is not on the allow list"
	}
	return hardCheck(p)
}
