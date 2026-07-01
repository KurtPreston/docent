package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/kurt/slakkr-ai/apps/docentd/internal/config"
	"github.com/kurt/slakkr-ai/apps/docentd/internal/engine"
	"github.com/kurt/slakkr-ai/apps/docentd/internal/registry"
)

func newTestServer(t *testing.T, token string) http.Handler {
	t.Helper()
	reg, err := registry.NewStore(filepath.Join(t.TempDir(), "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	web := t.TempDir()
	for name, body := range map[string]string{
		"index.html": "<!doctype html><title>t</title>",
		"auth.js":    "// auth",
	} {
		if err := os.WriteFile(filepath.Join(web, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.DaemonConfig{Token: token}
	eng := engine.New(cfg, reg)
	return New(cfg, eng, reg, web).Handler()
}

func status(t *testing.T, h http.Handler, method, path, bearer string) int {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr.Code
}

func TestAuth_openWhenNoToken(t *testing.T) {
	h := newTestServer(t, "")
	for _, p := range []string{"/api/collectors", "/api/signals"} {
		if code := status(t, h, http.MethodGet, p, ""); code == http.StatusUnauthorized {
			t.Errorf("%s without token: got 401, want open", p)
		}
	}
}

func TestAuth_requiredWhenTokenSet(t *testing.T) {
	const tok = "s3cret"
	h := newTestServer(t, tok)

	gated := []struct{ method, path string }{
		{http.MethodGet, "/sessions"},
		{http.MethodGet, "/api/workitems"},
		{http.MethodGet, "/api/workitems/SALSA-1"},
		{http.MethodPost, "/api/workitems/SALSA-1/launch"},
		{http.MethodGet, "/api/signals"},
		{http.MethodGet, "/api/collectors"},
	}
	for _, g := range gated {
		if code := status(t, h, g.method, g.path, ""); code != http.StatusUnauthorized {
			t.Errorf("%s %s without bearer: got %d, want 401", g.method, g.path, code)
		}
		if code := status(t, h, g.method, g.path, "wrong"); code != http.StatusUnauthorized {
			t.Errorf("%s %s with wrong bearer: got %d, want 401", g.method, g.path, code)
		}
	}

	// A correct bearer reaches the (cheap, in-memory) handlers.
	for _, p := range []string{"/api/collectors", "/api/signals"} {
		if code := status(t, h, http.MethodGet, p, tok); code != http.StatusOK {
			t.Errorf("%s with valid bearer: got %d, want 200", p, code)
		}
	}
}

func TestAuth_healthAndStaticStayOpen(t *testing.T) {
	h := newTestServer(t, "s3cret")
	if code := status(t, h, http.MethodGet, "/health", ""); code != http.StatusOK {
		t.Errorf("/health without token: got %d, want 200", code)
	}
	if code := status(t, h, http.MethodGet, "/auth.js", ""); code != http.StatusOK {
		t.Errorf("/auth.js (static) without token: got %d, want 200", code)
	}
	if code := status(t, h, http.MethodGet, "/", ""); code != http.StatusOK {
		t.Errorf("/ (index) without token: got %d, want 200", code)
	}
}
