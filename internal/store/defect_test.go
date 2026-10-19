package store

import (
	"database/sql"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// acceptedFixture: demo#1 accepted on commit A.
func acceptedFixture(t *testing.T) *sql.DB {
	t.Helper()
	db, ev := acceptFixture(t)
	if _, err := RecordAccept(db, AcceptInput{Item: "demo#1", Revision: "abc1234",
		Checks: []string{"1=" + ev["ciA"], "2=" + ev["revA"]}, Review: ev["revA"], By: "crius"}); err != nil {
		t.Fatal(err)
	}
	return db
}

func defect(t *testing.T, db *sql.DB, item, class string) string {
	t.Helper()
	out, err := RecordDefect(db, DefectInput{Item: item, Title: "mail sent twice", FoundBy: "us", Severity: "S2",
		Class: class})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func count(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// §9: a revoke keeps the acceptance row (state, reason, defect) and opens a fix item with its own criteria.
func TestRevokeKeepsTheRowAndOpensAFixItem(t *testing.T) {
	db := acceptedFixture(t)
	if out := defect(t, db, "demo#1", "stale context"); !strings.HasPrefix(out, "defect D-1 on demo#1") {
		t.Fatal(out)
	}
	if rev := count(t, db, "select count(*) from defect where id = 1 and revision = ?", shaA); rev != 1 {
		t.Fatal("the defect is not on the accepted revision")
	}
	for name, in := range map[string]RevokeInput{
		"no criteria":    {Item: "demo#1", Defect: "D-1", Reason: "r", By: "crius"},
		"unknown defect": {Item: "demo#1", Defect: "D-9", Reason: "r", By: "crius", Criteria: []string{"x"}},
		"bad defect id":  {Item: "demo#1", Defect: "1", Reason: "r", By: "crius", Criteria: []string{"x"}},
		"bad by":         {Item: "demo#1", Defect: "D-1", Reason: "r", By: "lead", Criteria: []string{"x"}},
		"no reason":      {Item: "demo#1", Defect: "D-1", By: "crius", Criteria: []string{"x"}},
	} {
		if _, err := RecordRevoke(db, in); !IsRefused(err) {
			t.Errorf("%s: want refused, got %v", name, err)
		}
	}
	out, err := RecordRevoke(db, RevokeInput{Item: "demo#1", Defect: "D-1", Reason: "sends twice", By: "crius",
		Criteria: []string{"regression test red before the fix"}})
	if err != nil || !strings.Contains(out, "opened fix demo#2") {
		t.Fatal(out, err)
	}
	var state, reason, by, def, fix string
	var revokedTS sql.NullString
	if err := db.QueryRow("select state, revoke_reason, revoked_by, defect, fix_item, revoked_ts from acceptance").
		Scan(&state, &reason, &by, &def, &fix, &revokedTS); err != nil {
		t.Fatal(err)
	}
	if acceptRows(t, db) != 1 || state != "revoked" || reason != "sends twice" || by != "crius" || def != "D-1" ||
		fix != "demo#2" || !revokedTS.Valid {
		t.Fatal(state, reason, by, def, fix, revokedTS)
	}
	if s := itemState(t, db, "demo#1"); s != "revoked" {
		t.Fatal(s)
	}
	var parent, criteria string
	if err := db.QueryRow("select parent, criteria from item where id = 'demo#2' and state = 'open'").
		Scan(&parent, &criteria); err != nil {
		t.Fatal(err)
	}
	var crit []string
	json.Unmarshal([]byte(criteria), &crit)
	if parent != "demo#1" || !reflect.DeepEqual(crit, []string{"regression test red before the fix"}) {
		t.Fatal(parent, crit)
	}
	// Revoked once, the item is not accepted: a second revoke and a revoke of an open item are refused.
	for _, item := range []string{"demo#1", "demo#2"} {
		if _, err := RecordRevoke(db, RevokeInput{Item: item, Defect: "D-1", Reason: "r", By: "crius",
			Criteria: []string{"x"}}); !IsRefused(err) {
			t.Errorf("revoke %s: want refused, got %v", item, err)
		}
	}
	if n := count(t, db, "select count(*) from event where kind in ('defect', 'revoked')"); n != 2 {
		t.Fatal("events:", n)
	}
}

// A defect on another item cannot revoke this one.
func TestRevokeRefusesADefectOfAnotherItem(t *testing.T) {
	db := acceptedFixture(t)
	if _, err := RecordItem(db, "demo", "other", "code", []string{"c"}, "", false); err != nil {
		t.Fatal(err)
	}
	defect(t, db, "demo#2", "bad data")
	if _, err := RecordRevoke(db, RevokeInput{Item: "demo#1", Defect: "D-1", Reason: "r", By: "crius",
		Criteria: []string{"x"}}); !IsRefused(err) {
		t.Fatal(err)
	}
	if s := itemState(t, db, "demo#1"); s != "accepted" {
		t.Fatal(s)
	}
}

// §7b: the second confirmed defect of one class opens a guard item; a third joins the open guard; once the guard
// is accepted, the next one opens a new guard.
func TestSecondDefectOfAClassOpensAGuard(t *testing.T) {
	db := acceptedFixture(t)
	for name, in := range map[string]DefectInput{
		"class":    {Item: "demo#1", Title: "t", FoundBy: "us", Severity: "S2", Class: "typo"},
		"severity": {Item: "demo#1", Title: "t", FoundBy: "us", Severity: "S4", Class: "bad data"},
		"found by": {Item: "demo#1", Title: "t", FoundBy: "lead", Severity: "S2", Class: "bad data"},
		"item":     {Item: "demo#9", Title: "t", FoundBy: "us", Severity: "S2", Class: "bad data"},
		"title":    {Item: "demo#1", Title: " ", FoundBy: "us", Severity: "S2", Class: "bad data"},
	} {
		if _, err := RecordDefect(db, in); !IsRefused(err) {
			t.Errorf("%s: want refused, got %v", name, err)
		}
	}
	if out := defect(t, db, "demo#1", "stale context"); strings.Contains(out, "guard") {
		t.Fatal(out)
	}
	if out := defect(t, db, "demo#1", "missing test"); strings.Contains(out, "guard") {
		t.Fatal(out)
	}
	if out := defect(t, db, "demo#1", "stale context"); !strings.Contains(out, "opened guard demo#2") {
		t.Fatal(out)
	}
	var title, criteria string
	if err := db.QueryRow("select title, criteria from item where id = 'demo#2'").Scan(&title, &criteria); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(title, "stale context") || !strings.Contains(criteria, "D-1") || !strings.Contains(criteria, "D-3") {
		t.Fatal(title, criteria)
	}
	if out := defect(t, db, "demo#1", "stale context"); !strings.Contains(out, "guard demo#2") ||
		strings.Contains(out, "opened") {
		t.Fatal(out)
	}
	if n := count(t, db, "select count(*) from defect where guard_item = 'demo#2'"); n != 3 {
		t.Fatal("covered by the guard:", n)
	}
	if _, err := db.Exec("update item set state = 'accepted' where id = 'demo#2'"); err != nil {
		t.Fatal(err)
	}
	if out := defect(t, db, "demo#1", "stale context"); !strings.Contains(out, "opened guard demo#3") {
		t.Fatal(out)
	}
	if n := count(t, db, "select count(*) from item"); n != 3 {
		t.Fatal("items:", n)
	}
}

// §9: a correction changes the view, never the target row; a second correction starts from the corrected value.
func TestCorrectionLeavesTheRowAndChangesTheView(t *testing.T) {
	db := acceptedFixture(t)
	defect(t, db, "demo#1", "stale context")
	for name, in := range map[string]CorrectInput{
		"field":       {Target: "demo#1", Field: "state", Value: "open", Reason: "r", By: "crius"},
		"target":      {Target: "demo#9", Field: "project", Value: "axi", Reason: "r", By: "crius"},
		"shape":       {Target: "event:1", Field: "project", Value: "axi", Reason: "r", By: "crius"},
		"kind value":  {Target: "demo#1", Field: "kind", Value: "poetry", Reason: "r", By: "crius"},
		"same value":  {Target: "demo#1", Field: "project", Value: "demo", Reason: "r", By: "crius"},
		"class value": {Target: "D-1", Field: "cause_class", Value: "typo", Reason: "r", By: "crius"},
		"no reason":   {Target: "demo#1", Field: "project", Value: "axi", By: "crius"},
		"bad by":      {Target: "demo#1", Field: "project", Value: "axi", Reason: "r", By: "lead"},
	} {
		if _, err := RecordCorrect(db, in); !IsRefused(err) {
			t.Errorf("%s: want refused, got %v", name, err)
		}
	}
	for _, in := range []CorrectInput{
		{Target: "demo#1", Field: "project", Value: "axi", Reason: "wrong project", By: "crius"},
		{Target: "demo#1", Field: "project", Value: "quotes", Reason: "still wrong", By: "owner"},
		{Target: "D-1", Field: "cause_class", Value: "missing test", Reason: "misfiled", By: "crius"},
	} {
		if _, err := RecordCorrect(db, in); err != nil {
			t.Fatal(err)
		}
	}
	var row, view, defRow, defView string
	if err := db.QueryRow("select i.project, v.project, d.cause_class, dv.cause_class from item i, item_v v, defect d,"+
		" defect_v dv where i.id = 'demo#1' and v.id = i.id and d.id = 1 and dv.id = 1").
		Scan(&row, &view, &defRow, &defView); err != nil {
		t.Fatal(err)
	}
	if row != "demo" || view != "quotes" || defRow != "stale context" || defView != "missing test" {
		t.Fatal(row, view, defRow, defView)
	}
	var olds []string
	rows, err := db.Query("select old_value from correction where field = 'project' order by id")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var o string
		rows.Scan(&o)
		olds = append(olds, o)
	}
	rows.Close()
	if !reflect.DeepEqual(olds, []string{"demo", "axi"}) {
		t.Fatal(olds)
	}
	if n := count(t, db, "select count(*) from event where kind = 'correction'"); n != 3 {
		t.Fatal("events:", n)
	}
}
