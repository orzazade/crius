// Package needref reads in what a lead's GOAL "Needs owner" points to, so the owner queue shows the questions
// themselves (Crius 10-30 16:45: row q8 said only "the 3 questions in STATUS-2026-10-28.md section 6"): the section of
// a Markdown file it names, or a labelled paragraph of the lead's own status file.
package needref

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// MaxRunes caps each part read in: a card is no document (q8's three questions are 1,650).
const MaxRunes = 2000

// walkBudget caps the search for named files: Read runs under the owner queue's lock (review round 1 of f1c57f3).
var walkBudget = 2 * time.Second

// clock is the time the search reads (a test's own clock).
var clock = time.Now

var (
	// a Markdown file name or path: "STATUS-2026-10-28.md", "docs/build/ARCHITECTURE.md", "~/x/GAINS.md"
	fileRe = regexp.MustCompile(`(?:~/)?[\w./-]*[A-Za-z][\w.-]*\.md\b`)
	// a doc named without .md, read only when <NAME>.md is found: "PROD-HANDOVER 12.2-12.8", "BUILD-DECISIONS B4"
	bareRe = regexp.MustCompile(`\b[A-Z]{2,}(?:-[A-Z]{2,})+\b`)
	// what follows a file name: "section 6", "Section 4", "sections 2-3", "§5", "12.2-12.8", "B4"; not "2026-10-28"
	sectRe = regexp.MustCompile(`^\s*(?:(?i:sections?|§)\s*)?(\d{1,2}(?:\.\d{1,2})?|[A-Z]\d{1,3})(?:\s*[-–]\s*(\d{1,2}(?:\.\d{1,2})?))?\b`)
	// `see "Open with owner"`: a label in the lead's own status file
	labelRe   = regexp.MustCompile(`(?i)\bsee\s+"([^"]{3,60})"`)
	headingRe = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	numRe     = regexp.MustCompile(`^(\d{1,2}(?:\.\d{1,2})?)\b`)
	itemRe    = regexp.MustCompile(`^(\d+)[.)]\s+(~~)?`)
	boldRe    = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	// folders never searched for a named file
	skipDirs = map[string]bool{"node_modules": true, "vendor": true, "dist": true, "bin": true, "obj": true, "target": true, "coverage": true}
)

// Status is a lead's status file: Path as the card names it, Dir the folder searched first for a named file, and Text,
// when set, the text the GOAL block was read from (a git object), where labels are read; else the file at Path. Git,
// when set, is that object's tree: files the ask names are read from it too, never from a checkout of another branch
// (review round 2 of ee67b2e).
type Status struct {
	Path, Dir string
	Text      []byte
	Git       *Tree
}

// Tree is a git revision of a repo; Dir is the status file's folder in it ("." for the top).
type Tree struct{ Repo, Rev, Dir string }

// Read gives the text ask points to, with where it came from, or "" when ask names nothing to read. Files are looked
// for under the status file's folder, then dirs, the project's other folders; nothing outside them is read. A file ask
// names with ".md" that cannot be found or read is said in the text, never left out, and so is a name that more than
// one file under a folder has, and a search that ran out of time.
func Read(ask string, st Status, dirs []string) string {
	type ref struct {
		name, tail string
		bare       bool // named without ".md": read only when a file has that name
	}
	var want []ref
	var names []string
	seen := map[string]bool{}
	for _, loc := range refs(ask) {
		r := ref{name: ask[loc[0]:loc[1]], tail: ask[loc[1]:]}
		if r.bare = !strings.HasSuffix(r.name, ".md"); r.bare {
			r.name += ".md"
		}
		if key := r.name + "\x00" + sectionOf(r.tail); !seen[key] {
			seen[key] = true
			want = append(want, r)
			names = append(names, r.name)
		}
	}
	var found map[string]hit
	if st.Git != nil {
		found = st.Git.locate(names)
	} else {
		found = locate(names, rootsOf(append([]string{st.Dir}, dirs...)), clock().Add(walkBudget))
	}
	var parts, late []string
	for _, r := range want {
		h := found[r.name]
		if r.bare && errors.Is(h.err, errLate) {
			late = append(late, r.name) // most such words are no file: said once, after what was read
			continue
		}
		if h.err != nil {
			if !r.bare || !errors.Is(h.err, errNone) {
				parts = append(parts, fmt.Sprintf("Could not read %s: %v.", r.name, h.err))
			}
			continue
		}
		text, where, err := section(string(h.text), r.tail)
		if err != nil {
			parts = append(parts, fmt.Sprintf("Could not read%s in %s: %v.", where, h.path, err))
			continue
		}
		parts = append(parts, clip(text)+"\n(from "+h.path+where+h.note+")")
	}
	for _, m := range labelRe.FindAllStringSubmatch(ask, -1) {
		if text, err := labelled(st, m[1]); err != nil {
			parts = append(parts, fmt.Sprintf("Could not read %q in %s: %v.", m[1], st.Path, err))
		} else {
			parts = append(parts, clip(text)+"\n(from "+st.Path+")")
		}
	}
	if late != nil {
		parts = append(parts, fmt.Sprintf("Not looked for to the end (%v after %s): %s.", errLate, walkBudget, strings.Join(late, ", ")))
	}
	return strings.Join(parts, "\n\n")
}

// hit is a named file found and read, or why not.
type hit struct {
	path, note string // note: what the card must say about how it was found
	text       []byte
	err        error
}

// rootsOf gives the folders to search, cleaned, once each, and without one inside another already listed.
func rootsOf(dirs []string) []string {
	var out []string
	for _, d := range dirs {
		if d == "" {
			continue
		}
		d = filepath.Clean(d)
		if !slices.ContainsFunc(out, func(r string) bool { return within(d, r) }) {
			out = append(out, d)
		}
	}
	return out
}

// within says p is root or a path under it.
func within(p, root string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

// refs gives each file a text names: ".md" names, and doc names without it; a doc name inside a ".md" name is one ref.
// A name in a URL is no file here (review round 2 of ee67b2e: ".../blob/main/PROD-HANDOVER.md" read the local file).
func refs(ask string) [][]int {
	inURL := func(l []int) bool {
		a := strings.LastIndexAny(ask[:l[0]], " \t\n(<\"'`") + 1
		b := strings.IndexAny(ask[l[1]:], " \t\n)>\"'`")
		if b < 0 {
			b = len(ask) - l[1]
		}
		return strings.Contains(ask[a:l[1]+b], "//")
	}
	var locs [][]int
	for _, l := range fileRe.FindAllStringIndex(ask, -1) {
		if !inURL(l) {
			locs = append(locs, l)
		}
	}
	for _, b := range bareRe.FindAllStringIndex(ask, -1) {
		if !inURL(b) && !slices.ContainsFunc(locs, func(l []int) bool { return b[0] >= l[0] && b[1] <= l[1] }) {
			locs = append(locs, b)
		}
	}
	sort.Slice(locs, func(i, j int) bool { return locs[i][0] < locs[j][0] })
	return locs
}

func sectionOf(tail string) string {
	if m := sectRe.FindStringSubmatch(tail); m != nil {
		return m[1] + "-" + m[2]
	}
	return ""
}

var (
	// errNone is a name no file under the roots has: for a doc named without .md, just words.
	errNone = errors.New("no such file")
	// errLate is a search that ran out of its time.
	errLate = errors.New("the search stopped")
)

// locate finds and reads each name under roots, in one walk of each root for all names (review round 2 of ee67b2e: a
// walk per name let a word that is no file use up the time of the file after it). A path (from home, absolute, or
// under a root) is that file when it is under a root; a name is the one file of that name under the first root that
// has one. More than one there is an error: the newest may be another project's (review round 1 of f1c57f3:
// ~/Projects/Appxite holds 5 BUILD-DECISIONS.md). The file read must be under a root once links are followed.
func locate(names []string, roots []string, deadline time.Time) map[string]hit {
	out := map[string]hit{}
	var left []string
	for _, name := range names {
		if _, done := out[name]; done {
			continue
		}
		path, err := pathOf(name, roots)
		switch {
		case err != nil:
			out[name] = hit{err: err}
		case path != "":
			out[name] = read(path, roots)
		default:
			out[name] = hit{err: fmt.Errorf("%w under %s", errNone, strings.Join(roots, ", "))}
			left = append(left, name)
		}
	}
	for i, r := range roots {
		if len(left) == 0 {
			break
		}
		found := map[string][]string{}
		late := false
		_ = filepath.WalkDir(r, func(p string, d fs.DirEntry, err error) error {
			switch {
			case clock().After(deadline):
				late = true
				return filepath.SkipAll
			case err != nil:
				return nil // an unreadable folder is skipped; a file found elsewhere is still found
			case d.IsDir() && p != r && (strings.HasPrefix(d.Name(), ".") || skipDirs[d.Name()] || strings.Count(p[len(r):], "/") > 8 ||
				slices.Contains(roots[:i], p)): // an earlier root was searched already
				return filepath.SkipDir
			case !d.IsDir():
				for _, name := range left {
					if matches(p, name) {
						found[name] = append(found[name], p)
					}
				}
			}
			return nil
		})
		var still []string
		for _, name := range left {
			switch f := found[name]; {
			case len(f) == 1:
				out[name] = read(f[0], roots)
				if late { // review round 3 of baf3a3e: a second file of the name may lie past where the walk stopped
					h := out[name]
					h.note = fmt.Sprintf("; %v after %s, so another file of that name may be there", errLate, walkBudget)
					out[name] = h
				}
			case len(f) > 1:
				out[name] = hit{err: several(f, r, r)}
			case late:
				out[name] = hit{err: fmt.Errorf("%w after %s, under %s", errLate, walkBudget, r)}
			default:
				still = append(still, name)
			}
		}
		if left = still; late {
			break
		}
	}
	return out
}

// pathOf gives the file a name with a path names: "" with no error for a name to search for. A path from home,
// absolute or "../" must be under a root.
func pathOf(name string, roots []string) (string, error) {
	var path string
	switch {
	case len(roots) == 0:
		return "", errors.New("no project folder to look in")
	case strings.HasPrefix(name, "~/"):
		h, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(h, name[2:])
	case filepath.IsAbs(name):
		path = filepath.Clean(name)
	case strings.HasPrefix(name, "../"):
		path = filepath.Join(roots[0], name)
	case strings.Contains(name, "/"):
		for _, r := range roots {
			if p := filepath.Join(r, name); within(p, r) {
				if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
					return p, nil
				}
			}
		}
		return "", nil
	default:
		return "", nil
	}
	if !slices.ContainsFunc(roots, func(r string) bool { return within(path, r) }) {
		return "", fmt.Errorf("outside the project folders (%s)", strings.Join(roots, ", "))
	}
	if fi, err := os.Stat(path); err != nil || fi.IsDir() {
		return "", fmt.Errorf("%w: %s", errNone, path)
	}
	return path, nil
}

// matches says file p is the one name names: its base name, and its path's end when name has folders.
func matches(p, name string) bool {
	return filepath.Base(p) == filepath.Base(name) && (!strings.Contains(name, "/") || strings.HasSuffix(p, "/"+name))
}

// several is the error of a name more than one file under root has; at names root on the card.
func several(found []string, root, at string) error {
	sort.Strings(found)
	n := len(found)
	shown := make([]string, 0, 4)
	for _, f := range found[:min(n, 3)] {
		rel, _ := filepath.Rel(root, f)
		shown = append(shown, rel)
	}
	if n > 3 {
		shown = append(shown, "…")
	}
	return fmt.Errorf("%d files of that name under %s (%s): the ask must name its path", n, at, strings.Join(shown, ", "))
}

// read reads path when, links followed, it is still under a root (review round 2 of ee67b2e: a link inside a root read
// /etc/hosts).
func read(path string, roots []string) hit {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return hit{path: path, err: err}
	}
	if !slices.ContainsFunc(roots, func(r string) bool {
		rr, err := filepath.EvalSymlinks(r)
		return err == nil && within(real, rr)
	}) {
		return hit{path: path, err: fmt.Errorf("%s leads outside the project folders, to %s", path, real)}
	}
	b, err := os.ReadFile(real)
	return hit{path: path, text: b, err: err}
}

// locate finds and reads each name in the tree: a path from the status file's folder or from the top, else the one
// file of that name under the status file's folder, else under the top.
func (t *Tree) locate(names []string) map[string]hit {
	out := map[string]hit{}
	if len(names) == 0 {
		return out
	}
	// -z: unquoted paths ("café/NOTE.md" is quoted without it); --full-tree: paths from the top, as cat-file reads them
	files, err := t.git("ls-tree", "-r", "-z", "--full-tree", "--name-only", "--end-of-options", t.Rev)
	if err != nil {
		for _, n := range names {
			out[n] = hit{err: err}
		}
		return out
	}
	all := strings.Split(strings.TrimSuffix(string(files), "\x00"), "\x00")
	where := t.Repo + " " + t.Rev + ":"
	at := t.Rev + " of " + t.Repo
	for _, name := range names {
		if strings.HasPrefix(name, "~/") || strings.HasPrefix(name, "../") || filepath.IsAbs(name) {
			out[name] = hit{err: fmt.Errorf("outside %s", at)}
			continue
		}
		var found []string
		for _, top := range slices.Compact([]string{t.Dir, "."}) {
			if p := path.Join(top, name); slices.Contains(all, p) {
				found = []string{p}
				break
			}
			for _, f := range all {
				if (top == "." || strings.HasPrefix(f, top+"/")) && matches("/"+f, name) {
					found = append(found, f)
				}
			}
			if len(found) > 0 {
				break
			}
		}
		switch len(found) {
		case 0:
			out[name] = hit{err: fmt.Errorf("%w in %s", errNone, at)}
		case 1:
			b, err := t.git("cat-file", "blob", "--end-of-options", t.Rev+":"+found[0])
			out[name] = hit{path: where + found[0], text: b, err: err}
		default:
			for i := range found {
				found[i] = "/" + found[i]
			}
			out[name] = hit{err: several(found, "/", at)}
		}
	}
	return out
}

func (t *Tree) git(args ...string) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("git", append([]string{"-C", t.Repo}, args...)...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		why, _, _ := strings.Cut(strings.TrimSpace(stderr.String()), "\n")
		return nil, fmt.Errorf("git %s in %s: %v: %s", args[0], t.Repo, err, why)
	}
	return stdout.Bytes(), nil
}

// section reads the part of text that tail names: "section 6" or "6" is the heading that starts with 6; "12.2" is that
// heading, else item 2 of section 12; "12.2-12.8" the headings 12.2 to 12.8, else items 2 to 8 of section 12; "2-3"
// the headings 2 to 3; "B4" the heading that starts with B4. With no section it is the whole text. where says which
// part, as " section 6".
func section(text, tail string) (body, where string, err error) {
	lines := strings.Split(text, "\n")
	m := sectRe.FindStringSubmatch(tail)
	if m == nil {
		return strings.TrimSpace(text), "", nil
	}
	from, to := m[1], m[2]
	where = " section " + from
	if to != "" {
		where += "-" + to
	}
	hs := headsOf(lines)
	if h, ok := hs.find(from); ok {
		last := h
		if to != "" {
			last = hs.through(h, to)
			if m := numRe.FindStringSubmatch(last.text); m == nil || m[1] != to {
				where += fmt.Sprintf(" (no heading %s: read to %q)", to, last.text)
			}
		}
		return strings.TrimSpace(strings.Join(lines[h.at:hs.end(last, len(lines))], "\n")), where, nil
	}
	top, first, ok := strings.Cut(from, ".")
	if !ok {
		return "", where, errors.New("no heading starts with " + from)
	}
	last := first
	if to != "" {
		toTop, toItem, ok := strings.Cut(to, ".")
		if !ok || toTop != top {
			return "", where, fmt.Errorf("%s-%s is no range of items in one section", from, to)
		}
		last = toItem
	}
	h, ok := hs.find(top)
	if !ok {
		return "", where, errors.New("no heading starts with " + top)
	}
	items, err := listItems(lines[h.at+1:hs.end(h, len(lines))], first, last)
	return items, where, err
}

// heading is a Markdown heading outside code fences: its line, its level and its text ("§" and "Section " dropped).
type heading struct {
	at, level int
	text      string
}

type heads []heading

func headsOf(lines []string) heads {
	var hs heads
	fence := false
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			fence = !fence
		}
		if h := headingRe.FindStringSubmatch(l); h != nil && !fence {
			hs = append(hs, heading{i, len(h[1]), strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(h[2], "§"), "Section "))})
		}
	}
	return hs
}

// find gives the shallowest heading whose text starts with key ("6. Questions", "B4 — Owner", "§5 Fields"), the first
// of them (review round 1 of f1c57f3: "### 1. sub step" came before "## 1. Real").
func (hs heads) find(key string) (heading, bool) {
	best := heading{at: -1}
	for _, h := range hs {
		if strings.HasPrefix(h.text, key) && !startsWord(h.text[len(key):]) && (best.at < 0 || h.level < best.level) {
			best = h
		}
	}
	return best, best.at >= 0
}

// through gives the last heading of h's level, after h and before the next higher one, whose number comes at or
// before to: the end of the range "12.2-12.8" or "2-3".
func (hs heads) through(h heading, to string) heading {
	last := h
	for _, o := range hs {
		switch {
		case o.at <= h.at || o.level > h.level:
		case o.level < h.level:
			return last
		default:
			if m := numRe.FindStringSubmatch(o.text); m == nil || !numLE(m[1], to) {
				return last
			}
			last = o
		}
	}
	return last
}

// end gives the line where the part under h ends: the next heading of its level or higher.
func (hs heads) end(h heading, n int) int {
	for _, o := range hs {
		if o.at > h.at && o.level <= h.level {
			return o.at
		}
	}
	return n
}

// numLE says section number a ("12.3") comes at or before b ("12.8").
func numLE(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		x, _ := strconv.Atoi(as[i])
		y, _ := strconv.Atoi(bs[i])
		if x != y {
			return x < y
		}
	}
	return len(as) <= len(bs)
}

// startsWord says rest goes on with key's own token: "6" in "6.1", "B4" in "B40", "12" in "12.2".
func startsWord(rest string) bool {
	if rest == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(rest)
	if r == '.' && len(rest) > 1 && rest[1] >= '0' && rest[1] <= '9' {
		return true
	}
	return r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z'
}

// listItems gives the numbered items first to last of a section's own list (the lines before its first sub-heading),
// each with its own lines; an item struck through ("4. ~~Shadow first~~ — decided") is no question and is left out.
func listItems(lines []string, first, last string) (string, error) {
	lo, err1 := strconv.Atoi(first)
	hi, err2 := strconv.Atoi(last)
	if err1 != nil || err2 != nil || hi < lo {
		return "", fmt.Errorf("items %s to %s", first, last)
	}
	var out []string
	in, fence := false, false
	for _, l := range lines {
		mark := strings.HasPrefix(strings.TrimSpace(l), "```")
		if mark {
			fence = !fence
		}
		if fence || mark { // a code block is part of its item (review round 2 of ee67b2e)
		} else if headingRe.MatchString(l) {
			break // a sub-section's list is another one
		} else if m := itemRe.FindStringSubmatch(l); m != nil {
			n, _ := strconv.Atoi(m[1])
			in = n >= lo && n <= hi && m[2] == ""
		} else if l != "" && !strings.HasPrefix(l, " ") && !strings.HasPrefix(l, "\t") {
			in = false // a line back at the margin ends the item
		}
		if in {
			out = append(out, l)
		}
	}
	if len(out) == 0 {
		return "", fmt.Errorf("no open items %s to %s", first, last)
	}
	return strings.TrimSpace(strings.Join(out, "\n")), nil
}

// labelled gives the part of the status text that opens with label: a heading and what is under it, or a line
// "label …" and the lines after it up to a blank line or a heading, a list under it included (review round 1 of
// f1c57f3: a stop at a list item or a "Yes: …" line lost the questions).
func labelled(st Status, label string) (string, error) {
	b := st.Text
	if b == nil {
		var err error
		if b, err = os.ReadFile(st.Path); err != nil {
			return "", err
		}
	}
	lines := strings.Split(string(b), "\n")
	hs := headsOf(lines)
	if h, ok := hs.find(label); ok {
		return strings.TrimSpace(strings.Join(lines[h.at:hs.end(h, len(lines))], "\n")), nil
	}
	for i, l := range lines {
		if !strings.HasPrefix(strings.TrimLeft(l, "-* "), label) {
			continue
		}
		end := i + 1
		for end < len(lines) && strings.TrimSpace(lines[end]) != "" && !headingRe.MatchString(lines[end]) {
			end++
		}
		return strings.TrimSpace(strings.Join(lines[i:end], "\n")), nil
	}
	return "", errors.New("no heading or line opens with it")
}

// clip takes the Markdown bold marks out and keeps at most MaxRunes runes, cut at a word, saying where the rest is.
func clip(s string) string {
	s = boldRe.ReplaceAllString(s, "$1")
	if utf8.RuneCountInString(s) <= MaxRunes {
		return s
	}
	cut := string([]rune(s)[:MaxRunes+1])
	if i := strings.LastIndexAny(cut, " \n"); i > 0 {
		cut = cut[:i]
	}
	return strings.TrimSpace(cut) + " … (cut: the rest is in the file)"
}
