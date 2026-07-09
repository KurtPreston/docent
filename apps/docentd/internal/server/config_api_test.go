package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/KurtPreston/docent/apps/docentd/internal/config"
	"github.com/KurtPreston/docent/apps/docentd/internal/engine"
	"github.com/KurtPreston/docent/apps/docentd/internal/registry"
)

// putConfigBody JSON-encodes {"content": content} for the config API's PUT
// and validate-only bodies.
func putConfigBody(t *testing.T, content string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{"content": content})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// newConfigTestServer builds a server whose config.yaml/docentd.yaml live
// under isolated temp directories, so tests can freely write config content
// without touching the real filesystem.
func newConfigTestServer(t *testing.T) (h http.Handler, configPath string, daemonPath string) {
	t.Helper()
	configDir := t.TempDir()
	daemonDir := t.TempDir()
	reg, err := registry.NewStore(filepath.Join(t.TempDir(), "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.DaemonConfig{ConfigDir: configDir, DaemonConfigPath: filepath.Join(daemonDir, "docentd.yaml")}
	eng := engine.New(cfg, reg)
	h = New(cfg, eng, reg, t.TempDir(), nil).Handler()
	return h, filepath.Join(configDir, "config.yaml"), cfg.DaemonConfigPath
}

func TestConfigAPI_listsMissingFilesAsNotExisting(t *testing.T) {
	h, _, _ := newConfigTestServer(t)
	rr := doJSON(t, h, http.MethodGet, "/api/config", "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/config: got %d body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Files []struct {
			ID     string `json:"id"`
			Exists bool   `json:"exists"`
		} `json:"files"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal %s: %v", rr.Body.Bytes(), err)
	}
	if len(resp.Files) != 2 {
		t.Fatalf("resp=%+v", resp)
	}
	for _, f := range resp.Files {
		if f.Exists {
			t.Errorf("file %s: expected not to exist yet", f.ID)
		}
	}
}

func TestConfigAPI_saveValidConfigYAML(t *testing.T) {
	h, configPath, _ := newConfigTestServer(t)
	content := "directives:\n  - id: one\n    name: One\n    collector: webhook\n    enabled: true\n"
	rr := doJSON(t, h, http.MethodPut, "/api/config/config", "", putConfigBody(t, content))
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT /api/config/config: got %d body=%s", rr.Code, rr.Body.String())
	}
	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Fatalf("written content=%q want %q", got, content)
	}
}

func TestConfigAPI_rejectsInvalidConfigYAML(t *testing.T) {
	h, configPath, _ := newConfigTestServer(t)
	// Duplicate directive IDs fail userdata.ConfigFile.Validate.
	content := "directives:\n" +
		"  - id: dup\n    name: One\n    collector: webhook\n    enabled: true\n" +
		"  - id: dup\n    name: Two\n    collector: webhook\n    enabled: true\n"
	rr := doJSON(t, h, http.MethodPut, "/api/config/config", "", putConfigBody(t, content))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("PUT /api/config/config with dup ids: got %d body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		OK       bool     `json:"ok"`
		Problems []string `json:"problems"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal %s: %v", rr.Body.Bytes(), err)
	}
	if resp.OK || len(resp.Problems) == 0 {
		t.Fatalf("resp=%+v", resp)
	}
	if _, err := os.Stat(configPath); err == nil {
		t.Fatal("invalid content should not have been written")
	}
}

func TestConfigAPI_validateOnlyDoesNotWrite(t *testing.T) {
	h, configPath, _ := newConfigTestServer(t)
	content := "directives:\n  - id: one\n    name: One\n    collector: webhook\n    enabled: true\n"
	rr := doJSON(t, h, http.MethodPost, "/api/config/config/validate", "", putConfigBody(t, content))
	if rr.Code != http.StatusOK {
		t.Fatalf("POST validate: got %d body=%s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(configPath); err == nil {
		t.Fatal("validate-only should not write the file")
	}
}

func TestConfigAPI_daemonConfigRejectsUnknownField(t *testing.T) {
	h, _, _ := newConfigTestServer(t)
	content := "totallyBogusField: true\n"
	rr := doJSON(t, h, http.MethodPut, "/api/config/docentd", "", putConfigBody(t, content))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("PUT /api/config/docentd with unknown field: got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestConfigAPI_daemonConfigAcceptsKnownFields(t *testing.T) {
	h, _, daemonPath := newConfigTestServer(t)
	content := "port: 12345\ntoken: s3cret\n"
	rr := doJSON(t, h, http.MethodPut, "/api/config/docentd", "", putConfigBody(t, content))
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT /api/config/docentd: got %d body=%s", rr.Code, rr.Body.String())
	}
	got, err := os.ReadFile(daemonPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Fatalf("written content=%q want %q", got, content)
	}
}

func TestConfigAPI_unknownIDIs404(t *testing.T) {
	h, _, _ := newConfigTestServer(t)
	rr := doJSON(t, h, http.MethodPut, "/api/config/nope", "", putConfigBody(t, ""))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("PUT /api/config/nope: got %d want 404", rr.Code)
	}
}
