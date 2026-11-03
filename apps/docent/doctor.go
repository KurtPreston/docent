package main

import (
	"context"
	"fmt"
	"os"

	"github.com/KurtPreston/docent/libs/automation"
	"github.com/KurtPreston/docent/libs/buildinfo"
	"github.com/KurtPreston/docent/libs/collectors"
	"github.com/KurtPreston/docent/libs/config/daemonconfig"
	"github.com/KurtPreston/docent/libs/cursorhooks"
)

func (a app) doctor(args []string) error {
	fs := a.flags("doctor")
	cfgPath := fs.String("config", "", "daemon configuration path")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "docent %s\n", buildinfo.String())
	cfg, err := daemonconfig.Load(*cfgPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	cfg.Directives = daemonconfig.EnsureDirectives(cfg.Directives)
	reg := collectors.NewRegistry(nil)
	opts := &collectors.ValidateOpts{UserdataDir: cfg.ConfigDir}
	issues := reg.Validate(context.Background(), cfg.Directives, opts)
	a.reportRuleWarnings(cfg.Automations)
	hooksOK := a.reportHooks()
	if len(issues) == 0 {
		if hooksOK {
			fmt.Fprintln(a.out, "doctor: all enabled directives PASS")
			return nil
		}
		fmt.Fprintln(a.out, "doctor: all enabled directives PASS (see hook warnings above)")
		return nil
	}
	failed := false
	for _, iss := range issues {
		label := "FAIL"
		if iss.Severity == "warning" {
			label = "WARN"
		} else {
			failed = true
		}
		fmt.Fprintf(a.out, "%s %s (%s): %s\n", label, iss.DirectiveID, iss.Collector, iss.Message)
		if iss.Remediation != "" {
			fmt.Fprintf(a.out, "  -> %s\n", iss.Remediation)
		}
	}
	if failed {
		return fmt.Errorf("doctor: blocking issues found")
	}
	// Only non-fatal warnings remain; report success but leave them visible.
	fmt.Fprintln(a.out, "doctor: no blocking issues (warnings above are non-fatal)")
	return nil
}

// reportRuleWarnings prints automation settings that load but do nothing.
// They are non-fatal, so doctor still passes.
func (a app) reportRuleWarnings(rules []automation.Rule) {
	for _, w := range automation.RuleWarnings(rules) {
		fmt.Fprintf(a.out, "WARN %s\n", w)
	}
}

// reportHooks checks the Cursor agent-activity hook and prints its state.
//
// The hook must live on the machine where the agent runs, which for a
// Remote-SSH window is the remote box — not the machine running the IDE GUI. So
// this only reports on the host doctor itself runs on, and names that host to
// make the distinction obvious. A drifted hook costs all agent status while
// window lifecycle keeps working, so it needs to be visible even when every
// collector passes.
func (a app) reportHooks() (ok bool) {
	if _, err := os.Stat(cursorhooks.Dir()); err != nil {
		fmt.Fprintf(a.out, "hooks: skipped, no %s on this host (Cursor's agent does not run here)\n", cursorhooks.Dir())
		return true
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "this host"
	}
	st := cursorhooks.Check()
	if st.OK() {
		fmt.Fprintf(a.out, "PASS cursor hooks on %s (agent status reporting wired)\n", host)
		return true
	}
	for _, line := range st.Summary() {
		fmt.Fprintf(a.out, "WARN cursor hooks on %s: %s\n", host, line)
	}
	fmt.Fprintln(a.out, "  -> agent status (working / needs-followup) will not be reported; run: docent install-hooks")
	return false
}
