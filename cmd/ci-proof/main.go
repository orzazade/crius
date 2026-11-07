// Command ci-proof prints one line of proof that an Azure DevOps pull request's required build passed on the PR's
// current merge commit, for QA and merge steps to paste as evidence (owner 11-08):
//
//	ci-proof <pr-id>
//	PASS|FAIL|STALE|NONE pr=<id> head=<sha8> merge=<sha8> build=<id> result=<result> suites=<name passed/total,…>
//
// A PASS from several passing policies names each build: build=<id>,<id> suites=<id>: <suites>; <id>: <suites>.
//
// Only the build the PR's blocking Build policy names counts: a manual run on the same merge commit is no proof (owner
// 11-08: a manual run was taken for the policy build).
//
//   - NONE: no blocking Build policy, or it names no finished build yet; the PR's merge is not computed (mergeStatus
//     is not succeeded, so lastMergeCommit may be an old one); or the PR is abandoned.
//   - STALE: the build ran on another commit than the PR's lastMergeCommit, the policy evaluated another head or
//     merge, or the policy marks it expired or not current.
//   - FAIL: it finished on the right commit with any result but succeeded, or its test step shows failed tests.
//
// With several blocking Build policies the worst one is printed; when all pass, the tests of each build are read.
// Suites are the test projects the build's test-step logs name (dotnet's "Passed!  - ... - X.Tests.dll" lines);
// "none-named" when they name none.
//
// head is the source commit Azure DevOps last merged for the PR: in the seconds after a push, before Azure DevOps has
// seen it, the line still names the old head and merge. Run it once the push shows on the PR.
//
// Exit 0 only on PASS, 1 on FAIL, STALE or NONE, 2 when it cannot look (a LOUD line). Every az call reads only; the
// organization comes from az devops configure, the project from the PR.
package main

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

func main() {
	os.Exit(run(os.Args[1:], az, os.Stdout))
}

// az runs the az CLI and gives its stdout; an error carries its stderr.
func az(args ...string) ([]byte, error) {
	out, err := exec.Command("az", args...).Output()
	if ee, ok := err.(*exec.ExitError); ok {
		return out, fmt.Errorf("az %s: %v: %s", strings.Join(args[:min(3, len(args))], " "), err, strings.TrimSpace(string(ee.Stderr)))
	}
	return out, err
}

type pr struct {
	PullRequestID   int     `json:"pullRequestId"`
	Status          string  `json:"status"`
	MergeStatus     string  `json:"mergeStatus"`
	LastMergeCommit *commit `json:"lastMergeCommit"`
	LastMergeSource *commit `json:"lastMergeSourceCommit"`
	Repository      struct {
		Project struct{ Name string } `json:"project"`
	} `json:"repository"`
}

type commit struct {
	CommitID string `json:"commitId"`
}

type evaluation struct {
	Configuration struct {
		IsBlocking bool `json:"isBlocking"`
		IsEnabled  bool `json:"isEnabled"`
		Type       struct {
			DisplayName string `json:"displayName"`
		} `json:"type"`
	} `json:"configuration"`
	Context *struct {
		BuildID           int    `json:"buildId"`
		IsExpired         bool   `json:"isExpired"`
		BuildIsNotCurrent bool   `json:"buildIsNotCurrent"`
		MergeCommit       string `json:"lastMergeCommitId"`
		SourceCommit      string `json:"lastMergeSourceCommitId"`
	} `json:"context"`
}

type build struct {
	ID            int    `json:"id"`
	Status        string `json:"status"`
	Result        string `json:"result"`
	SourceVersion string `json:"sourceVersion"`
	SourceBranch  string `json:"sourceBranch"`
}

// verdict is one blocking Build policy's state, worst first by rank.
type verdict struct {
	state  string
	build  int
	result string
}

var rank = map[string]int{"PASS": 0, "NONE": 1, "STALE": 2, "FAIL": 3}

func run(args []string, az func(...string) ([]byte, error), out io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(out, "usage: ci-proof <ado-pr-id>")
		return 2
	}
	id, err := strconv.Atoi(args[0])
	if err != nil || id <= 0 {
		fmt.Fprintf(out, "usage: ci-proof <ado-pr-id>: %q is not a PR id\n", args[0])
		return 2
	}
	loud := func(what string, err error) int {
		fmt.Fprintf(out, "LOUD ci-proof: PR %d: %s: %v\n", id, what, err)
		return 2
	}
	var p pr
	if err := get(az, &p, "repos", "pr", "show", "--id", args[0], "-o", "json"); err != nil {
		return loud("cannot read the PR", err)
	}
	var merge, head string
	if p.LastMergeCommit != nil {
		merge = p.LastMergeCommit.CommitID
	}
	if p.LastMergeSource != nil {
		head = p.LastMergeSource.CommitID
	}
	// A merge still computing, or in conflict, may leave the last merge commit behind the new head (review P2-2); an
	// abandoned PR merges nothing. Both are said before a missing merge commit is (review r2 P3-3).
	switch {
	case p.Status == "abandoned":
		fmt.Fprintf(out, "NONE pr=%d head=%s merge=%s build=none result=pr-abandoned\n", id, short(head), short(merge))
		return 1
	case p.MergeStatus != "succeeded":
		fmt.Fprintf(out, "NONE pr=%d head=%s merge=%s build=none result=merge-%s\n", id, short(head), short(merge), cmp.Or(p.MergeStatus, "unknown"))
		return 1
	case len(merge) != 40 || len(head) != 40:
		return loud("no merge commit", fmt.Errorf("lastMergeCommit or lastMergeSourceCommit is not a sha"))
	}
	proj := p.Repository.Project.Name
	var evs []evaluation
	if err := get(az, &evs, "repos", "pr", "policy", "list", "--id", args[0], "-o", "json"); err != nil {
		return loud("cannot read its policies", err)
	}
	var worst *verdict
	var passes []int // each passing policy's build, once
	for _, e := range evs {
		c := e.Configuration
		if c.Type.DisplayName != "Build" || !c.IsBlocking || !c.IsEnabled {
			continue
		}
		v := verdict{state: "NONE", result: "none"}
		if e.Context != nil && e.Context.BuildID > 0 {
			v.build = e.Context.BuildID
			var b build
			if err := get(az, &b, "pipelines", "build", "show", "--id", strconv.Itoa(v.build), "--project", proj, "-o", "json"); err != nil {
				return loud(fmt.Sprintf("cannot read build %d", v.build), err)
			}
			v.result = b.Result
			switch {
			// the build, and the policy's own record of what it evaluated, must both be this head and merge
			case b.SourceVersion != merge || b.SourceBranch != fmt.Sprintf("refs/pull/%d/merge", id) || e.Context.IsExpired || e.Context.BuildIsNotCurrent ||
				e.Context.MergeCommit != merge || e.Context.SourceCommit != head:
				v.state = "STALE"
			case b.Status != "completed":
				v.result = b.Status
			case b.Result == "succeeded":
				v.state = "PASS"
			default:
				v.state = "FAIL"
			}
		}
		if v.state == "PASS" && !slices.Contains(passes, v.build) {
			passes = append(passes, v.build)
		}
		if worst == nil || rank[v.state] > rank[worst.state] {
			worst = &v
		}
	}
	if worst == nil {
		worst = &verdict{state: "NONE", result: "no-blocking-build-policy"}
	}
	pre := fmt.Sprintf("pr=%d head=%s merge=%s", id, short(head), short(merge))
	switch worst.state {
	case "PASS": // every blocking policy passed: the tests of each of their builds are read (review r2 P3-2)
		var named, ids []string
		for _, b := range passes {
			n, failed, err := suites(az, proj, b)
			if err != nil { // a PASS whose test logs were not read could hide failed tests (review r2 P2-1)
				return loud(fmt.Sprintf("cannot read the test logs of build %d", b), err)
			}
			if failed { // a build that succeeded with failed tests (a "|| true" step) proves nothing (review P3-4)
				fmt.Fprintf(out, "FAIL %s build=%d result=succeeded suites=%s\n", pre, b, n)
				return 1
			}
			if len(passes) > 1 {
				n = strconv.Itoa(b) + ": " + n
			}
			named, ids = append(named, n), append(ids, strconv.Itoa(b))
		}
		fmt.Fprintf(out, "PASS %s build=%s result=succeeded suites=%s\n", pre, strings.Join(ids, ","), strings.Join(named, "; "))
		return 0
	case "FAIL":
		named, _, err := suites(az, proj, worst.build)
		if err != nil {
			return loud(fmt.Sprintf("cannot read the test logs of build %d", worst.build), err)
		}
		fmt.Fprintf(out, "FAIL %s build=%d result=%s suites=%s\n", pre, worst.build, worst.result, named)
	default:
		fmt.Fprintf(out, "%s %s build=%s result=%s\n", worst.state, pre, buildRef(worst.build), worst.result)
	}
	return 1
}

// get runs one az call with the organization az devops configure holds, never one detected from the current folder's
// git remote (review P3-7).
func get(az func(...string) ([]byte, error), v any, args ...string) error {
	b, err := az(append(args, "--detect", "false")...)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("az %s: %v", strings.Join(args[:3], " "), err)
	}
	return nil
}

func short(sha string) string { return sha[:min(8, len(sha))] }

func buildRef(id int) string {
	if id == 0 {
		return "none"
	}
	return strconv.Itoa(id)
}

// dotnet test's summary line: "Passed!  - Failed:     0, Passed:    12, Skipped:     0, Total:    12, Duration: 1 s - Example.Tests.dll (net10.0)"
var suiteRe = regexp.MustCompile(`(Passed|Failed)!\s+-\s+Failed:\s+(\d+),\s+Passed:\s+(\d+),\s+Skipped:\s+\d+,\s+Total:\s+(\d+),.*?-\s+(\S+?)\.dll\b`)

// suites names each test project the logs of the build's test steps (a task named with "test") report, as "name
// passed/total" with "(N failed)" when any failed, and says whether one failed (a "Failed!" run with no failed test,
// an aborted one, counts); "none-named" when they name none. A log it cannot read is an error, never none.
func suites(az func(...string) ([]byte, error), proj string, id int) (named string, failed bool, err error) {
	var tl struct {
		Records []struct {
			Type, Name string
			Log        *struct{ ID int } `json:"log"`
		} `json:"records"`
	}
	route := []string{"devops", "invoke", "--area", "build", "--resource", "timeline", "--route-parameters", "project=" + proj, fmt.Sprintf("buildId=%d", id), "--api-version", "7.1", "-o", "json"}
	if err := get(az, &tl, route...); err != nil {
		return "", false, err
	}
	var found []string
	for _, r := range tl.Records {
		if r.Type != "Task" || r.Log == nil || !strings.Contains(strings.ToLower(r.Name), "test") {
			continue
		}
		var log struct{ Value []string }
		if err := get(az, &log, "devops", "invoke", "--area", "build", "--resource", "logs", "--route-parameters", "project="+proj,
			fmt.Sprintf("buildId=%d", id), fmt.Sprintf("logId=%d", r.Log.ID), "--api-version", "7.1", "-o", "json"); err != nil {
			return "", false, err
		}
		for _, l := range log.Value {
			if m := suiteRe.FindStringSubmatch(l); m != nil {
				s := m[5] + " " + m[3] + "/" + m[4]
				if m[2] != "0" {
					s += " (" + m[2] + " failed)"
				}
				failed = failed || m[1] == "Failed" || m[2] != "0"
				found = append(found, s)
			}
		}
	}
	if found == nil {
		return "none-named", false, nil
	}
	return strings.Join(found, ", "), failed, nil
}
