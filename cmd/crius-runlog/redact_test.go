package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/orzazade/crius/internal/runlog"
)

// Fake secrets in real shapes. Each is built from parts: the repo is public and scanners read the source.
var (
	ghToken  = "gh" + "o_" + strings.Repeat("aB3dE5fG7h", 4)
	ghFine   = "github" + "_pat_" + strings.Repeat("11ABCDE0x9", 6)
	jwt      = "ey" + "JhbGciOiJSUzI1NiJ9" + "." + "ey" + "JzdWIiOiIxMjM0NTYifQ" + "." + "c2lnbmF0dXJlLXZhbHVl"
	awsID    = "AK" + "IA" + "Q3EGR5TMZK7WBX2N"
	slack    = "xo" + "xb-" + "123456789012-abcdefGHIJKL"
	sas      = "Zm9vYmFy" + "c2lnbmF0dXJl" + "QUJD%3D"
	b64      = "OmZha2VwYXR2YWx1ZWZha2VwYXQ="
	password = "Tr0ub4dor-horse"
)

// everything is all text under home plus what the command printed: no sink of crius-runlog may hold a secret.
func everything(t *testing.T, home, out, errs string) string {
	t.Helper()
	all := out + "\n" + errs
	err := filepath.WalkDir(home, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		all += "\n" + p
		if !d.IsDir() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			all += "\n" + string(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return all
}

func oneLog(t *testing.T, home string) string {
	t.Helper()
	m, _ := filepath.Glob(filepath.Join(home, "runs", "*.md"))
	if len(m) != 1 {
		t.Fatalf("logs %v", m)
	}
	b, err := os.ReadFile(m[0])
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// 2026-11-23 00:12: the text of `sh -c "… GH_TOKEN=<real token> gh run view …"` went into ~/.crius/runs.
func TestASecretInTheCommandNeverReachesTheLog(t *testing.T) {
	for _, c := range []struct {
		name, secret string
		argv         []string
		keep         []string // what the cmd: line still shows
	}{
		{"env in sh -c", ghToken, []string{"sh", "-c", "cd / && GH_TOKEN=" + ghToken + " true run view 71 --json x"},
			[]string{"GH_TOKEN=<redacted> true run view 71 --json x"}},
		{"quoted env", password, []string{"sh", "-c", `DB_PASSWORD="` + password + ` two" true end`},
			[]string{"DB_PASSWORD=<redacted> true end"}},
		{"plain env word", password, []string{"env", "MY_SECRET=" + password + " and more", "true"}, []string{"MY_SECRET=<redacted> true"}},
		{"and more", "and more", []string{"env", "MY_SECRET=" + password + " and more", "true"}, nil},
		{"passwd", password, []string{"env", "X_PASSWD=" + password, "true"}, []string{"X_PASSWD=<redacted>"}},
		{"api key", password, []string{"env", "OPENAI_API_KEY=" + password, "true"}, []string{"OPENAI_API_KEY=<redacted>"}},
		{"account key", password, []string{"env", "AZURE_STORAGE_ACCOUNT_KEY=" + password, "true"}, nil},
		{"pat", password, []string{"env", "AZURE_DEVOPS_EXT_PAT=" + password, "true"}, nil},
		{"flag and word", password, []string{"true", "--token", password, "view"}, []string{"--token <redacted> view"}},
		{"flag and word of two", "horse battery", []string{"true", "--password", "horse battery", "view"}, []string{"--password <redacted> view"}},
		{"flag value opening with a dash", "-" + password, []string{"true", "--token", "-" + password}, nil},
		{"flag=", password, []string{"true", "--password=" + password, "view"}, []string{"--password=<redacted> view"}},
		{"flag in sh -c", password, []string{"sh", "-c", "true --api-key " + password + " view"}, []string{"--api-key <redacted> view"}},
		{"bearer header", password, []string{"true", "-H", "Authorization: Bearer " + password, "https://h/x"}, []string{"https://h/x"}},
		{"basic header in sh -c", b64, []string{"sh", "-c", `true -H "Authorization: Basic ` + b64 + `" https://h/x`}, []string{"https://h/x"}},
		{"git extraheader", b64, []string{"true", "-c", "http.extraheader=AUTHORIZATION: basic " + b64, "fetch"}, []string{"fetch"}},
		{"git extraheader quoted", b64, []string{"sh", "-c", `true -c http.extraheader="Authorization: Basic ` + b64 + `" fetch`}, []string{"fetch"}},
		{"key header", password, []string{"true", "-H", "X-Api-Key: " + password}, nil},
		{"apim header", password, []string{"true", "-H", "Ocp-Apim-Subscription-Key: " + password}, nil},
		{"curl user", password, []string{"true", "-u", "bob:" + password, "https://h/x"}, []string{"https://h/x"}},
		{"curl no user", password, []string{"sh", "-c", "true -u :" + password + " https://h/x"}, []string{"https://h/x"}},
		{"url", password, []string{"true", "https://bob:" + password + "@host/x"}, []string{"host/x"}},
		{"url query", password, []string{"true", "https://h/x?access_token=" + password + "&page=2"}, []string{"page=2"}},
		{"sas", sas, []string{"true", "https://a.blob/x?sv=2024&sig=" + sas + "&se=2026"}, []string{"se=2026"}},
		{"connection string", password, []string{"true", "Server=h;User Id=u;Password=" + password + " x;Timeout=3"}, nil},
		{"account key string", password, []string{"true", "AccountName=n;AccountKey=" + password + "==;EndpointSuffix=s"}, nil},
		{"json", password, []string{"true", "-d", `{"password":"` + password + `","user":"u"}`}, nil},
		{"github", ghToken, []string{"true", ghToken}, nil},
		{"github fine", ghFine, []string{"true", "x" + ghFine}, nil},
		{"jwt", jwt, []string{"true", jwt}, nil},
		{"aws", awsID, []string{"true", awsID}, nil},
		{"slack", slack, []string{"true", slack}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("CRIUS_HOME", home)
			rc, out, errs := runlogCmd(t, append([]string{"run", "cortex", "sec", "1e4d6df", "--"}, c.argv...)...)
			if rc != 0 {
				t.Fatalf("rc %d", rc)
			}
			log := oneLog(t, home)
			if strings.Contains(everything(t, home, out, errs), c.secret) {
				t.Errorf("secret kept in the command text")
			}
			if !strings.Contains(log, "<redacted>") {
				t.Errorf("no <redacted> in the log")
			}
			for _, k := range c.keep {
				if !strings.Contains(log, k) {
					t.Errorf("log lost %q: %s", k, log)
				}
			}
		})
	}
}

// The child gets its real argv and its exit decides pass or fail, as before.
func TestTheChildGetsItsRealArgv(t *testing.T) {
	for exit, want := range map[string]string{"0": "pass: 1\n", "7": "fail: 1\n"} {
		home := t.TempDir()
		t.Setenv("CRIUS_HOME", home)
		saw := filepath.Join(t.TempDir(), "saw")
		rc, out, errs := runlogCmd(t, "run", "cortex", "argv", "1e4d6df", "--",
			"sh", "-c", `GH_TOKEN=`+ghToken+` sh -c 'printf %s "$GH_TOKEN $1" > "$0"' "$1" "$2"; exit `+exit, "sh", saw, "--token="+password)
		b, _ := os.ReadFile(saw)
		if string(b) != ghToken+" --token="+password {
			t.Fatalf("the child saw other arguments than it was given (%d bytes)", len(b))
		}
		if (rc == 0) != (exit == "0") || !strings.Contains(oneLog(t, home), want+"cmd: ") || !strings.Contains(oneLog(t, home), "\nexit: "+exit+"\n") {
			t.Fatalf("exit %s: rc %d", exit, rc)
		}
		if all := everything(t, home, out, errs); strings.Contains(all, ghToken) || strings.Contains(all, password) {
			t.Fatal("secret kept in the command text")
		}
	}
}

func TestASecretInTheOutputNeverReachesTheLogOrTheEcho(t *testing.T) {
	for _, c := range []struct{ name, secret, text string }{
		{"env", password, "+ GH_TOKEN=" + password + " gh run view\n"},
		{"json", password, `{"id":1,"client_secret":"` + password + `","n":2}` + "\n"},
		{"escaped json", password, `{"b":"{\"password\":\"` + password + `\"}"}` + "\n"},
		{"token after an escaped newline", ghToken, `{"log":"a\n` + ghToken + `\nb"}` + "\n"},
		{"bearer", password, "> Authorization: Bearer " + password + "\n"},
		{"basic", b64, "> authorization: basic " + b64 + "\r\n"},
		{"cookie", password, "< Set-Cookie: sid=" + password + "; Path=/\n"},
		{"yaml below", password, "db:\n  password:\n    " + password + "\nnext: 1\n"},
		{"yaml block", password, "db:\n  password: |\n    first\n    " + password + "\nnext: 1\n"},
		{"private key", "MIIEpAIBAAKCAQEA", "-----BEGIN " + "RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA\nMIIEpAIBAAKCAQEA\n-----END " + "RSA PRIVATE KEY-----\n"},
		{"connection string", password, "Using Server=h;Uid=u;Pwd=" + password + ";Timeout=3\n"},
		{"account key", password, "DefaultEndpointsProtocol=https;AccountName=n;AccountKey=" + password + ";EndpointSuffix=x\n"},
		{"url", password, "origin\thttps://bob:" + password + "@host/x (fetch)\n"},
		{"token as url user", strings.Repeat("q7x2", 13), "origin https://" + strings.Repeat("q7x2", 13) + "@dev.azure.com/o\n"},
		{"sas", sas, "GET https://a.blob/x?sv=2024&sig=" + sas + " 200\n"},
		{"bare sas", sas, "sv=2024 sig=" + sas + "\n"},
		{"mysql", password, "+ mysql -u root -p" + password + " db\n"},
		{"docker login", password, "+ docker login -u bob -p " + password + " host\n"},
		{"redis", password, "redis-cli -a " + password + " ping\n"},
		{"github", ghToken, ghToken + "\n"},
		{"github fine", ghFine, "x " + ghFine + "\n"},
		{"jwt", jwt, "got " + jwt + "\n"},
		{"aws", awsID, "id " + awsID + "\n"},
		{"slack", slack, "hook " + slack + "\n"},
		{"no last newline", password, "done\nPASSWORD=" + password},
		{"stderr", password, "PASSWORD=" + password + "\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("CRIUS_HOME", home)
			file := filepath.Join(t.TempDir(), "out.txt")
			os.WriteFile(file, []byte(c.text), 0o600)
			argv := []string{"cat", file}
			if c.name == "stderr" {
				argv = []string{"sh", "-c", `cat "$0" >&2`, file}
			}
			rc, out, errs := runlogCmd(t, append([]string{"run", "cortex", "sec", "1e4d6df", "--"}, argv...)...)
			if rc != 0 {
				t.Fatalf("rc %d", rc)
			}
			if strings.Contains(everything(t, home, out, errs), c.secret) {
				t.Errorf("secret kept in the output text")
			}
			if !strings.Contains(oneLog(t, home), "<redacted>") || !strings.Contains(errs, "<redacted>") {
				t.Errorf("no <redacted> in the log or the echo")
			}
			if strings.Contains(c.text, "next: 1") && !strings.Contains(oneLog(t, home), "| next: 1\n") {
				t.Errorf("the line after the secret is gone: %s", oneLog(t, home))
			}
		})
	}
}

// A secret half in one read and half in the next, or across the place where the log cuts a line or drops the middle of
// the output, is caught: the output is redacted before it is cut. A part of it (8 bytes) must not show either.
func TestASecretAcrossACutIsCaught(t *testing.T) {
	dir := t.TempDir()
	file := func(name, text string) string {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(text), 0o600)
		return p
	}
	line := strings.Repeat("x", 99) + "\n"
	for _, c := range []struct {
		name string
		argv []string
	}{
		{"two reads", []string{"sh", "-c", `cat "$0"; sleep 0.2; cat "$1"`, file("a", "got "+ghToken[:20]), file("b", ghToken[20:]+" end\n")}},
		{"line cut", []string{"cat", file("c", strings.Repeat("x", 1980)+" "+ghToken+" end\n")}},
		{"name before the line cut", []string{"cat", file("d", strings.Repeat("x", 1980)+" x_password=hunter2"+ghToken+" end\n")}},
		{"head buffer", []string{"cat", file("e", strings.Repeat(line, 655)+strings.Repeat("x", 15)+" "+ghToken+" end\n"+strings.Repeat(line, 9000))}},
		{"tail buffer", []string{"cat", file("f", strings.Repeat(line, 2000)+ghToken+"\n"+strings.Repeat("x", 1<<18-25)+"\n")}},
		{"one line over every buffer", []string{"cat", file("g", strings.Repeat("x", 1<<20-20)+" "+ghToken+" "+strings.Repeat("y", 1<<18-30))}},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("CRIUS_HOME", home)
			rc, out, errs := runlogCmd(t, append([]string{"run", "cortex", "cut", "1e4d6df", "--"}, c.argv...)...)
			if rc != 0 {
				t.Fatalf("rc %d", rc)
			}
			all := everything(t, home, out, errs)
			for i := 4; i+8 <= len(ghToken); i++ {
				if strings.Contains(all, ghToken[i:i+8]) {
					t.Fatalf("part of the secret kept (from byte %d)", i)
				}
			}
			if !strings.Contains(all, "<redacted>") {
				t.Fatal("no <redacted>")
			}
		})
	}
}

// A log with nothing secret in it is the same to the byte, in the file and in the echo.
func TestANormalLogIsUnchanged(t *testing.T) {
	sha := "25969cd0f1e2d3c4b5a697887766554433221100"
	text := strings.Join([]string{
		"commit " + sha,
		"headSha: " + sha + " run 19283746501",
		`{"databaseId":19283746501,"headSha":"` + sha + `","author":"bob","conclusion":"success"}`,
		"| token tests: 12 passed, 0 failed",
		"Passed!  - Failed:     0, Passed:  7397, Skipped:     3, Total:  7400, Duration: 3 m 12 s",
		"ok  \tgithub.com/orzazade/crius/internal/redact\t2.013s",
		"--- PASS: TestTokenRefresh (0.00s)",
		"--- PASS: TestRepoHoldsNoRawToken (0.21s)",
		"internal/auth/token.go:12:5: undefined: foo",
		"src/secret_test.go:45: want 3, got 4",
		"tests/test_token.py::test_refresh PASSED",
		"   at App.Auth.TokenService.Get() in /src/Auth/Token.cs:line 45",
		"Microsoft.IdentityModel.Tokens, Version=8.0.0.0, Culture=neutral, PublicKeyToken=31bf3856ad364e35",
		"GH_TOKEN: ***",
		"git -c http.extraheader=\"AUTHORIZATION: basic ***\" fetch",
		"go test -count=1 -p 2 ./internal/login -update -u",
		"docker compose -p crius-test exec -u root db ls",
		"mkdir -p /tmp/a && cp -p x y",
		"docker run -u 1000:1000 --user 501:20 img",
		"input_tokens=1234 max_tokens: 4096 output_tokens=88",
		"https://github.com/orzazade/crius/commit/" + sha,
		"https://Appxite@dev.azure.com/Appxite/p/_git/r http://localhost:8080/a@b",
		"git@github.com:orzazade/crius.git ssh://git@host:22/x",
		"sonar, " + sha + " sonar: " + sha,
		"sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
		"PWD=/Users/x/Projects OLDPWD=/tmp PATH=/usr/bin:/bin",
		"run log ~/.crius/runs/cortex-O9.F4-2026-11-22-235959.md",
		"ünïcode … \xff\xfe bytes\r",
		"", "\tpass: 12", "last line, no newline",
	}, "\n")
	home := t.TempDir()
	t.Setenv("CRIUS_HOME", home)
	file := filepath.Join(t.TempDir(), "out.txt")
	os.WriteFile(file, []byte(text), 0o600)
	rc, _, errs := runlogCmd(t, "run", "cortex", "plain", sha, "--", "cat", file)
	if rc != 0 || errs != text {
		t.Fatalf("rc %d, echo is\n%q\nwant\n%q", rc, errs, text)
	}
	log := oneLog(t, home)
	head, body, _ := strings.Cut(log, "output:\n")
	if want := "| " + strings.ReplaceAll(text, "\n", "\n| ") + "\n"; body != want {
		t.Fatalf("output is\n%q\nwant\n%q", body, want)
	}
	if want := "sha: " + sha + "\npass: 1\ncmd: cat " + file + "\nexit: 0\nran: "; !strings.HasPrefix(head, want) {
		t.Fatalf("header %q", head)
	}

	// A command with nothing secret in it is the same too.
	for _, argv := range [][]string{
		{"true", "run", "view", "19283746501", "--json", "headSha,conclusion"},
		{"sh", "-c", `true -C ~/x rev-parse ` + sha + ` && true test -p 2 -run 'TestToken|TestSecret' ./internal/redact; mkdir -p "$TMPDIR"`},
		{"true", "run", "--name", "token tests: 12"},
	} {
		home := t.TempDir()
		t.Setenv("CRIUS_HOME", home)
		runlogCmd(t, append([]string{"run", "cortex", "plain", sha, "--"}, argv...)...)
		if log := oneLog(t, home); !strings.Contains(log, "\ncmd: "+strings.Join(argv, " ")+"\n") {
			t.Fatalf("cmd of %q: %s", argv, log)
		}
	}
}

// The hand form: cmd, notes and refusals. The log still reads back as sha and pass.
func TestTheHandFormKeepsNoSecret(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CRIUS_HOME", home)
	rc, out, errs := runlogCmd(t, "hand", "ca96b1d6", "12", "GH_TOKEN="+ghToken+" gh run view 71", "saw Password="+password+";x", "plain note")
	if rc != 0 {
		t.Fatalf("rc %d", rc)
	}
	if all := everything(t, home, out, errs); strings.Contains(all, ghToken) || strings.Contains(all, password) {
		t.Fatal("secret kept in the hand form")
	}
	if want := "sha: ca96b1d6\npass: 12\ncmd: GH_TOKEN=<redacted> gh run view 71\n"; !strings.HasPrefix(oneLog(t, home), want) ||
		!strings.Contains(oneLog(t, home), "\nplain note\n") {
		t.Fatalf("log %s", oneLog(t, home))
	}
	if sha, err := runlog.Proven("x", filepath.Join(home, "runs", "hand.md")); err != nil || sha != "ca96b1d6" {
		t.Fatal(sha, err)
	}
	// Refused values and a command that does not start are said without the secret; a secret is never a file name.
	for _, args := range [][]string{
		{"hand2", ghToken, "12", "x"},
		{"hand2", "ca96b1d6", ghToken, "x"},
		{ghToken, "ca96b1d6", "12", "x"},
		{"bad/" + ghToken, "ca96b1d6", "12", "x"},
		{"hand2", "ca96b1d6", "12", "x", "pass: 12", "y " + ghToken},
		{"run", "cortex", ghToken, "1e4d6df", "--", "true"},
		{"run", ghToken, "x", "1e4d6df", "--", "true"},
		{"run", "cortex", "x", "1e4d6df", "--", "/no/such/" + ghToken},
		{"run", "-limit", ghToken, "cortex", "x", "1e4d6df", "--", "true"},
	} {
		rc, out, errs := runlogCmd(t, args...)
		if rc != 2 {
			t.Errorf("rc %d of %d args, want 2", rc, len(args))
		}
		if strings.Contains(everything(t, home, out, errs), ghToken) {
			t.Error("secret kept by a refusal")
		}
	}
}

// The second review of 11-23: sshpass -p X and the like, with the flag right behind its client, in each place it can stand.
func TestAPasswordFlagRightBehindItsClientReachesNothing(t *testing.T) {
	for _, argv := range [][]string{
		{"sh", "-c", "echo mysql -p" + password + " -uroot; echo sshpass -p " + password + " ssh"},
		{"sh", "-c", "echo docker login -p " + password + " -u a >&2"},
		{"sshpass -p " + password + " ssh", "x"}, // does not start: the error names it
	} {
		home := t.TempDir()
		t.Setenv("CRIUS_HOME", home)
		_, out, errs := runlogCmd(t, append([]string{"run", "cortex", "sec", "1e4d6df", "--"}, argv...)...)
		if all := everything(t, home, out, errs); strings.Contains(all, password) {
			t.Errorf("%q: secret kept", argv)
		}
	}
	home := t.TempDir()
	t.Setenv("CRIUS_HOME", home)
	rc, out, errs := runlogCmd(t, "n-1", "1e4d6df", "1", "az login -p "+password, "sshpass -p"+password, "mongosh -p "+password)
	if all := everything(t, home, out, errs); rc != 0 || strings.Contains(all, password) {
		t.Errorf("hand form: rc %d, secret kept %v", rc, strings.Contains(all, password))
	}
}

// The second review of 11-23: the bytes a line over 1 MiB lost were in no count. The note counts all the log does not show.
func TestTheBytesCutNoteCountsAnOverLongLine(t *testing.T) {
	for _, c := range []struct {
		script string
		total  int
	}{
		{"head -c 3000000 /dev/zero | tr '\\0' x", 3000000},
		{"head -c 3000000 /dev/zero | tr '\\0' x; echo; echo LAST", 3000006},
		{"head -c 3000000 /dev/zero | tr '\\0' x; echo; yes 0123456789 | head -c 520000", 3520001},
		{"head -c 3000000 /dev/zero | tr '\\0' x; echo; head -c 100 /dev/zero | tr '\\0' y; echo; head -c 270000 /dev/zero | tr '\\0' z", 3270102},
	} {
		home := t.TempDir()
		t.Setenv("CRIUS_HOME", home)
		if rc, _, errs := runlogCmd(t, "run", "cortex", "long", "1e4d6df", "--", "sh", "-c", c.script); rc != 0 {
			t.Fatalf("rc %d: %.300s", rc, errs)
		}
		log := oneLog(t, home)
		_, output, _ := strings.Cut(log, "\noutput:\n")
		cut, shown := 0, 0
		for _, l := range strings.SplitAfter(output, "\n") {
			if m := regexp.MustCompile(`^… (\d+) bytes cut …\n$`).FindStringSubmatch(l); m != nil {
				cut, _ = strconv.Atoi(m[1])
				continue
			}
			l, ok := strings.CutPrefix(l, "| ")
			if !ok {
				continue
			}
			if s, long := strings.CutSuffix(l, " …\n"); long {
				l = s + "\n" // quote cut it
			}
			l = regexp.MustCompile(`<redacted> \(\d+ bytes of a line over 1048576 bytes left out\)`).ReplaceAllString(l, "")
			shown += len(l)
		}
		// A shown line's newline may be the log's own (a cut line's, or a part of a line): at most one per line shown.
		if n := cut + shown; n < c.total || n > c.total+2*strings.Count(output, " …\n")+2 {
			t.Errorf("%q: cut %d + shown %d is not the %d bytes the check wrote", c.script, cut, shown, c.total)
		}
	}
}
