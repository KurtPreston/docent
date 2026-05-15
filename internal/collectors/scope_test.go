package collectors

import (
	"reflect"
	"testing"
)

func TestParseFollowedList(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{name: "empty", in: "", want: nil},
		{name: "whitespace", in: "   \t \n", want: nil},
		{name: "single", in: "org/repo", want: []string{"org/repo"}},
		{name: "comma", in: "org/a, org/b, org/c", want: []string{"org/a", "org/b", "org/c"}},
		{name: "spaces", in: "org/a org/b\torg/c", want: []string{"org/a", "org/b", "org/c"}},
		{name: "newlines", in: "org/a\norg/b\n", want: []string{"org/a", "org/b"}},
		{name: "semicolons", in: "PROJ;OTHER", want: []string{"PROJ", "OTHER"}},
		{name: "dedupe", in: "PROJ, PROJ, OTHER", want: []string{"PROJ", "OTHER"}},
		{name: "trailing-commas", in: "a,,b,", want: []string{"a", "b"}},
		{name: "bare-owner", in: "org, org/repo", want: []string{"org", "org/repo"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseFollowedList(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parseFollowedList(%q) = %#v; want %#v", tc.in, got, tc.want)
			}
		})
	}
}

func TestCollectOptsEffectiveScope(t *testing.T) {
	if (*CollectOpts)(nil).EffectiveScope() != ScopeInvolved {
		t.Fatal("nil CollectOpts should resolve to ScopeInvolved")
	}
	if (&CollectOpts{}).EffectiveScope() != ScopeInvolved {
		t.Fatal("ScopeUnset should resolve to ScopeInvolved")
	}
	if (&CollectOpts{Scope: ScopeSelf}).EffectiveScope() != ScopeSelf {
		t.Fatal("explicit ScopeSelf should stick")
	}
	if (&CollectOpts{Scope: ScopeAll}).EffectiveScope() != ScopeAll {
		t.Fatal("explicit ScopeAll should stick")
	}
}

func TestSplitFollowedRepo(t *testing.T) {
	cases := []struct {
		in        string
		wantOwner string
		wantRepo  string
	}{
		{"", "", ""},
		{"  ", "", ""},
		{"org", "org", ""},
		{"org/repo", "org", "repo"},
		{"  org/repo  ", "org", "repo"},
		{"org/nested/repo", "org", "nested/repo"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			owner, repo := splitFollowedRepo(tc.in)
			if owner != tc.wantOwner || repo != tc.wantRepo {
				t.Fatalf("splitFollowedRepo(%q) = (%q, %q); want (%q, %q)", tc.in, owner, repo, tc.wantOwner, tc.wantRepo)
			}
		})
	}
}
