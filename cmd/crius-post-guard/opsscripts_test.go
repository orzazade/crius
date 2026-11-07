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
	leadCheck(t, dir, "~/.crius/state/crius/ops-bin/click-check.sh workspace:1000070003 surface:1000070012", "")
	leadCheck(t, dir, "bash ~/.crius/state/crius/ops-bin/click-check.sh workspace:1000070003 surface:1000070012", "")
	leadCheck(t, dir, "~/.crius/state/crius/ops-pins.sh; echo rc=$?", "")
}

func TestOpsCheckScriptsAskWhenChangedOrElsewhere(t *testing.T) {
	_, click, pins := opsHome(t)
	dir := t.TempDir()
	cmd := "~/.crius/state/crius/ops-bin/click-check.sh workspace:1000070003 surface:1000070012"
	// ops-pins.sh changed after its pin, with an az write: the click-check run asks
	b, _ := os.ReadFile(pins)
	write(t, pins, string(b)+"az repos pr update --id 1 --description x --org https://dev.azure.com/Appxite\n")
	leadCheck(t, dir, cmd, "ops-pins.sh")
	write(t, pins, string(b))
	// group-writable: anyone in the group could change it
	if err := os.Chmod(click, 0o775); err != nil {
		t.Fatal(err)
	}
	leadCheck(t, dir, cmd, "click-check.sh")
	if err := os.Chmod(click, 0o755); err != nil {
		t.Fatal(err)
	}
	// the same text in another folder is not the pinned ops script
	other := filepath.Join(dir, "click-check.sh")
	cb, _ := os.ReadFile(click)
	write(t, other, string(cb))
	leadCheck(t, dir, "bash "+other+" workspace:1 surface:2", "click-check.sh")
	// a command that writes the script before it runs it
	leadCheck(t, dir, "echo 'az repos pr update --id 1 --description x' > ~/.crius/state/crius/ops-pins.sh; "+cmd, "ops-pins.sh")
}
