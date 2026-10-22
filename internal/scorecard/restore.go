package scorecard

// The restore-after-expiry test (design §3c), weekly: restore the newest backup into a scratch store, regenerate each
// matured written week from it, and compare with that file's latest revision. Any difference fails. Transcripts are
// out of reach by construction: the scratch home holds only the restored record.db, and Build reads only the store,
// so a week that needs a transcript to regenerate cannot pass.

import (
	"compress/gzip"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/orzazade/crius/internal/quota"
	"github.com/orzazade/crius/internal/redact"
	"github.com/orzazade/crius/internal/store"
)

// Restore is one run of the test, saved as state/restore-test.json.
type Restore struct {
	Ran    string // RFC3339 UTC
	Backup string // the backup file restored
	Status string // pass, fail, or none (no matured week written yet)
	Text   string // why, for people
	Weeks  []RestoreWeek
}

// RestoreWeek is one written week's result: "same", or what differs and why.
type RestoreWeek struct{ Week, Result string }

func restorePath(home string) string { return filepath.Join(home, "state", "restore-test.json") }

// RestoreTest runs the test and saves its result; the error is only for a result that could not be saved.
func RestoreTest(home string, p quota.Projects, now time.Time) (*Restore, error) {
	r := &Restore{Ran: now.UTC().Format(stamp)}
	if err := restore(home, p, now, r); err != nil {
		r.Status, r.Text = "fail", err.Error()
	}
	r.Text, _ = redact.Redact(r.Text)
	for i := range r.Weeks {
		r.Weeks[i].Result, _ = redact.Redact(r.Weeks[i].Result)
	}
	b, err := json.MarshalIndent(r, "", " ")
	if err != nil {
		return nil, err
	}
	return r, os.WriteFile(restorePath(home), append(b, '\n'), 0o600)
}

// ReadRestore gives the last saved result, or nil when the test never ran.
func ReadRestore(home string) (*Restore, error) {
	b, err := os.ReadFile(restorePath(home))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r Restore
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", restorePath(home), err)
	}
	return &r, nil
}

func restore(home string, p quota.Projects, now time.Time, r *Restore) error {
	names, err := filepath.Glob(filepath.Join(home, "backup", "record-*.db.gz"))
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return fmt.Errorf("no backup in %s", filepath.Join(home, "backup"))
	}
	slices.Sort(names) // record-YYYY-MM-DD: the newest day last
	newest := names[len(names)-1]
	r.Backup = filepath.Base(newest)

	scratch, err := os.MkdirTemp("", "crius-restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	if err := os.Mkdir(filepath.Join(scratch, "state"), 0o700); err != nil {
		return err
	}
	if err := gunzip(newest, filepath.Join(scratch, "state", "record.db")); err != nil {
		return fmt.Errorf("restore %s: %w", r.Backup, err)
	}
	db, err := store.Open(scratch)
	if err != nil {
		return fmt.Errorf("open restored %s: %w", r.Backup, err)
	}
	defer db.Close()
	var check string
	if err := db.QueryRow("pragma integrity_check").Scan(&check); err != nil || check != "ok" {
		return fmt.Errorf("restored %s: integrity_check %q %v", r.Backup, check, err)
	}
	var lastEvent int64
	if err := db.QueryRow("select coalesce(max(id), 0) from event").Scan(&lastEvent); err != nil {
		return err
	}

	// A week is written as <name>.md and <name>.json: either one alone still weeks the week (review of P5).
	var weeks []string
	for _, ext := range []string{".md", ".json"} {
		files, err := filepath.Glob(filepath.Join(home, "state", "scorecards", "*"+ext))
		if err != nil {
			return err
		}
		for _, f := range files {
			weeks = append(weeks, strings.TrimSuffix(filepath.Base(f), ext))
		}
	}
	slices.Sort(weeks)
	weeks = slices.Compact(weeks)
	var same, failed []string
	provisional := 0
	for _, name := range weeks {
		base := filepath.Join(home, "state", "scorecards", name)
		w, err := WeekEnding(db, name)
		if err != nil {
			r.Weeks = append(r.Weeks, RestoreWeek{name, "the restored store has no such week: " + err.Error()})
			failed = append(failed, name)
			continue
		}
		if now.Before(w.End.Add(mature)) {
			provisional++
			continue
		}
		var old written
		b, err := os.ReadFile(base + ".json")
		if err == nil {
			err = json.Unmarshal(b, &old)
		}
		if err == nil && old.Def == "" {
			err = errors.New("no def: not a scorecard numbers file")
		}
		if err != nil {
			r.Weeks = append(r.Weeks, RestoreWeek{name, "numbers file: " + err.Error()})
			failed = append(failed, name)
			continue
		}
		if old.Def != Def {
			r.Weeks = append(r.Weeks, RestoreWeek{name, fmt.Sprintf("not compared: written with def %s, not %s", old.Def, Def)})
			continue
		}
		res, err := compareWeek(db, base, w, old, lastEvent, p, now)
		if err != nil {
			return fmt.Errorf("week %s: %w", name, err)
		}
		r.Weeks = append(r.Weeks, RestoreWeek{name, res})
		if res == "same" {
			same = append(same, name)
		} else {
			failed = append(failed, name)
		}
	}
	notCompared := fmt.Sprintf("%d provisional weeks not compared", provisional)
	switch {
	case len(failed) > 0:
		r.Status = "fail"
		for _, wk := range r.Weeks {
			if wk.Week == failed[0] {
				r.Text = fmt.Sprintf("week %s: %s", wk.Week, wk.Result)
			}
		}
		if len(failed) > 1 {
			r.Text += fmt.Sprintf(" (and %d more weeks)", len(failed)-1)
		}
	case len(same) == 0:
		r.Status = "none"
		r.Text = fmt.Sprintf("no matured week written yet; %s restored, integrity ok; %s", r.Backup, notCompared)
	default:
		r.Status = "pass"
		r.Text = fmt.Sprintf("%d matured weeks same as their files (newest %s) from %s; %s", len(same),
			same[len(same)-1], r.Backup, notCompared)
	}
	return nil
}

func gunzip(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	zr, err := gzip.NewReader(in)
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, zr)
	return errors.Join(err, zr.Close(), out.Close())
}

// compareWeek regenerates week w from the restored store and compares it with base.md and base.json as last written.
func compareWeek(db *sql.DB, base string, w Week, old written, lastEvent int64, p quota.Projects, now time.Time) (string, error) {
	var diff []string
	md, err := os.ReadFile(base + ".md")
	if err != nil {
		diff = append(diff, err.Error())
	} else if mdHash(md) != old.MD {
		diff = append(diff, "the .md is not what crius-rec wrote")
	}
	c, err := Build(db, w, p, now)
	if err != nil {
		return "", err
	}
	if d := changes(old.Numbers, c.Numbers); d != "" {
		diff = append(diff, d)
	}
	if !slices.Equal(old.Items, c.Items) {
		diff = append(diff, fmt.Sprintf("outcomes %v -> %v", old.Items, c.Items))
	}
	if !slices.Equal(old.Children, c.Children) {
		diff = append(diff, fmt.Sprintf("child tasks %v -> %v", old.Children, c.Children))
	}
	if len(diff) == 0 {
		return "same", nil
	}
	why := ""
	if old.Event > lastEvent {
		why = fmt.Sprintf("the file was written after this backup (event %d > %d)", old.Event, lastEvent)
	} else if why, err = since(db, old.Event, slices.Concat(old.Items, old.Children, c.Items, c.Children)); err != nil {
		return "", err
	}
	return strings.Join(diff, "; ") + " (" + why + ")", nil
}

func mdHash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
