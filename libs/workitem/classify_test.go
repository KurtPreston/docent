package workitem

import (
	"testing"

	"github.com/KurtPreston/docent/libs/model"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name       string
		facts      Facts
		wantStatus string
		wantRank   int
		wantAction bool
	}{
		{"done beats everything", Facts{Done: true, HasLiveSession: true}, StatusDone, RankDone, false},
		{"live session needs followup", Facts{HasLiveSession: true, SessionNeedsFollowup: true, BranchEvidence: true}, StatusActive, RankActive, true},
		{"live session no followup", Facts{HasLiveSession: true}, StatusActive, RankActive, false},
		{"open idle session pins to active", Facts{HasOpenSession: true, JiraStarted: true}, StatusActive, RankActive, false},
		{"open idle session with followup", Facts{HasOpenSession: true, SessionNeedsFollowup: true}, StatusActive, RankActive, true},
		{"approved beats started", Facts{AuthoredApproved: true, BranchEvidence: true}, StatusApproved, RankApproved, true},
		{"jira started no branch", Facts{JiraStarted: true}, StatusStarted, RankStarted, false},
		{"draft pr is started", Facts{AuthoredDraft: true}, StatusStarted, RankStarted, false},
		{"branch evidence is started with action", Facts{BranchEvidence: true}, StatusStarted, RankStarted, true},
		{"authored awaiting waits on others", Facts{AuthoredAwaiting: true}, StatusAwaiting, RankAwaiting, false},
		{"authored my turn", Facts{AuthoredAwaiting: true, AuthoredMyTurn: true}, StatusAwaiting, RankAwaiting, true},
		{"review requested needs action", Facts{ReviewRequested: true}, StatusAwaiting, RankAwaiting, true},
		{"assigned no action", Facts{JiraAssigned: true}, StatusAssigned, RankAssigned, false},
		{"nothing hidden", Facts{}, "", RankHidden, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, r, a := Classify(tc.facts)
			if s != tc.wantStatus || r != tc.wantRank || a != tc.wantAction {
				t.Errorf("Classify = (%q,%d,%v), want (%q,%d,%v)", s, r, a, tc.wantStatus, tc.wantRank, tc.wantAction)
			}
		})
	}
}

func TestClassifyPR(t *testing.T) {
	mk := func(state map[string]string) model.Entity {
		return model.Entity{Kind: "pr_review_status", State: state}
	}
	var f Facts
	ClassifyPR(&f, mk(map[string]string{"relation": "authored", "is_draft": "false", "review_decision": "APPROVED", "checks": "passing"}))
	if !f.AuthoredApproved {
		t.Error("approved+passing authored PR should set AuthoredApproved")
	}
	f = Facts{}
	ClassifyPR(&f, mk(map[string]string{"relation": "authored", "is_draft": "true"}))
	if !f.AuthoredDraft || f.AuthoredApproved {
		t.Errorf("draft PR facts wrong: %+v", f)
	}
	f = Facts{}
	ClassifyPR(&f, mk(map[string]string{"relation": "authored", "is_draft": "false", "review_decision": "CHANGES_REQUESTED", "checks": "passing"}))
	if !f.AuthoredAwaiting || !f.AuthoredMyTurn {
		t.Errorf("changes-requested should be awaiting+my-turn: %+v", f)
	}
	f = Facts{}
	ClassifyPR(&f, mk(map[string]string{"relation": "review_requested"}))
	if !f.ReviewRequested || f.AuthoredAwaiting {
		t.Errorf("review_requested facts wrong: %+v", f)
	}
}
