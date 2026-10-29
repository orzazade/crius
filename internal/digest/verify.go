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
	// The line's own header typed again at the start of its text (crius:lead 10-24 12:43).
	headerRe = regexp.MustCompile(`^VERIFIED\s+\S+\s+->\s+[^—]+—\s*`)
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
// line "row <id> · <evidence>", the id one token with a letter and a digit, counts for that row, and rows gives the
// distinct ids in any case; any other ref is no row (RULES 3.1, Crius 10-17 05:34: four review rounds found guesses at rows). The line is
// then matched to a milestone by its id.
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
		text = headerRe.ReplaceAllString(text, "")
		row := false
		if ref, evidence, _ := strings.Cut(text, " · "); strings.TrimSpace(evidence) != "" {
			if id, ok := strings.CutPrefix(strings.TrimSpace(ref), "row "); ok && isID(id) {
				// A row line never closes an open milestone, also in a lead that counts milestones (review of 26e2143).
				row = true
				if countsRows {
					seen[strings.ToUpper(id)] = true
					text = id + " · " + evidence
				}
			}
		}
		// The strongest match over all milestones decides; the line counts when it points at one milestone. One not
		// marked done is done by it (crius:lead 10-25 21:22), and the report says the status file is behind; a row line
		// checks one row, never a whole open milestone.
		best, hits := none, []int(nil)
		for i, m := range g.Milestones {
			s := match(text, m)
			if s > best {
				best, hits = s, nil
			}
			if s == best && s != none && (m.State == Done || !row) {
				hits = append(hits, i)
			}
		}
		if len(hits) != 1 {
			if !row || !countsRows {
				notCounted++
			}
			continue
		}
		if out[hits[0]] == 0 || g.Milestones[hits[0]].State != Done {
			out[hits[0]] = id
		}
	}
	if err := q.Err(); err != nil {
		return nil, 0, 0, err
	}
	// An open milestone is done by its last VERIFIED line only while no later FAIL, CORRECTION, CORRECTED or REOPEN
	// line names it: a bug found after acceptance withdraws it (RULES 2.3) and the lead un-ticks the milestone
	// (review of 26e2143).
	for i, m := range g.Milestones {
		if m.State == Done || out[i] == 0 {
			continue
		}
		withdrawn, err := withdrawnAfter(db, project, out[i], m)
		if err != nil {
			return nil, 0, 0, err
		}
		if withdrawn {
			out[i] = 0
		}
	}
	return out, len(seen), notCounted, nil
}

// withdrawnAfter: a FAIL, CORRECTION, CORRECTED or REOPEN line after event id, from the lead's project or Crius, names
// the milestone.
func withdrawnAfter(db *sql.DB, project string, id int64, m Milestone) (bool, error) {
	q, err := db.Query("select text from event where id > ? and kind in ('fail', 'correction', 'corrected', 'reopen') and"+
		" (project = ? or (project = 'crius' and actor = 'lead'))", id, project)
	if err != nil {
		return false, err
	}
	defer q.Close()
	for q.Next() {
		var text string
		if err := q.Scan(&text); err != nil {
			return false, err
		}
		if names(text, m) {
			return true, nil
		}
	}
	return false, q.Err()
}

// names: the text holds an id of the milestone line (same case), a sha of it, or its whole text when that has 4 or
// more words.
func names(text string, m Milestone) bool {
	line := m.Text + " " + m.Refs
	in := tokenRe.FindAllString(line, -1)
	for _, w := range tokenRe.FindAllString(text, -1) {
		if isID(w) && slices.Contains(in, w) || shaIn(w, line) {
			return true
		}
	}
	return len(tokenRe.FindAllString(m.Text, -1)) >= 4 && strings.Contains(text, m.Text)
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
	// The milestone named by its words, without its quote marks or a word of it: 4 or more words of the ref, each
	// in the text in the same case.
	if words := tokenRe.FindAllString(ref, -1); len(words) >= 4 {
		in := tokenRe.FindAllString(m.Text, -1)
		if !slices.ContainsFunc(words, func(w string) bool { return !slices.Contains(in, w) }) {
			return mentioned
		}
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
