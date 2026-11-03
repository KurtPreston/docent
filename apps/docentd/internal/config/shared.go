package config

import "github.com/KurtPreston/docent/libs/config/daemonconfig"

type DaemonConfig = daemonconfig.DaemonConfig

var (
	Load            = daemonconfig.Load
	ResolveBindHost = daemonconfig.ResolveBindHost
	IsLoopbackHost  = daemonconfig.IsLoopbackHost
)
