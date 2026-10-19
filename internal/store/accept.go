package store

// `record accept` (design §7): an item is accepted on one revision, with a check for every criterion and one
// independent review, each bound to that revision. Anything else is refused and nothing is written.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type AcceptInput struct {
	Item, Revision string
	Checks         []string // "N=REF": criterion N (1-based) is met by REF, a commit sha or event:<n>
	Review, By     string   // Review: event:<n> of a VERIFIED line; By: crius or owner
}

type AcceptCheck struct {
	Criterion int    `json:"criterion"`
	Ref       string `json:"ref"`
}

var (
	hexRe     = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	sha256Re  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	eventRe   = regexp.MustCompile(`^event:([1-9][0-9]*)$`)
	shaWordRe = regexp.MustCompile(`\b(?:[0-9a-f]{64}|[0-9a-f]{7,40})\b`)
)

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// revisionOf gives the full revision: a commit sha prefix that names exactly one commit, or an artifact sha256.
func revisionOf(ctx context.Context, q querier, rev string) (string, error) {
	var table, col string
	switch {
	case sha256Re.MatchString(rev):
		table, col = "artifact", "sha256"
	case hexRe.MatchString(rev):
		table, col = "git_commit", "sha"
	default:
		return "", refused("--revision must be a commit sha (7-40 hex) or an artifact sha256, got %q", rev)
	}
	var full string
	var n int
	err := q.QueryRowContext(ctx, "select count(*), coalesce(min("+col+"), '') from "+table+" where substr("+col+
		", 1, ?) = ?", len(rev), rev).Scan(&n, &full)
	switch {
	case err != nil:
		return "", err
	case n == 0:
		return "", refused("revision %s is not a known %s", rev, map[string]string{"git_commit": "commit",
			"artifact": "artifact"}[table])
	case n > 1 && table == "git_commit":
		return "", refused("revision %s names %d commits; give more of the sha", rev, n)
	}
	return full, nil
}

type boundRef struct {
	ref, kind, session string
}

// bind resolves a ref and checks it names the revision: a commit's evidence row must be on it; every revision an event
// names (a sha in its refs; in its text, a sha256 or a 7-40 hex word with a letter and a digit) must resolve to it, and it must name
// one. A name that is ambiguous, unknown or another revision is refused: a line that also cites another sha does not
// say which one it checked (codex review of 43067a2).
func bind(ctx context.Context, q querier, ref, rev string) (boundRef, error) {
	if m := eventRe.FindStringSubmatch(ref); m != nil {
		var kind, refs, text string
		var session sql.NullString
		err := q.QueryRowContext(ctx, "select kind, coalesce(refs, ''), coalesce(text, ''), session from event where id = ?",
			m[1]).Scan(&kind, &refs, &text, &session)
		if err == sql.ErrNoRows {
			return boundRef{}, refused("%s is not a known event", ref)
		}
		if err != nil {
			return boundRef{}, err
		}
		var names []string
		for _, w := range shaWordRe.FindAllString(text, -1) {
			// a sha, not a number or a word like "defaced"; a sha256 is never an English word
			if len(w) == 64 || strings.ContainsAny(w, "abcdef") && strings.ContainsAny(w, "0123456789") {
				names = append(names, w)
			}
		}
		var r map[string][]any
		if refs != "" {
			if err := json.Unmarshal([]byte(refs), &r); err != nil {
				return boundRef{}, fmt.Errorf("%s has bad refs %q: %w", ref, refs, err)
			}
		}
		for _, v := range r["sha"] {
			n, ok := v.(string)
			if !ok || !hexRe.MatchString(n) && !sha256Re.MatchString(n) {
				return boundRef{}, refused("%s has a sha ref that is no sha: %v", ref, v)
			}
			names = append(names, n)
		}
		if len(names) == 0 {
			return boundRef{}, refused("%s names no revision", ref)
		}
		for _, n := range names {
			full := n
			if !sha256Re.MatchString(n) {
				var k int
				if err := q.QueryRowContext(ctx, "select count(*), coalesce(min(sha), '') from git_commit where"+
					" substr(sha, 1, ?) = ?", len(n), n).Scan(&k, &full); err != nil {
					return boundRef{}, err
				}
				switch {
				case k == 0:
					return boundRef{}, refused("%s names %s, which is no known commit", ref, n)
				case k > 1:
					return boundRef{}, refused("%s names %s, which matches %d commits", ref, n, k)
				}
			}
			if full != rev {
				return boundRef{}, refused("%s is on %s, not on %s", ref, short(full), short(rev))
			}
		}
		return boundRef{ref, kind, session.String}, nil
	}
	if !hexRe.MatchString(ref) {
		return boundRef{}, refused("%s is no evidence ref (a commit sha or event:<n>)", ref)
	}
	var evRev string
	var n int
	if err := q.QueryRowContext(ctx, "select count(*), coalesce(min(revision), '') from evidence where kind = 'commit'"+
		" and substr(ref, 1, ?) = ?", len(ref), ref).Scan(&n, &evRev); err != nil {
		return boundRef{}, err
	}
	switch {
	case n == 0:
		return boundRef{}, refused("%s is not a known commit", ref)
	case n > 1:
		return boundRef{}, refused("%s names %d commits; give more of the sha", ref, n)
	case evRev != rev:
		return boundRef{}, refused("%s is on %s, not on %s", ref, short(evRev), short(rev))
	}
	return boundRef{ref: evRev, kind: "commit"}, nil
}

func short(rev string) string { return rev[:min(len(rev), 12)] }

func RecordAccept(db *sql.DB, in AcceptInput) (string, error) {
	if in.By != "crius" && in.By != "owner" {
		return "", refused("--by must be crius or owner, got %q", in.By)
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "begin immediate"); err != nil {
		return "", err
	}
	done := false
	defer func() {
		if !done {
			conn.ExecContext(ctx, "rollback")
		}
	}()
	var project, criteria, state string
	switch err := conn.QueryRowContext(ctx, "select project, criteria, state from item where id = ?", in.Item).
		Scan(&project, &criteria, &state); err {
	case nil:
	case sql.ErrNoRows:
		return "", refused("%s is not a known item", in.Item)
	default:
		return "", err
	}
	if state != "open" {
		return "", refused("%s is %s, not open", in.Item, state)
	}
	var crit []string
	if err := json.Unmarshal([]byte(criteria), &crit); err != nil {
		return "", fmt.Errorf("item %s has bad criteria %q: %w", in.Item, criteria, err)
	}
	rev, err := revisionOf(ctx, conn, in.Revision)
	if err != nil {
		return "", err
	}
	met := make([]bool, len(crit))
	var checks []AcceptCheck
	for _, c := range in.Checks {
		ns, ref, ok := strings.Cut(c, "=")
		n, err := strconv.Atoi(ns)
		if !ok || err != nil {
			return "", refused("--check must be N=REF, got %q", c)
		}
		if n < 1 || n > len(crit) {
			return "", refused("%s has criteria 1-%d, got --check %d", in.Item, len(crit), n)
		}
		b, err := bind(ctx, conn, ref, rev)
		if err != nil {
			return "", err
		}
		met[n-1] = true
		checks = append(checks, AcceptCheck{n, b.ref})
	}
	for i, ok := range met {
		if !ok {
			return "", refused("criterion %d (%s) has no check", i+1, crit[i])
		}
	}
	review, err := bind(ctx, conn, in.Review, rev)
	if err != nil {
		return "", err
	}
	if review.kind != "verified" {
		return "", refused("the review %s is a %s line; a review is a VERIFIED line", in.Review, review.kind)
	}
	var author sql.NullString
	if err := conn.QueryRowContext(ctx, "select session from git_commit where sha = ?", rev).Scan(&author); err != nil &&
		err != sql.ErrNoRows {
		return "", err
	}
	if review.session != "" && review.session == author.String {
		return "", refused("the review %s comes from session %s, which made %s", in.Review, review.session, short(rev))
	}
	cj, err := json.Marshal(checks)
	if err != nil {
		return "", err
	}
	ts := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	res, err := conn.ExecContext(ctx, "insert into acceptance (item, revision, checks, reviewer, review_ref, accepted_by, ts)"+
		" values (?,?,?,nullif(?, ''),?,?,?)", in.Item, rev, string(cj), review.session, in.Review, in.By, ts)
	if err != nil {
		return "", err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return "", err
	}
	refs, err := json.Marshal(map[string][]string{"item": {in.Item}, "sha": {rev}})
	if err != nil {
		return "", err
	}
	text := fmt.Sprintf("accepted %s on %s by %s (review %s)", in.Item, short(rev), in.By, in.Review)
	if _, err := conn.ExecContext(ctx, "insert into event (ts, project, kind, actor, item, refs, text, verified, source_key)"+
		" values (?,?,'accepted',?,?,?,?,1,?)", ts, project, in.By, in.Item, string(refs), text,
		fmt.Sprint("accept:", id)); err != nil {
		return "", err
	}
	if _, err := conn.ExecContext(ctx, "update item set state = 'accepted' where id = ?", in.Item); err != nil {
		return "", err
	}
	if _, err := conn.ExecContext(ctx, "commit"); err != nil {
		return "", err
	}
	done = true
	return text, nil
}
