package correlation

import (
	"regexp"
	"sort"
	"strings"
	"time"

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
// It anchors the configured pattern (default `^([a-z]+-\d+)`) to the start
// of the string, which is the right behavior for session/branch leaf names
// that begin with the ticket.
func ParseTicketKey(name string, cfg Config) string {
	re := ticketRegexp(cfg, false)
	m := re.FindStringSubmatch(strings.TrimSpace(name))
	if len(m) < 2 {
		return ""
	}
	return strings.ToUpper(m[1])
}

// ScanTicketKey extracts a ticket key from anywhere within a string, using
// the configured capture group but word-boundaried instead of anchored to
// the start. It exists for free-form text like commit subjects and reflog
// messages ("Fix SALSA-123", "checkout: moving from main to salsa-123-x")
// where the key does not lead the string. Because it scans anywhere it can
// false-match generic patterns, so callers should treat it as a fallback.
func ScanTicketKey(name string, cfg Config) string {
	re := ticketRegexp(cfg, true)
	m := re.FindStringSubmatch(strings.TrimSpace(name))
	if len(m) < 2 {
		return ""
	}
	return strings.ToUpper(m[1])
}

// ticketRegexp compiles the configured ticket pattern (case-insensitive).
// When scan is false the pattern is used as-is (the default is start-
// anchored); when scan is true a leading "^" is replaced by a word boundary
// so the key can be matched anywhere in the string.
func ticketRegexp(cfg Config, scan bool) *regexp.Regexp {
	pattern := cfg.TicketPattern
	if pattern == "" {
		pattern = DefaultTicketPattern
	}
	if scan {
		pattern = `\b` + strings.TrimPrefix(pattern, "^")
	}
	re, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		fallback := DefaultTicketPattern
		if scan {
			fallback = `\b` + strings.TrimPrefix(fallback, "^")
		}
		re = regexp.MustCompile("(?i)" + fallback)
	}
	return re
}

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
	if repo, branch, ok := branchAnchor(ent); ok {
		return branchKey(repo, branch)
	}
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

// BuildWorkItems merges entities into work-items by anchor key, then attaches
// orphan JIRA ticket entities to repo/branch units that reference them.
func BuildWorkItems(entities []model.Entity, cfg Config) []model.WorkItem {
	groups := map[string]*model.WorkItem{}
	order := []string{}

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

	attachTicketsToBranchUnits(groups, &order, cfg)

	out := make([]model.WorkItem, 0, len(order))
	for _, key := range order {
		wi := groups[key]
		if wi == nil {
			continue
		}
		enrichBranchWorkItem(wi, cfg)
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

// attachTicketsToBranchUnits moves JIRA entities from standalone ticket-keyed
// work items onto repo/branch units that reference the same ticket key.
func attachTicketsToBranchUnits(groups map[string]*model.WorkItem, order *[]string, cfg Config) {
	// ticket key -> jira entities collected from ticket-keyed work items
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
		var jira []model.Entity
		for _, ent := range wi.Entities {
			if isJiraEntity(ent) {
				jira = append(jira, ent)
			}
		}
		if len(jira) == 0 {
			continue
		}
		ticketEntities[t] = append(ticketEntities[t], jira...)
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
		keys := referencedTicketKeys(wi.Entities, cfg)
		for _, t := range keys {
			referenced[t] = true
		}
	}

	// Attach jira entities to branch units that reference each ticket.
	for t := range referenced {
		jira, ok := ticketEntities[t]
		if !ok {
			continue
		}
		for _, wi := range groups {
			if !strings.HasPrefix(wi.Key, "wb:") {
				continue
			}
			keys := referencedTicketKeys(wi.Entities, cfg)
			if !containsString(keys, t) {
				continue
			}
			for _, ent := range jira {
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

func referencedTicketKeys(entities []model.Entity, cfg Config) []string {
	seen := map[string]bool{}
	var out []string
	for _, ent := range entities {
		if t := ticketFromEntity(ent, cfg); t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
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

func enrichBranchWorkItem(wi *model.WorkItem, cfg Config) {
	if strings.HasPrefix(wi.Key, "wb:") {
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
			if p := ent.Coordinates["path"]; p != "" && (ent.Kind == "commit" || ent.Kind == "reflog" || ent.Kind == "branch") {
				wi.OpenPath = p
			}
		}
		if ts := ent.State["observedAt"]; ts != "" {
			if t, err := time.Parse(time.RFC3339Nano, ts); err == nil && t.After(lastActivity) {
				lastActivity = t
			}
		}
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
	if !lastActivity.IsZero() {
		wi.LastActivity = lastActivity.UTC().Format(time.RFC3339Nano)
	}
}

func needsFollowup(attention string) bool {
	return attention == "needs-followup" || attention == "working"
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
	if !s.ObservedAt.IsZero() {
		ent.State["observedAt"] = s.ObservedAt.UTC().Format(time.RFC3339Nano)
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
