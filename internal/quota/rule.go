package quota

import "time"

// ruleChange is one row of enterprise_rule: from since, the project may (ok) or may not run on the enterprise account.
type ruleChange struct {
	since time.Time
	ok    bool
}

// rules is each project's enterprise rule over time, oldest first, with the rule of projects.toml today for a project
// it has no rows for (crius:lead DECISION 11-08: a session is judged by the rule as it stood when it ran).
type rules struct {
	now     map[string]bool
	history map[string][]ruleChange
}

func loadRules(db Querier, now map[string]bool) (rules, error) {
	r := rules{now: now, history: map[string][]ruleChange{}}
	rows, err := db.Query("select project, ok, since from enterprise_rule order by project, since")
	if err != nil {
		return r, err
	}
	defer rows.Close()
	for rows.Next() {
		var p, since string
		var ok bool
		if err := rows.Scan(&p, &ok, &since); err != nil {
			return r, err
		}
		t, err := parseTS(since)
		if err != nil {
			return r, err
		}
		r.history[p] = append(r.history[p], ruleChange{t, ok})
	}
	return r, rows.Err()
}

// okDuring says whether the project may run on the enterprise account for the whole of [from, to]; a zero to is open.
func (r rules) okDuring(project string, from, to time.Time) bool {
	h := r.history[project]
	if len(h) == 0 {
		return r.now[project]
	}
	ok := h[0].ok // before its first row (from the epoch), its first rule
	for _, c := range h {
		if !c.since.After(from) {
			ok = c.ok
		} else if (to.IsZero() || !c.since.After(to)) && !c.ok {
			return false
		}
	}
	return ok
}
