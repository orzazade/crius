package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Owner order 11-08 item 4: on 11-07 from 20:00 crius:ops sat on its own prompt for click-check.sh, which runs
// ops-pins.sh: "a script the command or the script itself may change before it runs (ops-pins.sh)". Both are read-only
// checks Crius's ops tab runs before every click; pinned by hash, they run with no prompt.
func opsHome(t *testing.T) (home, click, pins string) {
	t.Helper()
	home, _ = quotesHomePRs(t, nil)
	t.Setenv("CRIUS_HOME", filepath.Join(home, ".crius"))
	click = filepath.Join(home, ".crius/state/crius/ops-bin/click-check.sh")
	pins = filepath.Join(home, ".crius/state/crius/ops-pins.sh")
	for name, p := range map[string]string{"click-check.sh": click, "ops-pins.sh": pins} {
		b, err := os.ReadFile(filepath.Join("testdata", "ops", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, p, string(b))
		if err := os.Chmod(p, 0o755); err != nil {
			t.Fatal(err)
		}
		pinBody(t, name, string(b))
	}
	return home, click, pins
}

func TestOpsCheckScriptsRunWithNoPromptOnTheirPins(t *testing.T) {
	_, _, _ = opsHome(t)
	dir := t.TempDir()
	// the 11-07 20:00 command, and the other shapes ops runs them in
	guardCall(t, dir, "~/.crius/state/crius/ops-bin/click-check.sh workspace:1000070003 surface:1000070012", "")
	guardCall(t, dir, "bash ~/.crius/state/crius/ops-bin/click-check.sh workspace:1000070003 surface:1000070012", "")
	guardCall(t, dir, "~/.crius/state/crius/ops-pins.sh; echo rc=$?", "")
}

func TestOpsCheckScriptsAreReadWhenChangedOrElsewhere(t *testing.T) {
	_, click, pins := opsHome(t)
	dir := t.TempDir()
	cmd := "~/.crius/state/crius/ops-bin/click-check.sh workspace:1000070003 surface:1000070012"
	// ops-pins.sh changed after its pin: click-check.sh runs it as a script it may change first, so it is refused, and
	// the reason says the pin no longer matches
	b, _ := os.ReadFile(pins)
	write(t, pins, string(b)+"az repos pr update --id 1 --description x --org https://dev.azure.com/Appxite\n")
	guardCall(t, dir, cmd, "deny", "ops-pins.sh", "does not match the SHA-256")
	write(t, pins, string(b))
	// group-writable: anyone in the group could change it
	if err := os.Chmod(click, 0o775); err != nil {
		t.Fatal(err)
	}
	guardCall(t, dir, cmd, "deny", "click-check.sh")
	if err := os.Chmod(click, 0o755); err != nil {
		t.Fatal(err)
	}
	// the same text in another folder is not the pinned ops script
	other := filepath.Join(dir, "click-check.sh")
	cb, _ := os.ReadFile(click)
	write(t, other, string(cb))
	guardCall(t, dir, "bash "+other+" workspace:1 surface:2", "deny", "click-check.sh")
	// a command that writes the script before it runs it
	guardCall(t, dir, "echo 'az repos pr update --id 1 --description x' > ~/.crius/state/crius/ops-pins.sh; "+cmd, "deny", "ops-pins.sh")
}
