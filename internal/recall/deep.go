package recall

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/redact"
)

// The bounds of the transcript search (design §4 `find --deep`). deepTimeout is a var so a test can hit it.
const (
	retention    = 30 * 24 * time.Hour // Claude Code deletes transcripts after 30 days
	deepFiles    = 50
	deepCitation = 10
)

var deepTimeout = 2 * time.Minute

// Deep is the bounded transcript search: the transcripts of one project's sessions changed in the window (at most
// the last 30 days), newest 50 files, 2 minutes, 10 citations (path, message uuid, time, a redacted one-line
// snippet). It runs in Go, not rg, so the CLI needs nothing on PATH. A window that starts before the 30 days is
// expired: it shows the durable rows left, plus what the retained part of the window still has.
func Deep(db *sql.DB, q Query, busTypes map[string]bool) (*Result, error) {
	terms := deepTerms(q.Text)
	switch {
	case q.Project == "":
		return nil, fmt.Errorf("%w: --deep searches one project's transcripts; add --project", ErrQuery)
	case len(terms) == 0:
		return nil, fmt.Errorf("%w: --deep needs words to search for", ErrQuery)
	case q.Waiting || q.Active || len(q.Kinds) > 0:
		return nil, fmt.Errorf("%w: --deep takes only a query, --project and --since", ErrQuery)
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
	files, total, err := deepFilesOf(db, q.Project, from)
	if err != nil {
		return nil, err
	}
	if total > len(files) {
		res.Status = "partial"
		res.Reasons = append(res.Reasons, fmt.Sprintf("searched the newest %d of %d files", len(files), total))
	}
	res.Scope = fmt.Sprintf("transcripts of project %s (%d files, since %s UTC)", q.Project, len(files),
		from.UTC().Format("2006-01-02 15:04"))
	deadline := time.Now().Add(deepTimeout)
	for _, f := range files {
		if len(res.Hits) == deepCitation {
			res.More = true
			break
		}
		stopped, err := deepFile(f, terms, from, deadline, res)
		if err != nil {
			return nil, err
		}
		if stopped {
			res.Status = "partial"
			res.Reasons = append(res.Reasons, fmt.Sprintf("stopped at the %s time limit", deepTimeout))
			break
		}
	}
	sort.SliceStable(res.Hits, func(i, j int) bool { return res.Hits[i].TS > res.Hits[j].TS })
	if expired {
		res.Status = "expired"
		res.Reasons = append(res.Reasons, fmt.Sprintf("transcripts older than 30 days are gone (asked since %s); "+
			"durable rows below", q.Since.UTC().Format("2006-01-02")))
		durable, err := Find(db, Query{Text: q.Text, Project: q.Project, Since: q.Since, Now: q.Now}, busTypes)
		if err != nil {
			return nil, err
		}
		for _, h := range durable.Hits {
			h.Match = "durable"
			res.Hits = append(res.Hits, h)
		}
	}
	return res, nil
}

// deepTerms are the lower-cased words of the query; a message must hold all of them.
func deepTerms(text string) [][]byte {
	var terms [][]byte
	for _, w := range wordRe.FindAllString(strings.ToLower(text), -1) {
		terms = append(terms, []byte(w))
	}
	return terms
}

// deepFilesOf lists the project's session transcripts and their subagent files changed since from, newest first,
// cut to deepFiles; total is the count before the cut. A file gone from disk (expired) is skipped.
func deepFilesOf(db *sql.DB, project string, from time.Time) ([]string, int, error) {
	rows, err := db.Query("select distinct transcript_path from session where project = ? and transcript_path is not null", project)
	if err != nil {
		return nil, 0, err
	}
	var mains []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return nil, 0, err
		}
		mains = append(mains, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	type file struct {
		path string
		mt   time.Time
	}
	var all []file
	for _, m := range mains {
		subs, err := filepath.Glob(filepath.Join(strings.TrimSuffix(m, ".jsonl"), "subagents", "*.jsonl"))
		if err != nil {
			return nil, 0, err
		}
		for _, p := range append([]string{m}, subs...) {
			fi, err := os.Stat(p)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, 0, err
			}
			if !fi.ModTime().Before(from) {
				all = append(all, file{p, fi.ModTime()})
			}
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].mt.After(all[j].mt) })
	var out []string
	for i, f := range all {
		if i == deepFiles {
			break
		}
		out = append(out, f.path)
	}
	return out, len(all), nil
}

type deepLine struct {
	Type, UUID, Timestamp string
	Message               struct {
		Content json.RawMessage
	}
}

// deepFile adds the citations of one file; stopped is true when the deadline passed.
func deepFile(path string, terms [][]byte, from, deadline time.Time, res *Result) (stopped bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	for len(res.Hits) < deepCitation {
		if !time.Now().Before(deadline) {
			return true, nil
		}
		line, err := r.ReadBytes('\n')
		if len(line) > 0 && containsAll(bytes.ToLower(line), terms) {
			deepCite(path, line, terms, from, res)
		}
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		if err != nil {
			return false, fmt.Errorf("%s: %w", path, err)
		}
	}
	return false, nil
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
func deepCite(path string, line []byte, terms [][]byte, from time.Time, res *Result) {
	var l deepLine
	if json.Unmarshal(line, &l) != nil || (l.Type != "user" && l.Type != "assistant") {
		return
	}
	ts, err := time.Parse(time.RFC3339Nano, l.Timestamp)
	if err != nil || ts.Before(from) {
		return
	}
	text := strings.Join(strings.Fields(messageText(l.Message.Content)), " ")
	lower := strings.ToLower(text)
	if !containsAll([]byte(lower), terms) {
		return
	}
	// A window of the text around the first term; lower-casing keeps byte offsets for these scripts' letters, and
	// the cut is moved to rune boundaries.
	at := strings.Index(lower, string(terms[0]))
	runes := []rune(text)
	pos := len([]rune(text[:min(at, len(text))]))
	lo, hi := max(0, pos-80), min(len(runes), pos+120)
	snip, _ := redact.Redact(string(runes[lo:hi]))
	res.Hits = append(res.Hits, Hit{Kind: "transcript", ID: l.UUID, TS: l.Timestamp, Text: snip, Match: "deep", Path: path})
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
