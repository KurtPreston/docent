package main

import (
	"fmt"
	"os"

	"github.com/KurtPreston/docent/libs/cursorhooks"
)

func (a app) installHooks(args []string) error {
	fs := a.flags("install-hooks")
	check := fs.Bool("check", false, "report hook status without changing anything")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *check {
		st := cursorhooks.Check()
		if st.OK() {
			fmt.Fprintln(a.out, "cursor hooks: up to date")
			return nil
		}
		for _, line := range st.Summary() {
			fmt.Fprintln(a.out, "cursor hooks: "+line)
		}
		return fmt.Errorf("cursor hooks need installation; run docent install-hooks on the host executing the agent")
	}
	st, err := cursorhooks.Install()
	if err != nil {
		return fmt.Errorf("install-hooks: %w", err)
	}
	fmt.Fprintf(a.out, "installed %s\nwired %v in %s\n", cursorhooks.ScriptPath(), st.WiredEvents, cursorhooks.ConfigPath())
	if !st.OK() {
		for _, line := range st.Summary() {
			fmt.Fprintln(a.err, "warning: "+line)
		}
		return fmt.Errorf("hook installation incomplete")
	}
	if _, err := os.Stat(cursorhooks.TokenPath()); err != nil {
		fmt.Fprintf(a.out, "note: %s is missing; authenticated daemons will reject hook requests until it is configured\n", cursorhooks.TokenPath())
	}
	fmt.Fprintln(a.out, "restart Cursor windows to pick up the new hook wiring")
	return nil
}
