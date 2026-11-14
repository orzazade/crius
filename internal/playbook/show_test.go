package playbook

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A session start shows the entries that fit its folder (team, stack, path)
// and nothing for a folder no entry fits.
func TestMatchShowsWhatFitsTheFolder(t *testing.T) {
	root := t.TempDir()
	quote := filepath.Join(root, "Projects", "scifi", "svc_shop")
	other := filepath.Join(root, "Projects", "scifi", "nothing")
	for _, d := range []string{quote, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(quote, "package.json"), []byte(`{"dependencies":{"typeorm":"0.2.45"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	b := &Book{Entries: []Entry{
		{ID: "typeorm-one-tx", Keys: []string{"typeorm"}, Evidence: []Evidence{{Team: "billing"}}},
		{ID: "shop-split", Keys: []string{"dotnet"}, Evidence: []Evidence{{Team: "svc_shop"}}},
		{ID: "helm-thing", Keys: []string{"helm"}, Evidence: []Evidence{{Team: "falcon"}}},
		{ID: "old", Keys: []string{"typeorm"}, Archived: true, Evidence: []Evidence{{Team: "svc_shop"}}},
	}}
	var ids []string
	for _, e := range b.Match(quote, "") {
		ids = append(ids, e.ID)
	}
	if strings.Join(ids, ",") != "shop-split,typeorm-one-tx" {
		t.Fatalf("matched %v, want the team entry then the stack entry, no archived or unrelated one", ids)
	}
	if got := ShowText(b.Match(other, ""), "/pb"); got != "" {
		t.Fatalf("a folder nothing fits printed %q, want nothing", got)
	}
}

// Crius 11-14: a session sees its own project's lessons first. A session
// works in svc_shop while its bus is shop.log, so the project comes from
// projects.toml, not the folder name, and outranks entries that only share words.
func TestOwnProjectComesFirst(t *testing.T) {
	b := &Book{Entries: []Entry{
		{ID: "generic-shell", Kind: "trap", When: "w", Do: "d", Because: "b", Keys: []string{"svc", "quote"},
			Evidence: []Evidence{{Team: "lessons"}, {Team: "lessons"}, {Team: "lessons"}}},
		{ID: "shop-api-groups", Kind: "tactic", When: "w", Do: "d", Because: "b",
			Evidence: []Evidence{{Team: "shop"}}},
		{ID: "axi-only", Kind: "tactic", When: "w", Do: "d", Because: "b", Evidence: []Evidence{{Team: "axi"}}},
	}}
	got := b.Match("/Users/x/Projects/Work/svc_shop", "shop")
	if len(got) != 2 || got[0].ID != "shop-api-groups" || got[1].ID != "generic-shell" {
		var ids []string
		for _, e := range got {
			ids = append(ids, e.ID)
		}
		t.Fatalf("match = %v, want [shop-api-groups generic-shell]", ids)
	}
}

// A transcript finding files under the session's project, as its bus does.
func TestTranscriptFindingFilesUnderTheProject(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, filepath.Join(dir, "p", "s.jsonl"), []string{"Root cause: the cache key had the date in it, so every run missed. 702 s to 198 s."})
	cur, _ := loadCursor(filepath.Join(dir, "state"))
	cfg := Config{TxRoots: []string{filepath.Join(dir, "p")}, Since: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		ProjectOf: func(string) string { return "shop" }}
	cs, _, err := Gather(cfg, cur, time.Now())
	if err != nil || len(cs) != 1 || cs[0].Team != "shop" {
		t.Fatalf("candidates %+v (%v), want one filed under shop", cs, err)
	}
}

// Crius 11-14: a session folder no project claims is counted in the run
// summary, so the next missing projects.toml line shows itself.
func TestUnmappedFoldersAreCounted(t *testing.T) {
	dir := t.TempDir()
	writeSession(t, filepath.Join(dir, "p", "a.jsonl"), []string{"ok"})
	cfg := Config{TxRoots: []string{filepath.Join(dir, "p")}, Since: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		ProjectOf: func(string) string { return "" }}
	cur, _ := loadCursor(filepath.Join(dir, "state"))
	_, st, err := Gather(cfg, cur, time.Now())
	if err != nil || len(st.Unmapped) != 1 || !strings.Contains(st.String(), "unmapped folders: 1 (/Users/x/Projects/scifi/crius)") {
		t.Fatalf("unmapped %v, summary %q (%v); want 1 folder named in the summary", st.Unmapped, st.String(), err)
	}
}

// Seen 11-14: 201 "unmapped folders" in one hour, nearly all temp folders and
// scratch worktrees, which are never projects; they hid the real gaps.
func TestTempFoldersAreNotCountedAsUnmapped(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "p", "a.jsonl")
	writeSession(t, p, []string{"ok"})
	data, _ := os.ReadFile(p)
	for i, cwd := range []string{"/private/tmp", "/tmp/x", "/private/var/folders/ab/T/wt", "/Users/x/Projects/scifi/crius"} {
		_ = os.WriteFile(filepath.Join(dir, "p", fmt.Sprintf("t%d.jsonl", i)),
			[]byte(strings.ReplaceAll(strings.ReplaceAll(string(data), "/Users/x/Projects/scifi/crius", cwd), `"uuid":"`, fmt.Sprintf(`"uuid":"t%d`, i))), 0o644)
	}
	_ = os.Remove(p)
	cfg := Config{TxRoots: []string{filepath.Join(dir, "p")}, Since: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		ProjectOf: func(string) string { return "" }}
	cur, _ := loadCursor(filepath.Join(dir, "state"))
	_, st, err := Gather(cfg, cur, time.Now())
	if err != nil || len(st.Unmapped) != 1 {
		t.Fatalf("unmapped %v (%v); want only the project folder", st.Unmapped, err)
	}
}
