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
	leads                                        map[string]string // role -> status file holding the GOAL block
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
			Leads        map[string]string
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
		leads := map[string]string{}
		for role, f := range p.Leads {
			if !store.Slug.MatchString(role) {
				return nil, fmt.Errorf("%s: project %s: bad lead role %q", path, slug, role)
			}
			leads[role] = expand(f)
		}
		c.projects = append(c.projects, project{slug: slug, accounts: p.Accounts, cwd: expandAll(p.CWD), repos: expandAll(p.Repos),
			teamLogs: expandAll(p.TeamLogs), artifactDirs: expandAll(p.ArtifactDirs), leads: leads})
	}
	sort.Slice(c.projects, func(i, j int) bool { return c.projects[i].slug < c.projects[j].slug })
	return c, nil
}

// enterpriseOK lists the projects that may run on the enterprise account (Appxite API billing).
// KnownProjects gives the test RedactName needs: whether a slug is a project in home's projects.toml.
func KnownProjects(home string) (func(slug string) bool, error) {
	cfg, err := loadConfig(home)
	if err != nil {
		return nil, err
	}
	return cfg.known, nil
}

// known says whether slug is a configured project.
func (c *config) known(slug string) bool {
	for _, p := range c.projects {
		if p.slug == slug {
			return true
		}
	}
	return false
}

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

// Lead is a lead as it signs bus lines (<project>:<role>) and the status file holding its GOAL block (RULES 3.1).
type Lead struct{ Name, StatusFile string }

// Leads reads projects.toml in home: every lead with its status file, sorted by name (step 5, the digest).
func Leads(home string) ([]Lead, error) {
	c, err := loadConfig(home)
	if err != nil {
		return nil, err
	}
	var out []Lead
	for _, p := range c.projects {
		for role, f := range p.leads {
			out = append(out, Lead{p.slug + ":" + role, f})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
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
