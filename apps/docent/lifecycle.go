package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/KurtPreston/docent/libs/config/docentconfig"
	"github.com/KurtPreston/docent/libs/daemoncontrol"
)

var errStopped = errors.New("daemon is stopped")

func (a app) lifecycle(command string, args []string) error {
	fs := a.flags(command)
	dir := docentconfig.DefaultDir()
	if command == "start" || command == "restart" {
		fs.StringVar(&dir, "config-dir", dir, "configuration for a new unmanaged daemon only")
	}
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	binary, err = filepath.EvalSymlinks(binary)
	if err != nil {
		return err
	}
	binary = filepath.Join(filepath.Dir(binary), "docentd")
	opts := daemoncontrol.StartOptions{Binary: binary, ConfigDir: dir}
	fs.Visit(func(_ *flag.Flag) { opts.ConfigOverride = true })
	if command == "status" {
		d, err := daemoncontrol.Inspect(binary)
		if err != nil {
			return err
		}
		fmt.Fprintln(a.out, "docentd: "+d.Describe())
		if !d.Running {
			return errStopped
		}
		return nil
	}
	return daemoncontrol.WithLock(binary, func() error {
		switch command {
		case "start":
			return daemoncontrol.Start(a.out, opts)
		case "stop":
			return daemoncontrol.Stop(a.out, binary)
		default:
			return daemoncontrol.Restart(a.out, opts)
		}
	})
}
