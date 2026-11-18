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

	"github.com/orzazade/crius/internal/sessionstate"
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
	s, err := readyLines(busPath, project, name)
	if err != nil {
		return ReadyBrief{}, err
	}
	ready, later := s.ready, s.later
	if s.block != nil { // the role went on working (or a fresh session of it started): its line no longer says where it is
		return ReadyBrief{}, fmt.Errorf("%s posted %d line(s) after its %s READY-RESTART line in %s, and its %s %s line is no "+
			"answer to Crius (%s): no READY-RESTART line newer than the last restart or work",
			role, s.own, ready.Time, busPath, s.block.Time, s.block.Type, s.why)
	}
	if s.own > 0 {
		// Only answers came after the line, but a session that started since may have posted them: a restart typed by
		// hand changes neither the template nor the bus. crius-hook's record of the role's current session is rewritten
		// when a session takes the role, so one written at or after the line's minute refuses (a /clear, resume or
		// compact of the same session too: the safe side). A role with no record here (a box's hook files its own) has
		// only the template check below and the START line a fresh session posts, which is no answer.
		at := ready.At // a Z stamp's instant, else its wall time
		if at.IsZero() {
			if at, err = lineTime(ready.Date+"T"+ready.Time, d.Now); err != nil {
				return ReadyBrief{}, fmt.Errorf("the newest READY-RESTART line of %s in %s has no readable time: %w", role, busPath, err)
			}
		}
		dir := filepath.Join(d.Home, "roles")
		cur, err := sessionstate.ReadRole(dir, project, name)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return ReadyBrief{}, fmt.Errorf("could not read crius-hook's record of the session of %s, to check that no session "+
				"started after its %s READY-RESTART line: %w", role, ready.Time, err)
		case !cur.Time.Before(at):
			return ReadyBrief{}, fmt.Errorf("a session of %s started at %s (crius-hook's record in %s), after its %s READY-RESTART "+
				"line in %s, so the answers since may be the new session's: no READY-RESTART line newer than the last restart",
				role, cur.Time.In(time.Local).Format("2006-01-02 15:04"), dir, ready.Time, busPath)
		}
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
	prev := path + ".prev-" + d.Now.Format("1504")
	if _, err := d.Read(place.Host, prev); err == nil {
		return ReadyBrief{}, fmt.Errorf("the old-copy name %s on %s is taken: run again in a minute", prev, place.Host)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return ReadyBrief{}, fmt.Errorf("could not check the old-copy name %s on %s: %w", prev, place.Host, err)
	}
	end := start + 1 // step 6 runs on over its indented lines
	for end < len(lines) && strings.TrimSpace(lines[end]) != "" && (lines[end][0] == ' ' || lines[end][0] == '\t') {
		end++
	}
	since := "nothing"
	if len(later) > 0 {
		var parts []string
		for _, l := range later {
			who := ""
			if l.Project != "crius" { // the role's own answer
				who = "your "
			}
			parts = append(parts, l.Time+" "+who+l.Type+" "+l.Text)
		}
		since = strings.Join(parts, "; ")
	}
	repl := []string{
		fmt.Sprintf("%s your %s READY-RESTART line (notes=%s, next=%s)", step6, ready.Time, notes, next),
		"   Crius since then: " + since,
	}
	out := slices.Concat(lines[:start], repl, lines[end:])
	return ReadyBrief{Host: place.Host, Path: path, Prev: prev,
		Text: strings.Join(out, "\n")}, nil
}

// sinceReady is a role's newest READY-RESTART line on a project bus and what came after it.
type sinceReady struct {
	ready *store.Line
	later []*store.Line // the crius:lead DECISION, INFO and ASK lines to the role and the role's answers to them, in bus order
	own   int           // the role's own lines after ready
	block *store.Line   // the role's first line after ready that is no answer to Crius, nil when every one is
	why   string        // why block is no answer
}

// readyLines reads the role's newest READY-RESTART line on a project bus and the lines after it (sinceReady).
func readyLines(busPath, project, name string) (sinceReady, error) {
	f, err := os.Open(busPath)
	if err != nil {
		return sinceReady{}, fmt.Errorf("could not read the bus %s: %w", busPath, err)
	}
	defer f.Close()
	role := project + ":" + name
	var s sinceReady
	var st store.State
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		l := store.ParseLine(sc.Text(), &st, false)
		if l == nil {
			continue
		}
		// a bare sender on a project's own bus is that project's role, as the ingest reads it
		mine := strings.EqualFold(l.Role, name) && (l.Project == "" || strings.EqualFold(l.Project, project))
		// the request is the line's type or opens its text: a line that only mentions the word asks nothing
		if mine && (store.RestartWord(l.Type) == "READY-RESTART" ||
			strings.HasPrefix(l.Text, "READY-RESTART") && store.RestartWord(l.Text) == "READY-RESTART") {
			s = sinceReady{ready: l}
			continue
		}
		if s.ready == nil {
			continue
		}
		if mine {
			s.own++
			if s.block != nil {
				continue
			}
			if why := answerWhy(l, project, s.later); why != "" {
				s.block, s.why = l, why
			} else {
				s.later = append(s.later, l)
			}
			continue
		}
		if l.Project == "crius" && l.Role == "lead" && (l.Type == "DECISION" || l.Type == "INFO" || l.Type == "ASK") &&
			slices.ContainsFunc(l.Recipients(), func(r string) bool { return toName(r, project) == role }) {
			s.later = append(s.later, l)
		}
	}
	if err := sc.Err(); err != nil {
		return sinceReady{}, fmt.Errorf("could not read the bus %s: %w", busPath, err)
	}
	if s.ready == nil {
		return sinceReady{}, fmt.Errorf("no READY-RESTART line from %s in %s", role, busPath)
	}
	return s, nil
}

// talkTypes are the bus types a role answers Crius in. By its type alone none reports work or a change of state, which
// every other type does (DONE, READY, PASS, FAIL, NEED, BLOCKED, LANDED, VERIFIED, START, RESUMED, ...): a role that
// posts one of those after its READY-RESTART line went on working, or is a fresh session (START), and the line no
// longer says where it is. INFO also carries work ("CLAIM item 17" after a crius pick), so the type is not enough.
var talkTypes = map[string]bool{"ANSWER": true, "ACK": true, "ASK": true, "INFO": true}

// answerRe is the mark the teams open an answer with ("Re 20:38: both done already"): the time of the line it answers.
var answerRe = regexp.MustCompile(`(?i)^re (\d\d:\d\d)\b`)

// answerWhy says why the role's line l after its READY-RESTART line is no answer to Crius, "" when it is one. An answer
// is one exchange with Crius that leaves the role where its READY-RESTART line says: a talk type (talkTypes), opening
// with "Re HH:MM" that names a line in crius (the crius:lead lines to the role since the READY-RESTART line, which step
// 6 shows, so the fresh session sees the question too), and sent to crius:lead and at most that line's own recipients
// (a reply-all). A line to anyone else is work with the team.
func answerWhy(l *store.Line, project string, crius []*store.Line) string {
	if !talkTypes[l.Type] {
		return fmt.Sprintf("a %s line reports work or a state; an answer is an ANSWER, ACK, ASK or INFO line", l.Type)
	}
	var asked *store.Line
	if m := answerRe.FindStringSubmatch(l.Text); m != nil {
		for _, c := range slices.Backward(crius) {
			if c.Project == "crius" && c.Time == m[1] {
				asked = c
				break
			}
		}
	}
	to := l.Recipients()
	if len(to) == 0 {
		return "it goes to the whole bus"
	}
	may, also := map[string]bool{"crius:lead": true}, ""
	if asked != nil {
		for _, r := range asked.Recipients() {
			may[toName(r, project)] = true
		}
		also = " and the " + asked.Time + " crius line it answers"
	}
	var others []string
	for _, r := range to {
		if !may[toName(r, project)] {
			others = append(others, r)
		}
	}
	if len(others) > 0 {
		return fmt.Sprintf("it goes to %s, not only to crius:lead%s", strings.Join(others, ","), also)
	}
	if asked == nil {
		return `it does not open with "Re HH:MM" naming a crius:lead DECISION, INFO or ASK line to it after the READY-RESTART line`
	}
	if !slices.ContainsFunc(to, func(r string) bool { return toName(r, project) == "crius:lead" }) {
		return "it does not go to crius:lead"
	}
	return ""
}

// toName is a recipient as the ingest reads it on a project's bus (busName), in lower case.
func toName(r, project string) string { return strings.ToLower(busName(project, r)) }

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
	tabs, err := d.Tabs()
	if err != nil {
		fmt.Fprintf(d.Out, "FAILED %s: could not read the remote tab list: %v. Nothing was sent or saved.\n", role, err)
		return RestartFailed
	}
	tab := slices.ContainsFunc(tabs, func(t RemoteTab) bool { return strings.EqualFold(t.Target, role) })
	code := Restart(d.RestartDeps, role, b.Text)
	// saved once the brief went or may have gone (tmux SENT, MAYBE SENT) or is Crius's to send (T3 READY), so a
	// run after it refuses the same line rather than send it twice; a busy or failed run leaves the line for the next
	if code != RestartReady && code != RestartMaybe {
		return code
	}
	if err := d.Save(b.Host, b.Path, b.Prev, []byte(b.Text)); err != nil {
		fmt.Fprintf(d.Out, "UNSAVED %s: after the restart step above, the template %s on %s was not saved (%v): it still holds "+
			"the old step 6, so a run of --from-ready now would build the same brief again. Save the brief by hand first.\n",
			role, b.Path, b.Host, err)
		return RestartUnsaved
	}
	fmt.Fprintf(d.Out, "SAVED %s: the brief is now %s on %s (old copy %s).", role, b.Path, b.Host, filepath.Base(b.Prev))
	switch {
	case code == RestartMaybe:
		fmt.Fprintf(d.Out, " The brief may not have gone: if the tab shows it did not, put the old copy back over the "+
			"template before the next --from-ready, which refuses this line now.")
	case !tab:
		fmt.Fprintf(d.Out, " It is not sent yet: send it with the t3_thread_send call above. If you do not send it, put "+
			"the old copy back over the template before the next --from-ready, which refuses this line now.")
	}
	fmt.Fprintln(d.Out)
	return code
}
