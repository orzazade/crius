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

// The usage reply as read on 2026-10-19 with the ~/.claude-work login, trimmed. The owner's usage page shows
// copper_kite as "Included usage" (35%, expires 14 Nov) and spend/extra_usage as "Spend limit" ($0 of $600).
const usageReply = `{"five_hour":null,"seven_day":null,"copper_kite":{"utilization":35.34724005,` +
	`"resets_at":"2026-11-14T18:28:47.775850+00:00","limit_dollars":2000,"used_dollars":706.944801,` +
	`"remaining_dollars":1293.055199,"locked_reason":null},"brass_thimble":null,` +
	`"extra_usage":{"is_enabled":true,"monthly_limit":60000,"used_credits":0.0,"utilization":null,"currency":"USD",` +
	`"decimal_places":2,"spend_limit_reached":false,"daily":null,"weekly":null},"limits":[],` +
	`"spend":{"used":{"amount_minor":0,"currency":"USD","exponent":2},"limit":{"amount_minor":60000,"currency":"USD",` +
	`"exponent":2},"percent":0,"enabled":true,"cap":{"money":null,"credits":{"amount_minor":60000,"exponent":2}}},` +
	`"member_dashboard_available":true}`

// body is a reply with the given included usage object (key "a") and spend limit state.
func body(included string, usedMinor int, enabled, reached bool) string {
	return fmt.Sprintf(`{"a":%s,"spend":{"used":{"amount_minor":%d,"currency":"USD","exponent":2},`+
		`"limit":{"amount_minor":60000,"currency":"USD","exponent":2},"enabled":%v},`+
		`"extra_usage":{"monthly_limit":60000,"decimal_places":2,"currency":"USD","spend_limit_reached":%v}}`, included, usedMinor, enabled, reached)
}

const incOK = `{"limit_dollars":2000,"used_dollars":5,"resets_at":"2026-11-14T18:28:47Z","locked_reason":null}`
const incFull = `{"limit_dollars":2000,"used_dollars":2000,"resets_at":"2026-11-14T18:28:47Z","locked_reason":null}`

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
func cannotRead(t *testing.T, m EntUsage, why string) {
	t.Helper()
	v, reason := m.Room()
	if m.Read || !strings.Contains(m.Reason, why) || v != "unknown" || !strings.HasPrefix(reason, "cannot read") ||
		!strings.Contains(m.Text(), "cannot read") || strings.Contains(m.Text(), "%") || strings.Contains(m.Text(), "$") {
		t.Fatalf("%+v · room %s %q · text %q", m, v, reason, m.Text())
	}
}

// Owner 10-19 14:44: the Enterprise login expires ~10-21; an expired or missing login is a loud "cannot read", never
// 0%, and crius never renews it (read-only), so the endpoint is not even asked.
func TestEntExpiredLoginCannotRead(t *testing.T) {
	cannotRead(t, ReadEntUsage(login(monthNow.Add(-time.Minute)), noCall(t), monthNow), "expired")
}

func TestEntMissingLoginCannotRead(t *testing.T) {
	none := func() ([]byte, error) { return nil, ErrNoLogin }
	cannotRead(t, ReadEntUsage(none, noCall(t), monthNow), "no login")
	broken := func() ([]byte, error) { return []byte(`{"claudeAiOauth":{}}`), nil }
	cannotRead(t, ReadEntUsage(broken, noCall(t), monthNow), "no access token")
	other := func() ([]byte, error) { return nil, fmt.Errorf("security: exit 51") }
	cannotRead(t, ReadEntUsage(other, noCall(t), monthNow), "exit 51")
}

func TestEntRejectedOrFailedReadCannotRead(t *testing.T) {
	ok := login(monthNow.Add(time.Hour))
	cannotRead(t, ReadEntUsage(ok, reply(401, `{"type":"error"}`), monthNow), "401")
	cannotRead(t, ReadEntUsage(ok, reply(500, ``), monthNow), "500")
	down := func(string) (int, []byte, error) { return 0, nil, fmt.Errorf("dial tcp: no route") }
	cannotRead(t, ReadEntUsage(ok, down, monthNow), "no route")
	cannotRead(t, ReadEntUsage(ok, reply(200, `not json`), monthNow), "usage reply")
}

// Included usage is found by its fields, not by the key's name; none or two such objects is a changed reply.
func TestEntShapeIsCheckedNotGuessed(t *testing.T) {
	ok := login(monthNow.Add(time.Hour))
	cannotRead(t, ReadEntUsage(ok, reply(200, `{"five_hour":null,"spend":{},"extra_usage":{}}`), monthNow),
		"no included usage")
	two := strings.Replace(body(incOK, 0, true, false), `"spend"`, `"b":`+incOK+`,"spend"`, 1)
	cannotRead(t, ReadEntUsage(ok, reply(200, two), monthNow), "2 included usages")
	past := body(`{"limit_dollars":2000,"used_dollars":0,"resets_at":"2026-10-14T18:28:47Z"}`, 0, true, false)
	cannotRead(t, ReadEntUsage(ok, reply(200, past), monthNow), "already past")
	zero := body(`{"limit_dollars":0,"used_dollars":0,"resets_at":"2026-11-14T18:28:47Z"}`, 0, true, false)
	cannotRead(t, ReadEntUsage(ok, reply(200, zero), monthNow), "not a limit")
	// The spend limit must be there, in USD, and agree with extra_usage.
	cannotRead(t, ReadEntUsage(ok, reply(200, `{"a":`+incOK+`,"extra_usage":{}}`), monthNow), "no spend limit")
	eur := strings.Replace(body(incOK, 0, true, false), `"USD"`, `"EUR"`, 1)
	cannotRead(t, ReadEntUsage(ok, reply(200, eur), monthNow), "no spend limit")
	noState := strings.Replace(body(incOK, 0, true, false), `,"spend_limit_reached":false`, ``, 1)
	cannotRead(t, ReadEntUsage(ok, reply(200, noState), monthNow), "no spend limit state")
	differ := strings.Replace(body(incOK, 0, true, false), `"monthly_limit":60000`, `"monthly_limit":50000`, 1)
	cannotRead(t, ReadEntUsage(ok, reply(200, differ), monthNow), "disagrees")
}

// Crius NEED 15:06: both allowances under the page's names; nothing called monthly, the spend limit's reset is not
// in the reply and is said so.
func TestEntReadsIncludedAndSpendLimit(t *testing.T) {
	m := ReadEntUsage(login(monthNow.Add(time.Hour)), reply(200, usageReply), monthNow)
	want := time.Date(2026, 11, 14, 18, 28, 47, 775850000, time.UTC)
	if !m.Read || m.Included != (Included{UsedUSD: 706.944801, LimitUSD: 2000, Expires: m.Included.Expires}) ||
		!m.Included.Expires.Equal(want) || m.Spend != (SpendLimit{UsedUSD: 0, LimitUSD: 600, Enabled: true}) {
		t.Fatalf("%+v", m)
	}
	v, reason := m.Room()
	if v != "room" || !strings.Contains(reason, "included usage $707 of $2000 used (35%), expires 14 Nov") ||
		!strings.Contains(reason, "spend limit $0 of $600 used, reset date not in the reply") {
		t.Fatal(v, reason)
	}
	if txt := m.Text(); !strings.HasPrefix(txt, "fallback: included usage $707") || strings.Contains(txt, "monthly") {
		t.Fatal(txt)
	}
}

// Room while either allowance has room; full only when neither has.
func TestEntRoomInEither(t *testing.T) {
	ok := login(monthNow.Add(time.Hour))
	locked := strings.Replace(incOK, `"locked_reason":null`, `"locked_reason":"admin"`, 1)
	for _, c := range []struct {
		name, body, want string
	}{
		{"both room", body(incOK, 0, true, false), "room"},
		{"included only", body(incOK, 60000, true, true), "room"},
		{"spend only", body(incFull, 100, true, false), "room"},
		{"locked, spend room", body(locked, 0, true, false), "room"},
		{"included full, spend off", body(incFull, 0, false, false), "full"},
		{"included full, spend reached", body(incFull, 100, true, true), "full"},
		{"included full, spend at limit", body(incFull, 60000, true, false), "full"},
		{"locked, spend off", body(locked, 0, false, false), "full"},
	} {
		if v, r := ReadEntUsage(ok, reply(200, c.body), monthNow).Room(); v != c.want {
			t.Errorf("%s: %s %s", c.name, v, r)
		}
	}
}

// Crius NEED 15:06: any dollar-limit object this code does not know is cannot read, never ignored.
func TestEntUnknownLimitObjectCannotRead(t *testing.T) {
	ok := login(monthNow.Add(time.Hour))
	for _, extra := range []string{`"new_cap":{"amount_minor":5000,"exponent":2}`, `"limits":[{"x":1}]`,
		`"z":{"limit_dollars":5,"used_dollars":1}`, `"team":{"credits":3}`} {
		b := usageReply[:len(usageReply)-1] + "," + extra + "}"
		cannotRead(t, ReadEntUsage(ok, reply(200, b), monthNow), "unknown limit")
	}
}

// The keychain entry Claude Code keeps a config dir's login under: its own name for ~/.claude-work (seen 10-19).
func TestKeychainService(t *testing.T) {
	if got := keychainService("/Users/orkhanrzazade/.claude-work"); got != "Claude Code-credentials-5313dd96" {
		t.Fatal(got)
	}
}

// Codex 3b P1: aitch gives `quota --room` 3 s in all; a slow read must not cost the Max verdict.
func TestEntReadHasADeadline(t *testing.T) {
	slow := func() EntUsage { time.Sleep(300 * time.Millisecond); return EntUsage{Read: true} }
	start := time.Now()
	m := WithDeadline(slow, 30*time.Millisecond)
	if el := time.Since(start); el > 200*time.Millisecond {
		t.Fatal("waited", el)
	}
	cannotRead(t, m, "took over")
	if m := WithDeadline(func() EntUsage { return EntUsage{Read: true} }, time.Second); !m.Read {
		t.Fatal(m)
	}
}

// Codex 3b P2: an object with the limit's fields that does not decode is not skipped: the reply is not understood.
func TestEntMalformedCandidateCannotRead(t *testing.T) {
	ok := login(monthNow.Add(time.Hour))
	two := `{"a":{"limit_dollars":2000,"used_dollars":5,"resets_at":"2026-11-14T18:28:47Z","locked_reason":null},` +
		`"b":{"limit_dollars":2000,"used_dollars":5,"resets_at":"2026-11-14T18:28:47Z","locked_reason":{"x":1}}}`
	cannotRead(t, ReadEntUsage(ok, reply(200, two), monthNow), "b")
	one := `{"a":{"limit_dollars":"2000","used_dollars":5,"resets_at":"2026-11-14T18:28:47Z"}}`
	cannotRead(t, ReadEntUsage(ok, reply(200, one), monthNow), "a")
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

// Codex review of 72e9793 (four P2), each on today's reply with one change.
func TestEntReviewOf72e9793(t *testing.T) {
	ok := login(monthNow.Add(time.Hour))
	edit := func(old, new string) string {
		if !strings.Contains(usageReply, old) {
			t.Fatal("not in reply:", old)
		}
		return strings.Replace(usageReply, old, new, 1)
	}
	// 1. An exponent out of range gives no number (an infinite one emptied the --room line).
	cannotRead(t, ReadEntUsage(ok, reply(200, edit(`"used":{"amount_minor":0,"currency":"USD","exponent":2}`,
		`"used":{"amount_minor":1,"currency":"USD","exponent":-400}`)), monthNow), "exponent")
	// 2. A limit object nested inside a known container is unknown too.
	cannotRead(t, ReadEntUsage(ok, reply(200, edit(`"daily":null`, `"daily":{"limit_dollars":10,"used_dollars":10}`)),
		monthNow), "extra_usage.daily")
	cannotRead(t, ReadEntUsage(ok, reply(200, edit(`"money":null`, `"money":{"amount_minor":5}`)), monthNow),
		"spend.cap.money")
	// 3. extra_usage in another currency does not agree with a USD limit.
	cannotRead(t, ReadEntUsage(ok, reply(200, edit(`"utilization":null,"currency":"USD"`,
		`"utilization":null,"currency":"EUR"`)), monthNow), "currency")
	// 4. Emptiness is decoded, not compared as text: an empty limits list with a space is still empty.
	if m := ReadEntUsage(ok, reply(200, edit(`"limits":[]`, `"limits":[ ]`)), monthNow); !m.Read {
		t.Fatal(m.Reason)
	}
}
