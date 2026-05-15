package workflow

import (
	"context"
	"time"

	"github.com/kurt/slakkr-ai/internal/collectors"
	"github.com/kurt/slakkr-ai/internal/userdata"
)

// Deps bundles dependencies for running collectors (CLI and tests).
type Deps struct {
	Registry          *collectors.Registry
	Now               func() time.Time
	ExpandRepoPath    func(string) string
	OnDirectiveUpdate func(collectors.DirectiveProgress)
}

// CollectStatuses runs all enabled directives in date-based mode since `until` is d.Now().
// scope is forwarded into CollectOpts as a placeholder for future
// scope-aware collection (collectors ignore it today).
func CollectStatuses(ctx context.Context, d Deps, cfg userdata.ConfigFile, userdataDir string, since, until time.Time, scope collectors.Scope) ([]collectors.StatusItem, error) {
	expand := d.ExpandRepoPath
	if expand == nil {
		expand = func(s string) string { return s }
	}
	opts := &collectors.CollectOpts{
		UserdataDir:       userdataDir,
		ExpandRepoPath:    expand,
		OnDirectiveUpdate: d.OnDirectiveUpdate,
		Since:             since,
		Until:             until,
		Scope:             scope,
	}
	return d.Registry.Collect(ctx, cfg.Directives, opts)
}
