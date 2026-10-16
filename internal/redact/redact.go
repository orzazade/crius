// Package redact is the secret scan for every output sink: stored text, CLI output, error text, logs and alerts
// (design §3c). Patterns from ~/.claude/hooks/check-secrets.sh plus common formats, a key = value scan that fails
// closed, and the gitleaks rules for token formats (rules.go). It imports only the standard library, so the watchdog
// can use it without depending on the ingest or the store.
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
	`authorization|[_.-](?:pass|pwd)\b)`

// operator is what stands between a key and its value: assignment and comparison in config, shell, Make and code.
const operator = `(?::=|=>|[?+!]?==?|:)`

var secretKeyRe = regexp.MustCompile(`(?i)` + secretKey)

// SecretKey says whether a key's value is a secret, whatever its shape. Text built from structured data uses it
// where the patterns would see the value apart from its key.
func SecretKey(name string) bool { return secretKeyRe.MatchString(name) }

// A whole private key block; a cut-off block is removed up to the end of the text. It goes first: a key = value
// match on its first line would leave the rest of the block.
var blockRe = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(?:-----END [A-Z ]*PRIVATE KEY-----|$)`)

// key = value, also as JSON ("api_key": "..."); the last group is empty and marks where the value starts.
// The rest of the name may follow the secret word (secret_access_key, password.prod); the key's quotes may come
// escaped (code or JSON quoted in a command); the operator may be code's (:=, =>, ==); a scheme may lead the value
// (Authorization: Basic ...).
var pairRe = regexp.MustCompile(`(?i)` + secretKey + `[\w.-]*\\*["']?[ \t]*` + operator + `[ \t]*` +
	`(?:(?:basic|bearer|digest|token)[ \t]+)?()`)

// blockStart is a key line's value that continues on the lines below: nothing (YAML), a block scalar (| or >) or an
// opening bracket, each after an optional tag (!vault) and before an optional comment, or a line that ends at
// another secret-named key and its operator (grep output: secrets.yaml: password:).
var blockStart = regexp.MustCompile(`(?i)^(?:![^\s]+[ \t]*)?(?:[|>][-+0-9]*|[(\[{])?[ \t]*(?:#.*)?$|` + secretKey +
	`[\w.-]*\\*["']?[ \t]*` + operator + `$`)

var secretRes = []*regexp.Regexp{
	// A long flag and its value, to the end of the line (--password hunter2).
	regexp.MustCompile(`(?i)--` + secretKey + `[\w-]*[ \t]+[^\s-][^\n]*`),
	// Credentials in a URL (postgres://user:pass@host, redis://:pass@host, ftp://me@corp.com:pass@host), to the last @
	// before the path.
	regexp.MustCompile(`\b[A-Za-z][A-Za-z0-9+.-]*://[^/\s:"']*:[^\s/"']+@`),
	// openssl's password argument (-pass pass:x, -passin, -passout).
	regexp.MustCompile(`(?i)-pass(?:in|out)?[ \t]+pass:\S*`),
	regexp.MustCompile(`\b(sk-[A-Za-z0-9_-]{20,}|gh[pousr]_[A-Za-z0-9]{36,}|xox[bpors]-[A-Za-z0-9-]+|AKIA[0-9A-Z]{16})`),
	regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/-]{20,}=*`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`),
}

// A short flag is a password on a command line where the line says so; elsewhere the flag means something else.
// -p: the line names a user (-u bob, -uroot, --user, --username) or a login, or a client that takes one that way
// (mysql -phunter2, sshpass -p hunter2). Keyed on the line, not a list of clients (Crius 23:53: docker login -u bob -p
// x was printed). A -p with a quoted value glued on (-p'x y') is a password on any line (Crius 00:39): no other -p
// takes one. -a or --pass on a redis-cli line. A line ending in a backslash goes on below, and an argument may be
// quoted (["-p", "x"]). The redaction runs from the first such flag (group 1) to the end of the command line.
var shortFlags = []struct{ line, flag *regexp.Regexp }{
	{regexp.MustCompile(`(?i)\b(?:mysql\w*|mariadb\w*|sshpass|login)\b|(?:^|[\s"'\[,])(?:-u|--user(?:name)?\b)`),
		regexp.MustCompile(`(?:^|[\s"'\[,])(-p)`)},
	{regexp.MustCompile(``), regexp.MustCompile(`(?:^|[\s"'\[,])(-p)["']`)},
	{regexp.MustCompile(`(?i)\bredis-cli\b`), regexp.MustCompile(`(?:^|[\s"'\[,])(-a|--pass)(?:[\s"'=,\]]|$)`)},
}

func redactShortFlag(s string) (string, int) {
	var b strings.Builder
	hits := 0
	for pos := 0; pos < len(s); {
		end := lineEnd(s, pos)
		for end < len(s) && strings.HasSuffix(strings.TrimRight(s[pos:end], "\r"), `\`) {
			end = lineEnd(s, end+1)
		}
		l := s[pos:end]
		body := strings.TrimRight(l, "\r")
		from := -1
		for _, f := range shortFlags {
			if m := f.flag.FindStringSubmatchIndex(body); m != nil && (from < 0 || m[2] < from) && f.line.MatchString(body) {
				from = m[2]
			}
		}
		if from >= 0 {
			b.WriteString(body[:from] + "[redacted]" + l[len(body):])
			hits++
		} else {
			b.WriteString(l)
		}
		if end < len(s) {
			b.WriteByte('\n')
		}
		pos = end + 1
	}
	if hits == 0 {
		return s, 0
	}
	return b.String(), hits
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
	s, n = redactShortFlag(s)
	hits += n
	s, n = redactRules(s)
	return s, hits + n
}

// redactPairs replaces the value of each secret-named key, any length: a short password is still a password. It
// fails closed (Crius 23:40, after six review rounds of value-end parsing): a value ends early only where its end is
// certain, at the close of a quote or backtick pair on the same line with no backslash inside; any other value runs
// to the end of the line (or of the JSON string, which JSON redacts alone). A value that continues below its key
// line (YAML, a block scalar, a line ending at an operator) takes the next line and the lines indented under the
// key. Over-redacting is accepted; printing part of a secret is not. Each step moves on through the text: linear.
func redactPairs(s string) (string, int) {
	var b strings.Builder
	hits, pos := 0, 0
	for {
		m := pairRe.FindStringSubmatchIndex(s[pos:])
		if m == nil {
			break
		}
		start, v := pos+m[0], pos+m[len(m)-2]
		b.WriteString(s[pos:start])
		b.WriteString("[redacted]")
		hits++
		pos = valueEnd(s, v)
	}
	if hits == 0 {
		return s, 0
	}
	b.WriteString(s[pos:])
	return b.String(), hits
}

// valueEnd is where the value that starts at v certainly ends (see redactPairs).
func valueEnd(s string, v int) int {
	le := len(s)
	if i := strings.IndexByte(s[v:], '\n'); i >= 0 {
		le = v + i
	}
	// v follows an operator or a space, so an escaped quote (\") starts with its backslash and is no opener here. The
	// close counts only before a separator: 'it''s x', 'a'"'"'b' and ""x go on after it.
	if q := s[v:min(v+1, le)]; q == `"` || q == "'" || q == "`" {
		if c := strings.IndexByte(s[v+1:le], q[0]); c >= 0 && !strings.Contains(s[v+1:v+1+c], `\`) {
			if e := v + 1 + c + 1; e == le || strings.IndexByte(" \t\r,;)]}#", s[e]) >= 0 {
				return e
			}
		}
	}
	if !blockStart.MatchString(strings.TrimRight(s[v:le], " \t\r")) || le == len(s) {
		return le
	}
	// The value goes on below: the next line whatever its indent, then each line indented deeper than the key line
	// or a list item (- x) at its indent.
	ls := strings.LastIndexByte(s[:v], '\n') + 1
	indent := len(s[ls:v]) - len(strings.TrimLeft(s[ls:v], " \t"))
	end := lineEnd(s, le+1)
	for end < len(s) {
		next := s[end+1 : lineEnd(s, end+1)]
		t := strings.TrimLeft(next, " \t")
		if d := len(next) - len(t); d <= indent && strings.TrimSpace(next) != "" &&
			!(d == indent && (strings.HasPrefix(t, "- ") || strings.TrimSpace(t) == "-")) {
			break
		}
		end = lineEnd(s, end+1)
	}
	return end
}

func lineEnd(s string, from int) int {
	if i := strings.IndexByte(s[from:], '\n'); i >= 0 {
		return from + i
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
