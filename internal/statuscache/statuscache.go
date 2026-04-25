package statuscache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kurt/slakkr-ai/internal/collectors"
)

const snapshotFile = "last-snapshot.json"

type snapshotFileBody struct {
	UpdatedAt time.Time    `json:"updated_at"`
	Items     []cachedItem `json:"items"`
}

type cachedItem struct {
	StableID    string `json:"stable_id"`
	Fingerprint string `json:"fingerprint"`
}

// StableID is a deterministic id for correlating the same logical status across runs.
func StableID(s collectors.StatusItem) string {
	path := ""
	repo := ""
	if s.Fields != nil {
		path = s.Fields["path"]
		repo = s.Fields["repo"]
	}
	raw := strings.Join([]string{
		s.DirectiveID,
		s.ProjectID,
		s.Source,
		s.Kind,
		s.Title,
		s.URL,
		path,
		repo,
	}, "|")
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:12])
}

// Fingerprint hashes fields that should trigger "updated" when they change.
func Fingerprint(s collectors.StatusItem) string {
	keys := make([]string, 0, len(s.Fields))
	for k := range s.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	fmt.Fprintf(&b, "%s|%s|%s|%s|%s", s.Summary, s.Severity, s.Kind, s.Title, s.URL)
	for _, k := range keys {
		fmt.Fprintf(&b, "|%s=%s", k, s.Fields[k])
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:16])
}

// Annotate sets StableID, ChangeState on each item and persists the new snapshot.
func Annotate(userdataRoot string, items []collectors.StatusItem, now time.Time) ([]collectors.StatusItem, error) {
	if userdataRoot == "" {
		for i := range items {
			items[i].StableID = StableID(items[i])
			if items[i].ChangeState == "" {
				items[i].ChangeState = "new"
			}
		}
		return items, nil
	}
	dir := filepath.Join(userdataRoot, "status-cache")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return items, err
	}
	path := filepath.Join(dir, snapshotFile)
	prev := loadSnapshot(path)
	prevFP := make(map[string]string, len(prev.Items))
	for _, it := range prev.Items {
		prevFP[it.StableID] = it.Fingerprint
	}
	for i := range items {
		id := StableID(items[i])
		fp := Fingerprint(items[i])
		items[i].StableID = id
		old, ok := prevFP[id]
		if !ok {
			items[i].ChangeState = "new"
		} else if old != fp {
			items[i].ChangeState = "updated"
		} else {
			items[i].ChangeState = "unchanged"
		}
	}
	next := snapshotFileBody{UpdatedAt: now.UTC(), Items: make([]cachedItem, 0, len(items))}
	for _, it := range items {
		next.Items = append(next.Items, cachedItem{
			StableID:    it.StableID,
			Fingerprint: Fingerprint(it),
		})
	}
	raw, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return items, err
	}
	return items, os.WriteFile(path, raw, 0o644)
}

func loadSnapshot(path string) snapshotFileBody {
	data, err := os.ReadFile(path)
	if err != nil {
		return snapshotFileBody{}
	}
	var body snapshotFileBody
	if json.Unmarshal(data, &body) != nil {
		return snapshotFileBody{}
	}
	return body
}
