//go:build darwin

package main

import (
	"fmt"
	"os"

	"github.com/orzazade/crius/internal/ingest"
	"github.com/orzazade/crius/internal/store"
)

// projectOf maps a session folder to its projects.toml project, the name its
// bus log has. It checks projects.toml once before use and reads it again per
// new folder (cached); a read that fails later is said on stderr, once.
func projectOf() (func(cwd string) string, error) {
	h := store.Home()
	if _, _, err := ingest.Filing(h, "", ""); err != nil {
		return nil, fmt.Errorf("projects.toml: %w", err)
	}
	seen := map[string]string{}
	warned := false
	return func(cwd string) string {
		if p, ok := seen[cwd]; ok {
			return p
		}
		p, _, err := ingest.Filing(h, cwd, "")
		if err != nil && !warned {
			warned = true
			fmt.Fprintln(os.Stderr, "playbook: projects.toml:", err, "(folder names used)")
		}
		seen[cwd] = p
		return p
	}, nil
}

// projectCwds is every project's folders from projects.toml.
func projectCwds() (map[string][]string, error) { return ingest.ProjectCwds(store.Home()) }
