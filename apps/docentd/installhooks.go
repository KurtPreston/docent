package main

import (
	"fmt"
	"os"

	"github.com/KurtPreston/docent/libs/cursorhooks"
)

// runInstallHooks installs the Cursor agent-activity hook on the host it runs
// on. It exists as a docentd subcommand rather than only as installer-script
// logic because the hook belongs on whichever machine executes the agent — the
// remote box for a Remote-SSH window — which is not always the machine the
// platform installer was run on. It also removes the jq dependency the shell
// installers carry for merging hooks.json.
func runInstallHooks(args []string) {
	for _, a := range args {
		switch a {
		case "-h", "--help":
			fmt.Println("usage: docentd install-hooks [--check]")
			fmt.Println()
			fmt.Println("Installs the Cursor hook that reports agent activity to docentd, into")
			fmt.Println(cursorhooks.Dir() + ". Run it on the machine where Cursor's agent executes:")
			fmt.Println("for a Remote-SSH window that is the remote host, not the one with the GUI.")
			fmt.Println()
			fmt.Println("  --check   report status without changing anything")
			return
		case "--check":
			st := cursorhooks.Check()
			if st.OK() {
				fmt.Println("cursor hooks: up to date")
				return
			}
			for _, line := range st.Summary() {
				fmt.Println("cursor hooks: " + line)
			}
			os.Exit(1)
		default:
			fmt.Fprintf(os.Stderr, "unknown argument %q (try --help)\n", a)
			os.Exit(2)
		}
	}

	st, err := cursorhooks.Install()
	if err != nil {
		fmt.Fprintf(os.Stderr, "install-hooks: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("installed %s\n", cursorhooks.ScriptPath())
	fmt.Printf("wired %v in %s\n", st.WiredEvents, cursorhooks.ConfigPath())
	if !st.OK() {
		for _, line := range st.Summary() {
			fmt.Fprintln(os.Stderr, "warning: "+line)
		}
		os.Exit(1)
	}
	if _, err := os.Stat(cursorhooks.TokenPath()); err != nil {
		fmt.Printf("note: %s is missing; the hook will post unauthenticated and docentd will reject it with 401 when a token is configured\n", cursorhooks.TokenPath())
	}
	fmt.Println("restart Cursor windows to pick up the new hook wiring")
}
