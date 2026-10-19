package scorecard

import (
	"compress/gzip"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// backup saves the store as the daily backup does: a VACUUM INTO copy, gzipped, as backup/record-<day>.db.gz.
func backup(t *testing.T, db *sql.DB, home, day string) {
	t.Helper()
	snap := filepath.Join(t.TempDir(), "snap.db")
	if _, err := db.Exec("vacuum into ?", snap); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(snap)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(home, "backup", "record-"+day+".db.gz"))
	if err != nil {
		t.Fatal(err)
	}
	zw := gzip.NewWriter(f)
	if _, err := zw.Write(b); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func restoreTest(t *testing.T, home, now string) *Restore {
	t.Helper()
	r, err := RestoreTest(home, at(now))
	if err != nil {
		t.Fatal(err)
	}
	saved, err := ReadRestore(home)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(saved, r) {
		t.Fatalf("saved %+v\nran %+v", saved, r)
	}
	return r
}

// writeWeekA writes week A (ends 09-17) as the weekly job would the day after it ended.
func writeWeekA(t *testing.T, db *sql.DB, home string) string {
	t.Helper()
	path, _, err := Write(db, filepath.Join(home, "state", "scorecards"),
		build(t, db, "2026-09-12T00:00:00Z", "2026-09-18T06:00:00Z"), at("2026-09-18T06:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// Step 6 P5 (design §3c): the newest backup regenerates a matured week the same as its file; a changed file fails.
func TestRestorePassesAndAChangedFileFails(t *testing.T) {
	db, home := fixture(t)
	md := writeWeekA(t, db, home)
	backup(t, db, home, "2026-10-01")
	backup(t, db, home, "2026-10-02")
	r := restoreTest(t, home, "2026-10-02T04:00:00Z")
	if r.Status != "pass" || r.Backup != "record-2026-10-02.db.gz" ||
		!reflect.DeepEqual(r.Weeks, []RestoreWeek{{"2026-09-17", "same"}}) {
		t.Fatalf("%+v", r)
	}

	js := strings.TrimSuffix(md, ".md") + ".json"
	orig, err := os.ReadFile(js)
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(orig), `"Code": 1,`, `"Code": 2,`, 1)
	if changed == string(orig) {
		t.Fatalf("no Code 1 in %s", orig)
	}
	if err := os.WriteFile(js, []byte(changed), 0o600); err != nil {
		t.Fatal(err)
	}
	r = restoreTest(t, home, "2026-10-02T04:00:00Z")
	if r.Status != "fail" || len(r.Weeks) != 1 || !strings.Contains(r.Weeks[0].Result, "code 2 -> 1") ||
		!strings.Contains(r.Text, "2026-09-17") {
		t.Fatalf("a changed number: %+v", r)
	}

	if err := os.WriteFile(js, orig, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(md, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("edited by hand\n")
	f.Close()
	r = restoreTest(t, home, "2026-10-02T04:00:00Z")
	if r.Status != "fail" || len(r.Weeks) != 1 || !strings.Contains(r.Weeks[0].Result, ".md") {
		t.Fatalf("a changed .md: %+v", r)
	}
}

// A difference names the record events since the file was written, so a stale file is told apart from lost rows.
func TestRestoreNamesWhyAWeekDiffers(t *testing.T) {
	db, home := fixture(t)
	writeWeekA(t, db, home)
	revoke(t, db, "demo#1")
	backup(t, db, home, "2026-10-02")
	r := restoreTest(t, home, "2026-10-02T04:00:00Z")
	if r.Status != "fail" || len(r.Weeks) != 1 || !strings.Contains(r.Weeks[0].Result, "code 1 -> 0") ||
		!strings.Contains(r.Weeks[0].Result, "demo#1 revoked") {
		t.Fatalf("%+v", r)
	}
}

// Before a written week matures there is nothing to compare: said, not a pass.
func TestRestoreWithNoMaturedWeek(t *testing.T) {
	db, home := fixture(t)
	writeWeekA(t, db, home)
	backup(t, db, home, "2026-09-20")
	r := restoreTest(t, home, "2026-09-20T04:00:00Z")
	if r.Status != "none" || len(r.Weeks) != 0 || !strings.Contains(r.Text, "no matured week") {
		t.Fatalf("%+v", r)
	}
	// a week written under another definition is listed, not compared
	js := filepath.Join(home, "state", "scorecards", "2026-09-17.json")
	b, err := os.ReadFile(js)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(js, []byte(strings.Replace(string(b), `"Def": "`+Def+`"`, `"Def": "v1"`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	r = restoreTest(t, home, "2026-10-02T04:00:00Z")
	if r.Status != "none" || len(r.Weeks) != 1 || !strings.Contains(r.Weeks[0].Result, "not compared") {
		t.Fatalf("old def: %+v", r)
	}
}

func TestRestoreWithoutBackupFailsAndNeverRunIsNil(t *testing.T) {
	db, home := fixture(t)
	writeWeekA(t, db, home)
	if r, err := ReadRestore(home); r != nil || err != nil {
		t.Fatalf("never run: %+v %v", r, err)
	}
	r := restoreTest(t, home, "2026-10-02T04:00:00Z")
	if r.Status != "fail" || !strings.Contains(r.Text, "no backup") {
		t.Fatalf("%+v", r)
	}
	// a backup that is not a store fails too
	if err := os.WriteFile(filepath.Join(home, "backup", "record-2026-10-02.db.gz"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if r := restoreTest(t, home, "2026-10-02T04:00:00Z"); r.Status != "fail" {
		t.Fatalf("bad backup: %+v", r)
	}
}

// Review of P5: a matured week whose .json is missing or empty fails, never drops out; a .md changed by hand is
// not made the trusted copy by a later revision (Write refuses it).
func TestRestoreFailsOnMissingOrEmptyNumbersAndWriteRefusesAnEditedMD(t *testing.T) {
	for _, c := range []string{"missing", "{}", "null"} {
		db, home := fixture(t)
		md := writeWeekA(t, db, home)
		js := strings.TrimSuffix(md, ".md") + ".json"
		if c == "missing" {
			os.Remove(js)
		} else if err := os.WriteFile(js, []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
		backup(t, db, home, "2026-10-02")
		if r := restoreTest(t, home, "2026-10-02T04:00:00Z"); r.Status != "fail" || !strings.Contains(r.Text, "2026-09-17") {
			t.Fatalf("%s .json: %+v", c, r)
		}
	}

	db, home := fixture(t)
	md := writeWeekA(t, db, home)
	f, err := os.OpenFile(md, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("edited by hand\n")
	f.Close()
	revoke(t, db, "demo#1")
	late := "2026-10-02T04:00:00Z"
	if _, _, err := Write(db, filepath.Join(home, "state", "scorecards"), build(t, db, "2026-09-12T00:00:00Z", late),
		at(late)); err == nil || !strings.Contains(err.Error(), "changed since") {
		t.Fatalf("an edited .md was revised: %v", err)
	}
}
