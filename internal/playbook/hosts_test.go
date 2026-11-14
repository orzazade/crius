package playbook

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// rsyncBin is an rsync with --append, --files-from and --mkpath (3.2.3+). The
// macOS /usr/bin/rsync (openrsync) has not all of them.
func rsyncBin(t *testing.T) string {
	for _, p := range []string{"/opt/homebrew/bin/rsync", "/usr/local/bin/rsync", "/usr/bin/rsync"} {
		if out, err := runOut(p, "--version"); err == nil && strings.Contains(out, "rsync  version 3.") {
			return p
		}
	}
	t.Skip("no rsync 3.x here")
	return ""
}

// fakeSSH writes a remote shell that runs the remote command here: it drops the
// target, as ssh would after connecting. down makes it fail like an ssh that
// cannot connect (exit 255).
func fakeSSH(t *testing.T, down bool) string {
	bin := filepath.Dir(rsyncBin(t)) // the "remote" side runs this rsync too
	p := filepath.Join(t.TempDir(), "ssh")
	body := "#!/bin/sh\nshift\nPATH=" + bin + ":$PATH exec sh -c \"$*\"\n"
	if down {
		body = "#!/bin/sh\necho 'ssh: connect to host x port 22: Operation timed out' >&2\nexit 255\n"
	}
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func remoteSession(t *testing.T, root string, texts ...string) string {
	p := filepath.Join(root, "-Users-x-Projects-scifi-shop", "r1.jsonl")
	writeSession(t, p, texts)
	return p
}

// Crius 11-14: a host that is down is a loud line in the run summary and makes
// the run unhealthy, never a quiet night with nothing read from it.
func TestHostDownIsALoudLine(t *testing.T) {
	cfg, log := busConfig(t)
	appendLine(t, log, time.Now().Add(-time.Hour).Format("2006-01-02T15:04")+" LEARNED shop:a -> crius:lead — testing: split the suite, 26 min to 9 min")
	cfg.CacheDir = filepath.Join(t.TempDir(), "hosts")
	cfg.Hosts = []Host{{Name: "legion", Target: "x", SSH: []string{fakeSSH(t, true)}, Roots: []string{"/nowhere"}, Rsync: rsyncBin(t)}}
	res, err := Run(context.Background(), cfg, &oneEach{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Healthy() || len(res.Hosts) != 1 || !strings.Contains(HostLine(res.Hosts), "legion DOWN") ||
		!strings.Contains(HostLine(res.Hosts), "timed out") {
		t.Fatalf("healthy %v, hosts %q; want unhealthy and a 'legion DOWN … timed out' line", res.Healthy(), HostLine(res.Hosts))
	}
	if res.New != 1 {
		t.Fatalf("new %d: a host that is down must not stop the rest of the night", res.New)
	}
}

// The run pulls new transcript lines into a cache per host (append only), reads
// them like local ones, and pushes the book back to each host.
func TestHostPullReadsAndPushesTheBook(t *testing.T) {
	remote := t.TempDir()
	root := filepath.Join(remote, "projects")
	sess := remoteSession(t, root, "Root cause: the cache key had the date in it, so every run missed. 702 s to 198 s.")
	old := filepath.Join(root, "-Users-x-old", "o.jsonl")
	writeSession(t, old, []string{"Root cause: something from long ago, 10 s to 2 s."})
	past := time.Now().AddDate(0, 0, -40)
	_ = os.Chtimes(old, past, past)
	bookOnHost := filepath.Join(remote, "book")
	cfg := Config{BookDir: filepath.Join(t.TempDir(), "book"), StateDir: filepath.Join(t.TempDir(), "state"),
		CacheDir: filepath.Join(t.TempDir(), "hosts"), Since: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		Hosts: []Host{{Name: "thinkpad", Target: "x", SSH: []string{fakeSSH(t, false)}, Roots: []string{root}, Book: bookOnHost, Rsync: rsyncBin(t)}}}
	res, err := Run(context.Background(), cfg, &oneEach{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Healthy() || res.New != 1 || res.Hosts[0].Files != 1 || !strings.Contains(res.Candidates[0].Ref, filepath.Join("hosts", "thinkpad")) {
		t.Fatalf("healthy %v new %d hosts %+v cands %+v; want 1 entry from the one recent remote file", res.Healthy(), res.New, res.Hosts, res.Candidates)
	}
	for _, f := range []string{"book.json", "SKILL.md", "testing.md"} {
		if _, err := os.Stat(filepath.Join(bookOnHost, f)); err != nil {
			t.Fatalf("book not pushed to the host: %v", err)
		}
	}
	// A line appended on the host arrives as an append to the cached copy.
	f, _ := os.OpenFile(sess, os.O_APPEND|os.O_WRONLY, 0o644)
	fmt.Fprintf(f, `{"type":"assistant","uuid":"r1-new","sessionId":"s","cwd":"/Users/x/Projects/scifi/shop","entrypoint":"cli","timestamp":"2026-11-20T18:00:00Z","message":{"role":"assistant","content":[{"type":"text","text":"Root cause: the lock was per process, not per host. 9 of 9 runs raced, 0 of 9 after."}]}}`+"\n")
	f.Close()
	later := time.Now().Add(time.Minute)
	_ = os.Chtimes(sess, later, later)
	res, err = Run(context.Background(), cfg, &oneEach{})
	if err != nil || len(res.Candidates) != 1 || !strings.Contains(res.Candidates[0].Text, "per host") {
		t.Fatalf("second run: %+v (%v), want only the appended line", res.Candidates, err)
	}
}

// Sessions on another host run in the Mac's own paths, so the project map is
// written with the Mac's absolute folders and read by longest prefix.
func TestProjectMapLongestPrefix(t *testing.T) {
	m := ProjectMap{"quotes": {"/Users/x/Projects/Work/svc_quote"}, "work": {"/Users/x/Projects/Work"}}
	for cwd, want := range map[string]string{
		"/Users/x/Projects/Work/svc_quote/src": "quotes",
		"/Users/x/Projects/Work/svc_other":     "work",
		"/Users/x/Projects/Work/svc_quotes":    "work",
		"/tmp":                                 "",
	} {
		if got := m.Lookup(cwd); got != want {
			t.Errorf("Lookup(%s) = %q, want %q", cwd, got, want)
		}
	}
}
