package playbook

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// staleModel marks the entries named in stale; it records what it was shown.
type staleModel struct {
	oneEach
	stale map[string]bool
	calls int
	rules string
	seen  []string
}

func (m *staleModel) Check(_ context.Context, rules string, es []Entry) ([]Stale, error) {
	m.calls++
	m.rules = rules
	var out []Stale
	for _, e := range es {
		m.seen = append(m.seen, e.ID)
		out = append(out, Stale{ID: e.ID, Stale: m.stale[e.ID], Why: "rules: only these guards stay (owner 11-08)"})
	}
	return out, nil
}

func staleBook(t *testing.T, dir string, ids ...string) {
	t.Helper()
	b := &Book{}
	for _, id := range ids {
		c := Candidate{ID: "seed:" + id, Team: "crius", Time: time.Now().AddDate(0, 0, -9), Ref: "seed " + id}
		if _, err := b.Apply(Op{Candidate: c.ID, Entry: Entry{ID: id, Topic: "harness-and-accounts", Kind: "trap",
			When: "a thing happens", Do: "do the thing", Because: "it worked"}}, c, c.Time.Format("2006-01-02")); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Save(dir); err != nil {
		t.Fatal(err)
	}
}

// Crius M3 eval (11-14): stale entries came back in round 2 (an entry about a
// guard removed 11-08) because the write step never linked them. Each night
// the live entries are checked against the current rule files and the latest
// decisions; what those say is gone is retired.
func TestStaleCheckRetiresWhatTheRulesRemoved(t *testing.T) {
	cfg, log := busConfig(t)
	rules := filepath.Join(t.TempDir(), "AGENTS.md")
	_ = os.WriteFile(rules, []byte("Few guards (owner 11-08): only the secret check stays.\n"), 0o644)
	cfg.RuleFiles = []string{rules}
	appendLine(t, log, time.Now().Add(-2*time.Hour).Format("2006-01-02T15:04")+" DECISION crius:lead -> shop:lead — post guard removed")
	appendLine(t, log, time.Now().AddDate(0, 0, -30).Format("2006-01-02T15:04")+" DECISION crius:lead -> shop:lead — an old decision")
	staleBook(t, cfg.BookDir, "post-guard-stops-scripts", "commit-only-paths")
	m := &staleModel{stale: map[string]bool{"post-guard-stops-scripts": true}}
	res, err := Run(context.Background(), cfg, m)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := LoadBook(cfg.BookDir)
	e := b.Find("post-guard-stops-scripts")
	if m.calls != 1 || !e.Archived || e.Retired == "" || len(res.Retired) != 1 || len(b.Live()) != 1 {
		t.Fatalf("calls %d, entry %+v, retired %v; want the guard entry retired after one check", m.calls, e, res.Retired)
	}
	if last := e.Evidence[len(e.Evidence)-1]; last.Outcome != "retired" || !strings.Contains(last.Note, "11-08") {
		t.Fatalf("retire evidence %+v, want outcome retired with the why", last)
	}
	if !strings.Contains(m.rules, "only the secret check") || !strings.Contains(m.rules, "post guard removed") || strings.Contains(m.rules, "an old decision") {
		t.Fatalf("rules shown: %q; want the rule file and this week's decisions only", m.rules)
	}
	// Checked entries wait a week: the same night again makes no check call.
	m.calls = 0
	if _, err := Run(context.Background(), cfg, m); err != nil || m.calls != 0 {
		t.Fatalf("second run: %d check calls (%v), want 0", m.calls, err)
	}
}

// One night may retire at most a twentieth of the book (5 at least); the rest
// are listed as needs a check, never dropped silently.
func TestStaleCheckIsCapped(t *testing.T) {
	cfg, log := busConfig(t)
	appendLine(t, log, time.Now().Add(-time.Hour).Format("2006-01-02T15:04")+" INFO shop:a -> crius:lead — nothing to learn")
	cfg.RuleFiles = []string{filepath.Join(t.TempDir(), "none.md")}
	_ = os.WriteFile(cfg.RuleFiles[0], []byte("rules"), 0o644)
	var ids []string
	stale := map[string]bool{}
	for i := 0; i < 8; i++ {
		id := "entry-" + string(rune('a'+i))
		ids = append(ids, id)
		stale[id] = true
	}
	staleBook(t, cfg.BookDir, ids...)
	res, err := Run(context.Background(), cfg, &staleModel{stale: stale})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Retired) != 5 || len(res.Review.NeedsCheck) != 3 {
		t.Fatalf("retired %v, needs a check %v; want 5 and 3", res.Retired, res.Review.NeedsCheck)
	}
}

// The stale check through the real model (one sonnet call). PLAYBOOK_LIVE=1.
func TestStaleCheckLive(t *testing.T) {
	if os.Getenv("PLAYBOOK_LIVE") != "1" {
		t.Skip("PLAYBOOK_LIVE=1 PLAYBOOK_CONFIG_DIR=<a Max account folder> runs claude -p")
	}
	rules := "== AGENTS.md\n- Few guards; trust the AI (owner, 2026-11-08: \"remove most of these guards\"). Only these stay: the secret check, " +
		"the no-AI-credit check, crius-hook (records events), and asks for production, force-push and delete commands. Azure DevOps posts, " +
		"searches and scripts never prompt.\n- When other sessions share the checkout, commit in one step: git commit --only -m \"...\" -- <paths>.\n" +
		"== latest decisions and corrections (oldest first)\n2026-11-08T02:40 CORRECTED crius:build -> crius:lead — Missed your 01:35 DECISION (post guard removed).\n"
	es := []Entry{
		{ID: "post-guard-cannot-read-scratch-scripts", When: "the Azure DevOps post guard stops a script it cannot read", Do: "Let the guard read the script itself and pass it when it has no ADO write; get the owner's OK for the hook change.", Because: "crius 2026-11-05: it asked 3 times in 25 min for read-only scripts"},
		{ID: "commit-only-paths-shared-index", When: "several sessions commit in one checkout", Do: "Commit with git commit --only -- <paths> in one step.", Because: "staffy 2026-09-26: another session's commit took staged files"},
		{ID: "typeorm-migrations-transaction-each", When: "a TypeORM 0.2 suite hangs on pending migrations", Do: "Set transaction: \"each\" in the data source.", Because: "svc_shop 2026-11-20: 600 s hang to 41 s"},
	}
	ss, err := ClaudeCLI{ConfigDir: os.Getenv("PLAYBOOK_CONFIG_DIR")}.Check(context.Background(), rules, es)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, s := range ss {
		got[s.ID] = s.Stale
		t.Logf("%s stale=%v why=%s", s.ID, s.Stale, s.Why)
	}
	if len(ss) != 3 || !got["post-guard-cannot-read-scratch-scripts"] || got["commit-only-paths-shared-index"] || got["typeorm-migrations-transaction-each"] {
		t.Fatalf("answers %v, want only the post guard entry stale", got)
	}
}

// partModel answers only for the stale entries, as a model may.
type partModel struct{ staleModel }

func (m *partModel) Check(ctx context.Context, rules string, es []Entry) ([]Stale, error) {
	all, _ := m.staleModel.Check(ctx, rules, es)
	var out []Stale
	for _, s := range all {
		if s.Stale {
			out = append(out, s)
		}
	}
	return out, nil
}

// Review (11-14): entries a model leaves out must not fill the check every night.
func TestEntriesLeftOutByTheCheckAreStillChecked(t *testing.T) {
	cfg, log := busConfig(t)
	appendLine(t, log, time.Now().Add(-time.Hour).Format("2006-01-02T15:04")+" INFO shop:a -> crius:lead — nothing")
	cfg.RuleFiles = []string{filepath.Join(t.TempDir(), "r.md")}
	_ = os.WriteFile(cfg.RuleFiles[0], []byte("rules"), 0o644)
	staleBook(t, cfg.BookDir, "keep-a", "keep-b", "gone-c")
	if _, err := Run(context.Background(), cfg, &partModel{staleModel{stale: map[string]bool{"gone-c": true}}}); err != nil {
		t.Fatal(err)
	}
	b, _ := LoadBook(cfg.BookDir)
	for _, id := range []string{"keep-a", "keep-b"} {
		if b.Find(id).Checked == "" {
			t.Errorf("%s left out by the model was not marked checked", id)
		}
	}
}

// Review (11-14): an owner preference is retired only by a rule file (the
// owner's words), never by a decision line; one named by a decision goes to
// needs a check.
func TestOwnerPreferenceRetiresOnlyOnARuleFile(t *testing.T) {
	cfg, log := busConfig(t)
	appendLine(t, log, time.Now().Add(-time.Hour).Format("2006-01-02T15:04")+" INFO shop:a -> crius:lead — nothing")
	cfg.RuleFiles = []string{filepath.Join(t.TempDir(), "AGENTS.md")}
	_ = os.WriteFile(cfg.RuleFiles[0], []byte("rules"), 0o644)
	b := &Book{}
	for _, id := range []string{"pref-by-decision", "pref-by-rule"} {
		c := Candidate{ID: "s:" + id, Team: "crius", Time: time.Now().AddDate(0, 0, -9), Ref: "s " + id}
		_, _ = b.Apply(Op{Candidate: c.ID, Entry: Entry{ID: id, Topic: "owner-preferences", Kind: "owner-preference",
			When: "a thing happens", Do: "do the thing", Because: "owner said"}}, c, "2026-11-01")
	}
	_ = b.Save(cfg.BookDir)
	m := &whyModel{why: map[string]string{"pref-by-decision": "2026-11-13 DECISION crius:lead: no longer", "pref-by-rule": "AGENTS.md (owner 11-08) replaced it"}}
	res, err := Run(context.Background(), cfg, m)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Retired) != 1 || res.Retired[0] != "pref-by-rule" || len(res.Review.NeedsCheck) != 1 || res.Review.NeedsCheck[0] != "pref-by-decision" {
		t.Fatalf("retired %v, needs a check %v", res.Retired, res.Review.NeedsCheck)
	}
}

type whyModel struct {
	oneEach
	why map[string]string
}

func (m *whyModel) Check(_ context.Context, _ string, es []Entry) ([]Stale, error) {
	var out []Stale
	for _, e := range es {
		out = append(out, Stale{ID: e.ID, Stale: m.why[e.ID] != "", Why: m.why[e.ID]})
	}
	return out, nil
}

// Review (11-14): an entry brought back by a newer sighting is checked again the
// next night, not a week later.
func TestARevivedEntryIsCheckedAgain(t *testing.T) {
	b, _ := retireBook(t, "trap")
	e := b.Find("some-entry")
	e.Archived, e.Retired, e.Checked = true, "2026-11-06", "2026-11-06"
	if _, err := b.Apply(Op{Candidate: "n", SameAs: "some-entry", Outcome: "hit"}, at(10, "n"), "2026-11-14"); err != nil {
		t.Fatal(err)
	}
	if e := b.Find("some-entry"); e.Archived || e.Checked != "" {
		t.Fatalf("entry %+v: want live with Checked cleared", e)
	}
}

// Review (11-14): the newest 150 lines were all from one day; owner lines come first.
func TestOwnerDecisionsSurviveTheCap(t *testing.T) {
	cfg, log := busConfig(t)
	now := time.Now()
	appendLine(t, log, now.AddDate(0, 0, -5).Format("2006-01-02T15:04")+" DECISION crius:lead -> shop:lead — [queue q7] owner: use the long name")
	for i := 0; i < decisionCap+20; i++ {
		appendLine(t, log, now.Add(-time.Duration(200-i)*time.Minute).Format("2006-01-02T15:04")+" DECISION crius:lead -> shop:lead — routine")
	}
	text, err := rulesText(cfg, now)
	if err != nil || !strings.Contains(text, "use the long name") || strings.Count(text, "routine") != decisionCap-1 {
		t.Fatalf("owner line kept %v, routine lines %d (err %v)", strings.Contains(text, "use the long name"), strings.Count(text, "routine"), err)
	}
}
