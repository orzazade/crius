package ingest

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/orzazade/crius/internal/testutil"
)

// Step 4 P4: health compares coverage with what a run should read, listed by the ingest's own rules.
func TestExpectedListsWhatARunShouldRead(t *testing.T) {
	root, home := testutil.Temp(t)
	tx := filepath.Join(root, "tx")
	repo := filepath.Join(root, "repo")
	team := filepath.Join(root, "team.log")
	testutil.Write(t, filepath.Join(home, "projects.toml"), "[transcripts]\npersonal = \""+tx+"\"\n"+
		"[projects.demo]\npersona = \"personal\"\naccounts = [\"max\"]\ncwd = []\nrepos = [\""+repo+"\"]\nteam_logs = []\nartifact_dirs = []\n"+
		"[projects.lab]\npersona = \"personal\"\naccounts = [\"max\"]\ncwd = []\nrepos = []\nteam_logs = [\""+team+"\"]\nartifact_dirs = []\n"+
		"[projects.idle]\npersona = \"personal\"\naccounts = [\"max\"]\ncwd = []\nrepos = []\nteam_logs = []\nartifact_dirs = []\n")
	testutil.Write(t, filepath.Join(tx, "-p", "s1.jsonl"), "{}\n")
	testutil.Write(t, filepath.Join(tx, "-p", "s1", "subagents", "a.jsonl"), "{}\n")
	testutil.Write(t, filepath.Join(tx, "-private-tmp-x", "s9.jsonl"), "{}\n")
	testutil.Write(t, filepath.Join(home, "bus", "demo.log"), "x\n")
	testutil.Write(t, filepath.Join(home, "bus", "idle.log"), "")
	exp, err := Expected(home)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{
		"transcript": {filepath.Join(tx, "-p", "s1", "subagents", "a.jsonl"), filepath.Join(tx, "-p", "s1.jsonl")},
		"bus":        {filepath.Join(home, "bus", "demo.log"), filepath.Join(home, "bus", "idle.log")},
		"team":       {team},
		"git":        {repo},
	}
	if !reflect.DeepEqual(exp.Files, want) || !reflect.DeepEqual(exp.Teams, []string{"demo", "lab"}) {
		t.Fatalf("%v\n%v", exp.Files, exp.Teams)
	}
}
