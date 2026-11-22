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
