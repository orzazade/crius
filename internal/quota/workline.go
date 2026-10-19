package quota

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// The work statusline (Crius ORDER 10-19 15:25): the parts the personal tab shows (model + account, branch, cost,
// context %) and, on an API-billed session, the Enterprise fallback in place of NoPlanLimits. The fallback comes from
// a cache that a detached refresh writes with the same read `quota` uses, so the line never waits for the keychain or
// the network.
const (
	EntFresh = 5 * time.Minute  // a cache this old is refreshed (the statusline's refreshInterval)
	EntStale = 15 * time.Minute // a cache this old is shown as STALE: the refresh is not landing
)

type entCache struct {
	At time.Time `json:"at"`
	U  EntUsage  `json:"usage"`
}

func entCachePath(home string) string { return filepath.Join(home, "spool", "ent-usage.json") }

// Work is the work persona's statusline. account is the login's badge (Account), branch names dir's git branch ("" for
// none), refresh starts a refresh of the fallback cache without waiting for it.
func Work(home string, in []byte, now time.Time, account string, branch func(dir string) string, refresh func() error) string {
	q, err := Sample(home, "work", in, now)
	if err != nil {
		q = fmt.Sprintf("crius quota sample FAILED: %v", err)
	} else if q == NoPlanLimits {
		q = entLine(home, now, refresh)
	}
	var d struct {
		Model struct {
			Name string `json:"display_name"`
		} `json:"model"`
		Workspace struct {
			Dir string `json:"current_dir"`
		} `json:"workspace"`
		Cost struct {
			USD float64 `json:"total_cost_usd"`
		} `json:"cost"`
		Context struct {
			Remaining *float64 `json:"remaining_percentage"`
		} `json:"context_window"`
	}
	_ = json.Unmarshal(in, &d) // a bad input is already in q from Sample
	model := d.Model.Name
	if model == "" {
		model = "model?"
	}
	parts := []string{model + " " + account}
	if b := branch(d.Workspace.Dir); b != "" {
		parts = append(parts, b)
	}
	parts = append(parts, fmt.Sprintf("$%.2f", d.Cost.USD))
	if r := d.Context.Remaining; r != nil {
		used := min(100, max(0, 100-int(math.Round(*r))))
		parts = append(parts, strings.Repeat("█", used/10)+strings.Repeat("░", 10-used/10)+fmt.Sprintf(" ctx %d%%", used))
	} else {
		parts = append(parts, "ctx –")
	}
	return strings.Join(append(parts, q), " │ ")
}

// entLine is the fallback part from the cache. A missing, unreadable, old or future-dated cache asks for a refresh.
func entLine(home string, now time.Time, refresh func() error) string {
	var t string
	var c entCache
	b, err := os.ReadFile(entCachePath(home))
	switch {
	case errors.Is(err, os.ErrNotExist):
		t = "fallback: not read yet"
	case err != nil:
		t = fmt.Sprintf("fallback: cache unreadable (%v)", err)
	case json.Unmarshal(b, &c) != nil:
		t = "fallback: cache unreadable (not JSON)"
	default:
		age := now.Sub(c.At)
		if age >= 0 && age < EntFresh {
			return c.U.short()
		}
		t = c.U.short()
		if age >= EntStale {
			t += fmt.Sprintf(" · STALE, read %s ago, see logs/ent-refresh.log", ago(age))
		}
	}
	// One refresh per EntFresh: one that does not land (the cache cannot be written) is not started on every render.
	// The attempt is recorded first; when that fails, nothing is started (review of the work line).
	try := filepath.Join(home, "spool", "ent-usage.try")
	if b, err := os.ReadFile(try); err == nil {
		if at, err := time.Parse(time.RFC3339, string(b)); err == nil && now.Sub(at) >= 0 && now.Sub(at) < EntFresh {
			return t
		}
	}
	if err := os.WriteFile(try, []byte(now.UTC().Format(time.RFC3339)), 0o600); err != nil {
		return t + fmt.Sprintf(" · refresh FAILED: %v", err)
	}
	if err := refresh(); err != nil {
		t += fmt.Sprintf(" · refresh FAILED: %v", err)
	}
	return t
}

// short is the fallback in the statusline: the numbers of Text, shorter.
func (u EntUsage) short() string {
	if !u.Read {
		return "fallback: cannot read (" + u.Reason + ")"
	}
	i, s := u.Included, u.Spend
	t := fmt.Sprintf("fallback: included %.0f%% ($%.0f of $%.0f, expires %s)", 100*i.UsedUSD/i.LimitUSD, i.UsedUSD, i.LimitUSD,
		day(i.Expires))
	if i.Locked != "" {
		t += ", locked: " + i.Locked
	}
	t += fmt.Sprintf(" · spend $%.0f of $%.0f", s.UsedUSD, s.LimitUSD)
	switch {
	case !s.Enabled:
		t += ", off"
	case s.Reached:
		t += ", reached"
	}
	return t
}

// RefreshEnt writes the cache from read, a cannot-read too. It returns at once while another refresh runs, and does
// not read again when the cache is fresh (two statuslines may both ask).
func RefreshEnt(home string, read func(time.Time) EntUsage, now time.Time) error {
	lock, err := os.OpenFile(filepath.Join(home, "spool", "ent-usage.lock"), os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); errors.Is(err, syscall.EWOULDBLOCK) {
		return nil
	} else if err != nil {
		return err
	}
	var c entCache
	if b, err := os.ReadFile(entCachePath(home)); err == nil && json.Unmarshal(b, &c) == nil {
		if age := now.Sub(c.At); age >= 0 && age < EntFresh {
			return nil
		}
	}
	b, err := json.Marshal(entCache{At: now, U: read(now)})
	if err != nil {
		return err
	}
	tmp := entCachePath(home) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, entCachePath(home))
}

// ReadWorkEnt reads the Enterprise account with the ~/.claude-work login within EntBudget: the read of `quota` and of
// the statusline's refresh.
func ReadWorkEnt(now time.Time) EntUsage {
	u, err := os.UserHomeDir()
	if err != nil {
		return EntUsage{Reason: fmt.Sprintf("home: %v", err)}
	}
	return WithDeadline(func() EntUsage {
		return ReadEntUsage(KeychainLogin(filepath.Join(u, ".claude-work")), UsageGet, now)
	}, EntBudget)
}

// Account is the badge of the login in configDir's .claude.json: ENT, MAX, NO-LOGIN, or ACCT? when it cannot be read.
// The folder name says nothing: aitch can start a work session on the personal Max login.
func Account(configDir string) string {
	b, err := os.ReadFile(filepath.Join(configDir, ".claude.json"))
	var c struct {
		Acct *struct {
			Billing string `json:"billingType"`
		} `json:"oauthAccount"`
	}
	switch {
	case err != nil || json.Unmarshal(b, &c) != nil:
		return "ACCT?"
	case c.Acct == nil || c.Acct.Billing == "":
		return "NO-LOGIN"
	case strings.Contains(c.Acct.Billing, "enterprise"):
		return "ENT"
	}
	return "MAX"
}
