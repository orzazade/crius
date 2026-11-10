package main

import (
	"bytes"
	"cmp"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeAz answers each az call from a testdata file: the PR, its policy list (policy), each build by id (builds), the
// build's timeline (timeline) and its logs. calls records every call.
type fakeAz struct {
	pr, policy, timeline string            // pr defaults to pr.json
	runs, ref, commit    string            // -pipeline: the pipeline's runs on the PR's merge branch, the target ref, the merge commit
	timelines            map[string]string // a timeline per build id, over timeline
	builds               map[string]string
	fail                 string // an az call whose args contain this fails
	calls                []string
}

func (f *fakeAz) az(args ...string) ([]byte, error) {
	a := strings.Join(args, " ")
	f.calls = append(f.calls, a)
	if !strings.HasSuffix(a, " --detect false") { // the organization az devops configure holds, never the folder's remote
		return nil, errors.New("not --detect false: az " + a)
	}
	if f.fail != "" && strings.Contains(a, f.fail) {
		return nil, errors.New("az: ERROR: TF400813: the user is not authorized")
	}
	file := ""
	switch {
	case strings.HasPrefix(a, "repos pr show --id 7001 "):
		file = cmp.Or(f.pr, "pr.json")
	case strings.HasPrefix(a, "repos pr policy list --id 7001 "):
		file = f.policy
	case strings.HasPrefix(a, "pipelines build show --id "):
		file = f.builds[args[4]]
		if !strings.Contains(a, "--project Example Project") {
			return nil, errors.New("no project")
		}
	case strings.HasPrefix(a, "pipelines runs list "):
		if !strings.Contains(a, "--project Example Project --pipeline-ids 42 --branch refs/pull/7001/merge ") {
			return nil, errors.New("not pipeline 42 on the PR's merge branch in its project: az " + a)
		}
		file = f.runs
	case strings.HasPrefix(a, "repos ref list "):
		if !strings.Contains(a, "--project Example Project --repository 3c5f1e0a-7b2d-4c8e-9f61-0d2a4b6c8e10 --filter heads/integration/x-b1 ") {
			return nil, errors.New("not the PR's target ref: az " + a)
		}
		file = f.ref
	case strings.Contains(a, "--resource commits"):
		if !strings.Contains(a, "repositoryId=3c5f1e0a-7b2d-4c8e-9f61-0d2a4b6c8e10 commitId=5d2e8a41c09f3b7716e4a2d8f0c95b13e7a6d402 ") {
			return nil, errors.New("not the merge commit: az " + a)
		}
		file = f.commit
	case strings.Contains(a, "--resource timeline"):
		_, id, _ := strings.Cut(a, "buildId=")
		id, _, _ = strings.Cut(id, " ")
		file = cmp.Or(f.timelines[id], f.timeline)
	case strings.Contains(a, "--resource logs"):
		_, id, _ := strings.Cut(a, "logId=")
		id, _, _ = strings.Cut(id, " ")
		file = "log-" + id + ".json"
	}
	if file == "" {
		return nil, errors.New("no fixture for: az " + a)
	}
	return os.ReadFile(filepath.Join("testdata", file))
}

func proof(t *testing.T, f *fakeAz, flags ...string) (string, int) {
	t.Helper()
	var out bytes.Buffer
	rc := run(append(flags, "7001"), f.az, &out)
	return strings.TrimSpace(out.String()), rc
}

// Owner 11-08: before its policy build, a PR whose only finished run was a manual one on the very same merge commit was
// taken as proven. Only the build the blocking Build policy names counts: none yet is NONE, and the manual run is never
// read.
func TestAManualRunOnTheMergeIsNoProof(t *testing.T) {
	f := &fakeAz{policy: "policy-none.json", builds: map[string]string{"401": "build-401-manual.json"}}
	line, rc := proof(t, f)
	if rc != 1 || line != "NONE pr=7001 head=9a0c47e1 merge=5d2e8a41 build=none result=none" {
		t.Fatalf("rc %d %q", rc, line)
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "pipelines build show") {
			t.Errorf("read a build no policy names: %s", c)
		}
	}
}

// The policy's build ran on an older merge commit, or the policy says its build expired: STALE.
func TestAPolicyBuildOnAnotherMergeIsStale(t *testing.T) {
	for _, c := range []struct{ policy, want string }{
		{"policy-stale.json", "STALE pr=7001 head=9a0c47e1 merge=5d2e8a41 build=400 result=succeeded"},
		{"policy-expired.json", "STALE pr=7001 head=9a0c47e1 merge=5d2e8a41 build=407 result=succeeded"},
	} {
		line, rc := proof(t, &fakeAz{policy: c.policy, builds: map[string]string{"400": "build-400.json", "407": "build-407.json"}})
		if rc != 1 || line != c.want {
			t.Errorf("%s: rc %d %q, want %q", c.policy, rc, line, c.want)
		}
	}
}

// After the policy build on the current merge: PASS with the suites its test step names, exit 0.
func TestThePolicyBuildOnTheCurrentMergePasses(t *testing.T) {
	line, rc := proof(t, &fakeAz{policy: "policy-pass.json", timeline: "timeline.json", builds: map[string]string{"407": "build-407.json"}})
	want := "PASS pr=7001 head=9a0c47e1 merge=5d2e8a41 build=407 result=succeeded suites=Example.Core.Tests 391/391, Example.Api.Tests 1429/1429"
	if rc != 0 || line != want {
		t.Fatalf("rc %d\n got %q\nwant %q", rc, line, want)
	}
}

// A build that names no suites says so; one still running is no proof; two policies print the worst; a failed one is
// FAIL, and so is a succeeded one whose test step shows failed tests (review P3-4).
func TestOtherStates(t *testing.T) {
	for _, c := range []struct {
		name, policy, timeline string
		builds                 map[string]string
		rc                     int
		want                   string
	}{
		{"no suites", "policy-pass.json", "timeline-notest.json", map[string]string{"407": "build-407.json"}, 0,
			"PASS pr=7001 head=9a0c47e1 merge=5d2e8a41 build=407 result=succeeded suites=none-named"},
		{"running", "policy-pass.json", "", map[string]string{"407": "build-407-running.json"}, 1,
			"NONE pr=7001 head=9a0c47e1 merge=5d2e8a41 build=407 result=inProgress"},
		{"two policies", "policy-two.json", "timeline-notest.json", map[string]string{"407": "build-407.json", "409": "build-409-failed.json"}, 1,
			"FAIL pr=7001 head=9a0c47e1 merge=5d2e8a41 build=409 result=failed suites=none-named"},
		{"failed tests", "policy-pass.json", "timeline-failedtests.json", map[string]string{"407": "build-407.json"}, 1,
			"FAIL pr=7001 head=9a0c47e1 merge=5d2e8a41 build=407 result=succeeded suites=Example.Api.Tests 1427/1429 (2 failed)"},
	} {
		line, rc := proof(t, &fakeAz{policy: c.policy, timeline: c.timeline, builds: c.builds})
		if rc != c.rc || line != c.want {
			t.Errorf("%s: rc %d\n got %q\nwant %q", c.name, rc, line, c.want)
		}
	}
}

// Failing to look is loud and exits 2, never a NONE or a PASS.
func TestFailingToLookIsLoud(t *testing.T) {
	for _, fail := range []string{"repos pr show", "policy list", "build show"} {
		line, rc := proof(t, &fakeAz{policy: "policy-pass.json", builds: map[string]string{"407": "build-407.json"}, fail: fail})
		if rc != 2 || !strings.HasPrefix(line, "LOUD ci-proof: PR 7001: ") || !strings.Contains(line, "TF400813") {
			t.Errorf("%s: rc %d %q", fail, rc, line)
		}
	}
	// a PASS whose test logs were not read could hide failed tests: loud, never exit 0 (review r2 P2-1)
	for _, fail := range []string{"logId=21", "--resource timeline"} {
		line, rc := proof(t, &fakeAz{policy: "policy-pass.json", timeline: "timeline.json", builds: map[string]string{"407": "build-407.json"}, fail: fail})
		if rc != 2 || !strings.HasPrefix(line, "LOUD ci-proof: PR 7001: cannot read the test logs of build 407: ") || !strings.Contains(line, "TF400813") {
			t.Errorf("%s: rc %d %q", fail, rc, line)
		}
	}
	var out bytes.Buffer
	if rc := run([]string{"abc"}, (&fakeAz{}).az, &out); rc != 2 {
		t.Errorf("a bad id: rc %d %q", rc, out.String())
	}
}

// Review P2-2: the PR's merge is not computed (still queued, or in conflict) so lastMergeCommit may be an older one that
// the policy build did pass on; or the policy's own record names another head or merge than the PR's. Neither is proof.
func TestAMergeNotCurrentIsNoProof(t *testing.T) {
	for _, c := range []struct{ pr, policy, want string }{
		{"pr-queued.json", "policy-pass.json", "NONE pr=7001 head=9a0c47e1 merge=5d2e8a41 build=none result=merge-queued"},
		{"pr-conflicts.json", "policy-pass.json", "NONE pr=7001 head=9a0c47e1 merge=5d2e8a41 build=none result=merge-conflicts"},
		{"pr-abandoned.json", "policy-pass.json", "NONE pr=7001 head=9a0c47e1 merge=5d2e8a41 build=none result=pr-abandoned"},
		{"", "policy-otherhead.json", "STALE pr=7001 head=9a0c47e1 merge=5d2e8a41 build=407 result=succeeded"},
		{"", "policy-othermerge.json", "STALE pr=7001 head=9a0c47e1 merge=5d2e8a41 build=407 result=succeeded"},
		// an abandoned PR with no merge commit left is NONE, not "cannot look" (review r2 P3-3)
		{"pr-abandoned-nomerge.json", "policy-pass.json", "NONE pr=7001 head=9a0c47e1 merge= build=none result=pr-abandoned"},
	} {
		line, rc := proof(t, &fakeAz{pr: c.pr, policy: c.policy, timeline: "timeline.json", builds: map[string]string{"407": "build-407.json"}})
		if rc != 1 || line != c.want {
			t.Errorf("%s %s: rc %d\n got %q\nwant %q", c.pr, c.policy, rc, line, c.want)
		}
	}
}

// The policy's own record matches the PR, but the build it names ran on another commit or ref: STALE, each check on
// its own (review r2: no fixture moved the build alone). A "Failed!" run with no failed test (aborted) is FAIL.
func TestTheBuildItselfMustMatch(t *testing.T) {
	for _, c := range []struct{ build, timeline, want string }{
		{"build-407-othersource.json", "timeline.json", "STALE pr=7001 head=9a0c47e1 merge=5d2e8a41 build=407 result=succeeded"},
		{"build-407-branch.json", "timeline.json", "STALE pr=7001 head=9a0c47e1 merge=5d2e8a41 build=407 result=succeeded"},
		{"build-407.json", "timeline-aborted.json", "FAIL pr=7001 head=9a0c47e1 merge=5d2e8a41 build=407 result=succeeded suites=Example.Api.Tests 40/120"},
	} {
		line, rc := proof(t, &fakeAz{policy: "policy-pass.json", timeline: c.timeline, builds: map[string]string{"407": c.build}})
		if rc != 1 || line != c.want {
			t.Errorf("%s %s: rc %d\n got %q\nwant %q", c.build, c.timeline, rc, line, c.want)
		}
	}
}

// Review r2 P3-2: with two blocking Build policies that both pass, the tests of each build are read; failed tests in
// the second one make the line FAIL and name that build.
func TestEveryPassingPolicyBuildsTestsAreRead(t *testing.T) {
	f := &fakeAz{policy: "policy-two-pass.json", timeline: "timeline.json", timelines: map[string]string{"411": "timeline-failedtests.json"},
		builds: map[string]string{"407": "build-407.json", "411": "build-411.json"}}
	line, rc := proof(t, f)
	want := "FAIL pr=7001 head=9a0c47e1 merge=5d2e8a41 build=411 result=succeeded suites=Example.Api.Tests 1427/1429 (2 failed)"
	if rc != 1 || line != want {
		t.Fatalf("rc %d\n got %q\nwant %q", rc, line, want)
	}
	f.timelines = nil
	line, rc = proof(t, f)
	want = "PASS pr=7001 head=9a0c47e1 merge=5d2e8a41 build=407,411 result=succeeded suites=407: Example.Core.Tests 391/391, Example.Api.Tests 1429/1429; 411: Example.Core.Tests 391/391, Example.Api.Tests 1429/1429"
	if rc != 0 || line != want {
		t.Fatalf("rc %d\n got %q\nwant %q", rc, line, want)
	}
}

// Quotes lead 11-08 03:30: the integration branch has no Build policy, so every PR into it read NONE; its proof is the
// team's own pipeline run on refs/pull/<pr>/merge. With -pipeline <id>, a PR with no blocking Build policy is proven by
// the newest run of that pipeline on its merge branch at its current merge commit, and only while that merge commit is
// still on the target's head (Azure DevOps does not compute a new merge commit when only the target moves).
func TestAPipelineRunOnTheCurrentMergeProvesAPRWithNoBuildPolicy(t *testing.T) {
	f := &fakeAz{pr: "pr-b1.json", policy: "policy-nobuild.json", runs: "runs-pass.json", ref: "ref-b1.json", commit: "commit-merge.json", timeline: "timeline.json"}
	line, rc := proof(t, f, "-pipeline", "42")
	want := "PASS pr=7001 head=9a0c47e1 merge=5d2e8a41 build=412 result=succeeded suites=Example.Core.Tests 391/391, Example.Api.Tests 1429/1429"
	if rc != 0 || line != want {
		t.Fatalf("rc %d\n got %q\nwant %q", rc, line, want)
	}
	// with no -pipeline the same PR still reads NONE: the flag is how a team names its pipeline
	if line, rc := proof(t, &fakeAz{pr: "pr-b1.json", policy: "policy-nobuild.json"}); rc != 1 ||
		line != "NONE pr=7001 head=9a0c47e1 merge=5d2e8a41 build=none result=no-blocking-build-policy" {
		t.Errorf("no flag: rc %d %q", rc, line)
	}
}

func TestPipelineRunStates(t *testing.T) {
	for _, c := range []struct {
		name, runs, ref string
		rc              int
		want            string
	}{
		{"target moved", "runs-pass.json", "ref-b1-moved.json", 1,
			"STALE pr=7001 head=9a0c47e1 merge=5d2e8a41 build=412 result=target-moved"},
		{"newest run failed", "runs-rerun-failed.json", "ref-b1.json", 1,
			"FAIL pr=7001 head=9a0c47e1 merge=5d2e8a41 build=413 result=failed suites=Example.Core.Tests 391/391, Example.Api.Tests 1429/1429"},
		{"running", "runs-running.json", "ref-b1.json", 1,
			"NONE pr=7001 head=9a0c47e1 merge=5d2e8a41 build=412 result=inProgress"},
		{"only on an older merge", "runs-othermerge.json", "ref-b1.json", 1,
			"NONE pr=7001 head=9a0c47e1 merge=5d2e8a41 build=none result=no-run-on-merge"},
		{"no run", "runs-none.json", "ref-b1.json", 1,
			"NONE pr=7001 head=9a0c47e1 merge=5d2e8a41 build=none result=no-run-on-merge"},
	} {
		f := &fakeAz{pr: "pr-b1.json", policy: "policy-nobuild.json", runs: c.runs, ref: c.ref, commit: "commit-merge.json", timeline: "timeline.json"}
		line, rc := proof(t, f, "-pipeline", "42")
		if rc != c.rc || line != c.want {
			t.Errorf("%s: rc %d\n got %q\nwant %q", c.name, rc, line, c.want)
		}
	}
}

// A blocking Build policy stays the only proof where one exists: -pipeline never reads the pipeline's runs then.
func TestABuildPolicyWinsOverThePipeline(t *testing.T) {
	f := &fakeAz{policy: "policy-pass.json", timeline: "timeline.json", builds: map[string]string{"407": "build-407.json"}}
	line, rc := proof(t, f, "-pipeline", "42")
	if rc != 0 || !strings.HasPrefix(line, "PASS pr=7001 head=9a0c47e1 merge=5d2e8a41 build=407 ") {
		t.Fatalf("rc %d %q", rc, line)
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "pipelines runs list") {
			t.Errorf("read the pipeline's runs beside a Build policy: %s", c)
		}
	}
}

func TestFailingToReadThePipelineIsLoud(t *testing.T) {
	for _, fail := range []string{"pipelines runs list", "repos ref list", "--resource commits"} {
		f := &fakeAz{pr: "pr-b1.json", policy: "policy-nobuild.json", runs: "runs-pass.json", ref: "ref-b1.json", commit: "commit-merge.json", timeline: "timeline.json", fail: fail}
		line, rc := proof(t, f, "-pipeline", "42")
		if rc != 2 || !strings.HasPrefix(line, "LOUD ci-proof: PR 7001: ") || !strings.Contains(line, "TF400813") {
			t.Errorf("%s: rc %d %q", fail, rc, line)
		}
	}
	// a target ref missing from the listing is a failure to look, never a pass
	f := &fakeAz{pr: "pr-b1.json", policy: "policy-nobuild.json", runs: "runs-pass.json", ref: "runs-none.json", commit: "commit-merge.json", timeline: "timeline.json"}
	if line, rc := proof(t, f, "-pipeline", "42"); rc != 2 || !strings.Contains(line, "refs/heads/integration/x-b1") {
		t.Errorf("no target ref: rc %d %q", rc, line)
	}
	var out bytes.Buffer
	if rc := run([]string{"-pipeline", "x", "7001"}, (&fakeAz{}).az, &out); rc != 2 {
		t.Errorf("a bad pipeline id: rc %d %q", rc, out.String())
	}
}
