package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/KurtPreston/docent/apps/docent-setup/internal/setup"
	"github.com/KurtPreston/docent/libs/config/docentconfig"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "check" {
		os.Exit(runCheck(os.Args[2:]))
	}
	os.Exit(runWizard(os.Args[1:]))
}

func runCheck(args []string) int {
	fs := flag.NewFlagSet("docent-setup check", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configDir := fs.String("config-dir", docentconfig.DefaultDir(), "docent config directory")
	configPath := fs.String("config", "", "config file (default <config-dir>/config.yaml)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	opts := setup.CheckOptions{
		ConfigDir:  *configDir,
		ConfigPath: *configPath,
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
	}
	if err := setup.RunCheck(context.Background(), opts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func runWizard(args []string) int {
	fs := flag.NewFlagSet("docent-setup", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configDir := fs.String("config-dir", docentconfig.DefaultDir(), "docent config directory")
	configPath := fs.String("config", "", "config file (default <config-dir>/config.yaml)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfgPath := *configPath
	if cfgPath == "" {
		cfgPath = filepath.Join(*configDir, "config.yaml")
	}
	opts := setup.Options{
		UserdataDir: *configDir,
		ConfigPath:  cfgPath,
		Stdin:       os.Stdin,
		Stdout:      os.Stdout,
		Stderr:      os.Stderr,
	}
	if err := setup.Run(opts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
