package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KurtPreston/docent/apps/docentd/internal/config"
	"github.com/KurtPreston/docent/apps/docentd/internal/engine"
	"github.com/KurtPreston/docent/apps/docentd/internal/registry"
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
		"app.js":     "// app",
	} {
		if err := os.WriteFile(filepath.Join(web, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.DaemonConfig{Token: token}
	eng := engine.New(cfg, reg)
	// nil webFS => disk-serve mode from the temp dir above.
	return New(cfg, eng, reg, web, nil).Handler()
}

func status(t *testing.T, h http.Handler, method, path, bearer string) int {
	t.Helper()
	return doJSON(t, h, method, path, bearer, "").Code
}

func doJSON(t *testing.T, h http.Handler, method, path, bearer, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
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
		{http.MethodGet, "/api/workitems"},
		{http.MethodGet, "/api/workitems/SALSA-1"},
		{http.MethodPost, "/api/workitems/SALSA-1/launch"},
		{http.MethodGet, "/api/signals"},
		{http.MethodGet, "/api/collectors"},
		{http.MethodGet, "/api/config"},
		{http.MethodPost, "/api/report"},
		{http.MethodGet, "/api/report/meta"},
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
	if code := status(t, h, http.MethodGet, "/app.js", ""); code != http.StatusOK {
		t.Errorf("/app.js (static) without token: got %d, want 200", code)
	}
	if code := status(t, h, http.MethodGet, "/", ""); code != http.StatusOK {
		t.Errorf("/ (index) without token: got %d, want 200", code)
	}
}

func TestSessionEvents(t *testing.T) {
	h := newTestServer(t, "")

	// A bad event is rejected.
	bad := doJSON(t, h, http.MethodPost, "/api/sessions/events", "",
		`{"ide":"cursor","ideHost":"mac","event":"nope"}`)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("bad event: got %d, want 400", bad.Code)
	}

	// An open event is accepted and echoes the composite key.
	openRR := doJSON(t, h, http.MethodPost, "/api/sessions/events", "",
		`{"ide":"cursor","ideHost":"mac","path":"/code/proj","event":"open"}`)
	if openRR.Code != http.StatusOK {
		t.Fatalf("open event: got %d", openRR.Code)
	}
	var openResp SessionEventResponse
	if err := json.Unmarshal(openRR.Body.Bytes(), &openResp); err != nil {
		t.Fatal(err)
	}
	if !openResp.OK || openResp.Key == "" || openResp.Event != "open" {
		t.Fatalf("unexpected open response: %+v", openResp)
	}

	// The session now appears in the ingest listing.
	listRR := doJSON(t, h, http.MethodGet, "/api/sessions", "", "")
	if listRR.Code != http.StatusOK {
		t.Fatalf("list: got %d", listRR.Code)
	}
	var list struct {
		Sessions []struct {
			Key  string `json:"key"`
			Name string `json:"name"`
			Path string `json:"path"`
			Live bool   `json:"live"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(listRR.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Sessions) != 1 || list.Sessions[0].Name != "proj" || !list.Sessions[0].Live {
		t.Fatalf("unexpected session list: %+v", list.Sessions)
	}

	// A close event removes it.
	closeRR := doJSON(t, h, http.MethodPost, "/api/sessions/events", "",
		`{"ide":"cursor","ideHost":"mac","path":"/code/proj","event":"close"}`)
	if closeRR.Code != http.StatusOK {
		t.Fatalf("close event: got %d", closeRR.Code)
	}
	list2 := doJSON(t, h, http.MethodGet, "/api/sessions", "", "")
	var after struct {
		Sessions []json.RawMessage `json:"sessions"`
	}
	if err := json.Unmarshal(list2.Body.Bytes(), &after); err != nil {
		t.Fatal(err)
	}
	if len(after.Sessions) != 0 {
		t.Fatalf("session should be gone after close, got %d", len(after.Sessions))
	}
}

func TestSessionEventsRemoteBindsToExtension(t *testing.T) {
	h := newTestServer(t, "")

	// A client-side extension window: concrete host + ssh alias (remote session).
	openRR := doJSON(t, h, http.MethodPost, "/api/sessions/events", "",
		`{"ide":"cursor","ideHost":"mac","targetHost":"desktop","path":"/home/me/proj","event":"open"}`)
	if openRR.Code != http.StatusOK {
		t.Fatalf("open: got %d", openRR.Code)
	}
	var openResp SessionEventResponse
	if err := json.Unmarshal(openRR.Body.Bytes(), &openResp); err != nil {
		t.Fatal(err)
	}

	// A remote hook event carries ideHost as the boolean true (host unknown).
	hookRR := doJSON(t, h, http.MethodPost, "/api/sessions/events", "",
		`{"ide":"cursor","ideHost":true,"path":"/home/me/proj","event":"agent_response_received"}`)
	if hookRR.Code != http.StatusOK {
		t.Fatalf("remote hook event: got %d", hookRR.Code)
	}
	var hookResp SessionEventResponse
	if err := json.Unmarshal(hookRR.Body.Bytes(), &hookResp); err != nil {
		t.Fatal(err)
	}
	if hookResp.Key != openResp.Key {
		t.Fatalf("remote event should bind to the extension record: got key %q, want %q", hookResp.Key, openResp.Key)
	}

	// The bind must not fork a second session.
	listRR := doJSON(t, h, http.MethodGet, "/api/sessions", "", "")
	var list struct {
		Sessions []json.RawMessage `json:"sessions"`
	}
	if err := json.Unmarshal(listRR.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Sessions) != 1 {
		t.Fatalf("expected 1 session after bind, got %d", len(list.Sessions))
	}
}

func TestReportJobLifecycle(t *testing.T) {
	h := newTestServer(t, "")

	// Meta lists modes (with defaults), scopes, collects, and the AI identity.
	metaRR := doJSON(t, h, http.MethodGet, "/api/report/meta", "", "")
	if metaRR.Code != http.StatusOK {
		t.Fatalf("meta: got %d", metaRR.Code)
	}
	var meta struct {
		Modes []struct {
			ID             string `json:"id"`
			Name           string `json:"name"`
			PromptRequired bool   `json:"promptRequired"`
			LookbackKind   string `json:"lookbackKind"`
			LookbackDays   int    `json:"lookbackDays"`
			Scope          string `json:"scope"`
			Prompt         string `json:"prompt"`
			Collect        string `json:"collect"`
		} `json:"modes"`
		Scopes   []string `json:"scopes"`
		Collects []string `json:"collects"`
		Provider struct {
			Label    string `json:"label"`
			Provider string `json:"provider"`
			Model    string `json:"model"`
		} `json:"provider"`
	}
	if err := json.Unmarshal(metaRR.Body.Bytes(), &meta); err != nil {
		t.Fatalf("meta json: %v", err)
	}
	if len(meta.Modes) == 0 {
		t.Fatal("meta: expected at least one mode")
	}
	if len(meta.Scopes) != 3 {
		t.Fatalf("meta scopes: got %v", meta.Scopes)
	}
	if len(meta.Collects) != 3 {
		t.Fatalf("meta collects: got %v", meta.Collects)
	}
	if meta.Provider.Provider != "rule-based" {
		t.Fatalf("meta provider: got %q want rule-based", meta.Provider.Provider)
	}
	// recent-activity leaves lookback/scope unset → non-interactive defaults.
	var foundRecent bool
	for _, m := range meta.Modes {
		if m.ID != "recent-activity" {
			continue
		}
		foundRecent = true
		if m.LookbackKind != "days" || m.LookbackDays != 7 {
			t.Fatalf("recent-activity lookback: kind=%q days=%d want days/7", m.LookbackKind, m.LookbackDays)
		}
		if m.Scope != "involved" {
			t.Fatalf("recent-activity scope: got %q want involved", m.Scope)
		}
		if m.Collect != "events" {
			t.Fatalf("recent-activity collect: got %q want events", m.Collect)
		}
		if m.Prompt == "" || m.PromptRequired {
			t.Fatalf("recent-activity prompt: required=%v prompt empty=%v", m.PromptRequired, m.Prompt == "")
		}
		break
	}
	if !foundRecent {
		t.Fatal("meta: missing recent-activity mode")
	}

	// Start a report: rule-based provider + no directives => fast, deterministic.
	startRR := doJSON(t, h, http.MethodPost, "/api/report", "", `{"mode":"recent-activity","days":7}`)
	if startRR.Code != http.StatusAccepted {
		t.Fatalf("start: got %d body=%s", startRR.Code, startRR.Body.String())
	}
	var start struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(startRR.Body.Bytes(), &start); err != nil || start.ID == "" {
		t.Fatalf("start json: err=%v id=%q", err, start.ID)
	}

	// Poll until the job reaches a terminal state.
	var final struct {
		Status   string `json:"status"`
		Markdown string `json:"markdown"`
		Error    string `json:"error"`
		Meta     *struct {
			LookbackDays int `json:"lookbackDays"`
		} `json:"meta"`
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		pollRR := doJSON(t, h, http.MethodGet, "/api/report/"+start.ID, "", "")
		if pollRR.Code != http.StatusOK {
			t.Fatalf("poll: got %d", pollRR.Code)
		}
		if err := json.Unmarshal(pollRR.Body.Bytes(), &final); err != nil {
			t.Fatalf("poll json: %v", err)
		}
		if final.Status == "done" || final.Status == "error" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if final.Status != "done" {
		t.Fatalf("report did not finish: status=%q err=%q", final.Status, final.Error)
	}
	if strings.TrimSpace(final.Markdown) == "" {
		t.Fatal("done report has empty markdown")
	}
	if final.Meta == nil || final.Meta.LookbackDays != 7 {
		t.Fatalf("resolved meta lookback: %+v", final.Meta)
	}

	// Bad requests and unknown ids are handled cleanly.
	if code := doJSON(t, h, http.MethodPost, "/api/report", "", `{"mode":""}`).Code; code != http.StatusBadRequest {
		t.Fatalf("empty mode: got %d want 400", code)
	}
	if code := doJSON(t, h, http.MethodPost, "/api/report", "", `{"mode":"x","scope":"bogus"}`).Code; code != http.StatusBadRequest {
		t.Fatalf("bad scope: got %d want 400", code)
	}
	if code := status(t, h, http.MethodGet, "/api/report/deadbeef", ""); code != http.StatusNotFound {
		t.Fatalf("unknown report id: got %d want 404", code)
	}
}

func TestReportStream(t *testing.T) {
	h := newTestServer(t, "")

	startRR := doJSON(t, h, http.MethodPost, "/api/report", "", `{"mode":"recent-activity","days":7}`)
	if startRR.Code != http.StatusAccepted {
		t.Fatalf("start: got %d body=%s", startRR.Code, startRR.Body.String())
	}
	var start struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(startRR.Body.Bytes(), &start); err != nil || start.ID == "" {
		t.Fatalf("start json: err=%v id=%q", err, start.ID)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/report/"+start.ID+"/stream", nil)
	rr := httptest.NewRecorder()
	// ServeHTTP blocks until the stream ends (terminal event). Rule-based is fast.
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(rr, req)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("stream timed out")
	}

	if rr.Code != http.StatusOK {
		t.Fatalf("stream status: got %d body=%s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type: got %q", ct)
	}

	var phases []string
	var sawDone bool
	for _, frame := range strings.Split(rr.Body.String(), "\n\n") {
		frame = strings.TrimSpace(frame)
		if frame == "" {
			continue
		}
		for _, line := range strings.Split(frame, "\n") {
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			raw := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			var ev reportEvent
			if err := json.Unmarshal([]byte(raw), &ev); err != nil {
				t.Fatalf("event json: %v raw=%q", err, raw)
			}
			switch ev.Type {
			case "phase":
				phases = append(phases, ev.Phase)
			case "done":
				sawDone = true
				if strings.TrimSpace(ev.Markdown) == "" {
					t.Fatal("done event missing markdown")
				}
			}
		}
	}
	if !sawDone {
		t.Fatalf("stream missing done event; body=%q", rr.Body.String())
	}
	// Rule-based runs Collect → Correlate → Render; expect the three phases.
	want := map[string]bool{"collecting": true, "correlating": true, "generating": true}
	for _, p := range phases {
		delete(want, p)
	}
	if len(want) > 0 {
		t.Fatalf("missing phases %v (got %v)", want, phases)
	}

	if code := status(t, h, http.MethodGet, "/api/report/deadbeef/stream", ""); code != http.StatusNotFound {
		t.Fatalf("unknown stream id: got %d want 404", code)
	}
}

func TestStaticSPAFallback(t *testing.T) {
	h := newTestServer(t, "")
	// A real asset is served as-is.
	if code := status(t, h, http.MethodGet, "/app.js", ""); code != http.StatusOK {
		t.Errorf("/app.js: got %d, want 200", code)
	}
	// Extensionless client-side routes fall back to index.html so react-router
	// can render them (including future routes like /report).
	for _, p := range []string{"/signals", "/collectors", "/workitem", "/report"} {
		if code := status(t, h, http.MethodGet, p, ""); code != http.StatusOK {
			t.Errorf("client route %s: got %d, want 200 (index fallback)", p, code)
		}
	}
	// Missing files with an extension, and unmatched /api/* paths, stay 404s.
	if code := status(t, h, http.MethodGet, "/missing.js", ""); code != http.StatusNotFound {
		t.Errorf("/missing.js: got %d, want 404", code)
	}
	if code := status(t, h, http.MethodGet, "/api/nope", ""); code != http.StatusNotFound {
		t.Errorf("/api/nope: got %d, want 404", code)
	}
}
