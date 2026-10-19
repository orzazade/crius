// crius-statusline is the statusLine command: it records a quota sample (design §5) and prints the quota line.
// A failure is printed in the statusline itself, with exit 0 because Claude Code shows nothing for a non-zero exit,
// so a broken sampler is seen at once; the ingest also alerts when open sessions have no sample for 45 minutes.
// The work line also shows model + account, branch, cost and context %, and the Enterprise fallback on an API-billed
// session (Crius ORDER 10-19 15:25); --refresh-ent is the detached refresh of that fallback's cache.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/orzazade/crius/internal/quota"
	"github.com/orzazade/crius/internal/store"
)

func main() {
	persona := flag.String("persona", "", "personal or work: whose statusline this is")
	refreshEnt := flag.Bool("refresh-ent", false, "refresh the Enterprise fallback cache (started by the work line)")
	flag.Parse()
	if *refreshEnt {
		if err := quota.RefreshEnt(store.Home(), quota.ReadWorkEnt, time.Now()); err != nil { // stderr: logs/ent-refresh.log
			fmt.Fprintf(os.Stderr, "%s crius-statusline --refresh-ent: %v\n", time.Now().Format(time.RFC3339), err)
			os.Exit(1)
		}
		return
	}
	if *persona != "personal" && *persona != "work" {
		fmt.Println("crius-statusline: FAILED, needs --persona personal|work")
		return
	}
	in, err := io.ReadAll(os.Stdin)
	if err == nil && *persona == "work" {
		fmt.Println(quota.Work(store.Home(), in, time.Now(), quota.Account(configDir()), branch, refresh))
		return
	}
	if err == nil {
		var out string
		out, err = quota.Sample(store.Home(), *persona, in, time.Now())
		if err == nil {
			fmt.Println(out)
			return
		}
	}
	fmt.Printf("crius quota sample FAILED: %v\n", err)
}

// configDir is the Claude Code config dir this session runs with.
func configDir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	h, _ := os.UserHomeDir()
	return h
}

// branch is dir's git branch, before the first commit too: "HEAD" when detached, "" outside a repository, "git?" when
// git fails otherwise or takes over a second.
func branch(dir string) string {
	if dir == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "symbolic-ref", "-q", "--short", "HEAD")
	cmd.WaitDelay = 100 * time.Millisecond // a child of git holding the output open must not hold the line
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return strings.TrimSpace(string(out))
	case ctx.Err() != nil || !errors.As(err, &ee):
		return "git?"
	case ee.ExitCode() == 1:
		return "HEAD"
	case ee.ExitCode() == 128 && strings.Contains(stderr.String(), "not a git repository"):
		return ""
	}
	return "git?"
}

// refresh starts this binary with --refresh-ent in its own session, its errors to logs/ent-refresh.log, and does not
// wait for it.
func refresh() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	log, err := os.OpenFile(filepath.Join(store.Home(), "logs", "ent-refresh.log"), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd := exec.Command(filepath.Clean(exe), "--refresh-ent")
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
