package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/orzazade/crius/internal/boxtools"
)

// fakeSSH stands in for ssh: it records every remote command and runs it on this machine with HOME set to a fake
// box home, so the installer's real remote scripts run. uname -m answers x86_64. FAKE_CUT=<tool> cuts that tool's
// upload after 5 bytes (a dropped connection mid-copy).
const fakeSSH = `#!/bin/sh
while [ "$1" != "u@box" ]; do shift; done; shift
printf '%s\n' "$*" >> "$FAKE_LOG"
[ "$*" = "uname -m" ] && { echo x86_64; exit 0; }
if [ -n "$FAKE_CUT" ] && case "$*" in *"n=$FAKE_CUT;"*) true;; *) false;; esac; then
  head -c 5 | HOME="$FAKE_BOX" PATH="$FAKE_SHIMS:$PATH" /bin/sh -c "$*"; exit $?
fi
HOME="$FAKE_BOX" PATH="$FAKE_SHIMS:$PATH" exec /bin/sh -c "$*"
`

type box struct {
	dir  string // the fake box's HOME
	log  string // one line per remote command
	deps deps
}

func newBox(t *testing.T) *box {
	t.Helper()
	tmp := t.TempDir()
	b := &box{dir: filepath.Join(tmp, "boxhome"), log: filepath.Join(tmp, "ssh.log")}
	shims := filepath.Join(tmp, "shims")
	for _, d := range []string{filepath.Join(b.dir, ".crius", "bin"), shims} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(t, filepath.Join(tmp, "ssh"), fakeSSH, 0o755)
	// The box is Linux (sha256sum); this Mac has shasum.
	write(t, filepath.Join(shims, "sha256sum"), "#!/bin/sh\nexec shasum -a 256 \"$@\"\n", 0o755)
	for _, name := range boxtools.Tools {
		write(t, filepath.Join(b.dir, ".crius", "bin", name), "old "+name+"\n", 0o755)
	}
	hosts := `[{"name":"legion","target":"u@box","ssh":["` + filepath.Join(tmp, "ssh") + `","-o","BatchMode=yes"]}]`
	write(t, filepath.Join(tmp, "hosts.json"), hosts, 0o600)
	t.Setenv("FAKE_LOG", b.log)
	t.Setenv("FAKE_BOX", b.dir)
	t.Setenv("FAKE_SHIMS", shims)
	t.Setenv("FAKE_CUT", "")
	b.deps = deps{repo: gitRepo(t), hosts: filepath.Join(tmp, "hosts.json"), build: fakeBuild}
	return b
}

// fakeBuild writes a small file per tool in place of a Go cross-build.
func fakeBuild(repo, goarch, name, out string, stamped bool) (string, error) {
	rev, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(rev)), os.WriteFile(out, []byte("new "+name+" "+goarch+"\n"), 0o755)
}

func gitRepo(t *testing.T) string {
	t.Helper()
	r := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "c"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", r}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return r
}

func write(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

func (b *box) bin(name string) string {
	data, err := os.ReadFile(filepath.Join(b.dir, ".crius", "bin", name))
	if err != nil {
		return "missing: " + err.Error()
	}
	return string(data)
}

func (b *box) files(t *testing.T) []string {
	t.Helper()
	es, err := os.ReadDir(filepath.Join(b.dir, ".crius", "bin"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	return names
}

func (b *box) commands(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(b.log)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// toolLines returns the tool name of each result line (lines that start with a tool name), in order.
func toolLines(out string) []string {
	var names []string
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) > 0 && slices.Contains(boxtools.Tools, f[0]) {
			names = append(names, f[0])
		}
	}
	return names
}

func TestDryRunNamesTheBoxToolsAndWritesNothing(t *testing.T) {
	b := newBox(t)
	before := b.files(t)
	var out, errs bytes.Buffer
	if rc := run([]string{"legion", "--dry-run"}, b.deps, &out, &errs); rc != 0 {
		t.Fatalf("dry run exit %d, want 0\nstdout:\n%s\nstderr:\n%s", rc, out.String(), errs.String())
	}
	if got := toolLines(out.String()); !slices.Equal(got, boxtools.Tools) {
		t.Errorf("dry run result lines name %v, want boxtools.Tools %v\n%s", got, boxtools.Tools, out.String())
	}
	if !strings.Contains(out.String(), "linux/amd64") {
		t.Errorf("dry run does not say it builds linux/amd64 for an x86_64 box:\n%s", out.String())
	}
	for _, name := range boxtools.Tools {
		if got := b.bin(name); got != "old "+name+"\n" {
			t.Errorf("dry run changed %s on the box: %q", name, got)
		}
	}
	if after := b.files(t); !slices.Equal(before, after) {
		t.Errorf("dry run changed the box's bin folder: %v -> %v", before, after)
	}
	for _, c := range b.commands(t) {
		if c != "uname -m" && !strings.HasPrefix(c, "ls -l ") {
			t.Errorf("dry run sent a remote command other than uname -m or ls -l: %q", c)
		}
	}
}

func TestInstallCopiesEveryToolWithABackupAndVerifies(t *testing.T) {
	b := newBox(t)
	var out, errs bytes.Buffer
	if rc := run([]string{"legion"}, b.deps, &out, &errs); rc != 0 {
		t.Fatalf("install exit %d, want 0\nstdout:\n%s\nstderr:\n%s", rc, out.String(), errs.String())
	}
	short := strings.TrimSpace(gitOut(t, b.deps.repo, "rev-parse", "--short=7", "HEAD"))
	for _, name := range boxtools.Tools {
		if got := b.bin(name); got != "new "+name+" amd64\n" {
			t.Errorf("%s on the box = %q, want the new build", name, got)
		}
		if got := b.bin(name + ".prev-" + short); got != "old "+name+"\n" {
			t.Errorf("%s.prev-%s = %q, want the old binary", name, short, got)
		}
		if !hasLine(out.String(), name, "OK") {
			t.Errorf("no OK line for %s:\n%s", name, out.String())
		}
	}
	for _, f := range b.files(t) {
		if strings.HasPrefix(f, ".") {
			t.Errorf("temp file left on the box: %s", f)
		}
	}
}

func TestACutCopyFailsLoudAndKeepsTheOldBinary(t *testing.T) {
	b := newBox(t)
	t.Setenv("FAKE_CUT", "bus-post")
	var out, errs bytes.Buffer
	rc := run([]string{"legion"}, b.deps, &out, &errs)
	if rc == 0 {
		t.Fatalf("install with a cut copy exit 0, want non-zero\nstdout:\n%s", out.String())
	}
	if got := b.bin("bus-post"); got != "old bus-post\n" {
		t.Errorf("bus-post on the box after a cut copy = %q, want the old binary untouched", got)
	}
	if !hasLine(out.String(), "bus-post", "FAILED") {
		t.Errorf("no FAILED line for bus-post:\n%s", out.String())
	}
	if !hasLine(out.String(), "crius-hook", "OK") {
		t.Errorf("the other tools should still install; no OK line for crius-hook:\n%s", out.String())
	}
	for _, f := range b.files(t) {
		if strings.HasPrefix(f, ".") {
			t.Errorf("half-written file left on the box: %s", f)
		}
	}
}

func TestADirtyTreeIsRefusedUnlessAllowed(t *testing.T) {
	b := newBox(t)
	write(t, filepath.Join(b.deps.repo, "wip.txt"), "x", 0o644)
	var out, errs bytes.Buffer
	if rc := run([]string{"legion"}, b.deps, &out, &errs); rc == 0 || !strings.Contains(errs.String(), "--allow-dirty") {
		t.Fatalf("dirty tree: exit %d, stderr %q; want a refusal that names --allow-dirty", rc, errs.String())
	}
	if got := b.bin("crius-hook"); got != "old crius-hook\n" {
		t.Errorf("a refused install changed crius-hook: %q", got)
	}
	out.Reset()
	errs.Reset()
	if rc := run([]string{"legion", "--allow-dirty"}, b.deps, &out, &errs); rc != 0 {
		t.Fatalf("--allow-dirty exit %d, want 0\n%s%s", rc, out.String(), errs.String())
	}
}

func TestAnUnknownBoxNamesTheKnownOnes(t *testing.T) {
	b := newBox(t)
	var out, errs bytes.Buffer
	if rc := run([]string{"nope", "--dry-run"}, b.deps, &out, &errs); rc == 0 || !strings.Contains(errs.String(), "legion") {
		t.Fatalf("unknown box: exit %d, stderr %q; want a refusal that names legion", rc, errs.String())
	}
}

func hasLine(out, name, word string) bool {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) > 0 && f[0] == name && f[len(f)-1] == word {
			return true
		}
	}
	return false
}

func gitOut(t *testing.T, repo string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// Go's vcs stamping skips a .git file, so a build in a git worktree stamps the revision of whatever repo holds the
// worktree folder (seen 11-19: the home folder's repo, vcs.modified=true). The tools must carry the checkout's HEAD.
func TestABuildInAWorktreeIsStampedWithItsOwnHead(t *testing.T) {
	gitIn := func(dir string, args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	outer, inner := t.TempDir(), t.TempDir()
	gitIn(outer, "init", "-q")
	gitIn(outer, "commit", "-q", "--allow-empty", "-m", "outer")
	gitIn(inner, "init", "-q")
	write(t, filepath.Join(inner, "go.mod"), "module example.com/x\n\ngo 1.21\n", 0o644)
	if err := os.MkdirAll(filepath.Join(inner, "cmd", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(inner, "cmd", "x", "main.go"), "package main\n\nfunc main() {}\n", 0o644)
	gitIn(inner, "add", ".")
	gitIn(inner, "commit", "-q", "-m", "inner")
	wt := filepath.Join(outer, "wt")
	gitIn(inner, "worktree", "add", "-q", wt)
	head := gitIn(wt, "rev-parse", "HEAD")

	src, err := buildSource(wt, head, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rev, err := goBuild(src, "amd64", "x", filepath.Join(t.TempDir(), "x"), true)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if rev != head {
		t.Fatalf("worktree build stamped %s, want its HEAD %s (outer repo is %s)", rev, head, gitIn(outer, "rev-parse", "HEAD"))
	}
}

// One commit must give the same bytes from any temp clone, or a reinstall never reads as unchanged and the binary
// carries a temp path (seen 11-19: the two boxes got different sha256s for one commit).
func TestTwoBuildsOfOneCommitAreTheSameBytes(t *testing.T) {
	repo := gitRepo(t)
	write(t, filepath.Join(repo, "go.mod"), "module example.com/x\n\ngo 1.21\n", 0o644)
	if err := os.MkdirAll(filepath.Join(repo, "cmd", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(repo, "cmd", "x", "main.go"), "package main\n\nfunc main() { panic(\"x\") }\n", 0o644)
	gitOut(t, repo, "add", ".")
	gitOut(t, repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "x")
	head := strings.TrimSpace(gitOut(t, repo, "rev-parse", "HEAD"))
	var sums []string
	for range 2 {
		src, err := buildSource(repo, head, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		out := filepath.Join(t.TempDir(), "x")
		if _, err := goBuild(src, "amd64", "x", out, true); err != nil {
			t.Fatal(err)
		}
		sum, err := fileSum(out)
		if err != nil {
			t.Fatal(err)
		}
		sums = append(sums, sum)
	}
	if sums[0] != sums[1] {
		t.Fatalf("two builds of %s from two clones differ: %s vs %s", head[:7], sums[0], sums[1])
	}
}
