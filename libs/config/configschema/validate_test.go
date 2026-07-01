package configschema_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kurt/slakkr-ai/libs/config/configschema"
	"github.com/kurt/slakkr-ai/libs/config/userdata"
	"gopkg.in/yaml.v3"
)

func TestEmbeddedMatchesRepoJSONSchema(t *testing.T) {
	wantPath := filepath.Join("..", "..", "..", "jsonschema", "config.schema.json")
	want, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read %s: %v", wantPath, err)
	}
	if string(want) != string(configschema.SchemaBytes) {
		t.Fatalf("libs/config/configschema/config.schema.json is out of sync with %s — copy schema files", wantPath)
	}
}

func TestWizardModelParsesCollectors(t *testing.T) {
	m, err := configschema.WizardModel()
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Collectors) != 9 {
		t.Fatalf("collectors: got %d", len(m.Collectors))
	}
	var foundFormatter bool
	for _, br := range m.AIProviders {
		for _, tf := range br.TopLevelFields {
			if tf.Key == "activity_formatter" {
				foundFormatter = true
				if len(tf.Enum) != 2 {
					t.Fatalf("activity_formatter enums: %+v", tf.Enum)
				}
				if !tf.SkipSetupPrompt {
					t.Fatal("expected activity_formatter x-slakkr-setup-skip-prompt")
				}
			}
		}
	}
	if !foundFormatter {
		t.Fatal("expected activity_formatter TopLevelFields on ai branches")
	}
	if !m.SkipDirectiveIDSetupPrompt || !m.SkipDirectiveNameSetupPrompt {
		t.Fatalf("directive identity skip flags: id=%v name=%v", m.SkipDirectiveIDSetupPrompt, m.SkipDirectiveNameSetupPrompt)
	}
	var ghUserSkips int
	for _, br := range m.Collectors {
		for _, f := range br.Fields {
			if br.Collector == "github" && f.Section == configschema.SectionTarget && f.Key == "username" {
				if !f.SkipSetupPrompt {
					t.Fatal("expected github target.username x-slakkr-setup-skip-prompt")
				}
				ghUserSkips++
			}
		}
	}
	if ghUserSkips != 1 {
		t.Fatalf("github username skip: got %d", ghUserSkips)
	}
}

func TestValidateYAML_badActivityFormatterFails(t *testing.T) {
	yamlDoc := `
ai:
  provider: rule-based
  activity_formatter: bogus
directives:
  - id: gh
    name: GH
    collector: github
    enabled: true
`
	err := configschema.ValidateYAML([]byte(strings.TrimSpace(yamlDoc)))
	if err == nil {
		t.Fatal("expected schema validation error")
	}
}

func TestValidateYAML_activityFormatterValuesOK(t *testing.T) {
	for _, val := range []string{"repo-chronological", "json-signal-list"} {
		yamlDoc := `
ai:
  provider: rule-based
  activity_formatter: VALUE
directives:
  - id: gh
    name: GH
    collector: github
    enabled: true
`
		doc := strings.ReplaceAll(strings.TrimSpace(yamlDoc), "VALUE", val)
		if err := configschema.ValidateYAML([]byte(doc)); err != nil {
			t.Fatalf("%q: %v", val, configschema.ValidationProblems(err))
		}
	}
}

func TestValidateYAML_badDirectiveFails(t *testing.T) {
	yamlDoc := `
ai:
  provider: rule-based
directives:
  - id: gh-e
    name: GH E
    collector: github-enterprise
    enabled: true
    config: {}
`
	err := configschema.ValidateYAML([]byte(yamlDoc))
	if err == nil {
		t.Fatal("expected schema validation error")
	}
	probs := configschema.ValidationProblems(err)
	if len(probs) == 0 {
		t.Fatalf("expected problem strings: %v", err)
	}
}

func TestValidateYAML_executionModesAccepted(t *testing.T) {
	yamlDoc := strings.TrimSpace(`
ai:
  provider: rule-based
directives:
  - id: github
    name: GitHub
    collector: github
    enabled: true
execution_modes:
  - id: repo-activity
    name: Repo activity (everyone)
    lookback: { kind: days, days: 14 }
    prompt:
      instruction: "Summarize repo activity."
    scope: involved
  - id: weekly
    lookback: { kind: previous-weekday }
    scope: self
  - id: free-form
    formatter: json-signal-list
`)
	if err := configschema.ValidateYAML([]byte(yamlDoc)); err != nil {
		t.Fatalf("execution_modes example should validate: %v", configschema.ValidationProblems(err))
	}
}

func TestValidateYAML_executionModesRejectsBadShape(t *testing.T) {
	cases := []struct {
		name string
		yaml string
	}{
		{
			name: "days without value",
			yaml: `
ai:
  provider: rule-based
directives:
  - id: gh
    name: GH
    collector: github
    enabled: true
execution_modes:
  - id: bad
    lookback: { kind: days }
`,
		},
		{
			name: "previous-weekday with days",
			yaml: `
ai:
  provider: rule-based
directives:
  - id: gh
    name: GH
    collector: github
    enabled: true
execution_modes:
  - id: bad
    lookback: { kind: previous-weekday, days: 3 }
`,
		},
		{
			name: "unknown scope",
			yaml: `
ai:
  provider: rule-based
directives:
  - id: gh
    name: GH
    collector: github
    enabled: true
execution_modes:
  - id: bad
    scope: team
`,
		},
		{
			name: "bad id pattern",
			yaml: `
ai:
  provider: rule-based
directives:
  - id: gh
    name: GH
    collector: github
    enabled: true
execution_modes:
  - id: BadID
`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := configschema.ValidateYAML([]byte(strings.TrimSpace(tc.yaml))); err == nil {
				t.Fatal("expected schema rejection")
			}
		})
	}
}

func TestValidateYAML_duplicateDirectiveIDsAcceptedBySchema(t *testing.T) {
	yamlDoc := `
ai:
  provider: rule-based
directives:
  - id: a
    name: One
    collector: github
    enabled: true
  - id: a
    name: Two
    collector: github
    enabled: false
`
	if err := configschema.ValidateYAML([]byte(yamlDoc)); err != nil {
		t.Fatalf("schema layer should accept duplicate ids: %v", err)
	}
	var cfg userdata.ConfigFile
	if err := yaml.Unmarshal([]byte(yamlDoc), &cfg); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected duplicate id error from userdata.Validate")
	}
}

func TestDirectiveExamplesRoundTrip(t *testing.T) {
	root := t.TempDir()
	repoDir := filepath.Join(root, "demo-repo")
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		yaml string
	}{
		{
			name: "local-git-paths",
			yaml: strings.ReplaceAll(`
ai:
  provider: rule-based
directives:
  - id: local-git
    name: Local
    collector: local-git
    enabled: true
    paths:
      - REPO_PLACEHOLDER
`, "REPO_PLACEHOLDER", repoDir),
		},
		{
			name: "github-default-user",
			yaml: `
ai:
  provider: rule-based
directives:
  - id: github
    name: GitHub
    collector: github
    enabled: true`,
		},
		{
			name: "github-explicit-user",
			yaml: `
ai:
  provider: rule-based
directives:
  - id: github
    name: GitHub
    collector: github
    enabled: true
    target:
      username: someone-else
    credential_refs:
      token: SLAKKR_GITHUB_TOKEN`,
		},
		{
			name: "github-enterprise",
			yaml: `
ai:
  provider: rule-based
directives:
  - id: github-enterprise
    name: GHE
    collector: github-enterprise
    enabled: true
    config:
      base_url: https://github.enterprise.example`,
		},
		{
			name: "gitea-default-user",
			yaml: `
ai:
  provider: rule-based
directives:
  - id: gitea
    name: Gitea
    collector: gitea
    enabled: true
    config:
      base_url: https://gitea.example
    credential_refs:
      token: SLAKKR_GITEA_TOKEN`,
		},
		{
			name: "gitea-explicit-owner",
			yaml: `
ai:
  provider: rule-based
directives:
  - id: gitea
    name: Gitea
    collector: gitea
    enabled: true
    target:
      owner: some-org
    config:
      base_url: https://gitea.example
    credential_refs:
      token: SLAKKR_GITEA_TOKEN`,
		},
		{
			name: "jira-pat",
			yaml: `
ai:
  provider: rule-based
directives:
  - id: jira
    name: Jira
    collector: jira
    enabled: true
    config:
      base_url: https://your-domain.atlassian.net
      query: project = FOO
    credential_refs:
      pat: SLAKKR_JIRA_PAT`,
		},
		{
			name: "google-calendar-env",
			yaml: `
ai:
  provider: rule-based
directives:
  - id: google-cal
    name: Cal
    collector: google-calendar
    enabled: true
    credential_refs:
      ical_url: SLAKKR_GOOGLE_CALENDAR_ICAL_URL`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(strings.TrimSpace(tc.yaml))
			if err := configschema.ValidateYAML(raw); err != nil {
				t.Fatal(configschema.ValidationProblems(err))
			}
			var cfg userdata.ConfigFile
			if err := yaml.Unmarshal(raw, &cfg); err != nil {
				t.Fatal(err)
			}
			if err := cfg.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
