package sessionmanager

import (
	"context"

	"github.com/KurtPreston/docent/libs/wmclient"
)

// WSMManager is a thin SessionManager over the local wsm window-manager REST
// service. wsm remains the option for reliable exact-window focus.
//
// It implements Lister but not DeepLinker: wsm has no clickable URI, so the
// frontend focuses via POST /focus (proxied to the wsm /focus endpoint).
type WSMManager struct {
	Client *wmclient.Client
	// Token is an optional bearer token for the wsm API. Reserved for when the
	// wsm client gains auth support; unused today.
	Token string
}

func (m *WSMManager) Provider() string { return "wsm" }

// List enumerates live windows reported by wsm.
func (m *WSMManager) List(ctx context.Context) ([]Session, error) {
	windows, err := m.Client.ListWindows(ctx)
	if err != nil {
		return nil, err
	}
	sessions := make([]Session, 0, len(windows))
	for _, w := range windows {
		leaf, host := wmclient.ParseCursorTitle(w.Title)
		if host == "" {
			host = w.Host
		}
		sessions = append(sessions, Session{
			ID:    w.ID,
			Title: w.Title,
			Leaf:  leaf,
			Host:  host,
			App:   w.App,
		})
	}
	return sessions, nil
}

// Open asks wsm to open a window on req.Path (optionally remote req.Host).
func (m *WSMManager) Open(ctx context.Context, req OpenReq) error {
	return m.Client.Open(ctx, wmclient.OpenRequest{
		Host: req.Host,
		Path: req.Path,
		Name: req.Name,
	})
}

// Focus asks wsm to focus an existing window by id or name.
func (m *WSMManager) Focus(ctx context.Context, req FocusReq) error {
	return m.Client.Focus(ctx, wmclient.FocusRequest{
		ID:   req.ID,
		Name: req.Name,
	})
}
