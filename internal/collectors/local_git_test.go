package collectors

import "testing"

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
