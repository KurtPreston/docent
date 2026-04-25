package userdata

import (
	"testing"
	"time"
)

func TestValidateTasksRequireKnownProjectAndValidEnums(t *testing.T) {
	projects := ProjectsFile{Projects: []Project{{ID: "slakkr-ai", Name: "Slakkr AI"}}}
	tasks := TasksFile{Tasks: []Task{{
		ID:        "daily-planner",
		ProjectID: "slakkr-ai",
		Name:      "Build daily planner",
		Status:    TaskStatusReady,
		Priority:  PriorityHigh,
		Delegation: Delegation{
			State: DelegationCandidate,
		},
	}}}
	if err := tasks.Validate(projects); err != nil {
		t.Fatalf("expected valid tasks: %v", err)
	}
	tasks.Tasks[0].ProjectID = "missing"
	if err := tasks.Validate(projects); err == nil {
		t.Fatal("expected unknown project to fail validation")
	}
}

func TestDirectiveValidationAllowsProjectScopedCollectors(t *testing.T) {
	projects := ProjectsFile{Projects: []Project{{ID: "work", Name: "Work"}}}
	directives := DirectivesFile{Directives: []Directive{{
		ID:        "work-prs",
		Name:      "Work PRs",
		Collector: "github-enterprise",
		Enabled:   true,
		ProjectID: "work",
		Target: map[string]string{
			"repo": "team/service",
		},
	}}}
	if err := directives.Validate(projects); err != nil {
		t.Fatalf("expected directive to validate: %v", err)
	}
}

func TestSignalsAndProposedTasksValidation(t *testing.T) {
	projects := ProjectsFile{Projects: []Project{{ID: "slakkr-ai", Name: "X"}}}
	tasks := TasksFile{Tasks: []Task{{
		ID:        "a-task",
		ProjectID: "slakkr-ai",
		Name:      "T",
		Status:    TaskStatusReady,
		Priority:  PriorityLow,
	}}}
	signals := SignalsFile{Signals: []Signal{{
		ID:     "sig-0123456789abcdef",
		Source: "jira",
		Kind:   "issue",
		Title:  "JIRA-1",
		URL:    "https://jira.example/browse/JIRA-1",
		Resolution: SignalResolutionTask,
		TaskID: "a-task",
		ObservedAt: YAMLDateTime{Time: time.Unix(1, 0).UTC()},
	}}}
	if err := signals.ValidateWithProjects(projects, tasks); err != nil {
		t.Fatalf("expected valid: %v", err)
	}
	if err := signals.ValidateWithProjects(projects, TasksFile{}); err == nil {
		t.Fatal("expected unknown task to fail")
	}
	c1 := 0.9
	pt := ProposedTasksFile{Proposed: []ProposedTask{{
		ID:              "pt-proposal-one",
		SourceSignalIDs: []string{"sig-0123456789abcdef"},
		ProjectID:       "slakkr-ai",
		Name:            "New",
		Confidence:      &c1,
		CreatedAt:       YAMLDateTime{Time: time.Unix(2, 0).UTC()},
	}}}
	if err := pt.Validate(projects, tasks); err != nil {
		t.Fatalf("expected valid proposed: %v", err)
	}
}
