// Package reconcile compares a team's board with git: a row whose work is already on the integration branch but whose
// state is not done (stale), and a row whose state is done but whose own sha or PR is not on the branch (unlanded).
//
// It reads git only through commands that never write: rev-parse, cat-file, merge-base --is-ancestor and log, run
// with GIT_OPTIONAL_LOCKS=0. It never fetches or checks out, so it judges the branch as the local repo has it.
//
// A sha in a row is looked up as an object id only (rev-parse --disambiguate), never as a branch or tag name.
//
// A row's shas and PRs are its own when they come from a TSV's sha and pr columns, and cited when they come from
// markdown text, where a row may also name a baseline, the old tip of "A -> B", or another row's PR.
//
// Landed evidence for a row, any of:
//   - sha: a sha of the row is an ancestor of the branch tip (git merge-base --is-ancestor). The left of "A -> B" is
//     never the row's own. For an open row a cited sha counts only when its commit subject names the row (the subject
//     rule below), arrow or not;
//   - merge-subject: a commit in the last Depth commits of the branch has a merge subject naming a PR of the row
//     ("Merged PR 12: …" (Azure DevOps), "Merge pull request #12 …" (GitHub merge), "… (#12)" (GitHub squash)). PR
//     numbers repeat across repos, so a PR merged in more than one repo is ambiguous and proves nothing; for an open
//     row, a PR merged in one of several repos, or a cited PR, counts only when the merge subject also names the row
//     (otherwise it is a note: "not tied to this row");
//   - row-id-subject: a commit in the last Depth commits names the row in its subject (the subject rule).
//
// The subject rule: a subject names a row only as a tag, with ids not split at dots or dashes (C1.M2 is not C1, P3-6
// is not P3): as a conventional-commit scope ("fix(R1): …", "feat(R1, R2): …", "fix(R1/web): …"), as "ID:" at the
// start of the subject after an optional type prefix ("R1: …", "fix(web): R1: …"), or in a parenthesised list at the
// very end ("… (R1)", "… (R1, R2)"). Matching is case-sensitive, except that a scope may differ in case when the id
// has 3+ characters with a letter and a digit ("fix(x12b): …" names X12b; "feat(s3): …" does not name S3). A row id
// in running text ("fix(review): P1 null check", "unblocks X9") does not count. Reverts: a subject starting with
// "revert" (any case) names no row; for each row id the newest commit that tags it decides, and a revert tags the ids
// of the subject it quotes (Revert "fix(R1): …") or, with no quote, its own tags (revert(R1): …), so reverted work no
// longer counts until a later commit tags the row again.
//
// Verdicts: an open row with any evidence is stale. A done row is unlanded when an own sha or PR is missing, or, for
// cited ones, when the right of an "A -> B" is missing or none of them landed (rows cite history: commits from before
// a rebase, red tests on rig branches); it is ambiguous when what is missing is only a PR merged in several repos. A
// subject naming a done row never clears it; a done row that names no sha or PR is unchecked. Partial rows with
// evidence are noted, skipped rows are not judged, and a row in a state no list names is reported as unknown. A board
// where no row is judged is an error, never a clean result.
//
// Known gaps:
//   - a component-like scope (ec2, e2e, oauth2, api2) names a row of that name (3+ characters, letter and digit);
//   - any commit type counts as landing: a "test(R1): red" commit makes an open R1 stale;
//   - "Merged PR 5: R6: …" (Azure DevOps) does not name R6: only the merge's own PR number is read;
//   - a TSV sha cell has no arrow rule: every sha in it is the row's own;
//   - an all-digit sha in board text is not read as a sha.
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

// Row is one board row: its id, its state as the board writes it, its line in the file, its shas (the left of
// "A -> B" left out), the subset that is the right of "A -> B", whether its shas and PRs are cited in free text
// (markdown) rather than given in sha and pr columns, and its PR numbers.
type Row struct {
	ID, State string
	Line      int
	SHAs      []string
	Arrow     []string
	Cited     bool
	PRs       []string
}

// NotRead is a line in the board's section that looks like a row (it starts with a status mark, a task box or a
// pipe) but gives no row the parser can judge.
type NotRead struct {
	Line int
	Text string
}

// Repo is a git repo and the branch rows land on.
type Repo struct{ Path, Branch string }

// Config names the board's states (compared without case): Done rows must be landed, Partial rows may have landed
// parts (noted, not judged), Skip rows (dropped) are not judged, Open rows must not be landed; a row in none of them
// is unknown (reported, not judged). Depth is how many commits of each branch the subject searches read.
type Config struct {
	Done, Partial, Skip, Open []string
	Depth                     int
}

// Evidence is one proof that a row's work is on a branch: Kind is sha, merge-subject or row-id-subject.
type Evidence struct{ Kind, Ref, Commit, Subject, Repo, Branch string }

// Finding is a row whose state and git disagree: Kind "stale" (open but landed, with Evidence) or "unlanded" (done
// but an own sha or PR missing, named in Misses).
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

// Report is the result: counts per kind of row, the findings, partial rows with landed work, done rows that name no
// own sha or PR (Unchecked), rows in an unknown state ("ID (state)"), and Notes (a PR merged in several repos).
type Report struct {
	Rows, Done, Open, Partial, Skipped int
	Findings                           []Finding
	PartialLanded, Unchecked           []string
	Unknown, Notes                     []string
	Scans                              []Scan
}

var (
	// shaWordRe splits text into words for the sha search: a dash joins (so "fp-0a1b2c3d" is one word, not a sha), a
	// dot does not ("abc1234." is a sha).
	shaWordRe = regexp.MustCompile(`[A-Za-z0-9_]+(?:-[A-Za-z0-9_]+)*`)
	// idRe is a whole id: a dash or a dot joins its parts (A8-5, C1.M2).
	idRe  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(?:[-.][A-Za-z0-9_]+)*$`)
	hexRe = regexp.MustCompile(`^[0-9a-fA-F]{7,40}$`)
	// arrowRe is the text between A and B in "A -> B".
	arrowRe = regexp.MustCompile(`^\s*(?:->|→|=>)\s*$`)
	// prRe finds PR numbers in board text: "PR 12", "PR12", "PR #12", "PR#12", "!12". A bare "#12" is not a PR.
	prRe     = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_])(?:PR\s*#?\s*|!)(\d+)\b`)
	mergeRes = []*regexp.Regexp{
		regexp.MustCompile(`^Merged PR (\d+):`),
		regexp.MustCompile(`^Merge pull request #(\d+)\b`),
		regexp.MustCompile(`\(#(\d+)\)\s*$`),
	}
	// typeRe is a conventional-commit prefix: type, optional (scope), optional "!", colon.
	typeRe = regexp.MustCompile(`^[A-Za-z]+(?:\(([^)]*)\))?!?:\s*`)
	// leadIDRe is "ID:" at the start of what follows the prefix.
	leadIDRe = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_]*(?:[-.][A-Za-z0-9_]+)*):`)
	numRe    = regexp.MustCompile(`\d+`)
	// trailRe is a parenthesised list at the very end of a subject.
	trailRe = regexp.MustCompile(`\(([^()]*)\)\s*$`)
)

func isSHA(w string) bool {
	return hexRe.MatchString(w) && strings.ContainsAny(w, "0123456789") && strings.ContainsAny(strings.ToLower(w), "abcdef")
}

// citedSHAs are the shas in free text s: words of 7 to 40 hex digits with at least one letter and one digit (plain
// numbers and words like "decade" are not shas; an all-digit sha is missed). own leaves out a sha that is only ever
// the left of "A -> B"; arrow is the rights.
func citedSHAs(s string) (own, arrow []string) {
	left, right := map[string]bool{}, map[string]bool{}
	var all []string
	prevSHA, prevEnd := "", 0
	for _, ix := range shaWordRe.FindAllStringIndex(s, -1) {
		w := s[ix[0]:ix[1]]
		if !isSHA(w) {
			prevSHA = ""
			continue
		}
		if prevSHA != "" && arrowRe.MatchString(s[prevEnd:ix[0]]) {
			left[prevSHA], right[w] = true, true
		}
		all = appendNew(all, w)
		prevSHA, prevEnd = w, ix[1]
	}
	for _, w := range all {
		if right[w] {
			arrow = append(arrow, w)
		}
		if !left[w] || right[w] {
			own = append(own, w)
		}
	}
	return own, arrow
}

func prs(s string) []string {
	var out []string
	for _, m := range prRe.FindAllStringSubmatch(s, -1) {
		out = appendNew(out, m[1])
	}
	return out
}

func appendNew(list []string, s string) []string {
	if contains(list, s) {
		return list
	}
	return append(list, s)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// isID says whether w can be a row id: it starts with a letter and holds a digit (docs/RULES.md §3.1); a dash or a
// dot may join its parts.
func isID(w string) bool { return idRe.MatchString(w) && strings.ContainsAny(w, "0123456789") }

// isRevert says whether a subject reverts: it starts with "revert" in any case ("Revert \"…\"", "revert(R1): …").
func isRevert(subject string) bool { return strings.HasPrefix(strings.ToLower(subject), "revert") }

// quoteRe is the quoted subject in a revert subject (Revert "fix(R1): thing").
var quoteRe = regexp.MustCompile(`"(.+)"`)

// subjectTags are the row ids a commit subject names as a tag (the subject rule in the package comment): exact ids
// (scope, lead "ID:", trailing list) and the scope ids, which may also match a row id that differs in case. A revert
// names no row.
func subjectTags(subject string) (exact, scope []string) {
	if isRevert(subject) {
		return nil, nil
	}
	return tagsOf(subject)
}

// revertedTags are the row ids a revert subject takes back: the tags of the subject it quotes, else its own tags.
func revertedTags(subject string) (exact, scope []string) {
	if m := quoteRe.FindStringSubmatch(subject); m != nil {
		return tagsOf(m[1])
	}
	return tagsOf(subject)
}

func tagsOf(subject string) (exact, scope []string) {
	rest := subject
	if m := typeRe.FindStringSubmatch(subject); m != nil {
		for _, s := range strings.FieldsFunc(m[1], func(r rune) bool { return r == ',' || r == '/' || r == ' ' }) {
			if isID(s) {
				scope = appendNew(scope, s)
			}
		}
		rest = subject[len(m[0]):]
	}
	exact = append(exact, scope...)
	if m := leadIDRe.FindStringSubmatch(rest); m != nil && isID(m[1]) {
		exact = appendNew(exact, m[1])
	}
	if m := trailRe.FindStringSubmatch(subject); m != nil {
		for _, s := range strings.Split(m[1], ",") {
			if s = strings.TrimSpace(s); isID(s) {
				exact = appendNew(exact, s)
			}
		}
	}
	return exact, scope
}

// foldable says whether a scope may name id in another case: 3+ characters with a letter and a digit.
func foldable(id string) bool { return len(id) >= 3 && isID(id) }

// namesRow applies the subject rule to one subject and one row id.
func namesRow(subject, id string) bool {
	exact, scope := subjectTags(subject)
	if contains(exact, id) {
		return true
	}
	if foldable(id) {
		for _, s := range scope {
			if strings.EqualFold(s, id) {
				return true
			}
		}
	}
	return false
}

// ParseTSV reads a board with a header row. Columns are found by name (any order, no case): the row id ("row" or
// "id") and the state ("state" or "status") are required; the row's own shas come from the "sha" or "commit" column,
// its PR numbers from the "pr" or "prs" column (any number in it) when there is one, else from the row, state and sha
// cells in the PR forms; never from other cells such as notes ("blocked by PR 41" is another row's PR). Blank lines
// and lines with an empty id are left out.
func ParseTSV(r io.Reader) ([]Row, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	col := map[string]int{"row": -1, "state": -1, "sha": -1, "pr": -1}
	names := map[string]string{"row": "row", "id": "row", "state": "state", "status": "state", "sha": "sha", "commit": "sha",
		"pr": "pr", "prs": "pr"}
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
		var own []string
		for _, w := range shaWordRe.FindAllString(cell("sha"), -1) {
			if isSHA(w) {
				own = appendNew(own, w)
			}
		}
		var pr []string
		if col["pr"] >= 0 {
			for _, d := range numRe.FindAllString(cell("pr"), -1) {
				pr = appendNew(pr, d)
			}
		} else {
			pr = prs(cell("row") + "\t" + cell("state") + "\t" + cell("sha"))
		}
		rows = append(rows, Row{ID: cell("row"), State: cell("state"), Line: n, SHAs: own, PRs: pr})
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
	headingRe   = regexp.MustCompile(`^(#+)\s+(.*)$`)
	bulletRe    = regexp.MustCompile(`^\s*(?:[-*+]|\d+\.)\s+(.*)$`)
	taskRe      = regexp.MustCompile(`^\[([ xX~])\]\s+(.*)$`)
	fenceRe     = regexp.MustCompile("^\\s*(```|~~~)")
	separatorRe = regexp.MustCompile(`^\|?[\s:|-]*-[\s:|-]*\|?$`)
)

// ParseMarkdown reads the rows under the heading that contains section (any level, up to the next heading of that
// level or higher; "" reads the whole file). Code fences are skipped. Rows:
//   - a bullet "- <mark> <id> …" (or "- <id> <mark> …") whose mark is a symbol (no letter or digit, e.g. ✅ ⬜ 🔶), or a task-list bullet
//     "- [ ] <id> …" / "- [x] …" / "- [~] …" (state "[ ]", "[x]", "[~]"); the id may be in **bold** or `code`;
//   - a table line "| <id> | … |": the state is the header's "state" or "status" column when the table has one,
//     else the first later cell that is a symbol, else the second cell. A dash ("-", "–", "—") is no state.
//
// Shas and PR numbers come from the whole line (shas are cited, see Row). A line in the section that starts with a
// mark, a task box or a pipe but gives no row (two ids "X1/X2", "(X1)", a first cell that is no id) is returned as
// NotRead; a table's header and separator lines are not.
func ParseMarkdown(r io.Reader, section string) ([]Row, []NotRead, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	var lines []string
	for sc.Scan() {
		lines = append(lines, strings.TrimRight(sc.Text(), "\r"))
	}
	if err := sc.Err(); err != nil {
		return nil, nil, err
	}
	in, found, level, fence := section == "", section == "", 0, false
	stateCol := -1
	var rows []Row
	var notRead []NotRead
	for i, line := range lines {
		if fenceRe.MatchString(line) {
			fence = !fence
			continue
		}
		if fence {
			continue
		}
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
		t := strings.TrimSpace(line)
		var id, state string
		switch {
		case strings.HasPrefix(t, "|"):
			if separatorRe.MatchString(t) {
				continue
			}
			cells := tableCells(t)
			if i+1 < len(lines) && separatorRe.MatchString(strings.TrimSpace(lines[i+1])) {
				stateCol = -1 // a header line: remember its state column
				for j, c := range cells {
					if k := strings.ToLower(c); k == "state" || k == "status" {
						stateCol = j
						break
					}
				}
				continue
			}
			if i == 0 || !strings.HasPrefix(strings.TrimSpace(lines[i-1]), "|") {
				stateCol = -1 // a table with no header
			}
			id = unwrap(cells[0])
			if stateCol >= 0 {
				if stateCol < len(cells) {
					state = cells[stateCol]
				}
			} else {
				for _, c := range cells[1:] {
					if isMark(c) {
						state = c
						break
					}
				}
				if state == "" && len(cells) > 1 {
					state = cells[1] // no header and no symbol: the second cell is the state
				}
			}
			if isDash(state) {
				state = ""
			}
		default:
			m := bulletRe.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			body := m[1]
			if tm := taskRe.FindStringSubmatch(body); tm != nil {
				state, body = "["+strings.ToLower(tm[1])+"]", tm[2]
			} else if f := strings.Fields(body); len(f) > 0 && isMark(f[0]) {
				state, body = f[0], strings.TrimSpace(strings.TrimPrefix(body, f[0]))
			} else if len(f) > 1 && isMark(f[1]) {
				state, body = f[1], f[0] // the mark after the id: "- **R8** ✅ …"
			} else {
				continue // a plain bullet: no row and no claim to be one
			}
			if f := strings.Fields(body); len(f) > 0 {
				id = unwrap(strings.TrimRight(f[0], ":,;"))
			}
		}
		if !isID(id) {
			notRead = append(notRead, NotRead{Line: i + 1, Text: t})
			continue
		}
		var own, arrow []string
		o, a := citedSHAs(line)
		for _, x := range o {
			if x != id {
				own = append(own, x)
			}
		}
		for _, x := range a {
			if x != id {
				arrow = append(arrow, x)
			}
		}
		rows = append(rows, Row{ID: id, State: state, Line: i + 1, SHAs: own, Arrow: arrow, Cited: true, PRs: prs(line)})
	}
	if !found {
		return nil, nil, fmt.Errorf("no section heading contains %q", section)
	}
	return rows, notRead, nil
}

func tableCells(t string) []string {
	cells := strings.Split(strings.Trim(t, "|"), "|")
	for i := range cells {
		cells[i] = strings.TrimSpace(cells[i])
	}
	return cells
}

// unwrap takes **bold**, __bold__ or `code` marks off an id.
func unwrap(s string) string { return strings.Trim(s, "*_`") }

func isDash(s string) bool { return strings.Trim(s, "-–— ") == "" }

// isMark says whether s is a status symbol: not empty, not a dash, no ASCII letter, digit, pipe or bracket
// (✅, ⬜, 🔶, ✖, …).
func isMark(s string) bool {
	if isDash(s) {
		return false
	}
	for _, c := range s {
		if c < 128 && (c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("|[(", c)) {
			return false
		}
	}
	return true
}

// norm compares states without case and without emoji variation selectors.
func norm(s string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), string(rune(0xFE0F)), ""))
}

func in(list []string, s string) bool {
	for _, x := range list {
		if norm(x) == norm(s) {
			return true
		}
	}
	return false
}

// branch is one repo's branch as read: its tip, the PR numbers its scanned merge subjects name, and for each row id
// the newest commit that tags it (word: exact ids; fold: foldable scope ids in lower case).
type branch struct {
	repo       Repo
	name       string
	tip        string
	pr         map[string]commitRef
	word, fold map[string]tagRef
	commits    int
	oldest     string
}

type commitRef struct{ sha, subject string }

// tagRef is the newest commit that tags a row id; reverted when that commit is a revert of the row's work.
type tagRef struct {
	commitRef
	reverted bool
}

// tag is the decision of b's subjects for a row id: the exact id first, then (for a foldable id) a scope in any case.
func (b *branch) tag(id string) (tagRef, bool) {
	if t, ok := b.word[id]; ok {
		return t, true
	}
	if foldable(id) {
		t, ok := b.fold[strings.ToLower(id)]
		return t, ok
	}
	return tagRef{}, false
}

// Check judges each row against every repo's branch. It fails (never a clean report) on 0 rows, a board where no
// row is judged, a folder that is no repo, a branch that does not resolve, or any git error.
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
		var kind string
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
		case in(cfg.Open, row.State):
			kind = "open"
			rep.Open++
		default:
			rep.Unknown = append(rep.Unknown, fmt.Sprintf("%s (%s)", row.ID, row.State))
			continue
		}
		wrap := func(err error) error { return fmt.Errorf("row %s (line %d): %w", row.ID, row.Line, err) }
		if kind == "done" {
			if len(row.SHAs)+len(row.PRs) == 0 {
				rep.Unchecked = append(rep.Unchecked, row.ID)
				continue
			}
			f, err := doneFinding(row, bs)
			if err != nil {
				return rep, wrap(err)
			}
			if f != nil {
				rep.Findings = append(rep.Findings, *f)
			}
			continue
		}
		ev, notes, err := openEvidence(row, bs)
		if err != nil {
			return rep, wrap(err)
		}
		rep.Notes = append(rep.Notes, notes...)
		switch {
		case kind == "open" && len(ev) > 0:
			rep.Findings = append(rep.Findings, Finding{Row: row, Kind: "stale", Evidence: ev})
		case kind == "partial" && len(ev) > 0:
			rep.PartialLanded = append(rep.PartialLanded, row.ID)
		}
	}
	if rep.Done+rep.Open+rep.Partial == 0 {
		return rep, fmt.Errorf("no row was judged: all %d rows are skipped or in a state no list names (%s)", rep.Rows,
			strings.Join(rep.Unknown, " "))
	}
	return rep, nil
}

// landed looks a sha up in every repo: the branch it is on and its full id, or (branch nil) why it is on none.
func landed(sha string, bs []*branch) (*branch, string, string, error) {
	var why []string
	for _, b := range bs {
		full, miss, err := resolve(b.repo.Path, sha)
		if err != nil {
			return nil, "", "", err
		}
		if full == "" {
			why = append(why, miss+" in "+filepath.Base(b.repo.Path))
			continue
		}
		anc, err := ancestor(b.repo.Path, full, b.tip)
		if err != nil {
			return nil, "", "", err
		}
		if anc {
			return b, full, "", nil
		}
		why = append(why, "not on "+label(b))
	}
	return nil, "", strings.Join(why, ", "), nil
}

// prMerged finds a PR's merge subject: the one branch whose scanned subjects name it, or (branch nil) why none counts:
// merged in none, or in several repos (ambiguous true).
func prMerged(p string, bs []*branch) (b *branch, c commitRef, why string, ambiguous bool) {
	var hits []*branch
	for _, b := range bs {
		if _, ok := b.pr[p]; ok {
			hits = append(hits, b)
		}
	}
	switch len(hits) {
	case 0:
		return nil, commitRef{}, "no merge subject in the scanned commits", false
	case 1:
		return hits[0], hits[0].pr[p], "", false
	}
	var names []string
	for _, b := range hits {
		names = append(names, label(b))
	}
	return nil, commitRef{}, "merged in " + strings.Join(names, " and ") + " (ambiguous)", true
}

// doneFinding judges a done row: unlanded when an own sha or PR is missing, or, for cited ones, when the right of an
// "A -> B" is missing or none of them landed; ambiguous when only PRs merged in several repos are in doubt.
func doneFinding(row Row, bs []*branch) (*Finding, error) {
	var misses, cited, amb []string
	anyLanded := false
	for _, s := range row.SHAs {
		b, _, why, err := landed(s, bs)
		if err != nil {
			return nil, err
		}
		switch {
		case b != nil:
			anyLanded = true
		case !row.Cited || contains(row.Arrow, s):
			misses = append(misses, "sha "+s+": "+why)
		default:
			cited = append(cited, "sha "+s+": "+why)
		}
	}
	for _, p := range row.PRs {
		b, _, why, ambiguous := prMerged(p, bs)
		switch {
		case b != nil:
			anyLanded = true
		case ambiguous:
			amb = append(amb, "PR "+p+": "+why)
		case !row.Cited:
			misses = append(misses, "PR "+p+": "+why)
		default:
			cited = append(cited, "PR "+p+": "+why)
		}
	}
	if row.Cited && anyLanded {
		amb = nil // a cited row with other proof on the branch: a PR in doubt changes nothing
	} else if !anyLanded {
		misses = append(misses, cited...)
	}
	switch {
	case len(misses) > 0:
		return &Finding{Row: row, Kind: "unlanded", Misses: append(misses, amb...)}, nil
	case len(amb) > 0:
		return &Finding{Row: row, Kind: "ambiguous", Misses: amb}, nil
	}
	return nil, nil
}

// openEvidence lists every proof that an open or partial row landed, and notes on PRs that did not count.
func openEvidence(row Row, bs []*branch) (ev []Evidence, notes []string, err error) {
	for _, s := range row.SHAs {
		b, full, _, err := landed(s, bs)
		if err != nil {
			return nil, nil, err
		}
		if b == nil {
			continue
		}
		if row.Cited {
			subj, err := subjectOf(b.repo.Path, full)
			if err != nil {
				return nil, nil, err
			}
			if t, ok := b.tag(row.ID); !namesRow(subj, row.ID) || ok && t.reverted {
				continue // a cited sha whose commit is not tagged with the row (a baseline), or reverted work: no proof
			}
		}
		ev = append(ev, Evidence{Kind: "sha", Ref: s, Commit: short(full), Repo: b.repo.Path, Branch: b.name})
	}
	for _, p := range row.PRs {
		b, c, why, ambiguous := prMerged(p, bs)
		switch {
		case ambiguous:
			notes = append(notes, fmt.Sprintf("%s: PR %s %s, not counted", row.ID, p, why))
			continue
		case b == nil:
			continue
		case (row.Cited || len(bs) > 1) && !namesRow(c.subject, row.ID):
			notes = append(notes, fmt.Sprintf("%s: PR %s merged in %s, not tied to this row (%s %q)", row.ID, p, label(b), short(c.sha), c.subject))
			continue
		}
		ev = append(ev, Evidence{Kind: "merge-subject", Ref: "PR " + p, Commit: short(c.sha), Subject: c.subject, Repo: b.repo.Path, Branch: b.name})
	}
	for _, b := range bs {
		t, ok := b.tag(row.ID)
		if !ok {
			continue
		}
		if t.reverted {
			notes = append(notes, fmt.Sprintf("%s: its tagged work was reverted by %s %q on %s, not counted", row.ID, short(t.sha), t.subject, label(b)))
			continue
		}
		ev = append(ev, Evidence{Kind: "row-id-subject", Ref: row.ID, Commit: short(t.sha), Subject: t.subject, Repo: b.repo.Path, Branch: b.name})
		break
	}
	return ev, notes, nil
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

// pickCommit chooses among the commits whose id starts with a sha: none is "not found", several are "ambiguous".
func pickCommit(commits []string) (full, why string) {
	switch len(commits) {
	case 0:
		return "", "not found"
	case 1:
		return commits[0], ""
	}
	return "", "ambiguous"
}

// resolve looks sha up as an object id only (never a branch or tag name): the full commit id, or why there is none.
func resolve(repo, sha string) (full, why string, err error) {
	out, _, err := gitRun(repo, "rev-parse", "--disambiguate="+sha)
	if err != nil {
		return "", "", err
	}
	var commits []string
	for _, o := range strings.Fields(out) {
		typ, _, err := gitRun(repo, "cat-file", "-t", o)
		if err != nil {
			return "", "", err
		}
		if strings.TrimSpace(typ) == "commit" {
			commits = append(commits, o)
		}
	}
	full, why = pickCommit(commits)
	return full, why, nil
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

func subjectOf(repo, full string) (string, error) {
	out, _, err := gitRun(repo, "log", "-1", "--format=%s", full)
	return strings.TrimSpace(out), err
}

func readBranch(r Repo, depth int) (*branch, error) {
	if _, code, err := gitRun(r.Path, "rev-parse", "--git-dir"); code != 0 {
		return nil, fmt.Errorf("%s is not a git repo: %w", r.Path, err)
	}
	out, code, err := gitRun(r.Path, "rev-parse", "--verify", "-q", r.Branch+"^{commit}")
	switch {
	case code == 1:
		return nil, fmt.Errorf("branch %q not found in %s", r.Branch, r.Path)
	case err != nil:
		return nil, err
	}
	tip := strings.TrimSpace(out)
	out, _, err = gitRun(r.Path, "log", fmt.Sprintf("-n%d", depth), "--format=%H%x1f%cs%x1f%s", tip)
	if err != nil {
		return nil, err
	}
	b := &branch{repo: r, name: r.Branch, tip: tip, pr: map[string]commitRef{}, word: map[string]tagRef{}, fold: map[string]tagRef{}}
	for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		f := strings.SplitN(l, "\x1f", 3)
		if len(f) != 3 {
			continue
		}
		b.commits++
		b.oldest = f[1]
		c := commitRef{sha: f[0], subject: f[2]}
		reverted := isRevert(c.subject)
		exact, scope := subjectTags(c.subject)
		if reverted {
			exact, scope = revertedTags(c.subject)
		} else {
			for _, re := range mergeRes {
				if m := re.FindStringSubmatch(c.subject); m != nil {
					if _, seen := b.pr[m[1]]; !seen {
						b.pr[m[1]] = c
					}
				}
			}
		}
		// git log is newest first: the first commit that tags an id decides it
		for _, id := range exact {
			if _, seen := b.word[id]; !seen {
				b.word[id] = tagRef{c, reverted}
			}
		}
		for _, id := range scope {
			if k := strings.ToLower(id); foldable(id) {
				if _, seen := b.fold[k]; !seen {
					b.fold[k] = tagRef{c, reverted}
				}
			}
		}
	}
	if b.commits == 0 {
		return nil, fmt.Errorf("git log of %s in %s read no commits", r.Branch, r.Path)
	}
	return b, nil
}
