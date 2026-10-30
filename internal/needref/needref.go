// Package needref reads in what a lead's GOAL "Needs owner" points to, so the owner queue shows the questions
// themselves (Crius 10-30 16:45: row q8 said only "the 3 questions in STATUS-2026-10-28.md section 6"): the section of
// a Markdown file it names, or a labelled paragraph of the lead's own status file.
package needref

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
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

// walkBudget caps the search for a named file: Read runs under the owner queue's lock (review round 1 of f1c57f3).
var walkBudget = 2 * time.Second

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
// when set, the text the GOAL block was read from (a git object), where labels are read; else the file at Path.
type Status struct {
	Path, Dir string
	Text      []byte
}

// Read gives the text ask points to, with where it came from, or "" when ask names nothing to read. Files are looked
// for under the status file's folder, then dirs, the project's other folders; nothing outside them is read. A file ask
// names with ".md" that cannot be found or read is said in the text, never left out, and so is a name that more than
// one file under a folder has.
func Read(ask string, st Status, dirs []string) string {
	roots := rootsOf(append([]string{st.Dir}, dirs...))
	deadline := time.Now().Add(walkBudget)
	var parts []string
	seen := map[string]bool{}
	for _, loc := range refs(ask) {
		name, tail := ask[loc[0]:loc[1]], ask[loc[1]:]
		explicit := strings.HasSuffix(name, ".md")
		if !explicit {
			name += ".md"
		}
		key := name + "\x00" + sectionOf(tail)
		if seen[key] {
			continue
		}
		seen[key] = true
		path, err := find(name, roots, deadline)
		if err != nil {
			if explicit || !errors.Is(err, errNone) {
				parts = append(parts, fmt.Sprintf("Could not read %s: %v.", name, err))
			}
			continue
		}
		b, err := os.ReadFile(path)
		if err != nil {
			parts = append(parts, fmt.Sprintf("Could not read %s: %v.", path, err))
			continue
		}
		text, where, err := section(string(b), tail)
		if err != nil {
			parts = append(parts, fmt.Sprintf("Could not read%s in %s: %v.", where, path, err))
			continue
		}
		parts = append(parts, clip(text)+"\n(from "+path+where+")")
	}
	for _, m := range labelRe.FindAllStringSubmatch(ask, -1) {
		if text, err := labelled(st, m[1]); err != nil {
			parts = append(parts, fmt.Sprintf("Could not read %q in %s: %v.", m[1], st.Path, err))
		} else {
			parts = append(parts, clip(text)+"\n(from "+st.Path+")")
		}
	}
	return strings.Join(parts, "\n\n")
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
// A name in a URL is no file here.
func refs(ask string) [][]int {
	var locs [][]int
	for _, l := range fileRe.FindAllStringIndex(ask, -1) {
		if !strings.Contains(ask[l[0]:l[1]], "//") {
			locs = append(locs, l)
		}
	}
	for _, b := range bareRe.FindAllStringIndex(ask, -1) {
		if !slices.ContainsFunc(locs, func(l []int) bool { return b[0] >= l[0] && b[1] <= l[1] }) {
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

// errNone is a name no file under the roots has: for a doc named without .md, just words.
var errNone = errors.New("no such file")

// find gives the file name names under roots: a path (from home, absolute, or under a root) when it is under a root,
// else the one file of that name under the first root that has one. More than one there is an error: the newest may
// be another project's (review round 1 of f1c57f3: ~/Projects/Appxite holds 5 BUILD-DECISIONS.md).
func find(name string, roots []string, deadline time.Time) (string, error) {
	var path string
	switch {
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
	}
	if path != "" {
		if !slices.ContainsFunc(roots, func(r string) bool { return within(path, r) }) {
			return "", fmt.Errorf("outside the project folders (%s)", strings.Join(roots, ", "))
		}
		if fi, err := os.Stat(path); err != nil || fi.IsDir() {
			return "", fmt.Errorf("%w: %s", errNone, path)
		}
		return path, nil
	}
	if strings.Contains(name, "/") {
		for _, r := range roots {
			if p := filepath.Join(r, name); within(p, r) {
				if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
					return p, nil
				}
			}
		}
	}
	base := filepath.Base(name)
	for i, r := range roots {
		var found []string
		var late bool
		_ = filepath.WalkDir(r, func(p string, d fs.DirEntry, err error) error {
			switch {
			case time.Now().After(deadline):
				late = true
				return filepath.SkipAll
			case err != nil:
				return nil // an unreadable folder is skipped; a file found elsewhere is still found
			case d.IsDir() && p != r && (strings.HasPrefix(d.Name(), ".") || skipDirs[d.Name()] || strings.Count(p[len(r):], "/") > 8 ||
				slices.Contains(roots[:i], p)): // an earlier root was searched already
				return filepath.SkipDir
			case !d.IsDir() && d.Name() == base && (!strings.Contains(name, "/") || strings.HasSuffix(p, "/"+name)):
				found = append(found, p)
			}
			return nil
		})
		switch {
		case late:
			return "", fmt.Errorf("the search stopped after %s, under %s", walkBudget, r)
		case len(found) == 1:
			return found[0], nil
		case len(found) > 1:
			sort.Strings(found)
			n := len(found)
			for j := range found {
				found[j], _ = filepath.Rel(r, found[j])
			}
			if n > 3 {
				found = append(found[:3], "…")
			}
			return "", fmt.Errorf("%d files of that name under %s (%s): the ask must name its path", n, r, strings.Join(found, ", "))
		}
	}
	return "", fmt.Errorf("%w under %s", errNone, strings.Join(roots, ", "))
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
	in := false
	for _, l := range lines {
		if headingRe.MatchString(l) {
			break // a sub-section's list is another one
		}
		if m := itemRe.FindStringSubmatch(l); m != nil {
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
