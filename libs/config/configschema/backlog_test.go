package configschema_test

import (
	"strings"
	"testing"

	"github.com/KurtPreston/docent/libs/config/configschema"
)

// backlogQueueDoc is the work-queue setup from the docs: a backlog directive
// plus a transition rule using include_existing and max_concurrent.
const backlogQueueDoc = `
ai:
  provider: rule-based
directives:
  - id: tasks
    name: Task board
    collector: backlog
    enabled: true
    config:
      board: ~/tasks
      pull_command: git pull --ff-only
      statuses: To Do, In Progress
      queue_label: agent
      queue_status: To Do
    state:
      poll: { interval: 5m, on_load: true }
automations:
  - id: work-queue
    enabled: true
    trigger:
      type: transition
      source: backlog
      kind: task
      include_existing: true
      when: { field: queued, to: "true" }
    conditions:
      cooldown: 1d
      max_concurrent: 1
    actions:
      - type: shell
        command: echo queued
`

func TestValidateYAML_backlogQueueAccepted(t *testing.T) {
	if err := configschema.ValidateYAML([]byte(strings.TrimSpace(backlogQueueDoc))); err != nil {
		t.Fatalf("backlog queue config should validate: %v", configschema.ValidationProblems(err))
	}

}

func TestValidateYAML_backlogGitEvents(t *testing.T) {
	for _, value := range []string{`"true"`, `"false"`, `"yes"`, `true`} {
		doc := strings.Replace(backlogQueueDoc, "      board: ~/tasks", "      board: ~/tasks\n      git_events: "+value, 1)
		err := configschema.ValidateYAML([]byte(doc))
		wantValid := value == `"true"` || value == `"false"`
		if (err == nil) != wantValid {
			t.Errorf("git_events=%s: %v", value, err)
		}
	}
}

func TestValidateYAML_backlogQueueRejectsBadShape(t *testing.T) {
	cases := map[string][2]string{
		"missing board":            {"      board: ~/tasks\n", ""},
		"negative max":             {"max_concurrent: 1", "max_concurrent: -1"},
		"fractional max":           {"max_concurrent: 1", "max_concurrent: 1.5"},
		"non-bool include":         {"include_existing: true", "include_existing: yes please"},
		"misplaced max_concurrent": {"      cooldown: 1d\n", "      cooldown: 1d\n    max_concurrent: 1\n"},
	}
	for name, c := range cases {
		doc := strings.Replace(backlogQueueDoc, c[0], c[1], 1)
		if doc == backlogQueueDoc {
			t.Fatalf("%s: replacement %q did not apply", name, c[0])
		}
		if err := configschema.ValidateYAML([]byte(strings.TrimSpace(doc))); err == nil {
			t.Errorf("%s: expected schema rejection", name)
		}
	}
}

func TestWizardModelOffersBacklogBoard(t *testing.T) {
	m, err := configschema.WizardModel()
	if err != nil {
		t.Fatal(err)
	}

	// Find the backlog branch and its board prompt
	for _, br := range m.Collectors {
		if br.Collector != "backlog" {
			continue
		}
		for _, f := range br.Fields {
			if f.Section == configschema.SectionConfig && f.Key == "board" {
				if !f.Required || f.Validator != "dir-exists" {
					t.Fatalf("board field = %+v, want required dir-exists", f)
				}
				return
			}
		}
		t.Fatal("backlog branch has no config.board field")
	}
	t.Fatal("wizard has no backlog collector branch")
}
