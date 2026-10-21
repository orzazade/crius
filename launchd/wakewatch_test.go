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
