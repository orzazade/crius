// Package redact is the secret scan for every output sink: stored text, CLI output, error text, logs and alerts
// (design §3c). Patterns from ~/.claude/hooks/check-secrets.sh plus common formats. It imports only the standard
// library, so the watchdog can use it without depending on the ingest or the store.
package redact

import (
	"io"
	"regexp"
)

var secretRes = []*regexp.Regexp{
	// A whole private key block; a cut-off block is removed up to the end of the text.
	regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(?:-----END [A-Z ]*PRIVATE KEY-----|$)`),
	// key = value, also as JSON ("api_key": "..."). Any length: a short password is still a password; a value
	// with no closing quote (cut off) is removed to the end of the line.
	regexp.MustCompile(`(?i)(api[_-]?key|passw(?:or)?d|secret[_-]?key|access[_-]?token|private[_-]?key)["']?\s*[=:]\s*` +
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
