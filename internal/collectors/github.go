package collectors

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/kurt/slakkr-ai/internal/userdata"
)

type GitHubCollector struct {
	Clock func() time.Time
}

type ghPR struct {
	Title          string `json:"title"`
	URL            string `json:"url"`
	State          string `json:"state"`
	IsDraft        bool   `json:"isDraft"`
	ReviewDecision string `json:"reviewDecision"`
}

func (c GitHubCollector) Collect(ctx context.Context, directive userdata.Directive, _ *CollectOpts) ([]StatusItem, error) {
	repo := directive.Target["repo"]
	if repo == "" {
		return nil, fmt.Errorf("target.repo is required")
	}
	args := []string{"pr", "list", "--repo", repo, "--json", "title,url,state,isDraft,reviewDecision", "--limit", "20"}
	if baseURL := directive.Config["base_url"]; baseURL != "" && !isGitHubDotCom(baseURL) {
		args = append([]string{"--hostname", hostname(baseURL)}, args...)
	}
	cmd := exec.CommandContext(ctx, "gh", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("gh %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	var prs []ghPR
	if err := json.Unmarshal(output, &prs); err != nil {
		return nil, err
	}
	items := make([]StatusItem, 0, len(prs))
	for _, pr := range prs {
		severity := "info"
		if pr.IsDraft {
			severity = "info"
		} else if pr.ReviewDecision == "CHANGES_REQUESTED" {
			severity = "warning"
		}
		items = append(items, StatusItem{
			DirectiveID: directive.ID,
			ProjectID:   directive.ProjectID,
			Source:      directive.Collector,
			Kind:        "pull_request",
			Title:       pr.Title,
			Summary:     fmt.Sprintf("state=%s draft=%t review=%s", pr.State, pr.IsDraft, pr.ReviewDecision),
			URL:         pr.URL,
			Severity:    severity,
			ObservedAt:  c.Clock(),
			Fields: map[string]string{
				"repo":            repo,
				"state":           pr.State,
				"review_decision": pr.ReviewDecision,
			},
		})
	}
	return items, nil
}

func isGitHubDotCom(raw string) bool {
	return hostname(raw) == "github.com"
}

func hostname(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return raw
	}
	return parsed.Hostname()
}
