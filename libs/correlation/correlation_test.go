package correlation

import (
	"testing"

	"github.com/KurtPreston/docent/libs/model"
)

func TestParseTicketKey(t *testing.T) {
	cfg := Config{}
	tests := []struct {
		in   string
		want string
	}{
		{"salsa-12345-foo-bar", "SALSA-12345"},
		{"SALSA-1", "SALSA-1"},
		{"no-ticket-here", ""},
		{"", ""},
	}
	for _, tc := range tests {
		got := ParseTicketKey(tc.in, cfg)
		if got != tc.want {
			t.Errorf("ParseTicketKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestBuildWorkItems_ticketGrouping(t *testing.T) {
	cfg := Config{}
	entities := []model.Entity{
		{ID: "jira:SALSA-1", Kind: "ticket", Title: "Fix widget NPE", Coordinates: map[string]string{"ticket": "SALSA-1"}},
		{ID: "pr:org/repo#5", Kind: "pr", Title: "salsa-1 fix", Coordinates: map[string]string{"ticket": "SALSA-1", "repo": "org/repo", "number": "5"}},
		{ID: "session:foo", Kind: "session", Title: "salsa-1-foo", Coordinates: map[string]string{"ticket": "SALSA-1"}},
	}
	items := BuildWorkItems(entities, cfg)
	if len(items) != 1 {
		t.Fatalf("got %d work items, want 1", len(items))
	}
	if items[0].Key != "SALSA-1" {
		t.Errorf("key = %q, want SALSA-1", items[0].Key)
	}
	if len(items[0].Entities) != 3 {
		t.Errorf("entities = %d, want 3", len(items[0].Entities))
	}
}

func TestBuildWorkItems_noTicketRepoFallback(t *testing.T) {
	cfg := Config{}
	entities := []model.Entity{
		{ID: "pr:org/repo#9", Kind: "pr", Title: "quick fix", Coordinates: map[string]string{"repo": "org/repo", "number": "9"}},
	}
	items := BuildWorkItems(entities, cfg)
	if len(items) != 1 {
		t.Fatalf("got %d work items, want 1", len(items))
	}
	if items[0].Key != "pr:org/repo#9" {
		t.Errorf("key = %q, want pr:org/repo#9", items[0].Key)
	}
}

func TestBuildWorkItems_multiTicketPrimary(t *testing.T) {
	cfg := Config{}
	// Entity with ticket in title uses that ticket as anchor.
	entities := []model.Entity{
		{ID: "pr:1", Kind: "pr", Title: "SALSA-2 and SALSA-3 combined", Coordinates: map[string]string{"repo": "org/r", "number": "1"}},
	}
	items := BuildWorkItems(entities, cfg)
	if len(items) != 1 {
		t.Fatalf("got %d items", len(items))
	}
	if items[0].Key != "SALSA-2" {
		t.Errorf("primary ticket key = %q, want SALSA-2 (first match)", items[0].Key)
	}
}

func TestParseTicketKey_projectRestricted(t *testing.T) {
	cfg := Config{Projects: []string{"salsa"}}
	tests := []struct {
		in   string
		want string
	}{
		{"SALSA-12684", "SALSA-12684"},
		{"[SALSA-12455] enable sound repeat loops", "SALSA-12455"},
		{"salsa-1-fix", "SALSA-1"},
		// Generic hyphenated tokens that used to false-match must not match
		// once matching is restricted to configured projects.
		{"PR-7373", ""},
		{"backport/pr-7373-to-release-2026.4.2", ""},
		{"release-2026", ""},
		// An unconfigured project key must not match either.
		{"JASPER-1", ""},
	}
	for _, tc := range tests {
		if got := ParseTicketKey(tc.in, cfg); got != tc.want {
			t.Errorf("ParseTicketKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestScanTicketKey_projectRestricted(t *testing.T) {
	cfg := Config{Projects: []string{"SALSA", "JASPER"}}
	tests := []struct {
		in   string
		want string
	}{
		{"[CONFLICTS] Backport PR #7373", ""},
		{"d786ad77b1 [SALSA-12455] enable sound repeat loops (#7373)", "SALSA-12455"},
		{"[JASPER-3300] some other project", "JASPER-3300"},
		{"[PR-7373] fix conflict", ""},
	}
	for _, tc := range tests {
		if got := ScanTicketKey(tc.in, cfg); got != tc.want {
			t.Errorf("ScanTicketKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestTicketKey_genericFallbackUnaffected(t *testing.T) {
	// With no Projects and no TicketPattern configured, behavior must stay
	// exactly as before (generic [a-z]+-\d+ core).
	cfg := Config{}
	if got := ParseTicketKey("salsa-12345-foo-bar", cfg); got != "SALSA-12345" {
		t.Errorf("ParseTicketKey = %q, want SALSA-12345", got)
	}
	if got := ScanTicketKey("Fix SALSA-123 crash on load", cfg); got != "SALSA-123" {
		t.Errorf("ScanTicketKey = %q, want SALSA-123", got)
	}
}

func TestTicketKey_explicitPatternOverridesProjects(t *testing.T) {
	// An explicit TicketPattern fully overrides Projects, matching
	// pre-existing behavior for a custom pattern.
	cfg := Config{TicketPattern: `^([A-Z]+-\d+)`, Projects: []string{"SALSA"}}
	if got := ParseTicketKey("JASPER-9", cfg); got != "JASPER-9" {
		t.Errorf("ParseTicketKey = %q, want JASPER-9 (explicit pattern should win over Projects)", got)
	}
}

func TestScanTicketKey(t *testing.T) {
	cfg := Config{}
	tests := []struct {
		in   string
		want string
	}{
		{"Fix SALSA-123 crash on load", "SALSA-123"},
		{"checkout: moving from main to salsa-999-feature", "SALSA-999"},
		{"salsa-1 at start still works", "SALSA-1"},
		{"no ticket in this subject", ""},
		{"", ""},
	}
	for _, tc := range tests {
		if got := ScanTicketKey(tc.in, cfg); got != tc.want {
			t.Errorf("ScanTicketKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// ParseTicketKey stays anchored and must NOT match a mid-string key.
	if got := ParseTicketKey("Fix SALSA-123 crash", cfg); got != "" {
		t.Errorf("ParseTicketKey should stay anchored, got %q", got)
	}
}

func TestSignalToEntity_commitFallback(t *testing.T) {
	cfg := Config{}
	// A commit whose subject embeds the ticket mid-string should still
	// correlate via the ScanTicketKey fallback.
	ent := SignalToEntity(model.Signal{
		Source: "local-git",
		Kind:   "commit",
		Title:  "Fix the SALSA-42 regression",
	}, cfg)
	if ent.Coordinates["ticket"] != "SALSA-42" {
		t.Errorf("commit ticket = %q, want SALSA-42", ent.Coordinates["ticket"])
	}
	// A non-commit kind must not get the anywhere-scan fallback.
	ent2 := SignalToEntity(model.Signal{
		Source: "slack",
		Kind:   "message",
		Title:  "chatting about SALSA-42",
	}, cfg)
	if ent2.Coordinates["ticket"] != "" {
		t.Errorf("non-commit kind should not scan-fallback, got %q", ent2.Coordinates["ticket"])
	}
	// An explicit ticket field wins over any scanning.
	ent3 := SignalToEntity(model.Signal{
		Source: "local-git",
		Kind:   "commit",
		Title:  "Fix the SALSA-42 regression",
		Fields: map[string]string{"ticket": "SALSA-99"},
	}, cfg)
	if ent3.Coordinates["ticket"] != "SALSA-99" {
		t.Errorf("explicit ticket field should win, got %q", ent3.Coordinates["ticket"])
	}
}

func TestGroupKey_branchAnchor(t *testing.T) {
	cfg := Config{}
	ent := model.Entity{
		ID:    "commit:1",
		Kind:  "commit",
		Title: "fix bug",
		Coordinates: map[string]string{
			"repo":   "org/repo",
			"branch": "salsa-123-fix",
			"ticket": "SALSA-123",
		},
	}
	if got := GroupKey(ent, cfg); got != "wb:org/repo@salsa-123-fix" {
		t.Errorf("GroupKey = %q, want wb:org/repo@salsa-123-fix", got)
	}
}

func TestBuildWorkItems_branchAnchoredWithTicketAttachment(t *testing.T) {
	cfg := Config{}
	entities := []model.Entity{
		{ID: "jira:SALSA-1", Kind: "issue", Title: "SALSA-1 Fix widget NPE", URL: "https://jira/SALSA-1", Coordinates: map[string]string{"ticket": "SALSA-1", "key": "SALSA-1"}, State: map[string]string{"status": "In Progress"}},
		{ID: "commit:1", Kind: "commit", Title: "fix", Coordinates: map[string]string{"repo": "org/repo", "branch": "salsa-1-fix", "ticket": "SALSA-1"}, State: map[string]string{"observedAt": "2026-06-01T12:00:00Z"}},
		{ID: "pr:1", Kind: "pr_review_status", Title: "salsa-1 fix", Coordinates: map[string]string{"repo": "org/repo", "head_branch": "salsa-1-fix", "ticket": "SALSA-1"}},
	}
	items := BuildWorkItems(entities, cfg)
	if len(items) != 1 {
		t.Fatalf("got %d work items, want 1 (branch unit with attached ticket)", len(items))
	}
	wi := items[0]
	if wi.Key != "wb:org/repo@salsa-1-fix" {
		t.Errorf("key = %q", wi.Key)
	}
	if wi.Branch != "salsa-1-fix" || wi.Repo != "org/repo" {
		t.Errorf("repo/branch = %q/%q", wi.Repo, wi.Branch)
	}
	if len(wi.Entities) != 3 {
		t.Errorf("entities = %d, want 3", len(wi.Entities))
	}
	if len(wi.Tickets) != 1 || wi.Tickets[0].Key != "SALSA-1" {
		t.Errorf("tickets = %+v", wi.Tickets)
	}
}

func TestBuildWorkItems_reviewRequestedPRBranchUnit(t *testing.T) {
	cfg := Config{}
	entities := []model.Entity{
		{ID: "pr:rr", Kind: "pr_review_status", Title: "their feature", Coordinates: map[string]string{
			"repo": "org/repo", "head_branch": "feature-x", "relation": "review_requested",
		}},
	}
	items := BuildWorkItems(entities, cfg)
	if len(items) != 1 {
		t.Fatalf("got %d items", len(items))
	}
	if items[0].Key != "wb:org/repo@feature-x" {
		t.Errorf("key = %q", items[0].Key)
	}
}

func TestBuildWorkItems_orphanTicketStaysStandalone(t *testing.T) {
	cfg := Config{}
	entities := []model.Entity{
		{ID: "jira:SALSA-9", Kind: "issue", Title: "SALSA-9 Unstarted task", Coordinates: map[string]string{"ticket": "SALSA-9"}, State: map[string]string{"status": "Assigned"}},
	}
	items := BuildWorkItems(entities, cfg)
	if len(items) != 1 || items[0].Key != "SALSA-9" {
		t.Fatalf("orphan ticket should stay standalone: %+v", items)
	}
}

func TestBuildWorkItems_multipleBranchesShareTicket(t *testing.T) {
	cfg := Config{}
	entities := []model.Entity{
		{ID: "jira:SALSA-1", Kind: "issue", Title: "SALSA-1 Shared", Coordinates: map[string]string{"ticket": "SALSA-1"}},
		{ID: "c1", Kind: "commit", Title: "a", Coordinates: map[string]string{"repo": "org/r", "branch": "salsa-1-a", "ticket": "SALSA-1"}},
		{ID: "c2", Kind: "commit", Title: "b", Coordinates: map[string]string{"repo": "org/r", "branch": "salsa-1-b", "ticket": "SALSA-1"}},
	}
	items := BuildWorkItems(entities, cfg)
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2 branch units", len(items))
	}
	for _, wi := range items {
		if len(wi.Tickets) != 1 || wi.Tickets[0].Key != "SALSA-1" {
			t.Errorf("%s tickets = %+v", wi.Key, wi.Tickets)
		}
	}
}

// TestBuildWorkItems_backportResolvesTicketViaPRNumber reproduces the real
// SALSA backport scenario: a backport branch (and its own delta commits)
// never names SALSA-12455 anywhere in its own text — only the PR number
// (#7373) it backports. That PR's squash-merge commit ("[SALSA-12455] ...
// (#7373)"), already collected as an unrelated entity (it lands on a
// different branch in the shared repo), supplies the PR#->ticket mapping.
func TestBuildWorkItems_backportResolvesTicketViaPRNumber(t *testing.T) {
	cfg := Config{Projects: []string{"SALSA"}}
	entities := []model.Entity{
		{ID: "b1", Kind: "branch", Title: "backport/pr-7373-to-release-2026.4.2", Coordinates: map[string]string{
			"repo": "Chip/salsa", "branch": "backport/pr-7373-to-release-2026.4.2", "path": "/code/salsa",
		}},
		{ID: "c1", Kind: "commit", Title: "[PR-7373] fix conflict", Coordinates: map[string]string{
			"repo": "Chip/salsa", "branch": "backport/pr-7373-to-release-2026.4.2",
		}},
		{ID: "c2", Kind: "commit", Title: "[CONFLICTS] Backport PR #7373", Coordinates: map[string]string{
			"repo": "Chip/salsa", "branch": "backport/pr-7373-to-release-2026.4.2",
		}},
		// The already-collected squash-merge commit, misattributed by
		// `git log --all --source` to an unrelated branch ("purge").
		{ID: "squash", Kind: "commit", Title: "[SALSA-12455] enable sound repeat loops when entering a playback window (#7373)", Coordinates: map[string]string{
			"repo": "Chip/salsa", "branch": "purge", "ticket": "SALSA-12455",
		}},
	}
	items := BuildWorkItems(entities, cfg)

	var backport *model.WorkItem
	for i := range items {
		if items[i].Key == "wb:Chip/salsa@backport/pr-7373-to-release-2026.4.2" {
			backport = &items[i]
		}
	}
	if backport == nil {
		t.Fatalf("backport work item not found: %+v", items)
	}
	if len(backport.Tickets) != 1 || backport.Tickets[0].Key != "SALSA-12455" {
		t.Errorf("backport tickets = %+v, want exactly [SALSA-12455]", backport.Tickets)
	}
}

// TestBuildWorkItems_leakedCommitsDontPolluteTickets reproduces the
// SALSA-12684 scenario: `git log --all --source` over a shared grove
// worktree can misattribute unrelated commits (each naming a different,
// genuinely real ticket) to this branch. Because the branch name itself
// names its own ticket, that alone must win — the leaked commits' distinct,
// disagreeing tickets must not be added.
func TestBuildWorkItems_leakedCommitsDontPolluteTickets(t *testing.T) {
	cfg := Config{Projects: []string{"SALSA"}}
	mk := func(id, title, ticket string) model.Entity {
		coords := map[string]string{"repo": "Chip/salsa", "branch": "SALSA-12684"}
		if ticket != "" {
			coords["ticket"] = ticket
		}
		return model.Entity{ID: id, Kind: "commit", Title: title, Coordinates: coords}
	}
	entities := []model.Entity{
		mk("own", "[SALSA-12684] Increase timeout for docker-compose container teardown", "SALSA-12684"),
		mk("leak1", "[SALSA-12683] libs can declare a 'codegen' script to generate types (#7358)", "SALSA-12683"),
		mk("leak2", "Fixing some build errors on AS3 import (#7289)", ""),
		mk("leak3", "[SALSA-12568] Fix variable migration (#7360)", "SALSA-12568"),
		mk("leak4", "[SALSA-12680] Restore `key` column in key/value tables (#7357)", "SALSA-12680"),
	}
	items := BuildWorkItems(entities, cfg)
	if len(items) != 1 {
		t.Fatalf("got %d work items, want 1: %+v", len(items), items)
	}
	wi := items[0]
	if wi.Key != "wb:Chip/salsa@SALSA-12684" {
		t.Fatalf("key = %q", wi.Key)
	}
	if len(wi.Tickets) != 1 || wi.Tickets[0].Key != "SALSA-12684" {
		t.Errorf("tickets = %+v, want exactly [SALSA-12684]", wi.Tickets)
	}
}

// TestBuildWorkItems_commitConsensusTicket guards the common case: a branch
// whose name carries no ticket, but whose own (single) commit does, should
// still resolve that ticket via consensus.
func TestBuildWorkItems_commitConsensusTicket(t *testing.T) {
	cfg := Config{Projects: []string{"SALSA"}}
	entities := []model.Entity{
		{ID: "c1", Kind: "commit", Title: "[SALSA-500] Fix misc bug", Coordinates: map[string]string{
			"repo": "org/repo", "branch": "misc-fix", "ticket": "SALSA-500",
		}},
	}
	items := BuildWorkItems(entities, cfg)
	if len(items) != 1 || len(items[0].Tickets) != 1 || items[0].Tickets[0].Key != "SALSA-500" {
		t.Fatalf("items = %+v, want single work item with ticket SALSA-500", items)
	}
}

// TestBuildWorkItems_multiTicketBranchOrdered covers a branch that
// legitimately carries more than one ticket: its own name resolves one
// (primary), and its PR resolves a second, distinct one.
func TestBuildWorkItems_multiTicketBranchOrdered(t *testing.T) {
	cfg := Config{Projects: []string{"SALSA"}}
	entities := []model.Entity{
		{ID: "c1", Kind: "commit", Title: "salsa-100 nice", Coordinates: map[string]string{
			"repo": "org/repo", "branch": "salsa-100-nice", "ticket": "SALSA-100",
		}},
		{ID: "pr1", Kind: "pr_review_status", Title: "SALSA-200 also fixes this", Coordinates: map[string]string{
			"repo": "org/repo", "head_branch": "salsa-100-nice",
		}},
	}
	items := BuildWorkItems(entities, cfg)
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1: %+v", len(items), items)
	}
	tickets := items[0].Tickets
	if len(tickets) != 2 || tickets[0].Key != "SALSA-100" || tickets[1].Key != "SALSA-200" {
		t.Errorf("tickets = %+v, want ordered [SALSA-100, SALSA-200]", tickets)
	}
}

// TestTicketFromEntity_rejectsBogusExplicitCoordinate guards against a
// collector-set Coordinates["ticket"] that isn't really a ticket (e.g. a
// generic scan upstream tagging a branch named "backport/pr-7373-..." as
// "PR-7373"): once Projects restricts matching, that value must be
// discarded rather than trusted verbatim.
func TestTicketFromEntity_rejectsBogusExplicitCoordinate(t *testing.T) {
	cfg := Config{Projects: []string{"SALSA"}}
	ent := model.Entity{
		Kind:        "branch",
		Title:       "backport/pr-7373-to-release-2026.4.2",
		Coordinates: map[string]string{"ticket": "PR-7373"},
	}
	if got := ticketFromEntity(ent, cfg); got != "" {
		t.Errorf("ticketFromEntity = %q, want \"\" (bogus explicit ticket must be rejected)", got)
	}
}

func TestBuildWorkItems_branchWithNoTickets(t *testing.T) {
	cfg := Config{}
	entities := []model.Entity{
		{ID: "c1", Kind: "commit", Title: "misc", Coordinates: map[string]string{"repo": "org/r", "branch": "misc-cleanup"}},
	}
	items := BuildWorkItems(entities, cfg)
	if len(items) != 1 || items[0].Key != "wb:org/r@misc-cleanup" {
		t.Fatalf("unexpected: %+v", items)
	}
	if len(items[0].Tickets) != 0 {
		t.Errorf("expected 0 tickets, got %+v", items[0].Tickets)
	}
}

func TestSignalsToEntities_session(t *testing.T) {
	cfg := Config{}
	signals := []model.Signal{
		{
			Source: "wsm",
			Kind:   "session",
			Title:  "my-feature",
			Fields: map[string]string{"window_id": "w1", "machine": "mac"},
		},
	}
	ents := SignalsToEntities(signals, cfg)
	if len(ents) != 1 {
		t.Fatalf("got %d entities", len(ents))
	}
	if ents[0].WindowID != "w1" {
		t.Errorf("window_id = %q", ents[0].WindowID)
	}
	if ents[0].Kind != "session" {
		t.Errorf("kind = %q", ents[0].Kind)
	}
}
