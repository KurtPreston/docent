package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/KurtPreston/docent/libs/collectors"
)

// hooksAPI handles POST /api/hooks/{directive}: a nudge webhook that forces
// collection of the named directive's units so automations can fire without
// waiting for the next poll. Auth: Bearer token (same as other APIs), or
// X-Hub-Signature-256 / X-Docent-Hook-Secret matching DOCENT_HOOK_SECRET /
// the daemon token.
func (s *Server) hooksAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/hooks/"), "/")
	if rest == "" || strings.Contains(rest, "/") {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "expected /api/hooks/{directive}"})
		return
	}
	directiveID := rest

	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if !s.hookAuthOK(r, body) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "unauthorized"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	collected := map[string]bool{}
	for _, mode := range []collectors.Mode{collectors.ModeState, collectors.ModeEvents} {
		if s.engine.CollectUnitNow(ctx, directiveID, mode) {
			collected[string(mode)] = true
		}
	}
	if len(collected) == 0 {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"ok":    false,
			"error": "no collection units found for directive " + directiveID,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"directive": directiveID,
		"collected": collected,
	})
}

func (s *Server) hookAuthOK(r *http.Request, body []byte) bool {
	// Prefer the shared daemon bearer token.
	if s.authOK(r) {
		return true
	}
	secret := strings.TrimSpace(os.Getenv("DOCENT_HOOK_SECRET"))
	if secret == "" {
		secret = strings.TrimSpace(s.cfg.Token)
	}
	if secret == "" {
		return false
	}
	if got := strings.TrimSpace(r.Header.Get("X-Docent-Hook-Secret")); got != "" {
		return subtle.ConstantTimeCompare([]byte(got), []byte(secret)) == 1
	}
	// GitHub-style HMAC signature.
	sig := strings.TrimSpace(r.Header.Get("X-Hub-Signature-256"))
	if strings.HasPrefix(sig, "sha256=") {
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write(body)
		want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		return subtle.ConstantTimeCompare([]byte(sig), []byte(want)) == 1
	}
	return false
}
