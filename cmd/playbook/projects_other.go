//go:build !darwin

package main

import "errors"

// On the other hosts the projects come from the map the Mac writes into the
// book (show -map); the nightly run itself runs only on the Mac.
var errNoProjects = errors.New("projects.toml is read only on the Mac; use show -map")

func projectOf() (func(cwd string) string, error) { return nil, errNoProjects }
func projectCwds() (map[string][]string, error)   { return nil, errNoProjects }
