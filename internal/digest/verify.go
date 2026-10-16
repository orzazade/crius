package digest

import (
	"database/sql"
	"regexp"
	"strings"
	"time"
)

var (
	wordRe   = regexp.MustCompile(`[0-9A-Za-z]+`)
	shaRe    = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	letterRe = regexp.MustCompile(`[a-f]`)
	busRefRe = regexp.MustCompile(`\bbus (\d{4}-\d{2}-\d{2}T\d{2}:\d{2})\b`)
	passRe   = regexp.MustCompile(`\bchecked PASS\b([^?]|$)`)
	failRe   = regexp.MustCompile(`\bFAIL`)
)

// Verified gives, per milestone, the id of the bus line that checked it, 0 for none (Crius 10-17: a % counts only
// with a Crius or QA check on the bus). A check is an info, done or decision line from Crius (crius:lead) or from the
// lead's own project's QA (role qa or qa-*) that says "checked PASS" (no "?" after it) and has no FAIL. A done
// milestone is verified when a check names one of its refs: a sha (7 or more hex with at least one letter, so a
// run id is not taken for one; any case; sha= allowed) that is, or begins, a whole word of the check; or "bus <local
// YYYY-MM-DDTHH:MM>", the check at that minute when it names the lead or is the lead's own QA. Other refs (run
// ids, PRs) cannot be matched, so a milestone with only those stays claimed. Milestones not done are never
// verified. The sender is trusted as written on the line.
func Verified(db *sql.DB, lead string, g *Goal) ([]int64, error) {
	project, _, _ := strings.Cut(lead, ":")
	rows, err := db.Query("select id, ts, actor, text from event where kind in ('info', 'done', 'decision') and"+
		" ((project = 'crius' and actor = 'lead') or (project = ? and (actor = 'qa' or actor like 'qa-%')))"+
		" and text like '%checked PASS%' order by id", project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type check struct {
		id           int64
		ts, text     string
		words        []string
		names, ownQA bool
	}
	var checks []check
	for rows.Next() {
		var c check
		var role string
		if err := rows.Scan(&c.id, &c.ts, &role, &c.text); err != nil {
			return nil, err
		}
		if !passRe.MatchString(c.text) || failRe.MatchString(c.text) {
			continue
		}
		c.words = wordRe.FindAllString(strings.ToLower(c.text), -1)
		c.names, c.ownQA = strings.Contains(strings.ToLower(c.text), lead), role == "qa" || strings.HasPrefix(role, "qa-")
		checks = append(checks, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]int64, len(g.Milestones))
	for i, m := range g.Milestones {
		if m.State != Done {
			continue
		}
		var shas, minutes []string
		for _, w := range wordRe.FindAllString(strings.ToLower(m.Refs), -1) {
			if shaRe.MatchString(w) && letterRe.MatchString(w) {
				shas = append(shas, w)
			}
		}
		for _, b := range busRefRe.FindAllStringSubmatch(m.Refs, -1) {
			if t, err := time.ParseInLocation("2006-01-02T15:04", b[1], time.Local); err == nil {
				minutes = append(minutes, t.UTC().Format("2006-01-02T15:04"))
			}
		}
	checks:
		for _, c := range checks {
			for _, s := range shas {
				for _, w := range c.words {
					if shaRe.MatchString(w) && (strings.HasPrefix(w, s) || strings.HasPrefix(s, w)) {
						out[i] = c.id
						break checks
					}
				}
			}
			for _, mn := range minutes {
				if strings.HasPrefix(c.ts, mn) && (c.names || c.ownQA) {
					out[i] = c.id
					break checks
				}
			}
		}
	}
	return out, nil
}
