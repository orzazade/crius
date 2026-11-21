package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/orzazade/crius/internal/playbook"
	"github.com/orzazade/crius/internal/tabsend"
)

// fakeBox makes a box the test reaches through a fake ssh: the box's home is box (its ~), with the real bus-post built
// into ~/.crius/bin, as crius-install-box puts it there. The fake ssh logs every line it is given to sshlog; a lock-hold
// line runs on the "box" (HOME=box, no CRIUS_HOME), every other line fails, so nothing reaches a real pane. A tmux line
// that runs before the file mark exists is logged as TOUCHED WHILE LOCKED.
func fakeBox(t *testing.T) (box, sshlog, mark string) {
	t.Helper()
	box, dir := t.TempDir(), t.TempDir()
	sshlog, mark = filepath.Join(dir, "ssh.log"), filepath.Join(dir, "released")
	bin := filepath.Join(box, ".crius", "bin", "bus-post")
	build := exec.Command("go", "build", "-o", bin, "../bus-post")
	build.Env = append(os.Environ(), "GOTOOLCHAIN=local")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build bus-post: %v\n%s", err, out)
	}
	ssh := filepath.Join(dir, "ssh")
	script := `#!/bin/sh
for a; do line=$a; done
case $line in
*--hold-lock*) echo "hold: $line" >>"$SSHLOG"; unset CRIUS_HOME; HOME="$BOX" exec /bin/sh -c "$line" ;;
esac
if [ -e "$MARK" ]; then echo "tmux: $line" >>"$SSHLOG"; else echo "TOUCHED WHILE LOCKED: $line" >>"$SSHLOG"; fi
exit 1
`
	if err := os.WriteFile(ssh, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	h := os.Getenv("CRIUS_HOME")
	hosts := `[{"name":"legion","target":"u@h","ssh":["` + ssh + `"]}]`
	if err := os.WriteFile(filepath.Join(h, "state", "playbook", "hosts.json"), []byte(hosts), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSHLOG", sshlog)
	t.Setenv("BOX", box)
	t.Setenv("MARK", mark)
	return box, sshlog, mark
}

// holdBoxLock holds the box's typing lock as bus-post on the box holds it while it types a wake line (the kernel's
// flock on ~/.crius/state/tsend.lock), and gives the func that lets it go.
func holdBoxLock(t *testing.T, box string) func() {
	t.Helper()
	path := filepath.Join(box, ".crius", "state", "tsend.lock")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	return func() { f.Close() }
}

// bus-post on the box holds the box's lock while it types a wake line into a pane. A crius-tsend from the Mac into that
// box must wait for that lock, not type over it: two typers in one pane mix their keys. Before the fix crius-tsend
// locked only the Mac's file, so it typed at once.
func TestASendWaitsForTheBoxLockBusPostHolds(t *testing.T) {
	home(t)
	box, sshlog, mark := fakeBox(t)
	release := holdBoxLock(t, box)
	go func() {
		time.Sleep(time.Second)
		if err := os.WriteFile(mark, nil, 0o600); err != nil {
			t.Error(err)
		}
		release()
	}()
	var out, errs bytes.Buffer
	code := run([]string{"legion", "quotes:qa", "hello"}, &out, &errs, sshRunner, sshHold)
	log, _ := os.ReadFile(sshlog)
	if strings.Contains(string(log), "TOUCHED WHILE LOCKED") {
		t.Fatalf("crius-tsend touched the pane while bus-post on the box held the box's lock (exit %d, said %q):\n%s",
			code, errs.String(), log)
	}
	if !strings.Contains(string(log), "tmux: ") {
		t.Fatalf("crius-tsend never reached the pane after the lock was let go (exit %d, said %q):\n%s", code, errs.String(), log)
	}
	unlock, err := tabsend.Lock(filepath.Join(box, ".crius", "state", "tsend.lock"), 100*time.Millisecond)
	if err != nil {
		t.Fatalf("the box lock stayed held after crius-tsend ended: %v", err)
	}
	unlock()
}

// freeLock is a box lock nobody else holds.
func freeLock(playbook.Host) (func(), error) { return func() {}, nil }

// A box lock held for the whole wait is NOT SENT with the reason, exit 2, and the pane is never touched.
func TestASendGivesUpWhenTheBoxLockStaysHeld(t *testing.T) {
	home(t)
	box, sshlog, _ := fakeBox(t)
	defer holdBoxLock(t, box)()
	old := tabsend.LockWait
	tabsend.LockWait = 300 * time.Millisecond
	defer func() { tabsend.LockWait = old }()
	var out, errs bytes.Buffer
	code := run([]string{"legion", "quotes:qa", "hello"}, &out, &errs, sshRunner, sshHold)
	log, _ := os.ReadFile(sshlog)
	want := "NOT SENT legion quotes:qa: nothing typed: the box's typing lock: another sender held the typing lock"
	if code != 2 || !strings.Contains(errs.String(), want) || strings.Contains(errs.String(), "crius-install-box") {
		t.Errorf("exit %d, said %q %q; want exit 2 saying %q and no install hint", code, out.String(), errs.String(), want)
	}
	if strings.Contains(string(log), "tmux") || strings.Contains(string(log), "TOUCHED") {
		t.Errorf("the pane was touched while the box lock stayed held:\n%s", log)
	}
}

// A box whose bus-post cannot hold the lock (one older than this crius-tsend) is NOT SENT, exit 2, with the fix named.
func TestASendToABoxWhoseBusPostCannotHoldSaysHowToFixIt(t *testing.T) {
	home(t)
	var out, errs bytes.Buffer
	hold := func(playbook.Host) (func(), error) {
		return tabsend.HoldOver(exec.Command("/bin/sh", "-c", "echo 'usage: bus-post [--at …] <log> …' >&2; exit 2"), time.Second)
	}
	code := run([]string{"legion", "quotes:qa", "hello"}, &out, &errs, pane(true), hold)
	if code != 2 || !strings.Contains(errs.String(), "usage: bus-post") || !strings.Contains(errs.String(), "bin/crius-install-box legion") {
		t.Errorf("exit %d, said %q; want exit 2 with the holder's words and the install line", code, errs.String())
	}
}
