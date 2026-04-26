package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/AlecAivazis/survey/v2"
	"github.com/kurt/slakkr-ai/internal/userdata"
	"golang.org/x/term"
)

// resolveProposedTasksInteractive walks each entry in proposed-tasks.yaml and asks
// the user to create a task, link to an existing task, dismiss, or skip.
func (a *App) resolveProposedTasksInteractive(ctx context.Context, userdataDir string) (int, error) {
	store := userdata.NewStore(userdataDir)
	store.GitClient = a.Git
	projects, err := store.LoadProjects()
	if err != nil {
		return 0, err
	}
	tasks, err := store.LoadTasks(projects)
	if err != nil {
		return 0, err
	}
	pfile, err := store.LoadProposedTasks(projects, tasks)
	if err != nil {
		return 0, err
	}
	if len(pfile.Proposed) == 0 {
		return 0, nil
	}
	sfile, err := store.LoadSignals(projects, tasks)
	if err != nil {
		return 0, err
	}

	fmt.Fprintln(a.Out, "")
	fmt.Fprintln(a.Out, "Proposed tasks (review)")
	fmt.Fprintln(a.Out, strings.Repeat("=", 50))

	prompter := StdioPrompter{In: a.In, Out: a.Out}
	useSurvey := a.canUseAdvancedPrompt()
	did := 0
	// Snapshot order; skip rows removed by earlier iterations in this run
	initial := append([]userdata.ProposedTask(nil), pfile.Proposed...)
	for _, p := range initial {
		if ctx.Err() != nil {
			return did, ctx.Err()
		}
		if !proposedContainsID(pfile, p.ID) {
			continue
		}
		fmt.Fprintf(a.Out, "\nId:   %s\n", p.ID)
		fmt.Fprintf(a.Out, "Name: %s\n", p.Name)
		if p.ProjectID != "" {
			fmt.Fprintf(a.Out, "Project: %s\n", p.ProjectID)
		}
		if p.Description != "" {
			fmt.Fprintf(a.Out, "Description: %s\n", p.Description)
		}
		if p.Reason != "" {
			fmt.Fprintf(a.Out, "Why:      %s\n", p.Reason)
		}
		if p.Confidence != nil {
			fmt.Fprintf(a.Out, "Confidence: %g\n", *p.Confidence)
		}
		choices := []string{
			"Create a new task from this proposal",
			"Link to an existing task (update signals, remove proposal)",
			"Dismiss (mark signals as ignored, remove proposal)",
			"Skip (leave in proposed-tasks for next time)",
		}
		var action string
		if useSurvey {
			prompt := &survey.Select{
				Message: "Choose a resolution",
				Options: choices,
			}
			if err := survey.AskOne(prompt, &action, survey.WithStdio(os.Stdin, os.Stdout, os.Stderr)); err != nil {
				return did, err
			}
		} else {
			_, _ = fmt.Fprintln(a.Out, "1) Create task  2) Link  3) Dismiss  4) Skip")
			line, err := prompter.Ask("Choice (1-4, default 4)", "4")
			if err != nil {
				return did, err
			}
			switch strings.TrimSpace(line) {
			case "1":
				action = choices[0]
			case "2":
				action = choices[1]
			case "3":
				action = choices[2]
			default:
				action = choices[3]
			}
		}
		now := a.Now()
		switch action {
		case choices[0]:
			taskID := nextAvailableTaskID(tasks, slugID(p.Name))
			confirm, err := prompter.Confirm("Create task "+taskID+" with the proposed name and fields?", true)
			if err != nil {
				return did, err
			}
			if !confirm {
				fmt.Fprintln(a.Out, "Skipped (no task created).")
				continue
			}
			t := userdata.Task{
				ID:          taskID,
				ProjectID:   p.ProjectID,
				Name:        p.Name,
				Description: p.Description,
				Status:      p.Status,
				Priority:    p.Priority,
				NextAction:  p.NextAction,
				UpdatedAt:   userdata.YAMLDateTime{Time: now},
			}
			if t.Status == "" {
				t.Status = userdata.TaskStatusReady
			}
			if t.Priority == "" {
				t.Priority = userdata.PriorityMedium
			}
			if t.NextAction == "" {
				t.NextAction = "Triage: confirm scope and next step"
			}
			upsertTask(&tasks, t)
			if err := store.SaveTasks(projects, tasks); err != nil {
				return did, err
			}
			sfile, err = store.LoadSignals(projects, tasks)
			if err != nil {
				return did, err
			}
			applyProposedToSignals(&sfile, p.SourceSignalIDs, userdata.SignalResolutionTask, t.ID, "created from proposed task", now)
			if err := store.SaveSignals(projects, tasks, sfile); err != nil {
				return did, err
			}
			removeProposedByID(&pfile, p.ID)
			did++
		case choices[1]:
			taskID, err := prompter.Ask("Existing task id", "")
			if err != nil {
				return did, err
			}
			taskID = strings.TrimSpace(taskID)
			if taskID == "" || !taskIDUsed(tasks, taskID) {
				fmt.Fprintln(a.Err, "Unknown task id; left proposal in place.")
				continue
			}
			applyProposedToSignals(&sfile, p.SourceSignalIDs, userdata.SignalResolutionTask, taskID, "linked from proposed task review", now)
			if err := store.SaveSignals(projects, tasks, sfile); err != nil {
				return did, err
			}
			removeProposedByID(&pfile, p.ID)
			did++
		case choices[2]:
			applyProposedToSignals(&sfile, p.SourceSignalIDs, userdata.SignalResolutionIgnored, "", "dismissed in proposed-task review", now)
			if err := store.SaveSignals(projects, tasks, sfile); err != nil {
				return did, err
			}
			removeProposedByID(&pfile, p.ID)
			did++
		default: // skip
			continue
		}
		// Re-save reduced proposed list after each decision so a crash does not re-prompt for resolved rows
		if err := store.SaveProposedTasks(projects, tasks, pfile); err != nil {
			return did, err
		}
		// next iteration needs fresh pfile for multi-item - pfile in memory is already updated
	}
	if did == 0 {
		return 0, nil
	}
	if err := store.CommitAll(ctx, "Resolve proposed slakkr tasks after update_tasks"); err != nil {
		fmt.Fprintf(a.Err, "warning: could not commit userdata: %v\n", err)
	}
	return did, nil
}

func (a *App) canUseAdvancedPrompt() bool {
	if a.In != os.Stdin {
		return false
	}
	if a.Out != os.Stdout {
		return false
	}
	inf, ok := a.In.(*os.File)
	if !ok || inf == nil {
		return false
	}
	outf, ok := a.Out.(*os.File)
	if !ok || outf == nil {
		return false
	}
	return term.IsTerminal(int(inf.Fd())) && term.IsTerminal(int(outf.Fd()))
}

func nextAvailableTaskID(tasks userdata.TasksFile, base string) string {
	if !taskIDUsed(tasks, base) {
		return base
	}
	for n := 2; ; n++ {
		id := fmt.Sprintf("%s-%d", base, n)
		if !taskIDUsed(tasks, id) {
			return id
		}
	}
}

func taskIDUsed(tasks userdata.TasksFile, id string) bool {
	for _, t := range tasks.Tasks {
		if t.ID == id {
			return true
		}
	}
	return false
}

func applyProposedToSignals(s *userdata.SignalsFile, sourceIDs []string, res userdata.SignalResolution, taskID, reason string, t time.Time) {
	want := make(map[string]bool, len(sourceIDs))
	for _, id := range sourceIDs {
		want[id] = true
	}
	for i := range s.Signals {
		if !want[s.Signals[i].ID] {
			continue
		}
		s.Signals[i].Resolution = res
		s.Signals[i].TaskID = ""
		if res == userdata.SignalResolutionTask {
			s.Signals[i].TaskID = taskID
		}
		s.Signals[i].Reason = reason
		s.Signals[i].ClassifiedAt = userdata.YAMLDateTime{Time: t}
	}
}

func proposedContainsID(f userdata.ProposedTasksFile, id string) bool {
	for _, p := range f.Proposed {
		if p.ID == id {
			return true
		}
	}
	return false
}

func removeProposedByID(f *userdata.ProposedTasksFile, id string) {
	keep := f.Proposed[:0]
	for _, p := range f.Proposed {
		if p.ID != id {
			keep = append(keep, p)
		}
	}
	f.Proposed = keep
}
