package store

// Mistakes after the fact (design §7b): `record defect`, `record revoke` and `record correct`. Nothing is rewritten:
// a revoke keeps the acceptance row, a correction is a new row that the views item_v and defect_v apply.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// CauseClasses is the short list of §7b; a defect names one, so two of a class are counted as one class.
var CauseClasses = []string{"missing test", "wrong assumption", "stale context", "silent failure", "misread request",
	"harness or tool", "review gap", "bad data"}

var (
	foundBy    = []string{"us", "owner", "others"}
	severities = []string{"S1", "S2", "S3"}
	defectRe   = regexp.MustCompile(`^D-([1-9][0-9]*)$`)
	itemIDRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*#[1-9][0-9]*$`)
)

type DefectInput struct{ Item, Title, FoundBy, Severity, Class string }

type RevokeInput struct {
	Item, Defect, Reason, By string
	Criteria                 []string // the fix item's
}

type CorrectInput struct{ Target, Field, Value, Reason, By string }

func oneOf(name, v string, allowed []string) error {
	if !slices.Contains(allowed, v) {
		return refused("%s must be one of %s, got %q", name, strings.Join(allowed, ", "), v)
	}
	return nil
}

// writeTx runs f in one immediate transaction: all of its writes land, or none.
func writeTx(db *sql.DB, f func(ctx context.Context, conn *sql.Conn) (string, error)) (string, error) {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "begin immediate"); err != nil {
		return "", err
	}
	out, err := f(ctx, conn)
	if err == nil {
		_, err = conn.ExecContext(ctx, "commit")
	}
	if err != nil {
		conn.ExecContext(ctx, "rollback")
		return "", err
	}
	return out, nil
}

func addEvent(ctx context.Context, conn *sql.Conn, ts, project, kind, actor, item string, refs map[string][]string,
	text, key string) error {
	rj, err := json.Marshal(refs)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "insert into event (ts, project, kind, actor, item, refs, text, verified, source_key)"+
		" values (?,?,?,?,?,?,?,1,?)", ts, project, kind, actor, item, string(rj), truncate(text, MaxLen), key)
	return err
}

func now() string { return time.Now().UTC().Format("2006-01-02T15:04:05Z") }

// RecordDefect records a confirmed defect on an item, on its accepted revision if it has one. The second defect of a
// class opens a guard item (a check that goes red on every defect of the class so far); while that guard is open,
// later ones join it; once it is closed, the next one opens a new guard.
func RecordDefect(db *sql.DB, in DefectInput) (string, error) {
	title, err := oneLine("--title", in.Title)
	for _, err := range []error{err, oneOf("--found-by", in.FoundBy, foundBy),
		oneOf("--severity", in.Severity, severities), oneOf("--class", in.Class, CauseClasses)} {
		if err != nil {
			return "", err
		}
	}
	return writeTx(db, func(ctx context.Context, conn *sql.Conn) (string, error) {
		var project string
		switch err := conn.QueryRowContext(ctx, "select project from item_v where id = ?", in.Item).Scan(&project); err {
		case nil:
		case sql.ErrNoRows:
			return "", refused("%s is not a known item", in.Item)
		default:
			return "", err
		}
		var rev sql.NullString
		if err := conn.QueryRowContext(ctx, "select revision from acceptance where item = ? and state = 'accepted'"+
			" order by id desc limit 1", in.Item).Scan(&rev); err != nil && err != sql.ErrNoRows {
			return "", err
		}
		ts := now()
		res, err := conn.ExecContext(ctx, "insert into defect (item, revision, title, found_by, found_ts, severity,"+
			" cause_class) values (?,?,?,?,?,?,?)", in.Item, rev, title, in.FoundBy, ts,
			in.Severity, in.Class)
		if err != nil {
			return "", err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return "", err
		}
		did := fmt.Sprint("D-", id)
		guard, opened, err := guardFor(ctx, conn, in.Class, id)
		if err != nil {
			return "", err
		}
		on := in.Item
		if rev.Valid {
			on += " (" + short(rev.String) + ")"
		}
		text := fmt.Sprintf("defect %s on %s, %s %s, found by %s", did, on, in.Severity, in.Class, in.FoundBy)
		items := []string{in.Item}
		switch {
		case opened:
			text += "; opened guard " + guard
		case guard != "":
			text += "; guard " + guard
		}
		if guard != "" {
			items = append(items, guard)
		}
		text += ": " + title
		if err := addEvent(ctx, conn, ts, project, "defect", in.FoundBy, in.Item,
			map[string][]string{"item": items, "defect": {did}}, text, "defect:"+did); err != nil {
			return "", err
		}
		return text, nil
	})
}

// guardFor links defect id to the open guard of its class, or opens one when the class has an earlier defect. It
// returns the guard ("" for the first defect of a class) and whether it opened it.
func guardFor(ctx context.Context, conn *sql.Conn, class string, id int64) (string, bool, error) {
	var guard string
	err := conn.QueryRowContext(ctx, "select d.guard_item from defect_v d join item g on g.id = d.guard_item"+
		" where d.cause_class = ? and g.state = 'open' order by d.id desc limit 1", class).Scan(&guard)
	if err != nil && err != sql.ErrNoRows {
		return "", false, err
	}
	opened := false
	if guard == "" {
		rows, err := conn.QueryContext(ctx, "select d.id, i.project from defect_v d join item_v i on i.id = d.item"+
			" where d.cause_class = ? order by d.id", class)
		if err != nil {
			return "", false, err
		}
		var ids []string
		projects := map[string]bool{}
		for rows.Next() {
			var n int64
			var p string
			if err := rows.Scan(&n, &p); err != nil {
				rows.Close()
				return "", false, err
			}
			ids, projects[p] = append(ids, fmt.Sprint("D-", n)), true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return "", false, err
		}
		if len(ids) < 2 {
			return "", false, nil
		}
		// A class repeated in one project is that project's guard; across projects it is Crius's.
		project := "crius"
		if len(projects) == 1 {
			for p := range projects {
				project = p
			}
		}
		crit, err := json.Marshal([]string{"a test, hook or health check goes red on " + strings.Join(ids, ", "),
			"a guard that changes config or hooks is approved by the owner"})
		if err != nil {
			return "", false, err
		}
		if guard, err = openItem(ctx, conn, project, truncate(fmt.Sprintf("guard: %s (%s)", class,
			strings.Join(ids, ", ")), MaxLen), "code", crit, "", false); err != nil {
			return "", false, err
		}
		opened = true
		// The defects no guard covers yet; this one is among them.
		if _, err := conn.ExecContext(ctx, "update defect set guard_item = ? where guard_item is null and id in"+
			" (select id from defect_v where cause_class = ?)", guard, class); err != nil {
			return "", false, err
		}
	}
	_, err = conn.ExecContext(ctx, "update defect set guard_item = ? where id = ?", guard, id)
	return guard, opened, err
}

// RecordRevoke revokes an item's acceptance because of one of its defects: the row stays, with state revoked, the
// reason and the defect, and a fix item opens under the same outcome with its own criteria.
func RecordRevoke(db *sql.DB, in RevokeInput) (string, error) {
	if in.By != "crius" && in.By != "owner" {
		return "", refused("--by must be crius or owner, got %q", in.By)
	}
	reason, err := oneLine("--reason", in.Reason)
	if err != nil {
		return "", err
	}
	m := defectRe.FindStringSubmatch(in.Defect)
	if m == nil {
		return "", refused("--defect must be D-<n>, got %q", in.Defect)
	}
	crit, err := criteriaJSON(in.Criteria)
	if err != nil {
		return "", refused("--criteria: the fix item needs its acceptance criteria")
	}
	return writeTx(db, func(ctx context.Context, conn *sql.Conn) (string, error) {
		var project, kind, state string
		var parent sql.NullString
		var taint int
		switch err := conn.QueryRowContext(ctx, "select project, kind, state, parent, taint from item_v where id = ?",
			in.Item).Scan(&project, &kind, &state, &parent, &taint); err {
		case nil:
		case sql.ErrNoRows:
			return "", refused("%s is not a known item", in.Item)
		default:
			return "", err
		}
		if state != "accepted" {
			return "", refused("%s is %s, not accepted", in.Item, state)
		}
		var accID int64
		var rev string
		if err := conn.QueryRowContext(ctx, "select id, revision from acceptance where item = ? and state = 'accepted'"+
			" order by id desc limit 1", in.Item).Scan(&accID, &rev); err != nil {
			return "", fmt.Errorf("%s is accepted but has no accepted acceptance row: %w", in.Item, err)
		}
		var defItem, title string
		switch err := conn.QueryRowContext(ctx, "select item, title from defect_v where id = ?", m[1]).
			Scan(&defItem, &title); err {
		case nil:
		case sql.ErrNoRows:
			return "", refused("%s is not a known defect", in.Defect)
		default:
			return "", err
		}
		if defItem != in.Item {
			return "", refused("%s is a defect of %s, not of %s", in.Defect, defItem, in.Item)
		}
		outcome := in.Item // the fix is a child of the owner outcome: the item's parent, or the item itself
		if parent.Valid && parent.String != "" {
			outcome = parent.String
		}
		fix, err := openItem(ctx, conn, project, truncate("fix "+in.Item+": "+title, MaxLen), kind, crit, outcome,
			taint != 0)
		if err != nil {
			return "", err
		}
		ts := now()
		if _, err := conn.ExecContext(ctx, "update acceptance set state = 'revoked', revoked_ts = ?, revoked_by = ?,"+
			" revoke_reason = ?, defect = ?, fix_item = ? where id = ?", ts, in.By, reason,
			in.Defect, fix, accID); err != nil {
			return "", err
		}
		if _, err := conn.ExecContext(ctx, "update item set state = 'revoked' where id = ?", in.Item); err != nil {
			return "", err
		}
		text := fmt.Sprintf("revoked %s on %s (%s, by %s); opened fix %s: %s", in.Item, short(rev), in.Defect, in.By,
			fix, reason)
		if err := addEvent(ctx, conn, ts, project, "revoked", in.By, in.Item, map[string][]string{
			"item": {in.Item, fix}, "defect": {in.Defect}, "sha": {rev}}, text, fmt.Sprint("revoke:", accID)); err != nil {
			return "", err
		}
		return text, nil
	})
}

// correctable is every field a correction may change, per table, with the check on its new value. The views item_v
// and defect_v are built from it: such a field reads its newest correction, else the row's own value.
var correctable = []struct {
	table, idOf, itemOf string // idOf: a row's id as a correction names it; itemOf: the item it belongs to
	target              *regexp.Regexp
	fields        map[string]func(ctx context.Context, conn *sql.Conn, target, v string) error
}{
	{"item", "item.id", "item.id", itemIDRe, map[string]func(context.Context, *sql.Conn, string, string) error{
		"project": func(_ context.Context, _ *sql.Conn, _, v string) error {
			if !Slug.MatchString(v) {
				return refused("project must be a slug, got %q", v)
			}
			return nil
		},
		"kind":  func(_ context.Context, _ *sql.Conn, _, v string) error { return oneOf("kind", v, Kinds) },
		"title": func(_ context.Context, _ *sql.Conn, _, v string) error {
			if t, err := oneLine("title", v); err != nil || t != v || utf8.RuneCountInString(v) > MaxLen {
				return refused("title must be one line of 1-%d chars, with no space around it", MaxLen)
			}
			return nil
		},
		"parent": func(ctx context.Context, conn *sql.Conn, target, v string) error {
			if v == target {
				return refused("an item cannot be its own parent")
			}
			return knownItem(ctx, conn, v)
		},
	}},
	{"defect", "'D-' || defect.id", "defect.item", defectRe, map[string]func(context.Context, *sql.Conn, string, string) error{
		"item":        func(ctx context.Context, conn *sql.Conn, _, v string) error { return knownItem(ctx, conn, v) },
		"found_by":    func(_ context.Context, _ *sql.Conn, _, v string) error { return oneOf("found_by", v, foundBy) },
		"severity":    func(_ context.Context, _ *sql.Conn, _, v string) error { return oneOf("severity", v, severities) },
		"cause_class": func(_ context.Context, _ *sql.Conn, _, v string) error { return oneOf("cause_class", v, CauseClasses) },
	}},
}

func knownItem(ctx context.Context, conn *sql.Conn, id string) error {
	var one int
	switch err := conn.QueryRowContext(ctx, "select 1 from item where id = ?", id).Scan(&one); err {
	case nil:
		return nil
	case sql.ErrNoRows:
		return refused("%s is not a known item", id)
	default:
		return err
	}
}

// RecordCorrect records a correction of one field of an item or a defect. The row itself is never edited.
func RecordCorrect(db *sql.DB, in CorrectInput) (string, error) {
	if in.By != "crius" && in.By != "owner" {
		return "", refused("--by must be crius or owner, got %q", in.By)
	}
	reason, err := oneLine("--reason", in.Reason)
	if err != nil {
		return "", err
	}
	t := -1
	for i, c := range correctable {
		if c.target.MatchString(in.Target) {
			t = i
		}
	}
	if t < 0 {
		return "", refused("--corrects must be an item (<project>#<n>) or a defect (D-<n>), got %q", in.Target)
	}
	c := correctable[t]
	check, ok := c.fields[in.Field]
	if !ok {
		var names []string
		for f := range c.fields {
			names = append(names, f)
		}
		slices.Sort(names)
		return "", refused("a %s correction changes one of %s, not %q", c.table, strings.Join(names, ", "), in.Field)
	}
	return writeTx(db, func(ctx context.Context, conn *sql.Conn) (string, error) {
		var old sql.NullString
		var item string
		switch err := conn.QueryRowContext(ctx, "select "+c.table+"."+in.Field+", "+c.itemOf+" from "+c.table+"_v "+
			c.table+" where "+c.idOf+" = ?", in.Target).Scan(&old, &item); err {
		case nil:
		case sql.ErrNoRows:
			return "", refused("%s is not a known %s", in.Target, c.table)
		default:
			return "", err
		}
		if err := check(ctx, conn, in.Target, in.Value); err != nil {
			return "", err
		}
		if old.String == in.Value {
			return "", refused("%s %s is %q already", in.Target, in.Field, in.Value)
		}
		var project string
		if err := conn.QueryRowContext(ctx, "select project from item_v where id = ?", item).Scan(&project); err != nil {
			return "", err
		}
		ts := now()
		res, err := conn.ExecContext(ctx, "insert into correction (target, field, old_value, new_value, reason, by, ts)"+
			" values (?,?,?,?,?,?,?)", in.Target, in.Field, old, in.Value, reason, in.By, ts)
		if err != nil {
			return "", err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return "", err
		}
		cid := "C-" + strconv.FormatInt(id, 10)
		text := fmt.Sprintf("correction %s: %s %s %s -> %s (by %s)", cid, in.Target, in.Field, old.String, in.Value, in.By)
		refs := map[string][]string{"item": {item}, "correction": {cid}}
		if c.table == "defect" {
			refs["defect"] = []string{in.Target}
		}
		if err := addEvent(ctx, conn, ts, project, "correction", in.By, item, refs, text, "correct:"+cid); err != nil {
			return "", err
		}
		return text, nil
	})
}

// ensureViews (re)builds item_v and defect_v when their SQL differs from what correctable gives, e.g. after a column
// or a correctable field was added.
func ensureViews(db *sql.DB) error {
	for _, c := range correctable {
		rows, err := db.Query("select name from pragma_table_info(?) order by cid", c.table)
		if err != nil {
			return err
		}
		var cols []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				rows.Close()
				return err
			}
			if _, ok := c.fields[name]; ok {
				name = fmt.Sprintf("coalesce((select new_value from correction c where c.target = %s and c.field = '%s'"+
					" order by c.id desc limit 1), %s) as %s", c.idOf, name, name, name)
			}
			cols = append(cols, name)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		view := c.table + "_v"
		want := "CREATE VIEW " + view + " AS select " + strings.Join(cols, ", ") + " from " + c.table
		have := func() (string, error) {
			var s string
			err := db.QueryRow("select sql from sqlite_master where type = 'view' and name = ?", view).Scan(&s)
			if err == sql.ErrNoRows {
				err = nil
			}
			return s, err
		}
		if s, err := have(); err != nil {
			return err
		} else if s == want {
			continue
		}
		if _, err := writeTx(db, func(ctx context.Context, conn *sql.Conn) (string, error) {
			_, err := conn.ExecContext(ctx, "drop view if exists "+view+"; "+want)
			return "", err
		}); err != nil {
			return fmt.Errorf("building %s: %w", view, err)
		}
		if s, err := have(); err != nil || s != want {
			return fmt.Errorf("building %s: sqlite_master keeps %q, not %q (%v)", view, s, want, err)
		}
	}
	return nil
}
