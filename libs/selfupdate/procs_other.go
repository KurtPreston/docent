//go:build !linux && !windows

package selfupdate

import (
	"path/filepath"
	"strconv"
	"strings"
)

// processes lists processes with ps. Unlike /proc, ps can't report a
// process's working directory, environment, or log file, so a relaunch
// inherits this process's environment and discards output.
func (s realSystem) processes() ([]process, error) {
	out, err := s.run("ps", "-axww", "-o", "pid=,args=")
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
		procs = append(procs, process{PID: pid, Exe: fields[1], Args: fields[1:]})
	}
	return procs, nil
}
