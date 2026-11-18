package quota

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"time"
)

// Limit is the 7d share at which crius-spawn stops starting sessions on a Max account (owner 10-28 23:5x).
const Limit = 95

// Pick is the Max account a new session starts on, the config folder to start it in, and why (owner 10-28 23:5x): of
// the rows with plan max (want "max"), the one whose week resets soonest, as quota left at a reset is lost, until its
// 7d share reaches Limit (or the project's pause_at, when lower); or the row named want. A folder counts only when it
// is signed in and its projects/ is root, the persona's transcript folder (an aitch link of the persona's folder), so
// crius files the session. A week that ended with no sample since is a new one at 0% that resets a week after the old
// reset, so it goes last. A row no line measured has no known reset: it is picked only when no measured row has room,
// and why says so. One full (Room) or at the limit is passed over, and when every row is, Pick says which and at what
// share. With no row that has such a folder and want "max", Pick returns "", and the launch is refused (launch.Run).
//
// Reserve, max1, comes last (owner 11-19): nothing starts or moves onto it until every other Max row of accounts.toml is
// at its limit, a row with no folder in this persona included. Until then a pick leaves it out and says so, and a
// launch that only it could take, or that names it, is refused with the rows that still have room.
func Pick(r *Report, as *Accounts, root, want string, pauseAt int) (name, dir, why string, err error) {
	if as == nil {
		as = &Accounts{}
	}
	held, full := reserveHeld(r, as, pauseAt)
	switch {
	case held == "":
		name, dir, why, err = pick(r, as, root, want, pauseAt)
		if name == Reserve && full != "" {
			why += "; every other Max account is at its limit (" + full + ")"
		}
		return name, dir, why, err
	case want == Reserve:
		return "", "", "", errors.New(held)
	case want != "max":
		return pick(r, as, root, want, pauseAt)
	}
	rest := &Accounts{}
	for _, a := range as.Rows {
		if a.Name != Reserve {
			rest.Rows = append(rest.Rows, a)
		}
	}
	name, dir, why, err = pick(r, rest, root, want, pauseAt)
	if name == "" || err != nil {
		// every other row of this persona is full, or none has a folder in it: aitch's own route must not take max1
		if err != nil {
			return "", "", "", fmt.Errorf("%s; the others: %w", held, err)
		}
		return "", "", "", errors.New(held)
	}
	return name, dir, why + "; " + Reserve + " held back (owner 11-19) until every other Max account is at its limit", nil
}

// Reserve is the Max account used last (owner 11-19: "nothing moves to or starts on account max1 until max2 and max3
// are both at their limit").
const Reserve = "max1"

// reserveHeld says why Reserve may not be picked yet, "" when every other Max row is at its limit (or there is no
// other, or no Reserve row); full lists those rows with their shares. A row is at its limit as Pick passes one over:
// full by Room, or its 7d share at Limit (or the lower pauseAt). A row not measured, or whose week ended with no sample
// since, is not.
func reserveHeld(r *Report, as *Accounts, pauseAt int) (held, full string) {
	limit := Limit
	if pauseAt > 0 && pauseAt < limit {
		limit = pauseAt
	}
	var room, at []string
	reserve := false
	for _, a := range as.Rows {
		if a.Plan != "max" {
			continue
		}
		if a.Name == Reserve {
			reserve = true
			continue
		}
		v := r.Account(a.Login)
		switch {
		case a.Login == "" || v == nil || v.Ended.IsZero() && v.Reset.IsZero():
			room = append(room, a.Name+" quota not measured")
		case !v.Ended.IsZero():
			room = append(room, a.Name+" 7d 0%, a new week since "+when(v.Ended))
		default:
			share := fmt.Sprintf("%s 7d %.0f%%", a.Name, math.Floor(v.SevenPct))
			if verdict, _ := Room(v); verdict == "full" || v.SevenPct >= float64(limit) {
				at = append(at, share)
			} else {
				room = append(room, share)
			}
		}
	}
	if !reserve || room == nil {
		return "", strings.Join(at, ", ")
	}
	return fmt.Sprintf("%s is held back (owner 11-19) until every other Max account is at its limit: %s", Reserve,
		strings.Join(room, ", ")), strings.Join(at, ", ")
}

// pick is Pick over the rows of as, Reserve treated as any other.
func pick(r *Report, as *Accounts, root, want string, pauseAt int) (name, dir, why string, err error) {
	limit := 100 // a row asked for by name may spend its last 5% on purpose
	if want == "max" {
		limit = Limit
	}
	if pauseAt > 0 && pauseAt < limit {
		limit = pauseAt
	}
	rows := as.Rows
	if want != "max" {
		rows = nil
		for _, a := range as.Rows {
			if a.Name == want && a.Plan == "max" {
				rows = []Acct{a}
			}
		}
		if rows == nil {
			return "", "", "", fmt.Errorf("accounts.toml has no Max account %s", want)
		}
	}
	type pick struct {
		name, dir, share string
		reset            time.Time
	}
	var best, blind *pick
	var full, unmeasured []string
	for _, a := range rows {
		d, err := linked(a, root)
		if err != nil {
			return "", "", "", err
		}
		if a.Plan != "max" || d == "" {
			continue
		}
		p := pick{name: a.Name, dir: d}
		switch v := r.Account(a.Login); {
		case v == nil || v.Ended.IsZero() && v.Reset.IsZero():
			unmeasured = append(unmeasured, a.Name)
			if blind == nil {
				blind = &p
			}
			continue
		case !v.Ended.IsZero():
			p.reset = v.Ended
			for !p.reset.After(r.Now) {
				p.reset = p.reset.Add(week)
			}
			p.share = "7d 0%, no sample since its reset " + when(v.Ended)
		default:
			share := fmt.Sprintf("7d %.0f%%", math.Floor(v.SevenPct))
			verdict, reason := Room(v)
			if verdict == "full" || v.SevenPct >= float64(limit) {
				if v.FivePct >= 100 {
					share += ", 5h 100%"
				}
				full = append(full, a.Name+" "+share)
				continue
			}
			if verdict == "unknown" && v.SevenPct >= 90 {
				// An old sample at 90% or more may be at the limit now (review of item 0): unsure, like unmeasured.
				unmeasured = append(unmeasured, a.Name+" ("+reason+")")
				if blind == nil {
					p.share = reason
					blind = &p
				}
				continue
			}
			p.reset, p.share = v.Reset, share
		}
		if best == nil || p.reset.Before(best.reset) {
			best = &p
		}
	}
	var notes []string
	if full != nil {
		notes = append(notes, "passed over: "+strings.Join(full, ", "))
	}
	switch {
	case best != nil && want != "max":
		return best.name, best.dir, fmt.Sprintf("account %s as asked: %s, resets %s", best.name, best.share, when(best.reset)), nil
	case best != nil:
		if unmeasured != nil {
			notes = append(notes, "quota unknown: "+strings.Join(unmeasured, ", "))
		}
		why = fmt.Sprintf("account %s: resets %s, the soonest under %d%% (%s)", best.name, when(best.reset), limit, best.share)
		if notes != nil {
			why += "; " + strings.Join(notes, "; ")
		}
		return best.name, best.dir, why, nil
	case blind != nil:
		why = fmt.Sprintf("account %s: quota not measured yet, so its share and reset are unknown", blind.name)
		if blind.share != "" {
			why = fmt.Sprintf("account %s: quota unsure (%s)", blind.name, blind.share)
		}
		if want == "max" {
			why += "; picked as no measured account has room"
		}
		if notes != nil {
			why += "; " + strings.Join(notes, "; ")
		}
		return blind.name, blind.dir, why, nil
	case want != "max" && full != nil:
		return "", "", "", fmt.Errorf("%s is at its limit: %s", want, strings.TrimPrefix(full[0], want+" "))
	case want != "max":
		return "", "", "", fmt.Errorf("%s has no folder linked to %s and signed in", want, root)
	case full != nil:
		return "", "", "", fmt.Errorf("every Max account is at its limit: %s", strings.Join(full, ", "))
	}
	return "", "", "", nil
}

// linked is the first of the row's folders that is signed into the row's login and whose projects/ is root; "" if
// none is.
func linked(a Acct, root string) (string, error) {
	if a.Login == "" {
		return "", nil
	}
	for _, d := range a.Dirs {
		if samePath(filepath.Join(d, "projects")) != samePath(root) {
			continue
		}
		l, err := LoginOf(d)
		if err != nil {
			return "", err
		}
		if l == a.Login {
			return d, nil
		}
	}
	return "", nil
}
