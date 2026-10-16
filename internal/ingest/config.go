package ingest

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/orzazade/crius/internal/store"
)

var defaultRoots = map[string]string{"personal": "~/.claude-personal/projects", "work": "~/.claude-work/projects"}

type project struct {
	slug                               string
	cwd, repos, teamLogs, artifactDirs []string
}

type root struct{ persona, path string }

type config struct {
	roots    []root
	projects []project // sorted by slug
}

func expand(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[1:])
		}
	}
	return p
}

func expandAll(ps []string) []string {
	var out []string
	for _, p := range ps {
		out = append(out, expand(p))
	}
	return out
}

// loadConfig reads ~/.crius/projects.toml. Unknown keys are an error, so a typo cannot quietly drop a source.
func loadConfig(home string) (*config, error) {
	var raw struct {
		Transcripts map[string]string
		Projects    map[string]struct {
			Persona      string
			Accounts     []string
			CWD          []string `toml:"cwd"`
			Repos        []string
			TeamLogs     []string `toml:"team_logs"`
			ArtifactDirs []string `toml:"artifact_dirs"`
		}
	}
	path := filepath.Join(home, "projects.toml")
	md, err := toml.DecodeFile(path, &raw)
	if err != nil {
		return nil, err
	}
	if und := md.Undecoded(); len(und) > 0 {
		return nil, fmt.Errorf("%s: unknown keys %v", path, und)
	}
	c := &config{}
	roots := raw.Transcripts
	if len(roots) == 0 {
		roots = defaultRoots
	}
	for persona, p := range roots {
		c.roots = append(c.roots, root{persona, expand(p)})
	}
	sort.Slice(c.roots, func(i, j int) bool { return c.roots[i].persona < c.roots[j].persona })
	for slug, p := range raw.Projects {
		if !store.Slug.MatchString(slug) {
			return nil, fmt.Errorf("%s: bad project name %q", path, slug)
		}
		c.projects = append(c.projects, project{slug: slug, cwd: expandAll(p.CWD), repos: expandAll(p.Repos),
			teamLogs: expandAll(p.TeamLogs), artifactDirs: expandAll(p.ArtifactDirs)})
	}
	sort.Slice(c.projects, func(i, j int) bool { return c.projects[i].slug < c.projects[j].slug })
	return c, nil
}

// projectForCwd maps a folder to the project with the longest matching cwd prefix.
func (c *config) projectForCwd(cwd string) string {
	best, bestLen := "", -1
	for _, p := range c.projects {
		for _, prefix := range p.cwd {
			if cwd != "" && (cwd == prefix || strings.HasPrefix(cwd, strings.TrimRight(prefix, "/")+"/")) && len(prefix) > bestLen {
				best, bestLen = p.slug, len(prefix)
			}
		}
	}
	return best
}
