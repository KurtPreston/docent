package selfupdate

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// processes lists this user's processes from /proc. Processes it can't read,
// such as other users', are skipped.
func (realSystem) processes() ([]process, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var procs []process
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if p, ok := readProc(filepath.Join("/proc", e.Name()), pid); ok {
			procs = append(procs, p)
		}
	}
	return procs, nil
}

// readProc reads one /proc/<pid> entry.
func readProc(dir string, pid int) (process, bool) {
	exe, err := os.Readlink(filepath.Join(dir, "exe"))
	if err != nil {
		return process{}, false
	}
	cmdline, err := os.ReadFile(filepath.Join(dir, "cmdline"))
	if err != nil || len(cmdline) == 0 {
		return process{}, false
	}
	p := process{
		PID:  pid,
		Exe:  strings.TrimSuffix(exe, " (deleted)"),
		Args: splitNul(cmdline),
	}

	// Optional details used to relaunch it the same way
	p.Dir, _ = os.Readlink(filepath.Join(dir, "cwd"))
	if env, err := os.ReadFile(filepath.Join(dir, "environ")); err == nil {
		p.Env = splitNul(env)
	}
	if out, err := os.Readlink(filepath.Join(dir, "fd", "1")); err == nil && isLogFile(out) {
		p.Stdout = out
	}
	return p, true
}

// isLogFile reports whether a /proc fd target is a regular file worth
// reopening, rather than a terminal, pipe, socket, or /dev/null.
func isLogFile(target string) bool {
	if !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "/dev/") {
		return false
	}
	info, err := os.Stat(target)
	return err == nil && info.Mode().IsRegular()
}

func splitNul(b []byte) []string {
	return strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
}
