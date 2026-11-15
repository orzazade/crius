package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orzazade/crius/internal/playbook"
	"github.com/orzazade/crius/internal/tabsend"
)

func init() { tabsend.Wait = 0 }

func home(t *testing.T) {
	t.Helper()
	h := t.TempDir()
	t.Setenv("CRIUS_HOME", h)
	if err := os.MkdirAll(filepath.Join(h, "state", "playbook"), 0o700); err != nil {
		t.Fatal(err)
	}
	hosts := `[{"name":"legion","target":"u@h","ssh":["ssh"]}]`
	if err := os.WriteFile(filepath.Join(h, "state", "playbook", "hosts.json"), []byte(hosts), 0o600); err != nil {
		t.Fatal(err)
	}
}

const rule = "────────────────────\n"

func screen(above, box string) []byte {
	return []byte(above + rule + "❯\u00a0" + box + "\n" + rule + "  status\n")
}

// pane is a box that shows the dim placeholder, the text once typed, and the placeholder again once Enter goes;
// enterWorks false keeps the text in the box.
func pane(enterWorks bool) func(playbook.Host) tabsend.Runner {
	return func(h playbook.Host) tabsend.Runner {
		typed, above := "", ""
		return func(line string) ([]byte, error) {
			switch {
			case strings.Contains(line, "display-message"):
				return []byte("%7\tquotes\tqa\t2\t0\t0\n"), nil
			case strings.Contains(line, "send-keys") && strings.Contains(line, " -l "):
				typed = "hello there"
			case strings.HasSuffix(line, " Enter") && enterWorks:
				above, typed = "❯ "+typed+"\n", ""
			}
			if typed == "" {
				return screen(above, "\x1b[2mTry something\x1b[0m"), nil
			}
			return screen(above, typed), nil
		}
	}
}

func TestExitCodes(t *testing.T) {
	home(t)
	for _, c := range []struct {
		name   string
		args   []string
		runner func(playbook.Host) tabsend.Runner
		code   int
		says   string
	}{
		{"sent", []string{"Legion", "quotes:qa", "hello", "there"}, pane(true), 0, "SENT legion quotes:qa"},
		{"stuck", []string{"legion", "quotes:qa", "hello there"}, pane(false), 1, "NOT SENT legion quotes:qa: not sent: the box still holds"},
		{"unknown box", []string{"vps", "quotes:qa", "hi"}, pane(true), 2, `no box "vps"`},
		{"no input box", []string{"legion", "quotes:qa", "hi"}, func(playbook.Host) tabsend.Runner {
			return func(line string) ([]byte, error) {
				if strings.Contains(line, "display-message") {
					return []byte("%7\tquotes\tqa\t2\t0\t0\n"), nil
				}
				return []byte("Do you want to proceed?\n❯ 1. Yes\n"), nil
			}
		}, 2, "shows no Claude input box"},
		{"usage", []string{"legion", "quotes:qa"}, pane(true), 2, "usage:"},
	} {
		var out, errs bytes.Buffer
		code := run(c.args, &out, &errs, c.runner)
		if code != c.code || !strings.Contains(out.String()+errs.String(), c.says) {
			t.Errorf("%s: exit %d, said %q %q; want exit %d saying %q", c.name, code, out.String(), errs.String(), c.code, c.says)
		}
	}
}
