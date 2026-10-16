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
)

// isSha: 7 to 40 hex with at least one letter, so a run id is not taken for one.
func isSha(w string) bool { return shaRe.MatchString(w) && letterRe.MatchString(w) }

// Verified gives, per milestone, the id of the VERIFIED line that checked it, 0 for none, and how many VERIFIED lines
// to the lead match no done milestone (RULES 3.4). A VERIFIED line counts when it is to the lead and from Crius
// (crius:lead) or the lead's own project's QA (role qa or qa-*); its text is "<ref> · <evidence>". The ref matches a
// done milestone when it is a sha that is, or begins, a sha word of the milestone's refs (any case); or equals one of
// the milestone's refs or its text (any case). The sender is trusted as written on the line.
func Verified(db *sql.DB, lead string, g *Goal) ([]int64, int, error) {
	project, _, _ := strings.Cut(lead, ":")
	rows, err := db.Query("select id, coalesce(refs, ''), text from event where kind = 'verified' and"+
		" ((project = 'crius' and actor = 'lead') or (project = ? and (actor = 'qa' or actor like 'qa-%'))) order by id",
		project)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out, unmatched := make([]int64, len(g.Milestones)), 0
	for rows.Next() {
		var id int64
		var refs, text string
		if err := rows.Scan(&id, &refs, &text); err != nil {
			return nil, 0, err
		}
		var r map[string][]string
		if refs != "" {
			if err := json.Unmarshal([]byte(refs), &r); err != nil {
				return nil, 0, err
			}
		}
		if !slices.Contains(r["to"], lead) {
			continue
		}
		ref, _, _ := strings.Cut(text, " · ")
		ref = strings.ToLower(strings.TrimSpace(ref))
		hit := false
		for i, m := range g.Milestones {
			if m.State == Done && matches(ref, m) {
				hit = true
				if out[i] == 0 {
					out[i] = id
				}
			}
		}
		if !hit {
			unmatched++
		}
	}
	return out, unmatched, rows.Err()
}

func matches(ref string, m Milestone) bool {
	if ref == strings.ToLower(m.Text) {
		return true
	}
	for _, r := range strings.Split(m.Refs, ",") {
		if ref == strings.ToLower(strings.TrimSpace(r)) {
			return true
		}
	}
	if !isSha(ref) {
		return false
	}
	for _, w := range wordRe.FindAllString(strings.ToLower(m.Refs), -1) {
		if isSha(w) && (strings.HasPrefix(w, ref) || strings.HasPrefix(ref, w)) {
			return true
		}
	}
	return false
}
