package redact

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

const pw = "Tr0ub4dor-horse"

// A run log keeps the names and the rest of the command, and not a byte of the secret.
func TestCommandKeepsAllButTheSecret(t *testing.T) {
	for _, c := range []struct {
		argv []string
		want string
	}{
		{[]string{"sh", "-c", "A=1 GH_TOKEN=" + pw + "; gh run view 7 | head"}, "sh -c A=1 GH_TOKEN=<redacted>; gh run view 7 | head"},
		{[]string{"sh", "-c", "TOKEN='" + pw + " x'y gh"}, "sh -c TOKEN=<redacted> gh"},
		{[]string{"sh", "-c", `TOKEN=a\ ` + pw + ` gh`}, "sh -c TOKEN=<redacted> gh"},
		{[]string{"sh", "-c", `TOKEN=$(cat "` + pw + ` x") gh`}, "sh -c TOKEN=<redacted> gh"},
		{[]string{"sh", "-c", `TOKEN= gh`}, "sh -c TOKEN= gh"},
		{[]string{"sh", "-c", `export "TOKEN="` + pw + ` x`}, `sh -c export "TOKEN=<redacted> x`},
		{[]string{"sh", "-c", `x "TOKEN=` + pw + `" y`}, `sh -c x "TOKEN=<redacted>" y`},
		{[]string{"sh", "-c", `sh -c "TOKEN='` + pw + ` x' gh" z`}, `sh -c sh -c "TOKEN=<redacted> gh" z`},
		{[]string{"sh", "-c", `curl -d '{"password":"` + pw + ` x","user":"u"}' h`}, `sh -c curl -d '{"password":<redacted>,"user":"u"}' h`},
		{[]string{"sh", "-c", `curl -d "{\"password\":\"` + pw + `\"}" h`}, `sh -c curl -d "{\"password\":<redacted>" h`},
		{[]string{"sh", "-c", "cat <<E\npassword=a " + pw + "\nE"}, "sh -c cat <<E\npassword=<redacted>\nE"},
		{[]string{"sh", "-c", "gh --token \\\n  " + pw + " x"}, "sh -c gh --token \\\n  <redacted> x"},
		{[]string{"sh", "-c", "a --token " + strings.Repeat("*", 3) + " --password " + pw + " x"}, "sh -c a --token *** --password <redacted> x"},
		{[]string{"sh", "-c", `curl -su "bob:` + pw + ` x" h`}, `sh -c curl -su "bob:<redacted>" h`},
		{[]string{"curl", "-su", "bob:" + pw + " x", "h"}, "curl -su bob:<redacted> h"},
		{[]string{"mysql", "-u", "root", "-p" + pw}, "mysql -u root -p<redacted>"},
		{[]string{"docker", "login", "-u", "bob", "-p", pw, "host"}, "docker login -u bob -p <redacted>"},
		{[]string{"x", "-token", pw, "y"}, "x -token <redacted> y"},
		{[]string{"x", "Password=" + pw + " y;A=b", "z"}, "x Password=<redacted> z"},
		// Names whose secret word is not the last, and known keys.
		{[]string{"sh", "-c", "GH_TOKEN_PROD=" + pw + " API_TOKENS=" + pw + " DB_PASS_2=" + pw + " OPENAI_KEY=" + pw + " REDISCLI_AUTH=" + pw + " x"},
			"sh -c GH_TOKEN_PROD=<redacted> API_TOKENS=<redacted> DB_PASS_2=<redacted> OPENAI_KEY=<redacted> REDISCLI_AUTH=<redacted> x"},
		{[]string{"curl", "-ubob:" + pw, "h"}, "curl -ubob:<redacted> h"},
		{[]string{"curl", "-U", "a/b:" + pw, "h"}, "curl -U a/b:<redacted> h"},
		{[]string{"sh", "-c", "curl 'https://x?a=1&b=2' \\\n -su D/bob:" + pw + " h"}, "sh -c curl 'https://x?a=1&b=2' \\\n -su D/bob:<redacted> h"},
		// Shell quoting.
		{[]string{"sh", "-c", "# it's\nPASSWORD='a " + pw + "' gh"}, "sh -c # it's\nPASSWORD=<redacted> gh"},
		{[]string{"sh", "-c", "TOKEN=`echo " + pw + "` gh"}, "sh -c TOKEN=<redacted> gh"},
		{[]string{"sh", "-c", `TOKEN="$(echo "a ` + pw + `")" gh`}, "sh -c TOKEN=<redacted> gh"},
		{[]string{"sh", "-c", `TOKEN=$'a\'b ` + pw + `' gh`}, "sh -c TOKEN=<redacted> gh"},
		{[]string{"sh", "-c", "x=$(TOKEN=" + pw + " gh) y"}, "sh -c x=$(TOKEN=<redacted> gh) y"},
		{[]string{"sh", "-c", "cat <<E\nit's\nE\nPASSWORD='a " + pw + "' gh"}, "sh -c cat <<E\nit's\nE\nPASSWORD=<redacted> gh"},
		{[]string{"sh", "-c", "docker \\\n login -u bob -p " + pw}, "sh -c docker \\\n login -u bob -p <redacted>"},
		{[]string{"mongosh", "-u", "admin", "-p", pw}, "mongosh -u admin -p <redacted>"},
		{[]string{"redis-cli", "-a" + pw, "ping"}, "redis-cli -a<redacted>"},
		{[]string{"x", `{"name":"X_PASSWORD","value":"` + pw + `"}`}, `x {"name":"X_PASSWORD","value":"<redacted>"}`},
		// No secret.
		{[]string{"docker", "compose", "-p", "crius", "exec", "db", "mysql", "-e", "select 1"}, "docker compose -p crius exec db mysql -e select 1"},
		{[]string{"docker", "run", "-d", "-p", "3306:3306", "mysql:8"}, "docker run -d -p 3306:3306 mysql:8"},
		{[]string{"sh", "-c", "mkdir -p /var/lib/mysql && az acr login; go test -p 2 ./internal/mysql -p 2"}, "sh -c mkdir -p /var/lib/mysql && az acr login; go test -p 2 ./internal/mysql -p 2"},
		{[]string{"x", "--token-file", "/a/b", "--password-stdin", "reg.io"}, "x --token-file /a/b --password-stdin reg.io"},
		{[]string{"git", "diff", "-u", "host:/path"}, "git diff -u host:/path"},
		{[]string{"date", "-u", "+%H:%M"}, "date -u +%H:%M"},
		{[]string{"docker", "run", "-u", "1000:1000", "img"}, "docker run -u 1000:1000 img"},
		{[]string{"go", "test", "-p", "2", "-u", "./internal/login"}, "go test -p 2 -u ./internal/login"},
	} {
		got := Command(c.argv)
		if got != c.want {
			t.Errorf("%q\n got %q\nwant %q", c.argv, got, c.want)
		}
		if strings.Contains(got, pw) {
			t.Errorf("%q: secret kept", c.argv)
		}
		if again := Command([]string{got}); strings.Contains(again, pw) {
			t.Errorf("%q: secret back", c.argv)
		}
	}
	if got := Script("GH_TOKEN=" + pw + " gh"); got != "GH_TOKEN=<redacted> gh" {
		t.Errorf("Script: %q", got)
	}
}

func TestLogKeepsAllButTheSecret(t *testing.T) {
	key := "-----BEGIN " + "PRIVATE KEY-----"
	end := "-----END " + "PRIVATE KEY-----"
	for _, c := range []struct{ in, want string }{
		{"a --token *** --password " + pw + " x\nb\n", "a --token *** --password <redacted>\nb\n"},
		{"DB_PASSWORD=abc;" + pw + "\n", "DB_PASSWORD=<redacted>\n"},
		{"password=abc;" + pw + "\n", "password=<redacted>\n"},
		{"Server=h;Password=abc;Timeout=3 ok\n", "Server=h;Password=<redacted>;Timeout=3 ok\n"},
		{"token: " + pw + " and " + pw + "\r\nnext\n", "token: <redacted>\r\nnext\n"},
		{"EMPTY_TOKEN=\nnext\n", "EMPTY_TOKEN=\nnext\n"},
		{"private_key: \n" + key + "\nMIIabc\n" + end + "\nnext\n", "private_key: \n<redacted>\n<redacted>\n<redacted>\nnext\n"},
		{"ts " + key + "\nts MIIabc\nts " + end + " tail\nnext\n", "ts <redacted>\n<redacted>\n<redacted> tail\nnext\n"},
		{"password:\n  " + pw + "\n\nnext\n", "password:\n  <redacted>\n\nnext\n"},
		{"password: |\n  a\n  " + pw + "\nnext\n", "password: <redacted>\n  <redacted>\n  <redacted>\nnext\n"},
		{"GET /x?code=" + strings.Repeat("aB3", 10) + "&n=1 code=3 ?code=404\n", "GET /x?code=<redacted>&n=1 code=3 ?code=404\n"},
		{"weighs_and_thoughs_per_instrument_in_total 1\n", "weighs_and_thoughs_per_instrument_in_total 1\n"},
		{"GH_TOKEN_PROD=" + pw + " x\nDB_PASS_2=" + pw + "\n", "GH_TOKEN_PROD=<redacted>\nDB_PASS_2=<redacted>\n"},
		{"[{\n  \"name\": \"X_PASSWORD\",\n  \"slotSetting\": false,\n  \"value\": \"" + pw + "\"\n}]\n", "[{\n  \"name\": \"X_PASSWORD\",\n  \"slotSetting\": false,\n  \"value\": <redacted>\n}]\n"},
		{"- name: DB_PASSWORD\n  value: " + pw + "\n- name: X\n  value: y\n", "- name: DB_PASSWORD\n  value: <redacted>\n- name: X\n  value: y\n"},
		{`<add key="Password" value="` + pw + `"/>` + "\n", `<add key="Password" value="<redacted>"/>` + "\n"},
		{"password=\\\n" + pw + "\nnext\n", "password=<redacted>\n<redacted>\nnext\n"},
		{`TOKEN="` + "gh" + "p_" + strings.Repeat("aB3d", 9) + " " + pw + `"` + "\n", "TOKEN=<redacted>\n"},
		{"err: missing " + key + " header\nFAIL TestX\n7397 passed\n", "err: missing <redacted>\nFAIL TestX\n7397 passed\n"},
		{key + "\n" + strings.Repeat("x\n", maxBlock+1), "<redacted>\n" + strings.Repeat("<redacted>\n", maxBlock) + "x\n"},
		{"db_pass: 12345\n", "db_pass: <redacted>\n"},
		{normal, normal},
		{`"js-cookie": "3.0.5", cookie: ok` + "\n", `"js-cookie": "3.0.5", cookie: ok` + "\n"},
		{"-unit:abc --user-agent: x -update\n", "-unit:abc --user-agent: x -update\n"},
	} {
		if got := Log(c.in); got != c.want {
			t.Errorf("%q\n got %q\nwant %q", c.in, got, c.want)
		}
		// The same in writes of one byte.
		var b bytes.Buffer
		w := NewStream(&b)
		for i := range len(c.in) {
			w.Write([]byte(c.in[i : i+1]))
		}
		if err := w.Close(); err != nil || b.String() != c.want {
			t.Errorf("%q byte by byte: %q, %v", c.in, b.String(), err)
		}
	}
}

// What looks like a key and is a count, a result, a file and its line.
const normal = `secrets: 0
unit-pass: 12
test_pass: 7397
Check secrets: success
--- PASS: TestX/token:a (0.00s)
=== RUN   TestX/password:b
TestAuthorization: PASS
/src/Appxite.Token.csproj : warning NU1903: x
secret.tf:12
token.proto:5:1
secret.json:12: bad
docker compose -p crius exec db mysql -e 'select 1'
MAX_TOKENS=4096
TOKENIZER_PATH=/x
`

// A command or a line of many pairs is read in one pass, not once per pair (deep review of 11-23): 256 KiB of the first
// four took longer than go test's own 10 minutes.
func TestManyPairsAreReadInOnePass(t *testing.T) {
	for i, unit := range []string{`token="a",`, "--token=x ", "--token ", `'"token":"a",`, "export A_TOKEN='x' B=2;\n", `"TOKEN=x" `,
		"token", "token:", "token: ***,", "\\", `"$(`, "`", "-u a:", "mysql ", "redis-cli ", "curl -u", "?token=a&", "password=a;"} {
		size := 8 << 10
		if i < 4 {
			size = 256 << 10
		}
		s := strings.Repeat(unit, size/len(unit))
		Log(s)
		Script(s)
		Command([]string{"x", s})
	}
}

// A line over the limit keeps its start; nothing that could hold part of a secret at the cut is kept.
func TestAnOverLongLineIsCutAndSaysSo(t *testing.T) {
	got := Log(strings.Repeat("x", maxLine-5) + " " + pw + strings.Repeat("y", maxLine) + "\nnext\n")
	if strings.Contains(got, pw[:3]) || !strings.HasPrefix(got, strings.Repeat("x", maxLine-lineGuard)) ||
		!strings.Contains(got, "of a line over 1048576 bytes left out)\nnext\n") {
		t.Fatalf("%d bytes, end %q", len(got), got[max(0, len(got)-120):])
	}
}

func TestAStreamWhoseRulesFailWritesNothingMore(t *testing.T) {
	var b bytes.Buffer
	w := NewStream(&b)
	w.Line = func(l string) string {
		if l == "bad" {
			panic("boom")
		}
		return l
	}
	if n, err := io.WriteString(w, "a\nbad\nafter\n"); n != 12 || err != nil { // the command writing to it runs on
		t.Fatal(n, err)
	}
	if err := w.Close(); err == nil || !strings.Contains(err.Error(), "redaction failed: boom") || b.String() != "a\n" {
		t.Fatalf("%v, %q", err, b.String())
	}
	w = NewStream(failing{})
	io.WriteString(w, "a\n")
	if w.Close() == nil {
		t.Fatal("a failed write is an error")
	}
}

type failing struct{}

func (failing) Write([]byte) (int, error) { return 0, errors.New("full") }

// hides fails when s, given as a command's arguments, a sh -c text or an output line, keeps the secret.
func hides(t *testing.T, argv ...string) {
	t.Helper()
	line := strings.Join(argv, " ")
	for _, got := range []string{Command(argv), Command([]string{"sh", "-c", line}), Script("x && " + line), Log(line + "\n"), Log("+ " + line + "\nnext\n")} {
		if strings.Contains(got, pw) || !strings.Contains(got, Mark) {
			t.Errorf("%q: secret kept: %q", argv, got)
		}
	}
}

// The second review of 11-23: a password flag directly behind its client was kept.
func TestAPasswordFlagRightBehindItsClient(t *testing.T) {
	for _, client := range []string{"sshpass", "mysql", "mysqldump", "mysqladmin", "mariadb", "mongo", "mongosh", "mongodump", "/usr/local/bin/mysql",
		"docker login", "podman login", "nerdctl login", "buildah login", "skopeo login", "helm registry login", "oras login", "oc login", "az login", "cf login"} {
		c := strings.Fields(client)
		hides(t, append(c[:len(c):len(c)], "-p", pw, "-u", "bob")...)
		hides(t, append(c[:len(c):len(c)], "-p"+pw, "-uroot")...)
		hides(t, append(c[:len(c):len(c)], "-u", "bob", "-p", pw)...)
	}
	hides(t, "redis-cli", "-a", pw, "ping")
	hides(t, "redis-cli", "-a"+pw, "ping")
	if got := Log(`["sshpass", "-p", "` + pw + `", "ssh"]` + "\n"); strings.Contains(got, pw) {
		t.Errorf("list form: %q", got)
	}
	for _, s := range []string{"docker run --name mysql -p 3306:3306 mysql:8", "docker run --name mysql -p 127.0.0.1:3306:3306/tcp -d mysql:8"} {
		if got := Script(s); got != s {
			t.Errorf("a port map: %q", got)
		}
		if got := Log(s + "\n"); got != s+"\n" {
			t.Errorf("a port map in output: %q", got)
		}
	}
	hides(t, "docker", "run", "--name", "mysql", "-p", "3306:3306", "img", "mysql", "-p"+pw)
}

func TestOpensslPassArgumentsAreHidden(t *testing.T) {
	for _, f := range []string{"-passin", "-passout", "-pass"} {
		hides(t, "openssl", "x", f, "pass:"+pw, "-in", "k.pem")
	}
	if got, want := Command([]string{"openssl", "x", "-passin", "pass:" + pw + " x", "-in", "k.pem"}), "openssl x -passin pass:<redacted> -in k.pem"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestMoreSecretNamesAreHidden(t *testing.T) {
	for _, name := range []string{"userPWD", "DbPwd", "DBPASS", "dbpass", "PASS", "Storage__Key", "Jwt__Signing__Key", "CREDS", "DB_CREDS", "CREDENTIALS"} {
		hides(t, name+"="+pw, "x")
	}
	for _, name := range []string{"x-functions-key", "credentials", "userPWD", "Db__Key"} {
		for _, form := range []string{name + ": " + pw, `"` + name + `": "` + pw + `"`} {
			if got := Log(form + "\n"); strings.Contains(got, pw) || !strings.Contains(got, Mark) {
				t.Errorf("%q kept: %q", form, got)
			}
			if got := Command([]string{"curl", "-H", form}); strings.Contains(got, pw) {
				t.Errorf("%q kept: %q", form, got)
			}
		}
	}
}

func TestIndexedNamesAreHidden(t *testing.T) {
	for _, l := range []string{`config["Password"] = "` + pw + `"`, `os.environ["TOKEN"]="` + pw + `"`, `headers['Authorization'] = '` + pw + `'`, `cfg["pass"] = "` + pw + `"`} {
		hides(t, l)
	}
}

func TestKubernetesSecretOutputIsHidden(t *testing.T) {
	for _, l := range []string{"  tls.key: " + pw, "  .dockerconfigjson: " + pw, `{"data":{"tls.key":"` + pw + `"}}`, `{"data":{".dockerconfigjson":"` + pw + `"}}`,
		"  tls.key: |\n    " + pw} {
		if got := Log(l + "\n"); strings.Contains(got, pw) || !strings.Contains(got, Mark) {
			t.Errorf("%q kept: %q", l, got)
		}
	}
}

func TestSecretSetCommandsAreHidden(t *testing.T) {
	hides(t, "az", "keyvault", "secret", "set", "--vault-name", "v", "-n", "n", "--value", pw)
	hides(t, "az", "keyvault", "secret", "set", "--value="+pw, "-n", "n")
	hides(t, "gh", "secret", "set", "NAME", "--body", pw)
	hides(t, "gh", "secret", "set", "--body", pw, "NAME")
	hides(t, "gh", "secret", "set", "NAME", "-b", pw)
	hides(t, "gh", "secret", "set", "NAME", "-b"+pw)
	hides(t, "kubectl", "create", "secret", "generic", "n", "--from-literal=k="+pw)
	hides(t, "kubectl", "create", "secret", "generic", "n", "--from-literal", "k="+pw)
	hides(t, "kubectl", "-n", "x", "create", "secret", "generic", "n", "--from-literal=a=1", "--from-literal=k="+pw)
	hides(t, "curl", "--oauth2-bearer", pw, "https://h/x")
}

// What the rules above must leave alone.
func TestWhatOnlyLooksLikeASecretIsKept(t *testing.T) {
	for _, l := range []string{"PASS=7 FAIL=0", "pass: 12", "PASS: TestX", "--- PASS: TestX (0.01s)", "PASS=$((PASS+1))", "BYPASS=yes", "bypass: on", "OLDPWD=/tmp/x",
		"GOOGLE_APPLICATION_CREDENTIALS=/a/b/key.json", "git -c credential.helper=osxkeychain push", "tls.key:  1704 bytes", "--from-file=tls.key=./tls.key",
		"pass-rate: 99%", "pass = the number of checks", "kubectl create configmap n --from-literal=k=v", "gh pr create --body text", "mkdir -p a && mysql --version",
		"credentials.go:12:5: x", "for k, v := range m[key] {", "RUNLOG /x sha=1e4d6df pass=1"} {
		if got := Log(l + "\n"); got != l+"\n" {
			t.Errorf("%q changed: %q", l, got)
		}
		if got := Script(l); got != l {
			t.Errorf("%q changed as a command: %q", l, got)
		}
	}
}

// The third review of 11-23.
func TestShortSecretNamesAreHidden(t *testing.T) {
	for _, name := range []string{"pwd", "PAT", "PW", "DB_PW", "SA_PW", "JWT", "_auth"} {
		hides(t, name+"="+pw, "x")
		for _, form := range []string{name + ": " + pw, `"` + name + `": "` + pw + `"`} {
			if got := Log(form + "\n"); strings.Contains(got, pw) || !strings.Contains(got, Mark) {
				t.Errorf("%q kept: %q", form, got)
			}
		}
	}
	for _, l := range []string{"cd $(pwd)", "pattern: x", "path=/a/b", "compat: yes", "author: x"} {
		if got := Log(l + "\n"); got != l+"\n" {
			t.Errorf("%q changed: %q", l, got)
		}
	}
}

func TestAColouredLineIsJudgedWithoutItsColours(t *testing.T) {
	for _, l := range []string{"\x1b[34;1m\"password\"\x1b[0m \x1b[1;39m: \x1b[0;32m\"" + pw + "\"\x1b[0m", "\x1b[1mTOKEN\x1b[0m=" + pw,
		"\x1b[1m-----BEGIN PRIVATE KEY-----\x1b[0m\n" + pw + "\n-----END PRIVATE KEY-----"} {
		if got := Log(l + "\n"); strings.Contains(got, pw) || !strings.Contains(got, Mark) {
			t.Errorf("%q kept: %q", l, got)
		}
	}
	if l := "\x1b[32mok\x1b[0m 12 pass\n"; Log(l) != l {
		t.Errorf("a coloured line with no secret changed: %q", Log(l))
	}
}
