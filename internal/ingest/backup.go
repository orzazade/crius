package ingest

// Daily backup of record.db: after 03:00 local, once a day, gzip, keep 7 daily + 8 weekly (design §3c).

import (
	"compress/gzip"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/orzazade/crius/internal/store"
)

var backupName = regexp.MustCompile(`^record-(\d{4}-\d\d-\d\d)\.db\.gz$`)

// keepBackups picks the days to keep: the 7 newest, the newest of each of the 8 newest ISO weeks, and every
// day after today (a clock that went backwards must not delete the newest copy).
func keepBackups(days []string, today time.Time) map[string]bool {
	t := today.Format("2006-01-02")
	kept, seen := map[string]bool{}, map[string]bool{}
	var past []string
	for _, d := range days {
		if seen[d] {
			continue
		}
		seen[d] = true
		if d > t {
			kept[d] = true
		} else {
			past = append(past, d)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(past)))
	weeks := map[[2]int]bool{}
	for i, d := range past {
		day, err := time.Parse("2006-01-02", d)
		if err != nil {
			continue
		}
		y, w := day.ISOWeek()
		if i < 7 {
			kept[d] = true
		}
		if !weeks[[2]int{y, w}] && len(weeks) < 8 {
			weeks[[2]int{y, w}] = true
			kept[d] = true
		}
	}
	return kept
}

// BackupIfDue backs up once per day after 03:00 and returns the new file, or "" when it is not due.
func BackupIfDue(home string, now time.Time) (path string, err error) {
	if now.Hour() < 3 {
		return "", nil
	}
	dir := filepath.Join(home, "backup")
	name := "record-" + now.Format("2006-01-02") + ".db"
	dest := filepath.Join(dir, name+".gz")
	if _, err := os.Stat(dest); err == nil {
		return "", nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	snap, gzTmp := filepath.Join(dir, "."+name+".tmp"), dest+".tmp"
	gzMade := false
	defer func() {
		os.Remove(snap)
		if err != nil && gzMade {
			os.Remove(gzTmp)
		}
	}()
	os.Remove(snap) // left by a crash; VACUUM INTO needs a free name
	db, err := store.Open(home)
	if err != nil {
		return "", err
	}
	_, err = db.Exec("vacuum into ?", snap)
	db.Close()
	if err != nil {
		return "", fmt.Errorf("backup: %w", err)
	}
	if err := integrityOK(snap); err != nil {
		return "", err
	}
	out, err := os.OpenFile(gzTmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	gzMade = true
	in, err := os.Open(snap)
	if err != nil {
		out.Close()
		return "", err
	}
	zw := gzip.NewWriter(out)
	_, err = io.Copy(zw, in)
	in.Close()
	err = errors.Join(err, zw.Close(), out.Close())
	if err != nil {
		return "", err
	}
	if err := os.Rename(gzTmp, dest); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return dest, err
	}
	var days []string
	for _, e := range entries {
		if m := backupName.FindStringSubmatch(e.Name()); m != nil {
			days = append(days, m[1])
		}
	}
	keep := keepBackups(days, now)
	for _, d := range days {
		if !keep[d] {
			if err := os.Remove(filepath.Join(dir, "record-"+d+".db.gz")); err != nil {
				return dest, err
			}
		}
	}
	return dest, nil
}

func integrityOK(path string) error {
	db, err := sql.Open("sqlite", store.URI(path, "mode=ro"))
	if err != nil {
		return err
	}
	defer db.Close()
	var res string
	if err := db.QueryRow("pragma integrity_check").Scan(&res); err != nil {
		return err
	}
	if res != "ok" {
		return fmt.Errorf("backup integrity_check: %s", res)
	}
	return nil
}
