package ingest

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

var revertsRe = regexp.MustCompile(`This reverts commit ([0-9a-f]{40})`)

// afterHeadHook is a test seam: it runs after the branch is resolved and before `git log`.
var afterHeadHook func()

func gitRun(repo string, args ...string) (string, error) {
	var out, errb bytes.Buffer
	cmd := exec.Command("git", append([]string{"-C", repo, "-c", "log.showSignature=false"}, args...)...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %v: %s", args[0], err, head(strings.TrimSpace(errb.String()), 200))
	}
	return out.String(), nil
}

// defaultBranch returns the default branch and its sha: origin's HEAD, then main, then master. Of the local branch
// and origin's copy, the newer one: work pushed from a worktree branch leaves the local branch behind. When they have
// diverged neither is newer, and that is an error, not a guess.
func defaultBranch(repo string) (name, sha string, err error) {
	var names []string
	if out, err := gitRun(repo, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if _, n, ok := strings.Cut(strings.TrimSpace(out), "/"); ok {
			names = append(names, n)
		}
	}
	for _, n := range append(names, "main", "master") {
		local, lerr := gitRun(repo, "rev-parse", "--verify", "-q", "refs/heads/"+n)
		remote, rerr := gitRun(repo, "rev-parse", "--verify", "-q", "refs/remotes/origin/"+n)
		local, remote = strings.TrimSpace(local), strings.TrimSpace(remote)
		switch {
		case lerr != nil && rerr != nil:
			continue
		case rerr != nil:
			return n, local, nil
		case lerr != nil:
			return n, remote, nil
		}
		if _, err := gitRun(repo, "merge-base", "--is-ancestor", local, remote); err == nil {
			return n, remote, nil
		}
		if _, err := gitRun(repo, "merge-base", "--is-ancestor", remote, local); err == nil {
			return n, local, nil
		}
		return "", "", fmt.Errorf("%s and origin/%s have diverged; neither is read until one contains the other", n, n)
	}
	return "", "", fmt.Errorf("no default branch (origin HEAD, main, master)")
}

func (r *runner) ingestGit() {
	for _, p := range r.cfg.projects {
		for _, repo := range p.repos {
			if err := r.gitRepo(p.slug, repo); err != nil {
				r.warn(true, "git %s: %v", repo, err)
			}
		}
	}
	// A commit made in a session shows its short sha in the tool result; link the earliest such session. A short
	// sha that matches more than one commit (in any repo) links none. Driven from the events, with a sha range on
	// the key (hex sorts below 'g'), so it does not scan every event for every commit.
	if _, err := r.db.Exec("update git_commit set session=m.session from (select g.sha, e.session," +
		" row_number() over (partition by g.sha order by e.ts) as n, count(*) over (partition by e.id) as k" +
		" from event e join git_commit g on g.sha >= json_extract(e.refs, '$.sha[0]')" +
		" and g.sha < json_extract(e.refs, '$.sha[0]') || 'g' where e.kind='commit_made') m" +
		" where m.n=1 and m.k=1 and git_commit.sha=m.sha and git_commit.session is null"); err != nil {
		r.warn(true, "cannot link commits to sessions: %v", err)
	}
}

func (r *runner) gitRepo(slug, repo string) error {
	if _, err := gitRun(repo, "rev-parse", "--git-dir"); err != nil {
		return fmt.Errorf("repo missing or not a git repo: %v", err)
	}
	// Resolve the branch to a sha first: a commit landing after this is read by the next run, never skipped.
	_, sha, err := defaultBranch(repo)
	if err != nil {
		return err
	}
	if afterHeadHook != nil {
		afterHeadHook()
	}
	return r.inTx(func(tx *sql.Tx) error {
		c, err := getCursor(tx, "git", repo)
		if err != nil {
			return err
		}
		// A commit read before subjects were stored has none: the repo is read whole once to fill them in.
		var unread bool
		if err := tx.QueryRow("select exists (select 1 from git_commit where repo = ? and subject is null)", repo).
			Scan(&unread); err != nil {
			return err
		}
		if c.sha == sha && !unread {
			return nil
		}
		rng := sha
		if c.sha != "" && !unread {
			if _, err := gitRun(repo, "merge-base", "--is-ancestor", c.sha, sha); err == nil {
				rng = c.sha + ".." + sha
			}
		}
		// -z: file names come NUL-separated and unquoted (git quotes tabs and non-ASCII letters otherwise).
		out, err := gitRun(repo, "log", rng, "--no-color", "--name-only", "-z", "--format=%x1e%H%x1f%cI%x1f%P%x1f%s%x1f%b%x1f")
		if err != nil {
			return err
		}
		n := 0
		for _, rec := range strings.Split(out, "\x1e")[1:] {
			f := append(strings.Split(rec, "\x1f"), make([]string, 6)...)
			commit, cts, subject, body := f[0], f[1], f[3], f[4]
			t, err := time.Parse(time.RFC3339, cts)
			if err != nil {
				return fmt.Errorf("commit %s: bad time %q", commit, cts)
			}
			ts := t.UTC().Format("2006-01-02T15:04:05Z")
			files := []string{}
			for _, name := range strings.Split(f[5], "\x00") {
				name = strings.TrimPrefix(name, "\n") // the newline between the header and the names
				if name != "" && len(files) < 200 {
					files = append(files, r.text(name))
				}
			}
			fj, err := json.Marshal(files)
			if err != nil {
				return err
			}
			var reverts any
			if m := revertsRe.FindStringSubmatch(body); m != nil {
				reverts = m[1]
			}
			isRevert := strings.HasPrefix(subject, `Revert "`) || reverts != nil
			stored := r.text(subject)
			res, err := tx.Exec("insert or ignore into git_commit (sha, repo, project, ts, files, is_revert, reverts_sha,"+
				" subject) values (?,?,?,?,?,?,?,?)", commit, repo, slug, ts, string(fj), isRevert, reverts, stored)
			if err != nil {
				return err
			}
			if _, err := tx.Exec("update git_commit set subject = ? where sha = ? and subject is null", stored,
				commit); err != nil {
				return err
			}
			k, err := res.RowsAffected()
			if err != nil {
				return err
			}
			n += int(k)
			if _, err := tx.Exec("insert or ignore into evidence (kind, ref, revision, ts, source_key)"+
				" values ('commit', ?, ?, ?, ?)", commit, commit, ts, "git:"+commit); err != nil {
				return err
			}
		}
		if unread { // a commit no longer on the branch has no subject to read; '' so the repo is not read whole again
			if _, err := tx.Exec("update git_commit set subject = '' where repo = ? and subject is null", repo); err != nil {
				return err
			}
		}
		c.sha = sha
		return saveCursor(tx, "git", repo, c, n)
	})
}
