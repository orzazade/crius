package playbook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		{ID: "typeorm-one-tx", Keys: []string{"typeorm"}, Evidence: []Evidence{{Team: "staffy"}}},
		{ID: "shop-split", Keys: []string{"dotnet"}, Evidence: []Evidence{{Team: "svc_shop"}}},
		{ID: "helm-thing", Keys: []string{"helm"}, Evidence: []Evidence{{Team: "falcon"}}},
		{ID: "old", Keys: []string{"typeorm"}, Archived: true, Evidence: []Evidence{{Team: "svc_shop"}}},
	}}
	var ids []string
	for _, e := range b.Match(quote) {
		ids = append(ids, e.ID)
	}
	if strings.Join(ids, ",") != "shop-split,typeorm-one-tx" {
		t.Fatalf("matched %v, want the team entry then the stack entry, no archived or unrelated one", ids)
	}
	if got := ShowText(b.Match(other), "/pb"); got != "" {
		t.Fatalf("a folder nothing fits printed %q, want nothing", got)
	}
}
