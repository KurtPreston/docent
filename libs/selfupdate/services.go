package selfupdate

import (
	"fmt"
	"strings"
)

// serviceValue reads a scalar from launchctl's human-readable service record.
func serviceValue(output, key string) string {
	for _, line := range strings.Split(output, "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), key+" = "); ok {
			return value
		}
	}
	return ""
}

func checkSystemdBinary(binary string) error {
	out, err := sys.run("systemctl", "--user", "show", "--property=ExecStart", "--value", SystemdUnit)
	if err != nil {
		return fmt.Errorf("cannot inspect %s: %w", SystemdUnit, err)
	}
	_, rest, found := strings.Cut(out, "path=")
	program, _, _ := strings.Cut(rest, " ;")
	if !found || !sameFile(program, binary) {
		return fmt.Errorf("cannot start %s: its executable does not match %s", SystemdUnit, binary)
	}
	return nil
}
