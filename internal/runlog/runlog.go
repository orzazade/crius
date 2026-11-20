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
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
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

	path, err = place(dir, name, b.String(), func(tmp string) error {
		got, gotPass, err := Read(tmp)
		if err != nil {
			return fmt.Errorf("the log it wrote does not read back: %v", err)
		}
		if got != sha || gotPass != n { // n >= 1, so this is also Proven's pass check
			return fmt.Errorf("the log it wrote reads back as sha %s pass %d, not sha %s pass %d", got, gotPass, sha, n)
		}
		return nil
	})
	if err != nil {
		return "", 0, err
	}
	return path, n, nil
}

// place writes content into dir as <name>.md. It goes to a temp file first, which check reads back with the reader
// bus-post uses, then it is hard-linked into place: a reader never sees half a header, and the link fails when the
// name is taken (a file, a folder or a link, even a dangling one).
func place(dir, name, content string, check func(tmp string) error) (string, error) {
	path := filepath.Join(dir, name+".md")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".crius-runlog-*.tmp")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	_, werr := tmp.WriteString(content)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return "", werr
	}
	if err := check(tmp.Name()); err != nil {
		return "", err
	}
	if err := os.Link(tmp.Name(), path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return "", fmt.Errorf("%s is there already; a run log is never replaced: pick another name (%w)", path, fs.ErrExist)
		}
		return "", fmt.Errorf("cannot link the log into place (the runs folder needs hard links): %w", err)
	}
	return path, nil
}

// How much of a check's output its log keeps: the first headKeep and last tailKeep lines, each cut to lineKeep bytes.
const (
	headKeep = 40
	tailKeep = 160
	lineKeep = 2000
)

// Check runs one check command (argv, no shell: pass `sh -c '…'` for pipes) with no input, and writes its log into
// dir as <project>-<ref>-<local time>.md: `sha: <sha>`, then `pass: 1` only when the command exits 0 (else
// `fail: 1`, which no VERIFIED line can name), `cmd:`, `exit:`, and the cut output, each line behind "| " so no
// output line can read as a header line. echo, when not nil, gets the output as it comes. A name taken already
// (two checks of one ref in one second) gets -2, -3, … so a check that ran always has its log. It gives the log's path
// and the command's exit code; err is set only when no log was written (bad values, or the command did not start).
// Before it places the log it reads it back with bus-post's reader: a passing log must give the sha and pass 1, a
// failing one must be refused by Proven. 11-20/11-21: Crius checked cortex 1e4d6df and 71eb7a0 by real commands and
// posted no VERIFIED, as the log had to be written by hand.
func Check(dir, project, ref, sha string, argv []string, echo io.Writer) (path string, exit int, err error) {
	for _, v := range []struct{ what, s string }{{"project", project}, {"ref", ref}} {
		if !nameRe.MatchString(v.s) {
			return "", 0, fmt.Errorf("%s %q is not part of a file name: letters, digits, '.', '_' and '-', starting with a letter or digit", v.what, v.s)
		}
	}
	if !headSha.MatchString("sha: "+sha) || strings.TrimSpace(sha) != sha {
		return "", 0, fmt.Errorf("sha %q is not 7 to 40 lower-case hex characters", sha)
	}
	if len(argv) == 0 || argv[0] == "" {
		return "", 0, errors.New("no check command given")
	}
	cmdText := OneLine(strings.Join(argv, " "))

	var out bytes.Buffer
	c := exec.Command(argv[0], argv[1:]...)
	c.Stdout, c.Stderr = &out, &out
	if echo != nil {
		c.Stdout = io.MultiWriter(&out, keepGoing{echo}) // a closed echo (piped into head) never cuts the log's output
		c.Stderr = c.Stdout
	}
	c.WaitDelay = 10 * time.Second // a child left running with the output open (`sleep 1000 &`) does not hold the log
	start := time.Now()
	if err := c.Run(); c.ProcessState == nil {
		return "", 0, fmt.Errorf("the check did not start: %v", err)
	}
	if exit = c.ProcessState.ExitCode(); exit == 0 && !c.ProcessState.Success() {
		exit = -1 // never a pass when it did not exit 0 by itself
	}

	var b strings.Builder
	if exit == 0 {
		b.WriteString(Header(sha, 1))
	} else {
		fmt.Fprintf(&b, "sha: %s\nfail: 1\n", sha)
	}
	fmt.Fprintf(&b, "cmd: %s\nexit: %d\nran: %s for %s\noutput:\n", cmdText, exit,
		start.Format(time.RFC3339), time.Since(start).Round(time.Millisecond))
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if out.Len() == 0 {
		lines = nil
	}
	if len(lines) > headKeep+tailKeep {
		for _, l := range lines[:headKeep] {
			b.WriteString(quote(l))
		}
		fmt.Fprintf(&b, "… %d lines cut …\n", len(lines)-headKeep-tailKeep)
		lines = lines[len(lines)-tailKeep:]
	}
	for _, l := range lines {
		b.WriteString(quote(l))
	}

	check := func(tmp string) error {
		if exit != 0 {
			if _, err := Proven(tmp, tmp); err == nil {
				return errors.New("the failed check's log reads back as passing")
			}
			return nil
		}
		got, pass, err := Read(tmp)
		if err != nil {
			return fmt.Errorf("the log it wrote does not read back: %v", err)
		}
		if got != sha || pass != 1 {
			return fmt.Errorf("the log it wrote reads back as sha %s pass %d, not sha %s pass 1", got, pass, sha)
		}
		return nil
	}
	// The check has run, so its log must land: a name taken (two checks in one second) gets -2, -3, … after it.
	name := project + "-" + ref + "-" + start.Format("2006-01-02-150405")
	path, err = place(dir, name, b.String(), check)
	for i := 2; errors.Is(err, fs.ErrExist) && i <= 99; i++ {
		path, err = place(dir, fmt.Sprintf("%s-%d", name, i), b.String(), check)
	}
	if err != nil {
		return "", 0, err
	}
	return path, exit, nil
}

// quote keeps one output line behind "| ", cut to lineKeep bytes, so it can never start a header or shot line.
func quote(l string) string {
	if len(l) > lineKeep {
		l = strings.ToValidUTF8(l[:lineKeep], "") + " …"
	}
	return "| " + l + "\n"
}

// keepGoing writes to w and drops its errors, so the copy into the log's buffer goes on when the echo is closed.
type keepGoing struct{ w io.Writer }

func (k keepGoing) Write(p []byte) (int, error) { k.w.Write(p); return len(p), nil }
