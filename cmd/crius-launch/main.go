// Command crius-launch opens a team session: crius-launch <project> <role> [max|enterprise]. It prints the workspace,
// the tab and the provenance tag line to send as the first line of the session's first prompt.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/orzazade/crius/internal/ingest"
	"github.com/orzazade/crius/internal/launch"
	"github.com/orzazade/crius/internal/store"
)

func main() {
	if len(os.Args) < 3 || len(os.Args) > 4 {
		fmt.Fprintln(os.Stderr, "usage: crius-launch <project> <role> [max|enterprise]")
		os.Exit(2)
	}
	account := ""
	if len(os.Args) == 4 {
		account = os.Args[3]
	}
	ws, surface, tag, err := launch.Run(os.Args[1], os.Args[2], account, launch.Deps{
		Lock: filepath.Join(store.Home(), "launch.lock"),
		Cmux: func(args ...string) (string, error) {
			out, err := exec.Command("cmux", args...).CombinedOutput()
			return string(out), err
		},
		Place: func(project string) (string, string, error) { return ingest.Place(store.Home(), project) },
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "crius-launch:", err)
		os.Exit(1)
	}
	fmt.Printf("OK %s %s\n%s\n", ws, surface, tag)
}
