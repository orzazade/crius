package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/testutil"
)

// fakeCLI puts fake gh and az binaries first on PATH. Each records its args and GH_TOKEN in <dir>/calls and prints
// <dir>/<name>-<subcommand words>.json (gh-run-view, gh-run-list, az-pipelines-build-show, az-pipelines-build-list);
// a missing file fails the call. `gh auth token --user orkhanappxite` prints tok-appxite, any other user fails as gh does.
// ghJSON and azJSON answer the view and show calls, with an empty list of other runs.
func fakeCLI(t *testing.T, ghJSON, azJSON string) (dir string) {
	return fakeCLIFiles(t, map[string]string{"gh-run-view": ghJSON, "gh-run-list": "[]",
		"az-pipelines-build-show": azJSON, "az-pipelines-build-list": "[]"})
}

func fakeCLIFiles(t *testing.T, files map[string]string) (dir string) {
	t.Helper()
	dir = t.TempDir()
	script := `#!/bin/sh
printf '%s %s|GH_TOKEN=%s\n' "$(basename "$0")" "$*" "$GH_TOKEN" >> "` + dir + `/calls"
if [ "$1 $2" = "auth token" ]; then
	if [ "$4" = orkhanappxite ]; then echo tok-appxite; exit 0; fi
	echo "no oauth token found for github.com account $4" >&2; exit 1
fi
key=$(basename "$0")
for a in "$1" "$2" "$3"; do case "$a" in -*|[0-9]*|"") break;; esac; key="$key-$a"; done
cat "` + dir + `/$key.json"
`
	for name, body := range files {
		testutil.Write(t, filepath.Join(dir, name+".json"), body)
	}
	for _, name := range []string{"gh", "az"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

func calls(t *testing.T, dir string) string {
	b, _ := os.ReadFile(filepath.Join(dir, "calls"))
	return string(b)
}

const ghRun = `{"attempt":1,"conclusion":"%s","databaseId":43924669607,"displayTitle":"feat: deploy\nline two","event":"push",` +
	`"headBranch":"main","headSha":"b021f9d0c3a1e2f4b5a6978877665544332211aa","name":"Deploy","status":"%s",` +
	`"url":"https://github.com/appxite/cortex/actions/runs/43924669607","workflowName":"Deploy"}`

func ghJSON(conclusion, status string) string {
	return strings.Replace(strings.Replace(ghRun, "%s", conclusion, 1), "%s", status, 1)
}

func runCI(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	now = func() time.Time { return time.Date(2026, 11, 17, 6, 30, 0, 0, time.UTC) }
	t.Cleanup(func() { now = time.Now })
	rc := run(args, az, &out)
	return rc, out.String()
}

func runFiles(t *testing.T, home string) []string {
	es, _ := os.ReadDir(filepath.Join(home, "runs"))
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	return names
}

// 11-16: Crius checked Cortex deploy run 43924669607 (success, b021f9d), but its VERIFIED was refused: no run log. A
// green GitHub Actions run becomes a run log in ~/.crius/runs whose header bus-post reads; the golden file is the one
// bus-post's own test posts a VERIFIED with (cmd/bus-post TestACIRunLogFromCIProofIsAccepted).
func TestAGreenGitHubRunBecomesARunLog(t *testing.T) {
	_, home := testutil.Temp(t)
	dir := fakeCLI(t, ghJSON("success", "completed"), "")
	rc, out := runCI(t, "gh", "-user", "orkhanappxite", "appxite/cortex", "43924669607")
	if rc != 0 {
		t.Fatalf("rc %d: %s", rc, out)
	}
	path := filepath.Join(home, "runs", "ci-gh-appxite-cortex-43924669607.log")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err, out)
	}
	want, _ := os.ReadFile(filepath.Join("testdata", "runlog-gh.log"))
	if string(got) != string(want) {
		t.Fatalf("run log:\n%s\nwant:\n%s", got, want)
	}
	if !strings.Contains(out, path) || !strings.Contains(out, "b021f9d0") {
		t.Fatalf("output does not name the log and sha: %s", out)
	}
	c := calls(t, dir)
	if !strings.Contains(c, "gh auth token --user orkhanappxite|") ||
		!strings.Contains(c, "gh run view 43924669607 --repo appxite/cortex --json ") ||
		!strings.Contains(c, "gh run list --repo appxite/cortex --commit b021f9d0c3a1e2f4b5a6978877665544332211aa ") ||
		strings.Count(c, "|GH_TOKEN=tok-appxite\n") != 2 || strings.Contains(c, "switch") {
		t.Fatalf("gh calls:\n%s", c)
	}
}

// Without -user, the caller's GH_TOKEN reaches gh untouched and no account is read or switched.
func TestGHTokenPassesThroughWithoutUser(t *testing.T) {
	testutil.Temp(t)
	t.Setenv("GH_TOKEN", "tok-env")
	dir := fakeCLI(t, ghJSON("success", "completed"), "")
	if rc, out := runCI(t, "gh", "appxite/cortex", "43924669607"); rc != 0 {
		t.Fatalf("rc %d: %s", rc, out)
	}
	if c := calls(t, dir); strings.Contains(c, "auth") || !strings.Contains(c, "|GH_TOKEN=tok-env\n") {
		t.Fatalf("gh calls:\n%s", c)
	}
}

// A run that did not conclude success writes nothing and says why, exit 1.
func TestARunThatDidNotSucceedWritesNothing(t *testing.T) {
	for _, c := range []struct{ conclusion, status, why string }{
		{"failure", "completed", "conclusion=failure"},
		{"cancelled", "completed", "conclusion=cancelled"},
		{"", "in_progress", "status=in_progress"},
		{"success", "in_progress", "status=in_progress"}, // never trust a conclusion before the run completed
	} {
		_, home := testutil.Temp(t)
		fakeCLI(t, ghJSON(c.conclusion, c.status), "")
		rc, out := runCI(t, "gh", "appxite/cortex", "43924669607")
		if rc != 1 || !strings.Contains(out, c.why) || !strings.Contains(out, "no run log written") || len(runFiles(t, home)) != 0 {
			t.Errorf("%+v: rc %d, files %v: %s", c, rc, runFiles(t, home), out)
		}
	}
}

// Cannot look: an unknown gh account, gh failing, or an answer for another run or with no sha is LOUD, exit 2.
func TestCannotLookIsLoud(t *testing.T) {
	for _, c := range []struct {
		name, json string
		args       []string
		why        string
	}{
		{"unknown user", ghJSON("success", "completed"), []string{"-user", "nobody", "appxite/cortex", "43924669607"}, "no oauth token"},
		{"another run", ghJSON("success", "completed"), []string{"appxite/cortex", "43924669608"}, "run 43924669607"},
		{"no sha", strings.Replace(ghJSON("success", "completed"), "b021f9d0c3a1e2f4b5a6978877665544332211aa", "", 1), []string{"appxite/cortex", "43924669607"}, "headSha"},
		{"not json", "HTTP 404: Not Found", []string{"appxite/cortex", "43924669607"}, "gh run view"},
		{"bad repo", ghJSON("success", "completed"), []string{"cortex", "43924669607"}, "owner/repo"},
	} {
		_, home := testutil.Temp(t)
		fakeCLI(t, c.json, "")
		rc, out := runCI(t, append([]string{"gh"}, c.args...)...)
		if rc != 2 || !strings.HasPrefix(out, "LOUD ") && !strings.HasPrefix(out, "usage") || !strings.Contains(out, c.why) || len(runFiles(t, home)) != 0 {
			t.Errorf("%s: rc %d, files %v: %s", c.name, rc, runFiles(t, home), out)
		}
	}
}

const adoBuild = `{"id":9120,"buildNumber":"20261116.3","status":"%s","result":"%s","sourceBranch":"refs/heads/main",` +
	`"sourceVersion":"5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f","definition":{"id":42,"name":"cortex-ci"},` +
	`"_links":{"web":{"href":"https://dev.azure.com/Appxite/Example%20Project/_build/results?buildId=9120"}}}`

func adoJSON(status, result string) string {
	return strings.Replace(strings.Replace(adoBuild, "%s", status, 1), "%s", result, 1)
}

func TestAGreenAzureDevOpsBuildBecomesARunLog(t *testing.T) {
	_, home := testutil.Temp(t)
	dir := fakeCLI(t, "", adoJSON("completed", "succeeded"))
	rc, out := runCI(t, "ado", "Appxite", "Example Project", "9120")
	if rc != 0 {
		t.Fatalf("rc %d: %s", rc, out)
	}
	got, err := os.ReadFile(filepath.Join(home, "runs", "ci-ado-Appxite-Example_Project-9120.log"))
	if err != nil {
		t.Fatal(err, out)
	}
	want, _ := os.ReadFile(filepath.Join("testdata", "runlog-ado.log"))
	if string(got) != string(want) {
		t.Fatalf("run log:\n%s\nwant:\n%s", got, want)
	}
	if c := calls(t, dir); !strings.Contains(c, "az pipelines build show --id 9120 --org https://dev.azure.com/Appxite --project Example Project --detect false -o json|") {
		t.Fatalf("az calls:\n%s", c)
	}
}

func TestAnAzureDevOpsBuildThatDidNotSucceedWritesNothing(t *testing.T) {
	for _, c := range []struct{ status, result, why string }{
		{"completed", "failed", "result=failed"},
		{"completed", "partiallySucceeded", "result=partiallySucceeded"},
		{"completed", "canceled", "result=canceled"},
		{"inProgress", "", "status=inProgress"},
	} {
		_, home := testutil.Temp(t)
		fakeCLI(t, "", adoJSON(c.status, c.result))
		rc, out := runCI(t, "ado", "Appxite", "Example Project", "9120")
		if rc != 1 || !strings.Contains(out, c.why) || !strings.Contains(out, "no run log written") || len(runFiles(t, home)) != 0 {
			t.Errorf("%+v: rc %d, files %v: %s", c, rc, runFiles(t, home), out)
		}
	}
}

// Review P2-1: a green labeler run on a commit whose tests failed proved the commit. A failed run of another workflow
// on the same sha refuses the run log; a cancelled one (a superseded deploy) does not.
func TestAGreenRunBesideAFailedRunOnTheSameShaWritesNothing(t *testing.T) {
	_, home := testutil.Temp(t)
	fakeCLIFiles(t, map[string]string{"gh-run-view": ghJSON("success", "completed"), "gh-run-list": `[
		{"databaseId":43924669607,"workflowName":"Deploy","status":"completed","conclusion":"success"},
		{"databaseId":43924660001,"workflowName":"Tests","status":"completed","conclusion":"failure"},
		{"databaseId":43924660002,"workflowName":"Deploy","status":"completed","conclusion":"cancelled"}]`})
	rc, out := runCI(t, "gh", "appxite/cortex", "43924669607")
	if rc != 1 || !strings.Contains(out, "run 43924660001 (Tests) failure") || strings.Contains(out, "43924660002") ||
		len(runFiles(t, home)) != 0 {
		t.Fatalf("rc %d, files %v: %s", rc, runFiles(t, home), out)
	}
	_, home = testutil.Temp(t)
	fakeCLIFiles(t, map[string]string{"gh-run-view": ghJSON("success", "completed"), "gh-run-list": `[
		{"databaseId":43924660002,"workflowName":"Deploy","status":"completed","conclusion":"cancelled"}]`})
	if rc, out := runCI(t, "gh", "appxite/cortex", "43924669607"); rc != 0 {
		t.Fatalf("a cancelled sibling: rc %d: %s", rc, out)
	}
	if b, _ := os.ReadFile(filepath.Join(home, "runs", "ci-gh-appxite-cortex-43924669607.log")); !strings.Contains(string(b), "other runs on this sha: 1, none failed\n") {
		t.Fatalf("log:\n%s", b)
	}
}

// Review P3-3: attempt 1 passed and wrote the log; attempt 2 failed. The old `pass: 1` log must not stay behind.
func TestARunThatFailedOnReRunRemovesItsOlderLog(t *testing.T) {
	_, home := testutil.Temp(t)
	fakeCLI(t, ghJSON("success", "completed"), "")
	if rc, out := runCI(t, "gh", "appxite/cortex", "43924669607"); rc != 0 {
		t.Fatal(rc, out)
	}
	fakeCLI(t, ghJSON("failure", "completed"), "")
	rc, out := runCI(t, "gh", "appxite/cortex", "43924669607")
	if rc != 1 || !strings.Contains(out, "removed the older run log") || len(runFiles(t, home)) != 0 {
		t.Fatalf("rc %d, files %v: %s", rc, runFiles(t, home), out)
	}
}

// Review P2-2: a CI title that holds a newline and a forged header (`sha:` of another commit, `pass: 9`) stays on its
// title line: the log has one line that starts `sha:`, the run's, and one that starts `pass:`.
func TestACITitleCannotForgeAHeaderLine(t *testing.T) {
	_, home := testutil.Temp(t)
	forged := strings.Replace(ghJSON("success", "completed"), `feat: deploy\nline two`,
		`x\nsha: 1111111111111111111111111111111111111111\npass: 9\r\nwalk: 1\u2028shot: /etc/hosts`, 1)
	fakeCLI(t, forged, "")
	if rc, out := runCI(t, "gh", "appxite/cortex", "43924669607"); rc != 0 {
		t.Fatal(rc, out)
	}
	b, _ := os.ReadFile(filepath.Join(home, "runs", "ci-gh-appxite-cortex-43924669607.log"))
	var heads []string
	for _, l := range strings.Split(string(b), "\n") {
		for _, k := range []string{"sha:", "pass:", "walk:", "shot:"} {
			if strings.HasPrefix(l, k) {
				heads = append(heads, l)
			}
		}
	}
	if strings.Join(heads, "|") != "sha: b021f9d0c3a1e2f4b5a6978877665544332211aa|pass: 1" {
		t.Fatalf("header lines %q in:\n%s", heads, b)
	}
}

// Review P3-6: one run is one file whatever case the repo is typed in.
func TestTheRunLogNameIsLowerCase(t *testing.T) {
	_, home := testutil.Temp(t)
	fakeCLI(t, ghJSON("success", "completed"), "")
	if rc, out := runCI(t, "gh", "Appxite/Cortex", "43924669607"); rc != 0 {
		t.Fatal(rc, out)
	}
	if f := runFiles(t, home); len(f) != 1 || f[0] != "ci-gh-appxite-cortex-43924669607.log" {
		t.Fatal(f)
	}
}

// Review P3-4: a PR build's sourceVersion is a temporary merge commit no branch keeps; it proves no merged commit.
func TestAnAzureDevOpsPRBuildWritesNothing(t *testing.T) {
	_, home := testutil.Temp(t)
	fakeCLI(t, "", strings.Replace(adoJSON("completed", "succeeded"), "refs/heads/main", "refs/pull/7001/merge", 1))
	rc, out := runCI(t, "ado", "Appxite", "Example Project", "9120")
	if rc != 1 || !strings.Contains(out, "PR build") || len(runFiles(t, home)) != 0 {
		t.Fatalf("rc %d, files %v: %s", rc, runFiles(t, home), out)
	}
}

func TestAnAzureDevOpsBuildBesideAFailedBuildOnTheSameShaWritesNothing(t *testing.T) {
	_, home := testutil.Temp(t)
	fakeCLIFiles(t, map[string]string{"az-pipelines-build-show": adoJSON("completed", "succeeded"), "az-pipelines-build-list": `[
		{"id":9120,"status":"completed","result":"succeeded","sourceVersion":"5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f"},
		{"id":9119,"status":"completed","result":"failed","sourceVersion":"0000000000000000000000000000000000000000"},
		{"id":9118,"status":"completed","result":"failed","sourceVersion":"5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f","definition":{"name":"cortex-tests"}}]`})
	rc, out := runCI(t, "ado", "Appxite", "Example Project", "9120")
	if rc != 1 || !strings.Contains(out, "build 9118 (cortex-tests) failed") || strings.Contains(out, "9119") || len(runFiles(t, home)) != 0 {
		t.Fatalf("rc %d, files %v: %s", rc, runFiles(t, home), out)
	}
}

// Review P2-2: the Azure DevOps side is loud when it cannot look, as the gh side is.
func TestAzureDevOpsCannotLookIsLoud(t *testing.T) {
	for _, c := range []struct{ name, show, why string }{
		{"another build", strings.Replace(adoJSON("completed", "succeeded"), `"id":9120`, `"id":9121`, 1), "build 9121"},
		{"no sha", strings.Replace(adoJSON("completed", "succeeded"), "5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f", "C1234", 1), "sourceVersion"},
		{"az fails", "", "az pipelines build"},
	} {
		_, home := testutil.Temp(t)
		files := map[string]string{"az-pipelines-build-list": "[]"}
		if c.show != "" {
			files["az-pipelines-build-show"] = c.show
		}
		fakeCLIFiles(t, files)
		rc, out := runCI(t, "ado", "Appxite", "Example Project", "9120")
		if rc != 2 || !strings.HasPrefix(out, "LOUD ") || !strings.Contains(out, c.why) || len(runFiles(t, home)) != 0 {
			t.Errorf("%s: rc %d, files %v: %s", c.name, rc, runFiles(t, home), out)
		}
	}
}

// Review r2 P2-a: Deploy is green while Tests on the same sha still runs; if Tests then fails, a log written now would
// stand. A sibling run that has not finished writes nothing yet (gh and Azure DevOps).
func TestASiblingStillRunningWritesNothingYet(t *testing.T) {
	_, home := testutil.Temp(t)
	fakeCLIFiles(t, map[string]string{"gh-run-view": ghJSON("success", "completed"), "gh-run-list": `[
		{"databaseId":43924660001,"workflowName":"Tests","status":"in_progress","conclusion":""}]`})
	rc, out := runCI(t, "gh", "appxite/cortex", "43924669607")
	if rc != 1 || !strings.Contains(out, "run 43924660001 (Tests) still in_progress") || len(runFiles(t, home)) != 0 {
		t.Fatalf("gh: rc %d, files %v: %s", rc, runFiles(t, home), out)
	}
	_, home = testutil.Temp(t)
	fakeCLIFiles(t, map[string]string{"az-pipelines-build-show": adoJSON("completed", "succeeded"), "az-pipelines-build-list": `[
		{"id":9121,"status":"inProgress","result":"","sourceVersion":"5e6f7a8b9c0d1e2f3a4b5c6d7e8f9a0b1c2d3e4f","definition":{"name":"cortex-tests"}}]`})
	rc, out = runCI(t, "ado", "Appxite", "Example Project", "9120")
	if rc != 1 || !strings.Contains(out, "build 9121 (cortex-tests) still inProgress") || len(runFiles(t, home)) != 0 {
		t.Fatalf("ado: rc %d, files %v: %s", rc, runFiles(t, home), out)
	}
}

// Review r2 P2-b: the Azure DevOps list holds the newest 100 builds of the build's branch; when all 100 are newer than
// the build, a failed build on its sha may sit outside it: "did not look" is loud, never "none failed".
func TestAnAzureDevOpsListThatDoesNotReachTheBuildIsLoud(t *testing.T) {
	_, home := testutil.Temp(t)
	var list []string
	for i := 0; i < 100; i++ {
		list = append(list, fmt.Sprintf(`{"id":%d,"status":"completed","result":"succeeded","sourceVersion":"0000000000000000000000000000000000000000"}`, 9200+i))
	}
	dir := fakeCLIFiles(t, map[string]string{"az-pipelines-build-show": adoJSON("completed", "succeeded"),
		"az-pipelines-build-list": "[" + strings.Join(list, ",") + "]"})
	rc, out := runCI(t, "ado", "Appxite", "Example Project", "9120")
	if rc != 2 || !strings.Contains(out, "do not reach back to build 9120") || len(runFiles(t, home)) != 0 {
		t.Fatalf("rc %d, files %v: %s", rc, runFiles(t, home), out)
	}
	if c := calls(t, dir); !strings.Contains(c, "az pipelines build list --org https://dev.azure.com/Appxite --project Example Project --branch refs/heads/main --top 100 ") {
		t.Fatalf("az calls:\n%s", c)
	}
}

// Review r2 P3: an older green log that cannot be removed still stands for bus-post: that is loud, exit 2.
func TestAnOlderLogThatCannotBeRemovedIsLoud(t *testing.T) {
	_, home := testutil.Temp(t)
	fakeCLI(t, ghJSON("success", "completed"), "")
	if rc, out := runCI(t, "gh", "appxite/cortex", "43924669607"); rc != 0 {
		t.Fatal(rc, out)
	}
	runs := filepath.Join(home, "runs")
	os.Chmod(runs, 0o500)
	t.Cleanup(func() { os.Chmod(runs, 0o700) })
	fakeCLI(t, ghJSON("failure", "completed"), "")
	rc, out := runCI(t, "gh", "appxite/cortex", "43924669607")
	if rc != 2 || !strings.Contains(out, "LOUD") || !strings.Contains(out, "cannot remove the older run log") {
		t.Fatalf("rc %d: %s", rc, out)
	}
}
