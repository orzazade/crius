package playbook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Host is another machine whose sessions the playbook learns from and shows to.
// It is set in a local hosts.json, never in this repo (addresses and keys).
type Host struct {
	Name   string   `json:"name"`   // cache folder and report name: legion, thinkpad
	Target string   `json:"target"` // user@address
	SSH    []string `json:"ssh"`    // the ssh command and its options, without the target
	Roots  []string `json:"roots"`  // absolute transcript roots on the host
	Book   string   `json:"book"`   // absolute folder the book is copied to on the host ("" = no copy)
	Rsync  string   `json:"rsync"`  // local rsync 3.x (default "rsync")
}

// HostResult is what one host gave this run.
type HostResult struct {
	Name   string
	Files  int    // transcript files pulled (new or grown)
	Pushed bool   // the book was copied there
	Err    string // why the host could not be read or written; "" when fine
}

// pullDays is how far back a remote transcript may have changed to be pulled.
// A session older than that has no new lines; the cursor skips old ones anyway.
const pullDays = 14

// hostTimeout bounds each ssh or rsync call, so a hung host cannot hold the night.
const hostTimeout = 15 * time.Minute

// LoadHosts reads a hosts.json; no file is no hosts.
func LoadHosts(path string) ([]Host, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var hs []Host
	if err := json.Unmarshal(data, &hs); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, h := range hs {
		if h.Name == "" || h.Target == "" || len(h.SSH) == 0 {
			return nil, fmt.Errorf("%s: host %q needs name, target and ssh", path, h.Name)
		}
	}
	return hs, nil
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func (h Host) rsync() string {
	if h.Rsync == "" {
		return "rsync"
	}
	return h.Rsync
}

// remoteShell is the ssh command as rsync's -e wants it: one shell string.
func (h Host) remoteShell() string {
	var q []string
	for _, a := range h.SSH {
		q = append(q, shq(a))
	}
	return strings.Join(q, " ")
}

// run runs name with args and stdin, with the host timeout; its error carries the last output line.
func run(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, hostTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if i := strings.LastIndex(msg, "\n"); i >= 0 {
			msg = msg[i+1:]
		}
		return nil, fmt.Errorf("%s: %v: %s", filepath.Base(name), err, msg)
	}
	return out.Bytes(), nil
}

func runOut(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}

// pull copies the host's transcripts changed in the last pullDays into
// cache/<name>/<root index>/. Transcripts only grow, so rsync appends to the
// cached copy (--append) and never sends a file again in full. It returns the
// cache folder of the host and how many files came.
func pull(ctx context.Context, h Host, cache string) (string, int, error) {
	dst := filepath.Join(cache, h.Name)
	files := 0
	for i, root := range h.Roots {
		list, err := run(ctx, nil, h.SSH[0], append(h.SSH[1:], h.Target,
			fmt.Sprintf("cd %s && find -L . -name '*.jsonl' -mtime -%d", shq(root), pullDays))...)
		if err != nil {
			return dst, files, fmt.Errorf("list %s: %w", root, err)
		}
		if len(bytes.TrimSpace(list)) == 0 {
			continue
		}
		d := filepath.Join(dst, strconv.Itoa(i))
		if err := os.MkdirAll(d, 0o755); err != nil {
			return dst, files, err
		}
		out, err := run(ctx, list, h.rsync(), "-rtL", "--append", "--files-from=-", "--out-format=%n",
			"-e", h.remoteShell(), h.Target+":"+strings.TrimRight(root, "/")+"/", d+"/")
		if err != nil {
			return dst, files, fmt.Errorf("pull %s: %w", root, err)
		}
		for _, l := range strings.Split(string(out), "\n") {
			if strings.HasSuffix(l, ".jsonl") {
				files++
			}
		}
	}
	return dst, files, nil
}

// push copies the book folder to the host (its git history stays here).
func push(ctx context.Context, h Host, book string) error {
	_, err := run(ctx, nil, h.rsync(), "-rt", "--delete", "--mkpath", "--exclude=.git",
		"-e", h.remoteShell(), strings.TrimRight(book, "/")+"/", h.Target+":"+strings.TrimRight(h.Book, "/")+"/")
	return err
}

// HostLine is the hosts part of the report: each host ok with its file count,
// or DOWN with the reason.
func HostLine(hs []HostResult) string {
	if len(hs) == 0 {
		return ""
	}
	var p []string
	for _, h := range hs {
		if h.Err != "" {
			p = append(p, fmt.Sprintf("%s DOWN (%s)", h.Name, h.Err))
			continue
		}
		s := fmt.Sprintf("%s ok, %d files", h.Name, h.Files)
		if h.Pushed {
			s += ", book pushed"
		}
		p = append(p, s)
	}
	return "hosts: " + strings.Join(p, "; ")
}

// ProjectMap is project -> its working folders, as absolute paths. Sessions on
// the other hosts run in the Mac's paths, so the map made on the Mac fits there.
type ProjectMap map[string][]string

// MapFile is the project map's name in the book folder.
const MapFile = "projects.json"

// Lookup gives the project with the longest folder that holds cwd, or "".
func (m ProjectMap) Lookup(cwd string) string {
	best, n := "", -1
	for p, dirs := range m {
		for _, d := range dirs {
			d = strings.TrimRight(d, "/")
			if (cwd == d || strings.HasPrefix(cwd, d+"/")) && len(d) > n {
				best, n = p, len(d)
			}
		}
	}
	return best
}

// LoadProjectMap reads the map from the book folder.
func LoadProjectMap(path string) (ProjectMap, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m ProjectMap
	return m, json.Unmarshal(data, &m)
}
