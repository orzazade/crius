package ingest

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/orzazade/crius/internal/testutil"
)

// Step 5 P1: projects.toml names each lead's status file, the one holding its GOAL block; the lead is named as it
// signs bus lines, <project>:<role>.
func TestLeadsComeFromProjectsToml(t *testing.T) {
	home := t.TempDir()
	testutil.Write(t, filepath.Join(home, "projects.toml"), `
[projects.crius]
cwd = ["/w/crius"]
leads = { build = "~/w/crius/RESUME.md", lead = "/w/crius/STATUS.md" }
[projects.axi]
cwd = ["/w/axi"]
leads = { leader = "/w/axi/STATUS.md" }
[projects.infra]
cwd = ["/w/infra"]
`)
	got, err := Leads(home)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := os.UserHomeDir()
	want := []Lead{{"axi:leader", "/w/axi/STATUS.md"}, {"crius:build", filepath.Join(h, "w/crius/RESUME.md")},
		{"crius:lead", "/w/crius/STATUS.md"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
	testutil.Write(t, filepath.Join(home, "projects.toml"), "[projects.axi]\nleads = { \"Lead One\" = \"/x\" }\n")
	if _, err := Leads(home); err == nil || !strings.Contains(err.Error(), `bad lead role "Lead One"`) {
		t.Fatal(err)
	}
}
