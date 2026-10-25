package main

// Per-project guards, from ~/.crius/bus.toml (Quotes 10-25: each session kept them in a private say.sh wrapper and lost
// them on every resume or move; P5 three times). A project's table applies to every line on its bus
// (~/.crius/bus/<project>.log) and to every line its own roles post:
//
//	[projects.quotes]
//	sha_repos = ["~/Projects/Appxite/svc_quote/channels"] # a word of 8-40 hex, not all digits, must be a commit in one
//	id_dirs = ["~/…/scripts"]                             # a hex word that starts a UUID in a file here is an id
//	live_window_min = 60                                  # one live run at a time on this bus (START rule)
//	preready_dir = "~/.crius/state/quotes/preready"       # a READY needs pre-ready's PASS record for a sha it names
//	preready_skip_repos = ["~/…/RethinkBack"]             # a commit only here needs no record
//
// The table lives outside projects.toml because every other Crius binary reads that file strictly and refuses a key it
// does not know.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

const (
	rcNotCommit = 3 // the wrappers' code for a hex word that is no commit
	rcLiveOpen  = 4 // the wrappers' code for a START while a live run is open
	rcNoSession = 5 // posted, but an ORDER or ASK names someone with no live session (or the check failed)
	rcOffClock  = 6 // the wrappers' code for a typed time off the clock
	rcNoRecord  = 7 // a READY names no sha with a pre-ready PASS record (6 is the wrappers' off-clock time)
)

type guards struct {
	ShaRepos      []string `toml:"sha_repos"`
	IDDirs        []string `toml:"id_dirs"`
	LiveWindowMin int      `toml:"live_window_min"`
	// A READY needs pre-ready's PASS record for a sha it names (readyProven), exit 7.
	PrereadyDir       string   `toml:"preready_dir"`
	PrereadySkipRepos []string `toml:"preready_skip_repos"`
}

// loadGuards reads <home>/bus.toml. No file: no project has guards. Unknown keys are an error, so a typo cannot quietly
// switch a guard off.
func loadGuards(home string) (map[string]guards, error) {
	path := filepath.Join(home, "bus.toml")
	var raw struct{ Projects map[string]guards }
	md, err := toml.DecodeFile(path, &raw)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if und := md.Undecoded(); len(und) > 0 {
		return nil, fmt.Errorf("%s: unknown keys %v", path, und)
	}
	for p, g := range raw.Projects {
		if g.LiveWindowMin < 0 {
			return nil, fmt.Errorf("%s: project %s: live_window_min %d is below 0", path, p, g.LiveWindowMin)
		}
	}
	return raw.Projects, nil
}

func expand(p string) string {
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[2:])
		}
	}
	return p
}

// A hex word runs between characters other than letters, digits, '_' and '-': a UUID, a branch or folder name with
// hyphens ("f9-riga-calllogs-767fdd3c") is one longer word, never a sha.
var (
	hexWordRe = regexp.MustCompile(`^[0-9a-f]{8,40}$`)
	hasLetter = regexp.MustCompile(`[a-f]`)
)

func hexWords(text string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(text, func(r rune) bool {
		return !(r == '_' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z')
	}) {
		if hexWordRe.MatchString(w) && hasLetter.MatchString(w) && !slices.Contains(out, w) {
			out = append(out, w)
		}
	}
	return out
}

// checkShas gives the hex words of text that are a commit in no repo and an id in no id dir, less those in
// SAY_NOT_SHA. A repo or dir it cannot read is an error: failed to look is not found nothing.
func checkShas(text string, repos, idDirs []string) ([]string, error) {
	notSha := strings.Split(os.Getenv("SAY_NOT_SHA"), ",")
	for i := range notSha {
		notSha[i] = strings.TrimSpace(notSha[i]) // "a, b"
	}
	var words []string
	for _, w := range hexWords(text) {
		if !slices.Contains(notSha, w) {
			words = append(words, w)
		}
	}
	for _, repo := range repos {
		if len(words) == 0 {
			return nil, nil
		}
		in, err := commits(repo, words)
		if err != nil {
			return nil, err
		}
		words = slices.DeleteFunc(words, func(w string) bool { return in[w] })
	}
	if len(words) == 0 || len(idDirs) == 0 {
		return words, nil
	}
	ids := map[string]bool{}
	// A word is an id when a whole UUID starts with it (review of 80b4c83: "cafebabe-1111-NOT-A-UUID" is none). Words
	// are 8 or more hex, so only one of 8 can start one.
	idRes := make([]*regexp.Regexp, len(words))
	for i, w := range words {
		idRes[i] = regexp.MustCompile(`(?i)(?:^|[^0-9a-z])` + w + `-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}(?:[^0-9a-z]|$)`)
	}
	for _, dir := range idDirs {
		err := filepath.WalkDir(expand(dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			for i, w := range words {
				if !ids[w] && idRes[i].Match(b) {
					ids[w] = true
				}
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("could not check ids in %s: %v", dir, err)
		}
	}
	var left []string
	for _, w := range words {
		if !ids[w] {
			left = append(left, w)
		}
	}
	return left, nil
}

// commits gives which of words are a commit in repo.
func commits(repo string, words []string) (map[string]bool, error) {
	var in bytes.Buffer
	for _, w := range words {
		in.WriteString(w + "^{commit}\n")
	}
	cmd := exec.Command("git", "-C", expand(repo), "cat-file", "--batch-check")
	cmd.Stdin = &in
	// A GIT_DIR (or another repo variable) inherited from the caller would win over -C: check the wrong repo.
	cmd.Env = slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return slices.ContainsFunc([]string{"GIT_DIR=", "GIT_WORK_TREE=", "GIT_COMMON_DIR=", "GIT_OBJECT_DIRECTORY=",
			"GIT_ALTERNATE_OBJECT_DIRECTORIES="}, func(p string) bool { return strings.HasPrefix(kv, p) })
	})
	var errb bytes.Buffer
	cmd.Stderr = &errb
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("could not check shas in %s: %v %s", repo, err, strings.TrimSpace(errb.String()))
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) != len(words) {
		return nil, fmt.Errorf("could not check shas in %s: %d answers for %d words", repo, len(lines), len(words))
	}
	is := map[string]bool{}
	for i, w := range words {
		// git reads "deadbeef^{commit}" as a branch or tag of that name too: only a commit whose sha starts with the word
		// counts (review of 80b4c83).
		if f := strings.Fields(lines[i]); len(f) >= 2 && f[1] == "commit" && strings.HasPrefix(f[0], w) {
			is[w] = true
		}
	}
	return is, nil
}

// readyProven tells whether a READY's text names a sha that pre-ready passed: a record in g.PrereadyDir named by the
// full sha, whose "head" is that sha and whose "line" starts "pre-ready PASS" (Quotes pre-ready.ts writes it), or a
// commit of a preready_skip_repos repo that is a commit in no other sha_repos repo (a RethinkBack fix has no pre-ready;
// quotes:lead 10-25 18:12). A record that does not read is an error: failed to look is not found nothing.
func readyProven(text string, g guards) (bool, error) {
	words := hexWords(text)
	if len(words) == 0 {
		return false, nil
	}
	dir := expand(g.PrereadyDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, fmt.Errorf("could not read pre-ready records: %v", err)
	}
	for _, w := range words {
		for _, e := range entries {
			if !strings.HasPrefix(e.Name(), w) || !hexWordRe.MatchString(e.Name()) {
				continue
			}
			b, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				return false, fmt.Errorf("could not read pre-ready record: %v", err)
			}
			var rec struct{ Head, Line string }
			if err := json.Unmarshal(b, &rec); err != nil {
				return false, fmt.Errorf("could not read pre-ready record %s: %v", filepath.Join(dir, e.Name()), err)
			}
			if rec.Head == e.Name() && strings.HasPrefix(rec.Line, "pre-ready PASS") {
				return true, nil
			}
		}
	}
	for _, skip := range g.PrereadySkipRepos {
		in, err := commits(skip, words)
		if err != nil {
			return false, err
		}
		left := slices.DeleteFunc(slices.Clone(words), func(w string) bool { return !in[w] })
		for _, repo := range g.ShaRepos {
			if len(left) == 0 || slices.Contains(g.PrereadySkipRepos, repo) {
				continue
			}
			other, err := commits(repo, left)
			if err != nil {
				return false, err
			}
			left = slices.DeleteFunc(left, func(w string) bool { return other[w] })
		}
		if len(left) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// openRuns reads the bus from f: each sender whose newest START at or after now-window has no later DONE or
// CORRECTION from that sender. It gives those START lines, cut to 160 characters.
func openRuns(f io.ReaderAt, size int64, now time.Time, window time.Duration) ([]string, error) {
	since := now.Add(-window)
	open := map[string]string{}
	var order []string
	sc := bufio.NewScanner(io.NewSectionReader(f, 0, size))
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		// A stamp ending in Z is UTC, as the bus parser reads it (review of 80b4c83).
		at, err := time.ParseInLocation("2006-01-02T15:04", fields[0], now.Location())
		if z, ok := strings.CutSuffix(fields[0], "Z"); ok {
			at, err = time.Parse("2006-01-02T15:04", z)
		}
		if err != nil {
			continue
		}
		switch who := fields[2]; fields[1] {
		case "START":
			if at.Before(since) {
				delete(open, who)
				continue
			}
			if _, ok := open[who]; !ok {
				order = append(order, who)
			}
			open[who] = line
		case "DONE", "CORRECTION":
			delete(open, who)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	var out []string
	for _, who := range order {
		if l, ok := open[who]; ok && !slices.Contains(out, l) {
			if r := []rune(l); len(r) > 160 {
				l = string(r[:160])
			}
			out = append(out, l)
		}
	}
	return out, nil
}

// upperName gives the first recipient name of to with a project part and an upper-case letter, or "".
func upperName(to string) string {
	for _, n := range strings.FieldsFunc(to, func(r rune) bool { return r == ',' || r == ' ' }) {
		if strings.Contains(n, ":") && strings.ToLower(n) != n {
			return n
		}
	}
	return ""
}

func logProject(log string) string {
	return strings.TrimSuffix(filepath.Base(log), ".log")
}
