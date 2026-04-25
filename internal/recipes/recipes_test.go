package recipes

import "testing"

func TestInstantiateRecipeMergesDefaultsAndLocalTarget(t *testing.T) {
	recipe := Recipe{
		ID:        "github-pull-requests",
		Name:      "GitHub PRs",
		Collector: "github",
		RequiredTarget: []TargetField{{
			Name:     "repo",
			Required: true,
		}},
		Defaults: RecipeDefaults{
			Schedule: "every-status-check",
			Config: map[string]string{
				"base_url": "https://github.com",
			},
		},
	}
	directive, err := recipe.Instantiate(InstantiateInput{
		ID:      "slakkr-prs",
		Name:    "Slakkr PRs",
		Enabled: true,
		Target: map[string]string{
			"repo": "owner/slakkr-ai",
		},
	})
	if err != nil {
		t.Fatalf("instantiate recipe: %v", err)
	}
	if directive.RecipeID != recipe.ID {
		t.Fatalf("recipe id = %q, want %q", directive.RecipeID, recipe.ID)
	}
	if directive.Config["base_url"] != "https://github.com" {
		t.Fatalf("base_url default was not copied")
	}
	if directive.Target["repo"] != "owner/slakkr-ai" {
		t.Fatalf("target repo was not copied")
	}
}

func TestInstantiateRecipeRequiresTargetFields(t *testing.T) {
	recipe := Recipe{
		ID:        "local-git",
		Name:      "Local Git",
		Collector: "local-git",
		RequiredTarget: []TargetField{{
			Name:     "path",
			Required: true,
		}},
	}
	if _, err := recipe.Instantiate(InstantiateInput{Enabled: true}); err == nil {
		t.Fatal("expected missing target to fail")
	}
}

func TestInstantiateLocalGitAllowsProjectRepoRef(t *testing.T) {
	recipe := Recipe{
		ID:        "local-git-repository-status",
		Name:      "Local Git",
		Collector: "local-git",
	}
	d, err := recipe.Instantiate(InstantiateInput{
		Enabled: true,
		Target: map[string]string{
			"project_id": "slakkr-ai",
			"repo_id":    "slakkr-ai",
		},
	})
	if err != nil {
		t.Fatalf("instantiate: %v", err)
	}
	if d.Target["project_id"] != "slakkr-ai" {
		t.Fatalf("target: %#v", d.Target)
	}
}
