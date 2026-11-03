package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/KurtPreston/docent/libs/buildinfo"
	"github.com/KurtPreston/docent/libs/config/docentconfig"
	"github.com/KurtPreston/docent/libs/daemoncontrol"
	"github.com/KurtPreston/docent/libs/selfupdate"
)

type updateOptions struct {
	target         string
	force          bool
	check          bool
	restart        string
	configDir      string
	configOverride bool
}

func (a app) runUpdate(args []string) error {
	fs := a.flags("update")
	var o updateOptions
	fs.StringVar(&o.target, "version", os.Getenv("DOCENT_VERSION"), "release tag (default: latest)")
	fs.BoolVar(&o.force, "force", false, "reinstall or replace a source build")
	fs.BoolVar(&o.check, "check", false, "only report the available release")
	fs.StringVar(&o.restart, "restart", "auto", "auto or never")
	fs.StringVar(&o.configDir, "config-dir", docentconfig.DefaultDir(), "config directory for a new unmanaged daemon")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "config-dir" {
			o.configOverride = true
		}
	})
	if o.restart != "auto" && o.restart != "never" {
		return usageError{fmt.Errorf("--restart must be auto or never")}
	}
	u, err := selfupdate.New(selfupdate.Options{Log: a.out})
	if err != nil {
		return err
	}
	if o.check {
		return a.update(u, o)
	}
	return daemoncontrol.WithLock(u.Binary(), func() error { return a.update(u, o) })
}

func (a app) update(u *selfupdate.Updater, o updateOptions) error {
	tag := o.target
	if tag == "" {
		var err error
		tag, err = u.LatestTag()
		if err != nil {
			return err
		}
	}
	current := buildinfo.CurrentVersion()
	upToDate := selfupdate.SameVersion(current, tag)
	if o.check {
		fmt.Fprintf(a.out, "Installed: %s\nAvailable: %s\n", buildinfo.String(), tag)
		return nil
	}
	if upToDate && !o.force {
		fmt.Fprintf(a.out, "docent %s is already installed.\n", current)
		return nil
	}
	if !buildinfo.IsRelease() && !o.force {
		return fmt.Errorf("this docent was built from source (%s); use --force to replace it with %s", buildinfo.String(), tag)
	}

	// Detect the sibling daemon, never this CLI process.
	daemon := daemoncontrol.Daemon{Manager: daemoncontrol.ManagerNone}
	if _, err := os.Stat(u.DaemonBinary()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("inspect installed daemon: %w", err)
	} else if err == nil && o.restart == "auto" {
		var err error
		daemon, err = daemoncontrol.Detect(u.DaemonBinary())
		if err != nil {
			return err
		}
		if o.configOverride {
			inspected, err := daemoncontrol.Inspect(u.DaemonBinary())
			if err != nil {
				return err
			}
			if inspected.Running || inspected.Manager != daemoncontrol.ManagerNone {
				return usageError{fmt.Errorf("--config-dir cannot override an existing daemon or service")}
			}
		}
	}
	res, err := u.Install(tag)
	if err != nil {
		return err
	}
	fmt.Fprintf(a.out, "Updated %s in %s from %s to %s.\n", strings.Join(res.Updated, ", "), res.BinDir, current, tag)
	return a.restartAfterUpdate(o, daemon, res)
}

func (a app) restartAfterUpdate(o updateOptions, daemon daemoncontrol.Daemon, res selfupdate.Result) error {
	if !contains(res.Updated, "docentd") {
		return nil
	}
	if o.restart == "never" {
		fmt.Fprintln(a.out, "docentd wasn't restarted; the update takes effect when it next starts.")
		return nil
	}
	if daemon.Manager != daemoncontrol.ManagerNone {
		fmt.Fprintf(a.out, "Restarting docentd (%s)...\n", daemon.Describe())
		return daemon.Restart(a.out, contains(res.Updated, "docent-tunnel"))
	}
	if !a.terminal {
		fmt.Fprintln(a.out, "docentd isn't running. Start it with: docent start")
		return nil
	}
	fmt.Fprint(a.out, "docentd isn't running. Start it now? [y/N] ")
	answer, err := bufio.NewReader(a.in).ReadString('\n')
	if err != nil {
		return fmt.Errorf("read startup confirmation: %w", err)
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	if answer != "y" && answer != "yes" {
		return nil
	}
	return daemoncontrol.Start(a.out, daemoncontrol.StartOptions{Binary: res.DaemonBinary, ConfigDir: o.configDir, ConfigOverride: o.configOverride})
}

func contains(list []string, name string) bool {
	for _, s := range list {
		if s == name {
			return true
		}
	}
	return false
}
