// Package redact is the secret scan for every output sink: stored text, CLI output, error text, logs and alerts
// (design §3c). Patterns from ~/.claude/hooks/check-secrets.sh plus common formats. It imports only the standard
// library, so the watchdog can use it without depending on the ingest or the store.
//
// The patterns need context: a key next to its value, quotes unescaped, a secret whole. Redact text before any step
// that removes context (cutting a snippet, decoding JSON, dropping keys), or carry the context through it as
// SecretKey and JSON do. Each of the three ways cost a leak in step 4 (reviews of 5d5fee4, 831be40, 4836329).
package redact

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
)

// secretKey names a key whose value is a secret: the key = value pattern and SecretKey share it.
const secretKey = `(api[_-]?key|passw(?:or)?d|secret[_-]?key|access[_-]?token|private[_-]?key)`

var secretKeyRe = regexp.MustCompile(`(?i)` + secretKey)

// SecretKey says whether a key's value is a secret, whatever its shape. Text built from structured data uses it
// where the patterns would see the value apart from its key.
func SecretKey(name string) bool { return secretKeyRe.MatchString(name) }

var secretRes = []*regexp.Regexp{
	// A whole private key block; a cut-off block is removed up to the end of the text.
	regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(?:-----END [A-Z ]*PRIVATE KEY-----|$)`),
	// key = value, also as JSON ("api_key": "..."). Any length: a short password is still a password; a value
	// with no closing quote (cut off) is removed to the end of the line.
	regexp.MustCompile(`(?i)` + secretKey + `["']?[ \t]*[=:][ \t]*` +
		`(?:"[^"\n]*"?|'[^'\n]*'?|[^\s"',;]+)`),
	regexp.MustCompile(`\b(sk-[A-Za-z0-9_-]{20,}|gh[pousr]_[A-Za-z0-9]{36,}|xox[bpors]-[A-Za-z0-9-]+|AKIA[0-9A-Z]{16})`),
	regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/-]{20,}=*`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`),
}

// Redact returns the text with each secret replaced by [redacted], and the number of secrets replaced.
func Redact(s string) (string, int) {
	hits := 0
	for _, re := range secretRes {
		s = re.ReplaceAllStringFunc(s, func(string) string { hits++; return "[redacted]" })
	}
	return s, hits
}

// Writer passes each write through Redact: output may quote a config value, an argument or an error.
type Writer struct{ W io.Writer }

func (r Writer) Write(b []byte) (int, error) {
	s, _ := Redact(string(b))
	if _, err := io.WriteString(r.W, s); err != nil {
		return 0, err
	}
	return len(b), nil
}

var jsonString = regexp.MustCompile(`"(?:[^"\\]|\\.)*"`)

// JSON is json.MarshalIndent with every string redacted after it is decoded: escaping hides a value's quotes
// (password: \"abc\") from the patterns, so a Writer alone is not enough for JSON. The scalar value of a
// secret-named key is redacted whole, since a string redacted alone never sees its key. Order and layout are kept.
func JSON(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	var out []byte
	last := 0
	// In valid JSON every match is one whole literal: a match ends where the next one may start.
	for _, m := range jsonString.FindAllIndex(b, -1) {
		if m[0] < last {
			continue // inside a value already replaced
		}
		var s string
		if err := json.Unmarshal(b[m[0]:m[1]], &s); err != nil {
			return nil, err
		}
		out = append(out, b[last:m[0]]...)
		last = m[1]
		colon := m[1] + len(b[m[1]:]) - len(bytes.TrimLeft(b[m[1]:], " \t\n"))
		if colon == len(b) || b[colon] != ':' {
			r, n := Redact(s)
			if n == 0 {
				out = append(out, b[m[0]:m[1]]...)
				continue
			}
			enc, err := json.Marshal(r)
			if err != nil {
				return nil, err
			}
			out = append(out, enc...)
			continue
		}
		out = append(out, b[m[0]:m[1]]...)
		start := colon + 1 + len(b[colon+1:]) - len(bytes.TrimLeft(b[colon+1:], " \t\n"))
		if !SecretKey(s) || start == len(b) || b[start] == '{' || b[start] == '[' {
			continue // an object or array value has keys of its own
		}
		end := start + bytes.IndexAny(b[start:], ",}] \t\n")
		if b[start] == '"' {
			end = start + jsonString.FindIndex(b[start:])[1]
		}
		out = append(append(out, b[last:start]...), `"[redacted]"`...)
		last = end
	}
	return append(out, b[last:]...), nil
}
