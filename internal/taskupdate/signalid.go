package taskupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/kurt/slakkr-ai/internal/collectors"
)

// DeriveSignalID returns a stable id for a status-derived signal: sig- + hex fragment.
func DeriveSignalID(s collectors.StatusItem) string {
	var path, key, branch, repo, directive string
	if s.Fields != nil {
		path = s.Fields["path"]
		key = s.Fields["key"]
		branch = s.Fields["branch"]
		repo = s.Fields["repo"]
	}
	directive = s.DirectiveID
	// order matters: avoid volatile fields (e.g. title text) so ids stay stable.
	raw := strings.Join([]string{
		s.Source,
		s.Kind,
		s.URL,
		s.ProjectID,
		directive,
		path,
		key,
		branch,
		repo,
	}, "|")
	sum := sha256.Sum256([]byte(raw))
	return "sig-" + hex.EncodeToString(sum[:16])
}

// SourceID is a best-effort external key for display and cross-reference.
func SourceID(s collectors.StatusItem) string {
	if s.Fields == nil {
		if s.URL != "" {
			return s.URL
		}
		return s.Title
	}
	if k := strings.TrimSpace(s.Fields["key"]); k != "" {
		return k
	}
	if b := strings.TrimSpace(s.Fields["branch"]); b != "" {
		p := strings.TrimSpace(s.Fields["path"])
		if p != "" {
			return fmt.Sprintf("%s@%s", b, p)
		}
		return b
	}
	if s.URL != "" {
		return s.URL
	}
	if p := strings.TrimSpace(s.Fields["path"]); p != "" {
		return p
	}
	return s.Title
}
