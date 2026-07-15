//go:build ignore

// One-off: reconstruct the Ollama prompt for recent-activity (same path as UI).
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/KurtPreston/docent/libs/ai"
	"github.com/KurtPreston/docent/libs/collectors"
	"github.com/KurtPreston/docent/libs/config/executionmode"
	"github.com/KurtPreston/docent/libs/config/userdata"
	"github.com/KurtPreston/docent/libs/report"
	"github.com/joho/godotenv"
)

func main() {
	configDir := filepath.Join(os.Getenv("HOME"), ".config", "docent")
	_ = godotenv.Load(filepath.Join(configDir, ".env"))

	store := userdata.Store{Dir: configDir}
	cfg, err := store.LoadConfig()
	if err != nil {
		fatal(err)
	}

	days := 1
	if len(os.Args) > 1 {
		fmt.Sscanf(os.Args[1], "%d", &days)
	}

	modes, err := executionmode.Load(executionmode.BuiltinModes(), cfg.ExecutionModes)
	if err != nil {
		fatal(err)
	}
	mode, ok := executionmode.Find(modes, "recent-activity")
	if !ok {
		fatal(fmt.Errorf("recent-activity mode not found"))
	}

	resolved, err := executionmode.Resolve(mode, executionmode.ResolveOpts{
		Now:          time.Now(),
		DaysOverride: days,
		ScopeOverride: executionmode.ScopeInvolved,
		CollectOverride: executionmode.CollectEvents,
		NonInteractive: true,
	})
	if err != nil {
		fatal(err)
	}

	fmt.Fprintf(os.Stderr, "Collecting: since=%s until=%s scope=%s days=%d\n",
		resolved.Since.Format(time.RFC3339), resolved.Until.Format(time.RFC3339),
		resolved.Scope, resolved.LookbackDays)

	reg := collectors.NewRegistry(time.Now)
	ctx := context.Background()
	statuses, err := report.Collect(ctx, reg, cfg, resolved, report.CollectOptions{
		ConfigDir: configDir,
	})
	if err != nil {
		fatal(err)
	}
	workItems, statuses, err := report.Correlate(ctx, reg, cfg, statuses, report.CorrelateOptions{
		ConfigDir: configDir,
	})
	if err != nil {
		fatal(err)
	}

	formatter := ai.SelectActivityFormatter(cfg.AI.ActivityFormatter)
	prompt, err := ai.BuildPrompt(resolved.Instruction, ai.RunInput{
		ModeID:       resolved.ModeID,
		ModeName:     resolved.ModeName,
		Now:          resolved.Until,
		Since:        resolved.Since,
		LookbackDays: resolved.LookbackDays,
		Instruction:  resolved.Instruction,
		Statuses:     statuses,
		WorkItems:    workItems,
	}, formatter)
	if err != nil {
		fatal(err)
	}

	outDir := filepath.Join("tmp", "prompt-dump")
	_ = os.MkdirAll(outDir, 0o755)
	outPath := filepath.Join(outDir, "ollama-prompt.txt")
	if err := os.WriteFile(outPath, []byte(prompt), 0o644); err != nil {
		fatal(err)
	}

	// Kind tallies across signals
	kindCounts := map[string]int{}
	for _, s := range statuses {
		k := s.Kind
		if k == "" {
			k = "(empty)"
		}
		kindCounts[k]++
	}

	branchOnly := 0
	commitWIs := 0
	for _, wi := range workItems {
		hasCommit := false
		hasBranch := false
		other := false
		for _, e := range wi.Entities {
			switch e.Kind {
			case "branch":
				hasBranch = true
			case "commit":
				hasCommit = true
			case "reflog":
				// ignore for "evidence"
			default:
				if e.Kind != "" {
					other = true
				}
			}
		}
		if hasBranch && !hasCommit && !other {
			branchOnly++
		}
		if hasCommit {
			commitWIs++
		}
	}

	fmt.Printf("signals=%d work_items=%d\n", len(statuses), len(workItems))
	fmt.Printf("signal kinds: %v\n", kindCounts)
	fmt.Printf("work items with commits: %d\n", commitWIs)
	fmt.Printf("work items that are branch-only (no commit/pr/ticket evidence): %d\n", branchOnly)
	fmt.Printf("prompt bytes: %d → %s\n", len(prompt), outPath)

	// Show a sample of branch-only headings from the prompt
	fmt.Println("\n--- first 80 lines of prompt ---")
	lines := strings.Split(prompt, "\n")
	for i, line := range lines {
		if i >= 80 {
			fmt.Printf("... (%d more lines)\n", len(lines)-80)
			break
		}
		fmt.Println(line)
	}

	fmt.Println("\n--- work items with only activity: branch×1 (sample) ---")
	n := 0
	for _, wi := range workItems {
		onlyBranch := len(wi.Entities) == 1 && wi.Entities[0].Kind == "branch"
		if !onlyBranch {
			continue
		}
		fmt.Printf("  %s @ %s  last=%s  title=%q\n", wi.Repo, wi.Branch, wi.LastActivity, wi.Title)
		n++
		if n >= 15 {
			fmt.Printf("  ... and more\n")
			break
		}
	}
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}
