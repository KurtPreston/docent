package ai

import (
	"strings"
	"testing"
	"time"

	"github.com/KurtPreston/docent/libs/collectors"
	"github.com/KurtPreston/docent/libs/model"
)

var (
	testSince  = time.Date(2026, 5, 4, 0, 0, 0, 0, time.UTC)
	testNow    = time.Date(2026, 5, 5, 0, 0, 0, 0, time.UTC)
	inWindow   = "2026-05-04T12:00:00Z"
	preWindow  = "2026-04-01T00:00:00Z"
	postWindow = "2026-05-06T00:00:00Z"
)

// TestRenderDailyPlanStandupVerbs exercises every verb the standup renderer
// can emit, plus the exclusions, in one document.
func TestRenderDailyPlanStandupVerbs(t *testing.T) {
	in := RunInput{
		ModeID:       "daily-plan",
		Since:        testSince,
		Now:          testNow,
		PrevDayLabel: "Monday",
		NextDayLabel: "Tuesday",
		WorkItems: []model.WorkItem{
			// Merged PR -> "Merged PR for", previous day only.
			ticketWorkItem("SALSA-10", "Merge me", "https://git.example/p/10", model.Entity{
				Kind: "authored_pr", URL: "https://git.example/p/10",
				Coordinates: map[string]string{"ticket": "SALSA-10"},
				State:       map[string]string{"state": "merged", "closed_at": inWindow},
			}),
			// Closed (not merged) PR -> "Closed PR for", previous day only.
			ticketWorkItem("SALSA-11", "Abandon me", "https://git.example/p/11", model.Entity{
				Kind: "authored_pr", URL: "https://git.example/p/11",
				Coordinates: map[string]string{"ticket": "SALSA-11"},
				State:       map[string]string{"state": "closed", "closed_at": inWindow},
			}),
			// Opened draft PR -> "Opened draft PR for".
			ticketWorkItem("SALSA-12", "Draft me", "https://git.example/p/12", model.Entity{
				Kind: "authored_pr", URL: "https://git.example/p/12",
				Coordinates: map[string]string{"ticket": "SALSA-12"},
				State:       map[string]string{"state": "open", "created_at": inWindow, "is_draft": "true"},
			}),
			// Existing PR I pushed to -> "Updated PR for".
			ticketWorkItem("SALSA-13", "Push me", "https://git.example/p/13",
				model.Entity{
					Kind: "authored_pr", URL: "https://git.example/p/13",
					Coordinates: map[string]string{"ticket": "SALSA-13"},
					State:       map[string]string{"state": "open", "created_at": preWindow, "is_draft": "false"},
				},
				model.Entity{Kind: "commit", Title: "wip",
					Coordinates: map[string]string{"ticket": "SALSA-13"},
					State:       map[string]string{"observedAt": inWindow}},
			),
			// Jira moved into the started tier + a branch checkout -> "Started".
			ticketWorkItem("SALSA-14", "Kick off", "https://jira.example/browse/SALSA-14",
				model.Entity{Kind: "issue_activity", URL: "https://jira.example/browse/SALSA-14",
					Coordinates: map[string]string{"ticket": "SALSA-14"},
					State:       map[string]string{"status": "In Development", "status_tier": "started", "updated": inWindow}},
				model.Entity{Kind: "reflog", Coordinates: map[string]string{"ticket": "SALSA-14"},
					State: map[string]string{"observedAt": inWindow}},
			),
			// Reviewed someone else's PR -> "Reviewed" section.
			ticketWorkItem("SALSA-15", "Review me", "https://git.example/p/15", model.Entity{
				Kind: "reviewed_pr", URL: "https://git.example/p/15", Title: "[SALSA-15] Review me",
				Coordinates: map[string]string{"ticket": "SALSA-15"},
				State:       map[string]string{"state": "open", "updated_at": inWindow},
			}),
			// EXCLUDED: open PR merely updated (no created_at, no commit).
			ticketWorkItem("SALSA-90", "Stale open PR", "https://git.example/p/90", model.Entity{
				Kind: "authored_pr", URL: "https://git.example/p/90",
				Coordinates: map[string]string{"ticket": "SALSA-90"},
				State:       map[string]string{"state": "open", "updated_at": inWindow},
			}),
			// EXCLUDED: reflog-only branch visit, no commit/PR.
			ticketWorkItem("SALSA-91", "Just a checkout", "https://jira.example/browse/SALSA-91",
				model.Entity{Kind: "reflog", Coordinates: map[string]string{"ticket": "SALSA-91"},
					State: map[string]string{"observedAt": inWindow}},
				model.Entity{Kind: "issue", URL: "https://jira.example/browse/SALSA-91",
					Coordinates: map[string]string{"ticket": "SALSA-91"},
					State:       map[string]string{"status": "In QA", "updated": inWindow}},
			),
			// EXCLUDED: started-tier ticket I only watch (no branch/commit/PR).
			ticketWorkItem("SALSA-92", "Watched in-dev", "https://jira.example/browse/SALSA-92",
				model.Entity{Kind: "issue_activity", URL: "https://jira.example/browse/SALSA-92",
					Coordinates: map[string]string{"ticket": "SALSA-92"},
					State:       map[string]string{"status": "In Development", "status_tier": "started", "updated": inWindow}},
			),
			// EXCLUDED: started-tier ticket with only a stale months-old draft
			// PR (created before the window) and no fresh activity.
			ticketWorkItem("SALSA-93", "Stale draft", "https://git.example/p/93",
				model.Entity{Kind: "issue_activity", URL: "https://jira.example/browse/SALSA-93",
					Coordinates: map[string]string{"ticket": "SALSA-93"},
					State:       map[string]string{"status": "In Development", "status_tier": "started", "updated": inWindow}},
				model.Entity{Kind: "pr_review_status", URL: "https://git.example/p/93",
					Coordinates: map[string]string{"ticket": "SALSA-93"},
					State:       map[string]string{"relation": "authored", "state": "open", "is_draft": "true", "created_at": preWindow}},
			),
		},
	}

	md := RenderDailyPlanMarkdown(in, nil)

	prev, _ := splitSection(t, md, "**Monday**", "**Tuesday**")
	next, _ := splitSection(t, md, "**Tuesday**", "Reviewed:")
	reviewed, _ := splitSection(t, md, "Reviewed:", "PRs ready for review:")

	// Previous-day verbs.
	wantPrev := []string{
		"- Merged PR for [SALSA-10](https://git.example/p/10) Merge me",
		"- Closed PR for [SALSA-11](https://git.example/p/11) Abandon me",
		"- Opened draft PR for [SALSA-12](https://git.example/p/12) Draft me",
		"- Updated PR for [SALSA-13](https://git.example/p/13) Push me",
		"- Started [SALSA-14](https://jira.example/browse/SALSA-14) Kick off",
	}
	for _, want := range wantPrev {
		if !strings.Contains(prev, want) {
			t.Errorf("previous-day section missing %q\n---\n%s", want, prev)
		}
	}

	// Merged/closed are done: previous day only, never "Continue on".
	for _, key := range []string{"SALSA-10", "SALSA-11"} {
		if strings.Contains(next, key) {
			t.Errorf("done item %s should not appear under Tuesday:\n%s", key, next)
		}
	}
	// In-flight items continue today.
	for _, key := range []string{"SALSA-12", "SALSA-13", "SALSA-14"} {
		if !strings.Contains(next, "Continue on ["+key+"]") {
			t.Errorf("expected Continue on %s:\n%s", key, next)
		}
	}

	// Reviewed section.
	if !strings.Contains(reviewed, "- [SALSA-15](https://git.example/p/15) Review me") {
		t.Errorf("reviewed section missing SALSA-15:\n%s", reviewed)
	}
	if strings.Contains(prev, "SALSA-15") || strings.Contains(next, "SALSA-15") {
		t.Errorf("reviewed PR should not appear in Monday/Tuesday sections")
	}

	// Exclusions.
	for _, key := range []string{"SALSA-90", "SALSA-91", "SALSA-92", "SALSA-93"} {
		if strings.Contains(md, key) {
			t.Errorf("noise item %s should be excluded entirely:\n%s", key, md)
		}
	}
}

// TestRenderDailyPlanStartedTierWithPR covers a ticket that moved into the
// started tier in-window and has one of my PRs (opened later/today, so no
// in-window PR action) but no commit yet -> "Started".
func TestRenderDailyPlanStartedTierWithPR(t *testing.T) {
	in := RunInput{
		Since: testSince, Now: testNow,
		PrevDayLabel: "Monday", NextDayLabel: "Tuesday",
		WorkItems: []model.WorkItem{
			ticketWorkItem("SALSA-30", "Kicked off", "https://jira.example/browse/SALSA-30",
				model.Entity{Kind: "issue_activity", URL: "https://jira.example/browse/SALSA-30",
					Coordinates: map[string]string{"ticket": "SALSA-30"},
					State:       map[string]string{"status": "In Development", "status_tier": "started", "updated": inWindow}},
				// PR authored but created after the window (e.g. today).
				model.Entity{Kind: "pr_review_status", URL: "https://git.example/p/30",
					Coordinates: map[string]string{"ticket": "SALSA-30"},
					State:       map[string]string{"relation": "authored", "state": "open", "is_draft": "true", "created_at": postWindow}},
			),
		},
	}
	md := RenderDailyPlanMarkdown(in, nil)
	// The ticket links to its PR (primaryTicketLink prefers the PR URL).
	if !strings.Contains(md, "- Started [SALSA-30](https://git.example/p/30) Kicked off") {
		t.Fatalf("expected Started for started-tier ticket with my PR:\n%s", md)
	}
}

func TestRenderDailyPlanExcludesBotCommit(t *testing.T) {
	in := RunInput{
		Since: testSince, Now: testNow,
		PrevDayLabel: "Monday", NextDayLabel: "Tuesday",
		WorkItems: []model.WorkItem{
			// A CI bot commit landed on a branch I have checked out; the
			// ticket is not in a started tier -> nothing to report.
			ticketWorkItem("SALSA-80", "Bot touched it", "https://jira.example/browse/SALSA-80",
				model.Entity{Kind: "commit", Title: "chore: bot",
					Coordinates: map[string]string{"ticket": "SALSA-80"},
					State:       map[string]string{"observedAt": inWindow, "is_self": "false"}},
				model.Entity{Kind: "reflog", Coordinates: map[string]string{"ticket": "SALSA-80"},
					State: map[string]string{"observedAt": inWindow}},
			),
			// My own commit on the same kind of item -> Started.
			ticketWorkItem("SALSA-81", "I did it", "https://jira.example/browse/SALSA-81",
				model.Entity{Kind: "commit", Title: "feat: mine",
					Coordinates: map[string]string{"ticket": "SALSA-81"},
					State:       map[string]string{"observedAt": inWindow, "is_self": "true"}},
			),
		},
	}
	md := RenderDailyPlanMarkdown(in, nil)
	if strings.Contains(md, "SALSA-80") {
		t.Errorf("bot-only commit should be excluded:\n%s", md)
	}
	if !strings.Contains(md, "Started [SALSA-81]") {
		t.Errorf("own commit should produce Started:\n%s", md)
	}
}

func TestRenderDailyPlanReviewedWithoutTicket(t *testing.T) {
	in := RunInput{
		Since: testSince, Now: testNow,
		PrevDayLabel: "Monday", NextDayLabel: "Tuesday",
		WorkItems: []model.WorkItem{{
			Key:   "pr:o/r#30",
			Title: "Fix flaky test",
			Entities: []model.Entity{{
				Kind: "reviewed_pr", URL: "https://git.example/p/30", Title: "Fix flaky test",
				State: map[string]string{"state": "open", "updated_at": inWindow},
			}},
		}},
	}
	md := RenderDailyPlanMarkdown(in, nil)
	if !strings.Contains(md, "Reviewed:\n- [Fix flaky test](https://git.example/p/30)") {
		t.Fatalf("expected reviewed PR linked by title:\n%s", md)
	}
}

// TestRenderDailyPlanOpenPRViaReviewStatus locks the real-world path: open
// authored PRs reach the pipeline as pr_review_status (not authored_pr), so the
// opened verbs must derive from created_at/is_draft on that entity.
func TestRenderDailyPlanOpenPRViaReviewStatus(t *testing.T) {
	in := RunInput{
		Since: testSince, Now: testNow,
		PrevDayLabel: "Monday", NextDayLabel: "Tuesday",
		WorkItems: []model.WorkItem{
			ticketWorkItem("SALSA-20", "Draft opened", "https://git.example/p/20", model.Entity{
				Kind: "pr_review_status", URL: "https://git.example/p/20",
				Coordinates: map[string]string{"ticket": "SALSA-20"},
				State:       map[string]string{"relation": "authored", "state": "open", "is_draft": "true", "created_at": inWindow},
			}),
			ticketWorkItem("SALSA-21", "Opened", "https://git.example/p/21", model.Entity{
				Kind: "pr_review_status", URL: "https://git.example/p/21",
				Coordinates: map[string]string{"ticket": "SALSA-21"},
				State:       map[string]string{"relation": "authored", "state": "open", "is_draft": "false", "created_at": inWindow},
			}),
		},
	}
	md := RenderDailyPlanMarkdown(in, nil)
	if !strings.Contains(md, "- Opened draft PR for [SALSA-20](https://git.example/p/20)") {
		t.Errorf("expected opened-draft verb from pr_review_status:\n%s", md)
	}
	if !strings.Contains(md, "- Opened PR for [SALSA-21](https://git.example/p/21)") {
		t.Errorf("expected opened verb from pr_review_status:\n%s", md)
	}
}

func TestRenderDailyPlanStandupEmpty(t *testing.T) {
	md := RenderDailyPlanMarkdown(RunInput{
		PrevDayLabel: "Friday",
		NextDayLabel: "Monday",
	}, nil)
	if !strings.Contains(md, "**Friday**\n- _none_") {
		t.Fatalf("expected empty prev placeholder:\n%s", md)
	}
	if !strings.Contains(md, "**Monday**\n- _none_") {
		t.Fatalf("expected empty next placeholder:\n%s", md)
	}
	if !strings.Contains(md, "PRs ready for review:\n- _none_") {
		t.Fatalf("expected empty ready placeholder:\n%s", md)
	}
	if strings.Contains(md, "Reviewed:") {
		t.Fatalf("Reviewed section should be omitted when empty:\n%s", md)
	}
}

func TestRenderDailyPlanSkipsUnticketed(t *testing.T) {
	md := RenderDailyPlanMarkdown(RunInput{
		PrevDayLabel: "Monday",
		NextDayLabel: "Tuesday",
		Since:        testSince,
		Now:          testNow,
		WorkItems: []model.WorkItem{{
			Key:   "commit:local-git:commit:abc123",
			Title: "wip without ticket",
			Entities: []model.Entity{
				{Kind: "commit", Title: "wip",
					Coordinates: map[string]string{"branch": "feature"},
					State:       map[string]string{"observedAt": inWindow}},
			},
		}},
	}, nil)
	if strings.Contains(md, "Started") || strings.Contains(md, "commit:") {
		t.Fatalf("unticketed commit should not produce a standup line:\n%s", md)
	}
	if !strings.Contains(md, "**Monday**\n- _none_") {
		t.Fatalf("expected empty prev section:\n%s", md)
	}
}

func TestRenderDailyPlanReadyForReviewSection(t *testing.T) {
	md := RenderDailyPlanMarkdown(RunInput{
		PrevDayLabel: "Monday", NextDayLabel: "Tuesday",
		Since: testSince, Now: testNow,
		Statuses: []collectors.StatusItem{
			{Kind: "pr_review_status", Title: "[SALSA-1] Ready", URL: "https://git.example/p/1",
				Fields: map[string]string{"ready": "true", "review_decision": "REVIEW_REQUIRED", "relation": "authored"}},
			{Kind: "pr_review_status", Title: "[SALSA-3] Approved", URL: "https://git.example/p/3",
				Fields: map[string]string{"ready": "true", "review_decision": "APPROVED", "relation": "authored"}},
		},
	}, nil)
	idx := strings.Index(md, "PRs ready for review:")
	if idx < 0 {
		t.Fatalf("missing ready section:\n%s", md)
	}
	section := md[idx:]
	if !strings.Contains(section, "https://git.example/p/1") {
		t.Fatalf("ready PR missing:\n%s", section)
	}
	if strings.Contains(section, "https://git.example/p/3") {
		t.Fatalf("approved PR should not be ready:\n%s", section)
	}
}

func TestPRWindowHelpers(t *testing.T) {
	merged := model.Entity{Kind: "authored_pr", State: map[string]string{"state": "merged", "closed_at": inWindow}}
	if !authoredPRMergedInWindow(merged, testSince, testNow) {
		t.Error("expected merged-in-window")
	}
	if authoredPRClosedNotMergedInWindow(merged, testSince, testNow) {
		t.Error("merged PR must not count as closed-not-merged")
	}

	closed := model.Entity{Kind: "authored_pr", State: map[string]string{"state": "closed", "closed_at": inWindow}}
	if !authoredPRClosedNotMergedInWindow(closed, testSince, testNow) {
		t.Error("expected closed-not-merged in window")
	}

	outOfWindow := model.Entity{Kind: "authored_pr", State: map[string]string{"state": "merged", "closed_at": postWindow}}
	if authoredPRMergedInWindow(outOfWindow, testSince, testNow) {
		t.Error("close time after window must be excluded")
	}

	openedDraft := model.Entity{State: map[string]string{"created_at": inWindow}}
	if !prCreatedInWindow(openedDraft, testSince, testNow) {
		t.Error("expected created-in-window")
	}
	openedBefore := model.Entity{State: map[string]string{"created_at": preWindow}}
	if prCreatedInWindow(openedBefore, testSince, testNow) {
		t.Error("created before window must be excluded")
	}
	noCreated := model.Entity{State: map[string]string{"state": "open"}}
	if prCreatedInWindow(noCreated, testSince, testNow) {
		t.Error("missing created_at must be treated as pre-existing")
	}
}

// ticketWorkItem builds a JIRA-ticket-anchored work item with the given
// entities, seeding a TicketRef so the bullet renders "[KEY](link) desc".
func ticketWorkItem(key, desc, url string, entities ...model.Entity) model.WorkItem {
	return model.WorkItem{
		Key:      key,
		Title:    key + " " + desc,
		Tickets:  []model.TicketRef{{Key: key, Title: key + " " + desc, URL: url}},
		Entities: entities,
	}
}

// splitSection returns the text between startMarker and endMarker (endMarker
// exclusive). endMarker "" means "to end of document".
func splitSection(t *testing.T, md, startMarker, endMarker string) (section, rest string) {
	t.Helper()
	start := strings.Index(md, startMarker)
	if start < 0 {
		return "", md
	}
	tail := md[start:]
	if endMarker == "" {
		return tail, ""
	}
	if e := strings.Index(tail, endMarker); e >= 0 {
		return tail[:e], tail[e:]
	}
	return tail, ""
}
