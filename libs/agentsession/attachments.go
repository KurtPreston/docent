package agentsession

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Attachment is a file the user handed the agent alongside a prompt. It lives
// on disk next to the transcript; the agent reads it by path, because neither
// CLI accepts bytes on stdin.
type Attachment struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	ContentType string `json:"contentType,omitempty"`
	Size        int64  `json:"size,omitempty"`
}

const (
	stagingDirName       = ".staging"
	attachmentsDirName   = "attachments"
	stagedMetaFile       = "meta.json"
	maxAttachmentBytes   = 20 << 20 // 20 MiB
	maxAttachmentsPerReq = 10
	defaultStagingMaxAge = 3 * time.Hour
)

// MaxAttachmentsPerRequest is the upload batch limit enforced by the HTTP handler.
func MaxAttachmentsPerRequest() int { return maxAttachmentsPerReq }

// StagedAttachment describes one upload waiting to be bound to a turn.
type StagedAttachment struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
}

type stagedMeta struct {
	Name        string `json:"name"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
}

// AttachmentStore stages uploads and promotes them into session directories.
type AttachmentStore struct {
	root string
}

// NewAttachmentStore opens a store under the agent-sessions root.
func NewAttachmentStore(root string) (*AttachmentStore, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, errors.New("agentsession: attachment store needs a directory")
	}
	if err := os.MkdirAll(filepath.Join(root, stagingDirName), 0o700); err != nil {
		return nil, fmt.Errorf("agentsession: creating staging dir: %w", err)
	}
	return &AttachmentStore{root: root}, nil
}

func (a *AttachmentStore) stagingRoot() string {
	return filepath.Join(a.root, stagingDirName)
}

func (a *AttachmentStore) stagingDir(id string) string {
	return filepath.Join(a.stagingRoot(), id)
}

func (a *AttachmentStore) sessionAttachmentsDir(sessionID string) string {
	return filepath.Join(a.root, sessionID, attachmentsDirName)
}

// SweepStaging removes staged uploads older than maxAge.
func (a *AttachmentStore) SweepStaging(maxAge time.Duration) error {
	if maxAge <= 0 {
		maxAge = defaultStagingMaxAge
	}
	entries, err := os.ReadDir(a.stagingRoot())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	cutoff := time.Now().Add(-maxAge)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			_ = os.RemoveAll(filepath.Join(a.stagingRoot(), e.Name()))
		}
	}
	return nil
}

// SanitizeAttachmentName returns a safe base filename for storage.
func SanitizeAttachmentName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("agentsession: attachment needs a name")
	}
	if strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("agentsession: unsafe attachment name %q", name)
	}
	base := filepath.Base(name)
	if base == "." || base == ".." || strings.HasPrefix(base, ".") {
		return "", fmt.Errorf("agentsession: unsafe attachment name %q", name)
	}
	if base != name {
		return "", fmt.Errorf("agentsession: unsafe attachment name %q", name)
	}
	return base, nil
}

// Stage writes one upload into staging and returns its id.
func (a *AttachmentStore) Stage(name, contentType string, r io.Reader, size int64) (StagedAttachment, error) {
	safe, err := SanitizeAttachmentName(name)
	if err != nil {
		return StagedAttachment{}, err
	}
	if size < 0 {
		return StagedAttachment{}, errors.New("agentsession: negative attachment size")
	}
	if size > maxAttachmentBytes {
		return StagedAttachment{}, fmt.Errorf("agentsession: attachment exceeds %d byte limit", maxAttachmentBytes)
	}

	id, err := newUUID()
	if err != nil {
		return StagedAttachment{}, err
	}
	dir := a.stagingDir(id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return StagedAttachment{}, err
	}

	dest := filepath.Join(dir, safe)
	if err := writeLimitedFile(dest, r, maxAttachmentBytes); err != nil {
		_ = os.RemoveAll(dir)
		return StagedAttachment{}, err
	}
	info, err := os.Stat(dest)
	if err != nil {
		_ = os.RemoveAll(dir)
		return StagedAttachment{}, err
	}
	if info.Size() > maxAttachmentBytes {
		_ = os.RemoveAll(dir)
		return StagedAttachment{}, fmt.Errorf("agentsession: attachment exceeds %d byte limit", maxAttachmentBytes)
	}

	ct := strings.TrimSpace(contentType)
	if ct == "" {
		ct = mime.TypeByExtension(filepath.Ext(safe))
	}
	meta := stagedMeta{Name: safe, ContentType: ct, Size: info.Size()}
	b, err := json.Marshal(meta)
	if err != nil {
		_ = os.RemoveAll(dir)
		return StagedAttachment{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, stagedMetaFile), b, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return StagedAttachment{}, err
	}

	return StagedAttachment{
		ID:          id,
		Name:        safe,
		ContentType: ct,
		Size:        info.Size(),
	}, nil
}

func writeLimitedFile(path string, r io.Reader, limit int64) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	n, err := io.Copy(f, io.LimitReader(r, limit+1))
	if err != nil {
		return err
	}
	if n > limit {
		return fmt.Errorf("agentsession: attachment exceeds %d byte limit", limit)
	}
	return f.Close()
}

// Promote moves staged uploads into a session's attachments directory.
func (a *AttachmentStore) Promote(sessionID string, stagedIDs []string) ([]Attachment, error) {
	if err := validID(sessionID); err != nil {
		return nil, err
	}
	destDir := a.sessionAttachmentsDir(sessionID)
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return nil, err
	}

	var out []Attachment
	for _, id := range stagedIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if err := validID(id); err != nil {
			return nil, err
		}
		srcDir := a.stagingDir(id)
		meta, err := readStagedMeta(srcDir)
		if err != nil {
			return nil, fmt.Errorf("agentsession: staged attachment %q: %w", id, err)
		}
		src := filepath.Join(srcDir, meta.Name)
		if _, err := os.Stat(src); err != nil {
			return nil, fmt.Errorf("agentsession: staged attachment %q: %w", id, err)
		}

		name := dedupeAttachmentName(destDir, meta.Name, id)
		dest := filepath.Join(destDir, name)
		if err := os.Rename(src, dest); err != nil {
			if err := copyFile(src, dest); err != nil {
				return nil, fmt.Errorf("agentsession: promoting %q: %w", id, err)
			}
		}
		_ = os.RemoveAll(srcDir)

		out = append(out, Attachment{
			Name:        name,
			Path:        dest,
			ContentType: meta.ContentType,
			Size:        meta.Size,
		})
	}
	return out, nil
}

func readStagedMeta(dir string) (stagedMeta, error) {
	b, err := os.ReadFile(filepath.Join(dir, stagedMetaFile))
	if err != nil {
		return stagedMeta{}, err
	}
	var meta stagedMeta
	if err := json.Unmarshal(b, &meta); err != nil {
		return stagedMeta{}, err
	}
	if strings.TrimSpace(meta.Name) == "" {
		return stagedMeta{}, errors.New("missing name in staged meta")
	}
	return meta, nil
}

func dedupeAttachmentName(dir, name, uploadID string) string {
	if _, err := os.Stat(filepath.Join(dir, name)); os.IsNotExist(err) {
		return name
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	prefix := uploadID
	if len(prefix) > 8 {
		prefix = prefix[:8]
	}
	candidate := base + "-" + prefix + ext
	if _, err := os.Stat(filepath.Join(dir, candidate)); os.IsNotExist(err) {
		return candidate
	}
	for i := 2; ; i++ {
		candidate = fmt.Sprintf("%s-%s-%d%s", base, prefix, i, ext)
		if _, err := os.Stat(filepath.Join(dir, candidate)); os.IsNotExist(err) {
			return candidate
		}
	}
}

func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

// OpenSessionAttachment opens one attachment file for serving.
func (a *AttachmentStore) OpenSessionAttachment(sessionID, name string) (*os.File, Attachment, error) {
	if err := validID(sessionID); err != nil {
		return nil, Attachment{}, err
	}
	safe, err := SanitizeAttachmentName(name)
	if err != nil {
		return nil, Attachment{}, err
	}
	path := filepath.Join(a.sessionAttachmentsDir(sessionID), safe)
	clean := filepath.Clean(path)
	base := filepath.Clean(a.sessionAttachmentsDir(sessionID))
	if !strings.HasPrefix(clean, base+string(filepath.Separator)) && clean != base {
		return nil, Attachment{}, fmt.Errorf("agentsession: attachment path escape")
	}
	f, err := os.Open(clean)
	if err != nil {
		return nil, Attachment{}, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, Attachment{}, err
	}
	ct := mime.TypeByExtension(filepath.Ext(safe))
	return f, Attachment{
		Name:        safe,
		Path:        clean,
		ContentType: ct,
		Size:        info.Size(),
	}, nil
}

// PromptWithAttachments returns the stdin prompt, appending absolute paths for
// the agent to read. The user's text is kept separate in KindPrompt events.
func PromptWithAttachments(prompt string, atts []Attachment) string {
	prompt = strings.TrimSpace(prompt)
	if len(atts) == 0 {
		return prompt
	}
	var b strings.Builder
	if prompt != "" {
		b.WriteString(prompt)
		b.WriteString("\n\n")
	}
	b.WriteString("Attached files (read these paths):\n")
	for _, a := range atts {
		if strings.TrimSpace(a.Path) == "" {
			continue
		}
		fmt.Fprintf(&b, "- %s", a.Path)
		if a.Name != "" {
			fmt.Fprintf(&b, " (%s)", a.Name)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// AttachmentDirs returns distinct parent directories to pass as --add-dir.
func AttachmentDirs(atts []Attachment) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, a := range atts {
		dir := filepath.Dir(a.Path)
		if dir == "" {
			continue
		}
		if _, ok := seen[dir]; ok {
			continue
		}
		seen[dir] = struct{}{}
		out = append(out, dir)
	}
	return out
}
