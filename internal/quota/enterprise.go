package quota

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// Monthly is the Enterprise account's monthly spend limit (the aitch fallback, ~/.claude-work; owner 10-19 14:44),
// read live from the usage endpoint with that persona's existing Claude Code login. Read-only: crius never renews
// or replaces the login, so an expired or missing one is "cannot read" (Read false, Reason says why), never 0%.
type Monthly struct {
	Read     bool      `json:"read"`
	Reason   string    `json:"reason,omitempty"`
	UsedUSD  float64   `json:"used_usd"`
	LimitUSD float64   `json:"limit_usd"`
	Reset    time.Time `json:"reset"`
	Locked   string    `json:"locked,omitempty"` // the account's locked_reason: no use until the reset
}

// ErrNoLogin: the persona has no login in the keychain.
var ErrNoLogin = errors.New("no login")

// UsageURL is the endpoint Claude Code's /usage reads with the same login.
const UsageURL = "https://api.anthropic.com/api/oauth/usage"

// ReadMonthly reads the month with the login that login returns (Claude Code's keychain item) through get.
func ReadMonthly(login func() ([]byte, error), get func(token string) (int, []byte, error), now time.Time) Monthly {
	no := func(format string, a ...any) Monthly { return Monthly{Reason: fmt.Sprintf(format, a...)} }
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
	// The limit's key is a code name that may change: it is the one object with a dollar limit, the dollars used and
	// a reset. None, or more than one, is a reply this code does not know: cannot read, never a guess.
	type limit struct {
		Limit  *float64 `json:"limit_dollars"`
		Used   *float64 `json:"used_dollars"`
		Resets *string  `json:"resets_at"`
		Locked *string  `json:"locked_reason"`
	}
	var found []limit
	var keys []string
	for k, raw := range top {
		var l limit
		if json.Unmarshal(raw, &l) == nil && l.Limit != nil && l.Used != nil && l.Resets != nil {
			found, keys = append(found, l), append(keys, k)
		}
	}
	switch len(found) {
	case 0:
		return no("no monthly limit in the usage reply")
	case 1:
	default:
		sort.Strings(keys)
		return no("%d monthly limits in the usage reply (%s)", len(found), strings.Join(keys, ", "))
	}
	l := found[0]
	reset, err := time.Parse(time.RFC3339, *l.Resets)
	switch {
	case err != nil:
		return no("monthly limit reset %q is not a time", *l.Resets)
	case !reset.After(now):
		return no("monthly limit reset %s is already past", day(reset))
	case *l.Limit <= 0 || *l.Used < 0:
		return no("monthly limit of %v dollars with %v used is not a limit", *l.Limit, *l.Used)
	}
	m := Monthly{Read: true, UsedUSD: *l.Used, LimitUSD: *l.Limit, Reset: reset}
	if l.Locked != nil {
		m.Locked = *l.Locked
	}
	return m
}

// Room is the fallback verdict: "room" below the limit, "full" at it or while locked, "unknown" when not read.
// The read is live, so no allowance for age as for the Max samples.
func (m Monthly) Room() (verdict, reason string) {
	switch {
	case !m.Read:
		return "unknown", "cannot read: " + m.Reason
	case m.Locked != "":
		return "full", m.nums() + ", locked: " + m.Locked
	case m.UsedUSD >= m.LimitUSD:
		return "full", m.nums()
	}
	return "room", m.nums()
}

func (m Monthly) nums() string {
	return fmt.Sprintf("$%.0f of $%.0f used (%.0f%%), resets %s", m.UsedUSD, m.LimitUSD, 100*m.UsedUSD/m.LimitUSD,
		day(m.Reset))
}

// Text is the line in the `quota` report.
func (m Monthly) Text() string {
	if !m.Read {
		return "monthly limit: cannot read (" + m.Reason + ")"
	}
	_, reason := m.Room()
	return "monthly limit: " + reason
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

// UsageGet asks the usage endpoint with the token.
func UsageGet(token string) (int, []byte, error) {
	req, err := http.NewRequest(http.MethodGet, UsageURL, nil)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, body, err
}
