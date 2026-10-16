package digest

import (
	"database/sql"
	"regexp"
	"strings"
	"time"
)

var (
	shaRe    = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	busRefRe = regexp.MustCompile(`^bus (\d{4}-\d{2}-\d{2}T\d{2}:\d{2})$`)
	passRe   = regexp.MustCompile(`\bPASS\b`)
)

// Verified gives, per milestone, the id of the bus line that checked it, 0 for none (Crius 10-17: a % counts only
// with a Crius or QA check on the bus). A check is a line from Crius (crius:lead) or a QA role that says PASS. A
// done milestone is verified when a check names one of its refs: a sha (7 or more hex, matched on its first 7), or
// "bus <local YYYY-MM-DDTHH:MM>", the check line itself. Other refs (run ids, PRs) cannot be matched, so a milestone
// with only those stays claimed. Milestones not done are never verified.
func Verified(db *sql.DB, g *Goal) ([]int64, error) {
	rows, err := db.Query("select id, ts, text from event where ((project = 'crius' and actor = 'lead') or actor like 'qa%')" +
		" and text like '%PASS%' order by id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type check struct {
		id       int64
		ts, text string
	}
	var checks []check
	for rows.Next() {
		var c check
		if err := rows.Scan(&c.id, &c.ts, &c.text); err != nil {
			return nil, err
		}
		if passRe.MatchString(c.text) {
			c.text = strings.ToLower(c.text)
			checks = append(checks, c)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]int64, len(g.Milestones))
	for i, m := range g.Milestones {
		if m.State != Done {
			continue
		}
	refs:
		for _, ref := range strings.Split(m.Refs, ",") {
			ref = strings.TrimSpace(ref)
			minute := ""
			if b := busRefRe.FindStringSubmatch(ref); b != nil {
				t, err := time.ParseInLocation("2006-01-02T15:04", b[1], time.Local)
				if err != nil {
					continue
				}
				minute = t.UTC().Format("2006-01-02T15:04")
			}
			for _, c := range checks {
				if minute != "" && strings.HasPrefix(c.ts, minute) || shaRe.MatchString(ref) && strings.Contains(c.text, ref[:7]) {
					out[i] = c.id
					break refs
				}
			}
		}
	}
	return out, nil
}
