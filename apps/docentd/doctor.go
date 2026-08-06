package main

import (
	"context"
	"fmt"
	"os"

	"github.com/KurtPreston/docent/apps/docentd/internal/config"
	"github.com/KurtPreston/docent/apps/docentd/internal/engine"
	"github.com/KurtPreston/docent/libs/collectors"
	"github.com/KurtPreston/docent/libs/cursorhooks"
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
	hooksOK := reportHooks()
	if len(issues) == 0 {
		if hooksOK {
			fmt.Println("doctor: all enabled directives PASS")
			return
		}
		fmt.Println("doctor: all enabled directives PASS (see hook warnings above)")
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

// reportHooks checks the Cursor agent-activity hook and prints its state.
//
// The hook must live on the machine where the agent runs, which for a
// Remote-SSH window is the remote box — not the machine running the IDE GUI. So
// this only reports on the host doctor itself runs on, and names that host to
// make the distinction obvious. A drifted hook costs all agent status while
// window lifecycle keeps working, so it needs to be visible even when every
// collector passes.
func reportHooks() (ok bool) {
	if _, err := os.Stat(cursorhooks.Dir()); err != nil {
		fmt.Printf("hooks: skipped, no %s on this host (Cursor's agent does not run here)\n", cursorhooks.Dir())
		return true
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "this host"
	}
	st := cursorhooks.Check()
	if st.OK() {
		fmt.Printf("PASS cursor hooks on %s (agent status reporting wired)\n", host)
		return true
	}
	for _, line := range st.Summary() {
		fmt.Printf("WARN cursor hooks on %s: %s\n", host, line)
	}
	fmt.Println("  -> agent status (working / needs-followup) will not be reported; run: docentd install-hooks")
	return false
}
