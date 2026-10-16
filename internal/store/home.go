package store

// The private home ~/.crius: layout and the git-leak test (design §3b).

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

var Subdirs = []string{"bus", "state", "spool", "backup", "logs"}

// leakGlobs are tracked paths in the crius checkout that would mean private state got committed.
// As in shell fnmatch, '*' also matches '/'.
var leakGlobs = []string{"*.db", "*.db-wal", "*.db-shm", "state/*", "*/state/*", "bus/*.log", "*/bus/*.log",
	"spool/*", "*/spool/*"}

var leakRes = func() []*regexp.Regexp {
	var res []*regexp.Regexp
	for _, g := range leakGlobs {
		res = append(res, regexp.MustCompile(`(?s)^`+strings.ReplaceAll(regexp.QuoteMeta(g), `\*`, `.*`)+`$`))
	}
	return res
}()

var decisionIDRe = regexp.MustCompile(`(?m)^## ([a-z0-9-]+-D\d+) `)

// Home is $CRIUS_HOME, or ~/.crius.
func Home() string {
	if h := os.Getenv("CRIUS_HOME"); h != "" {
		return h
	}
	u, err := os.UserHomeDir()
	if err != nil {
		panic(err)
	}
	return filepath.Join(u, ".crius")
}

// InitLayout creates the home and its folders (mode 700) and writes projects.toml (mode 600) if it is missing.
func InitLayout(home string, example []byte) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(home, 0o700); err != nil {
		return err
	}
	for _, sub := range Subdirs {
		p := filepath.Join(home, sub)
		if err := os.Mkdir(p, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
			return fmt.Errorf("%s exists and is not a folder", p)
		}
	}
	f, err := os.OpenFile(filepath.Join(home, "projects.toml"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := f.Write(example); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// git runs git in dir. err is set only when git could not run at all; a non-zero exit is in rc.
func git(dir string, args ...string) (stdout, stderr []byte, rc int, err error) {
	var out, errb bytes.Buffer
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return out.Bytes(), errb.Bytes(), exit.ExitCode(), nil
	}
	return out.Bytes(), errb.Bytes(), 0, err
}

// tracked lists `git ls-files -z` (no quoting of unusual names), or returns an error when git fails.
func tracked(repo string, args ...string) ([]string, error) {
	out, stderr, rc, err := git(repo, append([]string{"ls-files", "-z"}, args...)...)
	if err == nil && rc != 0 {
		err = fmt.Errorf("git ls-files exit %d: %s", rc, firstLine(stderr))
	}
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range bytes.Split(out, []byte{0}) {
		if len(p) > 0 {
			paths = append(paths, string(p))
		}
	}
	return paths, nil
}

func firstLine(b []byte) string {
	s, _, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
	return s
}

func resolve(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return p
}

// privateIDs are the decision and item ids that exist only in the private store.
func privateIDs(home string) ([]string, error) {
	ids := map[string]bool{}
	// ReadDir, not Glob: Glob treats the home path as a pattern and skips folders it cannot read.
	dirs, err := os.ReadDir(filepath.Join(home, "state"))
	if err != nil {
		return nil, err
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(home, "state", d.Name(), "DECISIONS.md"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, m := range decisionIDRe.FindAllSubmatch(b, -1) {
			ids[string(m[1])] = true
		}
	}
	dbf := filepath.Join(home, "state", "record.db")
	if _, err := os.Stat(dbf); err == nil {
		db, err := sql.Open("sqlite", sqliteURI(dbf, "mode=ro"))
		if err != nil {
			return nil, err
		}
		defer db.Close()
		var n int
		if err := db.QueryRow("select count(*) from sqlite_master where name='item'").Scan(&n); err != nil {
			return nil, err
		}
		if n > 0 {
			rows, err := db.Query("select id from item")
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					return nil, err
				}
				ids[id] = true
			}
			if err := rows.Err(); err != nil {
				return nil, err
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	var out []string
	for id := range ids {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

func isWord(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r)
}

// containsID finds id as a whole id: not after a word character, '#' or '-', and not before a word character
// or '#'. So demo-D3 does not match demo-D31 or xdemo-D3.
func containsID(text, id string) bool {
	for start := 0; ; {
		i := strings.Index(text[start:], id)
		if i < 0 {
			return false
		}
		i += start
		before, _ := utf8.DecodeLastRuneInString(text[:i])
		after, _ := utf8.DecodeRuneInString(text[i+len(id):])
		okBefore := i == 0 || !(isWord(before) || before == '#' || before == '-')
		okAfter := i+len(id) == len(text) || !(isWord(after) || after == '#')
		if okBefore && okAfter {
			return true
		}
		start = i + 1
	}
}

// LeakProblems lists every way private state could reach git. An empty list means the test passed.
func LeakProblems(home, repo string) []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	// 0. The layout is real folders: a symlink could point the private data anywhere.
	for _, p := range append([]string{home}, joinAll(home, Subdirs)...) {
		if fi, err := os.Lstat(p); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			add("%s is a symlink; private folders must be real folders", p)
		}
	}
	home, repo = resolve(home), resolve(repo)

	// 1. The home must not be committable: either outside every work tree, or ignored and untracked there.
	out, stderr, rc, err := git(home, "rev-parse", "--show-toplevel")
	switch {
	case err != nil:
		add("cannot run git: %v", err)
		return problems
	case rc == 0:
		top := strings.TrimSpace(string(out))
		rel, err := filepath.Rel(top, home)
		if err != nil {
			add("cannot place %s inside %s: %v", home, top, err)
			break
		}
		if _, _, rc, err := git(top, "check-ignore", "-q", "--no-index", rel+"/"); err != nil || rc != 0 {
			add("%s is inside the git work tree %s and not ignored there", home, top)
		}
		if t, err := tracked(top, "--", rel); err != nil {
			add("cannot list tracked files in %s: %v", top, err)
		} else if len(t) > 0 {
			add("%d file(s) under %s are tracked by %s, e.g. %s", len(t), home, top, t[0])
		}
	case !bytes.Contains(stderr, []byte("not a git repository")):
		add("cannot tell whether %s is in a git work tree: %s", home, firstLine(stderr))
	}

	// 2. No private-state file is tracked (or staged) in the crius checkout.
	paths, err := tracked(repo)
	if err != nil {
		add("cannot list tracked files in %s: %v", repo, err)
		return problems
	}
	for _, p := range paths {
		for _, re := range leakRes {
			if re.MatchString(p) {
				add("tracked private-state file in %s: %s", repo, p)
				break
			}
		}
	}

	// 3. No id that exists only in private files appears in a tracked file, in the work tree or the index.
	ids, err := privateIDs(home)
	if err != nil {
		add("cannot read the private ids in %s: %v", home, err)
		return problems
	}
	if len(ids) == 0 {
		return problems
	}
	var pattern []string
	for _, id := range ids {
		pattern = append(pattern, "-e", id)
	}
	seen := map[string]bool{}
	for _, where := range [][]string{nil, {"--cached"}} {
		args := append(append([]string{"grep", "-z", "-I", "-w", "-F"}, where...), pattern...)
		out, stderr, rc, err := git(repo, args...)
		if err != nil || (rc != 0 && rc != 1) {
			add("git grep failed in %s: %v %s", repo, err, firstLine(stderr))
			continue
		}
		// With -z (and no -n) each match is "path NUL line LF".
		for _, rec := range strings.Split(string(out), "\n") {
			path, text, _ := strings.Cut(rec, "\x00")
			var hits []string
			for _, id := range ids {
				if containsID(text, id) {
					hits = append(hits, id)
				}
			}
			if len(hits) == 0 {
				continue
			}
			msg := fmt.Sprintf("private id(s) %s appear in tracked file %s", strings.Join(hits, ", "), path)
			if where != nil {
				msg += " (staged)"
			}
			if !seen[msg] {
				seen[msg] = true
				problems = append(problems, msg)
			}
		}
	}
	return problems
}

func joinAll(dir string, names []string) []string {
	var out []string
	for _, n := range names {
		out = append(out, filepath.Join(dir, n))
	}
	return out
}
