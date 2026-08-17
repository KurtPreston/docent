package server

import (
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/KurtPreston/docent/libs/agentsession"
)

func (s *Server) attachmentStore() (*agentsession.AttachmentStore, error) {
	if s.agents == nil || s.agents.Store == nil {
		return nil, fmt.Errorf("agent sessions unavailable")
	}
	return agentsession.NewAttachmentStore(s.agents.Store.Root())
}

// agentAttachmentUpload handles POST /api/agents/attachments.
func (s *Server) agentAttachmentUpload(w http.ResponseWriter, r *http.Request) {
	if s.agents == nil {
		s.agentsUnavailable(w)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	const maxMemory = 32 << 20
	if err := r.ParseMultipartForm(maxMemory); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid multipart form"})
		return
	}

	store, err := s.attachmentStore()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	var files []*multipart.FileHeader
	if r.MultipartForm != nil && r.MultipartForm.File != nil {
		files = append(files, r.MultipartForm.File["file"]...)
		files = append(files, r.MultipartForm.File["files"]...)
	}
	if len(files) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "no files uploaded"})
		return
	}
	if len(files) > agentsession.MaxAttachmentsPerRequest() {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":    false,
			"error": fmt.Sprintf("at most %d files per request", agentsession.MaxAttachmentsPerRequest()),
		})
		return
	}

	var staged []agentsession.StagedAttachment
	for _, fh := range files {
		f, err := fh.Open()
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		item, err := store.Stage(fh.Filename, fh.Header.Get("Content-Type"), f, fh.Size)
		f.Close()
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		staged = append(staged, item)
	}

	if len(staged) == 1 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "attachment": staged[0]})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "attachments": staged})
}

// agentAttachmentServe handles GET /api/agents/{id}/attachments/{name}.
func (s *Server) agentAttachmentServe(w http.ResponseWriter, r *http.Request, sessionID, name string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	store, err := s.attachmentStore()
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	f, att, err := store.OpenSessionAttachment(sessionID, name)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "attachment not found"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	defer f.Close()

	ct := att.ContentType
	if ct == "" {
		ct = mime.TypeByExtension(filepath.Ext(att.Name))
	}
	if ct == "" {
		ct = "application/octet-stream"
	}
	if !allowedAttachmentContentType(ct) {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", att.Name))
	if info, err := f.Stat(); err == nil {
		http.ServeContent(w, r, att.Name, info.ModTime(), f)
	} else {
		io.Copy(w, f)
	}
}

func allowedAttachmentContentType(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(strings.Split(ct, ";")[0]))
	switch {
	case strings.HasPrefix(ct, "image/"):
		return true
	case ct == "application/pdf":
		return true
	case strings.HasPrefix(ct, "text/"):
		return true
	default:
		return false
	}
}
