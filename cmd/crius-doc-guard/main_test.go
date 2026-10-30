package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// costNotes are the real names in crius docs/notes on 10-30: three cost studies and two cost-lever notes, each a new
// doc for a new angle where the old one should have been rewritten.
var costNotes = []string{
	"COST-LEVER3-FRESH.md", "COST-LEVERS-STATUS.md", "COST-STUDY-2026-10-18.md", "COST-STUDY-2026-10-25-A.md",
	"COST-STUDY-2026-10-25-B.md", "RESUME.md", "step2.md", "TSTACK-COMPARISON-2026-10-26.md",
}

// testRoot makes an empty folder for one test. Not t.TempDir: with TMPDIR under /tmp (Linux, a sandbox, the
// scratchpad) every test folder would sit in a "tmp" series folder, where the guard refuses nothing.
func testRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(".", ".doc-guard-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if seriesDir(abs) {
		t.Fatalf("test folder %s is under a series folder, where the guard refuses nothing", abs)
	}
	return abs
}

// notes makes a docs/notes folder with the real names and returns its path.
func notes(t *testing.T, root string) string {
	t.Helper()
	dir := filepath.Join(root, "docs", "notes")
	mkfiles(t, dir, costNotes...)
	return dir
}

func mkfiles(t *testing.T, dir string, names ...string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func write(p string) input {
	in := input{ToolName: "Write"}
	in.ToolInput.FilePath = p
	return in
}

func bash(cmd, cwd string) input {
	in := input{ToolName: "Bash", Cwd: cwd}
	in.ToolInput.Command = cmd
	return in
}

func TestNewDocOnAnExistingTopicIsDenied(t *testing.T) {
	dir := notes(t, testRoot(t))
	for _, tc := range []struct{ name, want string }{
		{"COST-STUDY-2026-10-31.md", "COST-STUDY-2026-10-18.md"}, // the 10-18 / 10-25 pattern, a fourth time
		{"COST-STUDY-2026-10-25-C.md", "COST-STUDY-2026-10-18.md"},
		{"COST-STUDY.md", "COST-STUDY-2026-10-18.md"},
		{"cost-study-v2.md", "COST-STUDY-2026-10-18.md"},
		{"COST-STUDY-FRESH.md", "COST-STUDY-2026-10-18.md"},
		{"COST_STUDY_FINAL.md", "COST-STUDY-2026-10-18.md"},
		{"COST-STUDY-20261031.md", "COST-STUDY-2026-10-18.md"},
		{"COST-STUDY-1031.md", "COST-STUDY-2026-10-18.md"},
		{"COST-STUDY-2.txt", "COST-STUDY-2026-10-18.md"},
		{"COST-STUDY-DRAFT-v3.md", "COST-STUDY-2026-10-18.md"},
		{"COST-LEVERS-STATUS-UPDATED.md", "COST-LEVERS-STATUS.md"},
		{"COST-LEVER3-NEW.md", "COST-LEVER3-FRESH.md"}, // COST-LEVER3-FRESH is itself the "fresh" copy of COST-LEVER3
		{"TSTACK-COMPARISON-2026-10-31.md", "TSTACK-COMPARISON-2026-10-26.md"},
		{"RESUME-v2.md", "RESUME.md"},
	} {
		why := check(write(filepath.Join(dir, tc.name)))
		if !strings.Contains(why, "A doc on this topic exists: "+filepath.Join(dir, tc.want)) ||
			!strings.Contains(why, "Rewrite it in place (rule: one document per topic)") {
			t.Errorf("%s: %q", tc.name, why)
		}
	}
}

func TestNewTopicOrExistingDocIsAllowed(t *testing.T) {
	root := testRoot(t)
	dir := notes(t, root)
	for _, p := range []string{
		filepath.Join(dir, "KAFKA-RETRY-DESIGN.md"),             // a new topic
		filepath.Join(dir, "COST-STUDY-2026-10-18.md"),          // an existing doc rewritten in place
		filepath.Join(dir, "COST-STUDY-2026-10-25-B.md"),        // the same
		filepath.Join(dir, "COST-STUDY-2026-10-31.go"),          // not a doc
		filepath.Join(dir, "COST-STUDY-2026-10-31.json"),        // not a doc
		filepath.Join(root, "docs", "COST-STUDY-2026-10-31.md"), // another folder holds no cost study
		filepath.Join(root, "new", "COST-STUDY-2026-10-31.md"),  // a folder that does not exist yet
		filepath.Join(dir, "2026-10-31.md"),                     // a dated name with no topic in it
		filepath.Join(dir, "STEP3.md"),                          // step2 and STEP3 are different steps
	} {
		if why := check(write(p)); why != "" {
			t.Errorf("%s: %q", p, why)
		}
	}
	// Edits change an existing doc; they are what the rule asks for.
	in := write(filepath.Join(dir, "COST-STUDY-2026-10-31.md"))
	in.ToolName = "Edit"
	if why := check(in); why != "" {
		t.Errorf("Edit: %q", why)
	}
}

// Each pair would be refused in a plain folder (the control); under a series folder it passes.
func TestDatedSeriesFoldersAreAllowed(t *testing.T) {
	root := testRoot(t)
	for i, tc := range []struct{ dir, old, new string }{
		{"private/tmp/claude-501/x/scratchpad", "notes.md", "notes-2.md"},
		{".claude/jobs/bc6e4344/tmp/trackd/d4_logs", "run_data.txt", "run_data-2.txt"},
		{"work/.qa-scratch/t8", "real-fresh.txt", "real.txt"},
		{"svc/poc/verify/out", "offers-20260922-180232.md", "offers-20260922-180932.md"},
		{"staffy/docs/build/qa/evidence/parity-13a5685/home", "diff.txt", "diff-2.txt"},
		{"workflow/vendor/gstack/docs/designs", "PLAN_TUNING_V1.md", "PLAN_TUNING_V0.md"},
	} {
		plain := filepath.Join(root, "plain", strconv.Itoa(i))
		mkfiles(t, plain, tc.old)
		mkfiles(t, filepath.Join(root, tc.dir), tc.old)
		if why := check(write(filepath.Join(plain, tc.new))); why == "" {
			t.Errorf("control %s/%s: not refused", tc.dir, tc.new)
		}
		if why := check(write(filepath.Join(root, tc.dir, tc.new))); why != "" {
			t.Errorf("%s/%s: %q", tc.dir, tc.new, why)
		}
	}
	// ~/.crius/state/digest needs no entry: its names are only a date and time, which leave no topic.
	d := filepath.Join(root, ".crius", "state", "digest")
	mkfiles(t, d, "2026-10-30-0900.md")
	if why := check(write(filepath.Join(d, "2026-10-30-1800.md"))); why != "" {
		t.Errorf("digest: %q", why)
	}
}

func TestBashThatCreatesADocIsChecked(t *testing.T) {
	root := testRoot(t)
	dir := notes(t, root)
	mkfiles(t, dir, "my notes.md")
	mkfiles(t, filepath.Join(root, "other"), "COST-STUDY-2026-10-31.md")
	for _, cmd := range []string{
		"cat > docs/notes/COST-STUDY-2026-10-31.md <<'EOF'\n# Cost study\nEOF",
		"cat >docs/notes/COST-STUDY-2026-10-31.md <<EOF\nx\nEOF",
		"echo x >> docs/notes/COST-STUDY-2026-10-31.md",
		"cd docs/notes && cat > COST-STUDY-2026-10-31.md <<'EOF'\nx\nEOF",
		"cd " + dir + " && printf x > COST-STUDY-v2.md",
		"some-tool | tee docs/notes/COST-STUDY-2026-10-31.md",
		"some-tool | tee -a /dev/null " + filepath.Join(dir, "COST-STUDY-2026-10-31.md"),
		"cp docs/notes/COST-STUDY-2026-10-25-B.md docs/notes/COST-STUDY-2026-10-31.md",
		"cp docs/notes/COST-STUDY-2026-10-25-B.md \"docs/notes/COST-STUDY-v2.md\"",
		"cp other/COST-STUDY-2026-10-31.md docs/notes/",   // into a folder: the file keeps its name
		"cp other/COST-STUDY-2026-10-31.md docs/notes",    // the same, the folder without a slash
		"cp -t docs/notes other/COST-STUDY-2026-10-31.md", // -t DIR
		"echo x >& docs/notes/COST-STUDY-v2.md",           // >& file
		`echo x > docs/notes/COST\ STUDY\ 2026-10-31.md`,  // escaped spaces: "COST STUDY 2026-10-31.md"
		"cd docs/notes; echo x > COST-STUDY-v2.md",        // a cd that surely runs, then ;
	} {
		if why := check(bash(cmd, root)); !strings.Contains(why, "A doc on this topic exists: "+filepath.Join(dir, "COST-STUDY-2026-10-18.md")) {
			t.Errorf("%q: %q", cmd, why)
		}
	}
	for _, cmd := range []string{
		"cat > docs/notes/KAFKA-RETRY-DESIGN.md <<'EOF'\nx\nEOF",          // a new topic
		"cat > docs/notes/COST-STUDY-2026-10-18.md <<'EOF'\nx\nEOF",       // rewritten in place
		"echo x >> docs/notes/COST-STUDY-2026-10-25-B.md",                 // appended in place
		"cat docs/notes/COST-STUDY-2026-10-18.md > /dev/null",             // a read
		"go test ./... > docs/notes/COST-STUDY-2026-10-31.log",            // not a doc
		"cat > \"$OUT\"/COST-STUDY-2026-10-31.md <<'EOF'\nx\nEOF",         // a path the guard cannot read
		"cd \"$D\" && cat > COST-STUDY-2026-10-31.md <<'EOF'\nx\nEOF",     // a folder the guard cannot read
		"cp docs/notes/COST-STUDY-2026-10-18.md other/COST-STUDY-copy.md", // another stem in another folder
		`echo x >> docs/notes/my\ notes.md`,                               // an existing file, its space escaped
		`echo x >> "docs/notes/my notes.md"`,                              // the same, quoted
		"go test ./... 2> docs/notes/COST-STUDY-2.txt",                    // a stream other than stdout: a log
		"echo x >&2", // a stream, not a file
		// A cd that may not run in this shell must not move the folder: these write in root, which has no cost study.
		"(cd docs/notes); echo x > COST-STUDY-v2.md",
		"X=$(cd docs/notes && pwd); echo x > COST-STUDY-v2.md",
		"echo x | (cd docs/notes; cat); echo x > COST-STUDY-v2.md",
		"f() { cd docs/notes; }; echo x > COST-STUDY-v2.md",
		"false && cd docs/notes; echo x > COST-STUDY-v2.md",
		"true || cd docs/notes; echo x > COST-STUDY-v2.md",
		// Unparseable (an if with no fi): allowed, though it names a new cost study.
		"cat > docs/notes/COST-STUDY-2026-10-31.md <<'EOF'\nx\nEOF\nif [ ; then",
	} {
		if why := check(bash(cmd, root)); why != "" {
			t.Errorf("%q: %q", cmd, why)
		}
	}
}

// The guard steers agents; it is not a safety boundary, so input it cannot read lets the call go on, said on stderr.
func TestRunOutput(t *testing.T) {
	dir := notes(t, testRoot(t))
	var out, errb bytes.Buffer
	if err := run(strings.NewReader("{not json"), &out, &errb); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 || !strings.Contains(errb.String(), "crius-doc-guard: unreadable hook input") {
		t.Errorf("unreadable input: out %q, err %q", out.String(), errb.String())
	}

	out.Reset()
	errb.Reset()
	in, _ := json.Marshal(map[string]any{"tool_name": "Write", "tool_input": map[string]string{
		"file_path": filepath.Join(dir, "COST-STUDY-2026-10-31.md"), "content": "# x"}})
	if err := run(bytes.NewReader(in), &out, &errb); err != nil {
		t.Fatal(err)
	}
	var got struct {
		HookSpecificOutput struct {
			HookEventName            string `json:"hookEventName"`
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("%v: %q", err, out.String())
	}
	h := got.HookSpecificOutput
	if h.HookEventName != "PreToolUse" || h.PermissionDecision != "deny" ||
		!strings.Contains(h.PermissionDecisionReason, "If this really is a new topic, pick a name that says what is different.") {
		t.Errorf("%+v", h)
	}

	out.Reset()
	in, _ = json.Marshal(map[string]any{"tool_name": "Write", "tool_input": map[string]string{
		"file_path": filepath.Join(dir, "KAFKA-RETRY-DESIGN.md")}})
	if err := run(bytes.NewReader(in), &out, &errb); err != nil || out.Len() != 0 {
		t.Errorf("new topic: %v %q", err, out.String())
	}
}

// Names from the 10-30 scan of the real tree. A counter beside a doc without it is a copy; counters beside each other
// are a numbered series of distinct items, and a number inside a name is an id.
func TestCountersMarkACopyOnlyBesideTheOriginal(t *testing.T) {
	root := testRoot(t)
	for _, tc := range []struct {
		old  []string
		new  string
		deny bool
	}{
		{[]string{"BRIEF.md"}, "BRIEF-2.md", true},       // a lead state folder
		{[]string{"BRIEF-2.md"}, "BRIEF.md", true},       // the same pair the other way round
		{[]string{"checks.txt"}, "checks-3.txt", true},   // a build state folder
		{[]string{"qa-facts.md"}, "qa-facts-2.md", true}, // a build notes folder
		{[]string{"proof-5xx-watch.txt"}, "proof-5xx-watch-1018.txt", true},
		{[]string{"HANDOFF-build-2026-10-21.md"}, "HANDOFF-build-2026-10-21b.md", true}, // .crius/state/crius
		{[]string{"BRIEF-build-2026-10-19.md"}, "BRIEF-build-1029.md", true},            // .crius/state/crius
		{[]string{"ARCHITECTURE.md"}, "ARCHITECTURE_V2.md", true},
		{[]string{"verify-harness-v1.md"}, "verify-harness-v13.md", true}, // axon research/fx_hunt/qa
		{[]string{"real-fresh.txt"}, "real.txt", true},
		{[]string{"sprint-10.md", "sprint-11.md"}, "sprint-12.md", false}, // docs/sprints
		{[]string{"CLIENT-A.md", "CLIENT-B.md"}, "CLIENT-C.md", false},    // one doc per client
		{[]string{"OWNER-CARD-3.md"}, "OWNER-CARD-4.md", false},
		{[]string{"writer-1.md"}, "writer-2.md", false},
		{[]string{"rb26-P.txt"}, "rb26-Q.txt", false},
		{[]string{"F-236-PLAN.md"}, "F-131-PLAN.md", false}, // one plan per feature
		{[]string{"27-01-PLAN.md", "27-01-SUMMARY.md"}, "27-02-PLAN.md", false},
		{[]string{"proof-0076-live.md"}, "proof-0079-live.md", false},
		{[]string{"run_2680_correctness.txt"}, "run_1836_correctness.txt", false},
		{[]string{"ats-words-0609.txt"}, "ats-words-0617.txt", false},
		{[]string{"ids_20231002_part_1.txt"}, "ids_20231002_part_2.txt", false},
	} {
		dir, err := os.MkdirTemp(root, "")
		if err != nil {
			t.Fatal(err)
		}
		mkfiles(t, dir, tc.old...)
		why := check(write(filepath.Join(dir, tc.new)))
		if tc.deny != strings.Contains(why, "A doc on this topic exists: "+filepath.Join(dir, tc.old[0])) || !tc.deny && why != "" {
			t.Errorf("%v + %s: %q", tc.old, tc.new, why)
		}
	}
}

func TestTopicStem(t *testing.T) {
	for name, want := range map[string]string{
		"COST-STUDY-2026-10-18.md":       "cost-study",
		"COST-STUDY-2026-10-25-A.md":     "cost-study",
		"COST-STUDY-20261025.md":         "cost-study",
		"COST-LEVER3-FRESH.md":           "cost-lever3",
		"Plan_v2_FINAL.txt":              "plan",
		"HANDOFF-build-2026-10-21b.md":   "handoff-build",
		"offers-20260922-180932.md":      "offers",
		"COST-STUDY-2026-10-25-notes.md": "cost-study-notes",
		"COST-STUDY-2.md":                "cost-study-2",
		"report-120261030.md":            "report-120261030", // not a date: part of a longer number
		"2026-10-30.md":                  "",
		"2026-10-30-B.md":                "",
		"step2.md":                       "step2",
	} {
		if got := topicStem(name); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
}
