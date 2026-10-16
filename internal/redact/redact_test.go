package redact

import (
	"strings"
	"testing"
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
