package main

// A green CI run as a run log (night 11-17): RULES.md 1.4 lets a green CI build of the merged commit be the check, but
// bus-post takes a VERIFIED line only with a run log in ~/.crius/runs. On 11-16 Crius's VERIFIED for Cortex deploy run
// 43924669607 (success, b021f9d) was refused for want of one.
//
//	ci-proof gh [-user <gh-account>] <owner/repo> <run-id>
//	ci-proof ado <org> <project> <build-id>
//
// It reads the run (gh run view, az pipelines build show; read only) and, only when it completed with success
// (GitHub conclusion success, Azure DevOps result succeeded), writes ~/.crius/runs/ci-<gh|ado>-<…>-<id>.log with
// `sha: <commit>` and `pass: 1` in the header bus-post reads, and the source (url, run id, workflow) so a reader sees
// it came from CI. Any other state writes nothing: exit 1, NOPASS names the status and conclusion, and a log an earlier
// green attempt of the same run left is removed. So is a green run beside a failed run of another workflow on the same
// sha (gh run list --commit; for Azure DevOps the newest 100 builds of its branch, LOUD when they do not reach back to
// it), or beside one still running, and an Azure DevOps PR build (its
// sourceVersion is a temporary merge commit: prove a PR with `ci-proof <pr-id>`). Exit 2 (LOUD) when it cannot look. -user reads that account's token with `gh auth token --user` and hands it to gh as GH_TOKEN; without it
// the caller's GH_TOKEN, or gh's active account, is used. It never switches gh's account.

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/orzazade/crius/internal/runlog"
	"github.com/orzazade/crius/internal/store"
)

var (
	repoRe   = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	fullSha  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	unsafeRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
)

type ghRunView struct {
	DatabaseID   int64  `json:"databaseId"`
	Attempt      int    `json:"attempt"`
	Status       string `json:"status"`
	Conclusion   string `json:"conclusion"`
	HeadSha      string `json:"headSha"`
	HeadBranch   string `json:"headBranch"`
	Event        string `json:"event"`
	WorkflowName string `json:"workflowName"`
	DisplayTitle string `json:"displayTitle"`
	URL          string `json:"url"`
}

type adoBuildView struct {
	ID            int64  `json:"id"`
	BuildNumber   string `json:"buildNumber"`
	Status        string `json:"status"`
	Result        string `json:"result"`
	SourceBranch  string `json:"sourceBranch"`
	SourceVersion string `json:"sourceVersion"`
	Definition    struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"definition"`
	Links struct {
		Web struct {
			Href string `json:"href"`
		} `json:"web"`
	} `json:"_links"`
}

const usageCI = "usage: ci-proof gh [-user <gh-account>] <owner/repo> <run-id> | ci-proof ado <org> <project> <build-id>"

// ciRunLog runs `ci-proof gh …` or `ci-proof ado …`.
func ciRunLog(kind string, args []string, out io.Writer) int {
	usage := func(why string) int {
		fmt.Fprintf(out, "%s: %s\n", usageCI, why)
		return 2
	}
	fs := flag.NewFlagSet("ci-proof "+kind, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	user := ""
	if kind == "gh" {
		fs.StringVar(&user, "user", "", "the gh account whose token reads the run")
	}
	if err := fs.Parse(args); err != nil {
		return usage(err.Error())
	}
	args = fs.Args()
	if kind == "gh" {
		if len(args) != 2 {
			return usage("gh takes <owner/repo> <run-id> (flags first)")
		}
		if !repoRe.MatchString(args[0]) {
			return usage(fmt.Sprintf("%q is not owner/repo", args[0]))
		}
		id, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil || id <= 0 {
			return usage(fmt.Sprintf("%q is not a run id", args[1]))
		}
		return ghRunLog(user, args[0], id, out)
	}
	if len(args) != 3 || args[0] == "" || args[1] == "" {
		return usage("ado takes <org> <project> <build-id>")
	}
	id, err := strconv.ParseInt(args[2], 10, 64)
	if err != nil || id <= 0 {
		return usage(fmt.Sprintf("%q is not a build id", args[2]))
	}
	return adoRunLog(args[0], args[1], id, out)
}

// cli runs a read-only CLI call with extra environment; an error carries its stderr.
func cli(env []string, name string, args ...string) ([]byte, error) {
	c := exec.Command(name, args...)
	c.Env = append(os.Environ(), env...) // the last GH_TOKEN wins
	var stderr bytes.Buffer
	c.Stderr = &stderr
	b, err := c.Output()
	if err != nil {
		return b, fmt.Errorf("%s %s: %v: %s", name, strings.Join(args[:min(2, len(args))], " "), err, strings.TrimSpace(stderr.String()))
	}
	return b, nil
}

func ghRunLog(user, repo string, id int64, out io.Writer) int {
	what := fmt.Sprintf("gh %s run %d", repo, id)
	loud := func(err error) int {
		fmt.Fprintf(out, "LOUD ci-proof: %s: %v; no run log written\n", what, err)
		return 2
	}
	var env []string
	if user != "" {
		tok, err := cli(nil, "gh", "auth", "token", "--user", user)
		if err != nil {
			return loud(fmt.Errorf("no token for gh account %s: %w", user, err))
		}
		if t := strings.TrimSpace(string(tok)); t != "" {
			env = []string{"GH_TOKEN=" + t}
		} else {
			return loud(fmt.Errorf("gh auth token --user %s printed no token", user))
		}
	}
	b, err := cli(env, "gh", "run", "view", strconv.FormatInt(id, 10), "--repo", repo, "--json",
		"databaseId,attempt,status,conclusion,headSha,headBranch,event,workflowName,displayTitle,url")
	if err != nil {
		return loud(err)
	}
	var r ghRunView
	if err := json.Unmarshal(b, &r); err != nil {
		return loud(fmt.Errorf("gh run view: %v", err))
	}
	switch {
	case r.DatabaseID != id:
		return loud(fmt.Errorf("gh answered for run %d", r.DatabaseID))
	case !fullSha.MatchString(r.HeadSha):
		return loud(fmt.Errorf("headSha %q is not a commit sha", r.HeadSha))
	}
	owner, name, _ := strings.Cut(repo, "/")
	file := fmt.Sprintf("ci-gh-%s-%s-%d.log", safe(strings.ToLower(owner)), safe(strings.ToLower(name)), id)
	if r.Status != "completed" || r.Conclusion != "success" {
		return nopass(out, file, fmt.Sprintf("%s: status=%s conclusion=%s", what, r.Status, r.Conclusion))
	}
	// Any green run is not proof the commit is green (review P2-1): a labeler run passes while the tests fail.
	lb, err := cli(env, "gh", "run", "list", "--repo", repo, "--commit", r.HeadSha, "--limit", "100", "--json",
		"databaseId,workflowName,status,conclusion")
	if err != nil {
		return loud(fmt.Errorf("cannot read the other runs on %s: %w", r.HeadSha[:8], err))
	}
	var sibs []ghRunView
	if err := json.Unmarshal(lb, &sibs); err != nil {
		return loud(fmt.Errorf("gh run list: %v", err))
	}
	others, red := 0, []string{}
	for _, o := range sibs {
		if o.DatabaseID == id {
			continue
		}
		others++
		switch {
		case o.Status != "completed": // review r2 P2-a: it may still fail; a log written now would stand
			red = append(red, fmt.Sprintf("run %d (%s) still %s", o.DatabaseID, runlog.OneLine(o.WorkflowName), o.Status))
		case slices.Contains([]string{"failure", "timed_out", "startup_failure"}, o.Conclusion):
			red = append(red, fmt.Sprintf("run %d (%s) %s", o.DatabaseID, runlog.OneLine(o.WorkflowName), o.Conclusion))
		}
	}
	if len(red) > 0 {
		return nopass(out, file, fmt.Sprintf("%s: success, but on the same sha %s", what, strings.Join(red, ", ")))
	}
	return writeRunLog(out, file, r.HeadSha, []string{
		"source: CI, not a local run: GitHub Actions run read with gh run view",
		"repo: " + repo,
		fmt.Sprintf("run: %d attempt %d", id, r.Attempt),
		"workflow: " + r.WorkflowName,
		"title: " + r.DisplayTitle,
		"branch: " + r.HeadBranch,
		"event: " + r.Event,
		"status: " + r.Status,
		"conclusion: " + r.Conclusion,
		"url: " + r.URL,
		fmt.Sprintf("other runs on this sha: %d, none failed", others),
		"read: " + now().UTC().Format("2006-01-02T15:04:05Z") + " by ci-proof gh",
	})
}

func adoRunLog(org, project string, id int64, out io.Writer) int {
	what := fmt.Sprintf("ado %s/%s build %d", org, project, id)
	loud := func(err error) int {
		fmt.Fprintf(out, "LOUD ci-proof: %s: %v; no run log written\n", what, err)
		return 2
	}
	orgURL := org
	if !strings.HasPrefix(org, "https://") {
		orgURL = "https://dev.azure.com/" + org
	}
	b, err := cli(nil, "az", "pipelines", "build", "show", "--id", strconv.FormatInt(id, 10), "--org", orgURL,
		"--project", project, "--detect", "false", "-o", "json")
	if err != nil {
		return loud(err)
	}
	var r adoBuildView
	if err := json.Unmarshal(b, &r); err != nil {
		return loud(fmt.Errorf("az pipelines build show: %v", err))
	}
	switch {
	case r.ID != id:
		return loud(fmt.Errorf("az answered for build %d", r.ID))
	case !fullSha.MatchString(r.SourceVersion):
		return loud(fmt.Errorf("sourceVersion %q is not a commit sha", r.SourceVersion))
	}
	file := fmt.Sprintf("ci-ado-%s-%s-%d.log", safe(strings.TrimPrefix(org, "https://dev.azure.com/")), safe(project), id)
	switch {
	case r.Status != "completed" || r.Result != "succeeded":
		return nopass(out, file, fmt.Sprintf("%s: status=%s result=%s", what, r.Status, r.Result))
	case strings.HasPrefix(r.SourceBranch, "refs/pull/"): // review P3-4: a commit no branch keeps after the merge
		return nopass(out, file, fmt.Sprintf("%s: a PR build (%s) tests a temporary merge commit, not the merged one; "+
			"prove a PR with ci-proof <pr-id>", what, r.SourceBranch))
	}
	lb, err := cli(nil, "az", "pipelines", "build", "list", "--org", orgURL, "--project", project, "--branch", r.SourceBranch, "--top", "100",
		"--detect", "false", "-o", "json")
	if err != nil {
		return loud(fmt.Errorf("cannot read the other builds: %w", err))
	}
	var sibs []adoBuildView
	if err := json.Unmarshal(lb, &sibs); err != nil {
		return loud(fmt.Errorf("az pipelines build list: %v", err))
	}
	// review r2 P2-b: a full page of builds all newer than this one may leave a failed build on its sha unread
	if len(sibs) >= 100 && !slices.ContainsFunc(sibs, func(o adoBuildView) bool { return o.ID <= id }) {
		return loud(fmt.Errorf("the newest 100 builds on %s do not reach back to build %d: the other builds on its sha are unread", r.SourceBranch, id))
	}
	others, red := 0, []string{}
	for _, o := range sibs {
		if o.ID == id || o.SourceVersion != r.SourceVersion {
			continue
		}
		others++
		if o.Status != "completed" {
			red = append(red, fmt.Sprintf("build %d (%s) still %s", o.ID, runlog.OneLine(o.Definition.Name), o.Status))
		} else if o.Result == "failed" || o.Result == "partiallySucceeded" {
			red = append(red, fmt.Sprintf("build %d (%s) %s", o.ID, runlog.OneLine(o.Definition.Name), o.Result))
		}
	}
	if len(red) > 0 {
		return nopass(out, file, fmt.Sprintf("%s: succeeded, but on the same sha %s", what, strings.Join(red, ", ")))
	}
	return writeRunLog(out, file,
		r.SourceVersion, []string{
			"source: CI, not a local run: Azure DevOps build read with az pipelines build show",
			"org: " + orgURL,
			"project: " + project,
			fmt.Sprintf("run: %d build %s", id, r.BuildNumber),
			fmt.Sprintf("workflow: %s definition %d", r.Definition.Name, r.Definition.ID),
			"branch: " + r.SourceBranch,
			"status: " + r.Status,
			"conclusion: " + r.Result,
			"url: " + r.Links.Web.Href,
			fmt.Sprintf("other builds on this sha (newest 100 on its branch): %d, none failed", others),
			"read: " + now().UTC().Format("2006-01-02T15:04:05Z") + " by ci-proof ado",
		})
}

// nopass says why a run proves nothing and removes the run log an earlier, green attempt of the same run left
// (review P3-3: a re-run that failed must not leave `pass: 1` behind). Exit 1.
func nopass(out io.Writer, file, why string) int {
	path := filepath.Join(store.Home(), "runs", file)
	switch err := os.Remove(path); {
	case err == nil:
		why += "; removed the older run log " + path
	case !errors.Is(err, fs.ErrNotExist): // review r2 P3: its `pass: 1` still stands for bus-post
		fmt.Fprintf(out, "LOUD ci-proof: NOPASS %s; cannot remove the older run log %s, which bus-post still takes: %v\n", why, path, err)
		return 2
	}
	fmt.Fprintf(out, "NOPASS %s; no run log written\n", why)
	return 1
}

// safe makes a name part fit a run log's bare name, which bus-post finds as [A-Za-z0-9][A-Za-z0-9._-]*\.log.
func safe(s string) string { return strings.Trim(unsafeRe.ReplaceAllString(s, "_"), "_.-") }

// writeRunLog writes the run log under ~/.crius/runs through a temp file and a rename, so a reader never sees half
// a header; the first two lines are the `sha:` and `pass:` bus-post checks.
func writeRunLog(out io.Writer, name, sha string, fields []string) int {
	dir := filepath.Join(store.Home(), "runs")
	fail := func(err error) int {
		fmt.Fprintf(out, "LOUD ci-proof: cannot write run log %s: %v\n", filepath.Join(dir, name), err)
		return 2
	}
	var b strings.Builder
	b.WriteString(runlog.Header(sha, 1))
	for _, f := range fields {
		b.WriteString(runlog.OneLine(f) + "\n")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fail(err)
	}
	tmp, err := os.CreateTemp(dir, ".ci-proof-*.tmp")
	if err != nil {
		return fail(err)
	}
	_, werr := tmp.WriteString(b.String())
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), filepath.Join(dir, name))
	}
	if werr != nil {
		os.Remove(tmp.Name())
		return fail(werr)
	}
	path := filepath.Join(dir, name)
	fmt.Fprintf(out, "RUNLOG %s sha=%s pass=1\nname it in the VERIFIED line: <ref> %s … · %s\n", path, sha, sha[:8], name)
	return 0
}
