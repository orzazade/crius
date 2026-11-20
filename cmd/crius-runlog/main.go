// Command crius-runlog writes the run log a VERIFIED bus line must name. Two forms:
//
//	crius-runlog run <project> <ref> <sha> -- <cmd> [args...]
//	crius-runlog <name> <sha> <pass N> <cmd> [notes...]
//
// The run form runs the check command itself (no shell: give `sh -c '…'` for pipes) and writes
// ~/.crius/runs/<project>-<ref>-<local time>.md ($CRIUS_HOME/runs when set): `sha: <sha>`, `pass: 1` only when the
// command exits 0 (else `fail: 1`), `cmd:`, `exit:` and the cut output. It is for a check Crius does by a real command
// (git log of a deploy commit, a gh run, a live HTTP call, a test re-run); on 11-20/11-21 such checks posted no
// VERIFIED because the log had to be written by hand.
//
// The second form writes a hand check's log with a pass count typed in: `sha: <sha>`, `pass: <N>`, `cmd: <cmd>`, then
// one line per note. N is a bare whole number of 1 or more. On 11-19 four hand-written run logs were refused for
// header form (`pass:` with words after the number).
//
// Both build the header and read it back with internal/runlog, the code bus-post checks it with, so a log it writes
// is one bus-post takes. The sha is 7 to 40 lower-case hex. It never replaces a file that is there. It prints the
// path to name in the VERIFIED line.
//
// Exit codes: 0 written (the check passed), 1 the check failed (a `fail:` log is written; no VERIFIED can name it),
// 2 refused (nothing written).
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/orzazade/crius/internal/runlog"
	"github.com/orzazade/crius/internal/store"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "run" {
		return check(args[1:], stdout, stderr)
	}
	if len(args) < 4 {
		fmt.Fprintln(stderr, "usage: crius-runlog run <project> <ref> <sha> -- <cmd> [args...]\n"+
			"       crius-runlog <name> <sha> <pass N> <cmd> [notes...]")
		return 2
	}
	name, sha, pass, cmd := args[0], args[1], args[2], args[3]
	path, n, err := runlog.Write(filepath.Join(store.Home(), "runs"), name, sha, pass, cmd, args[4:])
	if err != nil {
		fmt.Fprintf(stderr, "crius-runlog: refused: %v\n", err)
		return 2
	}
	fmt.Fprintf(stdout, "RUNLOG %s sha=%s pass=%d\nname it in the VERIFIED line: <ref> %s · … %s\n", path, sha, n, sha, path)
	return 0
}

// check is the run form: it runs the command, echoing its output to stderr, and writes its log.
func check(args []string, stdout, stderr io.Writer) int {
	if len(args) < 5 || args[3] != "--" {
		fmt.Fprintln(stderr, "usage: crius-runlog run <project> <ref> <sha> -- <cmd> [args...]")
		return 2
	}
	project, ref, sha := args[0], args[1], args[2]
	path, exit, err := runlog.Check(filepath.Join(store.Home(), "runs"), project, ref, sha, args[4:], stderr)
	if err != nil {
		fmt.Fprintf(stderr, "crius-runlog: refused: %v\n", err)
		return 2
	}
	if exit != 0 {
		fmt.Fprintf(stderr, "crius-runlog: the check failed (exit %d); its log %s says `fail: 1`, so no VERIFIED line can name it\n", exit, path)
		return 1
	}
	fmt.Fprintf(stdout, "RUNLOG %s sha=%s pass=1\nname it in the VERIFIED line: %s %s · … %s\n", path, sha, ref, sha, path)
	return 0
}
