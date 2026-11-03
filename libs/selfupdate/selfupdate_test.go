package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testRepo = "owner/docent"

// release is a fake GitHub release served by newReleaseServer.
type release struct {
	tag       string
	files     map[string]string // archive entry name -> content
	checksums func(archive []byte) string
}

// buildArchive packs files into a tar.gz like GoReleaser's archives.
func buildArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// newReleaseServer serves /releases/latest and one release's downloads, and
// points the package at it for the test's duration.
func newReleaseServer(t *testing.T, rel *release) {
	t.Helper()
	mux := http.NewServeMux()

	// /releases/latest redirects to the tag, or to /releases with none
	mux.HandleFunc("/"+testRepo+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		if rel == nil {
			http.Redirect(w, r, "/"+testRepo+"/releases", http.StatusFound)
			return
		}
		http.Redirect(w, r, "/"+testRepo+"/releases/tag/"+rel.tag, http.StatusFound)
	})

	// The archive and its checksums
	if rel != nil {
		archive := buildArchive(t, rel.files)
		base := "/" + testRepo + "/releases/download/" + rel.tag + "/"
		mux.HandleFunc(base+AssetName(), func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(archive) })
		mux.HandleFunc(base+"checksums.txt", func(w http.ResponseWriter, r *http.Request) {
			sums := fmt.Sprintf("%s  %s\n", sha256hex(archive), AssetName())
			if rel.checksums != nil {
				sums = rel.checksums(archive)
			}
			_, _ = w.Write([]byte(sums))
		})
	}

	srv := httptest.NewServer(mux)
	old := defaultDownloadBase
	defaultDownloadBase = srv.URL
	t.Cleanup(func() {
		defaultDownloadBase = old
		srv.Close()
	})
}

// installDir creates a bin dir holding the named binaries, each containing "old".
func installDir(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("old"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func newTestUpdater(t *testing.T, dir string) *Updater {
	t.Helper()
	u, err := New(Options{Repo: testRepo, Binary: filepath.Join(dir, "docentd")})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func fullRelease(tag string) *release {
	return &release{tag: tag, files: map[string]string{
		"docentd": "new-docentd", "docent-tunnel": "new-tunnel",
		"docent-reporter": "new-reporter", "docent-setup": "new-setup", "README.md": "readme",
	}}
}

func TestLatestTagFollowsTheRedirect(t *testing.T) {
	newReleaseServer(t, fullRelease("v0.2.0"))
	tag, err := newTestUpdater(t, t.TempDir()).LatestTag()
	if err != nil || tag != "v0.2.0" {
		t.Fatalf("LatestTag() = %q, %v; want v0.2.0", tag, err)
	}
}

func TestLatestTagWithoutReleases(t *testing.T) {
	newReleaseServer(t, nil)
	_, err := newTestUpdater(t, t.TempDir()).LatestTag()
	if err == nil || !strings.Contains(err.Error(), "no published releases") {
		t.Fatalf("LatestTag() error = %v, want no published releases", err)
	}
}

func TestInstallReplacesOnlyInstalledBinaries(t *testing.T) {
	if !SupportedPlatform() {
		t.Skip("no release build for " + Platform())
	}
	newReleaseServer(t, fullRelease("v0.2.0"))
	dir := installDir(t, "docentd", "docent-tunnel")

	res, err := newTestUpdater(t, dir).Install("v0.2.0")
	if err != nil {
		t.Fatal(err)
	}

	// docentd and the tunnel are replaced; absent binaries stay absent
	if got := strings.Join(res.Updated, ","); got != "docentd,docent-tunnel" {
		t.Fatalf("Updated = %s, want docentd,docent-tunnel", got)
	}
	if got := readFile(t, filepath.Join(dir, "docentd")); got != "new-docentd" {
		t.Fatalf("docentd = %q", got)
	}
	if got := readFile(t, filepath.Join(dir, "docent-tunnel")); got != "new-tunnel" {
		t.Fatalf("docent-tunnel = %q", got)
	}
	for _, absent := range []string{"docent-setup", "docent-reporter", "README.md"} {
		if _, err := os.Stat(filepath.Join(dir, absent)); err == nil {
			t.Fatalf("%s was installed but wasn't there before", absent)
		}
	}
}

func TestInstallRejectsAChecksumMismatch(t *testing.T) {
	if !SupportedPlatform() {
		t.Skip("no release build for " + Platform())
	}
	rel := fullRelease("v0.2.0")
	rel.checksums = func([]byte) string { return strings.Repeat("0", 64) + "  " + AssetName() + "\n" }
	newReleaseServer(t, rel)
	dir := installDir(t, "docentd")

	_, err := newTestUpdater(t, dir).Install("v0.2.0")
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("Install error = %v, want checksum mismatch", err)
	}
	if got := readFile(t, filepath.Join(dir, "docentd")); got != "old" {
		t.Fatalf("docentd was replaced despite the mismatch: %q", got)
	}
}

func TestInstallNeedsDocentdInTheArchive(t *testing.T) {
	if !SupportedPlatform() {
		t.Skip("no release build for " + Platform())
	}
	newReleaseServer(t, &release{tag: "v0.2.0", files: map[string]string{"docent-tunnel": "x"}})
	_, err := newTestUpdater(t, installDir(t, "docentd")).Install("v0.2.0")
	if err == nil || !strings.Contains(err.Error(), "no docentd") {
		t.Fatalf("Install error = %v, want no docentd", err)
	}
}

func TestSameVersion(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.1.0", "v0.1.0", true},
		{"v0.1.0", "v0.1.0", true},
		{"0.1.0", "v0.2.0", false},
		{"", "v0.1.0", false},
	}
	for _, c := range cases {
		if got := SameVersion(c.a, c.b); got != c.want {
			t.Errorf("SameVersion(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestChecksumFor(t *testing.T) {
	sums := "abc  docent_linux_amd64.tar.gz\ndef *docent_darwin_arm64.tar.gz\n"
	if got := checksumFor(sums, "docent_darwin_arm64.tar.gz"); got != "def" {
		t.Fatalf("checksumFor = %q, want def", got)
	}
	if got := checksumFor(sums, "missing"); got != "" {
		t.Fatalf("checksumFor(missing) = %q", got)
	}
}
