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
	slug, persona, launch                        string
	accounts, cwd, repos, teamLogs, artifactDirs []string
	leads                                        map[string]Lead // role -> status file holding the GOAL block (no Name)
	pauseAt                                      int             // Max 7d share at which the project pauses; 0: none
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
			Launch       string
			Repos        []string
			TeamLogs     []string `toml:"team_logs"`
			ArtifactDirs []string `toml:"artifact_dirs"`
			Leads        map[string]string
			PauseAt      *int `toml:"pause_at"`
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
		pauseAt := 0
		if a := p.PauseAt; a != nil {
			if *a < 1 || *a > 100 {
				return nil, fmt.Errorf("%s: project %s: pause_at %d is not a percent from 1 to 100", path, slug, *a)
			}
			pauseAt = *a
		}
		leads := map[string]Lead{}
		for role, f := range p.Leads {
			if !store.Slug.MatchString(role) {
				return nil, fmt.Errorf("%s: project %s: bad lead role %q", path, slug, role)
			}
			g, ok := strings.CutPrefix(f, "git:")
			if !ok {
				leads[role] = Lead{StatusFile: expand(f)}
				continue
			}
			repo, object, _ := strings.Cut(g, "#")
			rev, file, _ := strings.Cut(object, ":")
			if repo == "" || rev == "" || file == "" {
				return nil, fmt.Errorf("%s: project %s: lead %s: want git:<repo>#<rev>:<path>, got %q", path, slug, role, f)
			}
			leads[role] = Lead{StatusFile: object, Repo: expand(repo)}
		}
		cwd := expandAll(p.CWD)
		for i, d := range cwd {
			if d != "" { // "c/" and "./d" match as the folder they name (review round 10)
				cwd[i] = filepath.Clean(d)
			}
		}
		c.projects = append(c.projects, project{slug: slug, persona: p.Persona, launch: expand(p.Launch), accounts: p.Accounts, cwd: cwd, repos: expandAll(p.Repos),
			teamLogs: expandAll(p.TeamLogs), artifactDirs: expandAll(p.ArtifactDirs), leads: leads, pauseAt: pauseAt})
	}
	sort.Slice(c.projects, func(i, j int) bool { return c.projects[i].slug < c.projects[j].slug })
	return c, nil
}

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

// quotaProjects also reads accounts.toml in home: the report names each login by its row.
func (c *config) quotaProjects(home string) (quota.Projects, error) {
	as, err := quota.LoadAccounts(home)
	if err != nil {
		return quota.Projects{}, err
	}
	return quota.Projects{EnterpriseOK: c.enterpriseOK(), ForCwd: c.projectForCwd, Accounts: as}, nil
}

// QuotaProjects reads projects.toml in home: which projects may use the enterprise account, and folder -> project; and
// accounts.toml: the name of each login.
func QuotaProjects(home string) (quota.Projects, error) {
	c, err := loadConfig(home)
	if err != nil {
		return quota.Projects{}, err
	}
	return c.quotaProjects(home)
}

// Lead is a lead as it signs bus lines (<project>:<role>) and the status file holding its GOAL block (RULES 3.1).
// With Repo set, StatusFile is a git object in that repo, <rev>:<path>: projects.toml names it
// "git:<repo>#<rev>:<path>" for a lead that commits its file elsewhere than a checkout (Crius 10-19 18:00).
type Lead struct{ Name, StatusFile, Repo string }

// Leads reads projects.toml in home: every lead with its status file, sorted by name (step 5, the digest).
func Leads(home string) ([]Lead, error) {
	c, err := loadConfig(home)
	if err != nil {
		return nil, err
	}
	var out []Lead
	for _, p := range c.projects {
		for role, l := range p.leads {
			l.Name = p.slug + ":" + role
			out = append(out, l)
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
func (c *config) filing(cwd, title string, recorded *store.Worktree) (project, role string, byTitle bool) {
	project, role = c.projectForCwd(cwd, recorded), "unknown"
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
	r := m[2]
	if before, account, ok := strings.Cut(r, " · "); ok && store.Slug.MatchString(account) {
		r = before // "<project> · <role> · <account>" on a picked Max account
	}
	if store.Slug.MatchString(r) {
		role, byTitle = r, true
	}
	return project, role, byTitle
}

// Filing reads projects.toml in home and files a session as ingest does (see filing): crius-hook's project and role.
func Filing(home, cwd, title string) (project, role string, err error) {
	c, err := loadConfig(home)
	if err != nil {
		return "", "", err
	}
	project, role, _ = c.filing(cwd, title, nil)
	return project, role, nil
}

// projectForCwd maps a folder to the project with the longest matching cwd prefix. A folder in a linked git worktree
// is mapped as the same place in its main checkout, unless a project names the worktree or a folder in it (Crius
// 22:46: quotes builders ran in <parent>/.worktrees/quotes-*, which only the parent folder's project named). A
// session's record of its folder (store.Worktrees, nil for none) comes before the disk, so a worktree since removed or
// made something else keeps the session where it was; one removed before it was recorded stays under its folder's
// project.
func (c *config) projectForCwd(cwd string, recorded *store.Worktree) string {
	best, bestLen := c.longestCwd(cwd)
	var wt, main string
	if recorded != nil {
		wt, main = recorded.Path, recorded.Main
	} else {
		wt, main = worktreeOf(cwd)
	}
	if wt != "" && bestLen < len(wt) {
		if p, _ := c.longestCwd(main + strings.TrimPrefix(cwd, wt)); p != "" {
			return p
		}
	}
	return best
}

// longestCwd gives the project whose cwd prefix of cwd is the longest, and that prefix's length (-1 for none).
func (c *config) longestCwd(cwd string) (string, int) {
	best, bestLen := "", -1
	for _, p := range c.projects {
		for _, prefix := range p.cwd {
			if cwd != "" && (cwd == prefix || strings.HasPrefix(cwd, strings.TrimRight(prefix, "/")+"/")) && len(prefix) > bestLen {
				best, bestLen = p.slug, len(prefix)
			}
		}
	}
	return best, bestLen
}

// worktreeOf is diskWorktree; tests swap it to remove a worktree between two looks.
var worktreeOf = diskWorktree

// diskWorktree gives the root of the linked git worktree holding dir and its main checkout, or "" when dir is in none.
// A linked worktree's .git is a file "gitdir: <common>/worktrees/<name>", and that folder's commondir file names the
// common .git (a submodule's .git file names a folder with no commondir: it is no worktree).
func diskWorktree(dir string) (wt, main string) {
	if !filepath.IsAbs(dir) {
		return "", "" // "" is no folder: Clean would make it the process's own
	}
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if fi, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			if fi.IsDir() {
				return "", ""
			}
			b, err := os.ReadFile(filepath.Join(d, ".git"))
			gitdir, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir: ")
			if err != nil || !ok {
				return "", ""
			}
			if !filepath.IsAbs(gitdir) {
				gitdir = filepath.Join(d, gitdir)
			}
			cb, err := os.ReadFile(filepath.Join(gitdir, "commondir"))
			if err != nil {
				return "", ""
			}
			common := strings.TrimSpace(string(cb))
			if !filepath.IsAbs(common) {
				common = filepath.Join(gitdir, common)
			}
			if filepath.Base(common) != ".git" {
				return "", ""
			}
			return d, filepath.Dir(common)
		}
		if d == filepath.Dir(d) {
			return "", ""
		}
	}
}

// Place gives where a project's sessions start, on which account and for which persona: its launch folder (its one
// cwd when it has no launch), first account and persona in projects.toml (crius-spawn). cwd is the list of folders
// whose sessions belong to the project, so the first of several is no place to start (qa3 10-22: quotes opened in
// svc_cpq). The launch folder must be filed under the project, or its sessions would land under another.
func Place(home, slug string) (cwd, account, persona string, err error) {
	c, err := loadConfig(home)
	if err != nil {
		return "", "", "", err
	}
	for _, p := range c.projects {
		if p.slug != slug {
			continue
		}
		if len(p.cwd) == 0 || len(p.accounts) == 0 {
			return "", "", "", fmt.Errorf("projects.toml: project %s needs cwd and accounts to be launched", slug)
		}
		launch := p.launch
		if launch == "" && len(p.cwd) > 1 {
			return "", "", "", fmt.Errorf("projects.toml: project %s has %d cwd folders; set launch to the folder its sessions start in", slug, len(p.cwd))
		} else if launch == "" {
			launch = p.cwd[0]
		}
		// The folder as cmux and claude see it: absolute, with no "..", and there before a tab opens (review round 10).
		if !filepath.IsAbs(launch) {
			return "", "", "", fmt.Errorf("projects.toml: project %s: launch %s must be an absolute folder (or start with ~/)", slug, launch)
		}
		launch = filepath.Clean(launch)
		if fi, err := os.Stat(launch); err != nil || !fi.IsDir() {
			return "", "", "", fmt.Errorf("projects.toml: project %s: launch %s: no folder there", slug, launch)
		}
		if got := c.projectForCwd(launch, nil); got != slug {
			return "", "", "", fmt.Errorf("projects.toml: project %s: launch %s is filed under project %q, not under %s", slug, launch, got, slug)
		}
		return launch, p.accounts[0], p.persona, nil
	}
	return "", "", "", fmt.Errorf("projects.toml has no project %s", slug)
}

// Persona gives the persona projects.toml names for a project (crius-spawn -resume: the session's own folder is its
// cwd, so it needs no launch folder).
func Persona(home, slug string) (string, error) {
	c, err := loadConfig(home)
	if err != nil {
		return "", err
	}
	for _, p := range c.projects {
		if p.slug == slug {
			return p.persona, nil
		}
	}
	return "", fmt.Errorf("projects.toml has no project %s", slug)
}

// ForSpawn gives what crius-spawn needs to pick a Max account for a project: its persona's transcript folder ("" when
// the project names no persona with one) and its pause_at (0: none).
func ForSpawn(home, slug string) (root string, pauseAt int, err error) {
	c, err := loadConfig(home)
	if err != nil {
		return "", 0, err
	}
	for _, p := range c.projects {
		if p.slug != slug {
			continue
		}
		for _, r := range c.roots {
			if r.persona == p.persona {
				root = r.path
			}
		}
		return root, p.pauseAt, nil
	}
	return "", 0, fmt.Errorf("projects.toml has no project %s", slug)
}
