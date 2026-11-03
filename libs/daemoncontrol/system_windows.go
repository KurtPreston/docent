//go:build windows

package daemoncontrol

import "errors"

// Releases and `docentd update` don't support Windows yet.
var errWindows = errors.New("restarting docentd isn't supported on Windows")

func (realSystem) stop(int) error                { return errWindows }
func (realSystem) alive(int) bool                { return false }
func (realSystem) start(Launch) (int, error)     { return 0, errWindows }
func (realSystem) processes() ([]process, error) { return nil, errWindows }
