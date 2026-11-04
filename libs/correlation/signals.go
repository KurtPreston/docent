package correlation

import (
	"time"

	"github.com/KurtPreston/docent/libs/model"
)

// commitLikeKind reports whether a signal kind is free-form git text where a
// ticket key, if present, is likely embedded rather than leading. These get
// the ScanTicketKey fallback during entity mapping.
func commitLikeKind(kind string) bool {
	switch kind {
	case "commit", "reflog", "github_commit", "repo_commit":
		return true
	}
	return false
}

// SignalsToEntities maps collector signals into participant entities.
func SignalsToEntities(signals []model.Signal, cfg Config) []model.Entity {
	out := make([]model.Entity, 0, len(signals))
	for _, s := range signals {
		ent := SignalToEntity(s, cfg)
		if ent.ID != "" {
			out = append(out, ent)
		}
	}
	return out
}

// SignalToEntity maps a single collector signal to the participant entity it
// becomes during correlation. It is deterministic, so callers can recompute a
// signal's entity ID to link raw signals back to the work item they joined.
func SignalToEntity(s model.Signal, cfg Config) model.Entity {
	coords := map[string]string{}
	if s.Repository != "" {
		coords["repo"] = s.Repository
	}
	ticket := ParseTicketKey(s.Title, cfg)
	if ticket == "" {
		ticket = ParseTicketKey(s.Summary, cfg)
	}
	// Commit/reflog subjects rarely lead with the ticket key, so fall back
	// to scanning anywhere in the text for git-ish signals. An explicit
	// ticket field (set by e.g. local-git from a branch name) still wins,
	// since Fields are copied into Coordinates below.
	if ticket == "" && commitLikeKind(s.Kind) {
		ticket = ScanTicketKey(s.Title, cfg)
		if ticket == "" {
			ticket = ScanTicketKey(s.Summary, cfg)
		}
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
	if s.Source == "backlog" && (s.Kind == "task" || s.Kind == "task_activity") {
		delete(ent.Coordinates, "ticket")
		ent.Coordinates["backlog_board"] = s.DirectiveID
		ent.Coordinates["backlog_id"] = s.Fields["id"]
	}
	if !s.ObservedAt.IsZero() {
		ent.State["observedAt"] = s.ObservedAt.UTC().Format(time.RFC3339Nano)
	}
	if s.Kind == "session" || s.Source == "wsm" || s.Source == "cursor" {
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
