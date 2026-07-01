package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/kurt/slakkr-ai/apps/docent-wm-macos/macos"
	"github.com/kurt/slakkr-ai/libs/wmclient"
)

func main() {
	port := flag.Int("port", 39788, "listen port (127.0.0.1 only)")
	corsOrigin := flag.String("cors-origin", "*", "Access-Control-Allow-Origin")
	flag.Parse()

	mux := http.NewServeMux()
	srv := &server{cors: *corsOrigin}
	mux.HandleFunc("/health", srv.health)
	mux.HandleFunc("/windows", srv.windows)
	mux.HandleFunc("/open", srv.open)
	mux.HandleFunc("/focus", srv.focus)

	addr := fmt.Sprintf("127.0.0.1:%d", *port)
	log.Printf("docent-wm-macos serving on http://%s/", addr)
	if err := http.ListenAndServe(addr, corsMiddleware(mux, *corsOrigin)); err != nil {
		log.Fatal(err)
	}
}

type server struct {
	cors string
}

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *server) windows(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	titles, err := macos.ListWindowTitles()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	wins := make([]wmclient.Window, 0, len(titles))
	for i, title := range titles {
		leaf, host := wmclient.ParseCursorTitle(title)
		id := leaf
		if id == "" {
			id = fmt.Sprintf("win-%d", i)
		}
		wins = append(wins, wmclient.Window{
			ID:    id,
			Title: title,
			App:   macos.ProcessName,
			Host:  host,
		})
	}
	writeJSON(w, wmclient.WindowsResponse{Windows: wins})
}

func (s *server) open(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req wmclient.OpenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if req.Host == "" || req.Path == "" {
		http.Error(w, "host and path required", http.StatusBadRequest)
		return
	}
	name := req.Name
	if name == "" {
		name = leafName(req.Path)
	}
	uri := req.URI
	if uri == "" {
		uri = fmt.Sprintf("vscode-remote://ssh-remote+%s%s", req.Host, req.Path)
	}
	if err := macos.OpenWorkspace(uri, name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "action": "opened", "name": name})
}

func (s *server) focus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req wmclient.FocusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	target := req.Name
	if target == "" {
		target = req.ID
	}
	if target == "" {
		http.Error(w, "id or name required", http.StatusBadRequest)
		return
	}
	if err := macos.FocusWindow(target); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "action": "focused", "name": target})
}

func leafName(path string) string {
	path = strings.TrimRight(path, "/")
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func corsMiddleware(next http.Handler, origin string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
