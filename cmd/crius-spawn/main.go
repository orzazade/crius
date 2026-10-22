// Command crius-spawn opens a team session and starts it:
//
//	crius-spawn [-account max|enterprise] [-parent <leader session id>] [-item <project>#<n>] <project> <role> <first prompt…>
//
// The tab lands in the project's workspace (made, named and coloured when missing), is named "<project> · <role>",
// gets the folder-trust dialog answered, its model checked, and the provenance tag line typed before the first prompt.
// It prints the workspace, the tab and the tag, and fails loudly when any step does not show on screen.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/ingest"
	"github.com/orzazade/crius/internal/launch"
	"github.com/orzazade/crius/internal/store"
)

func main() {
	account := flag.String("account", "", "max or enterprise (default: the project's first account in projects.toml)")
	parent := flag.String("parent", "", "the leader's session id, for the provenance tag")
	item := flag.String("item", "", "the work item, <project>#<n>, for the provenance tag")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: crius-spawn [-account max|enterprise] [-parent id] [-item project#n] <project> <role> <first prompt…>")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() < 3 {
		flag.Usage()
		os.Exit(2)
	}
	s := launch.Spawn{Project: flag.Arg(0), Role: flag.Arg(1), Account: *account, Parent: *parent, Item: *item,
		Prompt: strings.Join(flag.Args()[2:], " ")}
	ws, surface, tag, err := launch.Run(s, launch.Deps{
		Lock:  filepath.Join(store.Home(), "launch.lock"),
		Cmux:  runCmux,
		Place: func(project string) (string, string, string, error) { return ingest.Place(store.Home(), project) },
		Wait:  time.Second,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "crius-spawn:", err)
		os.Exit(1)
	}
	fmt.Printf("OK %s %s\n%s\n", ws, surface, tag)
}

// runCmux gives cmux's stdout, or on failure its stdout and stderr: a notice on stderr must not reach a parse.
func runCmux(args ...string) (string, error) {
	var stderr strings.Builder
	cmd := exec.Command("cmux", args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return string(out) + stderr.String(), err
	}
	return string(out), nil
}
