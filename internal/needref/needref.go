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
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// MaxRunes caps each part read in: a card is no document (q8's three questions are 1,650).
const MaxRunes = 2000

var (
	// a Markdown file name or path: "STATUS-2026-10-28.md", "docs/build/ARCHITECTURE.md", "~/x/GAINS.md"
	fileRe = regexp.MustCompile(`(?:~/)?[\w./-]*[A-Za-z][\w.-]*\.md\b`)
	// a doc named without .md, read only when <NAME>.md is found: "PROD-HANDOVER 12.2-12.8", "BUILD-DECISIONS B4"
	bareRe = regexp.MustCompile(`\b[A-Z]{2,}(?:-[A-Z]{2,})+\b`)
	// what follows a file name: "section 6", "§5", "12.2-12.8", "B4"
	sectRe = regexp.MustCompile(`^[\s,]*(?:(?:section|§)\s*)?(\d+(?:\.\d+)?|[A-Z]\d+)(?:\s*[-–]\s*(\d+(?:\.\d+)?))?\b`)
	// `see "Open with owner"`: a label in the lead's own status file
	labelRe   = regexp.MustCompile(`(?i)\bsee\s+"([^"]{3,60})"`)
	headingRe = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	itemRe    = regexp.MustCompile(`^(\d+)[.)]\s+(~~)?`)
	// a line at the margin that opens its own labelled paragraph: "Quota: …", "Mirror re-fetch (if …): …"
	labelLineRe = regexp.MustCompile(`^[A-Z][^:]{0,60}:\s`)
	boldRe      = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	// folders never searched for a named file
	skipDirs = map[string]bool{"node_modules": true, "vendor": true, "dist": true, "bin": true, "obj": true, "target": true, "coverage": true}
)

// Read gives the text ask points to, with where it came from, or "" when ask names nothing to read. statusFile is the
// lead's status file (labels are read there, and its folder is searched first); dirs are the project's other folders.
// A file ask names with ".md" that cannot be found or read is said in the text, never left out.
func Read(ask, statusFile string, dirs []string) string {
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
		path, note, err := find(name, statusFile, dirs)
		if err != nil {
			if explicit {
				parts = append(parts, fmt.Sprintf("Could not read %s: %v.", name, err))
			}
			continue
		}
		text, where, err := section(path, tail)
		if err != nil {
			if where == "" {
				parts = append(parts, fmt.Sprintf("Could not read %s: %v.", path, err))
			} else {
				parts = append(parts, fmt.Sprintf("Could not read%s in %s: %v.", where, path, err))
			}
			continue
		}
		parts = append(parts, clip(text)+"\n(from "+path+where+note+")")
	}
	for _, m := range labelRe.FindAllStringSubmatch(ask, -1) {
		if text, err := labelled(statusFile, m[1]); err != nil {
			parts = append(parts, fmt.Sprintf("Could not read %q in %s: %v.", m[1], statusFile, err))
		} else {
			parts = append(parts, clip(text)+"\n(from "+statusFile+")")
		}
	}
	return strings.Join(parts, "\n\n")
}

// refs gives each file a text names: ".md" names, and doc names without it; a doc name inside a ".md" name is one ref.
func refs(ask string) [][]int {
	locs := fileRe.FindAllStringIndex(ask, -1)
	for _, b := range bareRe.FindAllStringIndex(ask, -1) {
		inside := false
		for _, l := range locs {
			inside = inside || b[0] >= l[0] && b[1] <= l[1]
		}
		if !inside {
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

// find gives the file name names: a path from home, a path under the status file's folder or one of dirs, or a file of
// that name anywhere under them (the status file's folder first; of several, the newest, said in note).
func find(name, statusFile string, dirs []string) (path, note string, err error) {
	if rest, ok := strings.CutPrefix(name, "~/"); ok {
		h, err := os.UserHomeDir()
		if err != nil {
			return "", "", err
		}
		return file(filepath.Join(h, rest))
	}
	roots := append([]string{filepath.Dir(statusFile)}, dirs...)
	if filepath.IsAbs(name) {
		return file(name)
	}
	if strings.Contains(name, "/") {
		for _, r := range roots {
			if p, _, err := file(filepath.Join(r, name)); err == nil {
				return p, "", nil
			}
		}
	}
	base := filepath.Base(name)
	for _, r := range roots {
		var found []string
		_ = filepath.WalkDir(r, func(p string, d fs.DirEntry, err error) error {
			switch {
			case err != nil:
				return nil // an unreadable folder is skipped; a file found elsewhere is still found
			case d.IsDir() && p != r && (strings.HasPrefix(d.Name(), ".") || skipDirs[d.Name()] || strings.Count(p[len(r):], "/") > 8):
				return filepath.SkipDir
			case !d.IsDir() && d.Name() == base && (!strings.Contains(name, "/") || strings.HasSuffix(p, "/"+name)):
				found = append(found, p)
			}
			return nil
		})
		if len(found) == 0 {
			continue
		}
		sort.Slice(found, func(i, j int) bool { return mtime(found[i]) > mtime(found[j]) })
		if len(found) > 1 {
			note = fmt.Sprintf("; the newest of %d files named %s", len(found), base)
		}
		return found[0], note, nil
	}
	return "", "", fmt.Errorf("no such file under %s", strings.Join(roots, ", "))
}

func file(p string) (string, string, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return "", "", err
	}
	if fi.IsDir() {
		return "", "", errors.New("a folder, not a file")
	}
	return p, "", nil
}

func mtime(p string) int64 {
	fi, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return fi.ModTime().UnixNano()
}

// section reads the part of path that tail names: "section 6" or "6" is the heading that starts with 6; "12.2" is that
// heading, else item 2 of section 12; "12.2-12.8" items 2 to 8; "B4" the heading that starts with B4. With no section
// it is the whole file. where says which part, as " section 6".
func section(path, tail string) (text, where string, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	lines := strings.Split(string(b), "\n")
	m := sectRe.FindStringSubmatch(tail)
	if m == nil {
		return strings.TrimSpace(string(b)), "", nil
	}
	from, to := m[1], m[2]
	where = " section " + from
	if to != "" {
		where += "-" + to
	}
	if body, ok := heading(lines, from); ok && to == "" {
		return body, where, nil
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
	body, ok := heading(lines, top)
	if !ok {
		return "", where, errors.New("no heading starts with " + top)
	}
	items, err := listItems(body, first, last)
	return items, where, err
}

// heading gives the lines under the heading whose text starts with key ("6. Questions", "B4 — Owner", "§5 Fields"),
// up to the next heading of its level or higher, with the heading itself.
func heading(lines []string, key string) (string, bool) {
	start, level := -1, 0
	fence := false
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			fence = !fence
		}
		h := headingRe.FindStringSubmatch(l)
		if fence || h == nil {
			continue
		}
		if start >= 0 && len(h[1]) <= level {
			return strings.TrimSpace(strings.Join(lines[start:i], "\n")), true
		}
		t := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(h[2], "§"), "Section "))
		if start < 0 && strings.HasPrefix(t, key) && !startsWord(t[len(key):]) {
			start, level = i, len(h[1])
		}
	}
	if start < 0 {
		return "", false
	}
	return strings.TrimSpace(strings.Join(lines[start:], "\n")), true
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

// listItems gives the numbered items first to last of a section's top-level list, each with its own lines; an item
// struck through ("4. ~~Shadow first~~ — decided") is no question and is left out.
func listItems(body, first, last string) (string, error) {
	lo, err1 := strconv.Atoi(first)
	hi, err2 := strconv.Atoi(last)
	if err1 != nil || err2 != nil || hi < lo {
		return "", fmt.Errorf("items %s to %s", first, last)
	}
	var out []string
	in := false
	for _, l := range strings.Split(body, "\n") {
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

// labelled gives the paragraph of the status file that opens with label (a heading, or a line "label …: …"); it ends
// at a blank line, a heading, a list item or the next labelled line.
func labelled(statusFile, label string) (string, error) {
	b, err := os.ReadFile(statusFile)
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(b), "\n")
	if body, ok := heading(lines, label); ok {
		return body, nil
	}
	for i, l := range lines {
		if !strings.HasPrefix(strings.TrimLeft(l, "-* "), label) {
			continue
		}
		end := i + 1
		for end < len(lines) && strings.TrimSpace(lines[end]) != "" && !strings.HasPrefix(lines[end], "#") &&
			!strings.HasPrefix(lines[end], "- ") && !labelLineRe.MatchString(lines[end]) {
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
