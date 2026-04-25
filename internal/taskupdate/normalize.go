package taskupdate

import (
	"strings"

	"github.com/kurt/slakkr-ai/internal/collectors"
	"github.com/kurt/slakkr-ai/internal/userdata"
)

// IsActionableStatus returns false for placeholder and empty "no work" status rows.
func IsActionableStatus(s collectors.StatusItem) bool {
	if s.Severity == "error" && s.Kind == "collector_error" {
		return true
	}
	lowKind := strings.ToLower(s.Kind)
	lowSource := strings.ToLower(s.Source)
	if lowKind == "not_configured" {
		return false
	}
	// GitHub activity "nothing found" placeholder
	if lowSource == "github-activity" && lowKind == "activity" {
		if strings.Contains(strings.ToLower(s.Summary), "no open") {
			return false
		}
	}
	// Jira empty JQL
	if lowSource == "jira" && (lowKind == "issue_list" || lowKind == "issuelist") {
		if strings.Contains(strings.ToLower(s.Summary), "no issues") {
			return false
		}
	}
	if lowSource == "local-git" && lowKind == "git_report" {
		return false
	}
	return true
}

// NormalizedSignal is a working copy before merging with userdata.
type NormalizedSignal struct {
	ID         string
	Source     string
	Kind       string
	SourceID   string
	JiraKey    string
	URL        string
	Title      string
	Summary    string
	ProjectID  string
	ObservedAt userdata.YAMLDateTime
	StableID   string
}

// FromStatusItem maps a status row into a normalized signal with deterministic ID.
func FromStatusItem(s collectors.StatusItem) NormalizedSignal {
	var jiraKey string
	if s.Fields != nil {
		jiraKey = strings.TrimSpace(s.Fields["key"])
	}
	return NormalizedSignal{
		ID:         DeriveSignalID(s),
		Source:     s.Source,
		Kind:       s.Kind,
		SourceID:   SourceID(s),
		JiraKey:    jiraKey,
		URL:        s.URL,
		Title:      s.Title,
		Summary:    s.Summary,
		ProjectID:  s.ProjectID,
		ObservedAt: userdata.YAMLDateTime{Time: s.ObservedAt},
		StableID:   s.StableID,
	}
}
