// Command crius-install-box builds the Crius tools a Linux box runs (boxtools.Tools) from this checkout and copies
// them into the box's ~/.crius/bin. Run it through bin/crius-install-box, which sets CRIUS_REPO.
//
//	crius-install-box <box> [--dry-run] [--allow-dirty]
//
// <box> is a host name in ~/.crius/state/playbook/hosts.json (legion, thinkpad), reached with that host's ssh
// command, as crius-tsend and wakewatch reach it. The build is GOOS=linux, GOARCH from the box's uname -m,
// CGO_ENABLED=0, from a shared clone at HEAD so Go stamps the commit (go version -m shows vcs.revision); a tree
// with changes is refused unless --allow-dirty, which builds the working tree unstamped. Each binary goes up on ssh's stdin to a temp name, is checked against its sha256, the old one is kept
// as <name>.prev-<new short sha>, and only then is it moved into place, so a cut copy leaves the old binary running.
// After the copies it reads every binary's sha256 back and prints one line per tool: old, new, OK or FAILED.
// --dry-run builds locally and only runs uname -m and ls -l on the box.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/boxtools"
	"github.com/orzazade/crius/internal/playbook"
	"github.com/orzazade/crius/internal/store"
)

// deps are what tests replace: the checkout, the hosts file and the build.
type deps struct {
	repo  string
	hosts string
	// build cross-builds ./cmd/<name> in dir for linux/<goarch> into out; stamped, it returns the vcs.revision
	// stamped in the binary, else it builds with -buildvcs=false and returns "".
	build func(dir, goarch, name, out string, stamped bool) (string, error)
}

// callTimeout bounds each ssh call, so a hung box cannot hold the install.
const callTimeout = 5 * time.Minute

func main() {
	repo := os.Getenv("CRIUS_REPO")
	if repo == "" {
		fmt.Fprintln(os.Stderr, "crius-install-box: CRIUS_REPO is not set; run bin/crius-install-box from the checkout")
		os.Exit(2)
	}
	hosts := filepath.Join(store.Home(), "state", "playbook", "hosts.json")
	os.Exit(run(os.Args[1:], deps{repo: repo, hosts: hosts, build: goBuild}, os.Stdout, os.Stderr))
}

// goBuild runs the real cross-build and reads the revision back with go version -m.
func goBuild(dir, goarch, name, out string, stamped bool) (string, error) {
	// -trimpath keeps the temp clone's path out of the binary, so one commit always gives the same sha256.
	cmd := exec.Command("go", "-C", dir, "build", "-trimpath", fmt.Sprintf("-buildvcs=%t", stamped), "-o", out, "./cmd/"+name)
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOFLAGS=", "GOOS=linux", "GOARCH="+goarch, "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("go build %s: %v\n%s", name, err, b)
	}
	if !stamped {
		return "", nil
	}
	info, err := exec.Command("go", "version", "-m", out).Output()
	if err != nil {
		return "", fmt.Errorf("go version -m %s: %v", name, err)
	}
	m := regexp.MustCompile(`(?m)^\s*build\s+vcs\.revision=([0-9a-f]{40})$`).FindSubmatch(info)
	if m == nil {
		return "", fmt.Errorf("%s has no vcs.revision stamp:\n%s", name, info)
	}
	return string(m[1]), nil
}

// goarchOf maps uname -m to GOARCH.
var goarchOf = map[string]string{"x86_64": "amd64", "aarch64": "arm64", "arm64": "arm64"}

func run(args []string, d deps, out, errs io.Writer) int {
	var boxName string
	dry, allowDirty := false, false
	for _, a := range args {
		switch {
		case a == "--dry-run" || a == "-dry-run":
			dry = true
		case a == "--allow-dirty" || a == "-allow-dirty":
			allowDirty = true
		case strings.HasPrefix(a, "-") || boxName != "":
			fmt.Fprintln(errs, "usage: crius-install-box <box> [--dry-run] [--allow-dirty]")
			return 2
		default:
			boxName = a
		}
	}
	if boxName == "" {
		fmt.Fprintln(errs, "usage: crius-install-box <box> [--dry-run] [--allow-dirty]")
		return 2
	}
	fail := func(format string, a ...any) int {
		fmt.Fprintf(errs, "crius-install-box: NOTHING INSTALLED: "+format+"\n", a...)
		return 2
	}

	hosts, err := playbook.LoadHosts(d.hosts)
	if err != nil {
		return fail("%v", err)
	}
	var h *playbook.Host
	var names []string
	for i := range hosts {
		names = append(names, hosts[i].Name)
		if strings.EqualFold(hosts[i].Name, boxName) {
			h = &hosts[i]
		}
	}
	if h == nil {
		return fail("no box %q in %s (it has: %s)", boxName, d.hosts, strings.Join(names, ", "))
	}

	// Each git read is checked: a git that fails stops here, never reads as a clean tree.
	status, err := git(d.repo, "status", "--porcelain")
	if err != nil {
		return fail("%v", err)
	}
	if status != "" && !allowDirty {
		return fail("%s has changes not committed; commit them, or pass --allow-dirty to install them anyway", d.repo)
	}
	head, err := git(d.repo, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return fail("%v", err)
	}
	short := head[:7]
	mark := short
	if status != "" {
		mark += "+dirty"
	}

	arch, err := remote(h, "uname -m", nil)
	if err != nil {
		return fail("uname -m on %s: %v", h.Name, err)
	}
	goarch, ok := goarchOf[strings.TrimSpace(arch)]
	if !ok {
		return fail("%s reports uname -m %q, which has no GOARCH here", h.Name, strings.TrimSpace(arch))
	}

	tmp, err := os.MkdirTemp("", "crius-install-box-")
	if err != nil {
		return fail("%v", err)
	}
	defer os.RemoveAll(tmp)
	// A clean tree builds from a clone at HEAD, stamped by Go. A dirty tree (--allow-dirty) builds the working tree
	// itself, unstamped: the box then shows "-" as its revision, and only the sha256 check holds.
	src, stamped := d.repo, status == ""
	if stamped {
		if src, err = buildSource(d.repo, head, tmp); err != nil {
			return fail("%v", err)
		}
	} else {
		fmt.Fprintf(errs, "crius-install-box: building uncommitted changes in %s, unstamped (--allow-dirty)\n", d.repo)
	}
	type tool struct{ name, path, sum string }
	var tools []tool
	for _, name := range boxtools.Tools {
		p := filepath.Join(tmp, name)
		rev, err := d.build(src, goarch, name, p, stamped)
		if err != nil {
			return fail("%v", err)
		}
		if stamped && rev != head {
			return fail("%s is stamped %s, not HEAD %s", name, rev, head)
		}
		sum, err := fileSum(p)
		if err != nil {
			return fail("%v", err)
		}
		tools = append(tools, tool{name, p, sum})
	}
	fmt.Fprintf(out, "box %s (%s): built %d tools for linux/%s at %s from %s\n", h.Name, h.Target, len(tools), goarch, mark, d.repo)

	if dry {
		paths := make([]string, len(tools))
		for i, t := range tools {
			paths[i] = "~/.crius/bin/" + t.name
		}
		ls, err := remote(h, "ls -l "+strings.Join(paths, " ")+" 2>&1 || true", nil)
		if err != nil {
			return fail("ls -l on %s: %v", h.Name, err)
		}
		fmt.Fprintf(out, "on %s now:\n%s", h.Name, ls)
		for _, t := range tools {
			fmt.Fprintf(out, "%-18s new %s %s  would copy to ~/.crius/bin/%s (old kept as %s.prev-%s)  DRY-RUN\n",
				t.name, mark, t.sum[:12], t.name, t.name, short)
		}
		fmt.Fprintln(out, "dry run: nothing copied")
		return 0
	}

	before, err := state(h)
	if err != nil {
		return fail("reading ~/.crius/bin on %s: %v", h.Name, err)
	}
	copyErr := map[string]string{}
	for _, t := range tools {
		if before[t.name].sum == t.sum {
			continue
		}
		f, err := os.Open(t.path)
		if err != nil {
			copyErr[t.name] = err.Error()
			continue
		}
		_, err = remote(h, installScript(t.name, t.sum, short), f)
		f.Close()
		if err != nil {
			copyErr[t.name] = err.Error()
		}
	}
	after, err := state(h)
	if err != nil {
		fmt.Fprintf(errs, "crius-install-box: FAILED: copies sent, but reading them back on %s failed: %v\n", h.Name, err)
		return 1
	}
	failed := 0
	for _, t := range tools {
		b, a := before[t.name], after[t.name]
		verdict := "OK"
		switch {
		case a.sum != t.sum:
			verdict = "FAILED"
			failed++
		case b.sum == t.sum:
			verdict = "UNCHANGED OK"
		}
		fmt.Fprintf(out, "%-18s old %s %s  new %s %s  %s\n", t.name, b.rev, short12(b.sum), mark, t.sum[:12], verdict)
		if msg := copyErr[t.name]; msg != "" {
			fmt.Fprintf(errs, "%s: %s\n", t.name, strings.TrimSpace(msg))
		}
	}
	// None of these tools runs as a job on a box: Claude Code starts crius-hook and crius-statusline on each event,
	// bus-post and playbook run per call. The next call runs the new binary; nothing to restart.
	fmt.Fprintln(out, "restart: none; every tool starts fresh on its next call")
	if failed > 0 {
		fmt.Fprintf(errs, "crius-install-box: %d of %d tools FAILED on %s; their old binaries are still in place\n", failed, len(tools), h.Name)
		return 1
	}
	return 0
}

// installScript receives one binary on stdin into a temp name in ~/.crius/bin, checks its sha256, keeps the old
// binary as <name>.prev-<short> (an existing backup of that name is the true old one and is kept), and moves the
// new one into place. The temp file goes on any failure; the old binary is touched only by the final mv.
func installScript(name, sum, short string) string {
	return fmt.Sprintf(`set -eu; n=%s; d="$HOME/.crius/bin"; t="$d/.$n.new.$$"
mkdir -p "$d"; trap 'rm -f "$t"' EXIT
cat > "$t"; chmod 755 "$t"
got=$(sha256sum < "$t" | cut -c1-64)
[ "$got" = %s ] || { echo "copy of $n arrived damaged (sha256 $got)" >&2; exit 1; }
if [ -e "$d/$n" ] && [ ! -e "$d/$n.prev-%s" ]; then cp -p "$d/$n" "$d/$n.prev-%s"; fi
mv -f "$t" "$d/$n"`, name, sum, short, short)
}

type binState struct{ sum, rev string }

// state reads each tool's sha256 and stamped revision (short) on the box; a missing tool reads as "-".
// The revision is read from the binary's build info with grep, since a box has no Go.
func state(h *playbook.Host) (map[string]binState, error) {
	script := `d="$HOME/.crius/bin"; for n in ` + strings.Join(boxtools.Tools, " ") + `; do
if [ -f "$d/$n" ]; then s=$(sha256sum < "$d/$n" | cut -c1-64)
r=$(LC_ALL=C grep -ao 'vcs\.revision.[0-9a-f]\{40\}' "$d/$n" | head -1 | cut -c14-20); echo "$n $s ${r:--}"
else echo "$n - -"; fi; done`
	text, err := remote(h, script, nil)
	if err != nil {
		return nil, err
	}
	m := map[string]binState{}
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		f := strings.Fields(line)
		if len(f) == 3 {
			m[f[0]] = binState{sum: f[1], rev: f[2]}
		}
	}
	for _, n := range boxtools.Tools {
		if _, ok := m[n]; !ok {
			return nil, fmt.Errorf("no line for %s in:\n%s", n, text)
		}
	}
	return m, nil
}

// remote runs one shell command on the box with its hosts.json ssh command, stdin when given.
func remote(h *playbook.Host, script string, stdin io.Reader) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	args := append(append([]string{}, h.SSH[1:]...), h.Target, script)
	cmd := exec.CommandContext(ctx, h.SSH[0], args...)
	cmd.Stdin = stdin
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	if err := cmd.Run(); err != nil {
		return o.String(), fmt.Errorf("%v: %s", err, strings.TrimSpace(e.String()))
	}
	return o.String(), nil
}

func git(repo string, args ...string) (string, error) {
	b, err := exec.Command("git", append([]string{"-C", repo}, args...)...).Output()
	if err != nil {
		return "", fmt.Errorf("git %s in %s: %v", strings.Join(args, " "), repo, err)
	}
	return strings.TrimSpace(string(b)), nil
}

func fileSum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	s := sha256.New()
	if _, err := io.Copy(s, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(s.Sum(nil)), nil
}

func short12(sum string) string {
	if len(sum) < 12 {
		return sum
	}
	return sum[:12]
}

// buildSource returns a folder holding exactly head to build from: a shared clone of the checkout in tmp. Go's vcs
// stamping looks for a .git folder and skips a worktree's .git file, so a build in a worktree would stamp the
// revision of whatever repo holds that folder (11-19: the home folder's repo). A clone has its own .git folder.
func buildSource(repo, head, tmp string) (string, error) {
	src := filepath.Join(tmp, "src")
	if _, err := git(repo, "clone", "-q", "--shared", "--no-checkout", repo, src); err != nil {
		return "", err
	}
	if _, err := git(src, "-c", "advice.detachedHead=false", "checkout", "-q", head); err != nil {
		return "", err
	}
	return src, nil
}
