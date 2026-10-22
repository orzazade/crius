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
	want := []Lead{{"axi:leader", "/w/axi/STATUS.md", ""}, {"crius:build", filepath.Join(h, "w/crius/RESUME.md"), ""},
		{"crius:lead", "/w/crius/STATUS.md", ""}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v", got)
	}
	testutil.Write(t, filepath.Join(home, "projects.toml"), "[projects.axi]\nleads = { \"Lead One\" = \"/x\" }\n")
	if _, err := Leads(home); err == nil || !strings.Contains(err.Error(), `bad lead role "Lead One"`) {
		t.Fatal(err)
	}
}

// Crius 10-19 18:00: a lead whose status file is committed elsewhere than a checkout is named by a git object,
// "git:<repo>#<rev>:<path>"; a git form without both parts is refused when the config loads, not read as a file.
func TestLeadsNameAGitObject(t *testing.T) {
	home := t.TempDir()
	testutil.Write(t, filepath.Join(home, "projects.toml"),
		"[projects.crius]\nleads = { build = \"git:~/w/crius#origin/main:docs/notes/RESUME.md\" }\n")
	got, err := Leads(home)
	h, _ := os.UserHomeDir()
	if want := []Lead{{"crius:build", "origin/main:docs/notes/RESUME.md", filepath.Join(h, "w/crius")}}; err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("%+v %v", got, err)
	}
	for _, bad := range []string{"git:/w/crius", "git:/w/crius#origin/main", "git:#main:R.md", "git:/w/crius#:R.md", "git:/w/crius#main:"} {
		testutil.Write(t, filepath.Join(home, "projects.toml"), "[projects.crius]\nleads = { build = \""+bad+"\" }\n")
		if _, err := Leads(home); err == nil || !strings.Contains(err.Error(), "git:<repo>#<rev>:<path>") {
			t.Errorf("%s: %v", bad, err)
		}
	}
}

// crius-hook files a session as ingest does: by its -n name (crius-spawn writes "project · role"), else by its folder.
func TestFilingGivesProjectAndRoleFromNameElseFolder(t *testing.T) {
	home := t.TempDir()
	testutil.Write(t, filepath.Join(home, "projects.toml"), "[projects.crius]\ncwd = [\"/w/crius\"]\n[projects.quotes]\ncwd = [\"/w/quotes\"]\n")
	for _, c := range []struct{ cwd, name, project, role string }{
		{"/w/crius", "quotes · p1", "quotes", "p1"},
		{"/w/crius/sub", "", "crius", "unknown"},
		{"/elsewhere", "crius-build: build", "crius", "build"},
		{"/elsewhere", "", "", "unknown"},
	} {
		p, r, err := Filing(home, c.cwd, c.name)
		if err != nil || p != c.project || r != c.role {
			t.Errorf("%+v: got %q %q %v", c, p, r, err)
		}
	}
	if _, _, err := Filing(t.TempDir(), "/w/crius", "crius · build"); err == nil {
		t.Error("no projects.toml gave no error")
	}
}
