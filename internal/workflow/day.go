package workflow

import (
	"context"
	"time"

	"github.com/kurt/slakkr-ai/internal/ai"
	"github.com/kurt/slakkr-ai/internal/attention"
	"github.com/kurt/slakkr-ai/internal/collectors"
	"github.com/kurt/slakkr-ai/internal/daybook"
	"github.com/kurt/slakkr-ai/internal/statuscache"
	"github.com/kurt/slakkr-ai/internal/userdata"
)

// Deps bundles dependencies for gathering planning input (CLI and HTTP API).
type Deps struct {
	Registry       *collectors.Registry
	Now            func() time.Time
	ExpandRepoPath func(string) string
}

// BuildPlanningInput loads userdata, runs collectors, applies cache and attention, and loads the daybook entry.
func BuildPlanningInput(ctx context.Context, d Deps, userdataDir string, date time.Time, mode string) (ai.PlanningInput, daybook.Entry, error) {
	store := userdata.NewStore(userdataDir)
	projects, err := store.LoadProjects()
	if err != nil {
		return ai.PlanningInput{}, daybook.Entry{}, err
	}
	tasks, err := store.LoadTasks(projects)
	if err != nil {
		return ai.PlanningInput{}, daybook.Entry{}, err
	}
	directives, err := store.LoadDirectives(projects)
	if err != nil {
		return ai.PlanningInput{}, daybook.Entry{}, err
	}
	cfg, err := store.LoadConfig()
	if err != nil {
		return ai.PlanningInput{}, daybook.Entry{}, err
	}
	hostID, err := userdata.CurrentHostID()
	if err != nil {
		return ai.PlanningInput{}, daybook.Entry{}, err
	}
	now := time.Now
	if d.Now != nil {
		now = d.Now
	}
	expand := d.ExpandRepoPath
	if expand == nil {
		expand = func(s string) string { return s }
	}
	collectOpts := &collectors.CollectOpts{
		HostID:         hostID,
		UserdataDir:    userdataDir,
		Projects:       projects,
		Config:         cfg,
		ExpandRepoPath: expand,
	}
	statuses, err := d.Registry.Collect(ctx, directives.Directives, collectOpts)
	if err != nil {
		return ai.PlanningInput{}, daybook.Entry{}, err
	}
	statuses, err = statuscache.Annotate(userdataDir, statuses, now())
	if err != nil {
		return ai.PlanningInput{}, daybook.Entry{}, err
	}
	attention.Classify(statuses)
	entry, err := daybook.LoadOrCreate(userdataDir, date)
	if err != nil {
		return ai.PlanningInput{}, daybook.Entry{}, err
	}
	return ai.PlanningInput{
		Date:     date,
		Projects: projects.Projects,
		Tasks:    tasks.Tasks,
		Statuses: statuses,
		Daybook:  entry.Content,
		Mode:     mode,
	}, entry, nil
}
