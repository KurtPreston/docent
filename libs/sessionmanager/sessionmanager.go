// Package sessionmanager abstracts how docent lists and opens editor windows
// ("sessions") for a work item. It mirrors the AI-provider pattern in
// libs/ai: a global, config-selected provider chosen by a discriminator, with
// optional capability interfaces layered on top of a small core.
//
// The core interface is intentionally tiny (Open/Focus). Providers that can
// enumerate live windows implement Lister; providers that can produce a
// clickable link (so the frontend never hardcodes a URI scheme) implement
// DeepLinker. Capabilities are discovered via interface assertion, the same
// way libs/collectors distinguishes StateCollector from EventCollector.
package sessionmanager

import (
	"context"
	"errors"
	"strings"

	"github.com/KurtPreston/docent/libs/config/userdata"
	"github.com/KurtPreston/docent/libs/wmclient"
)

// ErrFocusUnsupported is returned by providers that cannot focus a specific
// window by id/name from the backend (e.g. Cursor, which is focused via a
// client-side deep link instead).
var ErrFocusUnsupported = errors.New("sessionmanager: focus by id/name is not supported by this provider")

// Session is one open editor window known to a provider.
type Session struct {
	// ID is a provider-specific window identifier (may be empty).
	ID string
	// Title is the raw window title as reported by the provider.
	Title string
	// Leaf is the workspace leaf (folder name) parsed from the title.
	Leaf string
	// Host is the ssh alias the window targets; empty for a local window.
	Host string
	// App is the editor application name (e.g. "Cursor").
	App string
}

// OpenReq asks a provider to open (or reveal) a window on Path, optionally on a
// remote ssh Host. Name is an optional label for the window.
type OpenReq struct {
	Path string
	Host string
	Name string
}

// FocusReq asks a provider to focus an already-open window.
type FocusReq struct {
	ID   string
	Name string
}

// SessionManager is the core capability every provider implements.
type SessionManager interface {
	// Provider returns the provider key (e.g. "cursor", "wsm").
	Provider() string
	// Open opens or reveals a window for req.
	Open(ctx context.Context, req OpenReq) error
	// Focus focuses an existing window; may return ErrFocusUnsupported.
	Focus(ctx context.Context, req FocusReq) error
}

// Lister is the optional capability of enumerating active sessions.
type Lister interface {
	List(ctx context.Context) ([]Session, error)
}

// DeepLinker is the optional capability of producing a clickable link that
// opens/focuses a window at path (on ssh host, when set). It returns "" when a
// link cannot be produced.
type DeepLinker interface {
	DeepLink(path, host string) string
}

// Select returns the SessionManager described by the open-trigger config, or
// nil when no provider is configured. A nil manager means "no open trigger":
// callers render no clickable open/focus links.
func Select(cfg userdata.OpenTriggerConfig) SessionManager {
	switch normalizeProvider(cfg.Provider) {
	case "cursor":
		cmd := strings.TrimSpace(cfg.Cursor.Command)
		if cmd == "" {
			cmd = "cursor"
		}
		return &CursorManager{Command: cmd, Host: strings.TrimSpace(cfg.Cursor.Host)}
	case "wsm":
		base := strings.TrimRight(strings.TrimSpace(cfg.WSM.BaseURL), "/")
		if base == "" {
			base = "http://127.0.0.1:39788"
		}
		return &WSMManager{Client: wmclient.New(base), Token: strings.TrimSpace(cfg.WSM.Token)}
	default:
		return nil
	}
}

func normalizeProvider(s string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), "_", "-"))
}
