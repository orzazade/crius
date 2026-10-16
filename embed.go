// Package crius holds the files that are built into the Crius binaries.
package crius

import _ "embed"

// ProjectsExample is the starting ~/.crius/projects.toml that `crius-rec init` writes.
//
//go:embed projects.example.toml
var ProjectsExample []byte
