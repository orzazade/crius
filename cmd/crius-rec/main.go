// crius-rec: the door to the record store. See internal/cli.
package main

import (
	"os"

	"github.com/orzazade/crius/internal/cli"
)

func main() { os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr)) }
