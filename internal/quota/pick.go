package quota

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Limit is the 7d share at which crius-spawn stops starting sessions on a Max account (owner 10-28 23:5x).
const Limit = 95

// Pick is the Max account a new session starts on, the config folder to start it in, and why (owner 10-28 23:5x): of
// the rows with plan max (want "max"), the one whose week resets soonest, as quota left at a reset is lost, until its
// 7d share reaches Limit (or the project's pause_at, when lower); or the row named want. A folder counts when it is
// signed in and its projects/ is root, the persona's transcript folder (an aitch link of the persona's folder); one of
// another persona only as below, while max1 is held. A row whose newest reading is from a week that has since reset is
// unknown, like a row no line measured: the reading says nothing about this week, which another machine may have
// spent (11-21: the Mac read max3 as
// "a new week, 0%" while the Legion saw it at 96%). An unknown row is picked only when no measured row has room, and why
// says so. One full (Room) or at the limit is passed over, and when every row is, Pick says which and at what
// share. With no row that has such a folder and want "max", Pick returns "", and the launch is refused (launch.Run).
//
// Reserve, max1, comes last (owner 11-19): nothing starts or moves onto it until every other Max row of accounts.toml is
// at its limit, a row with no folder in this persona included. Until then a pick leaves it out and says so; a row with
// room takes the session in a folder of another persona when no row but max1 has one of this persona with room (11-24),
// and a launch that names max1 is refused with the rows that still have room.
func Pick(r *Report, as *Accounts, root, want string, pauseAt int) (name, dir, why string, err error) {
	if as == nil {
		as = &Accounts{}
	}
	held, full := reserveHeld(r, as)
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
	var reserve Acct
	for _, a := range as.Rows {
		if a.Name != Reserve {
			rest.Rows = append(rest.Rows, a)
		} else {
			reserve = a
		}
	}
	heldNote := "; " + Reserve + " held back (owner 11-19) until every other Max account is at its limit"
	name, dir, why, err = pick(r, rest, root, want, pauseAt)
	if name != "" {
		return name, dir, why + heldNote, nil
	}
	// No other row has a folder in this persona with room. held says one of them has room, so it takes the session in a
	// folder of another persona (11-24: max2 took the only other personal folder with it, and every crius pick was
	// refused while max3 sat at 44%). With no folder of this persona on any row, no pick as before (launch.Run refuses it).
	if err == nil {
		if d, err := linked(reserve, root); err != nil {
			return "", "", "", err
		} else if d == "" {
			return "", "", "", nil
		}
	}
	name, dir, why, err = pick(r, rest, anyPersona, want, pauseAt)
	switch {
	case err != nil:
		return "", "", "", fmt.Errorf("%s; %s", held, strings.Replace(err.Error(), "every Max account is at its limit", "every other one is at its limit", 1))
	case name == "":
		return "", "", "", errors.New(held)
	}
	return name, dir, why + "; no other Max account has a folder under " + root + " with room, so its transcript goes under " +
		samePath(filepath.Join(dir, "projects")) + heldNote, nil
}

// Sees says whether a session in the config folder dir writes its transcripts under root: dir's projects/ is root, links
// resolved. claude --resume finds a session only from a folder that sees its transcript (crius-spawn -move, 11-24).
func Sees(dir, root string) bool { return samePath(filepath.Join(dir, "projects")) == samePath(root) }

// anyPersona as pick's root takes a row's first signed-in folder that has a projects/ folder, of whatever persona.
const anyPersona = "\x00any persona"

// Standing is how one login's week reads against limit (a 7d share): "room"; "full", when Room says full (a 5h window at
// 100% too) or the 7d share is at limit; or "unsure", when no line measured it, its newest reading is from a week that
// has since reset, or Room cannot tell (an old reading spent past since, 11-21; an old one at 90% or more, as it may be at
// the limit now; a top share not measured; a reading ahead of the clock). One at the limit is full at any age. why is the
// share ("7d 40%", ", 5h 100%" added when full by it) or what is unknown ("" when never measured); v is the login's
// view, nil when never measured. Pick, the PAUSE guard and quota --room read each login by it, from one report
// (WithHosts), so the three agree; an unsure login has room for none of them (Crius 11-21), and only Pick still tries
// one, when no measured row has room.
func Standing(r *Report, login string, limit int) (state, why string, v *Report) {
	v = r.Account(login)
	switch {
	case v == nil || v.Ended.IsZero() && v.Reset.IsZero():
		return "unsure", "", nil
	case !v.Ended.IsZero():
		return "unsure", unknownWeek(v), v
	}
	share := fmt.Sprintf("7d %.0f%%", math.Floor(v.SevenPct))
	verdict, reason := Room(v)
	switch {
	case verdict == "full" || v.SevenPct >= float64(limit):
		if v.FivePct >= 100 {
			share += ", 5h 100%"
		}
		return "full", share, v
	case verdict == "unknown":
		return "unsure", reason, v
	}
	return "room", share, v
}

// unknownWeek says why a row whose newest reading is from an earlier week (v.Ended set) has no known share.
func unknownWeek(v *Report) string { return "no reading since its week reset " + when(v.Ended) }

// PickedBlind is in Pick's why when it picked an unsure row, as no measured one had room.
const PickedBlind = "picked as no measured account has room"

// Reserve is the Max account used last (owner 11-19: "nothing moves to or starts on account max1 until max2 and max3
// are both at their limit").
const Reserve = "max1"

// reserveHeld says why Reserve may not be picked yet, "" when every other Max row is at its limit (or there is no
// other, or no Reserve row); full lists those rows with their shares. A row is at its limit as Pick passes one over:
// full by Room (a 5h window at 100% too), or its 7d share at Limit; a project's lower pause_at pauses that project and
// puts no account at its limit. A row not signed in, not measured, or unknown (its newest reading is from an earlier
// week) is not: max1 is the last resort after those too, so it stays held until they are measured at their limit.
func reserveHeld(r *Report, as *Accounts) (held, full string) {
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
		case a.Login == "":
			room = append(room, a.Name+" not signed in")
		case v == nil || v.Ended.IsZero() && v.Reset.IsZero():
			room = append(room, a.Name+" quota not measured")
		case !v.Ended.IsZero():
			room = append(room, a.Name+" quota unknown ("+unknownWeek(v)+")")
		default:
			share := fmt.Sprintf("%s 7d %.0f%%", a.Name, math.Floor(v.SevenPct))
			if verdict, _ := Room(v); verdict == "full" || v.SevenPct >= Limit {
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
		rank             int // unsure rows: 0 read under 90%, 1 not read this week, 2 read at 90% or more
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
		switch st, why, v := Standing(r, a.Login, limit); st {
		case "unsure": // tried only when no measured row has room
			if why == "" {
				unmeasured = append(unmeasured, a.Name)
			} else {
				unmeasured = append(unmeasured, a.Name+" ("+why+")")
			}
			// Of the unsure rows: first one read this week under 90%, then one whose share this week is not known (never
			// read, or its week reset since), then one read this week at 90% or more (a share only rises); within a rank,
			// the soonest reset (a reset does not age; one not known comes last).
			p.share, p.rank = why, 1
			if v != nil && v.Ended.IsZero() {
				p.reset = v.Reset
				if p.rank = 2; v.SevenPct < 90 && v.FivePct < 90 {
					p.rank = 0
				}
			}
			if blind == nil || p.rank < blind.rank ||
				p.rank == blind.rank && !p.reset.IsZero() && (blind.reset.IsZero() || p.reset.Before(blind.reset)) {
				blind = &p
			}
			continue
		case "full":
			full = append(full, a.Name+" "+why)
			continue
		default:
			p.reset, p.share = v.Reset, why
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
			why += "; " + PickedBlind
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
		p := filepath.Join(d, "projects")
		if root == anyPersona {
			if fi, err := os.Stat(p); err != nil || !fi.IsDir() { // not set up for any persona
				continue
			}
		} else if !Sees(d, root) {
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
