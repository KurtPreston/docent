package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/KurtPreston/docent/libs/buildinfo"
	"golang.org/x/term"
)

type app struct {
	in       io.Reader
	out      io.Writer
	err      io.Writer
	terminal bool
}

func main() {
	a := app{in: os.Stdin, out: os.Stdout, err: os.Stderr, terminal: term.IsTerminal(int(os.Stdin.Fd()))}
	os.Exit(a.run(os.Args[1:]))
}

func (a app) run(args []string) int {
	if len(args) == 0 {
		a.help()
		return 0
	}
	var err error
	switch args[0] {
	case "help", "-h", "--help":
		a.help()
	case "version", "-version", "--version", "-v":
		if len(args) != 1 {
			err = usageError{fmt.Errorf("version takes no arguments")}
		} else {
			fmt.Fprintln(a.out, "docent "+buildinfo.String())
		}
	case "doctor":
		err = a.doctor(args[1:])
	case "install-hooks":
		err = a.installHooks(args[1:])
	case "update":
		err = a.runUpdate(args[1:])
	case "start", "stop", "restart", "status":
		err = a.lifecycle(args[0], args[1:])
	default:
		err = usageError{fmt.Errorf("unknown command %q; run docent --help", args[0])}
	}
	if err == nil || errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if errors.Is(err, errStopped) {
		return 3
	}
	fmt.Fprintln(a.err, "docent:", err)
	var usage usageError
	if errors.As(err, &usage) {
		return 2
	}
	return 1
}

func (a app) help() {
	fmt.Fprintln(a.out, "Usage: docent <command> [options]\n\nCommands:\n  version        Show the installed version\n  doctor         Validate local configuration and hooks\n  install-hooks  Install or check local agent hooks\n  update         Update installed release binaries\n  start          Start the local daemon if stopped\n  stop           Stop the local daemon\n  restart        Restart or start the local daemon\n  status         Inspect the local daemon (exit 3 if stopped)")
}

type usageError struct{ error }

func (e usageError) Unwrap() error { return e.error }

func (a app) flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet("docent "+name, flag.ContinueOnError)
	fs.SetOutput(a.err)
	return fs
}

func parseFlags(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return usageError{err}
	}
	if fs.NArg() != 0 {
		return usageError{fmt.Errorf("unexpected argument %q", fs.Arg(0))}
	}
	return nil
}
