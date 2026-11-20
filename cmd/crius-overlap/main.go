// Command crius-overlap warns a team early when two open work branches change the same files, so they land in order
// instead of meeting at merge (AXI PRs 8461, 8462 and 8463 all edited the same files and clashed, 11-20).
//
//	crius-overlap <project>                 fetch, compare, post one INFO per new clashing pair, list every clash
//	crius-overlap <project> check <branch>  exit 3 and print the clashes when <branch> shares files with open work
//	crius-overlap all                       every project with a <home>/state/<project>/overlap.toml (the launchd job)
//
// The config is <home>/state/<project>/overlap.toml: optional project keys first, then one [[repo]] table per repo:
//
//	bus_post = "~/.crius/bin/bus-post-legion"  # optional: post with this command (same arguments as bus-post) …
//	bus_log = "/home/crius/.crius/bus/quotes.log" # … to this log, its path where the command runs (both or neither)
//	owners = { "feature/96365-" = "quotes:dev, quotes:builder-2" } # optional: branch name prefix -> the roles it belongs to
//
//	[[repo]]
//	name = "AppXite.AXI"                  # shown in the bus line when the project has more than one repo
//	path = "~/Projects/x/repo"            # a clone (its top folder, not a folder inside another repo)
//	remote = "origin"                     # a remote of that clone
//	base = "develop"                      # the branch work lands on
//	branches = "^(feature|bugfix)/"       # optional: only branch names this regexp matches
//	max_age_days = 7                      # only branches whose tip is this new
//	auth = "az"                           # optional: fetch with an Azure CLI bearer token (Azure DevOps over https)
//	ignore = ["**/package-lock.json"]     # optional: files never counted (** is any number of folders)
//	prs = true                            # optional: open work is only branches with an open PR into base
//
// Each run fetches every branch of the remote (and with prs, every refs/pull/<id>/merge: Azure DevOps and GitHub show
// each open PR so) into refs/overlap/<remote>/ and refs/overlap-pr/<remote>/ of the clone, namespaces of its own: the
// team's refs/remotes are not touched. For each open branch it takes the files changed since the branch left base, less
// the files whose change has landed (see files). Two branches that share a file are a clash, unless one holds every
// commit of the other that base does not have: a branch built on the other on purpose (a stack) lands after it with no
// clash. Each clash is posted once to <home>/bus/<project>.log (or with bus_post), again only when its shared files grow
// or it goes to someone new; the pairs posted are in <home>/state/<project>/overlap-seen.tsv. A line goes to the roles
// that own its two branches (see owners).
//
// With prs, a PR that conflicts with base may have no merge ref (GitHub) or an old one (Azure DevOps): it is compared at
// the tip its merge ref names, and the output says so.
//
// Exit codes: 0 looked and posted (the output says how many branches were compared), 1 could not look or could not post
// (says why: a fetch, a missing repo, a bad config, a bus that refused the line), 2 bad arguments, 3 check found a clash.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/BurntSushi/toml"
	"github.com/orzazade/crius/internal/board"
	"github.com/orzazade/crius/internal/sessionstate"
	"github.com/orzazade/crius/internal/store"
)

func main() {
	err := run(store.Home(), time.Now(), os.Args[1:], os.Stdout)
	var u usage
	switch {
	case errors.Is(err, errClash):
		os.Exit(3)
	case errors.As(err, &u):
		fmt.Fprintln(os.Stderr, "crius-overlap:", err)
		os.Exit(2)
	case err != nil:
		fmt.Fprintln(os.Stderr, "crius-overlap:", err)
		os.Exit(1)
	}
}

type usage string

func (u usage) Error() string { return string(u) }

var errClash = errors.New("the branch shares files with another open branch")

var errUnrelated = errors.New("no history shared with base")

// burst is the most pair lines one run posts to a project's bus; the rest go in one line (live Quotes 11-20: a first run
// had 29 pairs).
const burst = 5

var projectRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// adoResource is Azure DevOps' resource id for an Azure CLI token (the Quotes scripts fetch the same way).
const adoResource = "499b84ac-1321-427f-aa17-267ca6975798"

func run(home string, now time.Time, args []string, out io.Writer) error {
	if len(args) == 1 && args[0] == "all" {
		return all(home, now, out)
	}
	if len(args) != 1 && !(len(args) == 3 && args[1] == "check") {
		return usage("usage: crius-overlap <project> | <project> check <branch> | all")
	}
	if !projectRe.MatchString(args[0]) || args[0] == "all" {
		return usage(fmt.Sprintf("project %q is not one plain word", args[0]))
	}
	if len(args) == 3 {
		return check(home, now, args[0], args[2], out)
	}
	return watch(home, now, args[0], out, true)
}

// Repo is one [[repo]] table of overlap.toml.
type Repo struct {
	Name     string   `toml:"name"`
	Path     string   `toml:"path"`
	Remote   string   `toml:"remote"`
	Base     string   `toml:"base"`
	Branches string   `toml:"branches"`
	MaxAge   int      `toml:"max_age_days"`
	Auth     string   `toml:"auth"`
	Ignore   []string `toml:"ignore"`
	PRs      bool     `toml:"prs"`
	re       *regexp.Regexp
}

// config is one project's overlap.toml.
type config struct {
	Repos   []Repo            `toml:"repo"`
	BusPost string            `toml:"bus_post"`
	BusLog  string            `toml:"bus_log"`
	Owners  map[string]string `toml:"owners"`
}

var roleName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*:[a-z0-9][a-z0-9-]*$`)

// tilde gives a path with a leading ~/ in the user's home folder.
func tilde(p string) (string, error) {
	if !strings.HasPrefix(p, "~/") {
		return p, nil
	}
	u, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(u, p[2:]), nil
}

func load(home, project string) (config, error) {
	file := filepath.Join(home, "state", project, "overlap.toml")
	var c config
	md, err := toml.DecodeFile(file, &c)
	if err != nil {
		return c, fmt.Errorf("reading %s: %w", file, err)
	}
	if u := md.Undecoded(); len(u) > 0 {
		return c, fmt.Errorf("%s: keys crius-overlap does not know: %v", file, u)
	}
	if len(c.Repos) == 0 {
		return c, fmt.Errorf("%s names no [[repo]]", file)
	}
	if (c.BusPost == "") != (c.BusLog == "") {
		return c, fmt.Errorf("%s: bus_post and bus_log go together (the command, and the log it posts to)", file)
	}
	if c.BusPost, err = tilde(c.BusPost); err != nil {
		return c, err
	}
	if c.BusLog != "" && !strings.HasPrefix(c.BusLog, "/") {
		return c, fmt.Errorf("%s: bus_log is the log's whole path where bus_post runs (no ~: no shell reads it there), got %q",
			file, c.BusLog)
	}
	for prefix, to := range c.Owners {
		var names []string
		for _, n := range strings.Split(to, ",") {
			names = append(names, strings.TrimSpace(n))
		}
		if prefix == "" || slices.ContainsFunc(names, func(n string) bool { return !roleName.MatchString(n) }) {
			return c, fmt.Errorf("%s: owners: %q = %q: want a branch name prefix = \"project:role\" (more roles with commas)",
				file, prefix, to)
		}
		c.Owners[prefix] = strings.Join(names, ", ")
	}
	names := map[string]bool{}
	for i := range c.Repos {
		r := &c.Repos[i]
		if r.Path == "" || r.Remote == "" || r.Base == "" || r.MaxAge <= 0 {
			return c, fmt.Errorf("%s: repo %d needs path, remote, base and max_age_days > 0", file, i+1)
		}
		if r.Auth != "" && r.Auth != "az" {
			return c, fmt.Errorf("%s: repo %s: auth is \"az\" or not set, got %q", file, r.Path, r.Auth)
		}
		if r.Path, err = tilde(r.Path); err != nil {
			return c, err
		}
		if r.Name == "" {
			r.Name = filepath.Base(r.Path)
		}
		if names[r.Name] || strings.ContainsAny(r.Name, "\t\n") {
			return c, fmt.Errorf("%s: repo name %q is used twice or has a tab (name each [[repo]] apart)", file, r.Name)
		}
		names[r.Name] = true
		if r.re, err = regexp.Compile(r.Branches); err != nil {
			return c, fmt.Errorf("%s: repo %s: branches: %w", file, r.Name, err)
		}
		for _, g := range r.Ignore {
			if _, err := path.Match(g, ""); err != nil {
				return c, fmt.Errorf("%s: repo %s: ignore %q: %w", file, r.Name, g, err)
			}
		}
	}
	return c, nil
}

// branch is one open branch (or open PR): the name it is known by, the label a bus line shows, its tip, the branch
// names at that tip, and the files it changes that have not landed, by board.Key.
type branch struct {
	name, label, tip string
	heads            []string
	files            map[string]string
}

// scan is one repo looked at: its git, base sha, how many branches or PRs the remote has, the open ones, and notes on
// PRs read in part.
type scan struct {
	repo   Repo
	git    gitter
	base   string
	listed int
	open   []branch
	notes  []string
	// baseFiles is every path base has, read once
	baseFiles map[string]bool
}

func (s *scan) ref(name string) string { return "refs/overlap/" + s.repo.Remote + "/" + name }
func (s *scan) prRef(id string) string { return "refs/overlap-pr/" + s.repo.Remote + "/" + id }
func (s *scan) unit() string {
	if s.repo.PRs {
		return "open PRs"
	}
	return "branches"
}

// look fetches a repo and lists its open branches. Any step that fails is an error: never "no clashes".
func look(ctx context.Context, r Repo, now time.Time) (*scan, error) {
	fail := func(err error) (*scan, error) { return nil, fmt.Errorf("repo %s: %w", r.Name, err) }
	top, err := filepath.EvalSymlinks(r.Path)
	if err != nil {
		return fail(err)
	}
	s := &scan{repo: r, git: gitter{dir: top, env: append(os.Environ(), "GIT_TERMINAL_PROMPT=0")}}
	got, err := s.git.out(ctx, time.Minute, "rev-parse", "--show-toplevel")
	if err != nil {
		return fail(err)
	}
	if got = strings.TrimSpace(got); got != top {
		return fail(fmt.Errorf("%s is not the top of a git clone (git says the top is %s)", r.Path, got))
	}
	if r.Auth == "az" {
		raw, err := s.git.out(ctx, time.Minute, "remote", "get-url", r.Remote)
		if err != nil {
			return fail(err)
		}
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return fail(fmt.Errorf("auth = \"az\" needs an https remote; %s is %q", r.Remote, strings.TrimSpace(raw)))
		}
		tok, err := azToken(ctx)
		if err != nil {
			return fail(err)
		}
		// In the environment, not on the command line where ps shows it, and only for the remote's host: never to a
		// submodule's or any other host.
		s.git.env = append(s.git.env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.https://"+u.Host+"/.extraHeader",
			"GIT_CONFIG_VALUE_0=Authorization: Bearer "+tok)
		s.git.secret = tok
	}
	// --refmap= keeps git from also moving the team's refs/remotes; --prune drops what the remote dropped. A stalled
	// https transfer gives up after a minute below 1 KB/s.
	args := []string{"-c", "http.lowSpeedLimit=1000", "-c", "http.lowSpeedTime=60", "fetch", "--quiet", "--prune",
		"--no-tags", "--no-write-fetch-head", "--recurse-submodules=no", "--refmap=", r.Remote, "+refs/heads/*:" + s.ref("*")}
	if r.PRs {
		args = append(args, "+refs/pull/*/merge:"+s.prRef("*"))
	}
	if _, err := s.git.out(ctx, 10*time.Minute, args...); err != nil {
		return fail(err)
	}
	refs, err := s.git.out(ctx, time.Minute, "for-each-ref", "--format=%(objectname) %(committerdate:unix) %(refname)",
		"refs/overlap/"+r.Remote+"/")
	if err != nil {
		return fail(err)
	}
	var tips []branch
	heads := map[string][]string{} // sha -> the branches at it
	young := func(unix string) (bool, error) {
		at, err := strconv.ParseInt(strings.TrimSpace(unix), 10, 64)
		return now.Sub(time.Unix(at, 0)) <= time.Duration(r.MaxAge)*24*time.Hour, err
	}
	for _, line := range strings.Split(strings.TrimSpace(refs), "\n") {
		f := strings.SplitN(line, " ", 3)
		if len(f) != 3 {
			continue // no refs at all: one empty line
		}
		name := strings.TrimPrefix(f[2], s.ref(""))
		if name == r.Base {
			s.base = f[0]
			continue
		}
		heads[f[0]] = append(heads[f[0]], name)
		if r.PRs {
			continue
		}
		s.listed++
		ok, err := young(f[1])
		if err != nil {
			return fail(fmt.Errorf("date of %s: %w", name, err))
		}
		if ok && r.re.MatchString(name) {
			tips = append(tips, branch{name: name, label: name, tip: f[0], heads: []string{name}})
		}
	}
	if s.base == "" {
		return fail(fmt.Errorf("base branch %s is not on remote %s", r.Base, r.Remote))
	}
	if r.PRs {
		prs, err := s.git.out(ctx, time.Minute, "for-each-ref", "--format=%(refname)", "refs/overlap-pr/"+r.Remote+"/")
		if err != nil {
			return fail(err)
		}
		for _, ref := range strings.Fields(prs) {
			id := strings.TrimPrefix(ref, s.prRef(""))
			s.listed++
			p, err := s.git.out(ctx, time.Minute, "rev-parse", "--verify", "--quiet", ref+"^2")
			if err != nil {
				s.notes = append(s.notes, "PR "+id+": its merge ref has no second parent; not compared")
				continue
			}
			tip := strings.TrimSpace(p)
			if into, err := s.git.ancestor(ctx, ref+"^1", s.base); err != nil {
				return fail(err)
			} else if !into {
				continue // a PR into another branch
			}
			date, err := s.git.out(ctx, time.Minute, "show", "-s", "--format=%ct", tip)
			if err != nil {
				return fail(err)
			}
			ok, err := young(date)
			if err != nil {
				return fail(fmt.Errorf("date of PR %s: %w", id, err))
			}
			hs := slices.Sorted(slices.Values(heads[tip]))
			label := "PR " + id
			if len(hs) > 0 {
				label += " " + hs[0]
			} else if ok {
				s.notes = append(s.notes, "PR "+id+": its merge ref is not at a branch tip (a conflict or a push not merged yet); compared at "+tip[:12])
			}
			if ok && (len(hs) == 0 || slices.ContainsFunc(hs, r.re.MatchString)) {
				tips = append(tips, branch{name: "PR " + id, label: label, tip: tip, heads: hs})
			}
		}
	}
	for _, t := range tips {
		if t.files, err = s.files(ctx, t.tip); errors.Is(err, errUnrelated) {
			s.notes = append(s.notes, t.label+": no history shared with "+r.Base+"; not compared")
			continue
		} else if err != nil {
			return nil, err
		}
		if len(t.files) > 0 {
			s.open = append(s.open, t)
		}
	}
	return s, nil
}

// files are the files a commit changed since it left base, less the ignored ones and those whose change has landed.
// A file's change has landed when either holds:
//   - base, since the two split, has had that file with the very content the commit gives it: a rebase, squash or merge
//     put it there, and base may have changed it again since (live AXI 11-20: without this, branches landed days ago
//     clashed with every new one, 1571 pairs);
//   - merging the commit into base now leaves the file as base has it, with no conflict on it, and base has the file: a
//     squash merged together with another change to the file, so the commit's own content is nowhere in base. (Without
//     the last two, a branch deleting a file base edited since, or editing one base renamed since, would drop out.)
//
// A commit with no history shared with base (gh-pages) is errUnrelated.
func (s *scan) files(ctx context.Context, sha string) (map[string]string, error) {
	fail := func(err error) (map[string]string, error) { return nil, fmt.Errorf("repo %s: %w", s.repo.Name, err) }
	mb, err := s.git.out(ctx, time.Minute, "merge-base", s.base, sha)
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 && strings.TrimSpace(mb) == "" {
		return nil, errUnrelated
	}
	if err != nil {
		return fail(err)
	}
	mb = strings.TrimSpace(mb)
	changed, err := s.git.out(ctx, time.Minute, "diff", "--raw", "-z", "--no-abbrev", "--no-renames", "--no-ext-diff", mb, sha)
	if err != nil {
		return fail(err)
	}
	if changed == "" {
		return nil, nil
	}
	inBase, err := s.git.out(ctx, 2*time.Minute, "log", "--raw", "-z", "--no-abbrev", "--no-renames", "--format=", mb+".."+s.base)
	if err != nil {
		return fail(err)
	}
	landed := map[string]bool{}
	for path, blob := range raw(inBase) {
		landed[path+"\x00"+blob] = true
	}
	// -z: the tree, then one "<mode> <blob> <stage>\t<path>" per conflicted entry
	merged, err := s.git.out(ctx, 2*time.Minute, "merge-tree", "--write-tree", "--no-messages", "-z", s.base, sha)
	if err != nil && !(errors.As(err, &ee) && ee.ExitCode() == 1) { // 1: a conflict, the tree is still written
		return fail(err)
	}
	entries := strings.Split(merged, "\x00")
	tree, conflicted := entries[0], map[string]bool{}
	for _, e := range entries[1:] {
		if _, path, ok := strings.Cut(e, "\t"); ok {
			conflicted[path] = true
		}
	}
	if s.baseFiles == nil {
		names, err := s.git.out(ctx, time.Minute, "ls-tree", "-r", "-z", "--name-only", s.base)
		if err != nil {
			return fail(err)
		}
		s.baseFiles = map[string]bool{}
		for _, n := range strings.Split(names, "\x00") {
			s.baseFiles[n] = true
		}
	}
	moves, err := s.git.out(ctx, time.Minute, "diff", "--raw", "-z", "--no-abbrev", "--no-renames", "--no-ext-diff", s.base, tree)
	if err != nil {
		return fail(err)
	}
	merging := map[string]bool{}
	for path := range raw(moves) {
		merging[path] = true
	}
	files := map[string]string{}
	for f, blob := range raw(changed) {
		merges := !merging[f] && !conflicted[f] && s.baseFiles[f]
		if !merges && !landed[f+"\x00"+blob] && !slices.ContainsFunc(s.repo.Ignore, func(g string) bool { return glob(g, f) }) {
			files[board.Key(f)] = shown(f)
		}
	}
	return files, nil
}

// shown is a file name as a bus line and the seen list show it: quoted when it holds a newline, a tab or another control
// character, so the line stays one line and the seen list's tabs and lines still split it.
func shown(f string) string {
	if strings.ContainsFunc(f, unicode.IsControl) {
		return strconv.Quote(f)
	}
	return f
}

// narrow keeps of the files two branches share only those that both change in commits of their own: commits the other
// does not hold, nor a copy of (a rebase or cherry-pick: same change, new id). Work both hold is one change, landed once,
// so it is no clash: one branch built on the other or both on a third, also after a fix on the lower branch, a merge of
// base into either, or a rebase of the lower one (live Quotes 11-20: PR 8371 is built on 8348, 29 shared files). Merge
// commits are left out: what they bring in is base's or another branch's, whose own commits still count (a conflict
// fixed inside a merge is not seen). It keys on history, never on names.
func (s *scan) narrow(ctx context.Context, p pair, a, b branch) (pair, error) {
	own := func(tip, other string) (map[string]bool, error) {
		out, err := s.git.out(ctx, 2*time.Minute, "log", "--raw", "-z", "--no-abbrev", "--no-renames", "--diff-merges=off",
			"--format=", "--cherry-pick", "--left-only", tip+"..."+other, "^"+s.base)
		if err != nil {
			return nil, fmt.Errorf("repo %s: %w", s.repo.Name, err)
		}
		keys := map[string]bool{}
		for f := range raw(out) {
			keys[board.Key(f)] = true
		}
		return keys, nil
	}
	ka, err := own(a.tip, b.tip)
	if err != nil {
		return p, err
	}
	kb, err := own(b.tip, a.tip)
	if err != nil {
		return p, err
	}
	p.files = nil
	for k, f := range a.files {
		if _, ok := b.files[k]; ok && ka[k] && kb[k] {
			p.files = append(p.files, f)
		}
	}
	slices.Sort(p.files)
	return p, nil
}

// raw reads `git diff --raw -z` or `git log --raw -z --format=` output into path -> the content it was given (a blob
// id; all zeros for a delete). In a log, a path changed twice gives both contents: the yield sees each.
func raw(out string) func(yield func(string, string) bool) {
	return func(yield func(string, string) bool) {
		parts := strings.Split(out, "\x00")
		for i := 0; i+1 < len(parts); i++ {
			meta := strings.TrimLeft(parts[i], "\n")
			if !strings.HasPrefix(meta, ":") {
				continue
			}
			f := strings.Fields(meta) // :oldmode newmode oldsha newsha status
			if len(f) < 5 {
				continue
			}
			i++
			if !yield(parts[i], f[3]) {
				return
			}
		}
	}
}

// glob matches a path to a pattern by path.Match per folder, where a ** part is any number of folders; no case.
func glob(pattern, name string) bool {
	return globParts(strings.Split(board.Key(pattern), "/"), strings.Split(board.Key(name), "/"))
}

func globParts(p, n []string) bool {
	if len(p) == 0 {
		return len(n) == 0
	}
	if p[0] == "**" {
		for i := 0; i <= len(n); i++ {
			if globParts(p[1:], n[i:]) {
				return true
			}
		}
		return false
	}
	if len(n) == 0 {
		return false
	}
	ok, _ := path.Match(p[0], n[0]) // patterns are checked in load
	return ok && globParts(p[1:], n[1:])
}

// pair is two branches of one repo that change the same files (display names, sorted).
type pair struct {
	repo, a, b, la, lb string // names (the seen key) and labels (the bus line)
	files              []string
}

func (p pair) key() string { return p.repo + "\t" + p.a + "\t" + p.b }

// shared is the files two branches both change; two refs at one commit are one piece of work, not a clash.
func shared(repo string, a, b branch) (pair, bool) {
	p := pair{repo: repo, a: a.name, b: b.name, la: a.label, lb: b.label}
	if a.tip != "" && a.tip == b.tip {
		return p, false
	}
	for k, f := range a.files {
		if _, ok := b.files[k]; ok {
			p.files = append(p.files, f)
		}
	}
	slices.Sort(p.files)
	return p, len(p.files) > 0
}

func (p pair) text(multi bool) string {
	head, more := p.files, ""
	if len(head) > 3 {
		head, more = head[:3], fmt.Sprintf(" (+%d more)", len(head)-3)
	}
	s := fmt.Sprintf("%s and %s both change %d files: %s%s — land one first, rebase the other", p.la, p.lb, len(p.files),
		strings.Join(head, ", "), more)
	if multi {
		s = p.repo + ": " + s
	}
	return s
}

// watch looks at every repo of a project and posts each new or grown clash once, and once more when it goes to someone
// new. A repo that cannot be looked at keeps its seen pairs and makes the run fail after the others are done; so does a
// line the bus refuses, which stays unseen and is tried again next run. list prints every clash, not only the summary.
func watch(home string, now time.Time, project string, out io.Writer, list bool) error {
	c, err := load(home, project)
	if err != nil {
		return err
	}
	repos := c.Repos
	unlock, err := lock(home, project)
	if err != nil {
		return err
	}
	defer unlock()
	seenPath := filepath.Join(home, "state", project, "overlap-seen.tsv")
	seen, err := readSeen(seenPath, project)
	if err != nil {
		return err
	}
	b := projectBus(home, project, c)
	own := &owners{home: home, project: project, prefixes: c.Owners}
	next := map[string]seenRow{}
	held := map[string]seenRow{} // new, grown or moved pairs past the burst limit, posted as one line
	var errs []error
	sent := 0
	down := false // the bus was not reached: the rest wait for the next run, unseen
	send := func(to, text string) bool {
		if down {
			return false
		}
		err := b.post("INFO", to, text)
		if pe := (postError{}); errors.As(err, &pe) && pe.down {
			down = true
		}
		if err != nil {
			errs = append(errs, err)
		}
		return err == nil
	}
	ctx := context.Background()
	for _, r := range repos {
		s, err := look(ctx, r, now)
		if err != nil {
			errs = append(errs, err)
			fmt.Fprintf(out, "%s: could not look: %v\n", r.Name, err)
			for k, v := range seen {
				if strings.HasPrefix(k, r.Name+"\t") {
					next[k] = v
				}
			}
			continue
		}
		for _, n := range s.notes {
			fmt.Fprintf(out, "%s: %s\n", r.Name, n)
		}
		var clashes, posted, stacks int
		for i, a := range s.open {
			for _, b2 := range s.open[i+1:] {
				x, y := a, b2
				if y.name < x.name {
					x, y = y, x
				}
				p, ok := shared(r.Name, x, y)
				if !ok {
					continue
				}
				old, had := seen[p.key()]
				if had {
					next[p.key()] = old
				}
				if p, err = s.narrow(ctx, p, x, y); err != nil {
					errs = append(errs, err)
					continue
				} else if len(p.files) == 0 {
					stacks++
					delete(next, p.key())
					continue
				}
				clashes++
				if list {
					fmt.Fprintf(out, "  %s\n", p.text(len(repos) > 1))
				}
				to, err := own.pair(x, y)
				if err != nil {
					errs = append(errs, err)
					continue
				}
				grown := slices.ContainsFunc(p.files, func(f string) bool { return !slices.Contains(old.files, f) })
				if !grown && old.to == to {
					continue // posted before to the same roles, and not grown since
				}
				union := slices.Clone(p.files)
				for _, f := range old.files {
					if !slices.Contains(union, f) {
						union = append(union, f)
					}
				}
				slices.Sort(union)
				if sent >= burst {
					held[p.key()] = seenRow{to: to, files: union}
					continue
				}
				if !send(to, p.text(len(repos) > 1)) {
					continue
				}
				sent++
				posted++
				next[p.key()] = seenRow{to: to, files: union}
			}
		}
		summary := "no clashes"
		if clashes > 0 {
			summary = fmt.Sprintf("%d clashing pairs, %d posted now", clashes, posted)
		}
		if stacks > 0 {
			summary += fmt.Sprintf("; %d stacked pairs skipped (their shared files are only in work both hold)", stacks)
		}
		fmt.Fprintf(out, "%s: fetched %s; %d %s on the remote besides %s; compared %d %s (changed files not in %s, tip within %d days): %s\n",
			r.Name, r.Remote, s.listed, s.unit(), r.Base, len(s.open), s.unit(), r.Base, r.MaxAge, summary)
	}
	if len(held) > 0 {
		var to []string
		for _, row := range held {
			for _, n := range strings.Split(row.to, ", ") {
				if !slices.Contains(to, n) {
					to = append(to, n)
				}
			}
		}
		slices.Sort(to)
		text := fmt.Sprintf("%d more clashing pairs: crius-overlap %s lists them", len(held), project)
		if send(strings.Join(to, ", "), text) {
			maps.Copy(next, held)
			fmt.Fprintln(out, "posted: "+text)
		}
	}
	if err := writeSeen(seenPath, next); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// owners finds the roles a branch belongs to: the longest owners prefix of its name (or of a branch at the PR's tip);
// else <role> when the name starts with <role>/ and <project>:<role> has a role file in <home>/roles or has posted on
// the project's bus (a team on another machine has no role files here; its bus copy names its roles); else crius:lead.
type owners struct {
	home, project string
	prefixes      map[string]string
	posters       map[string]bool // roles that posted on the project's bus, read once
}

func (o *owners) of(b branch) ([]string, error) {
	best := ""
	for _, h := range b.heads {
		for p := range o.prefixes {
			if strings.HasPrefix(h, p) && (len(p) > len(best) || len(p) == len(best) && p < best) {
				best = p
			}
		}
	}
	if best != "" {
		return strings.Split(o.prefixes[best], ", "), nil
	}
	for _, h := range b.heads {
		role, _, ok := strings.Cut(h, "/")
		if !ok {
			continue
		}
		if f, err := sessionstate.RolePath(filepath.Join(o.home, "roles"), o.project, role); err == nil {
			if _, err := os.Stat(f); err == nil {
				return []string{o.project + ":" + role}, nil
			}
		}
		if o.posters == nil {
			if err := o.readPosters(); err != nil {
				return nil, err
			}
		}
		if o.posters[o.project+":"+role] {
			return []string{o.project + ":" + role}, nil
		}
	}
	return []string{"crius:lead"}, nil
}

// pair is the roles a clash goes to: the first branch's owners, then the second's, each once.
func (o *owners) pair(a, b branch) (string, error) {
	var to []string
	for _, x := range []branch{a, b} {
		ns, err := o.of(x)
		if err != nil {
			return "", err
		}
		for _, n := range ns {
			if !slices.Contains(to, n) {
				to = append(to, n)
			}
		}
	}
	return strings.Join(to, ", "), nil
}

// readPosters reads who posted on the project's bus here ("<time> <TYPE> <project:role> -> …"); a project with no bus
// here has none.
func (o *owners) readPosters() error {
	raw, err := os.ReadFile(filepath.Join(o.home, "bus", o.project+".log"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("reading who posted on the %s bus: %w", o.project, err)
	}
	posters := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		if f := strings.Fields(line); len(f) > 3 && f[3] == "->" && strings.HasPrefix(f[2], o.project+":") {
			posters[f[2]] = true
		}
	}
	o.posters = posters
	return nil
}

// check compares one branch with all open work. It reads the local branch (not pushed yet) when that holds the remote
// one, else the remote one; it skips the branch itself and its own PR, also when the PR's merge ref is behind.
func check(home string, now time.Time, project, name string, out io.Writer) error {
	c, err := load(home, project)
	if err != nil {
		return err
	}
	repos := c.Repos
	unlock, err := lock(home, project) // its fetch must not meet the launchd run's in the same clone
	if err != nil {
		return err
	}
	defer unlock()
	ctx := context.Background()
	found, clashes := false, 0
	for _, r := range repos {
		s, err := look(ctx, r, now)
		if err != nil {
			return err
		}
		sha := ""
		local, lerr := s.git.out(ctx, time.Minute, "rev-parse", "--verify", "--quiet", "refs/heads/"+name+"^{commit}")
		remote, rerr := s.git.out(ctx, time.Minute, "rev-parse", "--verify", "--quiet", s.ref(name)+"^{commit}")
		local, remote = strings.TrimSpace(local), strings.TrimSpace(remote)
		switch {
		case lerr == nil && rerr == nil:
			sha = remote
			if in, err := s.git.ancestor(ctx, remote, local); err != nil {
				return err
			} else if in {
				sha = local
			}
		case lerr == nil:
			sha = local
		case rerr == nil:
			sha = remote
		default:
			continue
		}
		found = true
		files, err := s.files(ctx, sha)
		if err != nil {
			return fmt.Errorf("branch %s: %w", name, err)
		}
		me := branch{name: name, label: name, tip: sha, files: files}
		n := 0
		for _, o := range s.open {
			if o.name == name || slices.Contains(o.heads, name) {
				continue // itself, or its own PR
			}
			p, ok := shared(r.Name, me, o)
			if ok {
				if p, err = s.narrow(ctx, p, me, o); err != nil {
					return err
				} else if len(p.files) == 0 {
					continue // an older tip of this branch (a PR merge ref behind), work it is built on, or work built on it
				}
			}
			n++
			if ok {
				clashes++
				fmt.Fprintln(out, p.text(len(repos) > 1))
			}
		}
		for _, n := range s.notes {
			fmt.Fprintf(out, "%s: %s\n", r.Name, n)
		}
		fmt.Fprintf(out, "%s: %s changes %d files not in %s; compared with %d %s\n", r.Name, name, len(files), r.Base, n, s.unit())
	}
	if !found {
		return fmt.Errorf("branch %s is in no repo of %s (looked for a local branch and one on the remote)", name, project)
	}
	if clashes > 0 {
		fmt.Fprintf(out, "%d clashes: land one first, rebase the other\n", clashes)
		return errClash
	}
	fmt.Fprintln(out, "no clashes")
	return nil
}

// all watches every project that has an overlap.toml. Each reason a project cannot be looked at (one line per repo) or
// a line of it could not be posted is posted to Crius's bus once; the reasons are kept in overlap-fail.txt with numbers blanked, until a clean run. So a
// launchd run that fails is never silent, and one that keeps failing the same way says it once.
func all(home string, now time.Time, out io.Writer) error {
	files, err := filepath.Glob(filepath.Join(home, "state", "*", "overlap.toml"))
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no project has %s", filepath.Join(home, "state", "<project>", "overlap.toml"))
	}
	var errs []error
	for _, f := range files {
		project := filepath.Base(filepath.Dir(f))
		fmt.Fprintf(out, "== %s %s\n", project, now.Format("2006-01-02T15:04"))
		failPath := filepath.Join(filepath.Dir(f), "overlap-fail.txt")
		err := watch(home, now, project, out, false)
		if err == nil {
			if err := os.Remove(failPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, err)
			}
			continue
		}
		errs = append(errs, fmt.Errorf("%s: %w", project, err))
		old, _ := os.ReadFile(failPath)
		was := strings.Split(string(old), "\n")
		var keys []string
		crius := bus{cmd: filepath.Join(home, "bin", "bus-post"), log: filepath.Join(home, "bus", "crius.log")}
		for _, e := range leaves(err) {
			what := "could not look"
			if pe := (postError{}); errors.As(e, &pe) {
				what = "could not post"
			}
			line := strings.ReplaceAll(e.Error(), "\n", " ")
			key := failKey(line)
			if slices.Contains(keys, key) {
				continue
			}
			keys = append(keys, key)
			if slices.Contains(was, key) {
				continue
			}
			if err := crius.post("ALERT", "crius:lead", "crius-overlap "+project+" "+what+": "+line); err != nil {
				errs = append(errs, err)
				keys = keys[:len(keys)-1] // not said: say it next run
			}
		}
		if err := os.WriteFile(failPath, []byte(strings.Join(keys, "\n")), 0o644); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// varies is what changes from run to run in one failure reason: a GUID (az's Trace and Correlation IDs), a sha that
// moves with each tip, or a number.
var varies = regexp.MustCompile(`[0-9a-fA-F]{8}(-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}|\b[0-9a-f]{7,40}\b|[0-9]+`)

// failKey is a failure reason with what varies blanked, so one reason is said once.
func failKey(line string) string { return varies.ReplaceAllString(line, "N") }

// hexWord is a word bus-post would take for a commit id; the bus line names only branches and files, so none is one.
var hexWord = regexp.MustCompile(`^[0-9A-Fa-f]{7,40}$`)

// bus is where a project's lines go: a command with bus-post's arguments, and the log it is given.
type bus struct{ cmd, log string }

// projectBus is <home>/bin/bus-post on <home>/bus/<project>.log, or the config's bus_post and bus_log (a bus on another
// machine: the copy here refuses lines).
func projectBus(home, project string, c config) bus {
	if c.BusPost != "" {
		return bus{cmd: c.BusPost, log: c.BusLog}
	}
	return bus{cmd: filepath.Join(home, "bin", "bus-post"), log: filepath.Join(home, "bus", project+".log")}
}

// postError is a line the bus did not take: the run could look, and says so apart. down is a bus not reached at all
// (ssh's exit 255, the time limit, a command that does not start), as against one line refused.
type postError struct {
	err  error
	down bool
}

func (e postError) Error() string { return e.err.Error() }

// leaves gives every error errors.Join put together, one by one.
func leaves(err error) []error {
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		var all []error
		for _, e := range j.Unwrap() {
			all = append(all, leaves(e)...)
		}
		return all
	}
	return []error{err}
}

// post sends one line. Exit 5 and 8 of bus-post are posted, with a recipient or thread not reached. A command that hangs
// (ssh to a machine that sleeps) is stopped, with its whole process group, after two minutes.
func (b bus) post(typ, to, text string) error {
	var notSha []string
	for _, w := range strings.FieldsFunc(text, func(r rune) bool {
		return !(r == '_' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z')
	}) {
		if hexWord.MatchString(w) {
			notSha = append(notSha, w)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, b.cmd, b.log, typ, "crius:overlap", to, text)
	cmd.Env = append(os.Environ(), "SAY_NOT_SHA="+strings.Join(notSha, ","))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	got, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if errors.As(err, &ee) && (ee.ExitCode() == 5 || ee.ExitCode() == 8) {
		return nil
	}
	if err != nil {
		down := !errors.As(err, &ee) || ee.ExitCode() == 255 || ctx.Err() != nil
		return postError{fmt.Errorf("%s to %s: %v: %s", filepath.Base(b.cmd), filepath.Base(b.log), err,
			strings.Join(strings.Fields(string(got)), " ")), down}
	}
	return nil
}

// seenRow is a pair posted: the roles it went to and the files it shared.
type seenRow struct {
	to    string
	files []string
}

// seenHead starts a seen list that names the roles each pair went to. A list without it (86fc592) has rows of
// repo, a, b, files…, all posted to <project>:lead.
const seenHead = "# crius-overlap seen v2: repo, branch, branch, to, files"

func readSeen(file, project string) (map[string]seenRow, error) {
	seen := map[string]seenRow{}
	b, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return seen, nil
	}
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	v2 := lines[0] == seenHead
	for _, line := range lines {
		f := strings.Split(line, "\t")
		switch {
		case v2 && len(f) >= 5:
			seen[strings.Join(f[:3], "\t")] = seenRow{to: f[3], files: f[4:]}
		case !v2 && len(f) >= 4:
			seen[strings.Join(f[:3], "\t")] = seenRow{to: project + ":lead", files: f[3:]}
		}
	}
	return seen, nil
}

// writeSeen writes the seen pairs whole (a new file, then renamed over the old one): a run stopped half way leaves the
// old list, never half a list.
func writeSeen(file string, seen map[string]seenRow) error {
	var b strings.Builder
	b.WriteString(seenHead + "\n")
	for _, k := range slices.Sorted(maps.Keys(seen)) {
		b.WriteString(k + "\t" + seen[k].to + "\t" + strings.Join(seen[k].files, "\t") + "\n")
	}
	if err := os.WriteFile(file+".new", []byte(b.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(file+".new", file)
}

// lock holds a project's lock: one run at a time fetches into its clones and writes its seen list.
func lock(home, project string) (func(), error) {
	file := filepath.Join(home, "state", project, "overlap.lock")
	f, err := os.OpenFile(file, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("locking %s: %w", file, err)
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

// gitter runs git in one clone. A secret (the bearer token) is cut out of any error text.
type gitter struct {
	dir    string
	env    []string
	secret string
}

// out runs git with a time limit. At the limit the whole process group goes (git's https or ssh helper too) and the
// read stops: a helper left holding the pipe cannot hang the run (and so its lock) for hours. The error is one line.
func (g gitter) out(ctx context.Context, limit time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	base := []string{"-C", g.dir, "-c", "gc.auto=0", "-c", "maintenance.auto=false", "-c", "log.showSignature=false",
		"-c", "color.ui=false", "-c", "diff.relative=false"}
	cmd := exec.CommandContext(ctx, "git", append(base, args...)...)
	cmd.Env = g.env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	var stderr strings.Builder
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if err != nil {
		msg := strings.Join(strings.Fields(stderr.String()), " ")
		if g.secret != "" {
			msg = strings.ReplaceAll(msg, g.secret, "<token>")
		}
		if ctx.Err() != nil {
			msg += fmt.Sprintf(" (stopped after %s: git runs in its own process group, so an ssh remote that asks for a key passphrase or a host key cannot be answered here)", limit)
		}
		verb := args[0]
		for i := 0; i+2 < len(args) && args[i] == "-c"; i += 2 {
			verb = args[i+2]
		}
		return string(b), fmt.Errorf("git %s: %w: %s", verb, err, msg)
	}
	return string(b), nil
}

// ancestor says whether commit a is in the history of b (exit 1 is a plain no, any other failure an error).
func (g gitter) ancestor(ctx context.Context, a, b string) (bool, error) {
	_, err := g.out(ctx, time.Minute, "merge-base", "--is-ancestor", a, b)
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return false, nil
	}
	return err == nil, err
}

// azToken is an Azure DevOps bearer token from the Azure CLI's sign-in. It is never printed.
func azToken(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	var stderr strings.Builder
	cmd := exec.CommandContext(ctx, "az", "account", "get-access-token", "--resource", adoResource, "--query", "accessToken", "-o", "tsv")
	cmd.Stderr = &stderr
	cmd.WaitDelay = 5 * time.Second
	b, err := cmd.Output()
	tok := strings.TrimSpace(string(b))
	if err != nil || tok == "" {
		return "", fmt.Errorf("az account get-access-token gave no token: %v: %s", err, strings.Join(strings.Fields(stderr.String()), " "))
	}
	return tok, nil
}
