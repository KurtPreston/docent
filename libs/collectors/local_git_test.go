package collectors

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/KurtPreston/docent/libs/config/userdata"
	"github.com/KurtPreston/docent/libs/correlation"
)

func TestParseReflogTime(t *testing.T) {
	got, ok := parseReflogTime("HEAD@{2026-07-06 10:51:08 -0500}")
	if !ok {
		t.Fatalf("expected a parseable reflog time")
	}
	want := time.Date(2026, 7, 6, 10, 51, 8, 0, time.FixedZone("", -5*3600))
	if !got.Equal(want) {
		t.Errorf("parseReflogTime = %v, want %v", got, want)
	}

	for _, gd := range []string{"HEAD@{2}", "salsa-123@{0}", "", "HEAD@{not a date}"} {
		if _, ok := parseReflogTime(gd); ok {
			t.Errorf("parseReflogTime(%q) unexpectedly ok", gd)
		}
	}
}

func TestLocalGitTicket(t *testing.T) {
	generic := correlation.Config{AllowGeneric: true}
	restricted := correlation.Config{Projects: []string{"SALSA"}}
	tests := []struct {
		name       string
		text       string
		repoTicket string
		cfg        correlation.Config
		want       string
	}{
		{name: "subject has ticket", text: "Fix SALSA-7 leak", repoTicket: "SALSA-1", cfg: generic, want: "SALSA-7"},
		{name: "falls back to repo ticket", text: "misc cleanup", repoTicket: "SALSA-1", cfg: generic, want: "SALSA-1"},
		{name: "neither", text: "misc cleanup", repoTicket: "", cfg: generic, want: ""},
		{name: "reflog subject", text: "checkout: moving from main to salsa-42-x", repoTicket: "", cfg: generic, want: "SALSA-42"},
		{name: "no generic without allow", text: "fontawesome-free-7.3.0", repoTicket: "", cfg: correlation.Config{}, want: ""},
		{name: "project restricted still matches", text: "Fix SALSA-7 leak", repoTicket: "", cfg: restricted, want: "SALSA-7"},
		{name: "project restricted ignores free-7", text: "fontawesome-free-7.3.0", repoTicket: "", cfg: restricted, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := localGitTicket(tt.text, tt.repoTicket, tt.cfg); got != tt.want {
				t.Errorf("localGitTicket(%q, %q) = %q, want %q", tt.text, tt.repoTicket, got, tt.want)
			}
		})
	}
}

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

func TestNormalizeGitRef(t *testing.T) {
	tests := []struct {
		ref  string
		want string
	}{
		{"refs/heads/salsa-123-fix", "salsa-123-fix"},
		{"refs/heads/main", "main"},
		{"refs/remotes/origin/main", ""},
		{"refs/tags/v1.0", ""},
		{"HEAD", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := normalizeGitRef(tt.ref); got != tt.want {
			t.Errorf("normalizeGitRef(%q) = %q, want %q", tt.ref, got, tt.want)
		}
	}
}

func TestLocalGitReflogBranch(t *testing.T) {
	tests := []struct {
		gd, gs string
		want   string
	}{
		{"salsa-123@{2}", "commit: foo", "salsa-123"},
		{"main@{0}", "commit: bar", "main"},
		{"HEAD@{1}", "checkout: moving from main to salsa-42-x", "salsa-42-x"},
		{"HEAD@{0}", "commit: initial", ""},
		{"", "", ""},
	}
	for _, tt := range tests {
		if got := localGitReflogBranch(tt.gd, tt.gs); got != tt.want {
			t.Errorf("localGitReflogBranch(%q, %q) = %q, want %q", tt.gd, tt.gs, got, tt.want)
		}
	}
}

func TestLocalGitReflogBranchAt(t *testing.T) {
	got := localGitReflogBranchAt(
		"HEAD@{2026-08-11 14:56:18 -0500}",
		"pull origin release/3.21.95: Merge made by the 'ort' strategy.",
		"release/3.23.100",
	)
	if got != "release/3.23.100" {
		t.Errorf("pull merge reflog = %q, want release/3.23.100", got)
	}
	if b := localGitReflogBranchAt("HEAD@{0}", "checkout: moving from main to other", "release/3.23.100"); b != "other" {
		t.Errorf("checkout should not use current branch fallback, got %q", b)
	}
}

func TestReflogIsWorkAction(t *testing.T) {
	if !ReflogIsWorkAction("pull origin release/3.21.95: Merge made by the 'ort' strategy.") {
		t.Error("expected pull merge to be work")
	}
	if ReflogIsWorkAction("checkout: moving from main to feature") {
		t.Error("checkout should not be work")
	}
}

func TestParseGitRemoteToRepositoryKey(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"", ""},
		{"https://github.com/kurt/docent.git", "kurt/docent"},
		{"https://github.com/kurt/docent", "kurt/docent"},
		{"http://gitea.example/kurt/docent.git", "kurt/docent"},
		{"git@github.com:kurt/docent.git", "kurt/docent"},
		{"git@github.com:kurt/docent", "kurt/docent"},
		{"ssh://git@github.com/kurt/docent.git", "kurt/docent"},
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

func TestLocalGitScanDepth(t *testing.T) {
	tests := []struct {
		raw  string
		want int
	}{
		{"", 1},
		{"  ", 1},
		{"0", 1},
		{"-3", 1},
		{"abc", 1},
		{"1", 1},
		{" 2 ", 2},
		{"3", 3},
		{"99", localGitMaxScanDepth},
	}
	for _, tt := range tests {
		d := userdata.Directive{Config: map[string]string{"scan_depth": tt.raw}}
		if got := localGitScanDepth(d); got != tt.want {
			t.Errorf("localGitScanDepth(scan_depth=%q) = %d, want %d", tt.raw, got, tt.want)
		}
	}
	if got := localGitScanDepth(userdata.Directive{}); got != 1 {
		t.Errorf("localGitScanDepth with no config = %d, want 1", got)
	}
}

// mkLocalGitScanTree builds a code_home fixture covering every case the depth
// walk has to get right:
//
//	plain/.git                an ordinary clone, found at any depth
//	plain/vendor/.git         a nested repo the walk must not descend to
//	project/.base/HEAD        a worktree project root: bare clone, no .git of its own
//	project/.hidden/.git      a dot-directory the walk must skip
//	project/wt-a/.git         the project's worktrees, one level further in
//	project/wt-b/.git
//	loose/notes.txt           no repo anywhere below
func mkLocalGitScanTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{
		"plain/.git",
		"plain/vendor/.git",
		"project/.base",
		"project/.hidden/.git",
		"project/wt-a/.git",
		"project/wt-b/.git",
		"loose",
	} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "project", ".base", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "loose", "notes.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// identityExpand keeps t.TempDir() paths verbatim so assertions can compare
// against the paths the fixture built (on macOS the real expander resolves
// /var to /private/var).
func identityExpand(s string) string { return s }

func relPaths(t *testing.T, root string, dirs []string) []string {
	t.Helper()
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		rel, err := filepath.Rel(root, d)
		if err != nil {
			t.Fatalf("Rel(%q, %q): %v", root, d, err)
		}
		out = append(out, filepath.ToSlash(rel))
	}
	sort.Strings(out)
	return out
}

func TestLocalGitRepoDirsCodeHomeDepth(t *testing.T) {
	root := mkLocalGitScanTree(t)
	tests := []struct {
		name  string
		depth string
		want  []string
	}{
		{
			name: "default depth stays at the immediate children",
			want: []string{"plain"},
		},
		{
			name:  "depth 2 reaches the worktree project's worktrees",
			depth: "2",
			want:  []string{"plain", "project/wt-a", "project/wt-b"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := userdata.Directive{ID: "lg", Collector: "local-git", CodeHome: root}
			if tt.depth != "" {
				d.Config = map[string]string{"scan_depth": tt.depth}
			}
			dirs, err := localGitRepoDirs(d, nil, identityExpand)
			if err != nil {
				t.Fatalf("localGitRepoDirs: %v", err)
			}
			got := relPaths(t, root, dirs)
			if !slices.Equal(got, tt.want) {
				t.Errorf("scanned %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLocalGitRepoDirsPathsDepth(t *testing.T) {
	root := mkLocalGitScanTree(t)
	project := filepath.Join(root, "project")

	// A worktree project root is not a working tree, so at the default depth the
	// entry contributes nothing and resolution falls through to code_home.
	d := userdata.Directive{ID: "lg", Collector: "local-git", Paths: []string{project}, CodeHome: root}
	dirs, err := localGitRepoDirs(d, nil, identityExpand)
	if err != nil {
		t.Fatalf("localGitRepoDirs: %v", err)
	}
	if got, want := relPaths(t, root, dirs), []string{"plain"}; !slices.Equal(got, want) {
		t.Errorf("default depth scanned %v, want %v (the project root itself is not a repo)", got, want)
	}

	d.Config = map[string]string{"scan_depth": "2"}
	dirs, err = localGitRepoDirs(d, nil, identityExpand)
	if err != nil {
		t.Fatalf("localGitRepoDirs: %v", err)
	}
	want := []string{"project/wt-a", "project/wt-b"}
	if got := relPaths(t, root, dirs); !slices.Equal(got, want) {
		t.Errorf("scanned %v, want %v", got, want)
	}
}

// A directory that is already a working tree ends the descent: its
// subdirectories are its source tree, so a vendored repo inside it is not a
// separate thing to collect.
func TestLocalGitRepoDirsDoesNotDescendIntoRepos(t *testing.T) {
	root := mkLocalGitScanTree(t)
	d := userdata.Directive{
		ID:        "lg",
		Collector: "local-git",
		Paths:     []string{filepath.Join(root, "plain")},
		Config:    map[string]string{"scan_depth": "3"},
	}
	dirs, err := localGitRepoDirs(d, nil, identityExpand)
	if err != nil {
		t.Fatalf("localGitRepoDirs: %v", err)
	}
	want := []string{"plain"}
	if got := relPaths(t, root, dirs); !slices.Equal(got, want) {
		t.Errorf("scanned %v, want %v (plain/vendor is inside a repo)", got, want)
	}
}

func TestLocalGitRepoDirsEmptyCodeHomeMentionsDepth(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	d := userdata.Directive{
		ID:        "lg",
		Collector: "local-git",
		CodeHome:  root,
		Config:    map[string]string{"scan_depth": "2"},
	}
	_, err := localGitRepoDirs(d, nil, identityExpand)
	if err == nil {
		t.Fatal("expected an error when nothing under code_home is a repo")
	}
	if !strings.Contains(err.Error(), "searched 2 levels deep") {
		t.Errorf("error %q should say how deep the scan looked", err)
	}
}
