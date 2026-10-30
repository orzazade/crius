package launchd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Owner 10-30 ("too much noise"): bin/crius-wait is Crius's one waiter over every bus log, with the digest, in place of
// one waiter per project. Its first run starts each log where the old crius-role cursors stopped (the furthest one of
// them; a log none names starts at its end), joins their asks (the latest per sender) and leaves other roles' cursors
// out; later runs keep their own cursor. It reads the bus with --watch-dir, so a log born later is read from line 1.
func TestCriusWaitStartsWhereTheOldCursorsStopped(t *testing.T) {
	home := t.TempDir()
	c := filepath.Join(home, ".crius")
	tw := filepath.Join(home, ".claude", "skills", "cmux-team", "scripts")
	for _, d := range []string{filepath.Join(c, "bus"), tw} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	bus, err := filepath.EvalSymlinks(filepath.Join(c, "bus")) // the waiters key their cursors on the real path
	if err != nil {
		t.Fatal(err)
	}
	write := func(p, s string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(bus, "cortex.log"), strings.Repeat("x\n", 60))
	write(filepath.Join(bus, "quotes.log"), strings.Repeat("x\n", 9))
	write(filepath.Join(bus, "aitch.log"), "x\nx\nx\n") // no old cursor names it: it starts at its end
	write(filepath.Join(c, "wait-cursor-cortex"), "40\t"+bus+"/cortex.log\n")
	write(filepath.Join(c, "wait-cursor-cortex.role"), "crius\n")
	write(filepath.Join(c, "wait-cursor-cortex.asks"), "cortex:lead\t100\ncortex:qa\t300\n")
	write(filepath.Join(c, "wait-cursor-old"), "55\t"+bus+"/cortex.log\n7\t"+bus+"/quotes.log\n")
	write(filepath.Join(c, "wait-cursor-old.role"), "crius\n")
	write(filepath.Join(c, "wait-cursor-old.asks"), "cortex:lead\t200\n")
	write(filepath.Join(c, "wait-cursor-q"), "90\t"+bus+"/quotes.log\n") // another role's: not Crius's place
	write(filepath.Join(c, "wait-cursor-q.role"), "quotes:lead\n")
	// the stub waiter prints its arguments and the cursor it was given
	write(filepath.Join(tw, "team-wait.sh"), `echo "$@"; for a in "$@"; do [ "$prev" = --cursor ] && cat "$a" && sort "$a.asks"; prev=$a; done`+"\n")

	run := func() string {
		t.Helper()
		cmd := exec.Command("bash", "../bin/crius-wait", "--max-hours", "1")
		cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatal(err, string(out))
		}
		return string(out)
	}
	out := run()
	want := "--mode crius --role crius --alias crius:lead --digest " + c + "/state/crius/bus-digest.log --cursor " + c +
		"/wait-cursor-all --watch-dir " + bus + " --max-hours 1\n"
	if !strings.HasPrefix(out, want) {
		t.Fatalf("want args %q, got\n%s", want, out)
	}
	cur := strings.TrimPrefix(out, want)
	for _, line := range []string{"55\t" + bus + "/cortex.log\n", "7\t" + bus + "/quotes.log\n", "3\t" + bus + "/aitch.log\n",
		"cortex:lead\t200\ncortex:qa\t300\n"} {
		if !strings.Contains(cur, line) {
			t.Fatalf("missing %q in the merged cursor:\n%s", line, cur)
		}
	}
	if strings.Contains(cur, "90\t") {
		t.Fatalf("another role's cursor was merged:\n%s", cur)
	}
	// a later run keeps its own cursor
	write(filepath.Join(c, "wait-cursor-all"), "60\t"+bus+"/cortex.log\n")
	if out := run(); !strings.HasSuffix(out, "60\t"+bus+"/cortex.log\ncortex:lead\t200\ncortex:qa\t300\n") {
		t.Fatalf("a later run did not keep its cursor:\n%s", out)
	}
}
