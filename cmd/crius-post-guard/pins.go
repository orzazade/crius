package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
)

// The pin file (Crius 11-06: "keep the pin list in one file so I can re-pin without a rebuild") is the one place the
// guard takes script hashes from: a JSON object of a script's base name to the SHA-256 hashes it may have, e.g.
// {"pr-build.sh": ["bbfc3d2f…"], "ado-suite.ts": ["ba3dd043…"]}. It lives at $CRIUS_HOME/state/post-guard-pins.json
// (~/.crius/state/…). testdata/pins.json is the file to install with this build: it pins the copies kept in testdata.
// The file must be a plain file of this user's, not group- or world-writable; one that is missing or unreadable asks
// and says so. Rules (which script may run where, ado-suite.ts report only) stay in code; only hashes live here.
// Known gap: any session of this user can write the file, as it can the scripts; a rule that keeps agents from
// editing it (a permissions ask on the path) belongs with the install.

// pinsPath gives the pin file's path.
var pinsPath = func() string {
	home := os.Getenv("CRIUS_HOME")
	if home == "" {
		u, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		home = filepath.Join(u, ".crius")
	}
	return filepath.Join(home, "state", "post-guard-pins.json")
}

// loadPins reads the pin file; why says why it could not.
func loadPins() (map[string][]string, string) {
	p := pinsPath()
	fi, err := os.Lstat(p)
	switch {
	case err != nil:
		return nil, "the pin file " + p + " cannot be read (" + err.Error() + ")"
	case !fi.Mode().IsRegular():
		return nil, "the pin file " + p + " is not a plain file"
	case fi.Mode().Perm()&0o022 != 0:
		return nil, "the pin file " + p + " is group- or world-writable"
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Getuid() {
		return nil, "the pin file " + p + " is not this user's"
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, "the pin file " + p + " cannot be read (" + err.Error() + ")"
	}
	var m map[string][]string
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, "the pin file " + p + " is not a JSON object of names to hash lists (" + err.Error() + ")"
	}
	return m, ""
}

// pinned asks unless p is a plain file (no symlink on its path) whose SHA-256 the pin file lists for name.
func pinned(p, name string) string {
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return asked(name + " at " + p + " is not a plain file (missing or a symlink)")
	}
	if real, err := filepath.EvalSymlinks(p); err != nil || real != p {
		return asked(name + " at " + p + " is reached through a symlink")
	}
	pins, why := loadPins()
	if why != "" {
		return asked(name + ": " + why)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return asked(name + " cannot be read: " + err.Error())
	}
	h := sha256.Sum256(b)
	if got := hex.EncodeToString(h[:]); !slices.Contains(pins[name], got) {
		return asked(name + " at " + p + " does not match the SHA-256 the guard pins (" + got[:12] + "): read the change, then pin the new hash")
	}
	return ""
}

// teamScripts are the team scripts that pass on a pin match in a lead session (Crius 11-06 item 5); ado-suite.ts only
// as `report <id>` (its one POST is in run), pass-post.sh only with verdict-check.ts beside it pinned too, merge.sh only
// for a PR into integration/96365-bN (it completes into develop too, which keeps the click).
// prTargetListRe is an integration/96365-bN branch: merge.sh completes into it on its pin.
var prTargetListRe = regexp.MustCompile(`^(refs/heads/)?integration/96365-b[0-9]+$`)

var teamScripts = map[string]bool{"merge.sh": true, "merge-ok.sh": true, "review-gate.sh": true, "pr-build.sh": true,
	"pass-post.sh": true, "ado-suite.ts": true}

// leadScratchRe is a lead scratchpad's lead/ folder: <scratch root>/<a svc_quote project>/<session>/scratchpad/lead.
var leadScratchRe = regexp.MustCompile(`^-Users-[A-Za-z0-9-]+-Projects-Appxite-svc-quote[A-Za-z0-9-]*/[0-9a-f-]{36}/scratchpad/lead$`)

// bunRunner gives why a pinned .ts file's interpreter words do not pass: only bun, with --no-env-file and
// --config=<the pinned empty-bunfig.toml>; any other flag may load code first (--preload, -r, a bunfig).
func bunRunner(runner []word) string {
	if len(runner) == 0 || !runner[0].ok || filepath.Base(runner[0].text) != "bun" {
		return "a pinned .ts file passes only when bun runs it"
	}
	for _, w := range runner[1:] {
		switch {
		case w.ok && w.text == "--no-env-file":
		case w.ok && strings.HasPrefix(w.text, "--config="):
			if why := pinned(strings.TrimPrefix(w.text, "--config="), "empty-bunfig.toml"); why != "" {
				return strings.TrimPrefix(why, reason+" ")
			}
		default:
			return "bun " + w.src + ": only --no-env-file and --config=<the pinned empty bunfig> pass with a pinned script"
		}
	}
	return ""
}

// aloneOwned gives why the script name at p could be changed by someone else ("" when it cannot): its folder is reached
// through a symlink, or it or its folder is not this user's or is group- or world-writable.
func aloneOwned(name, p string) string {
	dir := filepath.Dir(p)
	if real, err := filepath.EvalSymlinks(dir); err != nil || real != dir {
		return name + " at " + p + ": its folder is reached through a symlink"
	}
	for _, f := range []string{p, dir} {
		fi, err := os.Lstat(f)
		if err != nil {
			return name + ": " + err.Error()
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok || int(st.Uid) != os.Getuid() || fi.Mode().Perm()&0o022 != 0 {
			return name + ": " + f + " is not this user's alone (owner, or group- or world-writable)"
		}
	}
	return ""
}

// opsScripts are the read-only checks Crius's ops tab runs before every click, by name, and the folder under
// $CRIUS_HOME each lives in (owner order 11-08 item 4: on 11-07 from 20:00 crius:ops sat on its own prompt for
// click-check.sh, which runs ops-pins.sh, and answered no other tab). On their pins they pass unread: the pin stands for
// a reading of the whole file. Each one names the other ops scripts it runs, which pass only when pinned too.
var opsScripts = map[string]struct {
	dir  string
	runs []string
}{
	"click-check.sh": {"state/crius/ops-bin", []string{"state/crius/ops-pins.sh"}},
	"ops-pins.sh":    {"state/crius", nil},
}

// opsScript gives the files the ops script at p runs, itself first, and why it does not pass as a pinned ops check ("" when
// it does): its folder, its owner and mode, and the pins of it and of every ops script it runs.
func opsScript(p string) (files []string, why string) {
	name := filepath.Base(p)
	o, ok := opsScripts[name]
	if !ok {
		return nil, "not an ops script"
	}
	if want := filepath.Join(crHome(), o.dir); filepath.Dir(p) != want {
		return nil, name + " at " + p + " is not in " + want
	}
	files = []string{p}
	for _, r := range o.runs {
		files = append(files, filepath.Join(crHome(), r))
	}
	for _, f := range files {
		if why := aloneOwned(filepath.Base(f), f); why != "" {
			return files, why
		}
		if why := pinned(f, filepath.Base(f)); why != "" {
			return files, strings.TrimPrefix(why, reason+" ")
		}
	}
	return files, strings.TrimPrefix(bashEnv(name), reason+" ")
}

// teamScript gives why the script at p, run with the words after, does not pass as a pinned team script ("" when it
// does): its name, its folder (the Quotes build folder or a lead scratchpad's lead/, real folders), its owner and mode,
// its hash, and for ado-suite.ts the report subcommand.
func teamScript(p string, after []word, runDir string) string {
	name := filepath.Base(p)
	if !teamScripts[name] {
		return "not a team script"
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "no home folder"
	}
	dir := filepath.Dir(p)
	build := filepath.Join(home, ".crius/state/quotes/build")
	rel, err := filepath.Rel(scratchRoot(), dir)
	inBuild := dir == build || strings.HasPrefix(dir, build+"/")
	if !inBuild && (err != nil || !leadScratchRe.MatchString(rel)) {
		return name + " at " + p + " is outside the Quotes build folder and a lead scratchpad's lead/ folder"
	}
	if why := aloneOwned(name, p); why != "" {
		return why
	}
	if why := pinned(p, name); why != "" {
		return strings.TrimPrefix(why, reason+" ")
	}
	switch name {
	case "ado-suite.ts":
		if len(after) != 2 || !after[0].ok || after[0].text != "report" || !after[1].ok || !exactBuildRe.MatchString(after[1].text) {
			return "ado-suite.ts passes on its pin only as `report <id>`; run posts"
		}
	case "merge.sh":
		// merge.sh completes into develop as well as bN; completing into develop or main keeps the click
		if len(after) < 1 || !after[0].ok || !prIDRe.MatchString(after[0].text) {
			return "merge.sh passes on its pin only with a PR id the guard can read"
		}
		pi, err := prState(after[0].text)
		if err != nil {
			return "merge.sh: the guard cannot read the target of PR " + after[0].text
		}
		if !prTargetListRe.MatchString(pi.Target) {
			return "merge.sh completes PR " + after[0].text + " into " + pi.Target + ", which keeps the click"
		}
	case "pass-post.sh":
		if why := pinned(filepath.Join(dir, "verdict-check.ts"), "verdict-check.ts"); why != "" {
			return strings.TrimPrefix(why, reason+" ")
		}
	}
	// bun (ado-suite.ts, and pass-post.sh for verdict-check.ts) reads bunfig.toml and .env from the folder it runs in
	if name == "ado-suite.ts" || name == "pass-post.sh" {
		if runDir == "" {
			return name + ": the guard does not know the folder bun runs in"
		}
		ents, err := os.ReadDir(runDir)
		if err != nil {
			return name + ": the guard cannot list " + runDir
		}
		for _, e := range ents {
			if n := strings.ToLower(e.Name()); n == "bunfig.toml" || strings.HasPrefix(n, ".env") {
				return name + ": " + runDir + " holds " + e.Name() + ", which bun reads"
			}
		}
	}
	if why := bunEnv(home, name); why != "" {
		return strings.TrimPrefix(why, reason+" ")
	}
	if why := bashEnv(name); why != "" {
		return strings.TrimPrefix(why, reason+" ")
	}
	return ""
}
