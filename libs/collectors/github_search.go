package collectors

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"

	"github.com/KurtPreston/docent/libs/config/userdata"
)

// listOpenPRs runs `gh search prs <relationArgs> --state open` and returns
// the parsed rows. relationArgs is a flag/value pair such as
// {"--author", user} or {"--review-requested", user}.
func (c GitHubCollector) listOpenPRs(ctx context.Context, env []string, directive userdata.Directive, opts *CollectOpts, relationArgs ...string) ([]ghSearchActivityRow, error) {
	args := append([]string{"search", "prs"}, relationArgs...)
	args = append(args, "--state", "open", "--limit", "100", "--json", "title,url,isDraft,createdAt,repository,updatedAt,author,assignees")
	return c.runPRSearch(ctx, env, directive, opts, args)
}

// runPRSearch executes a prepared `gh search prs` invocation and parses its rows.
func (c GitHubCollector) runPRSearch(ctx context.Context, env []string, directive userdata.Directive, opts *CollectOpts, args []string) ([]ghSearchActivityRow, error) {
	release := acquireSearchGate(ctx)
	defer release()
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Env = env
	out, err := runAndLogExec(cmd, opts, directive.ID)
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("gh %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(exit.Stderr)))
		}
		return nil, fmt.Errorf("gh %s: %w", strings.Join(args, " "), err)
	}
	var rows []ghSearchActivityRow
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// searchGateKey marks the context value holding a collect's search gate.
type searchGateKey struct{}

// withSearchGate returns a context whose plain `gh search prs` calls run one
// at a time. GitHub's secondary rate limit trips on bursts of concurrent
// searches, so a collect sends them in turn; GraphQL watch scans don't go
// through runPRSearch and keep running alongside.
func withSearchGate(ctx context.Context) context.Context {
	return context.WithValue(ctx, searchGateKey{}, &sync.Mutex{})
}

// acquireSearchGate waits for the context's search gate, if it has one, and
// returns the function that releases it.
func acquireSearchGate(ctx context.Context) func() {
	gate, ok := ctx.Value(searchGateKey{}).(*sync.Mutex)
	if !ok {
		return func() {}
	}
	gate.Lock()
	return gate.Unlock
}
