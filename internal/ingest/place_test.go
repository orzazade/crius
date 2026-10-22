package ingest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/orzazade/crius/internal/testutil"
)

// qa3 10-22 19:2x: crius-spawn opened quotes in svc_cpq, the first of its cwd list, which is a list of folders whose
// sessions belong to the project, not a place to start in. A project with several cwd folders names its launch folder.
func TestPlaceStartsInTheLaunchFolderNeverTheFirstOfSeveralCwd(t *testing.T) {
	home, w := t.TempDir(), t.TempDir()
	for _, d := range []string{"svc_cpq", "svc_quote/channels", "axon", "bot", "crius", "a", "b", "elsewhere", "c", "d", "e"} {
		if err := os.MkdirAll(filepath.Join(w, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	testutil.Write(t, filepath.Join(home, "projects.toml"), fmt.Sprintf(`
[projects.quotes]
persona = "work"
accounts = ["max"]
cwd = ["%[1]s/svc_cpq", "%[1]s/svc_quote"]
launch = "%[1]s/svc_quote/channels"
[projects.trading]
accounts = ["max"]
cwd = ["%[1]s/axon", "%[1]s/bot"]
[projects.crius]
accounts = ["max"]
cwd = ["%[1]s/crius"]
[projects.stray]
accounts = ["max"]
cwd = ["%[1]s/a", "%[1]s/b"]
launch = "%[1]s/elsewhere"
[projects.climb]
accounts = ["max"]
cwd = ["%[1]s/svc_cpq", "%[1]s/a"]
launch = "%[1]s/a/../svc_cpq"
[projects.relative]
accounts = ["max"]
cwd = ["%[1]s/a", "%[1]s/b"]
launch = "a"
[projects.missing]
accounts = ["max"]
cwd = ["%[1]s/a", "%[1]s/b"]
launch = "%[1]s/a/gone"
[projects.slash]
accounts = ["max"]
cwd = ["%[1]s/c/"]
[projects.dot]
accounts = ["max"]
cwd = ["%[1]s/./d", "%[1]s/e"]
launch = "%[1]s/d"
`, w))
	if cwd, account, persona, err := Place(home, "quotes"); err != nil || cwd != w+"/svc_quote/channels" || account != "max" || persona != "work" {
		t.Errorf("quotes: %q %q %q %v", cwd, account, persona, err)
	}
	if cwd, _, _, err := Place(home, "crius"); err != nil || cwd != w+"/crius" {
		t.Errorf("crius (one cwd, no launch): %q %v", cwd, err)
	}
	if _, _, _, err := Place(home, "trading"); err == nil || !strings.Contains(err.Error(), "launch") {
		t.Errorf("trading (two cwd, no launch): err %v, want one naming launch", err)
	}
	// A session started outside every cwd folder would not be filed under its project.
	if _, _, _, err := Place(home, "stray"); err == nil || !strings.Contains(err.Error(), w+"/elsewhere") {
		t.Errorf("stray (launch outside cwd): err %v, want one naming the folder", err)
	}
	// Review round 10: a launch folder is the folder it names once "..", "." and "//" are gone.
	if cwd, _, _, err := Place(home, "climb"); err != nil || cwd != w+"/svc_cpq" {
		t.Errorf("climb (a/../svc_cpq): %q %v, want %s/svc_cpq", cwd, err, w)
	}
	// Review round 10: a relative launch folder has no place to start from.
	if _, _, _, err := Place(home, "relative"); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Errorf("relative launch: err %v, want one saying it must be absolute", err)
	}
	// Review round 10: a launch folder that is not there fails before a tab opens, naming it.
	if _, _, _, err := Place(home, "missing"); err == nil || !strings.Contains(err.Error(), w+"/a/gone") || !strings.Contains(err.Error(), "no folder") {
		t.Errorf("missing launch folder: err %v, want one naming it", err)
	}
	// Review round 10: a cwd folder is matched as cleaned too ("c/", "./d"), or its own launch folder is filed elsewhere.
	if cwd, _, _, err := Place(home, "slash"); err != nil || cwd != w+"/c" {
		t.Errorf("slash (cwd c/): %q %v, want %s/c", cwd, err, w)
	}
	if cwd, _, _, err := Place(home, "dot"); err != nil || cwd != w+"/d" {
		t.Errorf("dot (cwd ./d): %q %v, want %s/d", cwd, err, w)
	}
}
