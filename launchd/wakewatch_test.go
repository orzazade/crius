package launchd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orzazade/crius/internal/testutil"
)

// launchd starts crius-wakewatch itself (no run script, no secret: owner 10-21 23:5x) with cmux on its PATH.
func TestWakewatchJobRunsTheBinaryEvery5Min(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), "launchd", "space.scifilab.crius.wakewatch.plist.in"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<string>@HOME@/.crius/bin/crius-wakewatch</string>", "<integer>300</integer>",
		"/Applications/cmux.app/Contents/Resources/bin"} {
		if !strings.Contains(string(b), want) {
			t.Error("plist lacks", want)
		}
	}
}

// Brief fixes-1025 item 5: one job runs crius-wakewatch after-boot once per login (RunAtLoad, no interval), with cmux on
// its PATH to reach Crius's tab.
func TestAfterBootJobRunsOnceAtLogin(t *testing.T) {
	root := testutil.RepoRoot(t)
	b, err := os.ReadFile(filepath.Join(root, "launchd", "space.scifilab.crius.after-boot.plist.in"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<string>@HOME@/.crius/bin/crius-wakewatch</string>\n\t\t<string>after-boot</string>",
		"<key>RunAtLoad</key>\n\t<true/>", "/Applications/cmux.app/Contents/Resources/bin", "@HOME@/.crius/logs/after-boot.log"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("plist lacks %q", want)
		}
	}
	if strings.Contains(string(b), "StartInterval") || strings.Contains(string(b), "KeepAlive") {
		t.Error("plist runs more than once per login")
	}
	// The installer loads every template under launchd/: TestInstallLoadsEveryTemplate.
}
