package playbook

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

// MaxShown is how many entries a session start shows at most.
const MaxShown = 8

var wordRe = regexp.MustCompile(`[a-z0-9]+`)

// Words are the match words of a working folder: its path parts, its team, and
// the stacks its files name (go.mod, package.json deps, *.csproj, charts).
func Words(cwd string) map[string]bool {
	w := map[string]bool{}
	skip := map[string]bool{"users": true, "projects": true}
	if home, err := os.UserHomeDir(); err == nil {
		for _, p := range wordRe.FindAllString(strings.ToLower(home), -1) {
			skip[p] = true // in every path, so it would match every session
		}
	}
	for _, p := range wordRe.FindAllString(strings.ToLower(cwd), -1) {
		if len(p) > 2 && !skip[p] {
			w[p] = true
		}
	}
	w[strings.ToLower(teamOf(cwd))] = true
	has := func(glob string) bool { m, _ := filepath.Glob(filepath.Join(cwd, glob)); return len(m) > 0 }
	if has("go.mod") {
		w["go"] = true
	}
	if has("*.sln") || has("*.csproj") || has("src/*/*.csproj") {
		w["dotnet"], w["csharp"] = true, true
	}
	if has("Chart.yaml") || has("charts") || has("k8s") {
		w["k8s"], w["helm"] = true, true
	}
	if data, err := os.ReadFile(filepath.Join(cwd, "package.json")); err == nil {
		w["node"] = true
		s := string(data)
		for _, dep := range []string{"typeorm", "angular", "react", "nestjs", "vitest", "playwright", "bun"} {
			if strings.Contains(s, dep) {
				w[dep] = true
			}
		}
	}
	return w
}

// Match returns the live entries that fit the folder, best first: an entry's
// team counts most, then each of its keys found in the words. Entries with no
// hit are left out, so a folder nothing fits gets nothing.
func (b *Book) Match(cwd string) []Entry {
	words := Words(cwd)
	team := strings.ToLower(teamOf(cwd))
	type scored struct {
		e     Entry
		score int
	}
	var hits []scored
	for _, e := range b.Live() {
		s := 0
		for _, k := range e.Keys {
			if words[strings.ToLower(k)] {
				s++
			}
		}
		for _, ev := range e.Evidence {
			if strings.ToLower(ev.Team) == team {
				s += 2
				break
			}
		}
		if s > 0 {
			hits = append(hits, scored{e, s})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return len(hits[i].e.Evidence) > len(hits[j].e.Evidence)
	})
	var out []Entry
	for i := 0; i < len(hits) && i < MaxShown; i++ {
		out = append(out, hits[i].e)
	}
	return out
}

// ShowText is what a session start prints, or "" when nothing fits.
func ShowText(es []Entry, bookDir string) string {
	if len(es) == 0 {
		return ""
	}
	var s strings.Builder
	fmt.Fprintf(&s, "Playbook (what the teams learned; full list in %s/SKILL.md):\n", bookDir)
	for _, e := range es {
		fmt.Fprintf(&s, "- [%s] when %s: %s (because %s)\n", clean(e.ID), clause(e.When), clean(e.Do), clause(e.Because))
	}
	return s.String()
}

// LogShown appends one line per shown entry to dir/shown.log, for the
// shown/followed counts.
func LogShown(dir, session, cwd string, es []Entry, now time.Time) error {
	if len(es) == 0 {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "shown.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	var s strings.Builder
	for _, e := range es {
		fmt.Fprintf(&s, "%s\t%s\t%s\t%s\n", now.Format(time.RFC3339), session, e.ID, cwd)
	}
	_, err = f.WriteString(s.String())
	return err
}

// clause makes a sentence read after "when" or "because": no closing stop, and
// a capital first letter lowered unless the word is a name like "SSH" or "T3".
func clause(s string) string {
	s = strings.TrimRight(clean(s), ". ")
	r := []rune(s)
	if len(r) > 1 && unicode.IsUpper(r[0]) && unicode.IsLower(r[1]) {
		r[0] = unicode.ToLower(r[0])
	}
	return string(r)
}
