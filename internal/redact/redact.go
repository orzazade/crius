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
	"strings"
)

// secretKey names a key whose value is a secret: the key = value pattern and SecretKey share it.
const secretKey = `(api[_-]?key|passw(?:or)?d|passphrase|secret|token(?:[_-]?(?:v?\d+|value|key|secret))?\b|private[_-]?key|` +
	`authorization)`

var secretKeyRe = regexp.MustCompile(`(?i)` + secretKey)

// SecretKey says whether a key's value is a secret, whatever its shape. Text built from structured data uses it
// where the patterns would see the value apart from its key.
func SecretKey(name string) bool { return secretKeyRe.MatchString(name) }

// A whole private key block; a cut-off block is removed up to the end of the text. It goes first: a key = value
// match on its first line would leave the rest of the block.
var blockRe = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(?:-----END [A-Z ]*PRIVATE KEY-----|$)`)

// key = value, also as JSON ("api_key": "..."), with the start of the value as the last group; redactPairs finds
// its end.
// The rest of the name may follow the secret word (secret_access_key, password.prod); the key's quotes may come
// escaped (code or JSON quoted in a command); the operator may be code's (:=, =>, ==); a scheme may lead the value
// (Authorization: Basic ...).
var pairRe = regexp.MustCompile(`(?i)` + secretKey + `[\w.-]*\\*["']?[ \t]*(?::=|=>|==?|:)[ \t]*` +
	`(?:(?:basic|bearer|digest|token)[ \t]+)?` + "(\\\\*[\"']|`|[^\\s\"',;`]+)")

// keyStart is a secret-named key and its operator at the start of a text.
var keyStart = regexp.MustCompile(`(?i)^[\w.-]*?` + secretKey + `[\w.-]*\\*["']?[ \t]*(?::=|=>|==?|:)`)

var secretRes = []*regexp.Regexp{
	// A long flag and its value (--password hunter2); -p is too short to tell.
	regexp.MustCompile(`(?i)--` + secretKey + `[\w-]*[ \t]+[^\s-]\S*`),
	// Credentials in a URL (postgres://user:pass@host).
	regexp.MustCompile(`\b[A-Za-z][A-Za-z0-9+.-]*://[^/\s:@"']+:[^@\s/"']+@`),
	regexp.MustCompile(`\b(sk-[A-Za-z0-9_-]{20,}|gh[pousr]_[A-Za-z0-9]{36,}|xox[bpors]-[A-Za-z0-9-]+|AKIA[0-9A-Z]{16})`),
	regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/-]{20,}=*`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`),
}

// Redact returns the text with each secret replaced by [redacted], and the number of secrets replaced.
func Redact(s string) (string, int) {
	hits := 0
	s = blockRe.ReplaceAllStringFunc(s, func(string) string { hits++; return "[redacted]" })
	s, n := redactPairs(s)
	hits += n
	for _, re := range secretRes {
		s = re.ReplaceAllStringFunc(s, func(string) string { hits++; return "[redacted]" })
	}
	return s, hits
}

// redactPairs replaces the value of each secret-named key, any length: a short password is still a password. A
// regexp cannot tell where such a value ends, so the scan does: a quoted value at the quote escaped like its opener
// (\"a\\\"b\" ends at the last \"), or at the end of the line when cut off; a backtick value at its pair; and a
// "value" that is itself a secret-named key means the first name had none (grep output: secrets.yaml: password: x).
func redactPairs(s string) (string, int) {
	var b strings.Builder
	hits, pos := 0, 0
	for {
		m := pairRe.FindStringSubmatchIndex(s[pos:])
		if m == nil {
			break
		}
		v, end := pos+m[len(m)-2], pos+m[len(m)-1] // the value is the last group: secretKey has one too
		if keyStart.MatchString(s[v:]) {
			b.WriteString(s[pos:v])
			pos = v
			continue
		}
		switch q := s[end-1]; q {
		case '"', '\'':
			end = quoteEnd(s, end, q, end-1-v)
		case '`':
			e := strings.IndexAny(s[end:], "`\n")
			if end == len(s) || s[end] == ' ' || s[end] == '\t' || e < 0 || s[end+e] != '`' {
				b.WriteString(s[pos:end]) // a lone backtick (markdown around the key) is no value
				pos = end
				continue
			}
			end += e + 1
		}
		b.WriteString(s[pos : pos+m[0]])
		b.WriteString("[redacted]")
		hits++
		pos = end
	}
	if hits == 0 {
		return s, 0
	}
	b.WriteString(s[pos:])
	return b.String(), hits
}

// quoteEnd is the end of a value opened by q after n backslashes: the next q after exactly n backslashes (after an
// even number when n is 0), or the end of the line.
func quoteEnd(s string, from int, q byte, n int) int {
	for i := from; i < len(s); i++ {
		switch s[i] {
		case '\n':
			return i
		case q:
			k := 0
			for j := i - 1; j >= from && s[j] == '\\'; j-- {
				k++
			}
			if n == 0 && k%2 == 0 || n > 0 && k == n {
				return i + 1
			}
		}
	}
	return len(s)
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
		// A key: redacted like any string; the whole value of a secret-named key, any type, goes too.
		r, n := Redact(s)
		if n == 0 {
			out = append(out, b[m[0]:m[1]]...)
		} else {
			enc, err := json.Marshal(r)
			if err != nil {
				return nil, err
			}
			out = append(out, enc...)
		}
		if !SecretKey(s) {
			continue
		}
		start := colon + 1 + len(b[colon+1:]) - len(bytes.TrimLeft(b[colon+1:], " \t\n"))
		d := json.NewDecoder(bytes.NewReader(b[start:]))
		var value json.RawMessage
		if err := d.Decode(&value); err != nil {
			return nil, err
		}
		out = append(append(out, b[last:start]...), `"[redacted]"`...)
		last = start + int(d.InputOffset())
	}
	return append(out, b[last:]...), nil
}
