// crius-ingest: one incremental pass over every source into ~/.crius/state/record.db. See internal/ingest.
package main

import (
	"os"

	"github.com/orzazade/crius/internal/ingest"
)

func main() { os.Exit(ingest.Main(os.Args[1:], os.Stdout, os.Stderr)) }
