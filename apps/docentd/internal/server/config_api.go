package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/KurtPreston/docent/apps/docentd/internal/config"
	"github.com/KurtPreston/docent/libs/config/configschema"
	"github.com/KurtPreston/docent/libs/config/userdata"
	"gopkg.in/yaml.v3"
)

// configFileMeta describes one editable docent config file surfaced by the
// Settings page.
type configFileMeta struct {
	ID    string
	Label string
}

// configFileList is the fixed set of config files the Settings page can view
// and edit. Order here is the display order in the UI.
var configFileList = []configFileMeta{
	{ID: "config", Label: "config.yaml"},
	{ID: "docentd", Label: "docentd.yaml"},
}

// configFilePath resolves the on-disk path for a config file id, matching
// the same paths config.Load reads (so edits made here take effect on the
// next daemon restart without any extra reconciliation).
func (s *Server) configFilePath(id string) (path string, ok bool) {
	switch id {
	case "config":
		return filepath.Join(s.cfg.ConfigDir, "config.yaml"), true
	case "docentd":
		return s.cfg.DaemonConfigPath, true
	default:
		return "", false
	}
}

type configFileView struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Path    string `json:"path"`
	Content string `json:"content"`
	Exists  bool   `json:"exists"`
}

// configAPI serves GET /api/config: the current on-disk content of every
// editable config file, for the Settings page to render.
func (s *Server) configAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	views := make([]configFileView, 0, len(configFileList))
	for _, f := range configFileList {
		path, _ := s.configFilePath(f.ID)
		content, exists, err := readFileIfExists(path)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		views = append(views, configFileView{ID: f.ID, Label: f.Label, Path: path, Content: content, Exists: exists})
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": views})
}

// configItemAPI handles PUT /api/config/{id} (validate, then save) and
// POST /api/config/{id}/validate (validate only, no write — used for live
// feedback as the user types).
func (s *Server) configItemAPI(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/config/"), "/")
	parts := strings.Split(rest, "/")

	if len(parts) == 2 && parts[1] == "schema" {
		s.configSchemaAPI(w, r, parts[0])
		return
	}

	validateOnly := false
	switch {
	case len(parts) == 2 && parts[1] == "validate":
		validateOnly = true
	case len(parts) == 1 && parts[0] != "":
		// save path
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "expected /api/config/{id}, /api/config/{id}/validate, or /api/config/{id}/schema"})
		return
	}

	id := parts[0]
	path, ok := s.configFilePath(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "unknown config file id"})
		return
	}

	wantMethod := http.MethodPut
	if validateOnly {
		wantMethod = http.MethodPost
	}
	if r.Method != wantMethod {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad body"})
		return
	}
	var req struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid json"})
		return
	}

	if problems := validateConfigContent(id, []byte(req.Content)); len(problems) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "problems": problems})
		return
	}
	if validateOnly {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if err := os.WriteFile(path, []byte(req.Content), 0o644); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// configSchemaAPI handles GET /api/config/{id}/schema: the JSON Schema for a
// config file's contents, consumed by the dashboard's Monaco editor for
// inline validation/completion (via monaco-yaml).
func (s *Server) configSchemaAPI(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body []byte
	switch id {
	case "config":
		body = configschema.SchemaBytes
	case "docentd":
		body = configschema.DaemonSchemaBytes
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "no schema available for this config file"})
		return
	}
	w.Header().Set("Content-Type", "application/schema+json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// validateConfigContent validates raw YAML for a config file id, returning a
// list of human-readable problems (nil when valid).
func validateConfigContent(id string, content []byte) []string {
	switch id {
	case "config":
		return validateAppConfigYAML(content)
	case "docentd":
		return validateDaemonConfigYAML(content)
	default:
		return []string{"unknown config file id"}
	}
}

// validateAppConfigYAML mirrors the validation config.Load and
// userdata.Store.SaveConfig already perform for config.yaml, so the Settings
// page rejects exactly the content the daemon would refuse to load.
func validateAppConfigYAML(content []byte) []string {
	if len(bytes.TrimSpace(content)) == 0 {
		return nil
	}
	if err := configschema.ValidateYAML(content); err != nil {
		return configschema.ValidationProblems(err)
	}
	var file userdata.ConfigFile
	if err := yaml.Unmarshal(content, &file); err != nil {
		return []string{err.Error()}
	}
	if err := file.Validate(); err != nil {
		var ve userdata.ValidationError
		if errors.As(err, &ve) {
			return ve.Problems
		}
		return []string{err.Error()}
	}
	return nil
}

// validateDaemonConfigYAML validates docentd.yaml against the daemon JSON
// Schema, then decodes with KnownFields as a secondary check.
func validateDaemonConfigYAML(content []byte) []string {
	if len(bytes.TrimSpace(content)) == 0 {
		return nil
	}
	if err := configschema.ValidateDaemonYAML(content); err != nil {
		return configschema.ValidationProblems(err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(content))
	dec.KnownFields(true)
	var dc config.DaemonConfig
	if err := dec.Decode(&dc); err != nil && !errors.Is(err, io.EOF) {
		return []string{err.Error()}
	}
	return nil
}

func readFileIfExists(path string) (content string, exists bool, err error) {
	if path == "" {
		return "", false, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	return string(b), true, nil
}
