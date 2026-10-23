package quota

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// The accounts file (owner 10-23, several Max accounts for Quotes): ~/.crius/accounts.toml has one row per Claude login
// tabs may run on, with its name, plan and config folders. A new account is one row plus the owner's sign-in in its
// folder. The login, not the folder, is the account (Crius 13:41): several folders may share one login (~/.claude and
// ~/.claude-work-max), and each weekly limit belongs to a login. Each window's reset comes from the samples.
//
//	[[account]]
//	name = "max1"
//	plan = "max"
//	dirs = ["~/.claude", "~/.claude-work-max"]

// Acct is one row of the accounts file. Login is the login its folders are signed into ("" when none is).
type Acct struct {
	Name, Plan, Login string
	Dirs              []string // config folders, ~ expanded
}

// Accounts is the accounts file read with each row's login.
type Accounts struct {
	Rows []Acct
}

var acctName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// LoginOf is the login the config folder dir is signed into: the first 12 hex digits of the sha256 of its account id,
// never the id, a token or an email. "" when the folder has no .claude.json or no login in it. The default folder
// ~/.claude, Claude Code's with no CLAUDE_CONFIG_DIR, keeps it beside itself in ~/.claude.json.
func LoginOf(dir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(dir, ".claude.json"))
	if h, _ := os.UserHomeDir(); errors.Is(err, os.ErrNotExist) && h != "" && samePath(dir) == samePath(filepath.Join(h, ".claude")) {
		b, err = os.ReadFile(filepath.Join(h, ".claude.json"))
	}
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	var c struct {
		Acct *struct {
			UUID string `json:"accountUuid"`
		} `json:"oauthAccount"`
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return "", fmt.Errorf("%s: %w", filepath.Join(dir, ".claude.json"), err)
	}
	if c.Acct == nil || c.Acct.UUID == "" {
		return "", nil
	}
	h := sha256.Sum256([]byte(c.Acct.UUID))
	return hex.EncodeToString(h[:])[:12], nil
}

// samePath is a folder as the disk names it, so ~/.claude-personal (a link to ~/.claude) is ~/.claude.
func samePath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// LoadAccounts reads home/accounts.toml and the login of each row's folders. No file is no rows. Unknown keys, a bad
// name or plan, a row without folders, a name or folder in two rows, a row whose folders hold two logins and two rows
// on one login are errors: each would file one account's usage under another.
func LoadAccounts(home string) (*Accounts, error) {
	path := filepath.Join(home, "accounts.toml")
	var raw struct {
		Account []struct {
			Name, Plan string
			Dirs       []string
		}
	}
	md, err := toml.DecodeFile(path, &raw)
	if errors.Is(err, os.ErrNotExist) {
		return &Accounts{}, nil
	} else if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if u := md.Undecoded(); len(u) > 0 {
		return nil, fmt.Errorf("%s: unknown keys %v", path, u)
	}
	as := &Accounts{}
	names, dirs, logins := map[string]bool{}, map[string]string{}, map[string]string{}
	for _, r := range raw.Account {
		switch {
		case !acctName.MatchString(r.Name):
			return nil, fmt.Errorf("%s: account name %q: want lower-case letters, digits and -", path, r.Name)
		case names[r.Name]:
			return nil, fmt.Errorf("%s: account %s is in two rows", path, r.Name)
		case r.Plan != "max" && r.Plan != "enterprise":
			return nil, fmt.Errorf("%s: account %s: plan %q, want max or enterprise", path, r.Name, r.Plan)
		case len(r.Dirs) == 0:
			return nil, fmt.Errorf("%s: account %s has no dirs", path, r.Name)
		}
		names[r.Name] = true
		a := Acct{Name: r.Name, Plan: r.Plan}
		for _, d := range r.Dirs {
			d = expandHome(d)
			if o, ok := dirs[samePath(d)]; ok && o == r.Name {
				continue // a link to a folder of this row: the same folder
			} else if ok {
				return nil, fmt.Errorf("%s: folder %s is in accounts %s and %s", path, d, o, r.Name)
			}
			dirs[samePath(d)] = r.Name
			l, err := LoginOf(d)
			if err != nil {
				return nil, fmt.Errorf("%s: account %s: %w", path, r.Name, err)
			}
			if l != "" && a.Login != "" && l != a.Login {
				return nil, fmt.Errorf("%s: account %s: its folders are signed into two logins", path, r.Name)
			}
			if l != "" {
				a.Login = l
			}
			a.Dirs = append(a.Dirs, d)
		}
		if a.Login != "" {
			if o, ok := logins[a.Login]; ok {
				return nil, fmt.Errorf("%s: accounts %s and %s are one login", path, o, r.Name)
			}
			logins[a.Login] = r.Name
		}
		as.Rows = append(as.Rows, a)
	}
	return as, nil
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[1:])
		}
	}
	return p
}

// Of is the row of a login, nil when no row's folder is signed into it (or when as is nil: no accounts file read).
func (as *Accounts) Of(login string) *Acct {
	if as == nil {
		return nil
	}
	for i := range as.Rows {
		if login != "" && as.Rows[i].Login == login {
			return &as.Rows[i]
		}
	}
	return nil
}

// Name is the row name of a login, or "login <hash>" for a login no row lists.
func (as *Accounts) Name(login string) string {
	if a := as.Of(login); a != nil {
		return a.Name
	}
	return "login " + login
}

// Dir is the row listing the config folder dir, nil when none does (or when as is nil).
func (as *Accounts) Dir(dir string) *Acct {
	if as == nil {
		return nil
	}
	for i := range as.Rows {
		for _, d := range as.Rows[i].Dirs {
			if samePath(d) == samePath(dir) {
				return &as.Rows[i]
			}
		}
	}
	return nil
}
