// Package selfupdate implements `docentd update`: it replaces the installed
// docent binaries with a release published on GitHub, then restarts docentd
// the way it was already running (see restart.go).
//
// It mirrors the installers' release download: resolve the tag, fetch the
// per-platform archive, verify it against checksums.txt, and unpack it. Each
// binary is swapped by writing the new file beside it and renaming it over the
// old one. On Unix that replaces the directory entry while a running process
// keeps its now-unlinked inode, so a live docentd isn't disturbed until it's
// restarted.
package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// DefaultRepo is the owner/repo docent updates from. DOCENT_REPO overrides it.
const DefaultRepo = "KurtPreston/docent"

// Binaries are the executables a release archive carries (see
// .goreleaser.yaml). docentd is always replaced; the others only when they're
// already installed beside it.
var Binaries = []string{"docentd", "docent-tunnel", "docent-reporter", "docent-setup"}

// defaultDownloadBase is where releases download from. DOCENT_DOWNLOAD_BASE
// overrides it for a mirror; tests point it at an httptest server.
var defaultDownloadBase = "https://github.com"

// Options configures an Updater. Empty fields take their defaults.
type Options struct {
	Repo   string    // owner/repo; defaults to $DOCENT_REPO or DefaultRepo
	Binary string    // the docentd to replace; defaults to the running executable
	Log    io.Writer // progress and warnings; defaults to io.Discard
}

// Updater downloads and installs docent releases.
type Updater struct {
	repo   string
	dlBase string
	binary string
	log    io.Writer
	http   *http.Client
}

// Result reports what an install changed.
type Result struct {
	Tag     string   // the release tag installed
	Binary  string   // the docentd that was replaced
	BinDir  string   // the directory whose binaries were replaced
	Updated []string // binary names replaced, docentd first
}

// New resolves the repository and the binary to replace.
func New(opts Options) (*Updater, error) {
	u := &Updater{
		repo:   opts.Repo,
		dlBase: defaultDownloadBase,
		binary: opts.Binary,
		log:    opts.Log,
		http:   &http.Client{Timeout: 5 * time.Minute},
	}
	if u.repo == "" {
		u.repo = envOr("DOCENT_REPO", DefaultRepo)
	}
	if base := os.Getenv("DOCENT_DOWNLOAD_BASE"); base != "" {
		u.dlBase = strings.TrimRight(base, "/")
	}
	if u.log == nil {
		u.log = io.Discard
	}

	// Default to the running binary, with symlinks resolved so the real file
	// is replaced rather than a link to it
	if u.binary == "" {
		exe, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("could not locate the running docentd: %w", err)
		}
		u.binary = exe
	}
	if resolved, err := filepath.EvalSymlinks(u.binary); err == nil {
		u.binary = resolved
	}
	return u, nil
}

// Binary is the path of the docentd this updater replaces.
func (u *Updater) Binary() string { return u.binary }

// LatestTag returns the tag of the repository's latest GitHub release, read
// from the redirect /releases/latest serves to /releases/tag/<tag>.
//
// This deliberately avoids api.github.com: unauthenticated API calls are
// capped at 60 per hour per source IP, so behind a shared NAT the budget can
// be spent by other traffic. The redirect has no such cap.
func (u *Updater) LatestTag() (string, error) {
	loc, err := u.redirect(u.dlBase + "/" + u.repo + "/releases/latest")
	if err != nil {
		return "", fmt.Errorf("could not resolve the latest release of %s (pass --version to pin one): %w", u.repo, err)
	}
	tag := tagFromReleaseURL(loc)
	if tag == "" {
		return "", fmt.Errorf("%s has no published releases yet", u.repo)
	}
	return tag, nil
}

// Install downloads tag's archive for this platform, verifies it, and replaces
// docentd plus any sibling docent binaries already installed.
func (u *Updater) Install(tag string) (Result, error) {
	res := Result{Tag: tag, Binary: u.binary, BinDir: filepath.Dir(u.binary)}
	if !SupportedPlatform() {
		return res, fmt.Errorf("no prebuilt docent for %s (releases ship linux and darwin on amd64 and arm64)", Platform())
	}

	// Download and verify the archive
	archive := AssetName()
	fmt.Fprintf(u.log, "Downloading docent %s (%s)...\n", tag, Platform())
	data, err := u.get(u.releaseURL(tag, archive))
	if err != nil {
		return res, fmt.Errorf("could not download %s for %s: %w", archive, tag, err)
	}
	if err := u.verify(tag, archive, data); err != nil {
		return res, err
	}
	bins, err := extractArchive(data)
	if err != nil {
		return res, err
	}

	// Validate every installed target before replacing any binary.
	targets := make(map[string]string)
	for _, name := range Binaries {
		target := filepath.Join(res.BinDir, name)
		if name == "docentd" {
			target = u.binary
		}
		info, err := os.Lstat(target)
		if err != nil {
			if os.IsNotExist(err) && name != "docentd" {
				continue
			}
			return res, fmt.Errorf("cannot inspect %s: %w", target, err)
		}
		if !info.Mode().IsRegular() {
			return res, fmt.Errorf("cannot replace %s: expected a regular file", target)
		}
		if len(bins[name]) == 0 {
			return res, fmt.Errorf("release archive %s has no %s binary", archive, name)
		}
		targets[name] = target
	}
	if err := installFiles(targets, bins, os.Rename); err != nil {
		return res, err
	}
	for _, name := range Binaries {
		if _, ok := targets[name]; ok {
			res.Updated = append(res.Updated, name)
		}
	}
	return res, nil
}

// installFiles stages all binaries before changing any target, retaining the
// old files until every rename succeeds so an error can be rolled back.
func installFiles(targets map[string]string, bins map[string][]byte, rename func(string, string) error) error {
	staged := make(map[string]string)
	backups := make(map[string]string)
	defer func() {
		for _, path := range staged {
			_ = os.Remove(path)
		}
		for _, path := range backups {
			_ = os.Remove(path)
		}
	}()
	for _, name := range Binaries {
		target, ok := targets[name]
		if !ok {
			continue
		}
		path, err := stageFile(target, bins[name])
		if err != nil {
			return err
		}
		staged[name] = path
		backup := path + ".old"
		if err := os.Link(target, backup); err != nil {
			return fmt.Errorf("cannot retain %s for rollback: %w", target, err)
		}
		backups[name] = backup
	}
	var replaced []string
	for _, name := range Binaries {
		path, ok := staged[name]
		if !ok {
			continue
		}
		if err := rename(path, targets[name]); err != nil {
			for _, previous := range replaced {
				if restoreErr := rename(backups[previous], targets[previous]); restoreErr != nil {
					err = fmt.Errorf("%w; rollback failed for %s (old binary at %s): %v",
						err, targets[previous], backups[previous], restoreErr)
					delete(backups, previous)
				}
			}
			return fmt.Errorf("could not replace %s: %w", targets[name], err)
		}
		replaced = append(replaced, name)
	}
	return nil
}

// verify requires the release checksum; an unavailable or incomplete manifest
// must never turn a verified update into an unverified one.
func (u *Updater) verify(tag, archive string, data []byte) error {
	sums, err := u.get(u.releaseURL(tag, "checksums.txt"))
	if err != nil {
		return fmt.Errorf("could not download checksums.txt for %s: %w", tag, err)
	}
	want := checksumFor(string(sums), archive)
	if want == "" {
		return fmt.Errorf("no checksum listed for %s", archive)
	}
	if got := sha256hex(data); !strings.EqualFold(want, got) {
		return fmt.Errorf("checksum mismatch for %s (expected %s, got %s)", archive, want, got)
	}
	return nil
}

func (u *Updater) releaseURL(tag, file string) string {
	return u.dlBase + "/" + u.repo + "/releases/download/" + tag + "/" + file
}

// get performs a GET and returns the body, treating any non-200 as an error.
func (u *Updater) get(url string) ([]byte, error) {
	resp, err := u.do(url, u.http)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// redirect performs a GET that stops at the first response and returns the
// Location it pointed at.
func (u *Updater) redirect(url string) (string, error) {
	noFollow := *u.http
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := u.do(url, &noFollow)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", fmt.Errorf("GET %s: expected a redirect, got %s", url, resp.Status)
	}
	return loc, nil
}

// do issues a GET with a User-Agent, which GitHub requires.
func (u *Updater) do(url string, hc *http.Client) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "docent-selfupdate")
	return hc.Do(req)
}

// tagFromReleaseURL extracts the tag from a .../releases/tag/<tag> URL. A
// repository with no releases redirects to .../releases instead, giving "".
func tagFromReleaseURL(loc string) string {
	const marker = "/releases/tag/"
	i := strings.LastIndex(loc, marker)
	if i < 0 {
		return ""
	}
	return strings.Trim(loc[i+len(marker):], "/")
}

// extractArchive returns the docent binaries in a release tar.gz, keyed by name.
func extractArchive(data []byte) (map[string][]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("could not open release archive: %w", err)
	}
	defer gz.Close()

	// Keep only top-level entries named like a docent binary
	bins := map[string][]byte{}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("could not read release archive: %w", err)
		}
		name := strings.TrimPrefix(filepath.ToSlash(hdr.Name), "./")
		if hdr.Typeflag != tar.TypeReg || !isBinaryName(name) {
			continue
		}
		if bins[name], err = io.ReadAll(tr); err != nil {
			return nil, fmt.Errorf("could not read %s from the archive: %w", name, err)
		}
	}
	return bins, nil
}

func isBinaryName(name string) bool {
	for _, b := range Binaries {
		if name == b {
			return true
		}
	}
	return false
}

// stageFile writes a sibling temporary file without replacing the target.
func stageFile(target string, data []byte) (string, error) {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".docent-update-*")
	if err != nil {
		return "", fmt.Errorf("cannot stage update in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	ready := false
	defer func() {
		if !ready {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", fmt.Errorf("could not write update: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("could not write update: %w", err)
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return "", fmt.Errorf("could not set update permissions: %w", err)
	}
	ready = true
	return tmpName, nil
}

// checksumFor finds file's sha256 in a `sha256  name` list, as sha256sum and
// GoReleaser write, tolerating the `*name` binary-mode marker.
func checksumFor(sums, file string) string {
	for _, line := range strings.Split(sums, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if strings.TrimPrefix(fields[len(fields)-1], "*") == file {
			return fields[0]
		}
	}
	return ""
}

func sha256hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// SameVersion reports whether two versions name the same release, ignoring a
// leading "v": releases are tagged "v0.1.0" while builds report "0.1.0".
func SameVersion(a, b string) bool {
	na, nb := normalizeVersion(a), normalizeVersion(b)
	return na != "" && na == nb
}

func normalizeVersion(v string) string {
	return strings.TrimPrefix(strings.TrimSpace(v), "v")
}

// AssetName is the release archive for this platform, matching the
// name_template in .goreleaser.yaml.
func AssetName() string {
	return fmt.Sprintf("docent_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
}

// Platform names this OS and architecture, such as "linux/amd64".
func Platform() string { return runtime.GOOS + "/" + runtime.GOARCH }

// SupportedPlatform reports whether releases ship a build for this platform.
func SupportedPlatform() bool {
	osOK := runtime.GOOS == "linux" || runtime.GOOS == "darwin"
	archOK := runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64"
	return osOK && archOK
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
