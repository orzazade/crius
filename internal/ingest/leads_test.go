package ingest

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

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

// Crius 22:46: a linked git worktree is filed under its main repo's project. Six quotes builders ran in
// <parent>/.worktrees/quotes-*, which only the parent folder's project named, so the quota split gave quotes' use to
// that parent. A project that names the worktree's own folder still wins.
func TestAWorktreeIsFiledUnderItsMainRepo(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := filepath.Join(root, "w")
	repo := testutil.GitRepo(t, filepath.Join(w, "svc", "channels"))
	testutil.Write(t, filepath.Join(repo, "a.txt"), "1\n")
	testutil.CommitAll(t, repo)
	for _, name := range []string{"quotes-a", "pinned"} {
		testutil.Git(t, repo, "worktree", "add", "-q", "-b", name, filepath.Join(w, ".worktrees", name))
	}
	if err := os.MkdirAll(filepath.Join(w, ".worktrees", "quotes-a", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	testutil.Write(t, filepath.Join(home, "projects.toml"), fmt.Sprintf(
		"[projects.parent]\ncwd = [%q]\n[projects.quotes]\ncwd = [%q]\n[projects.pin]\ncwd = [%q]\n",
		w, filepath.Join(w, "svc"), filepath.Join(w, ".worktrees", "pinned")))
	p, err := QuotaProjects(home)
	if err != nil {
		t.Fatal(err)
	}
	for cwd, want := range map[string]string{
		filepath.Join(w, ".worktrees", "quotes-a"):        "quotes",
		filepath.Join(w, ".worktrees", "quotes-a", "sub"): "quotes",
		filepath.Join(w, ".worktrees", "pinned"):          "pin",
		filepath.Join(w, ".worktrees", "gone"):            "parent",
		filepath.Join(w, "other"):                         "parent",
		repo:                                              "quotes",
		"":                                                "",
	} {
		if got := p.ForCwd(cwd, nil); got != want {
			t.Errorf("%s: got %q, want %q", strings.TrimPrefix(cwd, root), got, want)
		}
	}
}

// Review of the 22:46 fix: a worktree's sessions keep its main repo's project after the worktree is removed. Builders
// remove theirs when done; re-filing from the disk alone then gave their sessions and quota share to the parent again,
// so the quota guard missed what they spent.
func TestAWorktreeKeepsItsProjectAfterItIsRemoved(t *testing.T) {
	clock = func() time.Time { return qNow }
	t.Cleanup(func() { clock = time.Now })
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := filepath.Join(root, "w")
	repo := testutil.GitRepo(t, filepath.Join(w, "svc"))
	testutil.Write(t, filepath.Join(repo, "a.txt"), "1\n")
	testutil.CommitAll(t, repo)
	wt := filepath.Join(w, ".worktrees", "quotes-a")
	testutil.Git(t, repo, "worktree", "add", "-q", "-b", "quotes-a", wt)
	x := newIC(t, cfg{cwd: w, extra: fmt.Sprintf("[projects.quotes]\npersona = \"personal\"\naccounts = [\"max\"]\n"+
		"cwd = [%q]\nrepos = []\nteam_logs = []\nartifact_dirs = []\n"+
		"[projects.rig]\npersona = \"personal\"\naccounts = [\"max\"]\ncwd = [%q]\nrepos = []\nteam_logs = []\nartifact_dirs = []\n",
		repo, filepath.Join(root, "rig"))})
	sid := "aaaaaaaa-0000-0000-0000-000000000001"
	at := qNow.Add(-time.Hour)
	x.session("personal", sid, filepath.Join(wt, "sub"), "", at)
	x.spool(sampleAt(sid, at, 30, qNow.Add(3*24*time.Hour)))
	for _, removed := range []bool{false, true} {
		if removed {
			testutil.Git(t, repo, "worktree", "remove", "--force", wt)
		}
		x.run()
		if got := x.q("select project from session where id = ?", sid); len(got) != 1 || got[0][0] != "quotes" {
			t.Errorf("removed=%v: session filed %v, want quotes", removed, got)
		}
		var shares []string
		for _, s := range x.report().Shares {
			shares = append(shares, s.Project)
		}
		if !reflect.DeepEqual(shares, []string{"quotes"}) {
			t.Errorf("removed=%v: quota shares %v, want [quotes]", removed, shares)
		}
	}

	// Round 2: the record is the session's own, not the path's. The removed worktree's folder made again as a plain
	// folder files a new session there by that folder, and the same path made a worktree of another repo, then
	// removed, leaves the quotes session where it was.
	plain, rigSid := "aaaaaaaa-0000-0000-0000-000000000002", "aaaaaaaa-0000-0000-0000-000000000003"
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	x.session("personal", plain, wt, "", at)
	x.run()
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	rig := testutil.GitRepo(t, filepath.Join(root, "rig"))
	testutil.Write(t, filepath.Join(rig, "a.txt"), "1\n")
	testutil.CommitAll(t, rig)
	testutil.Git(t, rig, "worktree", "add", "-q", "-b", "rig-a", wt)
	x.session("personal", rigSid, wt, "", at)
	x.run()
	testutil.Git(t, rig, "worktree", "remove", "--force", wt)
	x.run()
	for id, want := range map[string]string{sid: "quotes", plain: "demo", rigSid: "rig"} {
		if got := x.q("select project from session where id = ?", id); len(got) != 1 || got[0][0] != want {
			t.Errorf("after reuse: session %s filed %v, want %s", id[len(id)-1:], got, want)
		}
	}
	if s := x.report().Shares; len(s) != 1 || s[0].Project != "quotes" {
		t.Errorf("after reuse: quota shares %v, want quotes alone", s)
	}
}

// Review round 3: the worktree ingest saw when it first filed a session is the one recorded. A builder that removed
// its worktree later in the same run left the session recorded as in none, and the next run filed it by its folder.
func TestTheFirstLookAtAWorktreeIsTheOneRecorded(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := filepath.Join(root, "w")
	repo := testutil.GitRepo(t, filepath.Join(w, "svc"))
	testutil.Write(t, filepath.Join(repo, "a.txt"), "1\n")
	testutil.CommitAll(t, repo)
	wt := filepath.Join(w, ".worktrees", "quotes-a")
	testutil.Git(t, repo, "worktree", "add", "-q", "-b", "quotes-a", wt)
	x := newIC(t, cfg{cwd: w, extra: fmt.Sprintf("[projects.quotes]\npersona = \"personal\"\naccounts = [\"max\"]\n"+
		"cwd = [%q]\nrepos = []\nteam_logs = []\nartifact_dirs = []\n", repo)})
	sid := "aaaaaaaa-0000-0000-0000-000000000001"
	x.session("personal", sid, wt, "", time.Now().Add(-time.Hour))
	worktreeOf = func(dir string) (string, string) {
		p, m := diskWorktree(dir)
		if p != "" {
			testutil.Git(t, repo, "worktree", "remove", "--force", wt)
		}
		return p, m
	}
	t.Cleanup(func() { worktreeOf = diskWorktree })
	x.run()
	worktreeOf = diskWorktree
	x.run()
	if got := x.q("select project from session where id = ?", sid); len(got) != 1 || got[0][0] != "quotes" {
		t.Errorf("session filed %v, want quotes", got)
	}
	if got := x.q("select path from session_worktree where session = ?", sid); len(got) != 1 || got[0][0] != wt {
		t.Errorf("recorded %v, want %s", got, wt)
	}
}

// Review round 4: a first look whose transaction rolled back is still the one recorded. The session's row predates the
// worktree record (a store from before it), its next transcript failed after the look, and the sweep then looked again
// at a removed worktree and recorded none.
func TestALookWhoseTransactionRolledBackIsStillRecorded(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := filepath.Join(root, "w")
	repo := testutil.GitRepo(t, filepath.Join(w, "svc"))
	testutil.Write(t, filepath.Join(repo, "a.txt"), "1\n")
	testutil.CommitAll(t, repo)
	wt := filepath.Join(w, ".worktrees", "quotes-a")
	testutil.Git(t, repo, "worktree", "add", "-q", "-b", "quotes-a", wt)
	x := newIC(t, cfg{cwd: w, extra: fmt.Sprintf("[projects.quotes]\npersona = \"personal\"\naccounts = [\"max\"]\n"+
		"cwd = [%q]\nrepos = []\nteam_logs = []\nartifact_dirs = []\n", repo)})
	sid := "aaaaaaaa-0000-0000-0000-000000000001"
	at := time.Now().Add(-time.Hour)
	x.session("personal", sid, wt, "", at)
	x.run()
	x.q("delete from session_worktree")
	x.q("create trigger fail_rollup before update of human_prompts on session begin select raise(abort, 'test'); end")
	x.session("personal", sid, wt, "", at, at.Add(time.Minute))
	worktreeOf = func(dir string) (string, string) {
		p, m := diskWorktree(dir)
		if p != "" {
			testutil.Git(t, repo, "worktree", "remove", "--force", wt)
		}
		return p, m
	}
	t.Cleanup(func() { worktreeOf = diskWorktree })
	x.run()
	worktreeOf = diskWorktree
	x.q("drop trigger fail_rollup")
	x.run()
	if got := x.q("select path from session_worktree where session = ?", sid); len(got) != 1 || got[0][0] != wt {
		t.Errorf("recorded %v, want %s", got, wt)
	}
	if got := x.q("select project from session where id = ?", sid); len(got) != 1 || got[0][0] != "quotes" {
		t.Errorf("session filed %v, want quotes", got)
	}
}
