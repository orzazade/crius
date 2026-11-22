package redact

// The redaction of a run log (crius-runlog, 2026-11-23: a `sh -c "… GH_TOKEN=<token> gh run view …"` was saved in
// ~/.crius/runs). It uses the rules of this package, but a run log is evidence, so it keeps the name of a secret and the
// rest of the command, and leaves alone what only looks like a key: a git sha, a run id, a file:line, a test count. The
// generic gitleaks rule is off for that reason: a secret is known by its name or by its shape.

import (
	"bytes"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
)

// Mark stands for a secret in a run log.
const Mark = "<redacted>"

// logKey is secretKey plus the key names a run log meets: environment variables whose secret word is not the last one
// (GH_TOKEN_PROD, DB_PASS_2, TOKENS), and the known *_KEY names. A bare "key" is no secret's name (a cache key, a sort key).
const logKey = `(?:` + secretKey + `|(?-i:TOKENS?(?:_[A-Z0-9_]*)?\b|_(?:PASS|PWD|PAT|AUTH)(?:_[A-Z0-9_]*)?\b)|` +
	`(?:access|account|encryption|signing|master|storage|subscription|openai|anthropic|cosmos|deploy|sas|webhook|hmac|client|` +
	`auth|app|service|functions?|shared|admin|root|session|jwt)[_-]?key|shared[_-]?access[_-]?signature|[_-]pat\b|` +
	`[a-z0-9](?:pass|pwd)\b|__key\b|oauth2-bearer\b)`

// lastKey are names that are a secret's only where the name ends with them: pass-rate and credential.helper are none. The
// last two are what Kubernetes calls the value of a TLS and of a registry secret.
const lastKey = `(?:\bpass|credentials?|creds|\b(?:tls|ca|server|client|ssl)\.key|\.docker(?:configjson|cfg))`

// logPairRe is a secret-named key and its operator; the last group marks where the value starts. A scheme
// (Authorization: Bearer \u2026) is part of the value here: it goes with it. The name may be an index: x["TOKEN"] = .
var logPairRe = regexp.MustCompile(`(?i)(?:` + logKey + `[\w.-]*|` + lastKey + `)\\*(?:["']\\*\]?)?([ \t]*)(` + operator + `)([ \t]*)()`)

// A secret-named flag (the group before the last) and the space before its value, on a command line.
var logFlagRe = regexp.MustCompile(`(?i)` + flagStart + `(--?[\w-]*` + logKey + `[\w-]*)(?:[ \t]|\\\r?\n)+()`)

// A whole argument that is a secret-named flag: the next argument is its value.
var flagArgRe = regexp.MustCompile(`(?i)^--?[\w-]*` + logKey + `[\w-]*$`)

// notSecretName is a name that holds a secret word and not the secret: TOKEN_FILE, --password-stdin, BYPASS, OLDPWD.
var notSecretName = regexp.MustCompile(`(?i)(?:[_-](?:file|path|dir|count|name|length|type|stdin)|(?:^|[_.-])(?:bypass|compass|encompass|surpass|trespass|overpass|underpass|oldpwd))\\*(?:["']\\*\]?)?$`)

// name:pass behind a user flag; ids in digits (docker's 1000:1000) are no password. The name is in the first group set,
// the password in the next: to the quote that closes, else to the next blank. curlUserRe is for text that names curl, which
// takes a cluster and a glued value too (-su bob:pass, -ubob:pass).
const nameAndPass = `(?:"([^\s:"=+]*):([^"\n]*)|'([^\s:'=+]*):([^'\n]*)|([^\s:"'=+]*):([^\s]+))`

var (
	userRe     = regexp.MustCompile(flagStart + `(?:-u|--user|--proxy-user)(?:[ \t]+|=)` + nameAndPass)
	curlUserRe = regexp.MustCompile(flagStart + `-[a-zA-Z]*?[uU][ \t]*` + nameAndPass)
	curlRe     = regexp.MustCompile(`\bcurl\b`)
	userFlag   = regexp.MustCompile(`^(?:-u|--user|--proxy-user)$`)
)

// spanRes are secrets whose end is certain wherever they stand; the last group is the secret, its absence the match.
// Token shapes have no \b before them: a token after an escaped newline (\nghp_\u2026) has a letter there.
var spanRes = []*regexp.Regexp{
	regexp.MustCompile(`(?i)[?&](?:[\w.-]*` + logKey + `[\w.-]*|sig|signature)=([^&\s"'\\<>]+)`),
	regexp.MustCompile(`(?i)[?&]code=([A-Za-z0-9%+/=_-]{20,})`),
	regexp.MustCompile(`(?i)\bsig=([A-Za-z0-9%+/=_-]{16,})`),
	regexp.MustCompile(`(?i)\b(?:pwd|password|accountkey|sharedaccesskey|sharedaccesssignature|accesskey)[ \t]*=[ \t]*([^;\s"'<>]+);[ \t]*(?:[A-Za-z][\w ]*=|["'\r]|$)`),
	regexp.MustCompile(`(?i);[ \t]*(?:pwd|password|accountkey|sharedaccesskey|sharedaccesssignature|accesskey)[ \t]*=[ \t]*("[^"\n]*"|'[^'\n]*'|\{[^}\n]*\}|[^;\n"']+)`),
	// A name and its value in fields of their own: app settings, an env list, web.config.
	regexp.MustCompile(`(?i)"(?:name|key)"\s*:\s*"[^"]*` + logKey + `[^"]*"[^{}]*?"value"\s*:\s*"((?:[^"\\\n]|\\.)*)"`),
	regexp.MustCompile(`(?i)\b(?:name|key)="[^"]*` + logKey + `[^"]*"[^<>]*?\bvalue="([^"\n]*)"`),
	regexp.MustCompile(`://([^/\s:@"']{30,})@`),
	regexp.MustCompile(`(?i)\b(?:set-)?cookie["']?:[ \t]*["']?[^\s"'=;]+=([^\n"'\r]+)`),
	regexp.MustCompile(`gh[opsur]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|xox[a-z]-[A-Za-z0-9-]{10,}|(?:AKIA|ASIA)[0-9A-Z]{16}`),
	regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}(?:\.[A-Za-z0-9_-]*){2,4}`),
}

// The same name and value on two lines: "name": "X_PASSWORD", then "value": \u2026.
var (
	nameLine  = regexp.MustCompile(`(?i)^\s*(?:- )?["']?(?:name|key)["']?\s*:\s*["']?[\w.:-]*` + logKey + `[\w.-]*["']?,?\s*$`)
	valueLine = regexp.MustCompile(`(?i)^\s*["']?value["']?\s*:\s*()`)
)

// logShortFlags are the flags that hold a password or a secret only behind what takes one, in one command: -p is a parallel
// count, a project or "make parents" elsewhere (go test -p 2 ./internal/mysql), --value and --body are any text. The flag
// may be the first argument or a later one. The group marks the value.
const (
	oneCommand = `(?:[^\n;|&]|\\\r?\n)*?`
	laterFlag  = `(?:["'\s\\]` + oneCommand + `)?[\s"',]`
)

var logShortFlags = []*regexp.Regexp{
	regexp.MustCompile(`(?:^|[\s;&|("'` + "`" + `]|/bin/)(?:mysql(?:dump|admin|import|show|check|sh)?|mariadb(?:-\w+)?|mongo(?:sh|dump|restore|export|import)?|sshpass|` +
		`(?:docker|podman|nerdctl|buildah|skopeo|helm|oras|oc|az|cf)\b` + oneCommand + `\blogin)` + laterFlag + `-p(?:[ \t]+|=)?()[^\s=]`),
	regexp.MustCompile(`\bredis-cli` + oneCommand + `[ \t](?:-a[ \t]*|--pass[ \t=]+)()[^\s]`),
	regexp.MustCompile(`\bsecret[\s"',]+set\b` + laterFlag + `(?:--value|--body|-b)(?:[\s"',=]|\\\r?\n)*()[^\s=]`),
	regexp.MustCompile(`\bsecret` + laterFlag + `--from-literal[\s"',=]+[^\s=]+=()\S`),
}

// A port map of a container is no password: docker run --name mysql -p 3306:3306.
var (
	containerRun = regexp.MustCompile(`\b(?:docker|podman|nerdctl)\b` + oneCommand + `\b(?:run|create)\b`)
	portMap      = regexp.MustCompile(`(?m)[ \t]-p[ \t=](?:[\d.]+:)?\d+:\d+(?:/(?:tcp|udp))?(?:[ \t]|$)`)
)

var (
	// A source file before its line or message (token.go:12:5). A data file counts only before a line number.
	sourceFile = regexp.MustCompile(`(?i)\.(?:go|tsx?|jsx?|mjs|cjs|cs|csproj|sln|props|targets|razor|cshtml|fs|vb|py|rs|java|kt|swift|rb|php|lua|sh|sql|c|h|cpp|hpp|proto|tf|html?|css|scss|vue|svelte|md)$`)
	dataFile   = regexp.MustCompile(`(?i)\.(?:json|ya?ml|toml|xml|lock|snap)$`)
	lineNumber = regexp.MustCompile(`^\d+(?::|\b)`)
	// A count or a result behind a name that holds a secret word: secrets: 0, unit-pass: 12, Check secrets: success.
	lastFile    = regexp.MustCompile(`\.(?:key|dockerconfigjson|dockercfg)$`)
	credsName   = regexp.MustCompile(`cred(?:ential)?s?$`)
	pathValue   = regexp.MustCompile(`^["']?(?:~|\.{1,2})?/\S*\.\w+["']?(?:$|[\s;&|)])`)
	countName   = regexp.MustCompile(`(?i)(?:secrets|tokens|(?:tests?|unit|integration|e2e|smoke|check|suite|qa)[-_.]?pass)$`)
	countValue  = regexp.MustCompile(`^\d{1,9}(?:$|[\s,;)\]}])`)
	resultValue = regexp.MustCompile(`(?i)^(?:pass(?:ed)?|fail(?:ed)?|ok|success|succeeded|skipped|true|false|null|none|nil|required|missing|expired|invalid)[ \t]*(?:$|[,;)\]}\r])`)
	goTestLine  = regexp.MustCompile(`^\s*(?:--- (?:PASS|FAIL|SKIP): |=== [A-Z]+ +)\S+(?: \(\d+\.\d+s\))?\s*$`)
	notASecret  = regexp.MustCompile(`^(?:[0-9a-f]{7,40}|[0-9]+|[0-9a-fA-F]{8}(?:-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12})$`)
	masked      = `(?:(?:basic|bearer|token)[ \t]+)?(?:\*{3,}|` + Mark + `|\[redacted\])`
	maskedValue = regexp.MustCompile(`(?i)^(?:"` + masked + `"|'` + masked + `'|` + masked + `(?:$|[\s,;)\]}&\\"']))`)
	logRules    = withoutGeneric(rules)
)

func withoutGeneric(rs []rule) (out []rule) {
	for _, r := range rs {
		if r.id != "generic-api-key" {
			out = append(out, r)
		}
	}
	return out
}

// shapes replaces what is a secret by its own form, wherever it stands.
func shapes(s string) string {
	for _, re := range spanRes {
		s = replaceGroup(s, re)
	}
	s = userInfoRe.ReplaceAllStringFunc(s, func(m string) string { return m[:strings.Index(m, "://")+3] + Mark + "@" })
	for _, re := range shapeRes {
		s = re.ReplaceAllString(s, Mark)
	}
	s, _ = redactRulesAs(s, logRules, Mark, func(v string) bool { return !notASecret.MatchString(v) && v != Mark })
	return s
}

// replaceGroup replaces re's last group in s by Mark, the whole match when it has none.
func replaceGroup(s string, re *regexp.Regexp) string {
	ms := re.FindAllStringSubmatchIndex(s, -1)
	if ms == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, m := range ms {
		lo, hi := m[len(m)-2], m[len(m)-1]
		if lo == hi || maskedValue.MatchString(s[lo:hi]) {
			continue
		}
		b.WriteString(s[last:lo] + Mark)
		last = hi
	}
	return b.String() + s[last:]
}

// pair is a secret-named key and its value: where the value starts and its operator. adjacent says no space stands beside
// the operator, empty that it is NAME= with a space or nothing behind it: an empty value where a shell reads it.
type pair struct {
	v               int
	op              string
	adjacent, empty bool
}

// nextPair is the next pair in s from pos, less what is no key or no secret: a source file and its line (token.go:12), a
// path through :: (test_token.py::test_x), an assembly's public key token, a count, a result, a value masked already.
func nextPair(s string, pos int) (p pair, ok bool) {
	for {
		m := logPairRe.FindStringSubmatchIndex(s[pos:])
		if m == nil {
			return pair{}, false
		}
		// The operator, its spaces and the value's start are the last four groups.
		g := m[len(m)-8:]
		v := pos + m[len(m)-2]
		p = pair{v: v, op: s[pos+g[2] : pos+g[3]], adjacent: g[0] == g[1] && g[4] == g[5]}
		p.empty = p.op == "=" && g[0] == g[1] && (g[4] < g[5] || v == len(s))
		start := pos + m[0]
		for start > pos && wordByte(s[start-1]) {
			start--
		}
		name, rest := s[start:pos+g[0]], s[v:]
		last := strings.ToLower(strings.TrimRight(name, `\"']`))
		switch {
		case p.op == ":" && strings.HasPrefix(rest, ":"):
		case p.op == ":" && sourceFile.MatchString(name):
		case p.op == ":" && dataFile.MatchString(name) && lineNumber.MatchString(rest):
		case strings.HasSuffix(name, "PublicKeyToken"):
		case notSecretName.MatchString(name):
		case countName.MatchString(name) && countValue.MatchString(rest):
		case p.op == ":" && resultValue.MatchString(rest):
		case maskedValue.MatchString(rest):
		case last == "pass" && (p.op != "=" || !p.adjacent && (rest == "" || rest[0] != '"' && rest[0] != '\'') || countValue.MatchString(rest) || resultValue.MatchString(rest) || strings.HasPrefix(rest, "$((")): // a test count
		case lastFile.MatchString(last) && (p.op != ":" || countValue.MatchString(rest)): // --from-file=tls.key=x; tls.key: 1704 bytes
		case credsName.MatchString(last) && pathValue.MatchString(rest): // GOOGLE_APPLICATION_CREDENTIALS=/a/key.json
		default:
			return p, true
		}
		pos = v
	}
}

func wordByte(c byte) bool {
	return c == '_' || c == '-' || c == '.' || '0' <= c && c <= '9' || 'a' <= c|0x20 && c|0x20 <= 'z'
}

// lines redacts a text line by line, keeping what a later line needs to know: an open private key block, a value that
// goes on below its key, a name whose value comes on its own line.
type lines struct {
	block  int  // lines of a private key block still expected, up to its END
	below  bool // the next line belongs to the key above, whatever its indent
	inKey  bool // lines indented deeper than indent belong to it too
	indent int
	named  int // lines in which the value of a secret name may come
}

var (
	blockBegin = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY(?: BLOCK)?-----`)
	blockEnd   = regexp.MustCompile(`-----END [A-Z ]*PRIVATE KEY(?: BLOCK)?-----`)
	blockWhole = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY(?: BLOCK)?-----.*?(?:-----END [A-Z ]*PRIVATE KEY(?: BLOCK)?-----|$)`)
)

// maxBlock is the most lines a key block takes (a 16384-bit key has about 200), so a BEGIN with no END, in a message
// about a bad key, does not take the rest of the log.
const maxBlock = 300

// redact gives line l (no newline in it) as a run log keeps it. A line that is all secret is a Mark: no line is lost.
func (st *lines) redact(l string) string {
	if st.block > 0 {
		st.block--
		m := blockEnd.FindStringIndex(l)
		if m == nil {
			return Mark
		}
		st.block = 0
		if strings.Trim(l[m[1]:], " \t\r\"',;") == "" {
			return Mark
		}
		return Mark + st.redact(l[m[1]:])
	}
	if st.below || st.inKey {
		t := strings.TrimLeft(l, " \t")
		if strings.TrimSpace(l) == "" {
			return l
		}
		d := len(l) - len(t)
		if st.below || d > st.indent || d == st.indent && (strings.HasPrefix(t, "- ") || strings.TrimSpace(t) == "-") {
			st.below = false
			st.blocks(l) // a key on this line ("secrets.yaml: password:" over "password:") may go on below too
			return l[:d] + Mark
		}
		st.inKey = false
	}
	return st.blocks(l)
}

// blocks is line with the private key blocks of l replaced. One that l opens at its end goes on below; a BEGIN with more
// text behind it takes the rest of the line only.
func (st *lines) blocks(l string) string {
	var b strings.Builder
	for {
		m := blockBegin.FindStringIndex(l)
		if m == nil {
			return b.String() + st.line(l)
		}
		b.WriteString(st.line(l[:m[0]]) + Mark)
		st.below, st.inKey = false, false // the block is the value
		e := blockEnd.FindStringIndex(l[m[1]:])
		if e == nil {
			if strings.Trim(l[m[1]:], " \t\r\"'\\") == "" {
				st.block = maxBlock
			}
			return b.String()
		}
		l = l[m[1]+e[1]:]
	}
}

func (st *lines) line(l string) string {
	l = shapes(l) // certain ends first: what they replace is masked for the rules that run to the end of the line
	if goTestLine.MatchString(l) {
		return l // a test's name is no key and value
	}
	body := strings.TrimRight(l, "\r")
	cr := l[len(body):]
	for _, m := range logFlagRe.FindAllStringSubmatchIndex(body, -1) {
		v := m[len(m)-1]
		if v < len(body) && !maskedValue.MatchString(body[v:]) && !notSecretName.MatchString(body[m[2]:m[3]]) {
			body = body[:v] + Mark
			break
		}
	}
	body = userPass(shortFlag(body))
	if st.named > 0 {
		st.named--
		if m := valueLine.FindStringIndex(body); m != nil {
			st.named = 0
			if rest := strings.Trim(body[m[1]:], "\"', \t"); rest != "" && !maskedValue.MatchString(body[m[1]:]) {
				return body[:m[1]] + Mark + cr
			}
		}
	}
	if nameLine.MatchString(body) {
		st.named = 4
	}
	return st.pairs(body) + cr
}

// shortFlag replaces the value of a client's short password flag, to the end of the line.
func shortFlag(s string) string {
	seen := s
	if containerRun.MatchString(s) {
		seen = portMap.ReplaceAllStringFunc(s, func(m string) string { return strings.Repeat(" ", len(m)) })
	}
	for _, re := range logShortFlags {
		if m := re.FindStringSubmatchIndex(seen); m != nil {
			seen, s = seen[:m[2]], s[:m[2]]+Mark
		}
	}
	return s
}

// userPass replaces the password of -u name:password.
func userPass(s string) string {
	s = userPassBy(s, userRe)
	if curlRe.MatchString(s) {
		s = userPassBy(s, curlUserRe)
	}
	return s
}

func userPassBy(s string, re *regexp.Regexp) string {
	var b strings.Builder
	pos := 0
	for _, m := range re.FindAllStringSubmatchIndex(s, -1) {
		for g := 2; g < len(m); g += 4 {
			if m[g] >= 0 && isPass(s[m[g]:m[g+1]], s[m[g+2]:m[g+3]]) {
				b.WriteString(s[pos:m[g+2]] + Mark)
				pos = m[g+3]
			}
		}
	}
	return b.String() + s[pos:]
}

// isPass says whether pass behind name: is a password: not ids (1000:1000), a path (host:/x), a URL or a format (+%H:%M).
func isPass(name, pass string) bool {
	return strings.Trim(name+pass, "0123456789") != "" && pass != "" && !strings.HasPrefix(pass, "/") && !strings.HasPrefix(pass, Mark) &&
		!strings.HasPrefix(name, "+")
}

// pairs replaces the value of each secret-named key in a line: to its closing quote where that is certain (quotedEnd),
// else to the end of the line. Over-redacting is accepted; printing part of a secret is not.
func (st *lines) pairs(l string) string {
	var b strings.Builder
	pos := 0
	for {
		p, ok := nextPair(l, pos)
		if !ok || p.empty && p.adjacent && p.v == len(l) { // NAME= and nothing: an empty value
			return b.String() + l[pos:]
		}
		if end, ok := quotedEnd(l, p.v, len(l)); ok {
			b.WriteString(l[pos:p.v] + Mark)
			pos = end
			continue
		}
		if rest := strings.TrimRight(l[p.v:], " \t\r"); rest == "\\" || blockStart.MatchString(rest) {
			st.below, st.inKey = true, true
			st.indent = len(l) - len(strings.TrimLeft(l, " \t"))
		}
		b.WriteString(l[pos:p.v])
		if p.v < len(l) {
			b.WriteString(Mark)
		}
		return b.String()
	}
}

// Log gives output text as a run log keeps it.
func Log(s string) string {
	var b bytes.Buffer
	w := NewStream(&b)
	io.WriteString(w, s)
	if err := w.Close(); err != nil {
		panic(err)
	}
	return b.String()
}

// LeftOutNote is the note an over-long line ends on.
var LeftOutNote = regexp.MustCompile(Mark + ` \(\d+ bytes of a line over \d+ bytes left out\)$`)

// A line over maxLine bytes is redacted up to there and the rest of it is dropped with a note: what stands after the
// cut cannot be judged. The last lineGuard bytes before the cut go too, as a secret may start there.
const (
	maxLine   = 1 << 20
	lineGuard = 1 << 16
)

// Stream redacts what is written to it as Log does and hands it to W a line at a time, so a secret in two writes is
// whole when the rules see it. Close writes a last line with no newline. When a rule fails, nothing more is written and
// Close gives the error; Write never fails for it, so a command writing to it runs on.
type Stream struct {
	W       io.Writer
	Line    func(line string) string // the rules
	buf     []byte
	dropped int64 // bytes of an over-long line left out so far
	LeftOut int64 // bytes of over-long lines left out, less the notes that say so
	err     error
}

func NewStream(w io.Writer) *Stream { return &Stream{W: w, Line: (&lines{}).redact} }

func (s *Stream) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 && s.err == nil {
		i := bytes.IndexByte(p, '\n')
		part := p
		if i >= 0 {
			part = p[:i]
		}
		k := min(len(part), max(0, maxLine-len(s.buf)))
		s.buf = append(s.buf, part[:k]...)
		s.dropped += int64(len(part) - k)
		if i < 0 {
			break
		}
		s.flush("\n")
		p = p[i+1:]
	}
	return n, nil
}

func (s *Stream) flush(nl string) {
	defer func() {
		if r := recover(); r != nil {
			s.err = fmt.Errorf("redaction failed: %v", r)
		}
		s.buf, s.dropped = s.buf[:0], 0
	}()
	l := s.Line(string(s.buf))
	if s.dropped > 0 {
		kept := strings.ToValidUTF8(l[:max(0, len(l)-lineGuard)], "")
		left := s.dropped + int64(len(l)-len(kept))
		note := fmt.Sprintf("%s (%d bytes of a line over %d bytes left out)", Mark, left, maxLine)
		l = kept + note
		s.LeftOut += left - int64(len(note))
	}
	_, s.err = io.WriteString(s.W, l+nl)
}

// Close writes the last line when no newline ended it.
func (s *Stream) Close() error {
	if s.err == nil && len(s.buf)+int(s.dropped) > 0 {
		s.flush("")
	}
	return s.err
}

// Command gives a command's arguments, joined by spaces, as a run log keeps them. An argument behind -c is a shell
// script, where a value ends with its word; any other argument that assigns a secret is taken to its end.
func Command(argv []string) string {
	out := make([]string, len(argv))
	curl := false
	for i, a := range argv {
		prev := ""
		if i > 0 {
			prev = argv[i-1]
			curl = curl || path.Base(prev) == "curl"
		}
		user, pass, colon := strings.Cut(a, ":")
		glued := curl && curlGlued.MatchString(a)
		switch {
		case flagArgRe.MatchString(prev) && !notSecretName.MatchString(prev) && !strings.HasPrefix(a, "--"):
			out[i] = Mark
		case opensslPass.MatchString(prev) && user == "pass" && pass != "":
			out[i] = "pass:" + Mark
		case (glued || userFlag.MatchString(prev) || curl && curlUser.MatchString(prev)) && colon && isPass(user, pass):
			out[i] = user + ":" + Mark
		default:
			out[i] = script(a, scriptFlag.MatchString(prev))
		}
	}
	return shortFlag(strings.Join(out, " ")) // a short flag needs its client's name, which is another argument
}

var (
	opensslPass = regexp.MustCompile(`^-pass(?:in|out)?$`)
	scriptFlag  = regexp.MustCompile(`^-[a-zA-Z]*c$`)
	curlUser    = regexp.MustCompile(`^-[a-zA-Z]*[uU]$`)
	curlGlued   = regexp.MustCompile(`^-[a-zA-Z]*?[uU][^:]*:.`)
)

// Script is Command for a command written as one shell line.
func Script(s string) string { return Command([]string{"sh", "-c", s})[len("sh -c "):] }

// script redacts one argument. In a shell script (sh) the value of NAME=value ends with its word, or, where the key
// stands in quotes, with those quotes; a value after a colon or spaces, and any in a script with a here-document, ends
// with its line. Each search starts where the last one ended: linear.
func script(s string, sh bool) string {
	s = userPass(shapes(blockWhole.ReplaceAllString(s, Mark)))
	var b strings.Builder
	pos := 0
	here := strings.Contains(s, "<<")
	q := quoting{s: s}
	p, pair := nextPair(s, 0)
	flag, isFlag := nextFlag(s, 0)
	for {
		if pair && p.v < pos {
			p, pair = nextPair(s, pos)
		}
		if isFlag && flag < pos {
			flag, isFlag = nextFlag(s, pos)
		}
		use := p
		if isFlag && (!pair || flag < p.v) {
			use = pairAt(flag)
		} else if !pair {
			return b.String() + s[pos:]
		}
		v := use.v
		end := len(s)
		if sh && here {
			end = valueEnd(s, v)
		} else if e, ok := quotedEnd(s, v, len(s)); !sh && ok {
			end = e
		} else if sh {
			start, stop := q.at(v)
			switch {
			case start == v && use.empty:
				end = v
			case start == v && (!use.adjacent || use.op != "="):
				end = valueEnd(s, v)
			case start == v:
				end = wordEnd(s, v)
			case s[start] == '#':
				end = stop
			default: // in quotes: a quoted value inside them, else up to their close and what the word has behind it
				end = stop
				if closed := stop - 1; s[closed] == '"' || s[closed] == '\'' {
					end = closed
				}
				if e, ok := quotedEnd(s, v, end); ok {
					end = e
				} else if stop < len(s) && strings.IndexByte(" \t\r\n;|&<>)]}", s[stop]) < 0 {
					end = wordEnd(s, stop)
				}
			}
		}
		b.WriteString(s[pos:v])
		if strings.TrimSpace(s[v:end]) != "" {
			b.WriteString(Mark)
		} else {
			b.WriteString(s[v:end])
		}
		if pos = end; end == v {
			if v == len(s) {
				return b.String()
			}
			b.WriteByte(s[v])
			pos++
		}
	}
}

func pairAt(v int) pair { return pair{v: v, op: "=", adjacent: true} }

// nextFlag is where the value of the next secret-named flag in s starts, from pos on.
func nextFlag(s string, pos int) (int, bool) {
	for {
		m := logFlagRe.FindStringSubmatchIndex(s[pos:])
		if m == nil || pos+m[len(m)-1] == len(s) {
			return 0, false
		}
		v := pos + m[len(m)-1]
		if !maskedValue.MatchString(s[v:]) && !notSecretName.MatchString(s[pos+m[2]:pos+m[3]]) {
			return v, true
		}
		pos = v - 1 // the blank before the value may be the next flag's
		if s[v-1] == '\n' {
			pos = v
		}
	}
}

// quoting walks a shell script, so that at gives the quoted piece (or comment) a byte stands in. It is asked in order.
type quoting struct {
	s           string
	start, stop int // the piece last read
}

// at gives the piece byte v stands in; start is v when it stands in none.
func (q *quoting) at(v int) (start, stop int) {
	for q.stop <= v {
		i := q.stop
		if i == v {
			return v, v
		}
		q.start, q.stop = i, i+1
		switch c := q.s[i]; {
		case c == '\\':
			q.stop = min(i+2, len(q.s))
			if q.stop > v {
				return v, v
			}
		case c == '\'' || c == '"' || c == '$' && strings.HasPrefix(q.s[i:], "$'"):
			q.stop = piece(q.s, i)
		case c == '#' && (i == 0 || strings.IndexByte(" \t\n;|&(", q.s[i-1]) >= 0):
			q.stop = lineEnd(q.s, i)
		}
	}
	return q.start, q.stop
}

// piece is where the shell piece that opens at i ends: a quoted string, a substitution, an escape, or one byte.
func piece(s string, i int) int {
	rest := s[i:]
	switch {
	case rest[0] == '\\':
		return min(i+2, len(s))
	case rest[0] == '\'':
		return until(s, i+1, '\'', false)
	case strings.HasPrefix(rest, "$'"):
		return until(s, i+2, '\'', true)
	case rest[0] == '`':
		return until(s, i+1, '`', true)
	case rest[0] == '"':
		for i++; i < len(s) && s[i] != '"'; {
			if s[i] == '\\' || s[i] == '`' || strings.HasPrefix(s[i:], "$(") {
				i = piece(s, i)
			} else {
				i++
			}
		}
		return min(i+1, len(s))
	case rest[0] == '(' || strings.HasPrefix(rest, "$("):
		for i += 1 + strings.IndexByte(rest, '('); i < len(s) && s[i] != ')'; {
			i = piece(s, i)
		}
		return min(i+1, len(s))
	}
	return i + 1
}

// until is past the next c in s from i, or the end of s; a backslash escapes when esc.
func until(s string, i int, c byte, esc bool) int {
	for ; i < len(s); i++ {
		if esc && s[i] == '\\' {
			i++
		} else if s[i] == c {
			return i + 1
		}
	}
	return len(s)
}

// wordEnd is where the shell word at i ends: at a blank or an operator outside quotes and substitutions.
func wordEnd(s string, i int) int {
	for i < len(s) && strings.IndexByte(" \t\r\n;|&<>)", s[i]) < 0 {
		i = piece(s, i)
	}
	return i
}
