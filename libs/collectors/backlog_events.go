package collectors

import (
	"encoding/json"
	"sort"
	"strings"
)

type backlogChange struct {
	Before string `json:"before"`
	After  string `json:"after"`
}

type backlogSnapshots struct {
	before, after map[string]backlogSnapshotTask
}

func backlogRevisionEvents(directive string, rev backlogRevision, snapshots backlogSnapshots) []StatusItem {
	ids := make([]string, 0, len(snapshots.after))
	for id := range snapshots.after {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var events []StatusItem
	for _, id := range ids {
		after := snapshots.after[id]
		before, exists := snapshots.before[id]
		changes := map[string]backlogChange{}
		if !exists {
			label := "first recorded on board"
			if rev.parent == "" {
				label = "present in initial recorded baseline"
			}
			changes["record"] = backlogChange{After: label}
		} else {
			if before.Status != after.Status {
				changes["status"] = backlogChange{Before: before.Status, After: after.Status}
			}
			if before.Title != after.Title {
				changes["title"] = backlogChange{Before: before.Title, After: after.Title}
			}
			for key, value := range after.Sections {
				if old := before.Sections[key]; old != value {
					changes["section: "+key] = backlogContentChange(old, value)
				}
			}
			for key, value := range before.Sections {
				if _, ok := after.Sections[key]; !ok {
					changes["section: "+key] = backlogChange{Before: value}
				}
			}
		}
		if len(changes) == 0 {
			continue
		}
		// A map of strings cannot fail JSON encoding.
		encoded, _ := json.Marshal(changes)
		events = append(events, StatusItem{
			DirectiveID: directive, Source: "backlog", Kind: "task_activity",
			Title: after.Title, Summary: id + ": recorded task changes",
			ObservedAt: rev.at, IsSelf: true,
			StableID: "task-activity:" + directive + ":" + id + ":" + rev.hash,
			Fields: map[string]string{
				"id": id, "status": after.Status, "commit": rev.hash,
				"changes": string(encoded), "time_basis": "git_commit",
				"commit_author": rev.author,
			},
		})
	}
	return events
}

// Strip unchanged surrounding lines so appended notes don't re-report an
// earlier accomplishment. This is a content delta, not a second full snapshot.
func backlogContentChange(before, after string) backlogChange {
	old, next := strings.Split(before, "\n"), strings.Split(after, "\n")
	for len(old) > 0 && len(next) > 0 && old[0] == next[0] {
		old, next = old[1:], next[1:]
	}
	for len(old) > 0 && len(next) > 0 && old[len(old)-1] == next[len(next)-1] {
		old, next = old[:len(old)-1], next[:len(next)-1]
	}
	return backlogChange{Before: strings.Join(old, "\n"), After: strings.Join(next, "\n")}
}
