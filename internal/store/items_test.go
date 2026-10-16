package store

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/orzazade/crius/internal/testutil"
)

func openStore(t *testing.T) (root, home string, db *sql.DB) {
	root, home = testutil.Temp(t)
	initHome(t, home)
	db, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return root, home, db
}

func TestItemIDsCountPerProjectAndKeepCriteria(t *testing.T) {
	_, _, db := openStore(t)
	a, err1 := RecordItem(db, "demo", "price fix", "code", []string{"CI pass", "codex review"}, "", false)
	b, err2 := RecordItem(db, "demo", "child", "code", []string{"tests"}, a, false)
	c, err3 := RecordItem(db, "other", "x", "research", []string{"owner reads it"}, "", false)
	if err1 != nil || err2 != nil || err3 != nil {
		t.Fatal(err1, err2, err3)
	}
	if [3]string{a, b, c} != [3]string{"demo#1", "demo#2", "other#1"} {
		t.Fatal(a, b, c)
	}
	var crit, parent, state string
	if err := db.QueryRow("select criteria, parent, state from item where id=?", b).Scan(&crit, &parent, &state); err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := json.Unmarshal([]byte(crit), &got); err != nil || !reflect.DeepEqual(got, []string{"tests"}) {
		t.Fatal(crit, err)
	}
	if parent != "demo#1" || state != "open" {
		t.Fatal(parent, state)
	}
}

func TestItemNeedsCriteriaKnownKindAndKnownParent(t *testing.T) {
	_, _, db := openStore(t)
	cases := []struct {
		project, kind, parent string
		criteria              []string
	}{
		{"demo", "code", "", nil},
		{"demo", "size-l", "", []string{"t"}},
		{"demo", "code", "demo#9", []string{"t"}},
		{"demo\n", "code", "", []string{"t"}},
	}
	for _, c := range cases {
		if _, err := RecordItem(db, c.project, "x", c.kind, c.criteria, c.parent, false); !IsRefused(err) {
			t.Errorf("%+v: %v", c, err)
		}
	}
}

func TestPrivateItemIDInTrackedFileFailsTheLeakTest(t *testing.T) {
	root, home, db := openStore(t)
	if _, err := RecordItem(db, "demo", "x", "code", []string{"t"}, "", false); err != nil {
		t.Fatal(err)
	}
	repo := testutil.GitRepo(t, filepath.Join(root, "checkout"))
	testutil.Write(t, filepath.Join(repo, "NOTES.md"), "closes demo#1\n")
	testutil.CommitAll(t, repo)
	if p := LeakProblems(home, repo); !anyContains(p, "demo#1") {
		t.Fatal(p)
	}
}

func TestRecordDBIsPrivate(t *testing.T) {
	_, home, _ := openStore(t)
	if fi, err := statMode(filepath.Join(home, "state", "record.db")); err != nil || fi != 0o600 {
		t.Fatal(fi, err)
	}
}

func statMode(path string) (os.FileMode, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return fi.Mode().Perm(), nil
}

func TestOpenKeepsTheStoreInsideAHomeWithURICharacters(t *testing.T) {
	root, _ := testutil.Temp(t)
	home := filepath.Join(root, "pri#v?a%20te")
	initHome(t, home)
	db, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := RecordItem(db, "demo", "x", "code", []string{"t"}, "", false); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(home, "state", "record.db"))
	if err != nil || fi.Size() == 0 {
		t.Fatalf("record.db not used: %v", err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 1 {
		t.Fatalf("files outside the home: %v", entries)
	}
}
