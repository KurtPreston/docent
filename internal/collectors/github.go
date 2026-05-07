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

type ghPRActivity struct {
	Number         int    `json:"number"`
	Title          string `json:"title"`
	URL            string `json:"url"`
	State          string `json:"state"`
	IsDraft        bool   `json:"isDraft"`
	ReviewDecision string `json:"reviewDecision"`
	UpdatedAt      string `json:"updatedAt"`
	MergedAt       string `json:"mergedAt"`
}

type ghIssueActivity struct {
	Number    int    `json:"number"`
	Title     string `json:"title"`
	URL       string `json:"url"`
	State     string `json:"state"`
	UpdatedAt string `json:"updatedAt"`
}

// Collect lists PRs and issues updated since opts.Since for the repo (read-only gh invocations).
func (c GitHubCollector) Collect(ctx context.Context, directive userdata.Directive, opts *CollectOpts) ([]StatusItem, error) {
	repo := directive.Target["repo"]
	if repo == "" {
		return nil, fmt.Errorf("target.repo is required")
	}
	since := time.Time{}
	if opts != nil {
		since = opts.Since
	}
	now := c.Clock()
	if opts != nil {
		now = opts.windowEnd(c.Clock)
	}
	dateStr := since.Format("2006-01-02")
	search := "updated:>=" + dateStr
	hostArgs := []string(nil)
	if baseURL := directive.Config["base_url"]; baseURL != "" && !isGitHubDotCom(baseURL) {
		hostArgs = []string{"--hostname", hostname(baseURL)}
	}
	var items []StatusItem
	runGh := func(args []string) ([]byte, error) {
		full := append(append([]string(nil), hostArgs...), args...)
		cmd := exec.CommandContext(ctx, "gh", full...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("gh %s: %w\n%s", strings.Join(full, " "), err, strings.TrimSpace(string(out)))
		}
		return out, nil
	}
	prArgs := []string{"pr", "list", "--repo", repo, "--state", "all", "--search", search, "--json", "number,title,url,state,isDraft,reviewDecision,updatedAt,mergedAt", "--limit", "50"}
	prOut, err := runGh(prArgs)
	if err != nil {
		return nil, err
	}
	var prs []ghPRActivity
	if err := json.Unmarshal(prOut, &prs); err != nil {
		return nil, err
	}
	for _, pr := range prs {
		obs, err := time.Parse(time.RFC3339, strings.TrimSpace(pr.UpdatedAt))
		if err != nil || obs.Before(since) || obs.After(now) {
			continue
		}
		severity := "info"
		if !pr.IsDraft && pr.ReviewDecision == "CHANGES_REQUESTED" && strings.EqualFold(pr.State, "open") {
			severity = "warning"
		}
		items = append(items, StatusItem{
			DirectiveID: directive.ID,
			ProjectID:   directive.ProjectID,
			Source:      directive.Collector,
			Kind:        "pull_request_activity",
			Title:       pr.Title,
			Summary:     fmt.Sprintf("state=%s draft=%t review=%s updated=%s", pr.State, pr.IsDraft, pr.ReviewDecision, pr.UpdatedAt),
			URL:         pr.URL,
			Severity:    severity,
			ObservedAt:  obs.UTC(),
			Fields: map[string]string{
				"repo":             repo,
				"state":            pr.State,
				"review_decision":  pr.ReviewDecision,
				"updated_at":       pr.UpdatedAt,
				"merged_at":        pr.MergedAt,
				"number":           fmt.Sprintf("%d", pr.Number),
			},
		})
	}
	issueArgs := []string{"issue", "list", "--repo", repo, "--state", "all", "--search", search, "--json", "number,title,url,state,updatedAt", "--limit", "50"}
	issueOut, err := runGh(issueArgs)
	if err != nil {
		return nil, err
	}
	var issues []ghIssueActivity
	if err := json.Unmarshal(issueOut, &issues); err != nil {
		return nil, err
	}
	for _, iss := range issues {
		obs, err := time.Parse(time.RFC3339, strings.TrimSpace(iss.UpdatedAt))
		if err != nil || obs.Before(since) || obs.After(now) {
			continue
		}
		items = append(items, StatusItem{
			DirectiveID: directive.ID,
			ProjectID:   directive.ProjectID,
			Source:      directive.Collector,
			Kind:        "issue_activity",
			Title:       iss.Title,
			Summary:     fmt.Sprintf("state=%s updated=%s", iss.State, iss.UpdatedAt),
			URL:         iss.URL,
			Severity:    "info",
			ObservedAt:  obs.UTC(),
			Fields: map[string]string{
				"repo":       repo,
				"state":      iss.State,
				"updated_at": iss.UpdatedAt,
				"number":     fmt.Sprintf("%d", iss.Number),
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
