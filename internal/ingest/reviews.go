package ingest

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// reviewers are the agents whose definitions end a review with `FINDINGS P1=n P2=n P3=n` (step 6 P6, owner GO
// 2026-10-19 18:40).
var reviewers = map[string]bool{"codex": true, "quick-review": true}

var (
	// The line is found by its form anywhere in the answer: the first real run printed it before its list.
	findingsRe  = regexp.MustCompile("(?m)^[ \t*`]*FINDINGS P1=(\\d+) P2=(\\d+) P3=(\\d+)[ \t*`]*$")
	reviewedRe  = regexp.MustCompile(`(?m)^REVIEWED:\s*(?:commit\s+([0-9a-f]{7,40})\b|(uncommitted)\b|\S+\s+vs\s+([^\s,]+))`)
	codexAskRe  = regexp.MustCompile(`\bcodex-ask\s+\S+\s+(review|ask)\b(.*)`)
	codexTargRe = regexp.MustCompile(`--commit\s+([0-9a-f]{7,40})\b|--(uncommitted)\b|--base\s+(\S+)`)
	boundShaRe  = regexp.MustCompile(` commit=([0-9a-f]{7,40}) `)
)

// target names what a review looked at from a commit, uncommitted and base submatch: "commit=<sha>", "uncommitted",
// "base=<branch>", or "target=unknown".
func target(m []string, r *runner) string {
	switch {
	case m == nil:
		return "target=unknown"
	case m[1] != "":
		return "commit=" + m[1]
	case m[2] != "":
		return "uncommitted"
	}
	return "base=" + strings.ReplaceAll(truncate(r.text(m[3]), 100), " ", "_")
}

// reviewAnswers reads a reviewer's subagent file whole and stores each final answer (stop_reason end_turn) as
// evidence: kind review with the counts and the target, or kind review-uncounted when it has no FINDINGS line or
// lines that disagree, never a zero. A codex answer after a codex-ask `ask` (a plan critique) is no review. The
// revision is bound later, by bindReviews, once git has read the commit.
func (r *runner) reviewAnswers(tx *sql.Tx, path, sid, agent string) error {
	var codex []string // the newest codex-ask call: its mode and the rest of its line
	_, _, err := scanLines(path, cursor{}, func(pos int64, raw []byte) error {
		var o txLine
		var m struct {
			StopReason string          `json:"stop_reason"`
			Content    json.RawMessage `json:"content"`
		}
		if json.Unmarshal(raw, &o) != nil || o.Type != "assistant" || !isObject(o.Message) ||
			json.Unmarshal(o.Message, &m) != nil {
			return nil // a bad line is counted and alerted by transcriptFile
		}
		var blocks []struct {
			Type, Name, Text string
			Input            struct {
				Command string `json:"command"`
			} `json:"input"`
		}
		json.Unmarshal(m.Content, &blocks)
		var text []string
		for _, b := range blocks {
			if b.Type == "text" {
				text = append(text, b.Text)
			}
			if c := codexAskRe.FindStringSubmatch(b.Input.Command); b.Type == "tool_use" && b.Name == "Bash" && c != nil {
				codex = c
			}
		}
		if m.StopReason != "end_turn" {
			return nil
		}
		if o.UUID == "" {
			return fmt.Errorf("a %s answer in %s has no uuid; it cannot be stored once", agent, path)
		}
		answer := strings.Join(text, "\n")
		var where []string
		if agent == "codex" {
			if codex != nil && codex[1] == "ask" {
				return nil
			}
			if codex != nil {
				where = codexTargRe.FindStringSubmatch(codex[2])
			}
		} else {
			where = reviewedRe.FindStringSubmatch(answer)
		}
		kind, ref := "review", ""
		counts := map[string]bool{}
		for _, f := range findingsRe.FindAllStringSubmatch(answer, -1) {
			counts[fmt.Sprintf("P1=%s P2=%s P3=%s", f[1], f[2], f[3])] = true
		}
		switch len(counts) {
		case 0:
			kind, ref = "review-uncounted", agent+" no FINDINGS line"
		case 1:
			for c := range counts {
				ref = agent + " " + c + " " + target(where, r)
			}
		default:
			kind, ref = "review-uncounted", agent+" FINDINGS lines disagree"
		}
		_, err := tx.Exec("insert or ignore into evidence (kind, ref, ts, source_key) values (?,?,?,?)", kind,
			ref+" session="+sid, nullable(o.Timestamp), "review:"+o.UUID)
		return err
	})
	return err
}

// bindReviews sets the revision of each unbound review of a commit whose sha now names exactly one known commit.
func (r *runner) bindReviews() {
	rows, err := r.db.Query("select id, ref from evidence where kind = 'review' and revision is null and ref like '% commit=%'")
	if err != nil {
		r.warn(true, "cannot bind reviews to commits: %v", err)
		return
	}
	type unbound struct {
		id  int64
		sha string
	}
	var todo []unbound
	for rows.Next() {
		var u unbound
		var ref string
		if err := rows.Scan(&u.id, &ref); err != nil {
			rows.Close()
			r.warn(true, "cannot bind reviews to commits: %v", err)
			return
		}
		if m := boundShaRe.FindStringSubmatch(ref); m != nil {
			u.sha = m[1]
			todo = append(todo, u)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		r.warn(true, "cannot bind reviews to commits: %v", err)
		return
	}
	for _, u := range todo {
		if _, err := r.db.Exec("update evidence set revision = (select min(sha) from git_commit where substr(sha, 1, ?) = ?)"+
			" where id = ? and (select count(*) from git_commit where substr(sha, 1, ?) = ?) = 1",
			len(u.sha), u.sha, u.id, len(u.sha), u.sha); err != nil {
			r.warn(true, "cannot bind review %d to %s: %v", u.id, u.sha, err)
		}
	}
}
