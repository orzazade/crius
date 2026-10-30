package tidy

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 10, 31, 4, 30, 0, 0, time.Local)

// clone makes a Chrome copy of 1000 bytes in two files, last changed age ago.
func clone(t *testing.T, root, name string, age time.Duration) {
	t.Helper()
	d := filepath.Join(root, name, "Google Chrome.app.bundle", "Contents")
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(d, f), make([]byte, 500), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	at := now.Add(-age)
	if err := os.Chtimes(filepath.Join(root, name), at, at); err != nil {
		t.Fatal(err)
	}
}

// Crius 10-31 00:12: Chrome leaves a code_sign_clone copy per start (92 since 10-27, up to ~60 GB). A daily cleanup
// removes the copies older than a day, never the newest three, and says how much it freed; anything else in the
// folder stays.
func TestOldChromeClonesGoAndTheNewestThreeStay(t *testing.T) {
	var1 := t.TempDir()
	root := filepath.Join(var1, "nz", "abc", "X", "com.google.Chrome.code_sign_clone")
	for name, age := range map[string]time.Duration{
		"code_sign_clone.a3d": 72 * time.Hour, "code_sign_clone.b2d": 48 * time.Hour,
		"code_sign_clone.c30h": 30 * time.Hour, "code_sign_clone.d26h": 26 * time.Hour,
		"code_sign_clone.e18h": 18 * time.Hour, // young: stays though not among the newest three
		"code_sign_clone.f12h": 12 * time.Hour, "code_sign_clone.g2h": 2 * time.Hour, "code_sign_clone.h1h": time.Hour,
	} {
		clone(t, root, name, age)
	}
	// Only folders are copies: an old file with the copy's name stays.
	for _, f := range []string{"notes", "code_sign_clone.zfile"} {
		if err := os.WriteFile(filepath.Join(root, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	at := now.Add(-72 * time.Hour)
	if err := os.Chtimes(filepath.Join(root, "code_sign_clone.zfile"), at, at); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(root, "code_sign_clone.a3d")
	r, err := ChromeClones(filepath.Join(var1, "*", "*", "X", "com.google.Chrome.code_sign_clone"), now, nil)
	if err != nil {
		t.Fatal(err)
	}
	var gone []string
	for _, p := range r.Removed {
		gone = append(gone, filepath.Base(p))
	}
	sort.Strings(gone)
	want := []string{"code_sign_clone.a3d", "code_sign_clone.b2d", "code_sign_clone.c30h", "code_sign_clone.d26h"}
	if !reflect.DeepEqual(gone, want) || r.Kept != 4 || r.Bytes != 4000 {
		t.Fatalf("removed %v, kept %d, freed %d bytes; want %v, 4 kept, 4000 bytes", gone, r.Kept, r.Bytes, want)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("%s still there: %v", old, err)
	}
	left, _ := os.ReadDir(root)
	var names []string
	for _, e := range left {
		names = append(names, e.Name())
	}
	if strings.Join(names, " ") != "code_sign_clone.e18h code_sign_clone.f12h code_sign_clone.g2h code_sign_clone.h1h code_sign_clone.zfile notes" {
		t.Fatalf("left %v", names)
	}
}

// Four copies, all old: the newest three stay.
func TestTheNewestThreeStayWhenAllAreOld(t *testing.T) {
	root := filepath.Join(t.TempDir(), "X", "com.google.Chrome.code_sign_clone")
	for i, age := range []time.Duration{5, 4, 3, 2} {
		clone(t, root, "code_sign_clone."+string(rune('a'+i)), age*24*time.Hour)
	}
	r, err := ChromeClones(root, now, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Removed) != 1 || filepath.Base(r.Removed[0]) != "code_sign_clone.a" || r.Kept != 3 {
		t.Fatalf("%+v", r)
	}
}

// Review of S, Crius 00:25: a Chrome still running keeps its copy, however old, though it is not among the newest three.
// Its copy is the one made at its start: 30 s either side keeps that one copy alone. A Chrome that started an hour ago,
// after every copy, keeps the newest one, which stays anyway.
func TestARunningChromeKeepsItsCopy(t *testing.T) {
	root := filepath.Join(t.TempDir(), "X", "com.google.Chrome.code_sign_clone")
	for i, age := range []time.Duration{7, 6, 5, 4, 3, 2} {
		clone(t, root, "code_sign_clone."+string(rune('a'+i)), age*24*time.Hour)
	}
	for _, at := range []time.Duration{-30 * time.Second, 30 * time.Second} {
		running := []time.Time{now.Add(-6*24*time.Hour + at), now.Add(-time.Hour)}
		if gone, kept := removed(t, root, running); !reflect.DeepEqual(gone, []string{"code_sign_clone.a", "code_sign_clone.c"}) || kept != 4 {
			t.Fatalf("start %v from copy b: removed %v, kept %d", at, gone, kept)
		}
	}
	// Half way between copies b and c, the one made by then and the one made since both stay.
	if gone, kept := removed(t, root, []time.Time{now.Add(-5*24*time.Hour - 12*time.Hour)}); !reflect.DeepEqual(gone, []string{"code_sign_clone.a"}) || kept != 5 {
		t.Fatalf("between b and c: removed %v, kept %d", gone, kept)
	}
}

// removed runs a cleanup of a copy of root and gives the names it removed and the count it kept.
func removed(t *testing.T, root string, running []time.Time) ([]string, int) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "X")
	if out, err := exec.Command("cp", "-Rp", root, dir).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	r, err := ChromeClones(dir, now, running)
	if err != nil {
		t.Fatal(err)
	}
	var gone []string
	for _, p := range r.Removed {
		gone = append(gone, filepath.Base(p))
	}
	sort.Strings(gone)
	return gone, r.Kept
}

// The main process only, its start read in local time; a line ps did not write is an error.
func TestChromeStartsReadsTheMainProcess(t *testing.T) {
	ps := `Fri Oct 30 03:32:45 2026     /Applications/Google Chrome.app/Contents/MacOS/Google Chrome --origin-trial-disabled-features=CanvasTextNg --restart
Fri Oct 30 03:48:17 2026     /Applications/Google Chrome.app/Contents/Frameworks/Google Chrome Framework.framework/Versions/156.0.8078.74/Helpers/Google Chrome Helper (Renderer).app/Contents/MacOS/Google Chrome Helper (Renderer) --type=renderer
Sat Oct 31  0:22:15 2026     /Applications/Google Chrome.app/Contents/MacOS/Google Chrome
Sat Oct 31 00:22:16 2026     /bin/zsh
`
	got, err := ChromeStarts([]byte(ps))
	want := []time.Time{time.Date(2026, 10, 30, 3, 32, 45, 0, time.Local), time.Date(2026, 10, 31, 0, 22, 15, 0, time.Local)}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := ChromeStarts([]byte("ps: illegal option\n")); err == nil {
		t.Fatal("a line ps did not write was read")
	}
}

// "Found nothing" and "failed to look" differ, and both are loud: no folder (Chrome moved its copies) is an error, and
// so is a folder it cannot read. An empty folder is nothing to do.
func TestNoFolderAndAnUnreadableOneFail(t *testing.T) {
	none := filepath.Join(t.TempDir(), "none", "X")
	if _, err := ChromeClones(none, now, nil); err == nil || err.Error() != "no folder matches "+none {
		t.Fatalf("err %v", err)
	}
	empty := t.TempDir()
	if r, err := ChromeClones(empty, now, nil); err != nil || len(r.Removed) != 0 || r.Kept != 0 {
		t.Fatalf("%+v %v", r, err)
	}
	root := filepath.Join(t.TempDir(), "X")
	clone(t, root, "code_sign_clone.a", 72*time.Hour)
	if err := os.Chmod(root, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(root, 0o700) })
	if _, err := ChromeClones(root, now, nil); err == nil || !strings.Contains(err.Error(), root) {
		t.Fatalf("err %v, want one naming %s", err, root)
	}
}
