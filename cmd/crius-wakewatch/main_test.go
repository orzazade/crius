package main

import (
	"errors"
	"fmt"
	"github.com/orzazade/crius/internal/wakewatch"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeCmux puts a cmux on PATH that records each call, one per line, and gives Crius's tab in a config file.
func fakeCmux(t *testing.T, tab string) (log string) {
	log, _ = fakeTab(t, tab)
	return log
}

// fakeTab puts a fake cmux and ps on PATH. cmux logs every call but its reads (tree, read-screen); the tree shows
// Crius's surface on ttys015, read-screen prints dir/screen (an empty Claude prompt), ps on ttys015 prints dir/front
// (Claude in front).
func fakeTab(t *testing.T, tab string) (log, dir string) {
	dir = t.TempDir()
	log = filepath.Join(dir, "calls")
	script := "#!/bin/sh\ncase \"$1\" in\n" +
		"tree) echo '│   ├── surface surface:1000020029 [terminal] \"crius\" [selected] ◀ active tty=ttys015'; exit 0 ;;\n" +
		"read-screen) cat " + filepath.Join(dir, "screen") + "; exit 0 ;;\nesac\n" +
		"for a in \"$@\"; do printf '[%s]' \"$a\" >> " + log + "; done\necho >> " + log + "\necho OK\n"
	ps := "#!/bin/sh\n[ \"$2\" = ttys015 ] && cat " + filepath.Join(dir, "front") + "\nexit 0\n"
	for name, body := range map[string]string{"cmux": script, "ps": ps} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{
		"screen": "⏺ done.\n──────────────\n❯ \n──────────────\n  quota: 7d 97%\n",
		"front":  "52166 52763 /usr/bin/login\n52169 52763 -/bin/zsh\n52763 52763 /Users/o/.local/bin/claude\n52859 52763 <defunct>\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	home := t.TempDir()
	t.Setenv("CRIUS_HOME", home)
	if tab != "" {
		if err := os.WriteFile(filepath.Join(home, "crius-tab"), []byte(tab), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return log, dir
}

// Owner 10-21 23:5x: no Telegram; an alert is one line typed into Crius's own tab, then Enter.
func TestSendTypesTheLineIntoCriusTab(t *testing.T) {
	log := fakeCmux(t, "# Crius's tab\nworkspace:1000020000 surface:1000020029\n")
	if err := send("WAKEWATCH: quotes.log: DONE quotes:lead -> crius:lead — F-86"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	want := "[send][--workspace][workspace:1000020000][--surface][surface:1000020029][--][WAKEWATCH: quotes.log: DONE quotes:lead -> crius:lead — F-86]\n" +
		"[send-key][--workspace][workspace:1000020000][--surface][surface:1000020029][enter]\n"
	if string(b) != want {
		t.Fatalf("got\n%s\nwant\n%s", b, want)
	}
}

// With no tab named, nothing is typed anywhere (a bare cmux send would go to the caller's own tab) and it is loud.
func TestSendWithNoCriusTabFailsLoudly(t *testing.T) {
	for _, tab := range []string{"", "surface:1000020029\n", "workspace:1 surface:2 extra\n", "workspace:x surface:2\n"} {
		log := fakeCmux(t, tab)
		err := send("WAKEWATCH: x")
		if err == nil || !strings.Contains(err.Error(), "crius-tab") {
			t.Fatalf("%q: %v", tab, err)
		}
		if _, statErr := os.Stat(log); statErr == nil {
			t.Fatalf("%q: cmux was called", tab)
		}
	}
}

// One tab that cannot be read must not hide the others.
func TestOneFailedTabReadKeepsTheOthers(t *testing.T) {
	real := cmux
	t.Cleanup(func() { cmux = real })
	cmux = func(args ...string) ([]byte, error) {
		switch {
		case args[0] == "tree":
			return []byte(`surface surface:1 [terminal] "quotes · lead"` + "\n" + `surface surface:2 [terminal] "axi · lead"` + "\n"), nil
		case args[2] == "surface:2":
			return []byte("surface gone"), errors.New("exit status 1")
		}
		return []byte("question?\n❓\n"), nil
	}
	got, err := screens()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Text != "question?\n❓\n" || got[0].Err != "" || !strings.Contains(got[1].Err, "surface gone") {
		t.Fatalf("%+v", got)
	}
}

func TestSwapUsedIsReadInGB(t *testing.T) {
	for in, want := range map[string]float64{
		"total = 17408.00M  used = 15997.06M  free = 1410.94M  (encrypted)\n": 15997.06 / 1024,
		"total = 30.00G  used = 21.50G  free = 8.50G  (encrypted)\n":          21.5,
		"total = 0.00M  used = 0.00M  free = 0.00M\n":                         0,
	} {
		got, err := swapUsed(in)
		if err != nil || got < want-0.001 || got > want+0.001 {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	if _, err := swapUsed("vm.swapusage: unknown oid"); err == nil {
		t.Error("an unreadable answer gave no error")
	}
}

// Review of 91854cf, P1: if Claude has exited, Crius's tab is a shell and Enter would run the alert text there
// ("$(...)" in a bus line runs). Nothing is typed unless Claude is the tab's foreground process, and that is loud.
func TestSendTypesOnlyWhenClaudeIsInFront(t *testing.T) {
	for name, ps := range map[string]string{
		"shell in front":   "6777 6832 /usr/bin/login\n6832 6832 -/bin/zsh\n",
		"claude in back":   "52763 99 /Users/o/.local/bin/claude\n99 99 vim\n",
		"no front process": "",
		"claude-like name": "7 7 /usr/local/bin/claude-wrapper\n",
	} {
		log, dir := fakeTab(t, "workspace:1000020000 surface:1000020029\n")
		os.WriteFile(filepath.Join(dir, "front"), []byte(ps), 0o644)
		err := send("WAKEWATCH: x $(touch /tmp/pwned)")
		if err == nil || !strings.Contains(err.Error(), "not Claude") {
			t.Errorf("%s: %v", name, err)
		}
		if _, statErr := os.Stat(log); statErr == nil {
			t.Errorf("%s: cmux typed into the tab", name)
		}
	}
	log, _ := fakeTab(t, "workspace:1000020000 surface:1000020077\n")
	if err := send("WAKEWATCH: x"); err == nil || !strings.Contains(err.Error(), "surface:1000020077") {
		t.Errorf("surface not in the tree: %v", err)
	}
	if _, statErr := os.Stat(log); statErr == nil {
		t.Error("surface not in the tree: cmux typed")
	}
}

// Review of 91854cf, P2: a line typed but not sent (Enter failed) blocks every later cmux send (it refuses over a
// draft). A WAKEWATCH line left in the prompt is ours: press Enter on it first. Someone else's draft is left alone.
func TestSendFinishesItsOwnStuckLine(t *testing.T) {
	for draft, want := range map[string]string{
		"WAKEWATCH: an older alert": "[send-key][--workspace][workspace:1000020000][--surface][surface:1000020029][enter]\n[send]",
		"owner is typing here":      "[send]",
	} {
		log, dir := fakeTab(t, "workspace:1000020000 surface:1000020029\n")
		os.WriteFile(filepath.Join(dir, "screen"), []byte("──────────────\n❯ "+draft+"\n──────────────\n"), 0o644)
		send("WAKEWATCH: new")
		b, _ := os.ReadFile(log)
		if !strings.HasPrefix(string(b), want) {
			t.Errorf("%q: got\n%s", draft, b)
		}
	}
}

// Brief P7 item 4: each tool is asked once; a failure says which tool and why, a hang says it did not answer.
func TestToolsSaysWhichToolFailsOrHangs(t *testing.T) {
	_, dir := fakeTab(t, "")
	for name, body := range map[string]string{
		"gh": "echo 'You are not logged into any GitHub hosts' >&2; exit 1",
		"az": "echo 0f7b", "docker": "echo 29.1.0",
		"bun": "sleep 5",
	} {
		os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755)
	}
	real := toolWait
	t.Cleanup(func() { toolWait = real })
	toolWait = 300 * time.Millisecond
	start := time.Now()
	res := tools()
	if len(res) != 5 || res["cmux"] != nil || res["az"] != nil || res["docker"] != nil {
		t.Fatalf("%v", res)
	}
	if res["gh"] == nil || !strings.Contains(res["gh"].Error(), "not logged into any GitHub hosts") {
		t.Errorf("gh: %v", res["gh"])
	}
	if res["bun"] == nil || !strings.Contains(res["bun"].Error(), "no answer") {
		t.Errorf("bun: %v", res["bun"])
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("a hung tool held the check for %v", d)
	}
}

// The fresh-start check reads every config folder's live sessions; a linked folder (~/.claude-personal) lists the
// same process twice, a dead pid's file is left over, and a session with no transcript yet has no start.
func TestSessionsReadsLiveSessionsWithTheirStart(t *testing.T) {
	home := t.TempDir()
	write := func(path, body string) {
		os.MkdirAll(filepath.Dir(path), 0o700)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	me, parent := os.Getpid(), os.Getppid()
	write(filepath.Join(home, ".claude", "sessions", fmt.Sprint(me)+".json"),
		fmt.Sprintf(`{"pid":%d,"sessionId":"aaa","name":"quotes: lead","status":"busy"}`, me))
	write(filepath.Join(home, ".claude", "sessions", "99999999.json"), `{"pid":99999999,"sessionId":"dead","name":"axi: lead"}`)
	write(filepath.Join(home, ".claude", "projects", "-Users-o-x", "aaa.jsonl"), "{}\n")
	write(filepath.Join(home, ".claude-work", "sessions", fmt.Sprint(parent)+".json"),
		fmt.Sprintf(`{"pid":%d,"sessionId":"bbb","name":"cortex · lead"}`, parent))
	if err := os.Symlink(filepath.Join(home, ".claude"), filepath.Join(home, ".claude-personal")); err != nil {
		t.Fatal(err)
	}
	ss, err := sessions(home)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]wakewatch.Session{}
	for _, s := range ss {
		got[s.ID] = s
	}
	if len(ss) != 2 || got["aaa"].Name != "quotes: lead" || got["bbb"].Name != "cortex · lead" {
		t.Fatalf("%+v", ss)
	}
	if d := time.Since(got["aaa"].Started); d < 0 || d > time.Minute {
		t.Errorf("aaa started %v", got["aaa"].Started)
	}
	if !got["bbb"].Started.IsZero() {
		t.Errorf("bbb has no transcript: %v", got["bbb"].Started)
	}
	write(filepath.Join(home, ".claude", "sessions", "1.json"), `{"pid":`)
	if _, err := sessions(home); err == nil || !strings.Contains(err.Error(), "1.json") {
		t.Errorf("a broken session file: %v", err)
	}
}
