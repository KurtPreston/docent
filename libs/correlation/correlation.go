package correlation

import (
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/KurtPreston/docent/libs/model"
)

// Config controls anchor resolution and grouping.
type Config struct {
	// TicketPattern is a regex whose first capture group is the ticket key
	// (upper-cased). When set, it fully overrides Projects and the built-in
	// default and is used exactly as supplied (existing behavior).
	// Default when unset: ^([a-z]+-\d+)
	TicketPattern string

	// Projects restricts ticket-key matching to these JIRA project keys
	// (e.g. "SALSA", "JASPER"), so generic hyphenated tokens that aren't
	// really tickets — like "PR-7373" or "release-2026" — don't
	// false-match. Case-insensitive. Ignored when TicketPattern is set;
	// when empty (and TicketPattern is also empty), matching falls back to
	// the fully generic DefaultTicketPattern.
	Projects []string
}

// DefaultTicketPattern matches JIRA-style keys like salsa-12345. Used only
// when neither TicketPattern nor Projects is configured.
const DefaultTicketPattern = `^([a-z]+-\d+)`

// defaultTicketCore is the unanchored, ungrouped core of DefaultTicketPattern,
// reused when building the bracket-tolerant anchored pattern below.
const defaultTicketCore = `[a-z]+-\d+`

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
// An explicit cfg.TicketPattern fully overrides matching and is used as-is
// (existing behavior: start-anchored, with "^" swapped for a word boundary
// when scan is true). Otherwise the pattern is built from cfg.Projects (or
// the fully generic default when Projects is empty), and the anchored form
// additionally tolerates a leading "[" so "[TICKET] subject"-style titles
// still parse when the ticket leads the string.
func ticketRegexp(cfg Config, scan bool) *regexp.Regexp {
	if cfg.TicketPattern != "" {
		return compileTicketPattern(cfg.TicketPattern, scan)
	}
	core := projectTicketCore(cfg.Projects)
	if core == "" {
		core = defaultTicketCore
	}
	pattern := `\b(` + core + `)`
	if !scan {
		pattern = `^\[?\s*(` + core + `)`
	}
	re, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		// core is built from QuoteMeta'd project keys plus a fixed literal
		// suffix, so this should not happen in practice; fall back to the
		// fully generic core just in case.
		fallback := `\b(` + defaultTicketCore + `)`
		if !scan {
			fallback = `^\[?\s*(` + defaultTicketCore + `)`
		}
		re = regexp.MustCompile("(?i)" + fallback)
	}
	return re
}

// compileTicketPattern preserves the pre-existing behavior for a fully
// custom, user-supplied TicketPattern: used exactly as-is when anchored, or
// with a leading "^" swapped for a word boundary when scanning.
func compileTicketPattern(pattern string, scan bool) *regexp.Regexp {
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

// projectTicketCore builds an ungrouped, unanchored ticket-key core
// restricted to the given project keys, e.g. ["SALSA","JASPER"] ->
// "(?:SALSA|JASPER)-\d+". Returns "" when projects has no usable entries.
func projectTicketCore(projects []string) string {
	seen := map[string]bool{}
	var quoted []string
	for _, p := range projects {
		p = strings.ToUpper(strings.TrimSpace(p))
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		quoted = append(quoted, regexp.QuoteMeta(p))
	}
	if len(quoted) == 0 {
		return ""
	}
	sort.Strings(quoted)
	return `(?:` + strings.Join(quoted, "|") + `)-\d+`
}

// prReferenceRe matches a PR number referenced in free-form text: "#7373",
// "PR-7373", "PR 7373", "pr/7373" (case-insensitive).
var prReferenceRe = regexp.MustCompile(`(?i)(?:#|\bpr[-/ ]?)(\d+)`)

// parsePRNumbers extracts every PR number referenced anywhere in text
// (order preserved, deduplicated), or nil when none are found.
func parsePRNumbers(text string) []string {
	matches := prReferenceRe.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range matches {
		n := m[1]
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	return out
}

// prNumberFromURL extracts the PR number from a ".../pull/<n>" URL, or ""
// when the URL doesn't look like a PR link.
func prNumberFromURL(raw string) string {
	const marker = "/pull/"
	i := strings.LastIndex(raw, marker)
	if i < 0 {
		return ""
	}
	rest := raw[i+len(marker):]
	j := 0
	for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
		j++
	}
	if j == 0 {
		return ""
	}
	return rest[:j]
}

// prNumbersFromEntity extracts PR numbers referenced by an entity's title
// text or its URL (a ".../pull/<n>" link).
func prNumbersFromEntity(ent model.Entity) []string {
	nums := parsePRNumbers(ent.Title)
	if n := prNumberFromURL(ent.URL); n != "" {
		nums = append(nums, n)
	}
	return nums
}

// buildPRTicketIndex scans every entity for one that carries both a
// resolvable ticket and a PR-number reference in its own text/URL — e.g. a
// squash-merge commit subject "[SALSA-12455] ... (#7373)" — and returns a
// PR-number -> ticket index (first entity found for a given number wins).
// This lets a branch or commit that only references a PR number (no ticket
// of its own) resolve the ticket that PR closes, using data already
// collected elsewhere: no extra network calls.
func buildPRTicketIndex(entities []model.Entity, cfg Config) map[string]model.TicketRef {
	index := map[string]model.TicketRef{}
	for _, ent := range entities {
		t := ticketFromEntity(ent, cfg)
		if t == "" {
			continue
		}
		for _, n := range prNumbersFromEntity(ent) {
			if _, exists := index[n]; !exists {
				index[n] = model.TicketRef{Key: t}
			}
		}
	}
	return index
}

// jiraMetadataByKey indexes JIRA entity metadata (title/url/status) by
// ticket key, so a key resolved via a branch name or a PR reference still
// gets its JIRA title/status/url whenever a jira entity for that key was
// collected.
func jiraMetadataByKey(entities []model.Entity) map[string]model.TicketRef {
	index := map[string]model.TicketRef{}
	for _, ent := range entities {
		if !isJiraEntity(ent) {
			continue
		}
		key := strings.ToUpper(strings.TrimSpace(ent.Coordinates["ticket"]))
		if key == "" {
			continue
		}
		ref := model.TicketRef{Key: key, Title: ent.Title, URL: ent.URL}
		if ent.State != nil {
			ref.Status = ent.State["status"]
		}
		index[key] = ref
	}
	return index
}

// commitLikeEntityKind reports whether an entity kind directly carries git
// history/state text (as opposed to a PR/session/jira entity), for the
// branch-ticket consensus rule below.
func commitLikeEntityKind(kind string) bool {
	switch kind {
	case "commit", "reflog", "branch":
		return true
	}
	return false
}

// commitConsensusTicket returns the ticket key that every commit/reflog/
// branch-kind entity carrying one agrees on, or "" when none of them carry
// one, or when they disagree (2+ distinct values). Disagreement most likely
// means one or more of those entities were merely reachable from this
// branch — a `git log --all` artifact of a shared repository/worktree setup
// — rather than truly belonging to it, in which case showing nothing is
// better than guessing wrong.
func commitConsensusTicket(entities []model.Entity, cfg Config) string {
	seen := map[string]bool{}
	var distinct []string
	for _, ent := range entities {
		if !commitLikeEntityKind(ent.Kind) {
			continue
		}
		t := ticketFromEntity(ent, cfg)
		if t == "" || seen[t] {
			continue
		}
		seen[t] = true
		distinct = append(distinct, t)
	}
	if len(distinct) != 1 {
		return ""
	}
	return distinct[0]
}

// candidateTicketKeys returns, in precedence order, the ticket keys a
// repo/branch work item should carry:
//  1. the branch name's own ticket;
//  2. a PR entity anchored to this branch resolving its own ticket;
//  3. the ticket every commit/reflog/branch-kind entity agrees on (if any
//     disagree, none of them are trusted);
//  4. a ticket resolved via a PR number referenced by the branch name, or
//     by an entity that has no directly-resolvable ticket of its own (so a
//     leaked, unrelated commit that already resolves its own ticket can't
//     also drag in another one just because it mentions a PR number).
//
// The result is deduplicated, preserving this precedence order, so the
// first entry is the primary ticket.
func candidateTicketKeys(branch string, entities []model.Entity, cfg Config, prTicket map[string]model.TicketRef) []string {
	seen := map[string]bool{}
	var out []string
	add := func(key string) {
		if key == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, key)
	}

	add(ParseTicketKey(branch, cfg))

	for _, ent := range entities {
		if !strings.Contains(ent.Kind, "pr") {
			continue
		}
		add(ticketFromEntity(ent, cfg))
	}

	add(commitConsensusTicket(entities, cfg))

	for _, n := range parsePRNumbers(branch) {
		if ref, ok := prTicket[n]; ok {
			add(ref.Key)
		}
	}
	for _, ent := range entities {
		if ticketFromEntity(ent, cfg) != "" {
			continue
		}
		for _, n := range prNumbersFromEntity(ent) {
			if ref, ok := prTicket[n]; ok {
				add(ref.Key)
			}
		}
	}
	return out
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

// ticketFromEntity resolves an entity's ticket key: an explicit
// Coordinates["ticket"] wins, but only when it actually looks like a ticket
// key under the configured pattern/projects — a collector-set field can
// itself be a false match (e.g. a branch named "backport/pr-7373-..." gets
// tagged "PR-7373" by a generic scan upstream), so an unrecognized value
// falls back to parsing the title instead of being trusted verbatim.
func ticketFromEntity(ent model.Entity, cfg Config) string {
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

// attachTicketsToBranchUnits moves JIRA entities from standalone ticket-keyed
// work items onto repo/branch units that reference the same ticket key.
func attachTicketsToBranchUnits(groups map[string]*model.WorkItem, order *[]string, cfg Config, prTicket map[string]model.TicketRef) {
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
		_, branch := parseBranchKey(wi.Key)
		keys := candidateTicketKeys(branch, wi.Entities, cfg, prTicket)
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
			_, branch := parseBranchKey(wi.Key)
			keys := candidateTicketKeys(branch, wi.Entities, cfg, prTicket)
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
			if p := ent.Coordinates["path"]; p != "" && (ent.Kind == "commit" || ent.Kind == "reflog" || ent.Kind == "branch") {
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
