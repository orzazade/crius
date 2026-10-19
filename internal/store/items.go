package store

// Items `<project>#<n>`: one owner outcome or a child task, with acceptance criteria fixed at open (design §7).

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

var Kinds = []string{"code", "research", "falcon", "other"}

func RecordItem(db *sql.DB, project, title, kind string, criteria []string, parent string, taint bool) (string, error) {
	if !Slug.MatchString(project) {
		return "", refused("bad project slug")
	}
	if !slices.Contains(Kinds, kind) {
		return "", refused("kind must be one of %v", Kinds)
	}
	title = strings.TrimSpace(title)
	if title == "" || strings.Contains(title, "\n") || utf8.RuneCountInString(title) > MaxLen {
		return "", refused("title must be one line of 1-%d chars", MaxLen)
	}
	critJSON, err := criteriaJSON(criteria)
	if err != nil {
		return "", err
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "begin immediate"); err != nil {
		return "", err
	}
	done := false
	defer func() {
		if !done {
			conn.ExecContext(ctx, "rollback")
		}
	}()
	id, err := openItem(ctx, conn, project, title, kind, critJSON, parent, taint)
	if err != nil {
		return "", err
	}
	if _, err := conn.ExecContext(ctx, "commit"); err != nil {
		return "", err
	}
	done = true
	return id, nil
}

// criteriaJSON drops blank criteria; an item with none is refused.
func criteriaJSON(criteria []string) ([]byte, error) {
	var crit []string
	for _, c := range criteria {
		if c = strings.TrimSpace(c); c != "" {
			crit = append(crit, c)
		}
	}
	if len(crit) == 0 {
		return nil, refused("an item needs its acceptance criteria when it opens")
	}
	return json.Marshal(crit)
}

// openItem inserts an item inside the caller's transaction; the caller has checked the fields.
func openItem(ctx context.Context, conn *sql.Conn, project, title, kind string, critJSON []byte, parent string,
	taint bool) (string, error) {
	if parent != "" {
		var one int
		switch err := conn.QueryRowContext(ctx, "select 1 from item where id=?", parent).Scan(&one); err {
		case nil:
		case sql.ErrNoRows:
			return "", refused("parent %s is not a known item", parent)
		default:
			return "", err
		}
	}
	var n int
	if err := conn.QueryRowContext(ctx, "select coalesce(max(n), 0) + 1 from item where project=?", project).Scan(&n); err != nil {
		return "", err
	}
	id := fmt.Sprintf("%s#%d", project, n)
	var parentVal any
	if parent != "" {
		parentVal = parent
	}
	taintVal := 0
	if taint {
		taintVal = 1
	}
	if _, err := conn.ExecContext(ctx, "insert into item (id, project, n, parent, kind, title, criteria, opened, taint) "+
		"values (?,?,?,?,?,?,?,?,?)", id, project, n, parentVal, kind, title, string(critJSON),
		time.Now().UTC().Format("2006-01-02T15:04:05Z"), taintVal); err != nil {
		return "", err
	}
	return id, nil
}
