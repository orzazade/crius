package main

// A text over the bus limit (store.MaxLen runes) is posted cut, not refused: agents hit the refusal about 150 times in
// 21 sessions from 11-05 to 11-15, each a retry round trip. The limit stays, so bus lines stay short for wakewatch, the
// digest and terminals. The whole text goes to a note next to the bus log, <bus dir>/notes/<bus>-<YYYYMMDD-HHMM>-<6
// letters>.txt, and the line is the head of the text cut before a space, then " … full: <note>". The rules (the tests
// are in overflow_test.go; the first and last rules have none of their own):
//   - Every check of bus-post reads the whole text, before the cut (typed time, owner-queue tag, hex words, READY's sha).
//   - The head is the start of the text, so a "[queue qN]" tag that opens the text still opens the line.
//   - Words that readers pick out of a line and that fall in the cut part are carried after the head, in their order
//     (" … also: sha=… PR 12 · full: …"): the refs of store.RefsOf (ingest links items and events by them), hex words
//     (shas: a READY's first sha stays its first) and dispatch.TextTokens (a DONE's PR number and PARTIAL). When they do
//     not all fit, the line carries those that do and says how many more the note holds.
//   - Strict types are refused over the limit, never cut, because a reader needs their whole text on the bus:
//     VERIFIED (checkRunLog reads its sha from the word after its ref and its run log from any path in the text, so the
//     note pointer would read as a run log path), NEED-OWNER (the owner queue builds the row, its why and its option
//     buttons from the text) and LEARNED (the playbook takes the whole text as the lesson).
//   - The note's 6 letters are base32 (a-z, 2-7): never an 8+ hex word that the sha guard would check as a commit.

import (
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/orzazade/crius/internal/dispatch"
	"github.com/orzazade/crius/internal/store"
)

// strictTypes are refused over the limit (see above).
var strictTypes = map[string]string{
	"VERIFIED":   "its sha and run log are read from the whole text",
	"NEED-OWNER": "the owner queue builds its row and option buttons from the whole text",
	"LEARNED":    "the playbook takes the whole text as the lesson",
}

var noteName = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// minHead is the shortest head a cut line keeps before it gives up carrying every word.
const minHead = 40

// errNoFit is a cut line that cannot fit: the note pointer alone takes the whole limit.
var errNoFit = errors.New("no fit")

// newNote writes text to a new note in dir and gives its path. The name is the bus and the minute plus 6 letters from a
// hash of the text, the clock and a try count; O_EXCL makes two posts in one minute take two names.
func newNote(dir, bus, text string, stamp time.Time) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	for try := range 10 {
		h := sha256.Sum256(fmt.Appendf(nil, "%s\x00%d\x00%d\x00%d", text, time.Now().UnixNano(), os.Getpid(), try))
		p := filepath.Join(dir, fmt.Sprintf("%s-%s-%s.txt", bus, stamp.Format("20060102-1504"), noteName.EncodeToString(h[:])[:6]))
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, err = f.WriteString(text + "\n")
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(p)
			return "", err
		}
		return p, nil
	}
	return "", fmt.Errorf("no free note name in %s after 10 tries", dir)
}

// shown is a path as a line shows it: under the home folder as ~/…
func shown(p string) string {
	if h, err := os.UserHomeDir(); err == nil && h != "" && strings.HasPrefix(p, h+string(filepath.Separator)) {
		return "~" + p[len(h):]
	}
	return p
}

// ending is what follows the head: the carried words, how many more the note holds, and the pointer.
func ending(carried []string, more int, ptr string) string {
	end := " … "
	if len(carried) > 0 || more > 0 {
		also := slices.Clip(carried)
		if more > 0 {
			also = append(also, fmt.Sprintf("+%d more in the note", more))
		}
		end += "also: " + strings.Join(also, " ") + " · "
	}
	return end + "full: " + ptr
}

// cutText gives text cut to fit store.MaxLen with the pointer ptr: the head before a space, the carried words the head
// lacks, then "full: ptr". errNoFit only when the pointer leaves no room for a head of minHead runes.
func cutText(text, ptr string) (string, error) {
	var carried []string
	for range 50 { // carried only grows as the head shrinks, so this settles in a few rounds
		end := ending(carried, 0, ptr)
		budget := store.MaxLen - utf8.RuneCountInString(end)
		if budget < minHead {
			break
		}
		head := headOf(text, budget)
		more := carry(head, text)
		if slices.Equal(more, carried) {
			return head + end, nil
		}
		carried = more
	}
	// Too many words to carry: a short head, then the words that fit and how many more the note holds.
	head := headOf(text, minHead)
	all := carry(head, text)
	for k := len(all); k >= 0; k-- {
		if end := ending(all[:k], len(all)-k, ptr); utf8.RuneCountInString(head+end) <= store.MaxLen {
			return head + end, nil
		}
	}
	return "", errNoFit
}

// headOf is the start of text, at most n runes (an invalid byte counts as one, as in utf8.RuneCountInString), cut
// before the last space that fits (a hard cut only when no space does), with trailing spaces trimmed. It cuts on byte
// offsets, so the head is always a byte prefix of text.
func headOf(text string, n int) string {
	if utf8.RuneCountInString(text) <= n {
		return text
	}
	at, space := 0, -1
	for i := 0; ; i++ {
		r, size := utf8.DecodeRuneInString(text[at:])
		if i > 0 && unicode.IsSpace(r) {
			space = at // a head of i runes ends before this space
		}
		if i == n {
			break
		}
		at += size
	}
	if space > 0 {
		at = space
	}
	return strings.TrimRightFunc(text[:at], unicode.IsSpace)
}

// tokens gives the words readers pick out of text, in their order: refs (as key=value, a decision id bare), hex words
// and dispatch.TextTokens matches.
func tokens(text string) []string {
	type tok struct {
		at int
		s  string
	}
	var toks []tok
	add := func(at int, s string) { toks = append(toks, tok{at, s}) }
	for key, vs := range store.RefsOf(text) {
		from := map[string]int{}
		for _, v := range vs {
			s := key + "=" + v
			if key == "decision" {
				s = v
			}
			i := strings.Index(text[from[s]:], s)
			if i < 0 {
				i = 0
			}
			add(from[s]+i, s)
			from[s] += i + len(s)
		}
	}
	for _, w := range hexWords(text) {
		add(strings.Index(text, w), w)
	}
	for _, re := range dispatch.TextTokens {
		for _, m := range re.FindAllStringIndex(text, -1) {
			add(m[0], text[m[0]:m[1]])
		}
	}
	slices.SortStableFunc(toks, func(a, b tok) int { return a.at - b.at })
	out := make([]string, len(toks))
	for i, t := range toks {
		out[i] = t.s
	}
	return out
}

// carry gives the tokens of text that head does not hold as often, in their order, each once; a hex word that is
// part of a carried ref ("sha=…") is not carried again.
func carry(head, text string) []string {
	inHead := map[string]int{}
	for _, t := range tokens(head) {
		inHead[t]++
	}
	hex := hexWords(text)
	var out []string
	for _, t := range tokens(text) {
		if inHead[t] > 0 {
			inHead[t]--
			continue
		}
		if slices.Contains(out, t) ||
			slices.Contains(hex, t) && slices.ContainsFunc(out, func(o string) bool { return strings.Contains(o, t) }) {
			continue
		}
		out = append(out, t)
	}
	return out
}
