package correlation

import (
	"regexp"
	"sort"
	"strings"

	"github.com/kurt/slakkr-ai/libs/model"
)

// Config controls anchor resolution and grouping.
type Config struct {
	// TicketPattern is a regex whose first capture group is the ticket key (upper-cased).
	// Default: ^([a-z]+-\d+)
	TicketPattern string
}

// DefaultTicketPattern matches JIRA-style keys like salsa-12345.
const DefaultTicketPattern = `^([a-z]+-\d+)`

// ParseTicketKey extracts a ticket key from a name/title, or "" when none.
func ParseTicketKey(name string, cfg Config) string {
	pattern := cfg.TicketPattern
	if pattern == "" {
		pattern = DefaultTicketPattern
	}
	re, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		re = regexp.MustCompile("(?i)" + DefaultTicketPattern)
	}
	m := re.FindStringSubmatch(strings.TrimSpace(name))
	if len(m) < 2 {
		return ""
	}
	return strings.ToUpper(m[1])
}

// GroupKey returns the work-item anchor key for an entity.
func GroupKey(ent model.Entity, cfg Config) string {
	if t := ticketFromEntity(ent, cfg); t != "" {
		return t
	}
	return fallbackGroupKey(ent)
}

func ticketFromEntity(ent model.Entity, cfg Config) string {
	if ent.Coordinates != nil {
		if t := ent.Coordinates["ticket"]; t != "" {
			return strings.ToUpper(t)
		}
	}
	return ParseTicketKey(ent.Title, cfg)
}

func fallbackGroupKey(ent model.Entity) string {
	switch ent.Kind {
	case "session":
		if ent.ID != "" {
			return "session:" + ent.ID
		}
	case "pr":
		if ent.Coordinates != nil {
			if repo := ent.Coordinates["repo"]; repo != "" {
				if num := ent.Coordinates["number"]; num != "" {
					return "pr:" + repo + "#" + num
				}
			}
		}
	case "branch", "repo":
		if ent.Coordinates != nil {
			if repo := ent.Coordinates["repo"]; repo != "" {
				return "repo:" + repo
			}
		}
	}
	if ent.ID != "" {
		return ent.Kind + ":" + ent.ID
	}
	return "item:" + ent.Kind
}

// BuildWorkItems merges entities into work-items by anchor key.
func BuildWorkItems(entities []model.Entity, cfg Config) []model.WorkItem {
	groups := map[string]*model.WorkItem{}
	order := []string{}

	for _, ent := range entities {
		key := GroupKey(ent, cfg)
		wi, ok := groups[key]
		if !ok {
			title := ent.Title
			if t := ticketFromEntity(ent, cfg); t != "" {
				title = t
				if ent.Title != "" && !strings.EqualFold(ent.Title, t) {
					// keep ticket as key; summary may be filled later from jira entity
				}
			}
			wi = &model.WorkItem{
				Key:        key,
				Title:      title,
				Attention:  "idle",
				Entities:   []model.Entity{},
			}
			groups[key] = wi
			order = append(order, key)
		}
		wi.Entities = append(wi.Entities, ent)
		// Promote jira ticket summary as work-item title when available.
		if ent.Kind == "ticket" && ent.Title != "" {
			if t := ticketFromEntity(ent, cfg); t != "" {
				wi.Key = t
				wi.Title = ent.Title
			}
		}
		// Bubble up session attention.
		if ent.Kind == "session" && ent.State != nil {
			if att := ent.State["attention"]; att != "" && att != "idle" {
				wi.Attention = att
			}
		}
	}

	out := make([]model.WorkItem, 0, len(order))
	for _, key := range order {
		wi := groups[key]
		if wi == nil {
			continue
		}
		out = append(out, *wi)
	}
	sort.Slice(out, func(i, j int) bool {
		// needs-followup first
		if needsFollowup(out[i].Attention) != needsFollowup(out[j].Attention) {
			return needsFollowup(out[i].Attention)
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func needsFollowup(attention string) bool {
	return attention == "needs-followup" || attention == "working"
}

// SignalsToEntities maps collector signals into participant entities.
func SignalsToEntities(signals []model.Signal, cfg Config) []model.Entity {
	out := make([]model.Entity, 0, len(signals))
	for _, s := range signals {
		ent := signalToEntity(s, cfg)
		if ent.ID != "" {
			out = append(out, ent)
		}
	}
	return out
}

func signalToEntity(s model.Signal, cfg Config) model.Entity {
	coords := map[string]string{}
	if s.Repository != "" {
		coords["repo"] = s.Repository
	}
	ticket := ParseTicketKey(s.Title, cfg)
	if ticket == "" {
		ticket = ParseTicketKey(s.Summary, cfg)
	}
	if ticket != "" {
		coords["ticket"] = ticket
	}

	kind := s.Kind
	if kind == "" {
		kind = s.Source
	}

	id := s.StableID
	if id == "" {
		id = s.Source + ":" + s.Kind + ":" + s.Title
	}

	ent := model.Entity{
		ID:          id,
		Kind:        kind,
		Title:       s.Title,
		URL:         s.URL,
		Coordinates: coords,
		State:       map[string]string{},
	}
	if s.Fields != nil {
		for k, v := range s.Fields {
			ent.Coordinates[k] = v
			ent.State[k] = v
		}
	}
	if s.Kind == "session" || s.Source == "docent-wm" {
		ent.Kind = "session"
		if s.Fields != nil {
			if wid := s.Fields["window_id"]; wid != "" {
				ent.WindowID = wid
			}
			if m := s.Fields["machine"]; m != "" {
				ent.Machine = m
			}
		}
	}
	return ent
}
