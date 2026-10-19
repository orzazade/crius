package quota

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var monthNow = time.Date(2026, 10, 19, 15, 0, 0, 0, time.UTC)

func login(expires time.Time) func() ([]byte, error) {
	return func() ([]byte, error) {
		return []byte(fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"tok","expiresAt":%d}}`, expires.UnixMilli())), nil
	}
}

// The usage reply as read on 2026-10-19 with the ~/.claude-work login, trimmed: the monthly limit is the one object
// with limit_dollars, used_dollars and resets_at; extra_usage is the separate credits pool.
const usageReply = `{"five_hour":null,"seven_day":null,"copper_kite":{"utilization":35.34724005,` +
	`"resets_at":"2026-11-14T18:28:47.775850+00:00","limit_dollars":2000,"used_dollars":706.944801,` +
	`"remaining_dollars":1293.055199,"locked_reason":null},"brass_thimble":null,` +
	`"extra_usage":{"is_enabled":true,"monthly_limit":60000,"used_credits":0.0,"utilization":null}}`

func reply(code int, body string) func(string) (int, []byte, error) {
	return func(tok string) (int, []byte, error) {
		if tok != "tok" {
			return 0, nil, fmt.Errorf("token %q", tok)
		}
		return code, []byte(body), nil
	}
}

func noCall(t *testing.T) func(string) (int, []byte, error) {
	return func(string) (int, []byte, error) {
		t.Fatal("usage endpoint called")
		return 0, nil, nil
	}
}

// cannotRead: not read, a reason naming why, no room verdict, and nothing that reads as a share used.
func cannotRead(t *testing.T, m Monthly, why string) {
	t.Helper()
	v, reason := m.Room()
	if m.Read || !strings.Contains(m.Reason, why) || v != "unknown" || !strings.HasPrefix(reason, "cannot read") ||
		!strings.Contains(m.Text(), "cannot read") || strings.Contains(m.Text(), "%") || strings.Contains(m.Text(), "$") {
		t.Fatalf("%+v · room %s %q · text %q", m, v, reason, m.Text())
	}
}

// Owner 10-19 14:44: the Enterprise login expires ~10-21; an expired or missing login is a loud "cannot read", never
// 0%, and crius never renews it (read-only), so the endpoint is not even asked.
func TestMonthlyExpiredLoginCannotRead(t *testing.T) {
	cannotRead(t, ReadMonthly(login(monthNow.Add(-time.Minute)), noCall(t), monthNow), "expired")
}

func TestMonthlyMissingLoginCannotRead(t *testing.T) {
	none := func() ([]byte, error) { return nil, ErrNoLogin }
	cannotRead(t, ReadMonthly(none, noCall(t), monthNow), "no login")
	broken := func() ([]byte, error) { return []byte(`{"claudeAiOauth":{}}`), nil }
	cannotRead(t, ReadMonthly(broken, noCall(t), monthNow), "no access token")
	other := func() ([]byte, error) { return nil, fmt.Errorf("security: exit 51") }
	cannotRead(t, ReadMonthly(other, noCall(t), monthNow), "exit 51")
}

func TestMonthlyRejectedOrFailedReadCannotRead(t *testing.T) {
	ok := login(monthNow.Add(time.Hour))
	cannotRead(t, ReadMonthly(ok, reply(401, `{"type":"error"}`), monthNow), "401")
	cannotRead(t, ReadMonthly(ok, reply(500, ``), monthNow), "500")
	down := func(string) (int, []byte, error) { return 0, nil, fmt.Errorf("dial tcp: no route") }
	cannotRead(t, ReadMonthly(ok, down, monthNow), "no route")
	cannotRead(t, ReadMonthly(ok, reply(200, `not json`), monthNow), "usage reply")
}

// The limit is found by its fields, not by the key's name; none or two such objects is a changed reply: cannot read.
func TestMonthlyShapeIsCheckedNotGuessed(t *testing.T) {
	ok := login(monthNow.Add(time.Hour))
	cannotRead(t, ReadMonthly(ok, reply(200, `{"five_hour":null,"extra_usage":{"monthly_limit":60000}}`), monthNow),
		"no monthly limit")
	two := `{"a":{"limit_dollars":1,"used_dollars":0,"resets_at":"2026-11-14T18:28:47Z"},` +
		`"b":{"limit_dollars":2,"used_dollars":0,"resets_at":"2026-11-14T18:28:47Z"}}`
	cannotRead(t, ReadMonthly(ok, reply(200, two), monthNow), "2 monthly limits")
	past := `{"a":{"limit_dollars":2000,"used_dollars":0,"resets_at":"2026-10-14T18:28:47Z"}}`
	cannotRead(t, ReadMonthly(ok, reply(200, past), monthNow), "reset")
	zero := `{"a":{"limit_dollars":0,"used_dollars":0,"resets_at":"2026-11-14T18:28:47Z"}}`
	cannotRead(t, ReadMonthly(ok, reply(200, zero), monthNow), "limit")
}

func TestMonthlyReadsUsedLimitAndReset(t *testing.T) {
	m := ReadMonthly(login(monthNow.Add(time.Hour)), reply(200, usageReply), monthNow)
	want := time.Date(2026, 11, 14, 18, 28, 47, 775850000, time.UTC)
	if !m.Read || m.UsedUSD != 706.944801 || m.LimitUSD != 2000 || !m.Reset.Equal(want) || m.Locked != "" {
		t.Fatalf("%+v", m)
	}
	v, reason := m.Room()
	if v != "room" || !strings.Contains(reason, "$707 of $2000") || !strings.Contains(reason, "35%") ||
		!strings.Contains(reason, "14 Nov") {
		t.Fatal(v, reason)
	}
	if txt := m.Text(); !strings.Contains(txt, "$707 of $2000") || !strings.Contains(txt, "resets") {
		t.Fatal(txt)
	}
}

func TestMonthlyFullAtLimitOrLocked(t *testing.T) {
	ok := login(monthNow.Add(time.Hour))
	at := `{"a":{"limit_dollars":2000,"used_dollars":2000,"resets_at":"2026-11-14T18:28:47Z","locked_reason":null}}`
	if v, r := ReadMonthly(ok, reply(200, at), monthNow).Room(); v != "full" {
		t.Fatal(v, r)
	}
	locked := `{"a":{"limit_dollars":2000,"used_dollars":5,"resets_at":"2026-11-14T18:28:47Z","locked_reason":"admin"}}`
	if v, r := ReadMonthly(ok, reply(200, locked), monthNow).Room(); v != "full" || !strings.Contains(r, "admin") {
		t.Fatal(v, r)
	}
}

// The keychain entry Claude Code keeps a config dir's login under: its own name for ~/.claude-work (seen 10-19).
func TestKeychainService(t *testing.T) {
	if got := keychainService("/Users/orkhanrzazade/.claude-work"); got != "Claude Code-credentials-5313dd96" {
		t.Fatal(got)
	}
}

// Codex 3b P1: aitch gives `quota --room` 3 s in all; a slow read must not cost the Max verdict.
func TestMonthlyReadHasADeadline(t *testing.T) {
	slow := func() Monthly { time.Sleep(300 * time.Millisecond); return Monthly{Read: true, LimitUSD: 1} }
	start := time.Now()
	m := WithDeadline(slow, 30*time.Millisecond)
	if el := time.Since(start); el > 200*time.Millisecond {
		t.Fatal("waited", el)
	}
	cannotRead(t, m, "took over")
	if m := WithDeadline(func() Monthly { return Monthly{Read: true, LimitUSD: 1} }, time.Second); !m.Read {
		t.Fatal(m)
	}
}

// Codex 3b P2: an object with the limit's fields that does not decode is not skipped: the reply is not understood.
func TestMonthlyMalformedCandidateCannotRead(t *testing.T) {
	ok := login(monthNow.Add(time.Hour))
	two := `{"a":{"limit_dollars":2000,"used_dollars":5,"resets_at":"2026-11-14T18:28:47Z","locked_reason":null},` +
		`"b":{"limit_dollars":2000,"used_dollars":5,"resets_at":"2026-11-14T18:28:47Z","locked_reason":{"x":1}}}`
	cannotRead(t, ReadMonthly(ok, reply(200, two), monthNow), "b")
	one := `{"a":{"limit_dollars":"2000","used_dollars":5,"resets_at":"2026-11-14T18:28:47Z"}}`
	cannotRead(t, ReadMonthly(ok, reply(200, one), monthNow), "a")
}

// Codex 3b P2: Go's HTTP/2 debug log (GODEBUG=http2debug) prints every request header, the token among them; the
// usage client speaks HTTP/1.1 only, whose transport logs no headers.
func TestUsageClientSpeaksHTTP1(t *testing.T) {
	var proto int
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proto = r.ProtoMajor }))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	tr := usageClient.Transport.(*http.Transport).Clone()
	// The client's own TLS config (its ALPN offer decides the protocol), trusting the test server only.
	tr.TLSClientConfig.RootCAs = srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	resp, err := (&http.Client{Transport: tr}).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if proto != 1 {
		t.Fatal("HTTP", proto)
	}
}
