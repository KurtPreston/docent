package model

import "testing"

// The same repository is spelled several ways across one machine -- SSH in a
// a bare project clone, HTTPS in a docent config, sometimes with a port -- and
// every spelling has to reduce to the one key the collectors join on. A miss
// here shows up as a repository appearing twice in the cockpit.
func TestRepoKeyFromRemote(t *testing.T) {
	for _, tc := range []struct {
		raw, want string
	}{
		{"git@git.drwholdings.com:Chip/salsa.git", "Chip/salsa"},
		{"git@github.com:owner/repo", "owner/repo"},
		{"https://git.drwholdings.com/Chip/salsa.git", "Chip/salsa"},
		{"https://github.com/owner/repo", "owner/repo"},
		{"ssh://git@git.drwholdings.com:2222/Chip/salsa.git", "Chip/salsa"},
		{"https://user:token@github.com/owner/repo.git", "owner/repo"},
		{"  git@github.com:owner/repo.git  ", "owner/repo"},
		// Nested groups (GitLab-style) keep every segment: the full path is the
		// identity, and truncating it would merge distinct repos.
		{"git@gitlab.com:group/sub/repo.git", "group/sub/repo"},
		{"https://gitlab.com/group/sub/repo.git", "group/sub/repo"},
		// A single segment is not an owner/repo identity, so it is rejected
		// rather than returned as a name that would collide across forges.
		{"https://github.com/repo", ""},
		{"git@github.com:repo.git", ""},
		{"/srv/git/bare.git", ""},
		{"", ""},
		{"not a url at all", ""},
	} {
		if got := RepoKeyFromRemote(tc.raw); got != tc.want {
			t.Errorf("RepoKeyFromRemote(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

// Every spelling of one repo must agree, since that is the whole point.
func TestRepoKeySpellingsAgree(t *testing.T) {
	spellings := []string{
		"git@git.drwholdings.com:Chip/salsa.git",
		"https://git.drwholdings.com/Chip/salsa.git",
		"ssh://git@git.drwholdings.com/Chip/salsa.git",
	}
	want := RepoKeyFromRemote(spellings[0])
	if want == "" {
		t.Fatal("baseline spelling did not parse")
	}
	for _, s := range spellings[1:] {
		if got := RepoKeyFromRemote(s); got != want {
			t.Errorf("%q = %q, want %q", s, got, want)
		}
	}
}
