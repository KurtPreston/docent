package correlation

import (
	"strings"

	"github.com/KurtPreston/docent/libs/model"
)

// DanglingTicketKeys returns the distinct ticket keys that a work item
// references but for which no JIRA metadata was collected (TicketRef.Title is
// empty). Only keys that parse as known JIRA project keys are returned, so a
// non-JIRA reference (e.g. a "PR-1234" false match) is never handed to the
// JIRA resolver.
func DanglingTicketKeys(workItems []model.WorkItem, cfg Config) []string {
	seen := map[string]bool{}
	var out []string
	for _, wi := range workItems {
		for _, tr := range wi.Tickets {
			if tr.Title != "" {
				continue
			}
			key := ParseTicketKey(tr.Key, cfg)
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, key)
		}
	}
	return out
}

// SeedProjectsFromSignals returns a copy of cfg whose Projects also includes
// any JIRA project key observed on collected jira signals (parsed from
// Fields["key"], e.g. "SALSA-1234" -> "SALSA") plus any seed projects. This
// narrows ticket-key matching to real, known projects. It's a no-op when
// cfg.TicketPattern is set, since Projects is ignored in that case.
func SeedProjectsFromSignals(signals []model.Signal, cfg Config, seedProjects []string) Config {
	if cfg.TicketPattern != "" {
		return cfg
	}
	seen := make(map[string]bool, len(cfg.Projects)+len(seedProjects))
	projects := make([]string, 0, len(cfg.Projects)+len(seedProjects))
	add := func(p string) {
		p = strings.ToUpper(strings.TrimSpace(p))
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		projects = append(projects, p)
	}
	for _, p := range cfg.Projects {
		add(p)
	}
	for _, p := range seedProjects {
		add(p)
	}
	for _, s := range signals {
		if s.Source != "jira" {
			continue
		}
		key := ""
		if s.Fields != nil {
			key = strings.TrimSpace(s.Fields["key"])
		}
		idx := strings.Index(key, "-")
		if idx <= 0 {
			continue
		}
		add(key[:idx])
	}
	cfg.Projects = projects
	return cfg
}

// FollowedProjectsFromDirectives extracts project keys from every enabled
// jira directive's config.followed_projects.
func FollowedProjectsFromDirectives(followedLists []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, raw := range followedLists {
		for _, p := range splitProjectList(raw) {
			p = strings.ToUpper(strings.TrimSpace(p))
			if p == "" || seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

func splitProjectList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	return strings.FieldsFunc(raw, func(r rune) bool {
		switch r {
		case ',', ';', '\n', '\r', '\t', ' ':
			return true
		default:
			return false
		}
	})
}
