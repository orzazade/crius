// Command crius-runlog writes the run log for a check done by hand (a git tree compare, a deploy run read), which a
// VERIFIED bus line must name:
//
//	crius-runlog <name> <sha> <pass N> <cmd> [notes...]
//
// It writes ~/.crius/runs/<name>.md ($CRIUS_HOME/runs when set) with the header bus-post reads: `sha: <sha>`,
// `pass: <N>`, `cmd: <cmd>`, then one line per note. The header is built and read back with internal/runlog, the code
// bus-post checks it with, so a log it writes is one bus-post takes. N is a bare whole number of 1 or more; the sha is
// 7 to 40 lower-case hex. It never replaces a file that is there. It prints the path to name in the VERIFIED line.
// On 11-19 four hand-written run logs were refused for header form (`pass:` with words after the number).
//
// Exit codes: 0 written, 2 refused (nothing written).
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
	if len(args) < 4 {
		fmt.Fprintln(stderr, "usage: crius-runlog <name> <sha> <pass N> <cmd> [notes...]")
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
