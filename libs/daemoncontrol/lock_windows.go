package daemoncontrol

import "fmt"

func WithLock(_ string, _ func() error) error {
	return fmt.Errorf("release updates and local daemon lifecycle are unsupported on Windows")
}
