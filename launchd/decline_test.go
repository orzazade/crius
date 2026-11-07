package launchd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ops 11-06 15:47: crius-decline sent Escape before bus-post, and bus-post refuses a text over 300 chars, so the tab's
// turn ended and nothing told it why (the 15:45 quotes lead sat idle). The bus line goes first; a refusal sends nothing.
func runDecline(t *testing.T, text string) (calls string, err error) {
	t.Helper()
	home := t.TempDir()
	bin := filepath.Join(home, ".crius", "bin")
	fake := filepath.Join(home, "fake")
	for _, d := range []string{bin, fake} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	log := filepath.Join(home, "calls")
	write := func(p, s string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(s), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// bus-post's own limit: it refuses a text over 300 characters
	write(filepath.Join(bin, "bus-post"), "#!/bin/bash\necho \"bus-post $3\" >> "+log+"\n[ ${#5} -le 300 ] || { echo 'bus-post: refused: text over 300' >&2; exit 1; }\n")
	write(filepath.Join(fake, "cmux"), "#!/bin/bash\necho \"cmux $1 ${@: -1}\" >> "+log+"\n")
	cmd := exec.Command("bash", "../bin/crius-decline", "workspace:1", "surface:2", "quotes", "quotes:lead", text)
	cmd.Env = []string{"HOME=" + home, "PATH=" + fake + ":/usr/bin:/bin"}
	_, err = cmd.CombinedOutput()
	b, _ := os.ReadFile(log)
	return string(b), err
}

func TestDeclineWithATextBusPostRefusesSendsNoKey(t *testing.T) {
	calls, err := runDecline(t, strings.Repeat("x", 301))
	if err == nil {
		t.Fatal("crius-decline exited 0 although bus-post refused the text")
	}
	if strings.Contains(calls, "cmux") {
		t.Fatalf("a key went to the tab before the refused bus-post; calls:\n%s", calls)
	}
}

func TestDeclinePostsThenEscapesThenSendsTheReason(t *testing.T) {
	calls, err := runDecline(t, "run the suite with -p 2")
	if err != nil {
		t.Fatal(err, calls)
	}
	want := "bus-post crius:lead\ncmux send-key Escape\ncmux send Crius: I declined your last prompt. run the suite with -p 2\ncmux send-key Enter\n"
	if calls != want {
		t.Fatalf("calls:\n%s\nwant:\n%s", calls, want)
	}
}
