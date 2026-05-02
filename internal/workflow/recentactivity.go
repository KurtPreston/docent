package workflow

import (
	"context"
	"path/filepath"
	"time"

	"github.com/kurt/slakkr-ai/internal/ai"
	"github.com/kurt/slakkr-ai/internal/collectors"
	"github.com/kurt/slakkr-ai/internal/userdata"
)

// BuildRecentActivityInput loads userdata and runs collectors in activity (lookback) mode.
func BuildRecentActivityInput(ctx context.Context, d Deps, userdataDir string, now time.Time, lookback time.Duration) (ai.RecentActivityInput, error) {
	store := userdata.NewStore(userdataDir)
	projects, err := store.LoadProjects()
	if err != nil {
		return ai.RecentActivityInput{}, err
	}
	tasks, err := store.LoadTasks(projects)
	if err != nil {
		return ai.RecentActivityInput{}, err
	}
	directives, err := store.LoadDirectives(projects)
	if err != nil {
		return ai.RecentActivityInput{}, err
	}
	cfg, err := store.LoadConfig()
	if err != nil {
		return ai.RecentActivityInput{}, err
	}
	hostID, err := userdata.CurrentHostID()
	if err != nil {
		return ai.RecentActivityInput{}, err
	}
	expand := d.ExpandRepoPath
	if expand == nil {
		expand = func(s string) string { return s }
	}
	since := now.Add(-lookback)
	collectOpts := &collectors.CollectOpts{
		HostID:            hostID,
		UserdataDir:       userdataDir,
		Projects:          projects,
		Config:            cfg,
		ExpandRepoPath:    expand,
		OnDirectiveUpdate: d.OnDirectiveUpdate,
	}
	statuses, err := d.Registry.CollectActivity(ctx, directives.Directives, since, collectOpts)
	if err != nil {
		return ai.RecentActivityInput{}, err
	}
	days := int(lookback / (24 * time.Hour))
	if days < 1 {
		days = 1
	}
	return ai.RecentActivityInput{
		Now:          now.UTC(),
		Since:        since.UTC(),
		LookbackDays: days,
		HostID:       hostID,
		Projects:     projects.Projects,
		Tasks:        tasks.Tasks,
		Directives:   directives.Directives,
		Statuses:     statuses,
		DebugDir:     filepath.Join(userdataDir, "status-cache", "ai-debug"),
	}, nil
}
