package userdata

import "testing"

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
