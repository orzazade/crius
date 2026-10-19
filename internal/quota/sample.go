// Package quota is step 3 (design §5, §9): the statusline sampler that writes spool/quota.jsonl, and the `quota`
// report built from the store. The sampler never touches the store: it runs on every statusline update.
package quota

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Heartbeat: a sample is written at least this often while any session renders its statusline, so a gap means
// "not measured", not "no use".
const Heartbeat = 30 * time.Minute

type window struct {
	Pct   *float64 `json:"used_percentage"`
	Reset *int64   `json:"resets_at"`
}

type input struct {
	SessionID string `json:"session_id"`
	Cost      struct {
		USD   float64 `json:"total_cost_usd"`
		APIms float64 `json:"total_api_duration_ms"`
	} `json:"cost"`
	RateLimits *struct {
		Five  *window `json:"five_hour"`
		Seven *window `json:"seven_day"`
	} `json:"rate_limits"`
}

// Line is one spool record. Kind: `session` (first sight of a session, or its rate_limits came or went),
// `change` (the session's own values changed), `heartbeat` (no line for Heartbeat: from a Max session, none of the
// current epoch; from a known enterprise session, none at all, so an enterprise-only persona is not stale).
// RL says whether the statusline had rate_limits: present only on Pro/Max, so false after a response means an
// API-billed (enterprise) session.
type Line struct {
	TS         string   `json:"ts"`
	Kind       string   `json:"kind"`
	Session    string   `json:"session"`
	Persona    string   `json:"persona"`
	RL         bool     `json:"rl"`
	FivePct    *float64 `json:"five_pct,omitempty"`
	FiveReset  *int64   `json:"five_reset,omitempty"`
	SevenPct   *float64 `json:"seven_pct,omitempty"`
	SevenReset *int64   `json:"seven_reset,omitempty"`
	// Responded: a response came since the session's last render, so the values are new. False for a session first
	// seen (its reading may be old) and an idle repeat; absent on lines from before the field.
	Responded *bool `json:"responded,omitempty"`
}

type seen struct {
	TS    string  `json:"ts"`
	Lined bool    `json:"lined"` // a line was written for the session; RL and Vals are what it said
	RL    bool    `json:"rl"`
	Vals  string  `json:"vals"`
	USD   float64 `json:"usd"` // cost and API time at the last render, to see a new response arrive
	APIms float64 `json:"api_ms"`
	// Render: the session's last render, kept to the minute; a response is dated by it (review of d25f4f2).
	Render string `json:"render,omitempty"`
}

// RenderGap: the statusline renders at least every refreshInterval (300 s), so a session's renders are closer than
// this while its sampler runs. A response found after a longer gap may be that old: its values are not new.
const RenderGap = 10 * time.Minute

type state struct {
	Last     map[string]string `json:"last"`  // per persona: its newest line with rate_limits of the current epoch
	Alive    map[string]string `json:"alive"` // per persona: its newest line of any kind
	Reset    int64             `json:"reset"` // the newest seven-day resets_at seen: the current epoch
	Sessions map[string]seen   `json:"sessions"`
}

const tsFormat = "2006-01-02T15:04:05Z"

// Sample records what one statusline update shows and returns the text to display. It writes a line only when
// something is new (see Line), so the spool grows with changes, not with renders.
func Sample(home, persona string, in []byte, now time.Time) (string, error) {
	var d input
	if err := json.Unmarshal(in, &d); err != nil {
		return "", fmt.Errorf("statusline input: %w", err)
	}
	if d.SessionID == "" {
		return "", errors.New("statusline input has no session_id")
	}
	rl := d.RateLimits != nil
	l := Line{TS: now.UTC().Format(tsFormat), Session: d.SessionID, Persona: persona, RL: rl}
	if rl {
		if w := d.RateLimits.Five; w != nil {
			l.FivePct, l.FiveReset = w.Pct, w.Reset
		}
		if w := d.RateLimits.Seven; w != nil {
			l.SevenPct, l.SevenReset = w.Pct, w.Reset
		}
		// rate_limits that do not give the seven-day share and reset measure nothing: an empty object or renamed
		// fields would otherwise pass as a Max sample with no values.
		if l.SevenPct == nil || l.SevenReset == nil {
			return "", errors.New("statusline rate_limits has no seven_day used_percentage and resets_at")
		}
	}
	vals := fmt.Sprintf("%v/%v/%v/%v", deref(l.FivePct), deref(l.FiveReset), deref(l.SevenPct), deref(l.SevenReset))

	spool := filepath.Join(home, "spool")
	lock, err := os.OpenFile(filepath.Join(spool, "quota.lock"), os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return "", err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return "", err
	}
	statePath := filepath.Join(spool, "quota.state.json")
	st := state{Sessions: map[string]seen{}, Last: map[string]string{}, Alive: map[string]string{}}
	if b, err := os.ReadFile(statePath); err == nil {
		if err := json.Unmarshal(b, &st); err != nil {
			return "", fmt.Errorf("%s: %w", statePath, err)
		}
		if st.Sessions == nil {
			st.Sessions = map[string]seen{}
		}
		if st.Last == nil {
			st.Last = map[string]string{}
		}
		if st.Alive == nil {
			st.Alive = map[string]string{}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	s, known := st.Sessions[d.SessionID]
	last, _ := time.Parse(tsFormat, st.Last[persona])
	alive, _ := time.Parse(tsFormat, st.Alive[persona])
	// rate_limits come with a response, so their absence means an API-billed session only when a response arrived
	// since the last render: a new session shows none before its first response, a resumed one shows its old cost.
	// The session's last render: the kept render time; in a state file from before that field, its last line (a
	// line is a render too). Never the line when Render is kept: a line written while the clock ran ahead is not
	// corrected and would date a response again once the clock passes it (review of 45ca441). None: a zero time, so
	// no response counts as new. A render ahead of the clock (it stepped back) proves nothing (review of 778abca).
	r := s.Render
	if r == "" {
		r = s.TS
	}
	rendered, _ := time.Parse(tsFormat, r)
	gap := now.Sub(rendered)
	responded := known && (d.Cost.USD > s.USD || d.Cost.APIms > s.APIms) && gap >= 0 && gap <= RenderGap
	// An idle session can repeat a past epoch's window: it never heartbeats, so it cannot stand in for a live one.
	current := rl && *l.SevenReset > st.Reset-3600
	// A session whose response came without rate_limits is API-billed: it measures no quota, but its render shows
	// the persona's statusline is alive. A session not known yet (resumed, no response) may be Max: no line.
	enterprise := !rl && s.Lined && !s.RL
	switch {
	case rl && !(s.Lined && s.RL):
		l.Kind = "session"
	case rl && s.Vals != vals:
		l.Kind = "change"
	case current && now.Sub(last) >= Heartbeat:
		l.Kind = "heartbeat"
	case enterprise && now.Sub(alive) >= Heartbeat:
		l.Kind = "heartbeat"
	case !rl && !(s.Lined && !s.RL) && responded:
		l.Kind = "session"
	}
	n := seen{TS: s.TS, Lined: s.Lined, RL: s.RL, Vals: s.Vals, USD: d.Cost.USD, APIms: d.Cost.APIms, Render: s.Render}
	if gap >= time.Minute || gap < 0 {
		n.Render = l.TS
	}
	if !known {
		n.TS = l.TS
	}
	if l.Kind != "" {
		n.TS, n.Lined, n.RL, n.Vals = l.TS, true, rl, vals
	}
	if l.Kind != "" {
		l.Responded = &responded
		b, err := json.Marshal(l)
		if err != nil {
			return "", err
		}
		f, err := os.OpenFile(filepath.Join(spool, "quota.jsonl"), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
		if err != nil {
			return "", err
		}
		_, err = f.Write(append(b, '\n'))
		if err := errors.Join(err, f.Close()); err != nil {
			return "", err
		}
		if current {
			st.Last[persona] = l.TS
			st.Reset = max(st.Reset, *l.SevenReset)
		}
		st.Alive[persona] = l.TS
	}
	if l.Kind != "" || n != s {
		st.Sessions[d.SessionID] = n
		for id, v := range st.Sessions { // a week since its last line (or first sight): the session is gone
			if t, err := time.Parse(tsFormat, v.TS); err != nil || now.Sub(t) > 7*24*time.Hour {
				delete(st.Sessions, id)
			}
		}
		b, err := json.Marshal(st)
		if err != nil {
			return "", err
		}
		tmp := statePath + ".tmp"
		if err := os.WriteFile(tmp, b, 0o600); err != nil {
			return "", err
		}
		if err := os.Rename(tmp, statePath); err != nil {
			return "", err
		}
	}
	if !rl && !(n.Lined && !n.RL) {
		return "quota: no response yet", nil
	}
	return display(l), nil
}

func deref[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

// NoPlanLimits is the quota text of an API-billed session.
const NoPlanLimits = "quota: no plan limits (API-billed account)"

func display(l Line) string {
	if !l.RL {
		return NoPlanLimits
	}
	out := "quota: "
	if l.FivePct != nil {
		out += fmt.Sprintf("5h %.0f%% · ", *l.FivePct)
	}
	// Sample refuses rate_limits without the seven-day share and reset, so both are here.
	return out + fmt.Sprintf("7d %.0f%% (resets %s)", *l.SevenPct, time.Unix(*l.SevenReset, 0).Local().Format("Mon 15:04"))
}
