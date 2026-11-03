package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/KurtPreston/docent/libs/buildinfo"
	"github.com/KurtPreston/docent/libs/config/docentconfig"
	"github.com/KurtPreston/docent/libs/selfupdate"
	"golang.org/x/term"
)

// updateOptions are the `docentd update` flags.
type updateOptions struct {
	target    string // release tag to install; "" means the latest
	force     bool   // reinstall the same release, or replace a source build
	check     bool   // only report whether an update exists
	restart   string // "auto" or "never"
	configDir string // where a newly started docentd finds docentd.yaml
}

func runUpdate(args []string) {
	fs := flag.NewFlagSet("docentd update", flag.ExitOnError)
	var o updateOptions
	fs.StringVar(&o.target, "version", os.Getenv("DOCENT_VERSION"), "release tag to install, such as v0.2.0 (default: the latest release; DOCENT_VERSION also works)")
	fs.BoolVar(&o.force, "force", false, "reinstall even when already on that release, or replace a build from source")
	fs.BoolVar(&o.check, "check", false, "only report whether an update is available")
	fs.StringVar(&o.restart, "restart", "auto", "auto: restart docentd the way it's running, and offer to start it if it isn't; never: leave it alone")
	fs.StringVar(&o.configDir, "config-dir", docentconfig.DefaultDir(), "config directory used if docentd has to be started")
	_ = fs.Parse(args)

	if err := update(o); err != nil {
		fmt.Fprintf(os.Stderr, "docentd update: %v\n", err)
		os.Exit(1)
	}
}

func update(o updateOptions) error {
	if o.restart != "auto" && o.restart != "never" {
		return fmt.Errorf("--restart must be auto or never, not %q", o.restart)
	}
	u, err := selfupdate.New(selfupdate.Options{Log: os.Stdout})
	if err != nil {
		return err
	}

	// Resolve the release to install
	tag := o.target
	if tag == "" {
		if tag, err = u.LatestTag(); err != nil {
			return err
		}
	}
	current := buildinfo.CurrentVersion()
	upToDate := selfupdate.SameVersion(current, tag)
	if o.check {
		reportCheck(current, tag, upToDate)
		return nil
	}
	if upToDate && !o.force {
		fmt.Printf("docentd %s is already the latest release.\n", current)
		return nil
	}
	if !buildinfo.IsRelease() && !o.force {
		return fmt.Errorf("this docentd was built from source (%s), so there's no release to compare against; rerun with --force to replace it with %s", buildinfo.String(), tag)
	}

	// Note how docentd runs before replacing the binary it runs from
	daemon := selfupdate.Daemon{Manager: selfupdate.ManagerNone}
	if o.restart == "auto" {
		daemon, err = selfupdate.Detect(u.Binary())
		if err != nil {
			return err
		}
	}
	res, err := u.Install(tag)
	if err != nil {
		return err
	}
	fmt.Printf("Updated %s in %s from %s to %s.\n", strings.Join(res.Updated, ", "), res.BinDir, current, tag)

	return restartAfterUpdate(o, daemon, res)
}

// reportCheck prints the result of `docentd update --check`.
func reportCheck(current, tag string, upToDate bool) {
	switch {
	case upToDate:
		fmt.Printf("docentd %s is the latest release.\n", current)
	case !buildinfo.IsRelease():
		fmt.Printf("docentd was built from source (%s); the latest release is %s.\n", buildinfo.String(), tag)
	default:
		fmt.Printf("docentd %s can update to %s; run `docentd update`.\n", current, tag)
	}
}

// restartAfterUpdate restarts docentd the way it was running, or offers to
// start it when it wasn't.
func restartAfterUpdate(o updateOptions, daemon selfupdate.Daemon, res selfupdate.Result) error {
	if o.restart == "never" {
		fmt.Println("docentd wasn't restarted; the update takes effect the next time it starts.")
		return nil
	}
	if daemon.Manager != selfupdate.ManagerNone {
		fmt.Printf("Restarting docentd (%s)...\n", daemon.Describe())
		return daemon.Restart(os.Stdout, contains(res.Updated, "docent-tunnel"))
	}

	// It wasn't running: ask in a terminal, otherwise say how to start it
	start := selfupdate.StartOptions{Binary: res.Binary, ConfigDir: o.configDir}
	if !stdinIsTerminal() {
		fmt.Printf("docentd isn't running. Start it with: %s -config %s\n", start.Binary, filepath.Join(o.configDir, "docentd.yaml"))
		return nil
	}
	if !confirm("docentd isn't running. Start it now? [y/N] ") {
		return nil
	}
	return selfupdate.Start(os.Stdout, start)
}

// stdinIsTerminal reports whether someone can answer a prompt. /dev/null is
// a character device too, so a mode check isn't enough.
func stdinIsTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// confirm asks a yes/no question on stdin, defaulting to no.
func confirm(prompt string) bool {
	fmt.Print(prompt)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
