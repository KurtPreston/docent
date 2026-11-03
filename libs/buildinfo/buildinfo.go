// Package buildinfo reports which docent build is running. Release builds set
// Version, Commit, and Date through ldflags (see .goreleaser.yaml); source
// builds fall back to the VCS details the Go toolchain embeds.
package buildinfo

import (
	"fmt"
	"runtime/debug"
	"strings"
)

// Set at link time, e.g.
// -X github.com/KurtPreston/docent/libs/buildinfo.Version=0.1.0
var (
	Version = ""
	Commit  = ""
	Date    = ""
)

// devVersion is reported by builds that weren't stamped with a release version.
const devVersion = "dev"

// readBuildInfo is a variable so tests can supply fixed VCS settings.
var readBuildInfo = debug.ReadBuildInfo

// IsRelease reports whether this binary was stamped with a release version.
func IsRelease() bool {
	return Version != "" && Version != devVersion
}

// CurrentVersion returns the release version, or "dev" for source builds.
func CurrentVersion() string {
	if IsRelease() {
		return Version
	}
	return devVersion
}

// String describes the build in one line, such as "0.1.0 (abc1234, 2026-11-03)"
// or, for a source build, "dev (abc1234, modified)".
func String() string {
	commit, date, dirty := Commit, Date, false
	if commit == "" {
		commit, date, dirty = vcsInfo()
	}

	// Collect whichever details are known
	var details []string
	if commit != "" {
		details = append(details, shortCommit(commit))
	}
	if date != "" {
		details = append(details, date)
	}
	if dirty {
		details = append(details, "modified")
	}
	if len(details) == 0 {
		return CurrentVersion()
	}
	return fmt.Sprintf("%s (%s)", CurrentVersion(), strings.Join(details, ", "))
}

// IsVersionArg reports whether args ask a binary for its version, as in
// `docent-tunnel -version` or `docent-setup --version`.
func IsVersionArg(args []string) bool {
	if len(args) != 1 {
		return false
	}
	switch args[0] {
	case "-version", "--version", "-v":
		return true
	}
	return false
}

// vcsInfo reads the commit, commit date, and dirty flag recorded by `go build`.
func vcsInfo() (commit, date string, dirty bool) {
	info, ok := readBuildInfo()
	if !ok || info == nil {
		return "", "", false
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			commit = s.Value
		case "vcs.time":
			date = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	return commit, date, dirty
}

// shortCommit trims a full hash to the 7 characters git shows by default.
func shortCommit(c string) string {
	if len(c) > 7 {
		return c[:7]
	}
	return c
}
