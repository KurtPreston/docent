package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/kurt/slakkr-ai/internal/setup"
	"github.com/kurt/slakkr-ai/internal/userdata"
)

func main() {
	userdataDir := flag.String("userdata", userdata.DefaultDir, "userdata directory")
	configPath := flag.String("config", "", "config file (default <userdata>/config.yaml)")
	flag.Parse()

	opts := setup.Options{
		UserdataDir: *userdataDir,
		ConfigPath:  *configPath,
		Stdin:       os.Stdin,
		Stdout:      os.Stdout,
		Stderr:      os.Stderr,
	}
	if err := setup.Run(opts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
