// Command bus-post appends one line to a bus log:
//
//	bus-post [--at <YYYY-MM-DDTHH:MM>] <log> <TYPE> <project:role> <to> <text>
//
// It stamps the local clock itself. A time given with --at that is more than 2 min off the clock is refused: twice a
// line went out stamped in UTC or with a hand-typed time (10-20 23:45, 10-21 12:48). A time typed at the start of the
// text ("Crius 17:50", "Lead to P5 13:28") is held to the same 2 min (store.FormatLine; 31 such lines 10-23 to 10-25).
//
// A VERIFIED line must name a run log of Crius's own: a file under ~/.crius/runs whose header (first 20 lines) holds
// `sha: <sha>` for a sha the line names and `pass: <N>` with N >= 1. Twice a VERIFIED went out before its run
// (10-21 12:08 and 14:54).
//
// A recipient name with a project part is lower case: a line to quotes:P5 went unread for 2 h (quotes:lead 04:58).
//
// A project's table in ~/.crius/bus.toml adds the guards in guards.go: hex words must be commits, and one live run at a
// time (the check and the write hold a lock on the bus log).
//
// An ORDER or ASK to someone with no live Claude Code session is posted and said, exit 5: crius:lead's ORDERs of 10-25
// 13:18 to crius:build went nowhere for hours. Posted, so a caller must not send it again.
//
// Exit codes: 0 posted, 2 refused (nothing written), 3 refused: a hex word is no commit, 4 refused: a START while a live
// run is open, 5 posted, but a recipient has no live session (or the check failed), 6 refused: a time typed at the start
// of the text is off the clock, 7 refused: a READY with no pre-ready PASS record, 1 the write failed. 3, 4 and 6 are the
// codes the Quotes say.sh wrappers gave; 7 was agreed with quotes:lead.
package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/orzazade/crius/internal/store"
	"github.com/orzazade/crius/internal/wakewatch"
)

const (
	maxSkew     = 2 * time.Minute
	headerLines = 20
)

var (
	shaRe   = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	pathRe  = regexp.MustCompile(`(?:^|\s)((?:~|/)[^\s,;·]*)`)
	headSha = regexp.MustCompile(`^sha:\s*([0-9a-f]{7,40})\s*$`)
	headRun = regexp.MustCompile(`^pass:\s*(\d+)\s*$`)
)

func main() { os.Exit(run(os.Args[1:], time.Now(), os.Stderr)) }

func run(args []string, now time.Time, stderr io.Writer) int {
	stamp := now
	if len(args) > 0 && args[0] == "--at" {
		if len(args) < 2 {
			return refuse(stderr, "--at needs a time")
		}
		given, err := time.ParseInLocation("2006-01-02T15:04", args[1], now.Location())
		if err != nil {
			return refuse(stderr, "--at %q is not YYYY-MM-DDTHH:MM local time", args[1])
		}
		if d := given.Sub(now); d > maxSkew || d < -maxSkew {
			return refuse(stderr, "--at %s is %s off the clock (%s local); leave --at out and bus-post stamps it",
				args[1], d.Round(time.Minute), now.Format("2006-01-02T15:04"))
		}
		stamp, args = given, args[2:]
	}
	if len(args) != 5 {
		return refuse(stderr, "usage: bus-post [--at YYYY-MM-DDTHH:MM] <log> <TYPE> <project:role> <to> <text>")
	}
	log, typ, from, to, text := args[0], args[1], args[2], args[3], args[4]
	project, role, ok := strings.Cut(from, ":")
	if !ok {
		return refuse(stderr, "from %q is not project:role", from)
	}
	if n := upperName(to); n != "" {
		return refuse(stderr, "names after -> are lower case (quotes:p5), not %q", n)
	}
	if err := store.CheckTypedTime(project, role, text, stamp); err != nil {
		fmt.Fprintf(stderr, "bus-post: refused: %v\n", err)
		return rcOffClock
	}
	line, err := store.FormatLine(typ, project, role, text, to, stamp)
	if err != nil {
		return refuse(stderr, "%v", err)
	}
	if typ == "VERIFIED" {
		if err := checkRunLog(text); err != nil {
			return refuse(stderr, "%v", err)
		}
	}
	all, err := loadGuards(store.Home())
	if err != nil {
		return refuse(stderr, "%v", err)
	}
	// The bus's own guards, and for the sha check also the sender's: a sha may be in the repos of either.
	bus := all[logProject(log)]
	repos, idDirs := bus.ShaRepos, bus.IDDirs
	if p := project; p != logProject(log) {
		repos, idDirs = append(slices.Clip(repos), all[p].ShaRepos...), append(slices.Clip(idDirs), all[p].IDDirs...)
	}
	if len(repos) > 0 {
		bad, err := checkShas(text, repos, idDirs)
		if err != nil {
			return refuse(stderr, "%v", err)
		}
		if len(bad) > 0 {
			fmt.Fprintf(stderr, "bus-post: refused: %s is not a commit in %s (an id that is no commit: SAY_NOT_SHA=%s)\n",
				strings.Join(bad, ", "), strings.Join(repos, ", "), strings.Join(bad, ","))
			return rcNotCommit
		}
	}
	if g := bus; typ == "READY" {
		if g.PrereadyDir == "" {
			g = all[project]
		}
		if g.PrereadyDir != "" {
			ok, err := readyProven(text, g)
			if err != nil {
				return refuse(stderr, "%v", err)
			}
			if !ok {
				fmt.Fprintf(stderr, "bus-post: refused: a READY names the sha pre-ready passed: no pre-ready PASS record "+
					"in %s for a sha it names (run pre-ready on it first)\n", g.PrereadyDir)
				return rcNoRecord
			}
		}
	}
	// No O_CREATE: a typo in the path would start a bus nobody reads.
	f, err := os.OpenFile(log, os.O_RDWR|os.O_APPEND, 0)
	if errors.Is(err, os.ErrNotExist) {
		return refuse(stderr, "no bus log at %s", log)
	}
	if err == nil {
		// One writer at a time: the open-run check and the write hold this lock, so two STARTs at once cannot both
		// pass (Quotes 10-25 01:44, 02:20). It is released when f is closed.
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
	}
	if err == nil {
		// A last line without its newline would swallow this one.
		if last, ok := lastByte(f); ok && last != '\n' {
			f.Close()
			return refuse(stderr, "%s ends in the middle of a line", log)
		}
		if typ == "START" && bus.LiveWindowMin > 0 {
			st, serr := f.Stat()
			if serr != nil {
				f.Close()
				return refuse(stderr, "could not read %s for open runs: %v", log, serr)
			}
			open, rerr := openRuns(f, st.Size(), stamp, time.Duration(bus.LiveWindowMin)*time.Minute)
			if rerr != nil {
				f.Close()
				return refuse(stderr, "could not read %s for open runs: %v", log, rerr)
			}
			if len(open) > 0 {
				f.Close()
				fmt.Fprintf(stderr, "bus-post: refused: a live run is open (a START of the last %d min with no later DONE "+
					"or CORRECTION from its sender); wait for its DONE, or close your own with DONE or CORRECTION:\n%s\n",
					bus.LiveWindowMin, strings.Join(open, "\n"))
				return rcLiveOpen
			}
		}
		_, err = f.WriteString(line + "\n")
		if cerr := f.Close(); err == nil {
			err = cerr
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, "bus-post: write failed:", err)
		return 1
	}
	if typ == "ORDER" || typ == "ASK" {
		if dead, err := deadRecipients(to, logProject(log)); err != nil {
			fmt.Fprintf(stderr, "bus-post: posted, but could not check who has a live session: %v\n", err)
			return rcNoSession
		} else if len(dead) > 0 {
			fmt.Fprintf(stderr, "bus-post: posted, but %s has no live session: nobody reads it until one starts "+
				"(start it, or send it to someone running)\n", strings.Join(dead, ", "))
			return rcNoSession
		}
	}
	return 0
}

// deadRecipients gives each recipient of to with no live Claude Code session. A bare name is a role on the bus's own
// project ("lead"), "crius" is crius:lead, and a whole team ("quotes:all") or "cc" is no one.
func deadRecipients(to, project string) ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	ss, err := wakewatch.LiveSessions(home)
	if err != nil {
		return nil, err
	}
	live := map[string]bool{}
	for _, s := range ss {
		live[wakewatch.BusName(s.Name)] = true
	}
	var dead []string
	for _, n := range strings.FieldsFunc(to, func(r rune) bool { return r == ',' || r == ' ' }) {
		n = strings.ToLower(n)
		switch {
		case n == "cc" || n == "all" || strings.HasSuffix(n, ":all"):
			continue
		case n == "crius":
			n = "crius:lead"
		case !strings.Contains(n, ":"):
			n = project + ":" + n
		}
		if !live[n] && !slices.Contains(dead, n) {
			dead = append(dead, n)
		}
	}
	return dead, nil
}

func refuse(stderr io.Writer, format string, args ...any) int {
	fmt.Fprintf(stderr, "bus-post: refused: "+format+"\n", args...)
	return 2
}

func lastByte(f *os.File) (byte, bool) {
	st, err := f.Stat()
	if err != nil || st.Size() == 0 {
		return 0, false
	}
	b := make([]byte, 1)
	if _, err := f.ReadAt(b, st.Size()-1); err != nil {
		return 0, false
	}
	return b[0], true
}

// shasOf gives the words of text that are a sha. A word runs between spaces, less the punctuation at its ends, so a
// sha inside a path, a file name or another word is never one.
func shasOf(text string) []string {
	var shas []string
	for _, w := range strings.Fields(text) {
		if w = strings.TrimFunc(w, unicode.IsPunct); shaRe.MatchString(w) {
			shas = append(shas, w)
		}
	}
	return shas
}

// checkRunLog passes when every sha the text names is the sha in the header of a run log the text names, under
// ~/.crius/runs, with a pass count of at least 1. Naming a sha no run tested is the mistake it stops.
func checkRunLog(text string) error {
	shas := shasOf(text)
	if len(shas) == 0 {
		return errors.New("a VERIFIED line names the sha it checked")
	}
	runs, err := filepath.Abs(filepath.Join(store.Home(), "runs"))
	if err == nil {
		runs, err = filepath.EvalSymlinks(runs)
	}
	if err != nil {
		return fmt.Errorf("no runs folder for run logs: %v", err)
	}
	var problems, ran []string
	for _, m := range pathRe.FindAllStringSubmatch(text, -1) {
		p := m[1]
		if strings.HasPrefix(p, "~/") {
			home, _ := os.UserHomeDir()
			p = filepath.Join(home, p[2:])
		}
		real, err := filepath.EvalSymlinks(p)
		if t := strings.TrimRightFunc(p, unicode.IsPunct); err != nil && t != p { // "log <path>. Next" (Crius 10-24)
			if t == "" { // "/...": no path
				continue
			}
			p = t
			real, err = filepath.EvalSymlinks(p)
		}
		if errors.Is(err, fs.ErrNotExist) {
			problems = append(problems, p+" does not exist")
			continue
		} else if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		if !strings.HasPrefix(real, runs+string(filepath.Separator)) {
			problems = append(problems, p+" is not under "+runs)
			continue
		}
		sha, pass, err := readHeader(real)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		if pass < 1 {
			problems = append(problems, p+": header has no pass count of 1 or more (`pass: <N>`)")
			continue
		}
		ran = append(ran, sha)
	}
	// Other paths (an evidence folder, say) may stand beside the run log; only an unproven sha refuses the line.
	var unproven []string
	for _, s := range shas {
		// The line may shorten the run's sha, never lengthen it.
		if !slices.ContainsFunc(ran, func(r string) bool { return strings.HasPrefix(r, s) }) {
			unproven = append(unproven, s)
		}
	}
	if len(unproven) == 0 {
		return nil
	}
	if len(ran) == 0 {
		return errors.New(strings.Join(append([]string{"a VERIFIED line names its run log (a file under ~/.crius/runs " +
			"with `sha:` and `pass:` in its header)"}, problems...), "; "))
	}
	problems = append(problems, fmt.Sprintf("no passing run log named here ran %s (they ran: %s)",
		strings.Join(unproven, ", "), strings.Join(ran, ", ")))
	return errors.New(strings.Join(problems, "; "))
}

func readHeader(path string) (sha string, pass int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for i := 0; i < headerLines && sc.Scan(); i++ {
		if m := headSha.FindStringSubmatch(sc.Text()); m != nil {
			sha = m[1]
		}
		if m := headRun.FindStringSubmatch(sc.Text()); m != nil {
			pass, _ = strconv.Atoi(m[1])
		}
	}
	if sha == "" {
		return "", 0, fmt.Errorf("%s: header has no `sha: <sha>` line", path)
	}
	return sha, pass, sc.Err()
}
