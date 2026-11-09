package collectors

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

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
