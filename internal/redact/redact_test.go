package redact

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRedactCommonSecretShapes(t *testing.T) {
	for _, s := range []string{
		"token ghp_" + strings.Repeat("A", 36),
		"api_key=" + strings.Repeat("a", 24),
		`"api_key": "abcdefghijklmnopqrstuvwxyz123456"`, // codex P1: JSON-quoted keys
		"Authorization: Bearer " + strings.Repeat("b", 30),
		"sk-" + strings.Repeat("c", 30),
	} {
		got, n := Redact(s)
		if n == 0 || strings.Contains(got, strings.Repeat("A", 20)) || strings.Contains(got, "abcdefghijklmnop") ||
			strings.Contains(got, strings.Repeat("b", 20)) || strings.Contains(got, strings.Repeat("c", 20)) ||
			strings.Contains(got, strings.Repeat("a", 20)) {
			t.Errorf("%q -> %q", s, got)
		}
	}
	if got, n := Redact("plain words, nothing secret"); n != 0 || got != "plain words, nothing secret" {
		t.Fatal(got)
	}
}

// Codex P1 on the Python version: only the BEGIN line of a private key was replaced.
func TestRedactRemovesTheWholePrivateKeyBlock(t *testing.T) {
	body := strings.Repeat("MIIEvQIBADANBgkqhkiG9w0BAQEFAASC", 3)
	for _, s := range []string{
		"x -----BEGIN RSA PRIVATE KEY-----\n" + body + "\n-----END RSA PRIVATE KEY----- y",
		"cut -----BEGIN PRIVATE KEY-----\n" + body,
	} {
		got, _ := Redact(s)
		if strings.Contains(got, "MIIEvQ") {
			t.Errorf("%q", got)
		}
	}
}

// Codex P1 on the Go version: a secret-named key needed a 20-character value, so short passwords were kept.
func TestRedactShortValuesOfSecretNamedKeys(t *testing.T) {
	for s, secret := range map[string]string{"password=hunter2": "hunter2", `{"password": "pa ss!"}`: "pa ss",
		"PASSWD: s3cr3t": "s3cr3t", "api_key='k1'": "k1", "access_token: tok-9": "tok-9"} {
		if got, n := Redact(s); n == 0 || strings.Contains(got, secret) {
			t.Errorf("%q -> %q", s, got)
		}
	}
	if got, n := Redact("the password is required"); n != 0 {
		t.Fatal(got)
	}
}

// Codex P1 on the Go version: a value with no closing quote (a cut-off credential) was kept.
func TestRedactAnUnterminatedQuotedValue(t *testing.T) {
	for _, s := range []string{`password="abcdefghijklmnop`, `api_key: 'abcdefghijklmnop`} {
		if got, n := Redact(s); n == 0 || strings.Contains(got, "abcdefghijklmnop") {
			t.Errorf("%q -> %q", s, got)
		}
	}
}

// Review of step 4 P1: JSON escapes the quotes around a value (password: \"abc\"), so the line patterns missed it.
func TestJSONRedactsEachStringBeforeEncoding(t *testing.T) {
	v := map[string]any{"text": `password: "hunter22"`, "list": []any{"token ghp_" + strings.Repeat("D", 36)}, "n": 3}
	b, err := JSON(v)
	if err != nil {
		t.Fatal(err)
	}
	if s := string(b); strings.Contains(s, "hunter22") || strings.Contains(s, "DDDD") || !strings.Contains(s, `"n": 3`) {
		t.Fatal(s)
	}
}

// Review 0178403: a key at the end of a line takes nothing from the next line.
func TestRedactStaysOnItsLine(t *testing.T) {
	out, _ := Redact("new password:\n2026-10-16 next line")
	if !strings.Contains(out, "\n2026-10-16 next line") {
		t.Fatalf("%q", out)
	}
}

// Review 4836329: the value of a secret-named key is a secret whatever its shape, also when the key is a JSON key
// (each string redacted alone never sees the key).
func TestJSONRedactsTheValueOfASecretKey(t *testing.T) {
	v := map[string]any{"refs": map[string]any{"password": "two words", "env": map[string]any{"DB_PASSWORD": "pg9"},
		"api_key": 123456, "user": "bob"}}
	b, err := JSON(v)
	if err != nil {
		t.Fatal(err)
	}
	if s := string(b); strings.Contains(s, "two words") || strings.Contains(s, "pg9") || strings.Contains(s, "123456") ||
		!strings.Contains(s, `"user": "bob"`) || !strings.Contains(s, `"password": "[redacted]"`) {
		t.Fatal(s)
	}
	for k, want := range map[string]bool{"password": true, "DB_PASSWORD": true, "x-api-key": true, "user": false, "pass": false} {
		if SecretKey(k) != want {
			t.Error(k, !want)
		}
	}
}

// Live run on 9b807e4: text that quotes code or JSON carries escaped quotes (PASSWORD=\"x\"); the value pattern
// stopped at the backslash and the secret followed.
func TestRedactAnEscapedQuotedValue(t *testing.T) {
	for _, in := range []string{`export PASSWORD=\"hunter2xyz\" && echo`, `{\"api_key\": \"two words k3y\"}`,
		`PASSWORD=\\\"hunter2xyz\\\"`, `secret_key=\'two words k3y\'`} {
		if out, n := Redact(in); n != 1 || strings.Contains(out, "hunter2") || strings.Contains(out, "k3y") {
			t.Errorf("%s -> %s", in, out)
		}
	}
}

// Review 9b807e4: common secret names; a plural that only looks like one is left alone.
func TestRedactMoreSecretNames(t *testing.T) {
	for _, in := range []string{"aws_secret_access_key=hunter2", "client_secret: hunter2", "token=hunter2",
		"GITHUB_TOKEN=hunter2", "passphrase: hunter2"} {
		if out, _ := Redact(in); strings.Contains(out, "hunter2") {
			t.Errorf("%s -> %s", in, out)
		}
	}
	if out, n := Redact("tokens: 500"); n != 0 {
		t.Error(out)
	}
}

// Review 9b807e4: keys are redacted too, and an array or object under a secret-named key goes whole.
func TestJSONRedactsKeysAndWholeValues(t *testing.T) {
	v := map[string]any{"ghp_" + strings.Repeat("D", 36): 1, "password=hunter2": 1, "passwords": []any{"hunter3"},
		"password": map[string]any{"value": "hunter4"}, "after": "kept"}
	b, err := JSON(v)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, leak := range []string{"DDDD", "hunter2", "hunter3", "hunter4"} {
		if strings.Contains(s, leak) {
			t.Error(leak, s)
		}
	}
	if !strings.Contains(s, `"after": "kept"`) || !json.Valid(b) {
		t.Error(s)
	}
}

// Review 33c07fd: operators between key and value, an escaped quote inside the value, long flags, names after
// token, dotted names, backticks, URL credentials and Authorization headers.
func TestRedactMoreShapes(t *testing.T) {
	for _, in := range []string{`password := "hunter2"`, `'password' => 'hunter2'`, `if PASSWORD == "hunter2"`,
		`password="ab\"hunter2"`, `PASSWORD=\"ab\\\"hunter2\"`, `mysql --password hunter2`, "GH_TOKEN_2=hunter2",
		"tokenValue=hunter2", "password.prod=hunter2", "PASSWORD = `two hunter2`", "postgres://user:hunter2@db/x",
		"Authorization: Basic dXNlcjpodW50ZXIy", `"authorization": "Basic hunter2"`} {
		if out, _ := Redact(in); strings.Contains(out, "hunter2") || strings.Contains(out, "dXNlcjpodW50ZXIy") {
			t.Errorf("%s -> %s", in, out)
		}
	}
	for _, keep := range []string{"tokens: 500", "max_tokens: 4096", "token_count=3", "https://example.com/a@b",
		"basic setup done", "docker login --password-stdin --username bob"} {
		if out, n := Redact(keep); n != 0 {
			t.Errorf("%s -> %s", keep, out)
		}
	}
}

// Review 0d2402b: a name followed by a secret key is no key with a value (grep output: "config/secrets.yaml:
// password: x"); a value ends at the quote that matches its opener, escaped or not; a backtick needs its pair; a
// URL credential stays inside its JSON string.
func TestRedactValueEnds(t *testing.T) {
	for _, in := range []string{"config/secrets.yaml:  password: hunter2", "internal/secret.go:\tpassword = \"hunter2\"",
		"secrets.env: DB_PASSWORD = hunter2", "token.json: api_key: hunter2", "GH_TOKEN_2: password: hunter2",
		`PASSWORD=\"ab\\\"hunter2\" tail`, `password="ab\"hunter2" tail`} {
		if out, _ := Redact(in); strings.Contains(out, "hunter2") {
			t.Errorf("%s -> %s", in, out)
		}
	}
	for in, want := range map[string]string{
		`password: \"abc\" in quota --json output the JSON breaks.`: `[redacted] in quota --json output the JSON breaks.`,
		`curl -d "{\"password\":\"x\",\"user\":\"bob\"}"`:           `curl -d "{\"[redacted],\"user\":\"bob\"}"`,
		"The key `api_key:` in config.yaml sets the limit":          "The key `api_key:` in config.yaml sets the limit",
		`{"a":"http://h:1","b":"x@y.z","c":"keep"}`:                 `{"a":"http://h:1","b":"x@y.z","c":"keep"}`,
		"password=\"cut off\nnext line stays":                       "[redacted]\nnext line stays",
		"PASSWORD = `two hunter2` then more":                        "[redacted] then more",
	} {
		if out, _ := Redact(in); out != want {
			t.Errorf("%s\n got %s\nwant %s", in, out, want)
		}
	}
}

// Review of e5f5af1: a value that only looks like a key (a secret word and an operator, then nothing) is still the
// value, and all of it is redacted; an unclosed backtick value runs to the end of the line; a value that ends in a
// backslash still ends at its quote.
func TestRedactValueThatLooksLikeAKey(t *testing.T) {
	for _, in := range []string{"password=hunter2secret:", "password=hunter2Token= \nnext", "api_key=Hunter2.secret:,x",
		"password=hunter2.secret=x tail", "password=`hunter2", "password = `hunter2\nmore`"} {
		if out, n := Redact(in); n == 0 || strings.Contains(strings.ToLower(out), "hunter2") {
			t.Errorf("%q -> %q (%d)", in, out, n)
		}
	}
	if out, _ := Redact("password=hunter2 tail stays, token=x;y"); out != "[redacted] tail stays, [redacted];y" {
		t.Error(out)
	}
	in := `curl -d "{\"password\":\"a\\\\\"}" keep this text` + "\nnext line"
	if out, _ := Redact(in); out != `curl -d "{\"[redacted]}" keep this text`+"\nnext line" {
		t.Errorf("%s -> %s", in, out)
	}
}

// Review of e5f5af1: keys used as values, or a long run after them, take linear time (they took minutes).
func TestRedactKeysAsValuesAreLinear(t *testing.T) {
	for _, in := range []string{strings.Repeat("password=", 1<<13), strings.Repeat("password=", 500) + strings.Repeat("a", 1<<18)} {
		t0 := time.Now()
		Redact(in)
		if d := time.Since(t0); d > time.Second {
			t.Errorf("%d bytes took %v", len(in), d)
		}
	}
}
