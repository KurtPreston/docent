package selfupdate

import "github.com/KurtPreston/docent/libs/daemoncontrol"

type Manager = daemoncontrol.Manager
type Daemon = daemoncontrol.Daemon
type Launch = daemoncontrol.Launch
type StartOptions = daemoncontrol.StartOptions

const (
	ManagerSystemd = daemoncontrol.ManagerSystemd
	ManagerLaunchd = daemoncontrol.ManagerLaunchd
	ManagerProcess = daemoncontrol.ManagerProcess
	ManagerNone    = daemoncontrol.ManagerNone
	SystemdUnit    = daemoncontrol.SystemdUnit
	LaunchdLabel   = daemoncontrol.LaunchdLabel
	TunnelLabel    = daemoncontrol.TunnelLabel
)

var (
	Detect = daemoncontrol.Detect
	Start  = daemoncontrol.Start
)
