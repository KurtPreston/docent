package ai

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/KurtPreston/docent/libs/model"
)

func formatBacklogEntity(ent model.Entity) (string, error) {
	if ent.Coordinates["backlog_board"] == "" {
		return "", nil
	}
	id := ent.Coordinates["backlog_id"]
	if ent.Kind == "task" {
		return fmt.Sprintf("- current board state (not activity): %s [%s]\n", id, ent.State["status"]), nil
	}
	var changes map[string]struct {
		Before string `json:"before"`
		After  string `json:"after"`
	}
	if err := json.Unmarshal([]byte(ent.State["changes"]), &changes); err != nil {
		return "", fmt.Errorf("format backlog event %s: %w", ent.ID, err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "- recorded %s: %s; commit %s (recording time, not exact work time)\n",
		ent.State["observedAt"], id, ent.State["commit"])
	keys := make([]string, 0, len(changes))
	for key := range changes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		change := changes[key]
		fmt.Fprintf(&b, "  - %s: %q -> %q\n", key, change.Before, change.After)
	}
	return b.String(), nil
}

func hasBacklogInput(in RunInput) bool {
	for _, s := range in.Statuses {
		if s.Source == "backlog" {
			return true
		}
	}
	for _, wi := range in.WorkItems {
		for _, ent := range wi.Entities {
			if ent.Coordinates["backlog_board"] != "" {
				return true
			}
		}
	}
	return false
}

// The deterministic standup otherwise only understands PR/commit activity.
// Separate board sections prevent current snapshots becoming yesterday's work.
func backlogDailyContext(in RunInput) string {
	var b strings.Builder
	for _, kind := range []string{"task_activity", "task"} {
		var items []model.WorkItem
		for _, wi := range in.WorkItems {
			copy := wi
			copy.Entities = nil
			for _, ent := range wi.Entities {
				if ent.Coordinates["backlog_board"] != "" && ent.Kind == kind {
					copy.Entities = append(copy.Entities, ent)
				}
			}
			if len(copy.Entities) > 0 {
				items = append(items, copy)
			}
		}
		if len(items) == 0 {
			continue
		}
		heading := "Backlog recorded activity"
		if kind == "task" {
			heading = "Current Backlog board (planning context, not activity)"
		}
		fmt.Fprintf(&b, "\n\n*%s*\n\n", heading)
		body, err := formatWorkItemsMarkdown(items, 3)
		if err != nil {
			fmt.Fprintf(&b, "_formatter error: %v_", err)
		} else {
			b.WriteString(body)
		}
	}
	return b.String()
}
