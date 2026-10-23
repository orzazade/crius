package statelog

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	crius "github.com/orzazade/crius"
	"github.com/orzazade/crius/internal/sessionstate"
	"github.com/orzazade/crius/internal/store"
	"github.com/orzazade/crius/internal/testutil"
)

// today's real lines (10-22): wakewatch's pair and promptwatch's single line
const wake = `2026-10-22T16:23:29+04:00 tab:surface:1000020057 "quotes · p1" screen=idle state=busy decides=screen
2026-10-22T16:23:29+04:00 tab:surface:1000020056 "quotes · p6" screen=idle state=busy decides=screen
2026-10-22T16:28:30+04:00 tab:surface:1000020057 "quotes · p1" screen=busy state=busy decides=screen
2026-10-22T16:38:39+04:00 tab:surface:1000020055 "quotes · p2" screen=idle state=busy decides=screen
`

const prompt = `2026-10-22T15:36:10+0400 quotes_·_p3 workspace:1000020007 surface:1000020058 screen=prompt state=running decides=screen
2026-10-22T15:40:00+0400 quotes_·_p4 workspace:1000020007 surface:1000020059 screen=prompt state=idle decides=screen
`

func TestParseReadsBothLogs(t *testing.T) {
	w, err := Parse("wakewatch", strings.NewReader(wake))
	if err != nil {
		t.Fatal(err)
	}
	if len(w) != 4 {
		t.Fatalf("got %d lines", len(w))
	}
	want := Line{Time: time.Date(2026, 10, 22, 12, 23, 29, 0, time.UTC), Log: "wakewatch", Key: "tab:surface:1000020057",
		Tab: "quotes · p1", Screen: "idle", State: "busy", Decides: "screen"}
	if got := w[0]; !got.Time.Equal(want.Time) || got.Log != want.Log || got.Key != want.Key || got.Tab != want.Tab ||
		got.Screen != want.Screen || got.State != want.State || got.Decides != want.Decides {
		t.Errorf("got %+v, want %+v", got, want)
	}
	p, err := Parse("promptwatch", strings.NewReader(prompt))
	if err != nil {
		t.Fatal(err)
	}
	if len(p) != 2 || p[0].Tab != "quotes · p3" || p[0].Key != "workspace:1000020007/surface:1000020058" ||
		p[0].Screen != "prompt" || p[0].State != "running" || !p[0].Time.Equal(time.Date(2026, 10, 22, 11, 36, 10, 0, time.UTC)) {
		t.Errorf("got %+v", p)
	}
}

// A line that is not in the log's format is a failed read, not a line to skip.
func TestALineNotInTheFormatIsAnError(t *testing.T) {
	for _, c := range []struct{ log, text string }{
		{"wakewatch", "2026-10-22T16:23:29+04:00 tab:surface:1 quotes p1 screen=idle state=busy decides=screen\n"},
		{"wakewatch", "yesterday tab:surface:1 \"quotes · p1\" screen=idle state=busy decides=screen\n"},
		{"promptwatch", "2026-10-22T15:36:10+0400 quotes_·_p3 workspace:1 surface:2 screen=prompt decides=screen\n"},
	} {
		if _, err := Parse(c.log, strings.NewReader(c.text)); err == nil || !strings.Contains(err.Error(), "line 1") {
			t.Errorf("%s %q: err %v, want one naming line 1", c.log, c.text, err)
		}
	}
}

// tx is one transcript line at a UTC time: "u" a prompt or tool result ("u:<id>" the result of tool call id), "esc" the
// line Esc writes, "meta" a meta user line, "compact" the summary a compaction writes, "text:<content>" a user line with
// that text, "half" the start of a line still being written, "a:<stop_reason>:<request id>" a reply line
// ("a:tool_use:<request id>:<id>" one that calls tool id).
type tx struct{ at, kind string }

// transcript writes one transcript under dir and the store's request row that points at it, spanning its lines.
func transcript(t *testing.T, db *sql.DB, dir, sid, file string, lines ...tx) string {
	path := filepath.Join(dir, file)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	b := strings.Builder{}
	b.WriteString(`{"type":"queue-operation","timestamp":"2026-10-22T00:00:00Z"}` + "\n")
	first, last := "2026-10-22T00:00:00Z", "2026-10-22T00:00:00Z"
	for i, l := range lines {
		ts := "2026-10-22T" + l.at + "Z"
		if i == 0 {
			first = ts
		}
		last = ts
		switch kind, rest, _ := strings.Cut(l.kind, ":"); kind {
		case "u":
			fmt.Fprintf(&b, `{"type":"user","timestamp":%q,"message":{"content":[{"type":"tool_result","tool_use_id":%q,"content":"ok"}]}}`+"\n", ts, rest)
		case "esc":
			fmt.Fprintf(&b, `{"type":"user","timestamp":%q,"message":{"content":[{"type":"text","text":"[Request interrupted by user for tool use]"}]}}`+"\n", ts)
		case "meta":
			fmt.Fprintf(&b, `{"type":"user","isMeta":true,"timestamp":%q,"message":{"content":"<context>"}}`+"\n", ts)
		case "compact":
			fmt.Fprintf(&b, `{"type":"user","isCompactSummary":true,"timestamp":%q,"message":{"content":"This session is being continued"}}`+"\n", ts)
		case "text":
			fmt.Fprintf(&b, `{"type":"user","timestamp":%q,"message":{"content":%q}}`+"\n", ts, rest)
		case "half":
			fmt.Fprintf(&b, `{"type":"assistant","timestamp":%q,"message":{"stop_`, ts)
		case "a":
			stop, rest, _ := strings.Cut(rest, ":")
			req, id, _ := strings.Cut(rest, ":")
			content := "[]"
			if id != "" {
				content = fmt.Sprintf(`[{"type":"tool_use","id":%q}]`, id)
			}
			fmt.Fprintf(&b, `{"type":"assistant","timestamp":%q,"requestId":%q,"message":{"stop_reason":%q,"content":%s}}`+"\n", ts, req, stop, content)
		}
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("insert into request (request_id, session, ts_first, ts_last, transcript_path) values (?, ?, ?, ?, ?)",
		path, sid, first, last, path); err != nil {
		t.Fatal(err)
	}
	return path
}

func fixture(t *testing.T) (*sql.DB, []sessionstate.State, string) {
	_, home := testutil.Temp(t)
	if err := store.InitLayout(home, crius.ProjectsExample); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	dir := t.TempDir()
	// p1 at 16:23:29 (+04): a reply streaming across the line
	transcript(t, db, dir, "s-p1", "s-p1.jsonl", tx{"12:23:20", "u"}, tx{"12:23:22", "a:end_turn:r1"}, tx{"12:23:38", "a:end_turn:r1"})
	// p6: its turn ended at 16:20
	transcript(t, db, dir, "s-p6", "s-p6.jsonl", tx{"12:20:00", "a:end_turn:r0"})
	// p2: its turn ended at 16:30; only a subagent worked at 16:38
	transcript(t, db, dir, "s-p2", "s-p2.jsonl", tx{"12:30:00", "a:end_turn:r0"})
	transcript(t, db, dir, "s-p2", "s-p2/subagents/agent-a1.jsonl", tx{"12:38:30", "u"}, tx{"12:38:50", "a:tool_use:r9"})
	// p3: the "dialog" was code on screen; a tool result went back at 15:36:05 and the reply came at 15:36:30
	transcript(t, db, dir, "s-p3", "s-p3.jsonl", tx{"11:36:05", "u"}, tx{"11:36:30", "a:tool_use:r3"})
	// p4: a tool call open at 15:40, with the screen showing a dialog
	transcript(t, db, dir, "s-p4", "s-p4.jsonl", tx{"11:39:50", "a:tool_use:r4"})
	var ss []sessionstate.State
	for _, r := range []string{"p1", "p2", "p3", "p4", "p6"} {
		ss = append(ss, sessionstate.State{SessionID: "s-" + r, Project: "quotes", Role: r})
	}
	return db, ss, dir
}

func TestEachDisagreementSaysWhichSideWasRight(t *testing.T) {
	db, ss, _ := fixture(t)
	w, _ := Parse("wakewatch", strings.NewReader(wake))
	p, _ := Parse("promptwatch", strings.NewReader(prompt))
	fs, err := Judge(db, ss, append(w, p...))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range fs {
		got[f.Tab+" "+f.Time.UTC().Format("15:04")] = f.Right
	}
	want := map[string]string{
		"quotes · p1 12:23": "state",   // a reply streaming across the line
		"quotes · p6 12:23": "screen",  // its turn had ended
		"quotes · p2 12:38": "screen",  // only a subagent: the main thread was free
		"quotes · p3 11:36": "state",   // running and it worked: no dialog held it
		"quotes · p4 11:40": "unknown", // a tool call open: running or a dialog
	}
	if len(got) != len(want) {
		t.Errorf("got %d findings %v, want %d (the agreeing line is not one)", len(got), got, len(want))
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: right %q, want %q", k, got[k], v)
		}
	}
}

// A disagreement lasts until the tab's next line; one with no next line is still open at the log's end.
func TestADisagreementLastsUntilTheTabsNextLine(t *testing.T) {
	db, ss, _ := fixture(t)
	w, _ := Parse("wakewatch", strings.NewReader(wake))
	fs, err := Judge(db, ss, w)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fs {
		switch f.Tab {
		case "quotes · p1":
			if f.Until.Sub(f.Time) != 5*time.Minute+time.Second {
				t.Errorf("p1 until %v", f.Until)
			}
		case "quotes · p6":
			if !f.Until.IsZero() {
				t.Errorf("p6 until %v, want still open", f.Until)
			}
		}
	}
}

// A tab with no session file, no transcript, an unreadable one or no line before the time is not known: never "screen"
// for want of evidence.
func TestNoEvidenceIsUnknownNotScreen(t *testing.T) {
	db, ss, _ := fixture(t)
	if _, err := db.Exec("insert into request (request_id, session, ts_first, ts_last, transcript_path) values" +
		" ('gone', 's-p7', '2026-10-22T00:00:00Z', '2026-10-22T00:00:00Z', '/no/such/s-p7.jsonl')"); err != nil {
		t.Fatal(err)
	}
	ss = append(ss, sessionstate.State{SessionID: "s-p5", Project: "quotes", Role: "p5"},
		sessionstate.State{SessionID: "s-p7", Project: "quotes", Role: "p7"})
	late := `2026-10-22T10:00:00+04:00 tab:surface:1000020056 "quotes · p6" screen=idle state=busy decides=screen
2026-10-22T16:23:29+04:00 tab:surface:9 "quotes · rig" screen=idle state=busy decides=screen
2026-10-22T16:23:29+04:00 tab:surface:8 "scratch" screen=idle state=busy decides=screen
2026-10-22T16:23:29+04:00 tab:surface:5 "quotes · p5" screen=idle state=busy decides=screen
2026-10-22T16:23:29+04:00 tab:surface:7 "quotes · p7" screen=idle state=busy decides=screen
`
	w, _ := Parse("wakewatch", strings.NewReader(late))
	fs, err := Judge(db, ss, w)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"quotes · p6": "no line of the main thread before it",
		"quotes · rig": "no session file for quotes rig", "scratch": "not a project · role tab",
		"quotes · p5": "no transcript in the store for quotes p5", "quotes · p7": "the transcript: open /no/such/s-p7.jsonl"}
	if len(fs) != len(want) {
		t.Fatalf("got %d findings", len(fs))
	}
	for _, f := range fs {
		if f.Right != "unknown" || want[f.Tab] == "" || !strings.Contains(f.Why, want[f.Tab]) {
			t.Errorf("%s: right %q why %q, want unknown with %q", f.Tab, f.Right, f.Why, want[f.Tab])
		}
	}
}

// Today's quotes · p6 at 17:38:53 (+04), and the deep review's cases (10-22): a tool still open, an Esc during a tool,
// a turn that ended 40 s before the line, two sessions of one role, a meta line after the turn.
func TestWhatTheMainThreadDidDecides(t *testing.T) {
	db, _, dir := fixture(t)
	transcript(t, db, dir, "s-run", "s-run.jsonl", tx{"13:36:18.172", "a:tool_use:r1"}, tx{"13:46:18.771", "u"},
		tx{"13:46:21.863", "a:end_turn:r2"})
	transcript(t, db, dir, "s-open", "s-open.jsonl", tx{"12:00:00", "a:tool_use:r1"})
	transcript(t, db, dir, "s-esc", "s-esc.jsonl", tx{"11:50:00", "a:tool_use:r1"}, tx{"11:51:00", "u"},
		tx{"11:51:00", "esc"}, tx{"12:30:00", "u"})
	transcript(t, db, dir, "s-done", "s-done.jsonl", tx{"11:59:20", "a:end_turn:r1"})
	transcript(t, db, dir, "s-old", "s-old.jsonl", tx{"11:00:00", "a:tool_use:r1"}) // crashed with a tool open
	transcript(t, db, dir, "s-new", "s-new.jsonl", tx{"11:58:00", "u"}, tx{"11:59:00", "a:end_turn:r2"})
	transcript(t, db, dir, "s-meta", "s-meta.jsonl", tx{"11:59:00", "a:end_turn:r1"}, tx{"11:59:30", "meta"})
	ss := []sessionstate.State{{SessionID: "s-run", Project: "quotes", Role: "p6"},
		{SessionID: "s-open", Project: "q", Role: "open"}, {SessionID: "s-esc", Project: "q", Role: "esc"},
		{SessionID: "s-done", Project: "q", Role: "done"}, {SessionID: "s-old", Project: "q", Role: "two"},
		{SessionID: "s-new", Project: "q", Role: "two"}, {SessionID: "s-meta", Project: "q", Role: "meta"}}
	w, _ := Parse("wakewatch", strings.NewReader(`2026-10-22T17:38:53+04:00 tab:surface:6 "quotes · p6" screen=idle state=busy decides=screen
2026-10-22T16:10:00+04:00 tab:surface:1 "q · open" screen=idle state=busy decides=screen
2026-10-22T16:00:00+04:00 tab:surface:2 "q · esc" screen=idle state=busy decides=screen
2026-10-22T16:00:00+04:00 tab:surface:3 "q · done" screen=idle state=busy decides=screen
2026-10-22T16:00:00+04:00 tab:surface:4 "q · two" screen=idle state=busy decides=screen
2026-10-22T16:00:00+04:00 tab:surface:5 "q · meta" screen=idle state=busy decides=screen
`))
	want := map[string][2]string{
		"quotes · p6": {"state", "a tool call open from 17:36:18 to 17:46:18"},
		"q · open":    {"state", "a tool call open since 16:00:00, no result yet"},
		"q · esc":     {"screen", "Esc at 15:51:00"},
		"q · done":    {"screen", "the turn ended at 15:59:20"},
		"q · two":     {"screen", "the turn ended at 15:59:00"},
		"q · meta":    {"screen", "the turn ended at 15:59:00"},
	}
	for range 3 { // the same answer on every run
		fs, err := Judge(db, ss, w)
		if err != nil {
			t.Fatal(err)
		}
		if len(fs) != len(want) {
			t.Fatalf("got %d findings", len(fs))
		}
		for _, f := range fs {
			if w := want[f.Tab]; f.Right != w[0] || !strings.Contains(f.Why, w[1]) {
				t.Errorf("%s: right %q why %q, want %q with %q", f.Tab, f.Right, f.Why, w[0], w[1])
			}
		}
	}
}

// Review round 2 (10-22): what real transcripts hold. A compaction's summary line carries an older time than where it
// sits; a tool result can be stamped half a second before its call; the logs cut times to the second; parallel tool
// calls get one result line each; an old transcript of the role can be gone.
func TestRealTranscriptQuirks(t *testing.T) {
	db, _, dir := fixture(t)
	transcript(t, db, dir, "s-compact", "s-compact.jsonl", tx{"11:00:00", "a:end_turn:r1"}, tx{"11:50:00", "compact"})
	transcript(t, db, dir, "s-early", "s-early.jsonl", tx{"11:59:59.6", "a:tool_use:r1"}, tx{"11:59:59.2", "u"},
		tx{"12:00:30", "a:end_turn:r2"})
	transcript(t, db, dir, "s-second", "s-second.jsonl", tx{"11:59:00", "a:end_turn:r1"}, tx{"12:00:00.5", "u"},
		tx{"12:00:20", "a:end_turn:r2"})
	transcript(t, db, dir, "s-parallel", "s-parallel.jsonl", tx{"11:59:00", "a:tool_use:r1:t1"},
		tx{"11:59:01", "a:tool_use:r1:t2"}, tx{"11:59:10", "u:t1"}, tx{"12:04:00", "u:t2"}, tx{"12:04:30", "a:end_turn:r2"})
	transcript(t, db, dir, "s-both", "s-both.jsonl", tx{"11:59:00", "a:tool_use:r1:t1"}, tx{"11:59:01", "a:tool_use:r1:t2"},
		tx{"11:59:10", "u:t1"}, tx{"11:59:20", "u:t2"})
	gone := transcript(t, db, dir, "s-gone-old", "s-gone-old.jsonl", tx{"10:00:00", "u"}, tx{"10:05:00", "a:end_turn:r1"})
	transcript(t, db, dir, "s-gone-new", "s-gone-new.jsonl", tx{"11:58:00", "u"}, tx{"11:59:00", "a:end_turn:r2"})
	transcript(t, db, dir, "s-lost-old", "s-lost-old.jsonl", tx{"11:00:00", "a:end_turn:r1"})
	lost := transcript(t, db, dir, "s-lost-new", "s-lost-new.jsonl", tx{"11:30:00", "u"}, tx{"11:59:00", "a:end_turn:r2"})
	// a ! command, its output and a built-in /command start no request; a skill does
	transcript(t, db, dir, "s-local", "s-local.jsonl", tx{"11:00:00", "a:end_turn:r1"},
		tx{"11:59:30", "text:<bash-input>ls</bash-input>"}, tx{"11:59:31", "text:<bash-stdout>x</bash-stdout><bash-stderr></bash-stderr>"},
		tx{"11:59:40", "text:<command-name>/model</command-name>\n<command-message>model</command-message>"},
		tx{"11:59:41", "text:<local-command-stdout>Set model</local-command-stdout>"})
	transcript(t, db, dir, "s-skill", "s-skill.jsonl", tx{"11:00:00", "a:end_turn:r1"},
		tx{"11:59:30", "text:<command-message>review</command-message>\n<command-name>/review</command-name>"})
	// free for a moment inside the second, working at both its ends
	transcript(t, db, dir, "s-mid", "s-mid.jsonl", tx{"11:59:55", "u"}, tx{"12:00:00.2", "a:end_turn:r1"}, tx{"12:00:00.6", "u"},
		tx{"12:00:30", "a:end_turn:r2"})
	// a live transcript's last line can be half written when it is read
	transcript(t, db, dir, "s-half", "s-half.jsonl", tx{"11:00:00", "a:end_turn:r1"}, tx{"12:00:10", "half"})
	for _, p := range []string{gone, lost} {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
	ss := []sessionstate.State{{SessionID: "s-compact", Project: "q", Role: "compact"},
		{SessionID: "s-local", Project: "q", Role: "local"}, {SessionID: "s-skill", Project: "q", Role: "skill"},
		{SessionID: "s-half", Project: "q", Role: "half"}, {SessionID: "s-mid", Project: "q", Role: "mid"},
		{SessionID: "s-early", Project: "q", Role: "early"}, {SessionID: "s-second", Project: "q", Role: "second"},
		{SessionID: "s-parallel", Project: "q", Role: "parallel"}, {SessionID: "s-both", Project: "q", Role: "both"},
		{SessionID: "s-gone-old", Project: "q", Role: "gone"}, {SessionID: "s-gone-new", Project: "q", Role: "gone"},
		{SessionID: "s-lost-old", Project: "q", Role: "lost"}, {SessionID: "s-lost-new", Project: "q", Role: "lost"}}
	w, _ := Parse("wakewatch", strings.NewReader(`2026-10-22T16:00:00+04:00 tab:surface:1 "q · compact" screen=idle state=busy decides=screen
2026-10-22T16:00:00+04:00 tab:surface:2 "q · early" screen=prompt state=busy decides=screen
2026-10-22T16:00:00+04:00 tab:surface:3 "q · second" screen=idle state=busy decides=screen
2026-10-22T16:00:00+04:00 tab:surface:4 "q · parallel" screen=prompt state=busy decides=screen
2026-10-22T16:00:00+04:00 tab:surface:5 "q · both" screen=prompt state=busy decides=screen
2026-10-22T16:00:00+04:00 tab:surface:6 "q · gone" screen=idle state=busy decides=screen
2026-10-22T16:00:00+04:00 tab:surface:7 "q · lost" screen=idle state=busy decides=screen
2026-10-22T16:00:00+04:00 tab:surface:8 "q · local" screen=idle state=busy decides=screen
2026-10-22T16:00:00+04:00 tab:surface:9 "q · skill" screen=idle state=busy decides=screen
2026-10-22T16:00:00+04:00 tab:surface:10 "q · half" screen=idle state=busy decides=screen
2026-10-22T16:00:00+04:00 tab:surface:11 "q · mid" screen=idle state=busy decides=screen
`))
	want := map[string][2]string{
		"q · compact":  {"screen", "the turn ended at 15:00:00"},
		"q · early":    {"state", "a request due after the prompt or tool result at 15:59:59"},
		"q · second":   {"unknown", "a transcript line at 16:00:00, in the log line's own second, changes the answer"},
		"q · local":    {"screen", "the turn ended at 15:00:00"},
		"q · skill":    {"state", "a request due after the prompt or tool result at 15:59:30"},
		"q · half":     {"screen", "the turn ended at 15:00:00"},
		"q · mid":      {"unknown", "a transcript line at 16:00:00, in the log line's own second, changes the answer"},
		"q · parallel": {"unknown", "the transcript cannot tell a running tool from a permission dialog"},
		"q · both":     {"state", "a request due after the prompt or tool result at 15:59:20"},
		"q · gone":     {"screen", "the turn ended at 15:59:00"},
		"q · lost":     {"unknown", "the transcript: open " + lost},
	}
	fs, err := Judge(db, ss, w)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != len(want) {
		t.Fatalf("got %d findings", len(fs))
	}
	for _, f := range fs {
		if w := want[f.Tab]; f.Right != w[0] || !strings.Contains(f.Why, w[1]) {
			t.Errorf("%s: right %q why %q, want %q with %q", f.Tab, f.Right, f.Why, w[0], w[1])
		}
	}
}

func TestTheTextCountsEachTab(t *testing.T) {
	db, ss, _ := fixture(t)
	w, _ := Parse("wakewatch", strings.NewReader(wake))
	fs, err := Judge(db, ss, w)
	if err != nil {
		t.Fatal(err)
	}
	text := Text(fs)
	for _, want := range []string{
		"quotes · p1: 1 disagreements: state right 1",
		"quotes · p6: 1 disagreements: screen right 1",
		"total: 3 disagreements: state right 1, screen right 2",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text has no %q:\n%s", want, text)
		}
	}
}

// Owner 10-23: a tab on a picked Max account is titled "<project> · <role> · <account>"; it is still the role's tab.
func TestATabThatNamesItsAccountIsTheRolesTab(t *testing.T) {
	db, ss, _ := fixture(t)
	w, _ := Parse("wakewatch", strings.NewReader(`2026-10-22T16:23:29+04:00 tab:surface:1000020056 "quotes · p6 · max2" screen=idle state=busy decides=screen
`))
	fs, err := Judge(db, ss, w)
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 1 || fs[0].Right != "screen" {
		t.Errorf("got %+v, want p6's line judged: its turn had ended", fs)
	}
}
