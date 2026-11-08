package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/orzazade/crius/internal/wakewatch"
)

// claudes lists every running Claude Code process (isClaude) with the arguments and environment it was started with,
// from the kernel (kern.procargs2), not from ps text, where a value with a space cannot be told from the next variable.
// A process that ended after the listing (gone, exiting or a zombie) is left out; one whose read fails otherwise is
// listed with its error, so the others are still checked (review of the check P2-1).
func claudes() ([]wakewatch.Claude, error) {
	ps, err := procs()
	if err != nil {
		return nil, err
	}
	var out []wakewatch.Claude
	for _, p := range ps {
		if !isClaude(strings.Fields(p.Command)) {
			continue
		}
		args, env, err := procArgs(p.PID)
		if err != nil {
			if ended(p.PID) {
				continue
			}
			out = append(out, wakewatch.Claude{PID: p.PID, Err: err})
			continue
		}
		out = append(out, wakewatch.Claude{PID: p.PID, Args: args, Env: env})
	}
	return out, nil
}

// isClaude says a ps command is Claude Code: its first word is a file named claude (the CLI, its daemon and pty hosts,
// the app), or a release under .../claude/versions/ (a session the daemon resumes runs as .../versions/2.1.319, review
// of the check P1), or a word is a file of the npm package (node .../@anthropic-ai/claude-code/cli.js).
func isClaude(f []string) bool {
	if len(f) == 0 {
		return false
	}
	dir := filepath.Dir(f[0])
	if filepath.Base(f[0]) == "claude" || filepath.Base(dir) == "versions" && filepath.Base(filepath.Dir(dir)) == "claude" {
		return true
	}
	for _, w := range f {
		if strings.Contains(w, "/@anthropic-ai/claude-code/") {
			return true
		}
	}
	return false
}

// ended says the process is gone, exiting (P_WEXIT) or a zombie (SZOMB): its arguments go before its pid does.
func ended(pid int) bool {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	return errors.Is(unix.Kill(pid, 0), unix.ESRCH) ||
		err == nil && (k.Proc.P_pid != int32(pid) || k.Proc.P_stat == szomb || k.Proc.P_flag&pWexit != 0)
}

const (
	szomb  = 5      // p_stat of a zombie (sys/proc.h)
	pWexit = 0x2000 // p_flag P_WEXIT: working on exiting (sys/proc.h)
)

// procArgs reads the arguments and the environment a process was started with.
func procArgs(pid int) ([]string, map[string]string, error) {
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil, nil, fmt.Errorf("sysctl kern.procargs2: %v", err)
	}
	return parseProcArgs(b)
}

// parseProcArgs reads a kern.procargs2 buffer: argc (32 bits, little-endian), the executable path, NUL padding, argc
// arguments, then the environment as NUL-ended name=value strings, ended by an empty one or the buffer's end.
func parseProcArgs(b []byte) ([]string, map[string]string, error) {
	if len(b) < 4 {
		return nil, nil, fmt.Errorf("procargs of %d bytes", len(b))
	}
	argc := int(binary.LittleEndian.Uint32(b))
	rest := b[4:]
	i := bytes.IndexByte(rest, 0)
	if i < 0 {
		return nil, nil, errors.New("procargs: no end to the executable path")
	}
	rest = bytes.TrimLeft(rest[i:], "\x00")
	var args []string
	for n := 0; n < argc; n++ {
		i := bytes.IndexByte(rest, 0)
		if i < 0 {
			return nil, nil, fmt.Errorf("procargs: %d of %d arguments", n, argc)
		}
		args, rest = append(args, string(rest[:i])), rest[i+1:]
	}
	env := map[string]string{}
	for len(rest) > 0 {
		i := bytes.IndexByte(rest, 0)
		if i == 0 {
			break
		}
		if i < 0 {
			i = len(rest)
		}
		if k, v, ok := strings.Cut(string(rest[:i]), "="); ok {
			env[k] = v
		}
		rest = rest[min(i+1, len(rest)):]
	}
	return args, env, nil
}
