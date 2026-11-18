package testutil

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// FakeT3 writes a t3 that answers as T3 Code's CLI does, from files in dir: a send prints dir/send.out and exits with
// dir/send.rc (0 without it); show number N prints dir/show.N, else dir/show, and exits 1 with neither. Each call's
// arguments go to dir/calls, a send's stdin to dir/stdin. dir/hang makes every call sleep 5 s.
func FakeT3(t *testing.T) (bin, dir string) {
	t.Helper()
	dir = t.TempDir()
	bin = filepath.Join(dir, "t3")
	body := "#!/bin/sh\nd=" + dir + "\n" +
		"echo \"$*\" >> \"$d/calls\"\n" +
		"[ -f \"$d/hang\" ] && sleep 5\n" +
		"if [ \"$2\" = send ]; then cat >> \"$d/stdin\"; cat \"$d/send.out\" 2>/dev/null; exit $(cat \"$d/send.rc\" 2>/dev/null || echo 0); fi\n" +
		"n=$(( $(cat \"$d/shows\" 2>/dev/null || echo 0) + 1 )); echo $n > \"$d/shows\"\n" +
		"if [ -f \"$d/show.$n\" ]; then cat \"$d/show.$n\"; exit 0; fi\n" +
		"if [ -f \"$d/show\" ]; then cat \"$d/show\"; exit 0; fi\n" +
		"echo 'Thread not found.' >&2; exit 1\n"
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, dir
}

// Put writes body to dir/name, making the folders on the way.
func Put(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// T3Run is a run id of thread mcp:<thread> as T3 prints it: runs of a thread count up.
func T3Run(thread string, n int) string {
	return "run:thread:mcp%3A" + thread + ":ordinal:" + strconv.Itoa(n)
}

// T3Sent is what `t3 thread send --json` printed on 11-19 (fields cut to the ones read), for run n.
func T3Sent(thread string, n int) string {
	return `{"threadId":"mcp:` + thread + `","runId":"` + T3Run(thread, n) + `","status":"queued","delivery":"queued"}`
}

// T3Shown is what `t3 thread show --json` prints, cut to the fields read: active and latest are run numbers (0 for
// null), and msgs come in threes: role, run number, text.
func T3Shown(thread string, active, latest int, msgs ...string) string {
	id := func(n int) string {
		if n == 0 {
			return "null"
		}
		return strconv.Quote(T3Run(thread, n))
	}
	m := ""
	for i := 0; i+2 < len(msgs); i += 3 {
		n, _ := strconv.Atoi(msgs[i+1])
		if m != "" {
			m += ","
		}
		m += `{"role":"` + msgs[i] + `","runId":"` + T3Run(thread, n) + `","text":` + strconv.Quote(msgs[i+2]) + `}`
	}
	return `{"thread":{"threadId":"mcp:` + thread + `","status":"running","activeRunId":` + id(active) + `,"latestRunId":` +
		id(latest) + `,"pendingRequestCount":0,"updatedAt":"2026-11-19T08:00:00.000Z"},"messages":[` + m + `]}`
}
