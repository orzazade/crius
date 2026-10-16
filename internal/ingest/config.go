package ingest

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/orzazade/crius/internal/quota"
	"github.com/orzazade/crius/internal/store"
)

var defaultRoots = map[string]string{"personal": "~/.claude-personal/projects", "work": "~/.claude-work/projects"}

type project struct {
	slug                                         string
	accounts, cwd, repos, teamLogs, artifactDirs []string
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
		for _, a := range p.Accounts {
			if a != "max" && a != "enterprise" {
				return nil, fmt.Errorf("%s: project %s: unknown account %q", path, slug, a)
			}
		}
		c.projects = append(c.projects, project{slug: slug, accounts: p.Accounts, cwd: expandAll(p.CWD), repos: expandAll(p.Repos),
			teamLogs: expandAll(p.TeamLogs), artifactDirs: expandAll(p.ArtifactDirs)})
	}
	sort.Slice(c.projects, func(i, j int) bool { return c.projects[i].slug < c.projects[j].slug })
	return c, nil
}

// enterpriseOK lists the projects that may run on the enterprise account (Appxite API billing).
func (c *config) enterpriseOK() map[string]bool {
	ok := map[string]bool{}
	for _, p := range c.projects {
		for _, a := range p.accounts {
			if a == "enterprise" {
				ok[p.slug] = true
			}
		}
	}
	return ok
}

func (c *config) quotaProjects() quota.Projects {
	return quota.Projects{EnterpriseOK: c.enterpriseOK(), ForCwd: c.projectForCwd}
}

// QuotaProjects reads projects.toml in home: which projects may use the enterprise account, and folder -> project.
func QuotaProjects(home string) (quota.Projects, error) {
	c, err := loadConfig(home)
	if err != nil {
		return quota.Projects{}, err
	}
	return c.quotaProjects(), nil
}

// titleRe is a session's -n title as leads name them: "<project>[-build]: <role>" or "<project> · <role>".
var titleRe = regexp.MustCompile(`^\s*([a-z0-9][a-z0-9-]*)\s*(?:(?::|·)\s*(.*?))?\s*$`)

// filing is how a session with no tag is filed (Crius 00:18: team cost is read by project and role): the project its
// -n title names (as written, else without "-build") when that project is configured, else its folder's; the role is
// the title's part after the separator when it is one word, else unknown (prose like "fix: flaky test" is no role).
// byTitle says the title gave either.
func (c *config) filing(cwd, title string) (project, role string, byTitle bool) {
	project, role = c.projectForCwd(cwd), "unknown"
	m := titleRe.FindStringSubmatch(strings.ToLower(title))
	if m == nil {
		return project, role, false
	}
	for _, name := range []string{m[1], strings.TrimSuffix(m[1], "-build")} {
		for _, p := range c.projects {
			if !byTitle && p.slug == name {
				project, byTitle = name, true
			}
		}
	}
	if r := m[2]; store.Slug.MatchString(r) {
		role, byTitle = r, true
	}
	return project, role, byTitle
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
