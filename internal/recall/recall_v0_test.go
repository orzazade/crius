package recall

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/store"
)

// Step 4 P6, recall v0: questions with answers from STATUS.md, and (Crius 23:53) from elsewhere: bus lines, commits
// and hook events (testdata/recall-v0.json), asked of the LIVE store as it was when each question was written. A
// question is answered when its answer is in the top 5 of find, or (the caller's next step) of find --deep with the
// question's project ranked first. A STATUS.md answer is often found only where a transcript quoted STATUS.md, so
// the two scores are reported apart; the bar is 80% of all. Run:
// CRIUS_RECALL_LIVE=1 go test -run TestRecallV0 -v ./internal/recall
func TestRecallV0(t *testing.T) {
	if os.Getenv("CRIUS_RECALL_LIVE") == "" {
		t.Skip("asks the live store; set CRIUS_RECALL_LIVE=1")
	}
	b, err := os.ReadFile("testdata/recall-v0.json")
	if err != nil {
		t.Fatal(err)
	}
	var qs []evalQ
	if err := json.Unmarshal(b, &qs); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(store.Home())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	type tally struct{ n, find, deep int }
	score := map[string]*tally{"STATUS.md": {}, "elsewhere": {}}
	for _, x := range qs {
		how, err := ask(db, x, time.Now())
		if err != nil {
			t.Fatal(x.Q, err)
		}
		g := score["elsewhere"]
		if strings.HasPrefix(x.Source, "STATUS.md") {
			g = score["STATUS.md"]
		}
		g.n++
		switch how {
		case "find":
			g.find++
		case "deep":
			g.deep++
		}
		t.Logf("%-4s %-55s %s (%s)", how, x.Q, x.Answer, x.Source)
	}
	all, got := 0, 0
	for _, k := range []string{"STATUS.md", "elsewhere"} {
		g := score[k]
		all, got = all+g.n, got+g.find+g.deep
		t.Logf("answers from %-9s %2d/%2d in the top 5 (find %d, deep %d)", k, g.find+g.deep, g.n, g.find, g.deep)
	}
	t.Logf("recall v0: %d/%d = %.0f%%", got, all, 100*float64(got)/float64(all))
	if float64(got) < 0.8*float64(all) {
		t.Errorf("below the 80%% bar: %d/%d", got, all)
	}
}

// evalQ is one eval question: what is asked, the query typed, the project it is about, the answer as a regexp, where
// the answer was taken from, and when the question was written (RFC 3339): the store is asked as it was then.
type evalQ struct{ Q, Query, Project, Answer, Source, Asof string }

// ask asks one question as the eval does: "find" when the answer is in the top 5 of find, else "deep" when it is in
// the top 5 of find --deep with the question's project ranked first, else "MISS". It asks as no session, of the
// store up to the question's Asof, so the answer is the same for every caller and every later session.
func ask(db *sql.DB, x evalQ, now time.Time) (string, error) {
	re := regexp.MustCompile("(?i)" + x.Answer)
	until, err := time.Parse(time.RFC3339, x.Asof)
	if err != nil {
		return "", fmt.Errorf("%s: asof: %w", x.Q, err)
	}
	// The first screen: the top 5 hits and the commit block (Crius 01:02).
	in5 := func(r *Result) bool {
		for _, h := range append(slices.Clone(r.Hits[:min(5, len(r.Hits))]), r.Commits...) {
			if re.MatchString(h.ID + " " + h.Text) {
				return true
			}
		}
		return false
	}
	q := Query{Text: x.Query, Now: now, Until: until}
	r, err := Find(db, q, store.Types)
	if err != nil {
		return "", err
	}
	if in5(r) {
		return "find", nil
	}
	q.Project = x.Project
	if r, err = Deep(db, q, store.Types); err != nil {
		return "", err
	}
	if in5(r) {
		return "deep", nil
	}
	return "MISS", nil
}

// Crius 23:53: the eval scored 15/20 or 16/20 depending on who ran it. Its answer must not depend on the asking
// session (ranked last, the answer falls behind older quotes of the question), nor on sessions after the question
// was written (a check quotes the question and pushes the answer out of the top 5).
func TestEvalIsTheSameForAnyCaller(t *testing.T) {
	db := open(t)
	transcript(t, db, "falcon", "ans", msg("assistant", "a", "2026-10-14T10:00:00Z", `"the k3s hooks were armed in cf62176"`))
	var asked, later []string
	for i := range 6 {
		asked = append(asked, msg("user", fmt.Sprint("q", i), "2026-10-13T12:00:00Z", `"k3s hooks?"`))
		later = append(later, msg("user", fmt.Sprint("c", i), "2026-10-15T12:00:00Z", `"k3s hooks?"`))
	}
	transcript(t, db, "falcon", "asked", asked...)
	x := evalQ{Q: "Which commit armed the k3s hooks?", Query: "k3s hooks", Project: "falcon", Answer: "cf62176", Asof: "2026-10-15T00:00:00Z"}
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	before, err := ask(db, x, now)
	if err != nil || before == "MISS" {
		t.Fatal(before, err)
	}
	transcript(t, db, "falcon", "check", later...)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "ans")
	if after, err := ask(db, x, now); err != nil || after != before {
		t.Fatalf("%s before, %s after a later session and from the answering one (%v)", before, after, err)
	}
}

// Until leaves out what came later: rows of the text index and sessions found by id.
func TestFindUntil(t *testing.T) {
	db := open(t)
	event(t, db, "2026-10-14T10:00:00Z", "demo", "info", "zebra before", "")
	event(t, db, "2026-10-15T10:00:00Z", "demo", "info", "zebra after", "")
	exec(t, db, "insert into session (id, project, started) values (?,?,?)", "abcd1234-later", "demo", "2026-10-15T10:00:00Z")
	until := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	if r := find(t, db, Query{Text: "zebra", Until: until}); len(r.Hits) != 1 || !strings.Contains(r.Hits[0].Text, "before") {
		t.Fatalf("%+v", r.Hits)
	}
	if r := find(t, db, Query{Text: "abcd1234"}); len(r.Hits) != 1 {
		t.Fatalf("%+v", r.Hits)
	}
	if r := find(t, db, Query{Text: "abcd1234", Until: until}); len(r.Hits) != 0 {
		t.Fatalf("%+v", r.Hits)
	}
}
