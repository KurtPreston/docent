package buildinfo

import (
	"runtime/debug"
	"testing"
)

// stamp sets the link-time variables for one test and restores them after.
func stamp(t *testing.T, version, commit, date string) {
	t.Helper()
	oldV, oldC, oldD, oldRead := Version, Commit, Date, readBuildInfo
	Version, Commit, Date = version, commit, date
	t.Cleanup(func() {
		Version, Commit, Date, readBuildInfo = oldV, oldC, oldD, oldRead
	})
}

func TestStringRelease(t *testing.T) {
	stamp(t, "0.1.0", "1314b11aabbccdd", "2026-11-03T18:00:00Z")
	if got, want := String(), "0.1.0 (1314b11, 2026-11-03T18:00:00Z)"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	if !IsRelease() {
		t.Fatal("IsRelease() = false for a stamped version")
	}
}

func TestStringSourceBuildUsesVCS(t *testing.T) {
	stamp(t, "", "", "")
	readBuildInfo = func() (*debug.BuildInfo, bool) {
		return &debug.BuildInfo{Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "abcdef0123456"},
			{Key: "vcs.time", Value: "2026-11-02T10:00:00Z"},
			{Key: "vcs.modified", Value: "true"},
		}}, true
	}
	if got, want := String(), "dev (abcdef0, 2026-11-02T10:00:00Z, modified)"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	if IsRelease() {
		t.Fatal("IsRelease() = true for a source build")
	}
}

func TestStringWithoutDetails(t *testing.T) {
	stamp(t, "", "", "")
	readBuildInfo = func() (*debug.BuildInfo, bool) { return nil, false }
	if got := String(); got != "dev" {
		t.Fatalf("String() = %q, want dev", got)
	}
}

func TestIsVersionArg(t *testing.T) {
	cases := map[string]bool{"-version": true, "--version": true, "-v": true, "version": false, "-host": false}
	for arg, want := range cases {
		if got := IsVersionArg([]string{arg}); got != want {
			t.Errorf("IsVersionArg(%q) = %v, want %v", arg, got, want)
		}
	}
	if IsVersionArg([]string{"-version", "-host", "x"}) {
		t.Error("IsVersionArg with extra args should be false")
	}
}
