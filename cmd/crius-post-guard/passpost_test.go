package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// passPostHome is quotesHome plus qa/tools holding the shipped pass-post.sh and verdict-check.ts (the pins are theirs).
func passPostHome(t *testing.T) (home, tools string) {
	t.Helper()
	home, qa := quotesHome(t, nil)
	tools = filepath.Join(qa, "tools")
	if err := os.MkdirAll(tools, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"pass-post.sh", "verdict-check.ts"} {
		b, err := os.ReadFile(filepath.Join("testdata", "pass-post", n))
		if err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(tools, n), string(b))
		pinBody(t, n, string(b))
	}
	if err := os.Chmod(filepath.Join(tools, "pass-post.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	return home, tools
}

const (
	ppCd   = "cd ~/.crius/state/quotes/build/qa/tools && "
	ppCall = "SENDER=qa-backend TO='backend-orders, LEADER' ./pass-post.sh 7848 ../96365-x-1fa0a77f/verdict.md ../96365-x-1fa0a77f/msg.txt"
	ppNote = " && echo '11:20 PASS 7848 posted (rc 0)' >> ../../notes/qa-backend.md"
)

// Crius 11-06: pass-post.sh writes only BUILD-LOG.md and its ADO calls are GETs; it passes as one exact line while it
// and verdict-check.ts have the pinned SHA-256.
func TestPassPostExactShapesPass(t *testing.T) {
	home, _ := passPostHome(t)
	for _, cmd := range []string{
		ppCd + ppCall,
		ppCd + ppCall + " 2>&1 | tail -5",
		ppCd + ppCall + ppNote,
		ppCd + ppCall + " 2>&1 | tail -20" + ppNote,
		ppCd + "SENDER=qa-frontend-2 TO='page-list, LEADER' ./pass-post.sh --check 7833 ~/.crius/state/quotes/build/qa/a-c5d4ef11/VERDICT.md /tmp/m.txt 2>&1 | tail -8",
		"cd " + home + "/.crius/state/quotes/build/qa/tools && TO=\"customer-view, LEADER\" SENDER=qa-backend-2 ./pass-post.sh 7831 v.md m.txt",
		ppCd + ppCall + " && echo \"PASS 7848: posted, see BUILD-LOG\" done >> ../../notes/qa-backend-2.md",
	} {
		passes(t, cmd)
	}
}

func TestPassPostHookTimeChecksAsk(t *testing.T) {
	cmd := ppCd + ppCall + " 2>&1 | tail -5" + ppNote
	edit := func(t *testing.T, p string) {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		write(t, p, string(b)+"\naz repos pr update --id \"$PR\" --status completed --org https://dev.azure.com/Appxite\n")
	}
	t.Run("pass-post.sh changed", func(t *testing.T) {
		_, tools := passPostHome(t)
		edit(t, filepath.Join(tools, "pass-post.sh"))
		asks(t, cmd, "pass-post.sh does not match the SHA-256 the guard pins")
	})
	t.Run("verdict-check.ts changed", func(t *testing.T) {
		_, tools := passPostHome(t)
		edit(t, filepath.Join(tools, "verdict-check.ts"))
		asks(t, cmd, "verdict-check.ts does not match the SHA-256 the guard pins")
	})
	for _, n := range []string{"bunfig.toml", ".env", ".env.local", "BUNFIG.TOML"} {
		t.Run(n+" in qa/tools", func(t *testing.T) {
			_, tools := passPostHome(t)
			write(t, filepath.Join(tools, n), "PATH=/tmp/evil\n")
			asks(t, cmd, "qa/tools holds "+n)
		})
	}
	t.Run("BUN_OPTIONS", func(t *testing.T) {
		passPostHome(t)
		t.Setenv("BUN_OPTIONS", "--preload /tmp/x.ts")
		asks(t, cmd, "BUN_OPTIONS")
	})
	t.Run("global bunfig", func(t *testing.T) {
		home, _ := passPostHome(t)
		write(t, filepath.Join(home, ".bunfig.toml"), "preload = ['/tmp/x.ts']\n")
		asks(t, cmd, "global bun config")
	})
	t.Run("BASH_ENV", func(t *testing.T) {
		passPostHome(t)
		t.Setenv("BASH_ENV", "/tmp/x.sh")
		asks(t, cmd, "BASH_ENV")
	})
	t.Run("note file executable", func(t *testing.T) {
		home, _ := passPostHome(t)
		p := filepath.Join(home, ".crius/state/quotes/build/notes/qa-backend.md")
		write(t, p, "x\n")
		if err := os.Chmod(p, 0o755); err != nil {
			t.Fatal(err)
		}
		asks(t, cmd, "an executable file")
	})
	t.Run("note file a symlink", func(t *testing.T) {
		home, _ := passPostHome(t)
		if err := os.Symlink(filepath.Join(home, ".zshenv"), filepath.Join(home, ".crius/state/quotes/build/notes/qa-backend.md")); err != nil {
			t.Fatal(err)
		}
		asks(t, cmd, "not a plain file")
	})
}

// Red proofs Crius named: an extra command after the tail and an az write anywhere in the line still ask; so does any
// other change of shape.
func TestPassPostOffShapeAsks(t *testing.T) {
	passPostHome(t)
	o := " --org https://dev.azure.com/Appxite"
	for _, cmd := range []string{
		ppCd + ppCall + " 2>&1 | tail -5; az repos pr update --id 7848 --status completed" + o,
		ppCd + ppCall + " 2>&1 | tail -5 && az repos pr set-vote --id 7848 --vote approve" + o + " -o none",
		ppCd + ppCall + " 2>&1 | tail -5" + ppNote + "; az repos pr create --title t --source-branch feature/96365-x" + o,
		ppCd + ppCall + " 2>&1 | tail -5" + ppNote + " && az repos pr update --id 7848 --description d" + o,
		ppCd + ppCall + " && echo \"$(az repos pr update --id 7848 --status completed" + o + ")\" >> ../../notes/qa-backend.md",
		ppCd + ppCall + " && echo `az repos pr update --id 7848 --status completed" + o + "` >> ../../notes/qa-backend.md",
		ppCd + ppCall + " 2>&1 | az repos pr update --id 7848 --status completed" + o,
		ppCd + ppCall + " 2>&1 | tail -5\naz repos pr update --id 7848 --status completed" + o,
		ppCd + ppCall + " # && az repos pr update --id 7848 --status completed" + o,
		ppCd + "SENDER=qa-backend TO='backend-orders, LEADER' ./pass-post.sh 7848 v.md $(az repos pr update --id 7848 --status completed" + o + ")",
		ppCd + "SENDER=qa-backend TO='backend-orders, LEADER' AZURE_DEVOPS_EXT_PAT=x ./pass-post.sh 7848 v.md m.txt",
		ppCd + "SENDER=qa-backend TO='backend-orders, LEADER' PATH=/tmp/evil ./pass-post.sh 7848 v.md m.txt",
		"cd ~/.crius/state/quotes/build/qa/tools; " + ppCall,
		"cd /tmp && " + ppCall,
		ppCd + "SENDER=qa-backend TO='backend-orders, LEADER' bash ./pass-post.sh 7848 v.md m.txt",
		ppCd + "SENDER=qa-backend TO='backend-orders, LEADER' ./pass-post.sh 7848 v.md m.txt extra.md",
		ppCd + ppCall + " 2>/tmp/err.log",
		ppCd + ppCall + " > ../../BUILD-LOG.md",
		ppCd + ppCall + " && echo hi > ../../notes/qa-backend.md",
		ppCd + ppCall + " && echo hi >> ../../BUILD-LOG.md",
		ppCd + ppCall + " && echo hi >> ../../notes/run.sh",
		ppCd + ppCall + " && echo hi >> ~/.zshenv",
		ppCd + ppCall + " && echo =az >> ../../notes/qa-backend.md",
		ppCd + ppCall + " 2>&1 | head -5 | sh",
	} {
		if ok, why := passPostExact(cmd, "sid-1"); ok && why == "" {
			t.Errorf("exact rule passed an off shape:\n  %s", cmd)
		}
		// the lead allow list passes the pinned pass-post.sh in other shapes too (item 5): what must still ask is an az write
		if strings.Contains(cmd, "az repos") && !strings.Contains(cmd, " # ") {
			asks(t, cmd, "")
		}
	}
}
