package correlation

import (
	"sort"
	"strings"
	"time"

	"github.com/KurtPreston/docent/libs/model"
)

// branchKey returns the work-item key for a repo/branch unit.
func branchKey(repo, branch string) string {
	return "wb:" + repo + "@" + branch
}

// branchAnchor reports whether an entity can anchor a repo/branch work unit.
func branchAnchor(ent model.Entity) (repo, branch string, ok bool) {
	if ent.Coordinates == nil {
		return "", "", false
	}
	repo = strings.TrimSpace(ent.Coordinates["repo"])
	switch ent.Kind {
	case "branch", "commit", "reflog":
		branch = strings.TrimSpace(ent.Coordinates["branch"])
		if repo != "" && branch != "" {
			return repo, branch, true
		}
	default:
		if strings.Contains(ent.Kind, "pr") {
			// Group by base repo + head branch name. Fork PRs may use a
			// cross-fork head; we accept the common same-repo case.
			branch = strings.TrimSpace(ent.Coordinates["head_branch"])
			if repo != "" && branch != "" {
				return repo, branch, true
			}
		}
	}
	return "", "", false
}

// GroupKey returns the work-item anchor key for an entity.
func GroupKey(ent model.Entity, cfg Config) string {
	if board := ent.Coordinates["backlog_board"]; board != "" {
		return "backlog:" + board + ":" + ent.Coordinates["backlog_id"]
	}
	if repo, branch, ok := branchAnchor(ent); ok {
		return branchKey(repo, branch)
	}
	if t := ticketFromEntity(ent, cfg); t != "" {
		return t
	}
	return fallbackGroupKey(ent)
}

// ticketFromEntity resolves an entity's ticket key: an explicit
// Coordinates["ticket"] wins, but only when it actually looks like a ticket
// key under the configured pattern/projects — a collector-set field can
// itself be a false match (e.g. a branch named "backport/pr-7373-..." gets
// tagged "PR-7373" by a generic scan upstream), so an unrecognized value
// falls back to parsing the title instead of being trusted verbatim.
func ticketFromEntity(ent model.Entity, cfg Config) string {
	if ent.Coordinates["backlog_board"] != "" {
		return ""
	}
	if ent.Coordinates != nil {
		if t := strings.ToUpper(strings.TrimSpace(ent.Coordinates["ticket"])); t != "" && looksLikeTicketKey(t, cfg) {
			return t
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

// BuildWorkItems merges entities into work-items by anchor key, then attaches
// orphan JIRA ticket entities to repo/branch units that reference them.
func BuildWorkItems(entities []model.Entity, cfg Config) []model.WorkItem {
	groups := map[string]*model.WorkItem{}
	order := []string{}

	// Built once from the full, ungrouped entity set so a ticket can be
	// resolved via a PR reference even when the entity that names the
	// ticket (e.g. a squash-merge commit) lands in a different work item
	// than the one referencing its PR number.
	prTicket := buildPRTicketIndex(entities, cfg)
	jiraByKey := jiraMetadataByKey(entities)

	for _, ent := range entities {
		key := GroupKey(ent, cfg)
		wi, ok := groups[key]
		if !ok {
			title := workItemTitle(ent, key, cfg)
			wi = &model.WorkItem{
				Key:       key,
				Title:     title,
				Attention: "idle",
				Entities:  []model.Entity{},
			}
			groups[key] = wi
			order = append(order, key)
		}
		wi.Entities = append(wi.Entities, ent)
		if ent.Kind == "task" && ent.Coordinates["backlog_board"] != "" {
			wi.Title = ent.Title
		}
		// Promote jira ticket summary as work-item title when available.
		if isJiraEntity(ent) && ent.Title != "" && !strings.HasPrefix(wi.Key, "wb:") {
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

	attachTicketsToBranchUnits(groups, &order, cfg, prTicket)

	out := make([]model.WorkItem, 0, len(order))
	for _, key := range order {
		wi := groups[key]
		if wi == nil {
			continue
		}
		// A reflog line only records that a branch was visited (e.g.
		// "checkout: moving from main to salsa-42-x"), not that it's being
		// worked on. On its own that's not enough to justify a work item, so
		// drop units whose sole evidence is reflog entries. Reflogs still
		// count when combined with other evidence (a live branch/commit,
		// session, PR, or ticket) in the same unit.
		if reflogOnlyWorkItem(wi) {
			continue
		}
		enrichBranchWorkItem(wi, cfg, prTicket, jiraByKey)
		out = append(out, *wi)
	}
	sort.Slice(out, func(i, j int) bool {
		// needs-followup first
		if needsFollowup(out[i].Attention) != needsFollowup(out[j].Attention) {
			return needsFollowup(out[i].Attention)
		}
		// recency within attention tier
		if out[i].LastActivity != out[j].LastActivity {
			return out[i].LastActivity > out[j].LastActivity
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func workItemTitle(ent model.Entity, key string, cfg Config) string {
	if strings.HasPrefix(key, "wb:") {
		if _, branch, ok := branchAnchor(ent); ok {
			return branch
		}
		if repo, branch := parseBranchKey(key); branch != "" {
			_ = repo
			return branch
		}
	}
	if t := ticketFromEntity(ent, cfg); t != "" {
		return t
	}
	return ent.Title
}

func parseBranchKey(key string) (repo, branch string) {
	if !strings.HasPrefix(key, "wb:") {
		return "", ""
	}
	rest := strings.TrimPrefix(key, "wb:")
	at := strings.LastIndex(rest, "@")
	if at < 0 {
		return "", ""
	}
	return rest[:at], rest[at+1:]
}

func isJiraEntity(ent model.Entity) bool {
	switch ent.Kind {
	case "ticket", "issue", "issue_activity":
		return true
	}
	return false
}

// attachTicketsToBranchUnits moves standalone ticket-keyed work items onto the
// repo/branch units that reference the same ticket key. A ticket unit needs a
// JIRA entity to be eligible, but everything it holds moves across, because
// the unit is deleted afterwards and anything left behind is lost. That
// matters for entities the collector could not anchor to a branch: an
// events-side authored_pr carries no head branch, so a PR merged or closed
// yesterday groups by its ticket and would otherwise vanish the moment the
// same ticket also produced a JIRA update.
func attachTicketsToBranchUnits(groups map[string]*model.WorkItem, order *[]string, cfg Config, prTicket map[string]model.TicketRef) {
	// ticket key -> every entity collected from ticket-keyed work items
	ticketEntities := map[string][]model.Entity{}
	var ticketKeys []string

	for key, wi := range groups {
		if strings.HasPrefix(key, "wb:") {
			continue
		}
		t := strings.ToUpper(key)
		if t == "" || !looksLikeTicketKey(t, cfg) {
			continue
		}
		hasJira := false
		for _, ent := range wi.Entities {
			if isJiraEntity(ent) {
				hasJira = true
				break
			}
		}
		if !hasJira {
			continue
		}
		ticketEntities[t] = append(ticketEntities[t], wi.Entities...)
		ticketKeys = append(ticketKeys, t)
	}

	if len(ticketEntities) == 0 {
		return
	}

	referenced := map[string]bool{}
	for _, wi := range groups {
		if !strings.HasPrefix(wi.Key, "wb:") {
			continue
		}
		_, branch := parseBranchKey(wi.Key)
		keys := candidateTicketKeys(branch, wi.Entities, cfg, prTicket)
		for _, t := range keys {
			referenced[t] = true
		}
	}

	// Attach the ticket units' entities to branch units that reference each
	// ticket.
	for t := range referenced {
		moved, ok := ticketEntities[t]
		if !ok {
			continue
		}
		for _, wi := range groups {
			if !strings.HasPrefix(wi.Key, "wb:") {
				continue
			}
			_, branch := parseBranchKey(wi.Key)
			keys := candidateTicketKeys(branch, wi.Entities, cfg, prTicket)
			if !containsString(keys, t) {
				continue
			}
			for _, ent := range moved {
				if !entityPresent(wi.Entities, ent.ID) {
					wi.Entities = append(wi.Entities, ent)
				}
			}
		}
	}

	// Drop standalone ticket work items that were attached to branch units.
	for _, t := range ticketKeys {
		if !referenced[t] {
			continue
		}
		if groups[t] == nil {
			continue
		}
		delete(groups, t)
		filterOrder(order, t)
	}
}

func looksLikeTicketKey(key string, cfg Config) bool {
	re := ticketRegexp(cfg, false)
	return re.MatchString(strings.ToLower(key))
}

func containsString(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func entityPresent(entities []model.Entity, id string) bool {
	for _, ent := range entities {
		if ent.ID == id {
			return true
		}
	}
	return false
}

func filterOrder(order *[]string, drop string) {
	filtered := (*order)[:0]
	for _, k := range *order {
		if k != drop {
			filtered = append(filtered, k)
		}
	}
	*order = filtered
}

func enrichBranchWorkItem(wi *model.WorkItem, cfg Config, prTicket, jiraByKey map[string]model.TicketRef) {
	isBranchUnit := strings.HasPrefix(wi.Key, "wb:")
	if isBranchUnit {
		wi.Repo, wi.Branch = parseBranchKey(wi.Key)
		if wi.Branch != "" && (wi.Title == "" || wi.Title == wi.Key) {
			wi.Title = wi.Branch
		}
	}

	var lastActivity time.Time
	ticketSeen := map[string]bool{}

	for _, ent := range wi.Entities {
		if ent.Kind == "branch" {
			if p := ent.Coordinates["path"]; p != "" {
				wi.OpenPath = p
			}
		}
		if wi.OpenPath == "" {
			// A session's path is an open editor window's workspace folder, so
			// it is a checkout as surely as a branch's is. It matters most for
			// the work items that are *only* a session — a window on a
			// directory no collector attributed to a branch or ticket — which
			// otherwise had nowhere to point an "open" action, leaving the one
			// thing on screen you can definitely act on unclickable.
			if p := ent.Coordinates["path"]; p != "" && (ent.Kind == "commit" || ent.Kind == "reflog" || ent.Kind == "branch" || ent.Kind == "session") {
				wi.OpenPath = p
			}
		}
		if ts := ent.State["observedAt"]; ts != "" {
			if t, err := time.Parse(time.RFC3339Nano, ts); err == nil && t.After(lastActivity) {
				lastActivity = t
			}
		}
		// Ticket-anchored work items (key IS the ticket) were already
		// grouped onto exactly that ticket by GroupKey, so every entity
		// here legitimately shares it; a plain union is safe. Repo/branch
		// units get a precedence-ordered resolution below instead, since a
		// commit merely reachable from the branch (a shared-worktree
		// `git log --all` artifact) must not be trusted just because it
		// happens to carry some other, unrelated ticket.
		if !isBranchUnit {
			if t := ticketFromEntity(ent, cfg); t != "" && !ticketSeen[t] {
				ticketSeen[t] = true
				ref := model.TicketRef{Key: t}
				if isJiraEntity(ent) {
					ref.Title = ent.Title
					ref.URL = ent.URL
					if ent.State != nil {
						ref.Status = ent.State["status"]
					}
				}
				wi.Tickets = append(wi.Tickets, ref)
			}
		}
	}
	if !lastActivity.IsZero() {
		wi.LastActivity = lastActivity.UTC().Format(time.RFC3339Nano)
	}

	if isBranchUnit {
		for _, key := range candidateTicketKeys(wi.Branch, wi.Entities, cfg, prTicket) {
			ref := model.TicketRef{Key: key}
			if meta, ok := jiraByKey[key]; ok {
				ref.Title = meta.Title
				ref.URL = meta.URL
				ref.Status = meta.Status
			}
			wi.Tickets = append(wi.Tickets, ref)
		}
	}
}

func needsFollowup(attention string) bool {
	return attention == "needs-followup" || attention == "working"
}
