package recall

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/orzazade/crius/internal/redact"
)

// The bounds of the transcript search (design §4 `find --deep`). deepTimeout is a var so a test can hit it.
const (
	retention    = 30 * 24 * time.Hour // Claude Code deletes transcripts after 30 days
	deepCitation = 10
)

var deepTimeout = 2 * time.Minute

// Deep is the bounded transcript search: the transcripts of every session changed in the window (at most the last
// 30 days), for 2 minutes, citing 10 messages (path, message uuid, time, a redacted one-line snippet). The asked
// project only ranks first: an asker does not know where a talk was filed, and a filing error must not hide an
// answer (Crius, 10-16). The asking session ranks last: it holds the question itself. Files are read in that order,
// each part newest first; within a rank, the tightest match (the shortest stretch holding every word) comes first,
// then the newest. It runs in Go, not rg, so the CLI needs nothing on PATH. A
// window that starts before the 30 days is expired: it shows the durable rows left, plus what the retained part of
// the window still has.
func Deep(db *sql.DB, q Query, busTypes map[string]bool) (*Result, error) {
	terms := deepTerms(q.Text)
	switch {
	case len(terms) == 0:
		return nil, fmt.Errorf("%w: --deep needs words to search for", ErrQuery)
	case q.Waiting || q.Active || len(q.Kinds) > 0:
		return nil, fmt.Errorf("%w: --deep takes only a query, --project (ranks first) and --since", ErrQuery)
	}
	if q.Project != "" {
		if known, err := knownProject(db, q.Project); err != nil {
			return nil, err
		} else if !known {
			return nil, fmt.Errorf("%w: no project %q in the store", ErrQuery, q.Project)
		}
	}
	res := &Result{Query: q.Text, Hits: []Hit{}}
	var err error
	if res.Status, res.Reasons, err = Freshness(db, q.Now); err != nil {
		return nil, err
	}
	from, expired := q.Now.Add(-retention), false
	if !q.Since.IsZero() {
		if q.Since.Before(from) {
			expired = true
		} else {
			from = q.Since
		}
	}
	files, missing, unreadable, err := deepFilesOf(db, q.Project, q.Self, from)
	if err != nil {
		return nil, err
	}
	if len(unreadable) > 0 {
		res.Status = "partial"
		res.Reasons = append(res.Reasons, fmt.Sprintf("cannot read %d folders, e.g. %s", len(unreadable), unreadable[0]))
	}
	switch {
	case len(missing) == 1:
		res.Status = "partial"
		res.Reasons = append(res.Reasons, "1 transcript of a session active in the window is missing: "+missing[0])
	case len(missing) > 1:
		res.Status = "partial"
		res.Reasons = append(res.Reasons, fmt.Sprintf("%d transcripts of sessions active in the window are missing, e.g. %s",
			len(missing), missing[0]))
	}
	first := ""
	if q.Project != "" {
		first = ", " + q.Project + " first"
	}
	res.Scope = fmt.Sprintf("transcripts of all projects%s (%d files, since %s UTC)", first, len(files),
		from.UTC().Format("2006-01-02 15:04"))
	deadline := time.Now().Add(deepTimeout)
	var best []cand
	total, gone := 0, 0
	for i, f := range files {
		stopped, g, err := deepFile(f, terms, from, deadline, &best, &total)
		if err != nil {
			return nil, err
		}
		if g {
			gone++
		}
		if stopped {
			res.Status = "partial"
			res.Reasons = append(res.Reasons, fmt.Sprintf("stopped at the %s time limit, %d of %d files not read",
				deepTimeout, len(files)-i, len(files)))
			break
		}
	}
	if gone > 0 {
		res.Status = "partial"
		res.Reasons = append(res.Reasons, fmt.Sprintf("%d listed files were gone when read", gone))
	}
	for _, c := range best {
		res.Hits = append(res.Hits, c.cite(terms))
	}
	res.More = total > len(best)
	// Expired wins over partial: the asker learns the evidence is past retention; a cut stays among the reasons.
	if expired {
		res.Status = "expired"
		res.Reasons = append(res.Reasons, fmt.Sprintf("transcripts older than 30 days are gone (asked since %s); "+
			"durable rows below", q.Since.UTC().Format("2006-01-02")))
		durable, err := Find(db, Query{Text: q.Text, Since: q.Since, Now: q.Now}, busTypes)
		if err != nil {
			return nil, err
		}
		for _, h := range durable.Hits {
			h.Match = "durable"
			res.Hits = append(res.Hits, h)
		}
		res.More = res.More || durable.More
		res.Scope += "; durable rows from " + durable.Scope
	}
	return res, nil
}

// deepTerms are the distinct lower-cased words of the query; a message must hold all of them.
func deepTerms(text string) [][]byte {
	var terms [][]byte
	seen := map[string]bool{}
	for _, w := range wordRe.FindAllString(strings.ToLower(text), -1) {
		if !seen[w] {
			seen[w] = true
			terms = append(terms, []byte(w))
		}
	}
	return terms
}

// The ranks of a transcript: the asked project's, the others, the asking session's.
const (
	rankAsked = iota
	rankOther
	rankSelf
)

// deepSrc is one transcript to read.
type deepSrc struct {
	path string
	rank int
	mt   time.Time
}

// cand is a matching message: its hit without the snippet, its text (not yet redacted), and its ranking: by rank,
// then the shortest span holding every word, then the newest.
type cand struct {
	hit  Hit
	text string
	rank int
	span int
}

func (a cand) less(b cand) bool {
	if a.rank != b.rank {
		return a.rank < b.rank
	}
	if a.span != b.span {
		return a.span < b.span
	}
	return a.hit.TS > b.hit.TS
}

// keep adds c to the n best, in order: a search for a common word holds 10 messages, not every match.
func keep(best *[]cand, c cand, n int) {
	i := sort.Search(len(*best), func(i int) bool { return c.less((*best)[i]) })
	if i == n {
		return
	}
	*best = slices.Insert(*best, i, c)
	if len(*best) > n {
		*best = (*best)[:n]
	}
}

// cite redacts the whole message, then cuts the snippet around the tightest stretch: a secret cut in half by the
// window would lose the prefix the patterns look for. If redaction took a word away, the snippet starts the text.
func (c cand) cite(terms [][]byte) Hit {
	text, _ := redact.Redact(c.text)
	// strings.ToLower maps rune to rune, so a rune index in lower is the same in text (byte offsets may differ).
	at, _ := tightest([]rune(strings.ToLower(text)), terms)
	runes := []rune(text)
	lo := max(0, at-80)
	c.hit.Text = string(runes[lo:min(len(runes), lo+200)])
	return c.hit
}

// deepFilesOf lists the session transcripts and their subagent files changed since from, of every project (also
// sessions filed under none), by rank, each rank newest first; subagent files at any depth under the session's
// folder. A transcript gone from disk is skipped when its session ended before the window (expired), and listed in
// missing when the session was active in it: failing to look is not "no".
func deepFilesOf(db *sql.DB, project, self string, from time.Time) (all []deepSrc, missing, unreadable []string, err error) {
	rows, err := db.Query("select transcript_path, case when max(id = ?2) then ?3 when max(coalesce(project,'') = ?1"+
		" and ?1 <> '') then ?4 else ?5 end, max(coalesce(ended, started, '')) >= ?6 from session where transcript_path"+
		" is not null group by transcript_path", project, self, rankSelf, rankAsked, rankOther, from.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, nil, nil, err
	}
	type main struct {
		path   string
		rank   int
		active bool
	}
	var mains []main
	for rows.Next() {
		var m main
		if err := rows.Scan(&m.path, &m.rank, &m.active); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		mains = append(mains, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, nil, err
	}
	for _, m := range mains {
		fi, err := os.Stat(m.path)
		if errors.Is(err, os.ErrNotExist) {
			if m.active {
				missing = append(missing, m.path)
			}
			continue
		}
		if err != nil {
			return nil, nil, nil, err
		}
		if !fi.ModTime().Before(from) {
			all = append(all, deepSrc{m.path, m.rank, fi.ModTime()})
		}
		err = filepath.WalkDir(strings.TrimSuffix(m.path, ".jsonl"), func(p string, d fs.DirEntry, err error) error {
			switch {
			case errors.Is(err, fs.ErrNotExist) && p == strings.TrimSuffix(m.path, ".jsonl"):
				return fs.SkipAll
			case err != nil:
				unreadable = append(unreadable, fmt.Sprintf("%s: %v", p, err))
				if d != nil && d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			case d.IsDir() || !strings.HasSuffix(p, ".jsonl"):
				return nil
			}
			fi, err := d.Info()
			if err != nil {
				unreadable = append(unreadable, fmt.Sprintf("%s: %v", p, err))
				return nil
			}
			if !fi.ModTime().Before(from) {
				all = append(all, deepSrc{p, m.rank, fi.ModTime()})
			}
			return nil
		})
		if err != nil {
			return nil, nil, nil, err
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].rank != all[j].rank {
			return all[i].rank < all[j].rank
		}
		return all[i].mt.After(all[j].mt)
	})
	return all, missing, unreadable, nil
}

type deepLine struct {
	Type, UUID, Timestamp string
	Message               struct {
		Content json.RawMessage
	}
}

// deepFile counts the matching messages of one file and keeps the best; stopped is true when the deadline passed,
// gone when the file went away after it was listed.
func deepFile(src deepSrc, terms [][]byte, from, deadline time.Time, best *[]cand, total *int) (stopped, gone bool, err error) {
	path := src.path
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, true, nil
		}
		return false, false, err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	for {
		if !time.Now().Before(deadline) {
			return true, false, nil
		}
		line, err := r.ReadBytes('\n')
		if len(line) > 0 && containsAll(bytes.ToLower(line), terms) {
			if c, ok := deepCite(src, line, terms, from); ok {
				*total++
				keep(best, c, deepCitation)
			}
		}
		if errors.Is(err, io.EOF) {
			return false, false, nil
		}
		if err != nil {
			return false, false, fmt.Errorf("%s: %w", path, err)
		}
	}
}

func containsAll(s []byte, terms [][]byte) bool {
	for _, t := range terms {
		if !bytes.Contains(s, t) {
			return false
		}
	}
	return true
}

// deepCite makes a candidate of a user or assistant message whose own text (not its metadata) holds every term.
func deepCite(src deepSrc, line []byte, terms [][]byte, from time.Time) (cand, bool) {
	var l deepLine
	if json.Unmarshal(line, &l) != nil || (l.Type != "user" && l.Type != "assistant") {
		return cand{}, false
	}
	ts, err := time.Parse(time.RFC3339Nano, l.Timestamp)
	if err != nil || ts.Before(from) {
		return cand{}, false
	}
	text := strings.Join(strings.Fields(messageText(l.Message.Content)), " ")
	at, span := tightest([]rune(strings.ToLower(text)), terms)
	if at < 0 {
		return cand{}, false
	}
	return cand{Hit{Kind: "transcript", ID: l.UUID, TS: l.Timestamp, Match: "deep", Path: src.path}, text, src.rank, span}, true
}

// tightest finds the shortest stretch of text that holds every term, in runes (letters): its start and length; at
// is -1 when a term is missing. The stretch ends at the furthest word end in it (one term can contain another).
func tightest(text []rune, terms [][]byte) (at, span int) {
	type occ struct{ pos, term int }
	var occs []occ
	s := string(text)
	length, longest := make([]int, len(terms)), 0
	for i, t := range terms {
		length[i] = utf8.RuneCount(t)
		longest = max(longest, length[i])
		for off := 0; ; {
			j := strings.Index(s[off:], string(t))
			if j < 0 {
				break
			}
			occs = append(occs, occ{off + j, i})
			off += j + len(t)
		}
	}
	sort.Slice(occs, func(i, j int) bool { return occs[i].pos < occs[j].pos })
	// Byte offsets to rune offsets, in one pass over the sorted offsets.
	for i, b, r := 0, 0, 0; i < len(occs); i++ {
		r += utf8.RuneCountInString(s[b:occs[i].pos])
		b, occs[i].pos = occs[i].pos, r
	}
	at, span = -1, 0
	count, have := make([]int, len(terms)), 0
	for lo, hi := 0, 0; hi < len(occs); hi++ {
		if count[occs[hi].term]++; count[occs[hi].term] == 1 {
			have++
		}
		for ; have == len(terms); lo++ {
			// Only an occurrence starting less than the longest term's length before hi can end after hi does.
			end := occs[hi].pos + length[occs[hi].term]
			for k := hi - 1; k >= lo && occs[k].pos > occs[hi].pos-longest; k-- {
				end = max(end, occs[k].pos+length[occs[k].term])
			}
			if at < 0 || end-occs[lo].pos < span {
				at, span = occs[lo].pos, end-occs[lo].pos
			}
			if count[occs[lo].term]--; count[occs[lo].term] == 0 {
				have--
			}
		}
	}
	return at, span
}

// messageText is the readable text of a message: a string, or the text, tool input and tool result of its blocks.
func messageText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type, Text string
		Input      json.RawMessage
		Content    json.RawMessage
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		switch {
		case b.Text != "":
			parts = append(parts, b.Text)
		case len(b.Input) > 0:
			// Decoded, not raw JSON: an escaped quote (\") would hide a password from the redaction patterns.
			parts = append(parts, jsonStrings(b.Input)...)
		case len(b.Content) > 0:
			parts = append(parts, messageText(b.Content))
		}
	}
	return strings.Join(parts, " ")
}

// jsonStrings lists the string values of a JSON document, keys in order.
func jsonStrings(raw json.RawMessage) []string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case string:
			out = append(out, v)
		case []any:
			for _, e := range v {
				walk(e)
			}
		case map[string]any:
			for _, k := range slices.Sorted(maps.Keys(v)) {
				walk(v[k])
			}
		}
	}
	walk(v)
	return out
}
