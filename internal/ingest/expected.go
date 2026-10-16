package ingest

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/orzazade/crius/internal/recall"
)

// skipTranscriptDir is a transcript folder the ingest does not read (sessions run from temp folders).
func skipTranscriptDir(name string) bool { return strings.HasPrefix(name, "-private-tmp") }

// Expected lists what one run should read, by the ingest's own rules: transcripts under each root, bus files, team
// logs and repos; and the teams (projects with a non-empty bus file or team logs), whose leads must post.
func Expected(home string) (recall.Expected, error) {
	exp := recall.Expected{Files: map[string][]string{}}
	c, err := loadConfig(home)
	if err != nil {
		return exp, err
	}
	for _, rt := range c.roots {
		real, err := filepath.EvalSymlinks(rt.path)
		if err != nil {
			return exp, err
		}
		err = filepath.WalkDir(real, func(path string, d fs.DirEntry, err error) error {
			switch {
			case err != nil:
				return err
			case d.IsDir() && skipTranscriptDir(d.Name()):
				return filepath.SkipDir
			case !d.IsDir() && strings.HasSuffix(path, ".jsonl"):
				exp.Files["transcript"] = append(exp.Files["transcript"], path)
			}
			return nil
		})
		if err != nil {
			return exp, err
		}
	}
	bus, err := filepath.Glob(filepath.Join(home, "bus", "*.log"))
	if err != nil {
		return exp, err
	}
	sort.Strings(bus)
	exp.Files["bus"] = bus
	for _, p := range c.projects {
		exp.Files["git"] = append(exp.Files["git"], p.repos...)
		exp.Files["team"] = append(exp.Files["team"], p.teamLogs...)
		fi, err := os.Stat(filepath.Join(home, "bus", p.slug+".log"))
		if len(p.teamLogs) > 0 || (err == nil && fi.Size() > 0) {
			exp.Teams = append(exp.Teams, p.slug)
		}
	}
	for k, v := range exp.Files {
		if len(v) == 0 {
			delete(exp.Files, k)
		}
	}
	return exp, nil
}
