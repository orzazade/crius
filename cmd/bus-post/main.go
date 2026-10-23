// Command bus-post appends one line to a bus log:
//
//	bus-post [--at <YYYY-MM-DDTHH:MM>] <log> <TYPE> <project:role> <to> <text>
//
// It stamps the local clock itself. A time given with --at that is more than 2 min off the clock is refused: twice a
// line went out stamped in UTC or with a hand-typed time (10-20 23:45, 10-21 12:48).
//
// A VERIFIED line must name a run log of Crius's own: a file under ~/.crius/runs whose header (first 20 lines) holds
// `sha: <sha>` for a sha the line names and `pass: <N>` with N >= 1. Twice a VERIFIED went out before its run
// (10-21 12:08 and 14:54).
//
// Exit codes: 0 posted, 2 refused (nothing written), 1 the write failed.
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
	"time"
	"unicode"

	"github.com/orzazade/crius/internal/store"
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
	line, err := store.FormatLine(typ, project, role, text, to, stamp)
	if err != nil {
		return refuse(stderr, "%v", err)
	}
	if typ == "VERIFIED" {
		if err := checkRunLog(text); err != nil {
			return refuse(stderr, "%v", err)
		}
	}
	// No O_CREATE: a typo in the path would start a bus nobody reads.
	f, err := os.OpenFile(log, os.O_RDWR|os.O_APPEND, 0)
	if errors.Is(err, os.ErrNotExist) {
		return refuse(stderr, "no bus log at %s", log)
	}
	if err == nil {
		// A last line without its newline would swallow this one.
		if last, ok := lastByte(f); ok && last != '\n' {
			f.Close()
			return refuse(stderr, "%s ends in the middle of a line", log)
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
	return 0
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
	if len(ran) == 0 && len(problems) == 0 {
		return errors.New("a VERIFIED line names its run log (a file under ~/.crius/runs with `sha:` and `pass:` in its header)")
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
