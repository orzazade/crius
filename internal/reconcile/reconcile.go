// Package reconcile compares a team's board with git: a row whose work is already on the integration branch but whose
// state is not done (stale), and a row whose state is done but whose sha or PR is not on the branch (unlanded).
//
// It reads git only through commands that never write: rev-parse, merge-base --is-ancestor and log, run with
// GIT_OPTIONAL_LOCKS=0. It never fetches or checks out, so it judges the branch as the local repo has it.
//
// Landed evidence for a row, any of:
//   - sha: a sha the row names is an ancestor of the branch tip (git merge-base --is-ancestor);
//   - merge-subject: a commit in the last Depth commits of the branch has a merge subject naming a PR the row names
//     ("Merged PR 12: …" (Azure DevOps), "Merge pull request #12 …" (GitHub merge), "… (#12)" (GitHub squash));
//   - row-id-subject: a commit in the last Depth commits names the row id as a whole word in its subject
//     ("fix(A12b): …"; a word joined by "-" is another word, so "P3-6" does not name P3).
package reconcile

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// Row is one board row: its id, its state as the board writes it, its line in the file, and the shas and PR numbers
// it names.
type Row struct {
	ID, State string
	Line      int
	SHAs, PRs []string
}

// Repo is a git repo and the branch rows land on.
type Repo struct{ Path, Branch string }

// Config names the board's states (compared without case): Done rows must be landed, Partial rows may have landed
// parts (noted, not judged), Skip rows (dropped) are not judged; every other state is open and must not be landed.
// Depth is how many commits of each branch the subject searches read.
type Config struct {
	Done, Partial, Skip []string
	Depth               int
}

// Evidence is one proof that a row's work is on a branch: Kind is sha, merge-subject or row-id-subject.
type Evidence struct{ Kind, Ref, Commit, Subject, Repo, Branch string }

// Finding is a row whose state and git disagree: Kind "stale" (open but landed, with Evidence) or "unlanded" (done
// but nothing found, with what was looked for in Misses).
type Finding struct {
	Row      Row
	Kind     string
	Evidence []Evidence
	Misses   []string
}

// Scan is what was read of one branch: its tip and how many commits (back to Oldest, a date) the subject searches saw.
type Scan struct {
	Repo, Branch, Tip, Oldest string
	Commits                   int
}

// Report is the result: counts per kind of row, the findings, partial rows with landed work, and done rows that name
// no sha or PR and whose id is in no scanned subject (Unchecked: nothing proves them either way).
type Report struct {
	Rows, Done, Open, Partial, Skipped int
	Findings                           []Finding
	PartialLanded, Unchecked           []string
	Scans                              []Scan
}

var (
	wordRe = regexp.MustCompile(`[A-Za-z0-9_]+(?:-[A-Za-z0-9_]+)*`)
	hexRe  = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)
	// prRe finds PR numbers in board text: "PR 12", "PR12", "PR #12", "!12", "#12" (not "&#12", not "x#12").
	prRe     = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_&])(?:PR\s*#?\s*|!|#)(\d+)\b`)
	mergeRes = []*regexp.Regexp{
		regexp.MustCompile(`^Merged PR (\d+):`),
		regexp.MustCompile(`^Merge pull request #(\d+)\b`),
		regexp.MustCompile(`\(#(\d+)\)\s*$`),
	}
)

// shas are the words of s that look like commit shas: 7 to 40 hex digits with at least one letter and one digit (so
// plain numbers and words like "decade" are not shas; an all-digit sha is missed). A word joined by "-" such as a
// gate fingerprint "fp-0a1b2c3d4e" is not a sha.
func shas(s string) []string {
	var out []string
	for _, w := range wordRe.FindAllString(s, -1) {
		if hexRe.MatchString(w) && strings.ContainsAny(w, "0123456789") && strings.ContainsAny(strings.ToLower(w), "abcdef") {
			out = appendNew(out, w)
		}
	}
	return out
}

func prs(s string) []string {
	var out []string
	for _, m := range prRe.FindAllStringSubmatch(s, -1) {
		out = appendNew(out, m[1])
	}
	return out
}

func appendNew(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}

// isID says whether w can be a row id: a word that starts with a letter and holds a digit (docs/RULES.md §3.1).
func isID(w string) bool {
	return w != "" && wordRe.FindString(w) == w && strings.ContainsAny(w, "0123456789") &&
		strings.ContainsAny(strings.ToLower(w[:1]), "abcdefghijklmnopqrstuvwxyz")
}

// ParseTSV reads a board with a header row. Columns are found by name (any order, no case): the row id ("row" or
// "id") and the state ("state" or "status") are required; shas come from the "sha" or "commit" column, PR numbers
// from every cell. Blank lines and lines with an empty id are left out.
func ParseTSV(r io.Reader) ([]Row, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	col := map[string]int{"row": -1, "state": -1, "sha": -1}
	names := map[string]string{"row": "row", "id": "row", "state": "state", "status": "state", "sha": "sha", "commit": "sha"}
	var rows []Row
	n := 0
	for sc.Scan() {
		n++
		cells := strings.Split(strings.TrimRight(sc.Text(), "\r"), "\t")
		if n == 1 {
			cells[0] = strings.TrimPrefix(cells[0], string(rune(0xFEFF)))
			for i, c := range cells {
				if k, ok := names[strings.ToLower(strings.TrimSpace(c))]; ok && col[k] < 0 {
					col[k] = i
				}
			}
			for _, k := range []string{"row", "state"} {
				if col[k] < 0 {
					return nil, fmt.Errorf("tsv header has no %s column (want one named %s): %q", k, map[string]string{"row": "row or id", "state": "state or status"}[k], sc.Text())
				}
			}
			continue
		}
		cell := func(k string) string {
			if i := col[k]; i >= 0 && i < len(cells) {
				return strings.TrimSpace(cells[i])
			}
			return ""
		}
		if cell("row") == "" {
			continue
		}
		row := Row{ID: cell("row"), State: cell("state"), Line: n, SHAs: shas(cell("sha")), PRs: prs(strings.Join(cells, "\t"))}
		rows = append(rows, row)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, errors.New("tsv board is empty (no header row)")
	}
	return rows, nil
}

var (
	headingRe = regexp.MustCompile(`^(#+)\s+(.*)$`)
	bulletRe  = regexp.MustCompile(`^\s*(?:[-*+]|\d+\.)\s+(\S+)\s+(\S+)`)
)

// ParseMarkdown reads the rows under the heading that contains section (any level, up to the next heading of that
// level or higher; "" reads the whole file). A row is a bullet "- <mark> <id> …" whose mark is a symbol (no letter or
// digit, e.g. ✅ ⬜ 🔶) and whose id is a word with a letter and a digit, or a table line "| <id> | … |" whose state is
// the first later cell that is a symbol, else the second cell. Shas and PR numbers come from the whole line.
func ParseMarkdown(r io.Reader, section string) ([]Row, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	in, found, level := section == "", section == "", 0
	var rows []Row
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimRight(sc.Text(), "\r")
		if m := headingRe.FindStringSubmatch(line); m != nil {
			switch {
			case section != "" && strings.Contains(m[2], section) && !found:
				in, found, level = true, true, len(m[1])
			case in && section != "" && len(m[1]) <= level:
				in = false
			}
			continue
		}
		if !in {
			continue
		}
		var id, state string
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "|") {
			cells := strings.Split(strings.Trim(t, "|"), "|")
			for i := range cells {
				cells[i] = strings.TrimSpace(cells[i])
			}
			if len(cells) < 2 {
				continue
			}
			first := wordRe.FindString(cells[0])
			if !isID(first) || first != strings.Trim(cells[0], "*`") {
				continue
			}
			id, state = first, strings.Fields(cells[1] + " ")[0]
			for _, c := range cells[1:] {
				if isMark(c) {
					state = c
					break
				}
			}
		} else if m := bulletRe.FindStringSubmatch(line); m != nil && isMark(m[1]) {
			w := strings.TrimRight(m[2], ":,;.)")
			if !isID(w) {
				continue
			}
			id, state = w, m[1]
		} else {
			continue
		}
		var s []string
		for _, x := range shas(line) {
			if x != id {
				s = append(s, x)
			}
		}
		rows = append(rows, Row{ID: id, State: state, Line: n, SHAs: s, PRs: prs(line)})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("no section heading contains %q", section)
	}
	return rows, nil
}

// isMark says whether s is a status symbol: not empty, no letter or digit (✅, ⬜, 🔶, ✖, …).
func isMark(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < 128 && (c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') || c == '|' || c == '-' {
			return false
		}
	}
	return true
}

// norm compares states without case and without emoji variation selectors.
func norm(s string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), "️", ""))
}

func in(list []string, s string) bool {
	for _, x := range list {
		if norm(x) == norm(s) {
			return true
		}
	}
	return false
}

// branch is one repo's branch as read: its tip, and the PR numbers and row-id words its scanned subjects name.
type branch struct {
	repo     Repo
	name     string
	tip      string
	pr, word map[string]commitRef
	commits  int
	oldest   string
}

type commitRef struct{ sha, subject string }

// Check judges each row against every repo's branch. It fails (never a clean report) on 0 rows, a folder that is no
// repo, a branch that does not resolve, or any git error.
func Check(rows []Row, repos []Repo, cfg Config) (Report, error) {
	var rep Report
	if len(rows) == 0 {
		return rep, errors.New("the board parsed to 0 rows: nothing was checked")
	}
	if len(repos) == 0 {
		return rep, errors.New("no repo to check against")
	}
	if cfg.Depth <= 0 {
		return rep, fmt.Errorf("depth %d: must be at least 1", cfg.Depth)
	}
	var bs []*branch
	for _, r := range repos {
		b, err := readBranch(r, cfg.Depth)
		if err != nil {
			return rep, err
		}
		bs = append(bs, b)
		rep.Scans = append(rep.Scans, Scan{Repo: r.Path, Branch: r.Branch, Tip: b.tip, Oldest: b.oldest, Commits: b.commits})
	}
	rep.Rows = len(rows)
	for _, row := range rows {
		kind := "open"
		switch {
		case in(cfg.Skip, row.State):
			rep.Skipped++
			continue
		case in(cfg.Done, row.State):
			kind = "done"
			rep.Done++
		case in(cfg.Partial, row.State):
			kind = "partial"
			rep.Partial++
		default:
			rep.Open++
		}
		ev, misses, err := evidence(row, bs)
		if err != nil {
			return rep, fmt.Errorf("row %s (line %d): %w", row.ID, row.Line, err)
		}
		switch {
		case kind == "open" && len(ev) > 0:
			rep.Findings = append(rep.Findings, Finding{Row: row, Kind: "stale", Evidence: ev})
		case kind == "partial" && len(ev) > 0:
			rep.PartialLanded = append(rep.PartialLanded, row.ID)
		case kind == "done" && len(ev) == 0 && len(row.SHAs)+len(row.PRs) == 0:
			rep.Unchecked = append(rep.Unchecked, row.ID)
		case kind == "done" && len(ev) == 0:
			rep.Findings = append(rep.Findings, Finding{Row: row, Kind: "unlanded", Misses: misses})
		}
	}
	return rep, nil
}

// evidence lists every proof that row landed, and for each sha and PR it names that landed nowhere, why.
func evidence(row Row, bs []*branch) (ev []Evidence, misses []string, err error) {
	for _, s := range row.SHAs {
		var why []string
		hit := false
		for _, b := range bs {
			full, ok, err := resolve(b.repo.Path, s)
			if err != nil {
				return nil, nil, err
			}
			if !ok {
				why = append(why, "not found in "+label(b))
				continue
			}
			anc, err := ancestor(b.repo.Path, full, b.tip)
			if err != nil {
				return nil, nil, err
			}
			if anc {
				ev = append(ev, Evidence{Kind: "sha", Ref: s, Commit: short(full), Repo: b.repo.Path, Branch: b.name})
				hit = true
				break
			}
			why = append(why, "not on "+label(b))
		}
		if !hit {
			misses = append(misses, "sha "+s+": "+strings.Join(why, ", "))
		}
	}
	for _, p := range row.PRs {
		hit := false
		for _, b := range bs {
			if c, ok := b.pr[p]; ok {
				ev = append(ev, Evidence{Kind: "merge-subject", Ref: "PR " + p, Commit: short(c.sha), Subject: c.subject, Repo: b.repo.Path, Branch: b.name})
				hit = true
				break
			}
		}
		if !hit {
			misses = append(misses, fmt.Sprintf("PR %s: no merge subject in the scanned commits", p))
		}
	}
	for _, b := range bs {
		if c, ok := b.word[strings.ToLower(row.ID)]; ok {
			ev = append(ev, Evidence{Kind: "row-id-subject", Ref: row.ID, Commit: short(c.sha), Subject: c.subject, Repo: b.repo.Path, Branch: b.name})
			break
		}
	}
	return ev, misses, nil
}

func label(b *branch) string { return b.name + " (" + filepath.Base(b.repo.Path) + ")" }

func short(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}

// gitRun runs a read-only git command in repo and returns stdout and the exit code; a git that cannot start is an
// error. GIT_OPTIONAL_LOCKS=0 keeps git from refreshing the index as a side effect.
func gitRun(repo string, args ...string) (string, int, error) {
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return out.String(), ee.ExitCode(), fmt.Errorf("git %s in %s: exit %d: %s", strings.Join(args, " "), repo, ee.ExitCode(), strings.TrimSpace(errb.String()))
	}
	if err != nil {
		return "", -1, fmt.Errorf("git %s in %s: %w", strings.Join(args, " "), repo, err)
	}
	return out.String(), 0, nil
}

// resolve finds rev as a commit in repo: ok false when it is unknown (or an ambiguous short sha), an error for any
// other git failure.
func resolve(repo, rev string) (string, bool, error) {
	out, code, err := gitRun(repo, "rev-parse", "--verify", "-q", rev+"^{commit}")
	switch code {
	case 0:
		return strings.TrimSpace(out), true, nil
	case 1:
		return "", false, nil
	}
	return "", false, err
}

func ancestor(repo, sha, tip string) (bool, error) {
	_, code, err := gitRun(repo, "merge-base", "--is-ancestor", sha, tip)
	switch code {
	case 0:
		return true, nil
	case 1:
		return false, nil
	}
	return false, err
}

func readBranch(r Repo, depth int) (*branch, error) {
	if _, code, err := gitRun(r.Path, "rev-parse", "--git-dir"); code != 0 {
		return nil, fmt.Errorf("%s is not a git repo: %w", r.Path, err)
	}
	tip, ok, err := resolve(r.Path, r.Branch)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("branch %q not found in %s", r.Branch, r.Path)
	}
	out, _, err := gitRun(r.Path, "log", fmt.Sprintf("-n%d", depth), "--format=%H%x1f%cs%x1f%s", tip)
	if err != nil {
		return nil, err
	}
	b := &branch{repo: r, name: r.Branch, tip: tip, pr: map[string]commitRef{}, word: map[string]commitRef{}}
	for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		f := strings.SplitN(l, "\x1f", 3)
		if len(f) != 3 {
			continue
		}
		b.commits++
		b.oldest = f[1]
		c := commitRef{sha: f[0], subject: f[2]}
		for _, re := range mergeRes {
			if m := re.FindStringSubmatch(c.subject); m != nil {
				if _, seen := b.pr[m[1]]; !seen {
					b.pr[m[1]] = c
				}
			}
		}
		for _, w := range wordRe.FindAllString(c.subject, -1) {
			if k := strings.ToLower(w); isID(w) {
				if _, seen := b.word[k]; !seen {
					b.word[k] = c
				}
			}
		}
	}
	if b.commits == 0 {
		return nil, fmt.Errorf("git log of %s in %s read no commits", r.Branch, r.Path)
	}
	return b, nil
}
