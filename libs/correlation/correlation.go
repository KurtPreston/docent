package correlation

import (
	"regexp"
	"sort"
	"strings"

	"github.com/KurtPreston/docent/libs/model"
)

// Config controls anchor resolution and grouping.
type Config struct {
	// TicketPattern is a regex whose first capture group is the ticket key
	// (upper-cased). When set, it fully overrides Projects / AllowGeneric and
	// is used exactly as supplied (existing behavior).
	TicketPattern string

	// Projects restricts ticket-key matching to these JIRA project keys
	// (e.g. "SALSA", "JASPER"), so generic hyphenated tokens that aren't
	// really tickets — like "PR-7373" or "release-2026" — don't
	// false-match. Case-insensitive. Ignored when TicketPattern is set.
	Projects []string

	// AllowGeneric enables the fully generic DefaultTicketPattern when
	// TicketPattern is unset and Projects is empty. Callers that integrate
	// with JIRA (docentd with a jira directive, report with an enabled jira
	// directive) set this so zero-config ticket scanning still works before
	// followed_projects / observed issue keys narrow matching. Without it,
	// empty Projects means "match nothing", so Dependabot-style branch
	// names like fontawesome-free-7 can't invent phantom tickets.
	AllowGeneric bool
}

// DefaultTicketPattern matches JIRA-style keys like salsa-12345. Used only
// when AllowGeneric is set and neither TicketPattern nor Projects is configured.
const DefaultTicketPattern = `^([a-z]+-\d+)`

// defaultTicketCore is the unanchored, ungrouped core of DefaultTicketPattern,
// reused when building the bracket-tolerant anchored pattern below.
const defaultTicketCore = `[a-z]+-\d+`

// neverMatchTicket is returned when ticket scanning is intentionally disabled
// (no pattern, no projects, AllowGeneric false). It never matches.
var neverMatchTicket = regexp.MustCompile(`a\A`)

// ParseTicketKey extracts a ticket key from a name/title, or "" when none.
// It anchors the configured pattern to the start of the string, which is the
// right behavior for session/branch leaf names that begin with the ticket.
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
// when scan is true). Otherwise the pattern is built from cfg.Projects, or
// the fully generic default when Projects is empty and AllowGeneric is set.
// With neither Projects nor AllowGeneric, matching is disabled. The anchored
// form additionally tolerates a leading "[" so "[TICKET] subject"-style
// titles still parse when the ticket leads the string.
func ticketRegexp(cfg Config, scan bool) *regexp.Regexp {
	if cfg.TicketPattern != "" {
		return compileTicketPattern(cfg.TicketPattern, scan)
	}
	core := projectTicketCore(cfg.Projects)
	if core == "" {
		if !cfg.AllowGeneric {
			return neverMatchTicket
		}
		core = defaultTicketCore
	}
	pattern := `\b(` + core + `)`
	if !scan {
		pattern = `^\[?\s*(` + core + `)`
	}
	re, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		// core is built from QuoteMeta'd project keys plus a fixed literal
		// suffix, so this should not happen in practice. Prefer the generic
		// core only when AllowGeneric is set; otherwise disable matching.
		if !cfg.AllowGeneric {
			return neverMatchTicket
		}
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

// reflogOnlyWorkItem reports whether a work item's only evidence is git reflog
// entries. Such a unit was anchored purely by a branch being visited in the
// reflog, which isn't enough to treat it as real work.
func reflogOnlyWorkItem(wi *model.WorkItem) bool {
	if wi == nil || len(wi.Entities) == 0 {
		return false
	}
	for _, ent := range wi.Entities {
		if ent.Kind != "reflog" {
			return false
		}
	}
	return true
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
