package quota

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// EntUsage is what the Enterprise account (the aitch fallback, ~/.claude-work; owner 10-19 14:44) may still spend,
// read live from the usage endpoint with that persona's existing Claude Code login, under the names the owner's
// usage page gives them (Crius NEED 10-19 15:06): the seat's included usage, which expires, and the spend limit
// drawn after it. Read-only: crius never renews or replaces the login, so an expired or missing one is "cannot read"
// (Read false, Reason says why), never 0%.
type EntUsage struct {
	Read     bool       `json:"read"`
	Reason   string     `json:"reason,omitempty"`
	Included Included   `json:"included"`
	Spend    SpendLimit `json:"spend_limit"`
}

// Included is the seat's included usage: dollars used of a limit, until it expires.
type Included struct {
	UsedUSD  float64   `json:"used_usd"`
	LimitUSD float64   `json:"limit_usd"`
	Expires  time.Time `json:"expires"`
	Locked   string    `json:"locked,omitempty"` // its locked_reason: no use until it expires
}

// SpendLimit is the spend limit used after the included usage. The reply gives no reset date for it.
type SpendLimit struct {
	UsedUSD  float64 `json:"used_usd"`
	LimitUSD float64 `json:"limit_usd"`
	Enabled  bool    `json:"enabled"`
	Reached  bool    `json:"reached"`
}

// ErrNoLogin: the persona has no login in the keychain.
var ErrNoLogin = errors.New("no login")

// UsageURL is the endpoint Claude Code's /usage reads with the same login.
const UsageURL = "https://api.anthropic.com/api/oauth/usage"

// limitWords: an object whose JSON holds one of these is about a dollar limit. One this code does not know is a
// reply it does not understand (Crius NEED 15:06): cannot read, never ignored.
var limitWords = []string{"limit", "dollars", "credits", "amount_minor"}

// ReadEntUsage reads the account with the login that login returns (Claude Code's keychain item) through get.
func ReadEntUsage(login func() ([]byte, error), get func(token string) (int, []byte, error), now time.Time) EntUsage {
	no := func(format string, a ...any) EntUsage { return EntUsage{Reason: fmt.Sprintf(format, a...)} }
	b, err := login()
	if errors.Is(err, ErrNoLogin) {
		return no("no login for the enterprise persona in the keychain")
	} else if err != nil {
		return no("login: %v", err)
	}
	var c struct {
		OAuth struct {
			Token   string `json:"accessToken"`
			Expires int64  `json:"expiresAt"` // ms
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return no("login is not JSON")
	}
	if c.OAuth.Token == "" {
		return no("login has no access token")
	}
	// A zero expiry is not known: the endpoint decides. A past one is not renewed here (read-only).
	if exp := time.UnixMilli(c.OAuth.Expires); c.OAuth.Expires != 0 && !exp.After(now) {
		return no("login expired %s; a ~/.claude-work Claude session renews it", day(exp))
	}
	code, body, err := get(c.OAuth.Token)
	switch {
	case err != nil:
		return no("usage endpoint: %v", err)
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return no("login rejected (HTTP %d)", code)
	case code != http.StatusOK:
		return no("usage endpoint HTTP %d", code)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return no("usage reply is not a JSON object")
	}
	// Included usage sits under a code name that may change: it is the one object with a dollar limit, the dollars
	// used and a reset, found by its field names before decoding so one that does not decode is not skipped.
	// spend and extra_usage are known by name. Any other object about a dollar limit: cannot read.
	type included struct {
		Limit  *float64 `json:"limit_dollars"`
		Used   *float64 `json:"used_dollars"`
		Resets *string  `json:"resets_at"`
		Locked *string  `json:"locked_reason"`
	}
	var found []included
	var keys, unknown []string
	for k, raw := range top {
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) == nil {
			_, hasLimit := fields["limit_dollars"]
			_, hasUsed := fields["used_dollars"]
			_, hasResets := fields["resets_at"]
			if hasLimit && hasUsed && hasResets {
				var l included
				if err := json.Unmarshal(raw, &l); err != nil || l.Limit == nil || l.Used == nil || l.Resets == nil {
					return no("included usage %s in the usage reply does not decode", k)
				}
				found, keys = append(found, l), append(keys, k)
				continue
			}
		}
		if k == "spend" || k == "extra_usage" || string(raw) == "null" || string(raw) == "[]" {
			continue
		}
		for _, w := range limitWords {
			if strings.Contains(k, w) || strings.Contains(string(raw), w) {
				unknown = append(unknown, k)
				break
			}
		}
	}
	sort.Strings(keys)
	sort.Strings(unknown)
	switch {
	case len(unknown) > 0:
		return no("unknown limit in the usage reply: %s", strings.Join(unknown, ", "))
	case len(found) == 0:
		return no("no included usage in the usage reply")
	case len(found) > 1:
		return no("%d included usages in the usage reply (%s)", len(found), strings.Join(keys, ", "))
	}
	l := found[0]
	exp, err := time.Parse(time.RFC3339, *l.Resets)
	switch {
	case err != nil:
		return no("included usage expiry %q is not a time", *l.Resets)
	case !exp.After(now):
		return no("included usage expiry %s is already past", day(exp))
	case *l.Limit <= 0 || *l.Used < 0:
		return no("included usage of %v dollars with %v used is not a limit", *l.Limit, *l.Used)
	}
	u := EntUsage{Read: true, Included: Included{UsedUSD: *l.Used, LimitUSD: *l.Limit, Expires: exp}}
	if l.Locked != nil {
		u.Included.Locked = *l.Locked
	}

	// The spend limit: spend gives used and limit with their exponents; extra_usage says whether the limit is reached
	// and repeats the limit in minor units, which must agree.
	type money struct {
		Minor    *int64 `json:"amount_minor"`
		Currency string `json:"currency"`
		Exponent *int   `json:"exponent"`
	}
	var s struct {
		Used    *money `json:"used"`
		Limit   *money `json:"limit"`
		Enabled *bool  `json:"enabled"`
	}
	var x struct {
		Limit   *int64 `json:"monthly_limit"`
		Places  *int   `json:"decimal_places"`
		Reached *bool  `json:"spend_limit_reached"`
	}
	usd := func(m *money) (float64, bool) {
		if m == nil || m.Minor == nil || m.Exponent == nil || m.Currency != "USD" {
			return 0, false
		}
		return float64(*m.Minor) / math.Pow10(*m.Exponent), true
	}
	for k, v := range map[string]any{"spend": &s, "extra_usage": &x} {
		if raw, ok := top[k]; ok && json.Unmarshal(raw, v) != nil {
			return no("spend limit %s in the usage reply does not decode", k)
		}
	}
	used, okU := usd(s.Used)
	limit, okL := usd(s.Limit)
	switch {
	case !okU || !okL || s.Enabled == nil:
		return no("no spend limit in the usage reply (spend.used, spend.limit, spend.enabled in USD)")
	case x.Limit == nil || x.Places == nil || x.Reached == nil:
		return no("no spend limit state in the usage reply (extra_usage)")
	case float64(*x.Limit)/math.Pow10(*x.Places) != limit:
		return no("spend limit disagrees: spend %v dollars, extra_usage %d at %d places", limit, *x.Limit, *x.Places)
	case used < 0 || limit < 0:
		return no("spend limit of %v dollars with %v used is not a limit", limit, used)
	}
	u.Spend = SpendLimit{UsedUSD: used, LimitUSD: limit, Enabled: *s.Enabled, Reached: *x.Reached}
	return u
}

// room says whether each allowance can be drawn on.
func (i Included) room() bool   { return i.Locked == "" && i.UsedUSD < i.LimitUSD }
func (s SpendLimit) room() bool { return s.Enabled && !s.Reached && s.UsedUSD < s.LimitUSD }

// Room is the fallback verdict: "room" while either allowance has room, "full" when neither has, "unknown" when not
// read. The read is live, so no allowance for age as for the Max samples.
func (u EntUsage) Room() (verdict, reason string) {
	switch {
	case !u.Read:
		return "unknown", "cannot read: " + u.Reason
	case u.Included.room() || u.Spend.room():
		return "room", u.nums()
	}
	return "full", u.nums()
}

func (u EntUsage) nums() string {
	i, s := u.Included, u.Spend
	t := fmt.Sprintf("included usage $%.0f of $%.0f used (%.0f%%), expires %s", i.UsedUSD, i.LimitUSD,
		100*i.UsedUSD/i.LimitUSD, day(i.Expires))
	if i.Locked != "" {
		t += ", locked: " + i.Locked
	}
	t += fmt.Sprintf(" · spend limit $%.0f of $%.0f used", s.UsedUSD, s.LimitUSD)
	switch {
	case !s.Enabled:
		t += ", off"
	case s.Reached:
		t += ", reached"
	}
	return t + ", reset date not in the reply"
}

// Text is the line in the `quota` report.
func (u EntUsage) Text() string {
	if !u.Read {
		return "fallback: cannot read (" + u.Reason + ")"
	}
	return "fallback: " + u.nums()
}

// keychainService is the keychain item Claude Code keeps a config dir's login under.
func keychainService(dir string) string {
	h := sha256.Sum256([]byte(dir))
	return "Claude Code-credentials-" + hex.EncodeToString(h[:])[:8]
}

// KeychainLogin returns the login of the Claude Code config dir from the macOS keychain, read-only.
func KeychainLogin(dir string) func() ([]byte, error) {
	return func() ([]byte, error) {
		out, err := exec.Command("security", "find-generic-password", "-s", keychainService(dir), "-w").Output()
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 44 { // errSecItemNotFound
			return nil, ErrNoLogin
		}
		if err != nil {
			return nil, fmt.Errorf("security find-generic-password: %w", err)
		}
		return out, nil
	}
}

// EntBudget bounds the whole read (keychain and endpoint): aitch gives `quota --room` 3 s in all, and the Max
// verdict must not be lost to a slow fallback read (codex review of 3b).
const EntBudget = 1500 * time.Millisecond

// WithDeadline is read's answer, or "cannot read" when it takes over d.
func WithDeadline(read func() EntUsage, d time.Duration) EntUsage {
	ch := make(chan EntUsage, 1)
	go func() { ch <- read() }()
	select {
	case m := <-ch:
		return m
	case <-time.After(d):
		return EntUsage{Reason: fmt.Sprintf("the read took over %v", d)}
	}
}

// usageClient speaks HTTP/1.1 only: Go's HTTP/2 debug log (GODEBUG=http2debug) prints every request header, the
// token among them (codex review of 3b). A fresh TLS config: the default transport's offers h2 by ALPN, which the
// server then speaks even with Protocols set to HTTP/1 only (seen live 10-19).
var usageClient = func() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{}
	tr.Protocols = new(http.Protocols)
	tr.Protocols.SetHTTP1(true)
	return &http.Client{Timeout: EntBudget, Transport: tr}
}()

// UsageGet asks the usage endpoint with the token.
func UsageGet(token string) (int, []byte, error) {
	req, err := http.NewRequest(http.MethodGet, UsageURL, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	resp, err := usageClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, body, err
}
