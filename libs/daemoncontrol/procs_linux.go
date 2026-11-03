package daemoncontrol

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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
		info, err := e.Info()
		if err != nil {
			continue
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != uint32(os.Getuid()) {
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

	// A failed metadata read is not permission to silently use new settings.
	p.Dir, err = os.Readlink(filepath.Join(dir, "cwd"))
	if err != nil {
		p.LaunchError = fmt.Errorf("read working directory: %w", err)
		return p, true
	}
	env, err := os.ReadFile(filepath.Join(dir, "environ"))
	if err != nil {
		p.LaunchError = fmt.Errorf("read environment: %w", err)
		return p, true
	}
	p.Env = splitNul(env)
	out, err := os.Readlink(filepath.Join(dir, "fd", "1"))
	if err != nil {
		p.LaunchError = fmt.Errorf("read log destination: %w", err)
	} else if out == "/dev/null" || isLogFile(out) {
		p.Stdout = out
	} else {
		p.LaunchError = fmt.Errorf("cannot reopen output destination %s", out)
	}
	stderr, err := os.Readlink(filepath.Join(dir, "fd", "2"))
	if err != nil || stderr != out {
		p.LaunchError = fmt.Errorf("cannot preserve separate or unreadable stderr; redirect both streams to the same log")
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
	if len(b) == 0 {
		return []string{}
	}
	return strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
}

// Zombies still answer kill(pid, 0), but have already released their resources.
func processIsZombie(pid int) bool {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return false
	}
	end := strings.LastIndex(string(data), ") ")
	return end >= 0 && strings.HasPrefix(string(data[end+2:]), "Z ")
}
