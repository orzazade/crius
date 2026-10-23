package quota

import (
	"fmt"
	"math"
	"path/filepath"
	"strings"
)

// Pick is the Max account a new session starts on, and the config folder to start it in (owner 10-23): of the rows
// with plan max (want "max"), or the row named want, the one with the lowest 7d share. A folder counts only when it is
// signed in and its projects/ is root, the persona's transcript folder (an aitch link of the persona's folder), so
// crius files the session. A row no line measured, or whose week ended with no sample since, counts 0%; one full
// (Room) or at pauseAt (0: none) is passed over, and when every row is, Pick says which and at what share. With no
// row that has such a folder and want "max", Pick returns "" and the launch goes as before.
func Pick(r *Report, as *Accounts, root, want string, pauseAt int) (name, dir string, err error) {
	if as == nil {
		as = &Accounts{}
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
			return "", "", fmt.Errorf("accounts.toml has no Max account %s", want)
		}
	}
	best := math.Inf(1)
	var full []string
	for _, a := range rows {
		d, err := linked(a, root)
		if err != nil {
			return "", "", err
		}
		if a.Plan != "max" || d == "" {
			continue
		}
		pct, at := 0.0, ""
		if v := r.Account(a.Login); v != nil && v.Ended.IsZero() {
			pct = v.SevenPct
			if verdict, _ := Room(v); verdict == "full" || pauseAt > 0 && v.SevenPct >= float64(pauseAt) {
				at = fmt.Sprintf("7d %.0f%%", math.Floor(v.SevenPct))
				if v.FivePct >= 100 {
					at += ", 5h 100%"
				}
			}
		}
		if at != "" {
			full = append(full, a.Name+" "+at)
		} else if pct < best {
			best, name, dir = pct, a.Name, d
		}
	}
	switch {
	case name != "":
		return name, dir, nil
	case want != "max" && full != nil:
		return "", "", fmt.Errorf("%s is at its limit: %s", want, strings.TrimPrefix(full[0], want+" "))
	case want != "max":
		return "", "", fmt.Errorf("%s has no folder linked to %s and signed in", want, root)
	case full != nil:
		return "", "", fmt.Errorf("every Max account is at its limit: %s", strings.Join(full, ", "))
	}
	return "", "", nil
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
