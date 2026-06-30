package collectors

import "testing"

func TestLocalGitSelfMatcher(t *testing.T) {
	matcher := localGitSelfMatcher{
		repoEmail:   "kurt@repo.example",
		globalEmail: "kurt@global.example",
		user:        "kpreston",
	}
	tests := []struct {
		name   string
		author string
		email  string
		want   bool
		why    string
	}{
		{name: "repo email exact", author: "Some Body", email: "kurt@repo.example", want: true, why: "matches per-repo user.email"},
		{name: "repo email case-insensitive", author: "Some Body", email: "Kurt@Repo.Example", want: true, why: "case-insensitive email match"},
		{name: "global email", author: "Some Body", email: "kurt@global.example", want: true, why: "matches global user.email"},
		{name: "user substring", author: "Kurt Preston (kpreston)", email: "noreply@example.com", want: true, why: "USER appears in author name"},
		{name: "user substring case-insensitive", author: "KPRESTON Bot", email: "noreply@example.com", want: true, why: "case-insensitive USER substring"},
		{name: "no match", author: "Other Person", email: "other@example.com", want: false, why: "no email or user-name match"},
		{name: "user not substring of name parts", author: "Kurt Preston", email: "other@example.com", want: false, why: "USER (kpreston) is not a substring of 'kurt preston'"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matcher.Match(tt.author, tt.email)
			if got != tt.want {
				t.Errorf("Match(%q, %q) = %v; want %v (%s)", tt.author, tt.email, got, tt.want, tt.why)
			}
		})
	}
}

func TestLocalGitSelfMatcherEmpty(t *testing.T) {
	var matcher localGitSelfMatcher
	if matcher.Match("Anyone", "anyone@example.com") {
		t.Fatalf("empty matcher should never match")
	}
}

func TestParseGitRemoteToRepositoryKey(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"", ""},
		{"https://github.com/kurt/slakkr-ai.git", "kurt/slakkr-ai"},
		{"https://github.com/kurt/slakkr-ai", "kurt/slakkr-ai"},
		{"http://gitea.example/kurt/slakkr-ai.git", "kurt/slakkr-ai"},
		{"git@github.com:kurt/slakkr-ai.git", "kurt/slakkr-ai"},
		{"git@github.com:kurt/slakkr-ai", "kurt/slakkr-ai"},
		{"ssh://git@github.com/kurt/slakkr-ai.git", "kurt/slakkr-ai"},
		{"https://github.com/org/sub/repo.git", "org/sub/repo"},
		{"git@host:onlyone", ""},
		{"not-a-url", ""},
	}
	for _, tt := range tests {
		got := parseGitRemoteToRepositoryKey(tt.raw)
		if got != tt.want {
			t.Errorf("parseGitRemoteToRepositoryKey(%q) = %q; want %q", tt.raw, got, tt.want)
		}
	}
}
