//go:build !linux && !windows

package daemoncontrol

import (
	"path/filepath"
	"strconv"
	"strings"
)

// processes identifies executables with ps. Its flattened argv is unsuitable
// for relaunching, so Detect refuses automatic restarts outside launchd.
func (s realSystem) processes() ([]process, error) {
	out, err := s.run("ps", "-U", strconv.Itoa(s.uid()), "-ww", "-o", "pid=,comm=")
	if err != nil {
		return nil, err
	}
	var procs []process
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !filepath.IsAbs(fields[1]) {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		exe := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), fields[0]))
		procs = append(procs, process{PID: pid, Exe: exe, Args: []string{exe}})
	}
	return procs, nil
}

func processIsZombie(int) bool { return false }
