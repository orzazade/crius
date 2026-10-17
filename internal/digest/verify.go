package digest

import (
	"database/sql"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
)

var (
	shaRe    = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	letterRe = regexp.MustCompile(`[a-f]`)
	wordRe   = regexp.MustCompile(`[0-9A-Za-z]+`)
	tokenRe  = regexp.MustCompile(`[0-9A-Za-z]+(?:-[0-9A-Za-z]+)*`)
	digitRe  = regexp.MustCompile(`[0-9]`)
	alphaRe  = regexp.MustCompile(`[A-Za-z]`)
)

// isID: a token with a letter and a digit (F-126, RB-89, c84d78c), so a plain word or number is no id.
func isID(w string) bool {
	return tokenRe.FindString(w) == w && digitRe.MatchString(w) && alphaRe.MatchString(w)
}

// isSha: 7 to 40 hex with at least one letter, so a run id is not taken for one.
func isSha(w string) bool { return shaRe.MatchString(w) && letterRe.MatchString(w) }

// Verified gives, per milestone, the id of the first VERIFIED line that checked it, 0 for none, and how many VERIFIED
// lines to the lead count for no milestone (RULES 3.4). A VERIFIED line is to the lead and from Crius (crius:lead) or
// the lead's own project's QA (role qa or qa-*); it counts when it matches exactly one done milestone, so a check that
// does not land is seen. The sender is trusted as written on the line. When the Done line counts rows, rowIDs holds the
// row ids of its row file (Goal.RowIDs); a line "<ref> · <evidence>" whose ref is one of them, in any case, also counts
// for that row, and rows gives the distinct rows (Crius 10-17 04:23, 04:58, 05:13; reviews of fa0c4d0 and 9072051: a
// row guessed from the ref's shape made a false MISMATCH).
func Verified(db *sql.DB, lead string, g *Goal, rowIDs map[string]bool) (checked []int64, rows, notCounted int, err error) {
	project, _, _ := strings.Cut(lead, ":")
	q, err := db.Query("select id, coalesce(refs, ''), text from event where kind = 'verified' and"+
		" ((project = 'crius' and actor = 'lead') or (project = ? and (actor = 'qa' or actor like 'qa-%'))) order by id",
		project)
	if err != nil {
		return nil, 0, 0, err
	}
	defer q.Close()
	out, seen := make([]int64, len(g.Milestones)), map[string]bool{}
	for q.Next() {
		var id int64
		var refs, text string
		if err := q.Scan(&id, &refs, &text); err != nil {
			return nil, 0, 0, err
		}
		var r map[string][]string
		if refs != "" {
			if err := json.Unmarshal([]byte(refs), &r); err != nil {
				return nil, 0, 0, err
			}
		}
		if !slices.Contains(r["to"], lead) {
			continue
		}
		row := false
		if ref, evidence, _ := strings.Cut(text, " · "); strings.TrimSpace(evidence) != "" {
			if ref = strings.ToUpper(strings.TrimSpace(ref)); rowIDs[ref] {
				seen[ref], row = true, true
			}
		}
		// The strongest match over all milestones decides; the line counts when it points at one done milestone.
		best, hits := none, []int(nil)
		for i, m := range g.Milestones {
			s := match(text, m)
			if s > best {
				best, hits = s, nil
			}
			if s == best && s != none && m.State == Done {
				hits = append(hits, i)
			}
		}
		if len(hits) != 1 {
			if !row {
				notCounted++
			}
			continue
		}
		if out[hits[0]] == 0 {
			out[hits[0]] = id
		}
	}
	return out, len(seen), notCounted, q.Err()
}

type strength int

const (
	none      strength = iota
	mentioned          // an id or sha anywhere on the milestone line: counts only when no milestone matches exactly
	exactly
)

// match: the text is "<ref> · <evidence>" with some evidence. It matches exactly when the ref is the milestone's text
// (which may hold " · ") or one of its refs, exactly, or a sha that is, or begins, a sha word of its refs, in any case.
// The milestone mentions it when the ref is an id that is a token of the milestone line (text and refs), in the same
// case, or a sha of the line's words (Crius 10-17 04:23; review of 2edcda5: an exact match wins).
func match(text string, m Milestone) strength {
	if rest, ok := strings.CutPrefix(text, m.Text+" · "); ok && strings.TrimSpace(rest) != "" {
		return exactly
	}
	ref, evidence, ok := strings.Cut(text, " · ")
	if !ok || strings.TrimSpace(evidence) == "" {
		return none
	}
	ref = strings.TrimSpace(ref)
	for _, r := range strings.Split(m.Refs, ",") {
		if ref == strings.TrimSpace(r) {
			return exactly
		}
	}
	if shaIn(ref, m.Refs) {
		return exactly
	}
	line := m.Text + " " + m.Refs
	if isID(ref) && slices.Contains(tokenRe.FindAllString(line, -1), ref) || shaIn(ref, line) {
		return mentioned
	}
	return none
}

// shaIn: ref is a sha that is, or begins, a sha word of s, in any case.
func shaIn(ref, s string) bool {
	if ref = strings.ToLower(ref); !isSha(ref) {
		return false
	}
	for _, w := range wordRe.FindAllString(strings.ToLower(s), -1) {
		if isSha(w) && (strings.HasPrefix(w, ref) || strings.HasPrefix(ref, w)) {
			return true
		}
	}
	return false
}
