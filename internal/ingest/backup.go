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
	"strings"
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
		return "", syncDir(dir) // a sync that failed after the rename is retried here
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil { // a new home has none yet (Crius 10-17 05:47)
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
	// Temp files left by a crash (any day) would block this run or stay forever. The caller holds the writer
	// lock, so no other backup is writing them.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") && e.Type().IsRegular() {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				return "", err
			}
		}
	}
	// Made here with mode 600, so the raw copy is never readable by others; VACUUM INTO accepts an empty file.
	f, err := os.OpenFile(snap, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
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
	// On disk before the rename, so a power cut cannot leave a short file under the final name.
	err = errors.Join(err, zw.Close(), out.Sync(), out.Close())
	if err != nil {
		return "", err
	}
	if err := os.Rename(gzTmp, dest); err != nil {
		return "", err
	}
	if err := syncDir(dir); err != nil {
		return dest, err
	}
	entries, err = os.ReadDir(dir)
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

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
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
