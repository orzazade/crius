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
	"os"
	"path/filepath"
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
	files, missing, err := deepFilesOf(db, q.Project, q.Self, from)
	if err != nil {
		return nil, err
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
	var cands []cand
	for i, f := range files {
		stopped, err := deepFile(f, terms, from, deadline, &cands)
		if err != nil {
			return nil, err
		}
		if stopped {
			res.Status = "partial"
			res.Reasons = append(res.Reasons, fmt.Sprintf("stopped at the %s time limit, %d of %d files not read",
				deepTimeout, len(files)-i, len(files)))
			break
		}
	}
	sort.SliceStable(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if a.rank != b.rank {
			return a.rank < b.rank
		}
		if a.span != b.span {
			return a.span < b.span
		}
		return a.hit.TS > b.hit.TS
	})
	for i, c := range cands {
		if i == deepCitation {
			res.More = true
			break
		}
		res.Hits = append(res.Hits, c.hit)
	}
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

// cand is a cited message before ranking: by rank, then the shortest span holding every word.
type cand struct {
	hit  Hit
	rank int
	span int
}

// deepFilesOf lists the session transcripts and their subagent files changed since from, of every project (also
// sessions filed under none), by rank, each rank newest first; subagent files at any depth under the session's
// folder. A transcript gone from disk is skipped when its session ended before the window (expired), and listed in
// missing when the session was active in it: failing to look is not "no".
func deepFilesOf(db *sql.DB, project, self string, from time.Time) (all []deepSrc, missing []string, err error) {
	rows, err := db.Query("select transcript_path, case when max(id = ?2) then ?3 when max(coalesce(project,'') = ?1"+
		" and ?1 <> '') then ?4 else ?5 end, max(coalesce(ended, started, '')) >= ?6 from session where transcript_path"+
		" is not null group by transcript_path", project, self, rankSelf, rankAsked, rankOther, from.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, nil, err
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
			return nil, nil, err
		}
		mains = append(mains, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
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
			return nil, nil, err
		}
		if !fi.ModTime().Before(from) {
			all = append(all, deepSrc{m.path, m.rank, fi.ModTime()})
		}
		err = filepath.WalkDir(strings.TrimSuffix(m.path, ".jsonl"), func(p string, d fs.DirEntry, err error) error {
			switch {
			case errors.Is(err, fs.ErrNotExist) && p == strings.TrimSuffix(m.path, ".jsonl"):
				return fs.SkipAll
			case err != nil:
				return err
			case d.IsDir() || !strings.HasSuffix(p, ".jsonl"):
				return nil
			}
			fi, err := d.Info()
			if err != nil {
				return err
			}
			if !fi.ModTime().Before(from) {
				all = append(all, deepSrc{p, m.rank, fi.ModTime()})
			}
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].rank != all[j].rank {
			return all[i].rank < all[j].rank
		}
		return all[i].mt.After(all[j].mt)
	})
	return all, missing, nil
}

type deepLine struct {
	Type, UUID, Timestamp string
	Message               struct {
		Content json.RawMessage
	}
}

// deepFile adds the matching messages of one file; stopped is true when the deadline passed.
func deepFile(src deepSrc, terms [][]byte, from, deadline time.Time, cands *[]cand) (stopped bool, err error) {
	path := src.path
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	for {
		if !time.Now().Before(deadline) {
			return true, nil
		}
		line, err := r.ReadBytes('\n')
		if len(line) > 0 && containsAll(bytes.ToLower(line), terms) {
			if c, ok := deepCite(src, line, terms, from); ok {
				*cands = append(*cands, c)
			}
		}
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("%s: %w", path, err)
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

// deepCite cites a user or assistant message whose own text (not its metadata) holds every term.
func deepCite(src deepSrc, line []byte, terms [][]byte, from time.Time) (cand, bool) {
	var l deepLine
	if json.Unmarshal(line, &l) != nil || (l.Type != "user" && l.Type != "assistant") {
		return cand{}, false
	}
	ts, err := time.Parse(time.RFC3339Nano, l.Timestamp)
	if err != nil || ts.Before(from) {
		return cand{}, false
	}
	// Redacted before the cut: a secret cut in half by the window would lose the prefix the patterns look for.
	text, _ := redact.Redact(strings.Join(strings.Fields(messageText(l.Message.Content)), " "))
	// strings.ToLower maps rune to rune, so a rune index in lower is the same in text (byte offsets may differ).
	lower := []rune(strings.ToLower(text))
	at, span := tightest(lower, terms)
	if at < 0 {
		return cand{}, false
	}
	runes := []rune(text)
	lo, hi := max(0, at-80), min(len(runes), at+120)
	return cand{Hit{Kind: "transcript", ID: l.UUID, TS: l.Timestamp, Text: string(runes[lo:hi]), Match: "deep", Path: src.path},
		src.rank, span}, true
}

// tightest finds the shortest stretch of text that holds every term: its start as a rune index, and its length in
// bytes; at is -1 when a term is missing.
func tightest(text []rune, terms [][]byte) (at, span int) {
	type occ struct{ pos, term int }
	var occs []occ
	s := string(text)
	for i, t := range terms {
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
	at, span = -1, 0
	count, have := make([]int, len(terms)), 0
	for lo, hi := 0, 0; hi < len(occs); hi++ {
		if count[occs[hi].term]++; count[occs[hi].term] == 1 {
			have++
		}
		for ; have == len(terms); lo++ {
			end := occs[hi].pos + len(terms[occs[hi].term])
			if at < 0 || end-occs[lo].pos < span {
				at, span = occs[lo].pos, end-occs[lo].pos
			}
			if count[occs[lo].term]--; count[occs[lo].term] == 0 {
				have--
			}
		}
	}
	if at >= 0 {
		at = utf8.RuneCountInString(s[:at])
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
			parts = append(parts, string(b.Input))
		case len(b.Content) > 0:
			parts = append(parts, messageText(b.Content))
		}
	}
	return strings.Join(parts, " ")
}
