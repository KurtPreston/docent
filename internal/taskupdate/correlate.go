package taskupdate

import (
	"net/url"
	"strings"

	"github.com/kurt/slakkr-ai/internal/userdata"
)

// FromUserdataSignal builds a match-friendly view for correlation heuristics.
func FromUserdataSignal(s userdata.Signal) NormalizedSignal {
	n := NormalizedSignal{
		ID:        s.ID,
		Source:    s.Source,
		Kind:      s.Kind,
		SourceID:  s.SourceID,
		URL:       s.URL,
		Title:     s.Title,
		Summary:   s.Summary,
		ProjectID: s.ProjectID,
	}
	if s.Source == "jira" {
		n.JiraKey = strings.TrimSpace(s.SourceID)
	}
	return n
}

func taskIsTerminal(t userdata.Task) bool {
	return t.Status == userdata.TaskStatusDone || t.Status == userdata.TaskStatusDropped
}

// DeterministicTaskMatch returns a task id if this signal clearly maps to a single non-terminal task, else "".
func DeterministicTaskMatch(n NormalizedSignal, tasks []userdata.Task) string {
	var candidates []string
	for _, t := range tasks {
		if taskIsTerminal(t) {
			continue
		}
		if n.ProjectID != "" && t.ProjectID != n.ProjectID {
			continue
		}
		if n.URL != "" && linkTaskMatch(n.URL, t) {
			candidates = append(candidates, t.ID)
			continue
		}
		if n.JiraKey != "" && jiraKeyMatchesTask(n.JiraKey, t) {
			candidates = append(candidates, t.ID)
		}
	}
	if len(candidates) == 1 {
		return candidates[0]
	}
	return ""
}

func linkTaskMatch(signalURL string, t userdata.Task) bool {
	su, err := url.Parse(strings.TrimSpace(signalURL))
	if err != nil {
		return false
	}
	sPath := normPath(su.Path)
	sHost := strings.ToLower(su.Host)
	for _, l := range t.Links {
		lu, err := url.Parse(strings.TrimSpace(l.URL))
		if err != nil {
			continue
		}
		if normPath(lu.Path) == sPath && strings.EqualFold(lu.Host, sHost) {
			return true
		}
		// also allow prefix match for issue keys in path
		if sPath != "" && strings.HasSuffix(normPath(lu.Path), sPath) {
			return true
		}
	}
	return false
}

func normPath(p string) string {
	return strings.TrimSuffix(strings.ToLower(p), "/")
}

func jiraKeyMatchesTask(key string, t userdata.Task) bool {
	upper := strings.ToUpper(key)
	needle := strings.ToLower(key)
	if strings.Contains(strings.ToLower(t.Name), needle) {
		return true
	}
	if strings.Contains(strings.ToLower(t.Description), needle) {
		return true
	}
	if strings.Contains(strings.ToLower(t.NextAction), needle) {
		return true
	}
	for _, l := range t.Links {
		if strings.Contains(l.URL, upper) {
			return true
		}
	}
	return false
}

// MergeWithExisting copies resolution fields from a stored signal when ids match.
func MergeWithExisting(n NormalizedSignal, existing userdata.SignalsFile) (userdata.Signal, bool) {
	for _, s := range existing.Signals {
		if s.ID == n.ID {
			obs := s.ObservedAt
			if obs.IsZero() {
				obs = n.ObservedAt
			}
			merged := userdata.Signal{
				ID:           n.ID,
				Source:       n.Source,
				Kind:         n.Kind,
				SourceID:     n.SourceID,
				URL:          n.URL,
				Title:        n.Title,
				Summary:      n.Summary,
				ProjectID:    n.ProjectID,
				ObservedAt:   obs,
				LastSeenAt:   n.ObservedAt,
				Resolution:   s.Resolution,
				TaskID:       s.TaskID,
				Reason:       s.Reason,
				ClassifiedAt: s.ClassifiedAt,
			}
			return merged, true
		}
	}
	return userdata.Signal{
		ID:         n.ID,
		Source:     n.Source,
		Kind:       n.Kind,
		SourceID:   n.SourceID,
		URL:        n.URL,
		Title:      n.Title,
		Summary:    n.Summary,
		ProjectID:  n.ProjectID,
		ObservedAt: n.ObservedAt,
		LastSeenAt: n.ObservedAt,
		Resolution: userdata.SignalResolutionPending,
	}, false
}
