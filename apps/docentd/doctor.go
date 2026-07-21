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
	cfg.Directives = engine.EnsureDirectives(cfg.Directives)
	reg := collectors.NewRegistry(nil)
	opts := &collectors.ValidateOpts{UserdataDir: cfg.ConfigDir}
	issues := reg.Validate(context.Background(), cfg.Directives, opts)
	if len(issues) == 0 {
		fmt.Println("doctor: all enabled directives PASS")
		return
	}
	failed := false
	for _, iss := range issues {
		label := "FAIL"
		if iss.Severity == "warning" {
			label = "WARN"
		} else {
			failed = true
		}
		fmt.Printf("%s %s (%s): %s\n", label, iss.DirectiveID, iss.Collector, iss.Message)
		if iss.Remediation != "" {
			fmt.Printf("  -> %s\n", iss.Remediation)
		}
	}
	if failed {
		os.Exit(1)
	}
	// Only non-fatal warnings remain; report success but leave them visible.
	fmt.Println("doctor: no blocking issues (warnings above are non-fatal)")
}
