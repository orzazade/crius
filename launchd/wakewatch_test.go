package launchd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orzazade/crius/internal/testutil"
)

// The run script hands crius-wakewatch the two Telegram values from ~/.secrets and no other secret.
func TestWakewatchRunPassesOnlyTheTelegramValues(t *testing.T) {
	root, _ := testutil.Temp(t)
	home := filepath.Join(root, "home")
	testutil.Write(t, filepath.Join(home, ".secrets"),
		"export TELEGRAM_BOT_TOKEN='tok 1'\nTELEGRAM_CHAT_ID=42\nexport OTHER_SECRET=nope\necho noise\nfalse\n")
	fake := filepath.Join(home, ".crius", "bin", "crius-wakewatch")
	testutil.Write(t, fake, "#!/bin/sh\nenv\n")
	os.Chmod(fake, 0o755)
	cmd := exec.Command(filepath.Join(testutil.RepoRoot(t), "bin", "crius-wakewatch-run"))
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatal(err, string(out))
	}
	env := string(out)
	for _, want := range []string{"TELEGRAM_BOT_TOKEN=tok 1\n", "TELEGRAM_CHAT_ID=42\n"} {
		if !strings.Contains(env, want) {
			t.Errorf("missing %q in\n%s", want, env)
		}
	}
	if strings.Contains(env, "OTHER_SECRET") || strings.Contains(env, "noise") {
		t.Errorf("leaked:\n%s", env)
	}
}

func TestWakewatchJobRunsTheScriptEvery5Min(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), "launchd", "space.scifilab.crius.wakewatch.plist.in"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<string>@HOME@/.crius/bin/crius-wakewatch-run</string>", "<integer>300</integer>",
		"/Applications/cmux.app/Contents/Resources/bin"} {
		if !strings.Contains(string(b), want) {
			t.Error("plist lacks", want)
		}
	}
}

// An empty TELEGRAM_CHAT_ID must still reach crius-wakewatch, which then alerts on the bus; the script must not exit
// silently before it.
func TestWakewatchRunStillRunsWithAnEmptyChatID(t *testing.T) {
	root, _ := testutil.Temp(t)
	home := filepath.Join(root, "home")
	testutil.Write(t, filepath.Join(home, ".secrets"), "export TELEGRAM_BOT_TOKEN=tok\n")
	fake := filepath.Join(home, ".crius", "bin", "crius-wakewatch")
	testutil.Write(t, fake, "#!/bin/sh\necho ran\nenv\n")
	os.Chmod(fake, 0o755)
	cmd := exec.Command(filepath.Join(testutil.RepoRoot(t), "bin", "crius-wakewatch-run"))
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "ran\n") || !strings.Contains(string(out), "TELEGRAM_BOT_TOKEN=tok\n") {
		t.Fatal(err, string(out))
	}
}
