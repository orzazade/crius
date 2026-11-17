package wakewatch

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/store"
)

// Exit codes of crius-wakewatch restart --from-ready, beside the Restart ones.
const (
	RestartNoBrief = 7 // no brief could be built (no READY-RESTART line, an old one, no template, step 6 not once): nothing sent or saved
	RestartUnsaved = 8 // the restart went (tmux SENT, T3 READY) but the new template could not be saved: it still holds the old step 6
)

// RestartBriefsFile is the table of where each project keeps its restart brief templates, one
// "<project> <host> <path>" per line ("#" starts a comment). <host> is "mac" for this machine or a box name in the
// playbook's hosts.json; <path> holds "{role}" where the role's name goes (~/.crius/state/demo/RESTART-{role}.md).
func RestartBriefsFile(home string) string { return filepath.Join(home, "restart-briefs.txt") }

// BriefPlace is one row of RestartBriefsFile.
type BriefPlace struct{ Project, Host, Path string }

// briefPathRe keeps a template path to plain path characters, so it is never shell code on a box.
var briefPathRe = regexp.MustCompile(`^(?:~/|/)[A-Za-z0-9_./{}@+-]+$`)

// roleNameRe is one role name as the buses write it (qa, ms-flow, builder-2).
var roleNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// ReadBriefPlaces reads RestartBriefsFile. A missing file is an error here: --from-ready cannot work without it.
func ReadBriefPlaces(home string) ([]BriefPlace, error) {
	path := RestartBriefsFile(home)
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []BriefPlace
	for n, l := range strings.Split(string(b), "\n") {
		l, _, _ = strings.Cut(l, "#")
		f := strings.Fields(l)
		if len(f) == 0 {
			continue
		}
		if len(f) != 3 {
			return nil, fmt.Errorf("%s line %d: want \"<project> <host> <path with {role}>\", got %q", path, n+1, strings.TrimSpace(l))
		}
		if !briefPathRe.MatchString(f[2]) || strings.Contains(f[2], "..") || !strings.Contains(f[2], "{role}") {
			return nil, fmt.Errorf("%s line %d: path %q must start with ~/ or /, hold {role}, and use only plain path characters", path, n+1, f[2])
		}
		out = append(out, BriefPlace{strings.ToLower(f[0]), f[1], f[2]})
	}
	return out, nil
}

// FromReadyDeps is what RestartFromReady reads and does beside Restart's own. Read gives a file on a host ("mac" or a
// box); Save first copies path to prev (failing when prev exists), then writes body to path with path's mode kept.
type FromReadyDeps struct {
	RestartDeps
	Now  time.Time
	Read func(host, path string) ([]byte, error)
	Save func(host, path, prev string, body []byte) error
}

// ReadyBrief is a built restart brief: where its template lives and the full text to send.
type ReadyBrief struct {
	Host, Path, Prev string
	Text             string
}

// step6 is the start of the line a restart brief's step 6 holds; it must start exactly one line of the template.
const step6 = "6. Then continue with:"

// BuildFromReady builds a role's restart brief from its newest READY-RESTART bus line and its template. Every way it
// cannot is an error that names the reason; it reads and never writes.
func BuildFromReady(d FromReadyDeps, role string) (ReadyBrief, error) {
	project, name, ok := strings.Cut(role, ":")
	if !ok || project == "" || !roleNameRe.MatchString(name) {
		return ReadyBrief{}, fmt.Errorf("give the role as <project>:<role> (demo:qa), not %q", role)
	}
	places, err := ReadBriefPlaces(d.Home)
	if err != nil {
		return ReadyBrief{}, fmt.Errorf("could not read the template table: %w", err)
	}
	i := slices.IndexFunc(places, func(p BriefPlace) bool { return p.Project == project })
	if i < 0 {
		return ReadyBrief{}, fmt.Errorf("%s names no template place for project %s", RestartBriefsFile(d.Home), project)
	}
	place := places[i]
	path := strings.ReplaceAll(place.Path, "{role}", name)

	busPath := filepath.Join(d.Home, "bus", project+".log")
	ready, later, own, err := readyLines(busPath, project, name)
	if err != nil {
		return ReadyBrief{}, err
	}
	if len(own) > 0 { // the role went on working (or a fresh session of it started): its line no longer says where it is
		last := own[len(own)-1]
		return ReadyBrief{}, fmt.Errorf("%s posted %d line(s) after its %s READY-RESTART line (the newest %s %s) in %s: no "+
			"READY-RESTART line newer than the last restart or work", role, len(own), ready.Time, last.Time, last.Type, busPath)
	}
	notes, next, err := readyFields(ready)
	if err != nil {
		return ReadyBrief{}, fmt.Errorf("the newest READY-RESTART line of %s (%s %s in %s) %w", role, ready.Date, ready.Time, busPath, err)
	}

	tmpl, err := d.Read(place.Host, path)
	if errors.Is(err, fs.ErrNotExist) {
		return ReadyBrief{}, fmt.Errorf("no brief template at %s on %s", path, place.Host)
	}
	if err != nil {
		return ReadyBrief{}, fmt.Errorf("could not read the brief template %s on %s: %w", path, place.Host, err)
	}
	lines := strings.Split(string(tmpl), "\n")
	var at []int
	for n, l := range lines {
		if strings.HasPrefix(l, step6) {
			at = append(at, n)
		}
	}
	if len(at) != 1 {
		return ReadyBrief{}, fmt.Errorf("the template %s on %s has %d lines starting %q, want exactly 1", path, place.Host, len(at), step6)
	}
	start := at[0]
	if strings.Contains(lines[start], "your "+ready.Time+" READY-RESTART line") {
		return ReadyBrief{}, fmt.Errorf("the template %s on %s already continues from the %s READY-RESTART line of %s: no READY-RESTART "+
			"line newer than the last restart", path, place.Host, ready.Time, role)
	}
	end := start + 1 // step 6 runs on over its indented lines
	for end < len(lines) && strings.TrimSpace(lines[end]) != "" && (lines[end][0] == ' ' || lines[end][0] == '\t') {
		end++
	}
	since := "nothing"
	if len(later) > 0 {
		var parts []string
		for _, l := range later {
			parts = append(parts, l.Time+" "+l.Type+" "+l.Text)
		}
		since = strings.Join(parts, "; ")
	}
	repl := []string{
		fmt.Sprintf("%s your %s READY-RESTART line (notes=%s, next=%s)", step6, ready.Time, notes, next),
		"   Crius since then: " + since,
	}
	out := slices.Concat(lines[:start], repl, lines[end:])
	return ReadyBrief{Host: place.Host, Path: path, Prev: path + ".prev-" + d.Now.Format("1504"),
		Text: strings.Join(out, "\n")}, nil
}

// readyLines finds the role's newest READY-RESTART line on a project bus, the crius:lead DECISION and INFO lines to the
// role after it, and the role's own lines after it, each in bus order.
func readyLines(busPath, project, name string) (ready *store.Line, later, own []*store.Line, err error) {
	f, err := os.Open(busPath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("could not read the bus %s: %w", busPath, err)
	}
	defer f.Close()
	role := project + ":" + name
	var st store.State
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		l := store.ParseLine(sc.Text(), &st, false)
		if l == nil {
			continue
		}
		if strings.EqualFold(l.Project, project) && strings.EqualFold(l.Role, name) &&
			store.RestartWord(l.Type+" "+l.Text) == "READY-RESTART" {
			ready, later, own = l, nil, nil
			continue
		}
		if ready != nil && strings.EqualFold(l.Project, project) && strings.EqualFold(l.Role, name) {
			own = append(own, l)
			continue
		}
		if ready != nil && l.Project == "crius" && l.Role == "lead" && (l.Type == "DECISION" || l.Type == "INFO") &&
			slices.ContainsFunc(l.Recipients(), func(r string) bool { return strings.EqualFold(r, role) }) {
			later = append(later, l)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, nil, nil, fmt.Errorf("could not read the bus %s: %w", busPath, err)
	}
	if ready == nil {
		return nil, nil, nil, fmt.Errorf("no READY-RESTART line from %s in %s", role, busPath)
	}
	return ready, later, own, nil
}

// readyFields gives a READY-RESTART line's notes= and next= values: notes runs up to " next=", next to the line's end.
func readyFields(l *store.Line) (notes, next string, err error) {
	text := l.Text
	if i := strings.Index(text, "READY-RESTART"); i >= 0 {
		text = text[i:]
	}
	ni := strings.Index(text, "notes=")
	xi := strings.Index(text, " next=")
	if ni < 0 || xi < 0 || xi < ni {
		return "", "", errors.New("has no notes=<path> followed by next=<text>")
	}
	notes = strings.TrimSpace(text[ni+len("notes=") : xi])
	next = strings.TrimSpace(text[xi+len(" next="):])
	if notes == "" || next == "" {
		return "", "", errors.New("has an empty notes= or next=")
	}
	return notes, next, nil
}

// RestartFromReady is crius-wakewatch restart <role> --from-ready: it builds the role's brief (BuildFromReady) and, with
// dry, prints it and does nothing else. Otherwise it restarts the role with that brief through Restart and, once Restart
// says SENT (a tmux tab) or READY (a T3 thread, whose send is Crius's), keeps a .prev-HHMM copy of the template and
// saves the new brief over it.
func RestartFromReady(d FromReadyDeps, role string, dry bool) int {
	role = strings.ToLower(strings.TrimSpace(role))
	b, err := BuildFromReady(d, role)
	if err != nil {
		fmt.Fprintf(d.Out, "NO BRIEF %s: %v. Nothing was sent or saved.\n", role, err)
		return RestartNoBrief
	}
	if dry {
		fmt.Fprintf(d.Out, "DRY RUN %s: would save the brief below over %s on %s (old copy %s), then restart the role with it. "+
			"Nothing was sent or saved.\n-----\n%s\n-----\n", role, b.Path, b.Host, filepath.Base(b.Prev), b.Text)
		return RestartReady
	}
	if code := Restart(d.RestartDeps, role, b.Text); code != RestartReady {
		return code
	}
	if err := d.Save(b.Host, b.Path, b.Prev, []byte(b.Text)); err != nil {
		fmt.Fprintf(d.Out, "UNSAVED %s: the restart above went, but the template %s on %s was not saved (%v): it still holds the "+
			"old step 6. Save the brief by hand before the next restart.\n", role, b.Path, b.Host, err)
		return RestartUnsaved
	}
	fmt.Fprintf(d.Out, "SAVED %s: the brief is now %s on %s (old copy %s).\n", role, b.Path, b.Host, filepath.Base(b.Prev))
	return RestartReady
}
