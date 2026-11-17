package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/orzazade/crius/internal/playbook"
	"github.com/orzazade/crius/internal/store"
)

// The template reads and writes of crius-wakewatch restart --from-ready (wakewatch.FromReadyDeps): host "mac" is this
// machine, any other host is a box in the playbook's hosts.json, reached over its ssh. Paths are checked by
// wakewatch.ReadBriefPlaces to plain path characters, so the quoting below never meets a quote.

// fromReadyArgs reads the words after the role of restart --from-ready: --from-ready once, --dry-run at most once, in
// any order, nothing else.
func fromReadyArgs(rest []string) (dry, ok bool) {
	var from int
	for _, a := range rest {
		switch {
		case a == "--from-ready" && from == 0:
			from++
		case a == "--dry-run" && !dry:
			dry = true
		default:
			return false, false
		}
	}
	if from != 1 {
		return false, false
	}
	return dry, true
}

// briefRead gives a template's text.
func briefRead(host, path string) ([]byte, error) {
	if host == "mac" {
		p, err := macPath(path)
		if err != nil {
			return nil, err
		}
		return os.ReadFile(p)
	}
	return onBoxHost(host, nil, "cat -- "+shQuote(path))
}

// briefSave keeps a copy of the template as prev (refused when prev exists), then writes body over the template with
// its mode kept (some are read-only on purpose: the write is a new file renamed over it, as sed -i does).
func briefSave(host, path, prev string, body []byte) error {
	if host == "mac" {
		p, err := macPath(path)
		if err != nil {
			return err
		}
		q, err := macPath(prev)
		if err != nil {
			return err
		}
		return macSave(p, q, body)
	}
	_, err := onBoxHost(host, bytes.NewReader(body), boxSaveCmd(path, prev))
	return err
}

// macSave is briefSave on this machine, with absolute paths.
func macSave(path, prev string, body []byte) error {
	old, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(prev, os.O_WRONLY|os.O_CREATE|os.O_EXCL, st.Mode().Perm())
	if err != nil {
		return fmt.Errorf("could not keep the old copy: %w", err)
	}
	if _, err := f.Write(old); err != nil {
		f.Close()
		return fmt.Errorf("could not keep the old copy: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("could not keep the old copy: %w", err)
	}
	if err := os.Chmod(prev, st.Mode().Perm()); err != nil { // the umask took bits off at create
		return fmt.Errorf("could not keep the old copy: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".new-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(body)
	if cerr := tmp.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp.Name(), st.Mode().Perm())
	}
	if werr == nil {
		werr = os.Rename(tmp.Name(), path)
	}
	if werr != nil {
		os.Remove(tmp.Name())
	}
	return werr
}

// boxSaveCmd is briefSave as one shell command on a Linux box, the new text on its stdin.
func boxSaveCmd(path, prev string) string {
	p, q, t := shQuote(path), shQuote(prev), shQuote(path+".new")
	return "set -e; if [ -e " + q + " ]; then echo " + shQuote("old copy "+prev+" exists") + " >&2; exit 1; fi; " +
		"cp -p -- " + p + " " + q + "; cat > " + t + "; chmod --reference " + p + " -- " + t + "; mv -f -- " + t + " " + p
}

// shQuote quotes a checked path for a remote shell, leaving a leading ~/ outside the quotes so the box expands it.
func shQuote(path string) string {
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		return "~/'" + rest + "'"
	}
	return "'" + path + "'"
}

// macPath expands a leading ~/ to this user's home.
func macPath(path string) (string, error) {
	rest, ok := strings.CutPrefix(path, "~/")
	if !ok {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, rest), nil
}

// onBoxHost runs a shell command on a box of the playbook's hosts.json over its ssh, with stdin when given.
func onBoxHost(box string, stdin io.Reader, command string) ([]byte, error) {
	path := filepath.Join(store.Home(), "state", "playbook", "hosts.json")
	hosts, err := playbook.LoadHosts(path)
	if err != nil {
		return nil, err
	}
	for _, h := range hosts {
		if h.Name == box && len(h.SSH) > 0 {
			args := append(append([]string{}, h.SSH[1:]...), h.Target, command)
			return run(stdin, h.SSH[0], args...)
		}
	}
	return nil, fmt.Errorf("no box %q in %s", box, path)
}
