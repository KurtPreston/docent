//go:build !windows

package selfupdate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runInstallerHelper exercises the sourced Bash helper without invoking any
// installer service commands or touching the host's configuration.
func runInstallerHelper(t *testing.T, script string) string {
	t.Helper()
	helper, err := filepath.Abs("../../scripts/lib/release.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", `
set -euo pipefail
log() { printf '%s\n' "$*"; }
run() { "$@"; }
DRY_RUN=0
. "$HELPER"
`+script)
	cmd.Env = append(os.Environ(),
		"HELPER="+helper, "DOCENT_REPO="+testRepo,
		"DOCENT_DOWNLOAD_BASE="+defaultDownloadBase, "BINDIR="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper failed: %v\n%s", err, out)
	}
	return string(out)
}

func TestInstallerHelperInstallsVerifiedRelease(t *testing.T) {
	newReleaseServer(t, fullRelease("v0.2.0"))
	runInstallerHelper(t, `
docent_release_fetch "" || exit 1
[ "$DOCENT_RELEASE_TAG" = v0.2.0 ]
docent_release_install "$BINDIR" docentd docent-tunnel || exit 1
[ "$(cat "$BINDIR/docentd")" = new-docentd ]
[ -x "$BINDIR/docent-tunnel" ]
`)
}

func TestInstallerHelperRejectsIncompleteReleasesAndRetries(t *testing.T) {
	for _, mode := range []string{"missing manifest", "missing checksum", "wrong checksum", "missing binary"} {
		t.Run(mode, func(t *testing.T) {
			rel := fullRelease("v0.2.0")
			switch mode {
			case "missing manifest":
				rel.noChecksums = true
			case "missing checksum":
				rel.checksums = func([]byte) string { return "" }
			case "wrong checksum":
				rel.checksums = func([]byte) string { return strings.Repeat("0", 64) + "  " + AssetName() }
			case "missing binary":
				delete(rel.files, "docent-setup")
			}
			newReleaseServer(t, rel)
			runInstallerHelper(t, `
for attempt in 1 2; do
  if docent_release_fetch v0.2.0; then
    echo "invalid release accepted on attempt $attempt" >&2
    exit 1
  fi
  [ -z "$DOCENT_RELEASE_TAG" ]
done
`)
		})
	}
}

func TestInstallerHelperRequiresHashTool(t *testing.T) {
	newReleaseServer(t, fullRelease("v0.2.0"))
	runInstallerHelper(t, `
_rel_sha256() { return 1; }
if docent_release_fetch v0.2.0; then exit 1; fi
[ -z "$DOCENT_RELEASE_TAG" ]
`)
}

func TestInstallerHelperPropagatesInstallFailures(t *testing.T) {
	for _, command := range []string{"cp", "chmod", "mv"} {
		t.Run(command, func(t *testing.T) {
			newReleaseServer(t, fullRelease("v0.2.0"))
			runInstallerHelper(t, `
docent_release_fetch v0.2.0 || exit 1
printf old > "$BINDIR/docentd"
`+command+`() { return 1; }
if docent_release_install "$BINDIR" docentd; then
  echo "installation failure was ignored" >&2
  exit 1
fi
[ "$(cat "$BINDIR/docentd")" = old ]
[ "$(find "$BINDIR" -name '.docentd.*' | wc -l | tr -d ' ')" = 0 ]
`)
		})
	}
}
