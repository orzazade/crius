package ingest

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/orzazade/crius/internal/store"
	"github.com/orzazade/crius/internal/testutil"
)

func mainRun(t *testing.T, args ...string) (int, string) {
	var out, errb bytes.Buffer
	rc := Main(args, &out, &errb)
	return rc, out.String() + errb.String()
}

func TestSuccessStampsTheHeartbeat(t *testing.T) {
	x := newIC(t, cfg{})
	if rc, out := mainRun(t, "--repo", testutil.RepoRoot(t)); rc != 0 {
		t.Fatal(rc, out)
	}
	if _, err := os.Stat(filepath.Join(x.home, "heartbeat")); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(filepath.Join(x.home, "state", "record.db")); fi.Mode().Perm()&0o077 != 0 {
		t.Fatal(fi.Mode())
	}
}

func TestFailedLeakTestRefusesAndLeavesNoHeartbeat(t *testing.T) {
	x := newIC(t, cfg{})
	bad := testutil.GitRepo(t, filepath.Join(x.root, "bad"))
	testutil.Write(t, filepath.Join(bad, "x.db"), "x")
	testutil.CommitAll(t, bad)
	if rc, out := mainRun(t, "--repo", bad); rc != 4 {
		t.Fatal(rc, out)
	}
	for _, f := range []string{"heartbeat", "state/record.db"} {
		if _, err := os.Stat(filepath.Join(x.home, f)); !os.IsNotExist(err) {
			t.Errorf("%s exists", f)
		}
	}
}

// Codex P1 on the Python version: a partial run stamped the heartbeat, so repeated failures kept the watchdog quiet.
func TestPartialRunExits3AndDoesNotStamp(t *testing.T) {
	x := newIC(t, cfg{})
	writeConfig(t, x.home, x.personal, x.work, cfg{repo: filepath.Join(x.root, "gone")})
	if rc, out := mainRun(t, "--repo", testutil.RepoRoot(t)); rc != 3 {
		t.Fatal(rc, out)
	}
	if _, err := os.Stat(filepath.Join(x.home, "heartbeat")); !os.IsNotExist(err) {
		t.Fatal("partial run stamped the heartbeat")
	}
}

func TestAMissingTranscriptRootIsPartialNotEmpty(t *testing.T) {
	x := newIC(t, cfg{})
	writeConfig(t, x.home, filepath.Join(x.root, "nope"), x.work, cfg{})
	if rc, out := mainRun(t, "--repo", testutil.RepoRoot(t)); rc != 3 {
		t.Fatal(rc, out)
	}
}

func TestABusyStoreExits75(t *testing.T) {
	x := newIC(t, cfg{})
	unlock, err := store.WriterLock(x.home, true)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if rc, out := mainRun(t, "--repo", testutil.RepoRoot(t)); rc != 75 {
		t.Fatal(rc, out)
	}
}

func day(s string) time.Time { d, _ := time.ParseInLocation("2006-01-02", s, time.Local); return d }

func TestKeep7DailyAnd8Weekly(t *testing.T) {
	today := day("2026-10-16")
	var days []string
	for i := 0; i < 120; i++ {
		days = append(days, today.AddDate(0, 0, -i).Format("2006-01-02"))
	}
	kept := keepBackups(days, today)
	for _, d := range days[:7] {
		if !kept[d] {
			t.Error("daily", d)
		}
	}
	weeks := map[[2]int]bool{}
	for d := range kept {
		y, w := day(d).ISOWeek()
		weeks[[2]int{y, w}] = true
	}
	eq(t, len(weeks), 8)
	eq(t, len(kept), 13) // 7 days (2 of them also the newest of their week) + 6 older weeks
}

// Codex P2 on the Python version: after a clock rollback, backups dated after "today" were deleted.
func TestFutureDatedBackupsAreKept(t *testing.T) {
	kept := keepBackups([]string{"2026-10-20", "2026-10-16"}, day("2026-10-16"))
	if !kept["2026-10-20"] {
		t.Fatal(kept)
	}
}

func seedDB(t *testing.T, home string) {
	db, err := store.Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("create table t (x); insert into t values (42)"); err != nil {
		t.Fatal(err)
	}
}

func listDir(t *testing.T, dir string) []string {
	es, _ := os.ReadDir(dir)
	var n []string
	for _, e := range es {
		n = append(n, e.Name())
	}
	sort.Strings(n)
	return n
}

func TestBackupRunsOnceADayAfter3AndRestores(t *testing.T) {
	x := newIC(t, cfg{})
	seedDB(t, x.home)
	at := func(h, m int) time.Time { return time.Date(2026, 10, 16, h, m, 0, 0, time.Local) }
	if p, err := BackupIfDue(x.home, at(2, 59)); p != "" || err != nil {
		t.Fatal(p, err)
	}
	p, err := BackupIfDue(x.home, at(3, 1))
	if err != nil || filepath.Base(p) != "record-2026-10-16.db.gz" {
		t.Fatal(p, err)
	}
	if p2, err := BackupIfDue(x.home, at(9, 0)); p2 != "" || err != nil {
		t.Fatal(p2, err)
	}
	f, _ := os.Open(p)
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(zr)
	restored := filepath.Join(x.root, "restored.db")
	os.WriteFile(restored, raw, 0o600)
	db, _ := sql.Open("sqlite", restored)
	defer db.Close()
	var v int
	if err := db.QueryRow("select x from t").Scan(&v); err != nil || v != 42 {
		t.Fatal(v, err)
	}
	eq(t, listDir(t, filepath.Join(x.home, "backup")), []string{"record-2026-10-16.db.gz"})
}

// Codex P2 on the Python version: a failed backup left its temp files behind.
func TestAFailedBackupLeavesNoTempFiles(t *testing.T) {
	x := newIC(t, cfg{})
	seedDB(t, x.home)
	block := filepath.Join(x.home, "backup", "record-2026-10-16.db.gz.tmp")
	if err := os.Mkdir(block, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := BackupIfDue(x.home, time.Date(2026, 10, 16, 4, 0, 0, 0, time.Local)); err == nil {
		t.Fatal("expected an error")
	}
	os.Remove(block)
	eq(t, len(listDir(t, filepath.Join(x.home, "backup"))), 0)
}

func TestRedactCommonSecretShapes(t *testing.T) {
	for _, s := range []string{
		"token ghp_" + strings.Repeat("A", 36),
		"api_key=" + strings.Repeat("a", 24),
		`"api_key": "abcdefghijklmnopqrstuvwxyz123456"`, // codex P1: JSON-quoted keys
		"Authorization: Bearer " + strings.Repeat("b", 30),
		"sk-" + strings.Repeat("c", 30),
	} {
		got, n := Redact(s)
		if n == 0 || strings.Contains(got, strings.Repeat("A", 20)) || strings.Contains(got, "abcdefghijklmnop") ||
			strings.Contains(got, strings.Repeat("b", 20)) || strings.Contains(got, strings.Repeat("c", 20)) ||
			strings.Contains(got, strings.Repeat("a", 20)) {
			t.Errorf("%q -> %q", s, got)
		}
	}
	if got, n := Redact("plain words, nothing secret"); n != 0 || got != "plain words, nothing secret" {
		t.Fatal(got)
	}
}

// Codex P1 on the Python version: only the BEGIN line of a private key was replaced.
func TestRedactRemovesTheWholePrivateKeyBlock(t *testing.T) {
	body := strings.Repeat("MIIEvQIBADANBgkqhkiG9w0BAQEFAASC", 3)
	for _, s := range []string{
		"x -----BEGIN RSA PRIVATE KEY-----\n" + body + "\n-----END RSA PRIVATE KEY----- y",
		"cut -----BEGIN PRIVATE KEY-----\n" + body,
	} {
		got, _ := Redact(s)
		if strings.Contains(got, "MIIEvQ") {
			t.Errorf("%q", got)
		}
	}
}

func TestListCost(t *testing.T) {
	c, ok := ListCost("claude-opus-5-5-20261001", 1_000_000, 0, 0, 0, 0, 1_000_000)
	if !ok || c != 24 {
		t.Fatal(c, ok)
	}
	if _, ok := ListCost("<synthetic>", 1, 0, 0, 0, 0, 1); ok {
		t.Fatal("priced an unknown model")
	}
}

// Codex P2 on the Go version: a temp file left by a crash blocked every retry, and older ones stayed forever.
func TestTempFilesLeftByACrashAreCleanedAndDoNotBlock(t *testing.T) {
	x := newIC(t, cfg{})
	seedDB(t, x.home)
	dir := filepath.Join(x.home, "backup")
	for _, n := range []string{"record-2026-10-16.db.gz.tmp", "record-2026-10-15.db.gz.tmp", ".record-2026-10-15.db.tmp"} {
		testutil.Write(t, filepath.Join(dir, n), "half")
	}
	if _, err := BackupIfDue(x.home, time.Date(2026, 10, 16, 4, 0, 0, 0, time.Local)); err != nil {
		t.Fatal(err)
	}
	eq(t, listDir(t, dir), []string{"record-2026-10-16.db.gz"})
}

// Codex P2 on the Go version: every model read cache at 0.1x input, and Opus 4.0/4.1 took Opus 4.5's price.
// Rates from the bundled pricing reference (2026-10-06): cache reads 0.05x on Opus 5.5, 0.025x on Fable/Mythos 5.1.
func TestCacheReadRatesPerModelAndNoGuessedPrice(t *testing.T) {
	for model, want := range map[string]float64{"claude-opus-5-5": 0.2, "claude-fable-5-1": 0.25, "claude-mythos-5-1": 0.25,
		"claude-fable-5": 1, "claude-sonnet-5-5": 0.2, "claude-opus-4-8": 0.5} {
		if c, ok := ListCost(model, 0, 1_000_000, 0, 0, 0, 0); !ok || c != want {
			t.Errorf("%s: %v %v, want %v", model, c, ok, want)
		}
	}
	for _, m := range []string{"claude-opus-4-1-20250805", "claude-opus-4-20250514"} {
		if _, ok := ListCost(m, 1, 0, 0, 0, 0, 1); ok {
			t.Errorf("%s priced without a confirmed price", m)
		}
	}
}

// Codex P1 on the Go version: a secret-named key needed a 20-character value, so short passwords were kept.
func TestRedactShortValuesOfSecretNamedKeys(t *testing.T) {
	for s, secret := range map[string]string{"password=hunter2": "hunter2", `{"password": "pa ss!"}`: "pa ss",
		"PASSWD: s3cr3t": "s3cr3t", "api_key='k1'": "k1", "access_token: tok-9": "tok-9"} {
		if got, n := Redact(s); n == 0 || strings.Contains(got, secret) {
			t.Errorf("%q -> %q", s, got)
		}
	}
	if got, n := Redact("the password is required"); n != 0 {
		t.Fatal(got)
	}
}

// Codex P2 on the Go version: Main printed config errors and backup warnings without redaction.
func TestMainOutputIsRedacted(t *testing.T) {
	x := newIC(t, cfg{})
	secret := "sk-" + strings.Repeat("z", 30)
	p := filepath.Join(x.home, "projects.toml")
	b, _ := os.ReadFile(p)
	testutil.Write(t, p, string(b)+"\n\""+secret+"\" = 1\n")
	rc, out := mainRun(t, "--repo", testutil.RepoRoot(t))
	if rc == 0 || !strings.Contains(out, "unknown keys") || strings.Contains(out, strings.Repeat("z", 20)) {
		t.Fatal(rc, out)
	}
}

// Codex P1 on the Go version: a value with no closing quote (a cut-off credential) was kept.
func TestRedactAnUnterminatedQuotedValue(t *testing.T) {
	for _, s := range []string{`password="abcdefghijklmnop`, `api_key: 'abcdefghijklmnop`} {
		if got, n := Redact(s); n == 0 || strings.Contains(got, "abcdefghijklmnop") {
			t.Errorf("%q -> %q", s, got)
		}
	}
}

// Codex P2 on the Go version: a failed folder sync after the rename was never retried, because the next run saw
// the final file and returned success.
func TestAnExistingBackupStillNeedsAFolderSync(t *testing.T) {
	x := newIC(t, cfg{})
	now := time.Date(2026, 10, 16, 4, 0, 0, 0, time.Local)
	dir := filepath.Join(x.home, "backup")
	os.MkdirAll(dir, 0o700)
	testutil.Write(t, filepath.Join(dir, "record-2026-10-16.db.gz"), "x")
	os.Chmod(dir, 0o300) // the folder cannot be opened, so it cannot be synced
	defer os.Chmod(dir, 0o700)
	if _, err := BackupIfDue(x.home, now); err == nil {
		t.Fatal("a folder that cannot be synced passed as backed up")
	}
}
