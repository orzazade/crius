package ingest

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/orzazade/crius/internal/store"
	"github.com/orzazade/crius/internal/testutil"
)

// Step 6 P6b: a reviewer agent's final answer (codex, quick-review) is review evidence when it has a
// `FINDINGS P1=n P2=n P3=n` line, bound to the commit it reviewed.

var reviewedSha = "de68f7e" + strings.Repeat("0", 33)

// answer is a subagent's assistant line: its final answer when stop is end_turn.
func answer(uuid, ts, stop, text string, tools ...map[string]any) map[string]any {
	content := []any{map[string]any{"type": "text", "text": text}}
	for _, t := range tools {
		content = append(content, t)
	}
	return map[string]any{"type": "assistant", "uuid": uuid, "timestamp": ts, "isSidechain": true, "requestId": "req-" + uuid,
		"message": map[string]any{"id": "msg-" + uuid, "model": "claude-haiku-5-5", "role": "assistant", "stop_reason": stop,
			"content": content, "usage": map[string]any{"input_tokens": 5, "output_tokens": 10}}}
}

func bash(id, command string) map[string]any {
	return map[string]any{"type": "tool_use", "id": id, "name": "Bash", "input": map[string]any{"command": command}}
}

func (x *ic) subagent(name, agentType string, recs ...map[string]any) {
	x.t.Helper()
	sub := filepath.Join(x.personal, "-work-demo", x.sid, "subagents")
	testutil.Write(x.t, filepath.Join(sub, name+".meta.json"), `{"agentType": "`+agentType+`"}`)
	appendFile(x.t, filepath.Join(sub, name+".jsonl"), lines(recs...))
}

func (x *ic) commit(sha string) {
	x.t.Helper()
	db, err := store.Open(x.home)
	if err != nil {
		x.t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("insert into git_commit (sha, repo) values (?, 'r1')", sha); err != nil {
		x.t.Fatal(err)
	}
}

// The real run (de68f7e, 2026-10-19 18:4x) printed the FINDINGS line before the list, not last: it is found by its
// form anywhere in the answer, and the answer's REVIEWED line names the commit.
func TestAQuickReviewAnswerWithFindingsIsOneReviewRowOnItsCommit(t *testing.T) {
	x := newIC(t, cfg{})
	x.run()
	x.commit(reviewedSha)
	x.subagent("agent-q1", "quick-review",
		answer("q1a", "2026-10-19T18:40:00Z", "tool_use", "Looking at the diff.", bash("t1", "git show de68f7e")),
		answer("q1b", "2026-10-19T18:41:00Z", "end_turn", "REVIEWED: commit de68f7e (git show), 8 files\n\n"+
			"FINDINGS P1=0 P2=0 P3=1\n\n1. [P3] a.go:3 — minor\nVERDICT: 1 bug"))
	x.run()
	x.run()
	eq(t, x.q("select kind, ref, revision, ts from evidence where kind like 'review%'"), [][]string{{"review",
		"quick-review P1=0 P2=0 P3=1 commit=de68f7e session=" + x.sid, reviewedSha, "2026-10-19T18:41:00Z"}})
}

// A codex review names its target in the codex-ask command; a codex `ask` (plan critique) is no review, so its
// answer without a FINDINGS line is neither a row nor 'not counted'.
func TestACodexReviewIsBoundByItsCommandAndAnAskIsNoReview(t *testing.T) {
	x := newIC(t, cfg{})
	x.run()
	x.commit(reviewedSha)
	x.subagent("agent-c1", "codex",
		answer("c1a", "2026-10-19T18:30:00Z", "tool_use", "", bash("t1", "codex-ask /w review --commit de68f7e <<'Q'\nfocus\nQ")),
		answer("c1b", "2026-10-19T18:35:00Z", "end_turn", "1. **[P2] x** a.go:1\ncodex-ask: sol 3m\nFINDINGS P1=0 P2=1 P3=0"))
	x.subagent("agent-c2", "codex",
		answer("c2a", "2026-10-19T18:30:00Z", "tool_use", "", bash("t1", "codex-ask /w ask <<'Q'\nplan\nQ")),
		answer("c2b", "2026-10-19T18:35:00Z", "end_turn", "The plan misses a rollback.\ncodex-ask: sol 1m"))
	x.run()
	eq(t, x.q("select kind, ref, revision from evidence where kind like 'review%'"), [][]string{{"review",
		"codex P1=0 P2=1 P3=0 commit=de68f7e session=" + x.sid, reviewedSha}})
}

// A review answer with no FINDINGS line, or lines that disagree, is 'not counted' (its own row), never zero.
func TestAReviewWithoutAFindingsLineIsNotCountedNeverZero(t *testing.T) {
	x := newIC(t, cfg{})
	x.subagent("agent-q1", "quick-review",
		answer("q1b", "2026-10-19T18:41:00Z", "end_turn", "REVIEWED: uncommitted, 2 files\nVERDICT: no bugs found"))
	x.subagent("agent-q2", "quick-review",
		answer("q2b", "2026-10-19T18:42:00Z", "end_turn", "FINDINGS P1=0 P2=0 P3=0\n1. [P1] a.go:1 — crash\n"+
			"FINDINGS P1=1 P2=0 P3=0"))
	x.subagent("agent-q3", "quick-review",
		answer("q3b", "2026-10-19T18:43:00Z", "end_turn", "FINDINGS P1=0 P2=0"))
	x.run()
	eq(t, x.q("select kind, ref, revision from evidence where kind like 'review%' order by ts"), [][]string{
		{"review-uncounted", "quick-review no FINDINGS line session=" + x.sid, "<nil>"},
		{"review-uncounted", "quick-review FINDINGS lines disagree session=" + x.sid, "<nil>"},
		{"review-uncounted", "quick-review no FINDINGS line session=" + x.sid, "<nil>"}})
}

// A commit reviewed before it reached the branch git reads is bound by a later run; a review of uncommitted work
// is counted and says so, never bound.
func TestAReviewIsBoundOnceItsCommitIsKnown(t *testing.T) {
	x := newIC(t, cfg{})
	x.subagent("agent-q1", "quick-review",
		answer("q1b", "2026-10-19T18:41:00Z", "end_turn", "REVIEWED: commit de68f7e, 1 files\nVERDICT: no bugs found\n"+
			"FINDINGS P1=0 P2=0 P3=0"))
	x.subagent("agent-c1", "codex",
		answer("c1a", "2026-10-19T18:30:00Z", "tool_use", "", bash("t1", "cd /w &&\\\n  codex-ask /w review --uncommitted")),
		answer("c1b", "2026-10-19T18:35:00Z", "end_turn", "No findings.\nFINDINGS P1=0 P2=0 P3=0"))
	x.run()
	eq(t, x.q("select ref, revision from evidence where kind = 'review' order by ts"), [][]string{
		{"codex P1=0 P2=0 P3=0 uncommitted session=" + x.sid, "<nil>"},
		{"quick-review P1=0 P2=0 P3=0 commit=de68f7e session=" + x.sid, "<nil>"}})
	x.commit(reviewedSha)
	x.run()
	eq(t, x.q("select revision from evidence where kind = 'review' order by ts"), [][]string{{"<nil>"}, {reviewedSha}})
}

// Codex reviews of f095c93 and 83c4278: a target is never guessed. Calls in one command that disagree (two calls, an
// ask then a review, a call quoted in a here-document) give a review with no target; a revision expression
// (abc1234^, "abc1234"^) is not that commit; a quoted folder and a line continuation still parse.
func TestCodexAskCallsThatDisagreeGiveNoTarget(t *testing.T) {
	x := newIC(t, cfg{})
	x.run()
	x.commit(reviewedSha)
	x.commit("bbb2222" + strings.Repeat("0", 33))
	for i, c := range []struct{ command, answer string }{
		{"codex-ask /w review --commit de68f7e && codex-ask /w review --commit bbb2222", "FINDINGS P1=1 P2=0 P3=0"},
		{"codex-ask /w ask <<'Q'\nplan\nQ\ncodex-ask /w review --commit bbb2222", "FINDINGS P1=2 P2=0 P3=0"},
		{"codex-ask /w review --commit de68f7e^ && codex-ask /w review --commit \"de68f7e\"^", "FINDINGS P1=3 P2=0 P3=0"},
		{`codex-ask "/w/my project" ask`, "No review."},
		{"codex-ask /w review \\\n  --commit de68f7e", "FINDINGS P1=4 P2=0 P3=0"},
		{"codex-ask /w review --commit de68f7e <<'123'\nlast time: codex-ask /w review --commit bbb2222\n123", "FINDINGS P1=5 P2=0 P3=0"},
	} {
		n := strings.Repeat("x", i+1)
		x.subagent("agent-"+n, "codex", answer(n+"a", "2026-10-19T18:30:00Z", "tool_use", "", bash("t", c.command)),
			answer(n+"b", "2026-10-19T18:3"+string(rune('0'+i))+":00Z", "end_turn", c.answer))
	}
	x.subagent("agent-q", "quick-review", answer("qb", "2026-10-19T18:39:00Z", "end_turn",
		"REVIEWED: commit de68f7e~1, 3 files\nFINDINGS P1=6 P2=0 P3=0"))
	x.run()
	s := " session=" + x.sid
	eq(t, x.q("select kind, ref, coalesce(substr(revision, 1, 7), '') from evidence where kind like 'review%' order by ts"),
		[][]string{
			{"review", "codex P1=1 P2=0 P3=0 target=unknown" + s, ""},
			{"review", "codex P1=2 P2=0 P3=0 target=unknown" + s, ""},
			{"review", "codex P1=3 P2=0 P3=0 target=unknown" + s, ""},
			{"review", "codex P1=4 P2=0 P3=0 commit=de68f7e" + s, "de68f7e"},
			{"review", "codex P1=5 P2=0 P3=0 target=unknown" + s, ""},
			{"review", "quick-review P1=6 P2=0 P3=0 target=unknown" + s, ""}})
}

// Quick-review of 7a38fdc (the P6b real run): a sha in backticks or before a closing paren is still that commit, and
// a here-string (<<<) or a shift ($((1<<2))) hides no call.
func TestAShaInPunctuationIsBoundAndAShiftIsNoHereDocument(t *testing.T) {
	x := newIC(t, cfg{})
	x.run()
	x.commit(reviewedSha)
	for i, command := range []string{"r=$(codex-ask /w review --commit de68f7e)", "cat <<< \"x\"\ncodex-ask /w review --commit de68f7e",
		"echo $((1<<2))\ncodex-ask /w review --commit de68f7e"} {
		n := strings.Repeat("x", i+1)
		x.subagent("agent-"+n, "codex", answer(n+"a", "2026-10-19T18:30:00Z", "tool_use", "", bash("t", command)),
			answer(n+"b", "2026-10-19T18:3"+string(rune('0'+i))+":00Z", "end_turn", "FINDINGS P1=0 P2=0 P3=0"))
	}
	x.subagent("agent-q", "quick-review", answer("qb", "2026-10-19T18:39:00Z", "end_turn",
		"REVIEWED: commit `de68f7e`, 3 files\nFINDINGS P1=0 P2=0 P3=0"))
	x.run()
	eq(t, x.q("select distinct substr(ref, instr(ref, 'P3=0 ') + 5, 14), substr(revision, 1, 7) from evidence where kind = 'review'"),
		[][]string{{"commit=de68f7e", "de68f7e"}})
	eq(t, x.one("select count(*) from evidence where kind = 'review'"), "4")
}

// Quick-review of 063b163: a codex-ask call that cannot be parsed (an escaped space in the folder) must not leave an
// earlier call's target in place; the review has no target.
func TestAnUnparsedCodexAskCallClearsTheTarget(t *testing.T) {
	x := newIC(t, cfg{})
	x.run()
	x.commit(reviewedSha)
	x.subagent("agent-c", "codex", answer("ca", "2026-10-19T18:30:00Z", "tool_use", "", bash("t1", "codex-ask /w review --commit de68f7e")),
		answer("cb", "2026-10-19T18:31:00Z", "tool_use", "", bash("t2", `codex-ask /w/my\ project review --commit bbb2222`)),
		answer("cc", "2026-10-19T18:32:00Z", "end_turn", "FINDINGS P1=0 P2=0 P3=0"))
	x.run()
	eq(t, x.q("select ref, revision from evidence where kind = 'review'"), [][]string{
		{"codex P1=0 P2=0 P3=0 target=unknown session=" + x.sid, "<nil>"}})
}
