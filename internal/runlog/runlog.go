// Package runlog is the one home of a run log's header: the file under ~/.crius/runs a VERIFIED bus line names.
// bus-post reads it with Read and Proven; ci-proof and crius-runlog write it with Header and Write, so the form a
// writer makes and the form bus-post takes cannot drift apart. On 11-19 four hand-written run logs were refused for
// header form (`pass: 7397 of 7397 green`, "header has no pass count"): nothing wrote a hand check's log for Crius.
//
// The header is the first HeaderLines lines. It holds `sha: <7-40 lower-case hex>` and either `pass: <N>` (a test run,
// N >= 1) or `walk: <N>` with N `shot: <path>` lines anywhere in the file (a live walk, each shot an existing,
// non-empty file).
package runlog

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"

	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// HeaderLines is how many lines from the top Read takes as the header.
const HeaderLines = 20

var (
	headSha  = regexp.MustCompile(`^sha:\s*([0-9a-f]{7,40})\s*$`)
	headRun  = regexp.MustCompile(`^pass:\s*(\d+)\s*$`)
	headWalk = regexp.MustCompile(`^walk:\s*(\d+)\s*$`) // a live walk: N screenshots, each a `shot: <path>` line
	shotRe   = regexp.MustCompile(`^shot:\s*(\S.*?)\s*$`)
	nameRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`) // one file name, no folder part
)

// Header gives the first two lines of a test run's log: the `sha:` and `pass:` lines Read takes.
func Header(sha string, pass int) string { return fmt.Sprintf("sha: %s\npass: %d\n", sha, pass) }

// Read reads the header of the run log at path: its sha, and its pass count (a live walk's shot count).
func Read(path string) (sha string, pass int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024) // a run log may hold long lines of test output
	walk, hasPass := 0, false
	var shots []string
	for i := 0; sc.Scan(); i++ {
		if m := shotRe.FindStringSubmatch(sc.Text()); m != nil {
			shots = append(shots, m[1]) // a walk lists every shot, past the header too
		}
		if i >= HeaderLines {
			continue
		}
		if m := headSha.FindStringSubmatch(sc.Text()); m != nil {
			sha = m[1]
		}
		if m := headRun.FindStringSubmatch(sc.Text()); m != nil {
			pass, _ = strconv.Atoi(m[1])
			hasPass = true
		}
		if m := headWalk.FindStringSubmatch(sc.Text()); m != nil {
			walk, _ = strconv.Atoi(m[1])
		}
	}
	if err := sc.Err(); err != nil {
		return "", 0, fmt.Errorf("%s: %w", path, err)
	}
	if sha == "" {
		return "", 0, fmt.Errorf("%s: header has no `sha: <sha>` line", path)
	}
	if walk < 1 || hasPass { // a test run's log is proven by its pass count alone, never by shots added to it
		return sha, pass, nil
	}
	// a live walk: N distinct screenshots, each a regular, non-empty file named by an absolute or ~/ path (crius:lead 11-10)
	seen := map[string]bool{}
	for _, s := range shots {
		if strings.HasPrefix(s, "~/") {
			home, _ := os.UserHomeDir()
			s = filepath.Join(home, s[2:])
		}
		if !filepath.IsAbs(s) {
			return "", 0, fmt.Errorf("%s: shot %s is not an absolute or ~/ path", path, s)
		}
		real, err := filepath.EvalSymlinks(s)
		if err != nil {
			return "", 0, fmt.Errorf("%s: shot %s does not exist", path, s)
		}
		st, err := os.Stat(real)
		switch {
		case err != nil:
			return "", 0, fmt.Errorf("%s: shot %s: %w", path, s, err)
		case !st.Mode().IsRegular() || real == path:
			return "", 0, fmt.Errorf("%s: shot %s is not a file", path, s)
		case st.Size() == 0:
			return "", 0, fmt.Errorf("%s: shot %s is empty", path, s)
		}
		seen[real] = true
	}
	if len(seen) != walk {
		return "", 0, fmt.Errorf("%s: walk: %d but %d shot file(s)", path, walk, len(seen))
	}
	return sha, walk, nil
}

// Proven reads the run log at path and passes it only with a pass count of 1 or more; name is the run log as the
// caller named it, for the error.
func Proven(name, path string) (string, error) {
	sha, pass, err := Read(path)
	if err != nil {
		return "", err
	}
	if pass < 1 {
		return "", errors.New(name + ": header has no pass count of 1 or more (`pass: <N>`)")
	}
	return sha, nil
}

// OneLine keeps a value on its own line: control characters and line breaks become spaces, so a value can never start
// a `pass:` or `sha:` line of its own.
func OneLine(s string) string {
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '\u2028' || r == '\u2029' || r == '\u0085' {
			return ' '
		}
		return r
	}, s)), " ")
}

// Write writes a test run's log for a hand check into dir as <name>.md: the `sha:` and `pass:` lines, `cmd: <cmd>`,
// then one line per note. pass is the text as typed and must be a bare whole number of 1 or more. It never replaces a
// file that is there, and it takes the log only after Read (the reader bus-post uses) gives back the sha and pass it
// was given, so a log it writes is one bus-post takes. It gives the log's path and the pass count.
func Write(dir, name, sha, pass, cmd string, notes []string) (path string, n int, err error) {
	name = strings.TrimSuffix(name, ".md")
	if !nameRe.MatchString(name) {
		return "", 0, fmt.Errorf("name %q is not a file name: letters, digits, '.', '_' and '-', starting with a letter or digit", name)
	}
	if !headSha.MatchString("sha: "+sha) || strings.TrimSpace(sha) != sha {
		return "", 0, fmt.Errorf("sha %q is not 7 to 40 lower-case hex characters", sha)
	}
	m := headRun.FindStringSubmatch("pass: " + pass)
	if m == nil || strings.TrimSpace(pass) != pass {
		return "", 0, fmt.Errorf("pass %q is not a bare whole number: write the count alone (7397, not \"7397 of 7397\"); "+
			"put words in the cmd or a note", pass)
	}
	n, err = strconv.Atoi(m[1])
	if err != nil {
		return "", 0, fmt.Errorf("pass %q: %v", pass, err)
	}
	if n < 1 {
		return "", 0, fmt.Errorf("pass %d proves nothing: bus-post takes a run log only with a pass count of 1 or more", n)
	}
	if cmd = OneLine(cmd); cmd == "" {
		return "", 0, errors.New("cmd is empty: say what was run or checked")
	}
	var b strings.Builder
	b.WriteString(Header(sha, n))
	b.WriteString("cmd: " + cmd + "\n")
	for i, note := range notes {
		note = OneLine(note)
		if headSha.MatchString(note) || headRun.MatchString(note) || headWalk.MatchString(note) || shotRe.MatchString(note) {
			return "", 0, fmt.Errorf("note %d %q reads as a header line; reword it", i+1, note)
		}
		b.WriteString(note + "\n")
	}

	path = filepath.Join(dir, name+".md")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", 0, err
	}
	// A temp file, read back with the reader bus-post uses, then hard-linked into place: a reader never sees half a
	// header, and the link fails when the name is taken (a file, a folder or a link, even a dangling one).
	tmp, err := os.CreateTemp(dir, ".crius-runlog-*.tmp")
	if err != nil {
		return "", 0, err
	}
	defer os.Remove(tmp.Name())
	_, werr := tmp.WriteString(b.String())
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return "", 0, werr
	}
	got, gotPass, err := Read(tmp.Name())
	if err != nil {
		return "", 0, fmt.Errorf("the log it wrote does not read back: %v", err)
	}
	if got != sha || gotPass != n { // n >= 1, so this is also Proven's pass check
		return "", 0, fmt.Errorf("the log it wrote reads back as sha %s pass %d, not sha %s pass %d", got, gotPass, sha, n)
	}
	if err := os.Link(tmp.Name(), path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return "", 0, fmt.Errorf("%s is there already; a run log is never replaced: pick another name", path)
		}
		return "", 0, fmt.Errorf("cannot link the log into place (the runs folder needs hard links): %w", err)
	}
	return path, n, nil
}
