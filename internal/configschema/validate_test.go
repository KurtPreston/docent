package configschema_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kurt/slakkr-ai/internal/configschema"
	"github.com/kurt/slakkr-ai/internal/userdata"
	"gopkg.in/yaml.v3"
)

func TestEmbeddedMatchesRepoJSONSchema(t *testing.T) {
	wantPath := filepath.Join("..", "..", "jsonschema", "config.schema.json")
	want, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read %s: %v", wantPath, err)
	}
	if string(want) != string(configschema.SchemaBytes) {
		t.Fatalf("internal/configschema/config.schema.json is out of sync with %s — copy schema files", wantPath)
	}
}

func TestWizardModelParsesCollectors(t *testing.T) {
	m, err := configschema.WizardModel()
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Collectors) != 7 {
		t.Fatalf("collectors: got %d", len(m.Collectors))
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
    target:
      repo: a/b
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

func TestValidateYAML_duplicateDirectiveIDsAcceptedBySchema(t *testing.T) {
	yamlDoc := `
ai:
  provider: rule-based
directives:
  - id: a
    name: One
    collector: github
    enabled: true
    target:
      repo: x/y
  - id: a
    name: Two
    collector: github
    enabled: false
    target:
      repo: z/w
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
			name: "github",
			yaml: `
ai:
  provider: rule-based
directives:
  - id: github
    name: GitHub
    collector: github
    enabled: true
    target:
      repo: org/hello-world`,
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
    target:
      repo: org/repo
    config:
      base_url: https://github.enterprise.example`,
		},
		{
			name: "github-activity",
			yaml: `
ai:
  provider: rule-based
directives:
  - id: github-activity
    name: Act
    collector: github-activity
    enabled: true
    target:
      username: tester`,
		},
		{
			name: "gitea",
			yaml: `
ai:
  provider: rule-based
directives:
  - id: gitea
    name: Gitea
    collector: gitea
    enabled: true
    target:
      owner: me
    config:
      base_url: https://gitea.example
    credential_refs:
      token: SLAKKR_GITEA_TOKEN`,
		},
		{
			name: "jira-token",
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
      email: me@example.com
      query: project = FOO
    credential_refs:
      token: SLAKKR_JIRA_TOKEN`,
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
