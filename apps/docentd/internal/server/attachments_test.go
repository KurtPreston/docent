package server

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/KurtPreston/docent/apps/docentd/internal/config"
)

func TestAgentAttachmentUpload(t *testing.T) {
	cfg := config.DaemonConfig{}
	cfg.AI.Cursor.Command = stubAgentBinary(t)
	h := newTestServerConfig(t, cfg)

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", "note.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, "hello"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/agents/attachments", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		OK         bool `json:"ok"`
		Attachment struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"attachment"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.Attachment.ID == "" || resp.Attachment.Name != "note.txt" {
		t.Fatalf("resp: %+v", resp)
	}
}

func TestAgentAttachmentUpload_rejectsUnsafeName(t *testing.T) {
	cfg := config.DaemonConfig{}
	cfg.AI.Cursor.Command = stubAgentBinary(t)
	h := newTestServerConfig(t, cfg)

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", ".hidden")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, "nope"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/agents/attachments", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
}

func TestAgentAttachmentUpload_auth(t *testing.T) {
	cfg := config.DaemonConfig{Token: "secret"}
	cfg.AI.Cursor.Command = stubAgentBinary(t)
	h := newTestServerConfig(t, cfg)

	if code := status(t, h, http.MethodPost, "/api/agents/attachments", ""); code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", code)
	}
}
