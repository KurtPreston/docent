package workflow

import (
	"context"
	"time"

	"github.com/kurt/slakkr-ai/libs/collectors"
	"github.com/kurt/slakkr-ai/libs/config/userdata"
)

// Deps bundles dependencies for running collectors (CLI and tests).
type Deps struct {
	Registry          *collectors.Registry
	Now               func() time.Time
	ExpandRepoPath    func(string) string
	OnDirectiveUpdate func(collectors.DirectiveProgress)
	// RunLog routes per-directive HTTP and subprocess activity into
	// the per-run log directory. Nil disables logging.
	RunLog collectors.RunLog
}

// RunOptions carries the per-run knobs that vary by resolved execution
// mode: the collection window, scope, an optional collector-type allow
// list, and whether GitHub should collect PR review-readiness instead of
// the usual activity timeline.
type RunOptions struct {
	Since              time.Time
	Until              time.Time
	Scope              collectors.Scope
	OnlyCollectorTypes []string
	PRReviewReadiness  bool
}

// CollectStatuses runs the enabled directives for one run. When
// run.OnlyCollectorTypes is set, only directives using those collector
// types participate (e.g. `prs` → GitHub only).
func CollectStatuses(ctx context.Context, d Deps, cfg userdata.ConfigFile, userdataDir string, run RunOptions) ([]collectors.StatusItem, error) {
	expand := d.ExpandRepoPath
	if expand == nil {
		expand = func(s string) string { return s }
	}
	opts := &collectors.CollectOpts{
		UserdataDir:        userdataDir,
		ExpandRepoPath:     expand,
		OnDirectiveUpdate:  d.OnDirectiveUpdate,
		Since:              run.Since,
		Until:              run.Until,
		Scope:              run.Scope,
		OnlyCollectorTypes: run.OnlyCollectorTypes,
		PRReviewReadiness:  run.PRReviewReadiness,
		RunLog:             d.RunLog,
	}
	return d.Registry.Collect(ctx, cfg.Directives, opts)
}
