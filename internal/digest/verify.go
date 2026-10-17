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
	rowRe    = regexp.MustCompile(`^F-[0-9]+$`)
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
// does not land is seen. The sender is trusted as written on the line. When the Done line counts rows (Goal.Rows), a
// line "<F-id> · <evidence>" counts for that row instead, and rows gives the distinct rows (Crius 10-17 04:23).
func Verified(db *sql.DB, lead string, g *Goal) (checked []int64, rows, notCounted int, err error) {
	project, _, _ := strings.Cut(lead, ":")
	q, err := db.Query("select id, coalesce(refs, ''), text from event where kind = 'verified' and"+
		" ((project = 'crius' and actor = 'lead') or (project = ? and (actor = 'qa' or actor like 'qa-%'))) order by id",
		project)
	if err != nil {
		return nil, 0, 0, err
	}
	defer q.Close()
	_, countsRows := g.Rows()
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
		if ref, evidence, _ := strings.Cut(text, " · "); countsRows && rowRe.MatchString(strings.TrimSpace(ref)) &&
			strings.TrimSpace(evidence) != "" {
			seen[strings.TrimSpace(ref)] = true
			continue
		}
		var hits []int
		for i, m := range g.Milestones {
			if m.State == Done && matches(text, m) {
				hits = append(hits, i)
			}
		}
		if len(hits) != 1 {
			notCounted++
			continue
		}
		if out[hits[0]] == 0 {
			out[hits[0]] = id
		}
	}
	return out, len(seen), notCounted, q.Err()
}

// matches: the text is "<ref> · <evidence>" with some evidence, and the ref is the milestone's text (which may hold
// " · ") or one of its refs, exactly; or an id that is a token of the milestone line (text and refs), in the same case;
// or a sha that is, or begins, a sha word of the line, in any case (Crius 10-17 04:23).
func matches(text string, m Milestone) bool {
	if rest, ok := strings.CutPrefix(text, m.Text+" · "); ok && strings.TrimSpace(rest) != "" {
		return true
	}
	ref, evidence, ok := strings.Cut(text, " · ")
	if !ok || strings.TrimSpace(evidence) == "" {
		return false
	}
	ref = strings.TrimSpace(ref)
	for _, r := range strings.Split(m.Refs, ",") {
		if ref == strings.TrimSpace(r) {
			return true
		}
	}
	line := m.Text + " " + m.Refs
	if isID(ref) && slices.Contains(tokenRe.FindAllString(line, -1), ref) {
		return true
	}
	if ref = strings.ToLower(ref); !isSha(ref) {
		return false
	}
	for _, w := range wordRe.FindAllString(strings.ToLower(line), -1) {
		if isSha(w) && (strings.HasPrefix(w, ref) || strings.HasPrefix(ref, w)) {
			return true
		}
	}
	return false
}
