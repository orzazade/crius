package launch

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/sessionstate"
)

// fakeT3 is the t3 CLI: start prints start, and each show prints the next of shows (the last one again once they run
// out). Every call is kept with its stdin.
type fakeT3 struct {
	start string
	send  string
	shows []string
	calls [][]string
	stdin []string
}

func (f *fakeT3) run(stdin string, args ...string) (string, error) {
	f.calls = append(f.calls, args)
	f.stdin = append(f.stdin, stdin)
	switch args[1] {
	case "start":
		return f.start, nil
	case "send":
		return f.send, nil
	case "show":
		s := f.shows[0]
		if len(f.shows) > 1 {
			f.shows = f.shows[1:]
		}
		return s, nil
	}
	return "", fmt.Errorf("unexpected t3 %v", args)
}

func (f *fakeT3) named(sub string) [][]string {
	var out [][]string
	for _, c := range f.calls {
		if c[1] == sub {
			out = append(out, c)
		}
	}
	return out
}

func show(status, instance string, runs int, reply string) string {
	msgs := `[{"role":"user","text":"x"}]`
	if reply != "" {
		msgs = `[{"role":"user","text":"x"},{"role":"assistant","text":"` + reply + `"}]`
	}
	return `{"thread":{"threadId":"mcp:th-1","status":"` + status + `","activeRunId":null,"providerInstanceId":"` + instance +
		`","runCount":` + fmt.Sprint(runs) + `,"pendingRequestCount":0,"archived":false,"updatedAt":"2026-11-19T01:00:00Z"},"messages":` + msgs + `}`
}

// t3Fixture is a launch folder, a picked Max folder (max2's), T3's instances and a roles folder.
func t3Fixture(t *testing.T) (cwd, max2, roles string, d Deps, inst func() (map[string]string, error)) {
	root := t.TempDir()
	cwd, max2, roles = filepath.Join(root, "lab"), filepath.Join(root, ".claude-max2-personal"), filepath.Join(root, "roles")
	for _, p := range []string{cwd, max2} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	d = Deps{
		Place: func(string) (string, string, string, error) { return cwd, "max", "personal", nil },
		Pick:  func(project, want string) (string, string, error) { return "max2", max2, nil },
	}
	inst = func() (map[string]string, error) {
		return map[string]string{"claudeAgent": filepath.Join(root, ".claude"), "claude-max2": max2}, nil
	}
	return cwd, max2, roles, d, inst
}

// A role started as a T3 thread runs on the picked account's provider instance, in the launch folder, named like a tab,
// with the provenance tag line before its first prompt; it is proved to work, and its role record names the thread.
func TestRunT3StartsTheRoleOnThePickedAccountsInstance(t *testing.T) {
	cwd, max2, roles, d, inst := t3Fixture(t)
	f := &fakeT3{
		// an older t3 printed a log line before its JSON
		start: "timestamp=2026-11-19T01:00:00Z level=WARN message=\"old config\"\n" +
			`{"threadId":"mcp:th-1","projectId":"p-1","runId":"run:1"}` + "\n",
		shows: []string{show("starting", "claude-max2", 0, ""), show("running", "claude-max2", 1, "")},
	}
	sess := sessionstate.State{SessionID: "11111111-2222-3333-4444-555555555555", State: "running", Cwd: cwd, Name: "lab · probe",
		Project: "lab", Role: "probe", Thread: "mcp:th-1", Config: max2, Time: time.Now()}
	reads := 0
	tt := T3{CLI: f.run, Instances: inst, Roles: roles,
		Sessions: func() ([]sessionstate.State, error) {
			reads++
			if reads == 1 { // crius-hook has not filed it yet
				return nil, nil
			}
			return []sessionstate.State{{SessionID: "old", Thread: "mcp:other", Config: max2}, sess}, nil
		}}
	(&fakeClock{}).on(&tt, 5*time.Second)
	got, err := RunT3(Spawn{Project: "lab", Role: "probe", Prompt: "say hi and stop"}, d, tt)
	if err != nil {
		t.Fatal(err)
	}
	if got.Thread != "mcp:th-1" || got.Session != sess.SessionID || got.Account != "max2" || got.Instance != "claude-max2" {
		t.Errorf("got %+v", got)
	}
	if got.Tag != "[crius account=max project=lab role=probe]" {
		t.Errorf("tag %q", got.Tag)
	}
	starts := f.named("start")
	if len(starts) != 1 {
		t.Fatalf("t3 thread start ran %d times", len(starts))
	}
	want := []string{"thread", "start", "--json", "--title", "lab · probe", "--provider", "claude-max2", "--model", "claude-opus-5-5", cwd, "-"}
	if strings.Join(starts[0], "|") != strings.Join(want, "|") {
		t.Errorf("start args %q, want %q", starts[0], want)
	}
	if f.stdin[0] != got.Tag+"\nsay hi and stop" {
		t.Errorf("first message %q", f.stdin[0])
	}
	r, err := sessionstate.ReadRole(roles, "lab", "probe")
	if err != nil {
		t.Fatal(err)
	}
	if r.Thread != "mcp:th-1" || r.SessionID != sess.SessionID || r.Filed != max2 || r.Project != "lab" || r.Role != "probe" {
		t.Errorf("role record %+v", r)
	}
}

// The sonnet roles run on Sonnet in T3 too.
func TestRunT3PicksTheRolesModel(t *testing.T) {
	cwd, max2, roles, d, inst := t3Fixture(t)
	f := &fakeT3{start: `{"threadId":"mcp:th-1"}`, shows: []string{show("completed", "claude-max2", 1, "hi ✅")}}
	sess := sessionstate.State{SessionID: "11111111-2222-3333-4444-555555555555", Cwd: cwd, Project: "lab", Role: "qa-api", Thread: "mcp:th-1", Config: max2}
	tt := T3{CLI: f.run, Instances: inst, Roles: roles,
		Sessions: func() ([]sessionstate.State, error) { return []sessionstate.State{sess}, nil }}
	(&fakeClock{}).on(&tt, 3*time.Second)
	if _, err := RunT3(Spawn{Project: "lab", Role: "qa-api", Prompt: "say hi and stop"}, d, tt); err != nil {
		t.Fatal(err)
	}
	if a := f.named("start")[0]; !strings.Contains(strings.Join(a, " "), "--model claude-sonnet-5-5") {
		t.Errorf("start args %q", a)
	}
}

// Each way a T3 start can go wrong fails loudly, naming what it saw.
func TestRunT3FailsLoudly(t *testing.T) {
	cwd, max2, _, d, inst := t3Fixture(t)
	good := sessionstate.State{SessionID: "11111111-2222-3333-4444-555555555555", Cwd: cwd, Project: "lab", Role: "probe", Thread: "mcp:th-1", Config: max2}
	for _, c := range []struct {
		name  string
		start string
		shows []string
		inst  func() (map[string]string, error)
		sess  []sessionstate.State
		pick  func(project, want string) (string, string, error)
		err   string
	}{
		{"start prints no JSON", "level=WARN message=boom\nsomething else\n", nil, inst, nil, nil, "t3 thread start printed no JSON"},
		{"start JSON has no thread", `{"projectId":"p-1"}`, nil, inst, nil, nil, "t3 thread start printed no threadId"},
		{"no instance runs in the picked folder", "", nil,
			func() (map[string]string, error) { return map[string]string{"claudeAgent": "/nowhere/.claude"}, nil }, nil, nil,
			"no T3 provider instance runs in " + max2 + " (account max2)"},
		{"no run starts", `{"threadId":"mcp:th-1"}`, []string{show("idle", "claude-max2", 0, "")}, inst, []sessionstate.State{good}, nil,
			"T3 thread mcp:th-1 did not finish starting in 3s (limit 3s): still waiting for a run or a reply (status idle). Next: it may still start"},
		{"T3 runs it on another instance", `{"threadId":"mcp:th-1"}`, []string{show("running", "claudeAgent", 1, "")}, inst, nil, nil,
			"thread mcp:th-1 runs on provider instance claudeAgent, not claude-max2. Next: it may still start"},
		{"crius-hook files no session", `{"threadId":"mcp:th-1"}`, []string{show("running", "claude-max2", 1, "")}, inst, nil, nil,
			"lab · probe (thread mcp:th-1, running): T3 thread mcp:th-1 did not finish starting in 3s (limit 3s): still waiting for crius-hook to file its Claude session (the thread runs). Next: without it the role record cannot be written"},
		{"the session runs in another folder", `{"threadId":"mcp:th-1"}`, []string{show("running", "claude-max2", 1, "")}, inst,
			[]sessionstate.State{{SessionID: good.SessionID, Thread: "mcp:th-1", Config: "/x/.claude"}}, nil,
			"its Claude session " + good.SessionID + " runs in /x/.claude, not " + max2},
		{"no Max folder picked", "", nil, inst, nil, func(string, string) (string, string, error) { return "", "", nil },
			"no signed-in folder for lab on account max: not launched"},
		{"no accounts to pick from", "", nil, inst, nil, func(string, string) (string, string, error) { panic("nil Pick") },
			"no signed-in Max folder for lab on account max: a T3 thread needs one"},
	} {
		f := &fakeT3{start: c.start, shows: c.shows}
		dd := d
		if c.name == "no accounts to pick from" {
			dd.Pick = nil
		} else if c.pick != nil {
			dd.Pick = c.pick
		}
		tt := T3{CLI: f.run, Instances: c.inst, Roles: t.TempDir(),
			Sessions: func() ([]sessionstate.State, error) { return c.sess, nil }}
		(&fakeClock{}).on(&tt, 3*time.Second)
		_, err := RunT3(Spawn{Project: "lab", Role: "probe", Prompt: "say hi and stop"}, dd, tt)
		if err == nil || !strings.Contains(err.Error(), c.err) {
			t.Errorf("%s: err %v, want %q", c.name, err, c.err)
		}
	}
}

// fakeClock is a start-up check's clock: each pause moves it on, and so may a slow t3 call, so no test waits for real.
// said is what the check wrote on its Log.
type fakeClock struct {
	now  time.Time
	said strings.Builder
}

// on gives tt this clock, limit as its start-up limit and reads 1 s apart.
func (c *fakeClock) on(tt *T3, limit time.Duration) {
	tt.Wait, tt.Limit, tt.Log = time.Second, limit, &c.said
	tt.Now = func() time.Time { return c.now }
	tt.Sleep = func(d time.Duration) { c.now = c.now.Add(d) }
}

var waitLine = regexp.MustCompile(`^waiting on T3 thread mcp:th-1: (.+), (\d+)s of 3m0s$`)

// 11-21: the start-up check ran two waits of 120 reads each (2 x 2 min, longer when t3 answered slowly) with no output,
// so nobody could tell a slow T3 from a hang. Now one limit in real time bounds both waits, slow t3 calls included; it
// says what it waits on when that changes and at least once a minute, and the limit's error names the thread, what it
// still waited for and the next step.
func TestRunT3SaysWhatItWaitsOnAndStopsAtOneLimit(t *testing.T) {
	cwd, max2, roles, d, inst := t3Fixture(t)
	c := &fakeClock{now: time.Date(2026, 11, 22, 1, 0, 0, 0, time.UTC)}
	start := c.now
	f := &fakeT3{start: `{"threadId":"mcp:th-1"}`, shows: []string{show("starting", "claude-max2", 0, ""), show("starting", "claude-max2", 0, ""),
		show("starting", "claude-max2", 0, ""), show("running", "claude-max2", 1, "")}}
	slow := func(stdin string, args ...string) (string, error) { // T3 under load: each call takes 20 s
		c.now = c.now.Add(20 * time.Second)
		return f.run(stdin, args...)
	}
	tt := T3{CLI: slow, Instances: inst, Roles: roles,
		Sessions: func() ([]sessionstate.State, error) { // crius-hook never files the session
			return []sessionstate.State{{SessionID: "old", Thread: "mcp:other", Config: max2, Cwd: cwd}}, nil
		}}
	c.on(&tt, StartLimit)
	_, err := RunT3(Spawn{Project: "lab", Role: "probe", Prompt: "say hi and stop"}, d, tt)
	lines := strings.Split(strings.TrimSpace(c.said.String()), "\n")
	if c.said.Len() == 0 {
		t.Fatalf("no progress line on Log while the start-up check waited %s; err %v", c.now.Sub(start), err)
	}
	if took := c.now.Sub(start); took < StartLimit || took > StartLimit+time.Second {
		t.Errorf("the start-up check took %s, want one limit of %s for both waits, slow t3 calls included", took, StartLimit)
	}
	want := "T3 thread mcp:th-1 did not finish starting in 3m0s (limit 3m0s): still waiting for crius-hook to file its Claude session"
	if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "Next: ") {
		t.Errorf("err %v, want %q and a next step", err, want)
	}
	var whys []string
	last := -1
	for _, l := range lines {
		m := waitLine.FindStringSubmatch(l)
		if m == nil {
			t.Fatalf("line %q, want %s", l, waitLine)
		}
		n, _ := strconv.Atoi(m[2])
		if last >= 0 && n-last > 60+21 { // a line is due after a minute, at the next read: 20 s call + 1 s pause
			t.Errorf("%ds between two lines (%q), want one at least once a minute (at the next read)", n-last, l)
		}
		last = n
		if len(whys) == 0 || whys[len(whys)-1] != m[1] {
			whys = append(whys, m[1])
		}
	}
	if wantWhys := []string{"a run or a reply (status starting)", "crius-hook to file its Claude session (the thread runs)"}; strings.Join(whys, "|") != strings.Join(wantWhys, "|") {
		t.Errorf("waited on %q, want a line each time the reason changed: %q", whys, wantWhys)
	}
	if last < int((StartLimit - time.Minute).Seconds()) {
		t.Errorf("last line at %ds of a %s wait, want one within a minute of the limit", last, StartLimit)
	}
}

// -resume of a T3 role sends ResumedPrompt into its own thread and proves a new run started; a thread that still runs
// a turn, waits on a request or is archived is left alone.
func TestResumeT3SendsTheResumePromptToItsThread(t *testing.T) {
	f := &fakeT3{send: `{"threadId":"mcp:th-1","runId":"run:4"}`,
		shows: []string{show("waiting", "claude-max2", 3, "done ✅"), show("waiting", "claude-max2", 3, "done ✅"), show("running", "claude-max2", 4, "")}}
	if err := ResumeT3("mcp:th-1", T3{CLI: f.run, Tries: 5}); err != nil {
		t.Fatal(err)
	}
	sends := f.named("send")
	if len(sends) != 1 || strings.Join(sends[0], " ") != "thread send --json mcp:th-1 -" {
		t.Fatalf("sends %q", sends)
	}
	for i, c := range f.calls {
		if c[1] == "send" && f.stdin[i] != ResumedPrompt {
			t.Errorf("sent %q", f.stdin[i])
		}
	}
	for _, c := range []struct{ name, show, err string }{
		{"running", show("running", "claude-max2", 3, ""), "thread mcp:th-1 still runs a turn: nothing resumed"},
		{"archived", strings.Replace(show("waiting", "claude-max2", 3, "x"), `"archived":false`, `"archived":true`, 1), "thread mcp:th-1 is archived in T3: nothing resumed"},
		{"request", strings.Replace(show("waiting", "claude-max2", 3, "x"), `"pendingRequestCount":0`, `"pendingRequestCount":2`, 1), "thread mcp:th-1 waits on 2 open request(s)"},
	} {
		f := &fakeT3{shows: []string{c.show}}
		err := ResumeT3("mcp:th-1", T3{CLI: f.run, Tries: 2})
		if err == nil || !strings.Contains(err.Error(), c.err) {
			t.Errorf("%s: err %v, want %q", c.name, err, c.err)
		}
		if len(f.named("send")) != 0 {
			t.Errorf("%s: sent anyway", c.name)
		}
	}
	// a send that starts no run is loud
	f = &fakeT3{send: `{"threadId":"mcp:th-1"}`, shows: []string{show("waiting", "claude-max2", 3, "x")}}
	if err := ResumeT3("mcp:th-1", T3{CLI: f.run, Tries: 2}); err == nil || !strings.Contains(err.Error(), "no new run in thread mcp:th-1 after 2 reads") {
		t.Errorf("no run: err %v", err)
	}
}

// T3's provider instances come from its settings.json: only Claude ones, each with the config folder it runs in.
func TestT3InstancesReadsTheClaudeInstances(t *testing.T) {
	home, _ := os.UserHomeDir()
	p := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(p, []byte(`{"providerInstances":{
		"claudeAgent":{"driver":"claudeAgent","config":{"homePath":"~/.claude"}},
		"claude-max2":{"driver":"claudeAgent","config":{"homePath":"/abs/.claude-max2-personal"}},
		"codex":{"driver":"codex","config":{"homePath":"~/.codex"}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := T3Instances(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["claudeAgent"] != filepath.Join(home, ".claude") || got["claude-max2"] != "/abs/.claude-max2-personal" {
		t.Errorf("instances %v", got)
	}
	if _, err := T3Instances(filepath.Join(t.TempDir(), "none.json")); err == nil {
		t.Error("a missing settings.json gave no error")
	}
}
