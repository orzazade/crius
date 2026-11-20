package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/testutil"
)

// busPost is the real bus-post, built once: a fake one hid that WARN is no bus type and that quotes.log is only a copy
// of the Legion's bus (live run 11-20, both refused).
var busPost string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "overlap-bus-post")
	if err != nil {
		panic(err)
	}
	busPost = filepath.Join(dir, "bus-post")
	if out, err := exec.Command("go", "build", "-o", busPost, "../bus-post").CombinedOutput(); err != nil {
		panic(fmt.Sprintf("building bus-post: %v\n%s", err, out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// world is a remote (bare repo), a clone of it that crius-overlap reads, a work clone that pushes branches, and a Crius
// home with the real bus-post.
type world struct {
	root, home, remote, clone, work string
}

func newWorld(t *testing.T) world {
	t.Helper()
	root, home := testutil.Temp(t)
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	w := world{root: root, home: home, remote: filepath.Join(root, "remote.git"), clone: filepath.Join(root, "clone"),
		work: filepath.Join(root, "work")}
	testutil.Git(t, root, "init", "-q", "--bare", "-b", "main", w.remote)
	testutil.GitRepo(t, w.work)
	for _, f := range []string{"a.txt", "b.txt", "c.txt", "pkg/package-lock.json"} {
		testutil.Write(t, filepath.Join(w.work, f), f+"\n")
	}
	testutil.CommitAll(t, w.work)
	testutil.Git(t, w.work, "remote", "add", "origin", w.remote)
	testutil.Git(t, w.work, "push", "-q", "origin", "main")
	testutil.Git(t, root, "clone", "-q", w.remote, w.clone)
	for _, d := range []string{"bin", "bus"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(busPost, filepath.Join(home, "bin", "bus-post")); err != nil {
		t.Fatal(err)
	}
	for _, log := range []string{"demo.log", "crius.log"} { // bus-post posts only to a bus log that exists
		if err := os.WriteFile(filepath.Join(home, "bus", log), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	w.config(t, "demo", `[[repo]]
path = "`+w.clone+`"
remote = "origin"
base = "main"
max_age_days = 7
ignore = ["**/package-lock.json"]
`)
	return w
}

func (w world) config(t *testing.T, project, body string) {
	t.Helper()
	testutil.Write(t, filepath.Join(w.home, "state", project, "overlap.toml"), body)
}

// branch makes a branch off main in the work clone that changes files, and pushes it.
func (w world) branch(t *testing.T, name string, files ...string) {
	t.Helper()
	testutil.Git(t, w.work, "switch", "-q", "-C", name, "main")
	for _, f := range files {
		testutil.Write(t, filepath.Join(w.work, f), name+" "+f+"\n")
	}
	testutil.CommitAll(t, w.work)
	testutil.Git(t, w.work, "push", "-q", "-f", "origin", name)
	testutil.Git(t, w.work, "switch", "-q", "main")
}

// posted is every line on the demo bus, then on Crius's bus, as "<log>|<TYPE> <from> -> <to> — <text>" (no time).
func (w world) posted(t *testing.T) []string {
	t.Helper()
	var lines []string
	for _, log := range []string{"demo.log", "crius.log"} {
		b, err := os.ReadFile(filepath.Join(w.home, "bus", log))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if l == "" {
				continue
			}
			_, rest, _ := strings.Cut(l, " ")
			lines = append(lines, log+"|"+rest)
		}
	}
	return lines
}

func (w world) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := run(w.home, time.Now(), args, &out)
	return out.String(), err
}

// Two branches that change one file give one INFO on the project's bus; a third branch on other files gives none; a
// second run posts nothing new; when the shared set grows the pair is posted again.
func TestTwoBranchesOnOneFileWarnOnce(t *testing.T) {
	w := newWorld(t)
	w.branch(t, "feat/one", "a.txt", "pkg/package-lock.json")
	w.branch(t, "feat/two", "a.txt", "pkg/package-lock.json")
	w.branch(t, "feat/three", "c.txt")
	out, err := w.run(t, "demo")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	got := w.posted(t)
	want := "demo.log|INFO crius:overlap -> demo:lead — feat/one and feat/two both change 1 files: a.txt — land one first, rebase the other"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("posted %q\nwant %q", got, want)
	}
	if !strings.Contains(out, "compared 3 branches") {
		t.Fatalf("output does not say how many branches were compared:\n%s", out)
	}
	if out2, err := w.run(t, "demo"); err != nil || len(w.posted(t)) != 1 {
		t.Fatalf("second run: err %v, posted %q\n%s", err, w.posted(t), out2)
	}
	w.branch(t, "feat/two", "a.txt", "b.txt")
	w.branch(t, "feat/one", "a.txt", "b.txt")
	if _, err := w.run(t, "demo"); err != nil {
		t.Fatal(err)
	}
	got = w.posted(t)
	if len(got) != 2 || !strings.Contains(got[1], "feat/one and feat/two both change 2 files: a.txt, b.txt") {
		t.Fatalf("a grown set is not posted again: %q", got)
	}
}

// A branch whose change is already in base (a squash merge) drops out; a deleted branch drops out of the seen list,
// so a later clash of the same pair is posted again.
func TestLandedAndDeletedBranchesDropOut(t *testing.T) {
	w := newWorld(t)
	w.branch(t, "feat/one", "a.txt")
	w.branch(t, "feat/two", "a.txt")
	if _, err := w.run(t, "demo"); err != nil {
		t.Fatal(err)
	}
	// squash feat/one into main: same content, a new commit; then main changes a.txt again (it moved on)
	testutil.Git(t, w.work, "checkout", "-q", "feat/one", "--", "a.txt")
	testutil.CommitAll(t, w.work)
	testutil.Write(t, filepath.Join(w.work, "a.txt"), "main moved on\n")
	testutil.CommitAll(t, w.work)
	testutil.Git(t, w.work, "push", "-q", "origin", "main")
	w.branch(t, "feat/four", "c.txt")
	out, err := w.run(t, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "compared 2 branches") || !strings.Contains(out, "no clashes") {
		t.Fatalf("the landed branch still counts:\n%s", out)
	}
	seen := testutil.Read(t, filepath.Join(w.home, "state", "demo", "overlap-seen.tsv"))
	if strings.Contains(seen, "feat/one") {
		t.Fatalf("a landed pair stays in the seen list:\n%s", seen)
	}
	testutil.Git(t, w.work, "push", "-q", "origin", "--delete", "feat/one")
	w.branch(t, "feat/five", "a.txt")
	if _, err := w.run(t, "demo"); err != nil {
		t.Fatal(err)
	}
	got := w.posted(t)
	if len(got) != 2 || !strings.Contains(got[1], "feat/five and feat/two both change 1 files: a.txt") {
		t.Fatalf("posted %q", got)
	}
	if err := exec.Command("git", "-C", w.clone, "rev-parse", "--verify", "--quiet", "refs/overlap/origin/feat/one").Run(); err == nil {
		t.Fatal("the deleted branch was not pruned")
	}
	if err := exec.Command("git", "-C", w.clone, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/feat/two").Run(); err == nil {
		t.Fatal("the fetch wrote the team's refs/remotes")
	}
}

// check exits 3 (errClash) and names the clash when the branch shares files with another open branch, else nil; it
// posts nothing. A local branch not pushed yet is checked too.
func TestCheck(t *testing.T) {
	w := newWorld(t)
	w.branch(t, "feat/one", "a.txt")
	w.branch(t, "feat/three", "c.txt")
	testutil.Git(t, w.clone, "switch", "-q", "-c", "local/two")
	testutil.Write(t, filepath.Join(w.clone, "a.txt"), "local\n")
	testutil.Git(t, w.clone, "-c", "user.email=t@example.invalid", "-c", "user.name=t", "commit", "-q", "-am", "x")
	out, err := w.run(t, "demo", "check", "local/two")
	if !errors.Is(err, errClash) || !strings.Contains(out, "local/two and feat/one both change 1 files: a.txt") {
		t.Fatalf("want a clash, got %v\n%s", err, out)
	}
	out, err = w.run(t, "demo", "check", "feat/three")
	if err != nil || !strings.Contains(out, "no clashes") {
		t.Fatalf("want no clash, got %v\n%s", err, out)
	}
	if _, err := w.run(t, "demo", "check", "feat/nope"); err == nil || errors.Is(err, errClash) {
		t.Fatalf("a branch that is nowhere must fail, got %v", err)
	}
	if p := w.posted(t); len(p) != 0 {
		t.Fatalf("check posted %q", p)
	}
}

// Could not look is loud: a fetch that fails, a repo path that is missing or is only a folder inside another repo, a
// base that is not on the remote, and a bad config are errors that say why; nothing is posted.
func TestCouldNotLookIsAnError(t *testing.T) {
	w := newWorld(t)
	w.branch(t, "feat/one", "a.txt")
	w.branch(t, "feat/two", "a.txt")
	testutil.Git(t, w.clone, "remote", "set-url", "origin", filepath.Join(w.root, "gone.git"))
	_, err := w.run(t, "demo")
	if err == nil || !strings.Contains(err.Error(), "fetch") {
		t.Fatalf("want a fetch error, got %v", err)
	}
	testutil.Git(t, w.clone, "remote", "set-url", "origin", w.remote)
	for name, body := range map[string]string{
		"missing path": "[[repo]]\npath = \"" + filepath.Join(w.root, "nope") + "\"\nremote = \"origin\"\nbase = \"main\"\nmax_age_days = 7\n",
		"inside repo":  "[[repo]]\npath = \"" + filepath.Join(w.clone, "pkg") + "\"\nremote = \"origin\"\nbase = \"main\"\nmax_age_days = 7\n",
		"no base":      "[[repo]]\npath = \"" + w.clone + "\"\nremote = \"origin\"\nbase = \"develop\"\nmax_age_days = 7\n",
		"unknown key":  "[[repo]]\npath = \"" + w.clone + "\"\nremote = \"origin\"\nbase = \"main\"\nmax_age_days = 7\nbranch = \"x\"\n",
		"no age":       "[[repo]]\npath = \"" + w.clone + "\"\nremote = \"origin\"\nbase = \"main\"\n",
		"no repos":     "# nothing\n",
	} {
		w.config(t, "bad", body)
		if _, err := w.run(t, "bad"); err == nil {
			t.Errorf("%s: want an error, got none", name)
		}
	}
	if _, err := w.run(t, "nothere"); err == nil || !strings.Contains(err.Error(), "overlap.toml") {
		t.Fatalf("a project with no config must fail, got %v", err)
	}
	if p := w.posted(t); len(p) != 0 {
		t.Fatalf("posted %q", p)
	}
}

// all runs every project that has an overlap.toml; a project that cannot be looked at is posted once to Crius's bus
// (not every 10 min) and the run fails; the others still run.
func TestAllRunsEveryProjectAndPostsAFailureOnce(t *testing.T) {
	w := newWorld(t)
	w.branch(t, "feat/one", "a.txt")
	w.branch(t, "feat/two", "a.txt")
	w.config(t, "broken", "[[repo]]\npath = \""+filepath.Join(w.root, "nope")+"\"\nremote = \"origin\"\nbase = \"main\"\nmax_age_days = 7\n")
	for i := 0; i < 2; i++ {
		if _, err := w.run(t, "all"); err == nil {
			t.Fatal("want an error for the broken project")
		}
	}
	got := w.posted(t)
	if len(got) != 2 || !strings.Contains(got[0]+got[1], "demo.log|INFO crius:overlap -> demo:lead — feat/one and feat/two") ||
		!strings.Contains(got[0]+got[1], "crius.log|ALERT crius:overlap -> crius:lead — crius-overlap broken could not look:") {
		t.Fatalf("posted %q", got)
	}
	os.Remove(filepath.Join(w.home, "state", "broken", "overlap.toml"))
	if _, err := w.run(t, "all"); err != nil {
		t.Fatal(err)
	}
}

// A line bus-post refuses is not marked as said: the run fails, and once the bus takes lines again the pair and the
// failure reason are each posted once.
func TestARefusedPostIsTriedAgain(t *testing.T) {
	w := newWorld(t)
	w.branch(t, "feat/one", "a.txt")
	w.branch(t, "feat/two", "a.txt")
	w.config(t, "broken", "[[repo]]\npath = \""+filepath.Join(w.root, "nope")+"\"\nremote = \"origin\"\nbase = \"main\"\nmax_age_days = 7\n")
	for _, log := range []string{"demo.log", "crius.log"} {
		if err := os.Remove(filepath.Join(w.home, "bus", log)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.run(t, "all"); err == nil || !strings.Contains(err.Error(), "no bus log") {
		t.Fatalf("want the refusal as an error, got %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(w.home, "state", "demo", "overlap-seen.tsv")); strings.Contains(string(b), "feat/one") {
		t.Fatalf("a pair that was not posted is marked as seen:\n%s", b)
	}
	for _, log := range []string{"demo.log", "crius.log"} {
		if err := os.WriteFile(filepath.Join(w.home, "bus", log), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if _, err := w.run(t, "all"); err == nil {
			t.Fatal("want an error for the broken project")
		}
	}
	got := w.posted(t)
	if len(got) != 2 || !strings.HasPrefix(got[0], "demo.log|INFO crius:overlap -> demo:lead — feat/one and feat/two") ||
		!strings.HasPrefix(got[1], "crius.log|ALERT crius:overlap -> crius:lead — crius-overlap broken could not look:") {
		t.Fatalf("want the pair and the failure posted once each, got %q", got)
	}
}

func TestUsage(t *testing.T) {
	w := newWorld(t)
	for _, args := range [][]string{{}, {"../x"}, {"demo", "check"}, {"demo", "fly"}} {
		var u usage
		if _, err := w.run(t, args...); !errors.As(err, &u) {
			t.Errorf("%q: want a usage error, got %v", args, err)
		}
	}
}

// pr opens a PR the way Azure DevOps and GitHub show one in git: refs/pull/<id>/merge, a merge of the branch into base.
func (w world) pr(t *testing.T, id, name string) {
	t.Helper()
	testutil.Git(t, w.work, "switch", "-q", "--detach", "main")
	testutil.Git(t, w.work, "merge", "-q", "--no-ff", "-m", "merge", name)
	testutil.Git(t, w.work, "push", "-q", "-f", "origin", "HEAD:refs/pull/"+id+"/merge")
	testutil.Git(t, w.work, "switch", "-q", "main")
}

// With prs = true only branches with an open PR are open work: a branch whose PR was completed or abandoned (live AXI
// 11-20: 29 of 34 clashing branches) is not. check still compares a branch with no PR yet against the open PRs, and
// never against its own PR. A PR that goes away drops out.
func TestOpenPRsOnly(t *testing.T) {
	w := newWorld(t)
	w.config(t, "demo", "[[repo]]\npath = \""+w.clone+"\"\nremote = \"origin\"\nbase = \"main\"\nmax_age_days = 7\nprs = true\n")
	w.branch(t, "feat/one", "a.txt")
	w.branch(t, "feat/two", "a.txt", "b.txt")
	w.branch(t, "feat/done", "a.txt", "b.txt")
	w.pr(t, "11", "feat/one")
	w.pr(t, "12", "feat/two")
	out, err := w.run(t, "demo")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	got := w.posted(t)
	if len(got) != 1 || !strings.Contains(got[0], "— PR 11 feat/one and PR 12 feat/two both change 1 files: a.txt — ") ||
		!strings.Contains(out, "compared 2 open PRs") {
		t.Fatalf("posted %q\n%s", got, out)
	}
	out, err = w.run(t, "demo", "check", "feat/done")
	if !errors.Is(err, errClash) || !strings.Contains(out, "feat/done and PR 11 feat/one both change 1 files") ||
		!strings.Contains(out, "feat/done and PR 12 feat/two both change 2 files") {
		t.Fatalf("want two clashes, got %v\n%s", err, out)
	}
	out, err = w.run(t, "demo", "check", "feat/one")
	if !errors.Is(err, errClash) || strings.Contains(out, "PR 11") || !strings.Contains(out, "feat/one and PR 12 feat/two") {
		t.Fatalf("want the clash with PR 12 only, got %v\n%s", err, out)
	}
	testutil.Git(t, w.work, "push", "-q", "origin", "--delete", "refs/pull/12/merge")
	out, err = w.run(t, "demo")
	if err != nil || !strings.Contains(out, "compared 1 open PRs") || !strings.Contains(out, "no clashes") {
		t.Fatalf("a closed PR still counts: %v\n%s", err, out)
	}
}

// A first run on a busy repo posts at most 5 pair lines and one line for the rest (live Quotes 11-20: 29 pairs at once);
// all of them are seen, so the next run posts nothing.
func TestABurstIsCut(t *testing.T) {
	w := newWorld(t)
	for _, b := range []string{"feat/a", "feat/b", "feat/c", "feat/d", "feat/e"} {
		w.branch(t, b, "a.txt")
	}
	if _, err := w.run(t, "demo"); err != nil {
		t.Fatal(err)
	}
	got := w.posted(t)
	if len(got) != 6 || !strings.Contains(got[5], "— 5 more clashing pairs: crius-overlap demo lists them") {
		t.Fatalf("posted %d: %q", len(got), got)
	}
	if _, err := w.run(t, "demo"); err != nil || len(w.posted(t)) != 6 {
		t.Fatalf("second run: %v, posted %d", err, len(w.posted(t)))
	}
}

// Review P2-2: a PR merge ref left behind by a push (Azure DevOps lags, and keeps the old one while the PR conflicts)
// is still the branch's own PR for check, and the watch says the ref is not at a branch tip.
func TestStalePRMergeRef(t *testing.T) {
	w := newWorld(t)
	w.config(t, "demo", "[[repo]]\npath = \""+w.clone+"\"\nremote = \"origin\"\nbase = \"main\"\nmax_age_days = 7\nprs = true\n")
	w.branch(t, "feat/one", "a.txt")
	w.pr(t, "11", "feat/one")
	testutil.Git(t, w.work, "switch", "-q", "feat/one")
	testutil.Write(t, filepath.Join(w.work, "a.txt"), "more\n")
	testutil.CommitAll(t, w.work)
	testutil.Git(t, w.work, "push", "-q", "origin", "feat/one")
	testutil.Git(t, w.work, "switch", "-q", "main")
	out, err := w.run(t, "demo", "check", "feat/one")
	if err != nil || !strings.Contains(out, "compared with 0 open PRs") || !strings.Contains(out, "PR 11: its merge ref is not at a branch tip") {
		t.Fatalf("its own old PR counts: %v\n%s", err, out)
	}
}

// Review P2-5: two PRs on one file squash-merged in turn, the second not rebased: base's file holds both changes, so
// the second PR's own content is nowhere in base; it has landed all the same and must not clash with new work.
func TestSquashMergedWithAnotherChangeLands(t *testing.T) {
	w := newWorld(t)
	five := "1\n2\n3\n4\n5\n6\n7\n8\n9\n"
	testutil.Write(t, filepath.Join(w.work, "m.txt"), five)
	testutil.CommitAll(t, w.work)
	testutil.Git(t, w.work, "push", "-q", "origin", "main")
	line := func(name, from, to string) {
		testutil.Git(t, w.work, "switch", "-q", "-C", name, "main")
		testutil.Write(t, filepath.Join(w.work, "m.txt"), strings.Replace(five, from, to, 1))
		testutil.CommitAll(t, w.work)
		testutil.Git(t, w.work, "push", "-q", "-f", "origin", name)
		testutil.Git(t, w.work, "switch", "-q", "main")
	}
	line("feat/a", "1\n", "one\n")
	line("feat/c", "9\n", "nine\n")
	testutil.Write(t, filepath.Join(w.work, "m.txt"), strings.Replace(strings.Replace(five, "1\n", "one\n", 1), "9\n", "nine\n", 1))
	testutil.CommitAll(t, w.work) // both squashed into main; feat/c's m.txt (with 1, not one) never was in main
	testutil.Git(t, w.work, "push", "-q", "origin", "main")
	line("feat/new", "5\n", "five\n")
	out, err := w.run(t, "demo")
	if err != nil || !strings.Contains(out, "compared 1 branches") || len(w.posted(t)) != 0 {
		t.Fatalf("a landed branch still counts: %v, posted %q\n%s", err, w.posted(t), out)
	}
}

// Review P2-6: with auth = "az" the token from az goes to git, and never into the output, an error or the bus.
func TestTheTokenIsNeverShown(t *testing.T) {
	w := newWorld(t)
	bin := filepath.Join(w.root, "fakebin")
	testutil.Write(t, filepath.Join(bin, "az"), "#!/bin/sh\necho tok-SECRET-123\n")
	if err := os.Chmod(filepath.Join(bin, "az"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	testutil.Git(t, w.clone, "remote", "set-url", "origin", "https://tok-SECRET-123@127.0.0.1:9/x.git")
	w.config(t, "demo", "[[repo]]\npath = \""+w.clone+"\"\nremote = \"origin\"\nbase = \"main\"\nmax_age_days = 7\nauth = \"az\"\n")
	out, err := w.run(t, "all")
	if err == nil || !strings.Contains(err.Error(), "git fetch") {
		t.Fatalf("want a fetch error, got %v", err)
	}
	if strings.Contains(out+err.Error()+strings.Join(w.posted(t), ""), "tok-SECRET-123") {
		t.Fatalf("the token shows:\n%s\n%v\n%q", out, err, w.posted(t))
	}
	testutil.Git(t, w.clone, "remote", "set-url", "origin", "git@example.invalid:x.git")
	if _, err := w.run(t, "demo"); err == nil || !strings.Contains(err.Error(), "needs an https remote") {
		t.Fatalf("want the https error, got %v", err)
	}
	testutil.Write(t, filepath.Join(bin, "az"), "#!/bin/sh\necho 'Please run az login' >&2\nexit 1\n")
	testutil.Git(t, w.clone, "remote", "set-url", "origin", "https://127.0.0.1:9/x.git")
	if _, err := w.run(t, "demo"); err == nil || !strings.Contains(err.Error(), "az account get-access-token gave no token") ||
		!strings.Contains(err.Error(), "Please run az login") {
		t.Fatalf("want the az error, got %v", err)
	}
}

// Review P2-7: a repo that cannot be looked at keeps its seen pairs, so when it is back nothing is posted again; the
// other repo of the project still runs.
func TestARepoThatFailsKeepsItsSeenPairs(t *testing.T) {
	w := newWorld(t)
	other := filepath.Join(w.root, "other")
	testutil.Git(t, w.root, "clone", "-q", w.remote, other)
	w.config(t, "demo", "[[repo]]\npath = \""+w.clone+"\"\nremote = \"origin\"\nbase = \"main\"\nmax_age_days = 7\n\n"+
		"[[repo]]\nname = \"other\"\npath = \""+other+"\"\nremote = \"origin\"\nbase = \"main\"\nmax_age_days = 7\n")
	w.branch(t, "feat/one", "a.txt")
	w.branch(t, "feat/two", "a.txt")
	if _, err := w.run(t, "demo"); err != nil || len(w.posted(t)) != 2 {
		t.Fatalf("first run: %v, posted %q", err, w.posted(t))
	}
	if !strings.Contains(w.posted(t)[1], "— other: feat/one and feat/two both change") {
		t.Fatalf("a project of two repos names the repo: %q", w.posted(t))
	}
	testutil.Git(t, other, "remote", "set-url", "origin", filepath.Join(w.root, "gone.git"))
	if out, err := w.run(t, "demo"); err == nil || !strings.Contains(out, "clone: fetched origin") {
		t.Fatalf("second run: want an error and the other repo looked at, got %v\n%s", err, out)
	}
	testutil.Git(t, other, "remote", "set-url", "origin", w.remote)
	if _, err := w.run(t, "demo"); err != nil || len(w.posted(t)) != 2 {
		t.Fatalf("third run posted again: %v, posted %q", err, w.posted(t))
	}
}

// Review P3-17: max_age_days and the branches regexp leave branches out; two refs at one commit are no clash.
func TestFiltersAndTwinRefs(t *testing.T) {
	w := newWorld(t)
	w.config(t, "demo", "[[repo]]\npath = \""+w.clone+"\"\nremote = \"origin\"\nbase = \"main\"\nmax_age_days = 2\nbranches = \"^feat/\"\n")
	w.branch(t, "feat/one", "a.txt")
	w.branch(t, "fix/two", "a.txt")
	testutil.Git(t, w.work, "push", "-q", "origin", "feat/one:refs/heads/feat/twin")
	testutil.Git(t, w.work, "switch", "-q", "-C", "feat/old", "main")
	testutil.Write(t, filepath.Join(w.work, "a.txt"), "old\n")
	testutil.Git(t, w.work, "add", "-A")
	cmd := exec.Command("git", "-C", w.work, "commit", "-q", "-m", "old")
	cmd.Env = append(os.Environ(), "GIT_COMMITTER_DATE=2026-01-01T00:00:00", "GIT_AUTHOR_DATE=2026-01-01T00:00:00")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, b)
	}
	testutil.Git(t, w.work, "push", "-q", "origin", "feat/old")
	testutil.Git(t, w.work, "switch", "-q", "main")
	out, err := w.run(t, "demo")
	if err != nil || !strings.Contains(out, "4 branches on the remote") || !strings.Contains(out, "compared 2 branches") ||
		!strings.Contains(out, "no clashes") {
		t.Fatalf("%v\n%s", err, out)
	}
}

// Review P2-3: all says each new reason once, also a second repo's that starts failing while the first still fails;
// a clean run clears the reasons.
func TestAllSaysEachNewReason(t *testing.T) {
	w := newWorld(t)
	other := filepath.Join(w.root, "other")
	testutil.Git(t, w.root, "clone", "-q", w.remote, other)
	w.config(t, "demo", "[[repo]]\npath = \""+filepath.Join(w.root, "nope")+"\"\nremote = \"origin\"\nbase = \"main\"\nmax_age_days = 7\n\n"+
		"[[repo]]\nname = \"other\"\npath = \""+other+"\"\nremote = \"origin\"\nbase = \"main\"\nmax_age_days = 7\n")
	if _, err := w.run(t, "all"); err == nil || len(w.posted(t)) != 1 {
		t.Fatalf("first: %v %q", err, w.posted(t))
	}
	testutil.Git(t, other, "remote", "set-url", "origin", filepath.Join(w.root, "gone.git"))
	if _, err := w.run(t, "all"); err == nil || len(w.posted(t)) != 2 || !strings.Contains(w.posted(t)[1], "repo other: git fetch") {
		t.Fatalf("second: %v %q", err, w.posted(t))
	}
	if _, err := w.run(t, "all"); err == nil || len(w.posted(t)) != 2 {
		t.Fatalf("third said it again: %v %q", err, w.posted(t))
	}
	w.config(t, "demo", "[[repo]]\npath = \""+w.clone+"\"\nremote = \"origin\"\nbase = \"main\"\nmax_age_days = 7\n")
	if _, err := w.run(t, "all"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(w.home, "state", "demo", "overlap-fail.txt")); !os.IsNotExist(err) {
		t.Fatalf("a clean run left the reasons: %v", err)
	}
}

// Review round 2 P2: the merge rule must not drop real clashes: a branch that deletes a file base has since edited
// (merge-tree keeps base's file, a conflict), and two branches on a file base has since renamed.
func TestMergeRuleKeepsConflictsAndRenames(t *testing.T) {
	w := newWorld(t)
	testutil.Git(t, w.work, "switch", "-q", "-C", "feat/del", "main")
	testutil.Git(t, w.work, "rm", "-q", "b.txt")
	testutil.CommitAll(t, w.work)
	testutil.Git(t, w.work, "push", "-q", "origin", "feat/del")
	testutil.Git(t, w.work, "switch", "-q", "main")
	w.branch(t, "feat/edit-b", "b.txt")
	w.branch(t, "feat/one", "c.txt")
	w.branch(t, "feat/two", "c.txt")
	testutil.Write(t, filepath.Join(w.work, "b.txt"), "base edits b\n")
	testutil.Git(t, w.work, "mv", "c.txt", "c-renamed.txt")
	testutil.CommitAll(t, w.work)
	testutil.Git(t, w.work, "push", "-q", "origin", "main")
	out, err := w.run(t, "demo")
	if err != nil || !strings.Contains(out, "feat/del and feat/edit-b both change 1 files: b.txt") ||
		!strings.Contains(out, "feat/one and feat/two both change 1 files: c.txt") {
		t.Fatalf("a real clash was dropped: %v\n%s", err, out)
	}
}

// Review round 2 P2: a branch with no history shared with base (gh-pages) is left out with a note, not a failed repo.
func TestAnOrphanBranchIsSkipped(t *testing.T) {
	w := newWorld(t)
	w.branch(t, "feat/one", "a.txt")
	w.branch(t, "feat/two", "a.txt")
	testutil.Git(t, w.work, "switch", "-q", "--orphan", "gh-pages")
	testutil.Write(t, filepath.Join(w.work, "index.html"), "x\n")
	testutil.CommitAll(t, w.work)
	testutil.Git(t, w.work, "push", "-q", "origin", "gh-pages")
	testutil.Git(t, w.work, "switch", "-q", "-f", "main")
	out, err := w.run(t, "demo")
	if err != nil || !strings.Contains(out, "gh-pages: no history shared with main; not compared") ||
		!strings.Contains(out, "feat/one and feat/two both change") {
		t.Fatalf("%v\n%s", err, out)
	}
}

// A project whose bus here is only a copy of a bus on another machine (quotes.log, staffy.log: a .remote marker) gets
// its lines on Crius's bus, for its lead, instead of a refusal.
func TestARemoteBusGoesToCrius(t *testing.T) {
	w := newWorld(t)
	testutil.Write(t, filepath.Join(w.home, "bus", "demo.log.remote"), "use bus-post-elsewhere\n")
	w.branch(t, "feat/one", "a.txt")
	w.branch(t, "feat/two", "a.txt")
	if out, err := w.run(t, "demo"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	got := w.posted(t)
	if len(got) != 1 || !strings.HasPrefix(got[0], "crius.log|INFO crius:overlap -> crius:lead — for demo:lead (its bus is on another machine): feat/one and feat/two both change 1 files") {
		t.Fatalf("posted %q", got)
	}
}
