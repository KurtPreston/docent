package main

import (
	"context"
	"fmt"
	"os"

	"github.com/KurtPreston/docent/apps/docentd/internal/config"
	"github.com/KurtPreston/docent/apps/docentd/internal/engine"
	"github.com/KurtPreston/docent/libs/collectors"
)

func runDoctor(args []string) {
	cfgPath := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "-config" && i+1 < len(args) {
			cfgPath = args[i+1]
		}
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}
	cfg.Directives = engine.EnsureDirectives(cfg.Directives, cfg.SessionManager)
	reg := collectors.NewRegistry(nil)
	opts := &collectors.ValidateOpts{UserdataDir: cfg.ConfigDir}
	issues := reg.Validate(context.Background(), cfg.Directives, opts)
	if len(issues) == 0 {
		fmt.Println("doctor: all enabled directives PASS")
		return
	}
	for _, iss := range issues {
		fmt.Printf("FAIL %s (%s): %s\n", iss.DirectiveID, iss.Collector, iss.Message)
		if iss.Remediation != "" {
			fmt.Printf("  -> %s\n", iss.Remediation)
		}
	}
	os.Exit(1)
}
