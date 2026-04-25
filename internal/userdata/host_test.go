package userdata

import "testing"

func TestEffectiveCodeHomePrefersEnv(t *testing.T) {
	t.Setenv(EnvCodeHome, "/env/code")
	cfg := ConfigFile{Hosts: map[string]HostProfile{
		"box": {CodeHome: "/cfg/code"},
	}}
	if got := EffectiveCodeHome(cfg, "box"); got != "/env/code" {
		t.Fatalf("EffectiveCodeHome = %q", got)
	}
}

func TestEffectiveCodeHomeUsesConfigWhenEnvUnset(t *testing.T) {
	t.Setenv(EnvCodeHome, "")
	cfg := ConfigFile{Hosts: map[string]HostProfile{
		"box": {CodeHome: "/cfg/code"},
	}}
	if got := EffectiveCodeHome(cfg, "box"); got != "/cfg/code" {
		t.Fatalf("EffectiveCodeHome = %q", got)
	}
}

func TestRepoWorktreePathsHostScoped(t *testing.T) {
	r := Repo{
		PathsByHost: map[string][]string{
			"a": {"/one", "/two"},
		},
	}
	if got := RepoWorktreePaths(r, "a"); len(got) != 2 || got[0] != "/one" {
		t.Fatalf("paths for host a = %#v", got)
	}
	if got := RepoWorktreePaths(r, "missing"); len(got) != 0 {
		t.Fatalf("expected no paths for unknown host, got %#v", got)
	}
}

func TestCurrentHostIDUsesSlakkrHost(t *testing.T) {
	t.Setenv(EnvHostID, "my-laptop")
	t.Setenv("HOSTNAME", "ignored")
	id, err := CurrentHostID()
	if err != nil {
		t.Fatal(err)
	}
	if id != "my-laptop" {
		t.Fatalf("host id = %q", id)
	}
}

func TestSanitizeHostKeyAllowsHostnameLike(t *testing.T) {
	id, err := SanitizeHostKey("Kurt-MBP.local")
	if err != nil {
		t.Fatal(err)
	}
	if id != "kurt-mbp.local" {
		t.Fatalf("got %q", id)
	}
}

func TestExpandedRepoWorktrees(t *testing.T) {
	pf := ProjectsFile{Projects: []Project{{
		ID: "p",
		Repos: []Repo{{
			ID: "r1",
			PathsByHost: map[string][]string{
				"h": {"../x"},
			},
		}},
	}}}
	paths, err := ExpandedRepoWorktrees(pf, "p", "r1", "h", func(s string) string { return "/abs/" + s })
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != "/abs/../x" {
		t.Fatalf("paths %#v", paths)
	}
}
