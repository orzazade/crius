package recall

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/store"
)

// Step 4 P6, recall v0: 20 questions with answers from STATUS.md (testdata/recall-v0.json), asked of the LIVE store.
// A question is answered when its answer is in the top 5 of find, or (the caller's next step) of find --deep with
// the question's project ranked first and the asking session (as the CLI sees it) last. The bar is 80%. Run: CRIUS_RECALL_LIVE=1 go test -run TestRecallV0 -v ./internal/recall
func TestRecallV0(t *testing.T) {
	if os.Getenv("CRIUS_RECALL_LIVE") == "" {
		t.Skip("asks the live store; set CRIUS_RECALL_LIVE=1")
	}
	b, err := os.ReadFile("testdata/recall-v0.json")
	if err != nil {
		t.Fatal(err)
	}
	var qs []struct{ Q, Query, Project, Answer, Source string }
	if err := json.Unmarshal(b, &qs); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(store.Home())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	in5 := func(r *Result, re *regexp.Regexp) bool {
		for i, h := range r.Hits {
			if i == 5 {
				break
			}
			if re.MatchString(h.ID + " " + h.Text) {
				return true
			}
		}
		return false
	}
	byFind, byDeep := 0, 0
	for _, x := range qs {
		re := regexp.MustCompile("(?i)" + x.Answer)
		q := Query{Text: x.Query, Now: time.Now()}
		r, err := Find(db, q, store.Types)
		if err != nil {
			t.Fatal(x.Q, err)
		}
		how := "MISS"
		if in5(r, re) {
			byFind++
			how = "find"
		} else {
			q.Project, q.Self = x.Project, os.Getenv("CLAUDE_CODE_SESSION_ID")
			if r, err = Deep(db, q, store.Types); err != nil {
				t.Fatal(x.Q, err)
			}
			if in5(r, re) {
				byDeep++
				how = "deep"
			}
		}
		t.Logf("%-4s %-55s %s (%s)", how, x.Q, x.Answer, x.Source)
	}
	score := float64(byFind+byDeep) / float64(len(qs))
	t.Logf("recall v0: %d/%d in the top 5 (find %d, deep %d) = %.0f%%", byFind+byDeep, len(qs), byFind, byDeep, 100*score)
	if score < 0.8 {
		t.Error(fmt.Sprintf("below the 80%% bar: %.0f%%", 100*score))
	}
}
